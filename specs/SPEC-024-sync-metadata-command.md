---
spec_id: SPEC-024
title: Sync Metadata Command
status: draft
priority: P2
epic: "User Experience"
depends_on: [SPEC-008, SPEC-009, SPEC-010]
estimated_effort: M
cloudvoyager_ref: "src/commands/sync-metadata/"
---

# SPEC-024: Sync Metadata Command
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

CloudVoyager provides a dedicated `sync-metadata` command that re-synchronizes issue and hotspot metadata between SonarQube Server and SonarQube Cloud without re-extracting source code or re-uploading scanner reports. This command addresses a critical operational need: after an initial migration, metadata synchronization may be interrupted (network failure, rate limiting, process crash), or new issues may have been modified in SonarQube Server (status changes, comments added, tags updated). Rather than re-running the full `migrate` command -- which involves expensive extraction, protobuf building, and upload phases -- the `sync-metadata` command skips directly to the metadata synchronization phase.

In CloudVoyager's architecture, `sync-metadata` is implemented in `src/commands/sync-metadata/index.js` and delegates to `handleSyncMetadataAction` in the helpers subdirectory. The handler loads the same `migrate-config.json` configuration file as the `migrate` command, applies CLI flag overrides (`--skip-issue-metadata-sync`, `--skip-hotspot-metadata-sync`, `--skip-quality-profile-sync`), resolves performance configuration (including auto-tune), and then calls the same `migrateAll` pipeline function with `skipProjectConfig: true` -- effectively instructing the pipeline to bypass extraction, build, and upload phases and execute only the metadata sync stages.

The Go tool currently has no equivalent command. The `migrate` command always runs the full pipeline. Adding `sync-metadata` requires extracting the metadata sync logic from the migrate pipeline into a reusable component and exposing it as a standalone cobra command.

## Problem Statement

When a large-scale migration involves thousands of projects with millions of issues and hotspots, the metadata synchronization phase is often the longest-running and most failure-prone stage. Network interruptions, SonarCloud API rate limits, and transient errors can cause the sync to fail partway through. Currently, the only recovery path is to re-run the entire `migrate` command, which wastes hours re-extracting data and re-uploading scanner reports that have already been successfully processed. This creates a significant operational burden: a 1000-project migration that fails during issue sync at project 800 must re-do extraction and upload for all 1000 projects just to retry syncing the remaining 200.

Additionally, SonarQube Server is often still in active use during a phased migration. Issues may be triaged (status changes from Open to Confirmed/Resolved), comments may be added, and tags may be updated between migration batches. Without a lightweight re-sync command, these post-migration changes are lost.

## User Stories

- **As a** migration operator, **I want to** re-sync only issue and hotspot metadata after an interrupted migration, **so that** I do not waste time re-extracting and re-uploading data that was already successfully transferred.
- **As a** migration operator, **I want to** selectively skip issue sync, hotspot sync, or quality profile sync, **so that** I can target only the metadata types that need updating.
- **As a** migration operator, **I want to** control concurrency for the sync operation, **so that** I can balance throughput against SonarCloud API rate limits.
- **As a** migration operator, **I want to** use the same configuration file as the `migrate` command, **so that** I do not need to maintain separate configuration for re-sync operations.
- **As a** quality engineer, **I want to** re-sync quality profiles after making changes in SonarQube Server, **so that** rule configurations are kept in sync between Server and Cloud during a phased migration.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Implement `sync-metadata` as a new cobra command registered on the root command | Must |
| FR-2 | Accept `--config <path>` flag pointing to a `migrate-config.json` file | Must |
| FR-3 | Accept `--skip-issue-metadata-sync` flag to skip issue status, comment, tag, and assignment synchronization | Must |
| FR-4 | Accept `--skip-hotspot-metadata-sync` flag to skip hotspot status and review comment synchronization | Must |
| FR-5 | Accept `--skip-quality-profile-sync` flag to skip quality profile rule synchronization | Must |
| FR-6 | Accept `--concurrency N` flag to override parallel operation count | Must |
| FR-7 | Accept `--auto-tune` flag to auto-detect hardware and set optimal concurrency values | Should |
| FR-8 | Accept `--verbose` flag for debug-level logging | Must |
| FR-9 | Accept `--skip-all-branch-sync` flag to only sync metadata for main branches | Should |
| FR-10 | Sync issue statuses: map SQ Server statuses (OPEN, CONFIRMED, REOPENED, RESOLVED, CLOSED) to SC equivalents (OPEN, CONFIRMED, ACCEPTED, FIXED, FALSE_POSITIVE, WONTFIX) | Must |
| FR-11 | Sync issue comments: copy all comments from SQ Server issues to matched SC issues, preserving author attribution and timestamps | Must |
| FR-12 | Sync issue tags: apply SQ Server issue tags to matched SC issues | Must |
| FR-13 | Sync issue assignments: map SQ Server user logins to SC user identifiers and set assignees | Should |
| FR-14 | Sync hotspot statuses: map SQ Server hotspot statuses (TO_REVIEW, REVIEWED/SAFE, REVIEWED/FIXED) to SC equivalents | Must |
| FR-15 | Sync hotspot review comments: copy review comments from SQ Server hotspots to matched SC hotspots | Must |
| FR-16 | Match issues by composite key: rule key + component path + text range (line number) | Must |
| FR-17 | Match hotspots by composite key: rule key + component path + text range (line number) | Must |
| FR-18 | Generate a sync report at completion with per-project sync statistics | Must |
| FR-19 | Exit with code 0 on full success, code 1 if any project failed sync | Must |
| FR-20 | Do NOT re-extract source code, build protobuf messages, or upload scanner reports | Must |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Sync 10,000 issues per project within 60 seconds at default concurrency | < 60s / 10K issues |
| NFR-2 | Memory usage must not exceed 500 MB for a 1000-project sync | < 500 MB |
| NFR-3 | Must gracefully handle SonarCloud 429 rate limit responses with exponential backoff | Retry with backoff |
| NFR-4 | Must be idempotent: running sync-metadata twice produces the same result | Idempotent |
| NFR-5 | Must not create duplicate comments on re-run (detect existing comments by content hash) | No duplicates |

## Technical Design

### Architecture

The `sync-metadata` command reuses the existing migration infrastructure but skips the data transfer phases:

```
cmd/
├── sync_metadata.go     # NEW: cobra command definition

internal/
├── syncmetadata/        # NEW: sync metadata orchestrator
│   ├── sync.go          # RunSyncMetadata entry point
│   ├── config.go        # SyncMetadataConfig type
│   ├── issue_sync.go    # Issue matching + status/comment/tag sync
│   ├── hotspot_sync.go  # Hotspot matching + status/comment sync
│   ├── profile_sync.go  # Quality profile rule sync
│   ├── matcher.go       # Composite key matching algorithm
│   └── report.go        # Sync report generation
```

The `syncmetadata` package imports from existing packages:
- `lib/sq-api-go` -- SonarQube Server and SonarCloud API clients.
- `internal/migrate` -- Reuses config loading (`LoadMigrateConfigFile`), organization resolution.
- `internal/report` -- Reuses report generation infrastructure.

### Key Data Structures

```go
// SyncMetadataConfig holds all configuration for a sync-metadata run.
type SyncMetadataConfig struct {
    ConfigPath              string
    SkipIssueMetadataSync   bool
    SkipHotspotMetadataSync bool
    SkipQualityProfileSync  bool
    SkipAllBranchSync       bool
    Concurrency             int
    AutoTune                bool
    Verbose                 bool
    
    // Loaded from config file:
    SonarQube   SonarQubeConfig   // URL + token
    SonarCloud  SonarCloudConfig  // Organizations list + enterprise config
    Transfer    TransferConfig    // Mode, batch size, branch settings
    RateLimit   RateLimitConfig   // Retry settings
    Performance PerformanceConfig // Concurrency tuning
}

// IssueMatch represents a matched pair of issues between SQ Server and SC.
type IssueMatch struct {
    ServerIssue  ServerIssue  // Issue from SQ Server
    CloudIssue   CloudIssue   // Matched issue in SC
    MatchKey     string       // Composite key used for matching
    MatchScore   float64      // Confidence score (1.0 = exact match)
}

// CompositeKey is the matching key for issues and hotspots.
// The default matching uses 3 fields (RuleKey + ComponentPath + StartLine),
// consistent with SPEC-021's matching logic. StartOffset is used as an
// optional 4th-field tiebreaker when collisions are detected (multiple
// issues with same rule on same line).
type CompositeKey struct {
    RuleKey       string // e.g., "java:S1234"
    ComponentPath string // e.g., "src/main/java/com/example/Foo.java"
    StartLine     int    // Text range start line (primary match)
    StartOffset   int    // Text range start offset (tiebreaker for same-line disambiguation)
}

// SyncResult tracks the outcome of synchronization for a single project.
type SyncResult struct {
    ProjectKey    string
    Status        string // "success", "failed", "skipped"
    IssueSync     SyncStats
    HotspotSync   SyncStats
    ProfileSync   ProfileSyncStats
    Duration      time.Duration
    Errors        []error
}

// SyncStats should be defined in a shared package (e.g., internal/common/)
// and reused across SPEC-022 and SPEC-024. The struct below and the
// identically-named struct in SPEC-022 must be consolidated into a single
// definition to avoid duplication.
type SyncStats struct {
    Total        int // Total items in SQ Server
    Matched      int // Successfully matched to SC counterpart
    Transitioned int // Status transitions applied
    Commented    int // Comments synced
    Tagged       int // Tags applied
    Assigned     int // Assignments set
    Failed       int // Operations that failed
    Skipped      int // Skipped (no match, already synced, etc.)
}

type ProfileSyncStats struct {
    ProfilesChecked int
    RulesActivated  int
    RulesDeactivated int
    RulesUpdated    int
    Failed          int
}
```

### Key Algorithms

**Issue Matching by Composite Key**:

```
algorithm matchIssues(serverIssues, cloudIssues):
    // Build index of cloud issues by composite key
    cloudIndex = map[CompositeKey][]CloudIssue
    for each ci in cloudIssues:
        key = CompositeKey{
            RuleKey:       ci.rule,
            ComponentPath: ci.component (relative path),
            StartLine:     ci.textRange.startLine,
            StartOffset:   ci.textRange.startOffset
        }
        cloudIndex[key].append(ci)
    
    matches = []
    unmatched = []
    
    for each si in serverIssues:
        key = CompositeKey{
            RuleKey:       si.rule,
            ComponentPath: si.component (relative path, strip project prefix),
            StartLine:     si.textRange.startLine,
            StartOffset:   si.textRange.startOffset
        }
        
        candidates = cloudIndex[key]
        if len(candidates) == 0:
            // Try fuzzy match: same rule + component, within +/- 3 lines
            candidates = fuzzySearch(cloudIndex, key, lineWindow=3)
        
        if len(candidates) == 1:
            matches.append(IssueMatch{si, candidates[0], key, 1.0})
            remove candidates[0] from cloudIndex
        else if len(candidates) > 1:
            // Disambiguate by message similarity or creation date
            best = selectBestCandidate(si, candidates)
            matches.append(IssueMatch{si, best, key, 0.8})
            remove best from cloudIndex
        else:
            unmatched.append(si)
    
    return matches, unmatched
```

**Issue Status Transition Mapping**:

```
algorithm mapIssueStatus(serverStatus, serverResolution):
    mapping:
        (OPEN, null)       -> Do nothing (SC default is OPEN)
        (CONFIRMED, null)  -> POST /api/issues/do_transition {transition: "confirm"}
        (REOPENED, null)    -> POST /api/issues/do_transition {transition: "reopen"}
        (RESOLVED, FIXED)   -> POST /api/issues/do_transition {transition: "resolve"} -- but check: SC may auto-resolve on next analysis
        (RESOLVED, FALSE-POSITIVE) -> POST /api/issues/do_transition {transition: "falsepositive"}
        (RESOLVED, WONTFIX) -> POST /api/issues/do_transition {transition: "wontfix"}
        (CLOSED, *)         -> Skip (closed issues may not exist in SC)
    
    // Apply transition only if the SC issue is not already in the target status
    if cloudIssue.status == targetStatus:
        return SKIPPED
    
    return applyTransition(cloudIssue.key, transition)
```

**Comment Deduplication**:

```
algorithm syncComments(serverIssue, cloudIssue):
    existingHashes = set()
    for each comment in cloudIssue.comments:
        hash = sha256(comment.markdown)
        existingHashes.add(hash)
    
    for each comment in serverIssue.comments:
        hash = sha256(comment.markdown)
        if hash in existingHashes:
            continue  // Already synced, skip
        
        // Prefix with attribution since SC API does not support setting author
        body = fmt.Sprintf("[Migrated from SQ Server | %s | %s]\n\n%s",
            comment.login, comment.createdAt, comment.markdown)
        
        POST /api/issues/add_comment {issue: cloudIssue.key, text: body}
```

**Hotspot Status Mapping**:

```
algorithm mapHotspotStatus(serverStatus, serverResolution):
    mapping:
        (TO_REVIEW, null)     -> Do nothing (SC default)
        (REVIEWED, SAFE)      -> POST /api/hotspots/change_status {hotspot: key, status: "REVIEWED", resolution: "SAFE"}
        (REVIEWED, FIXED)     -> POST /api/hotspots/change_status {hotspot: key, status: "REVIEWED", resolution: "FIXED"}
        (REVIEWED, ACKNOWLEDGED) -> POST /api/hotspots/change_status {hotspot: key, status: "REVIEWED", resolution: "ACKNOWLEDGED"}
```

**Per-Project Sync Orchestration**:

```
algorithm syncProject(projectKey, config, sqClient, scClient):
    result = SyncResult{ProjectKey: projectKey}
    
    if not config.SkipIssueMetadataSync:
        // Fetch all issues from both systems
        serverIssues = sqClient.SearchIssues(projectKey, branch=mainBranch)
        cloudIssues = scClient.SearchIssues(projectKey, branch=mainBranch)
        
        matches, unmatched = matchIssues(serverIssues, cloudIssues)
        
        // Process matches with concurrency pool
        pool = newWorkerPool(config.Performance.IssueSync.Concurrency)
        for each match in matches:
            pool.submit(() => {
                syncIssueStatus(match)
                syncIssueComments(match)
                syncIssueTags(match)
                syncIssueAssignment(match)
            })
        pool.wait()
        
        result.IssueSync = computeStats(matches, unmatched)
    
    if not config.SkipHotspotMetadataSync:
        serverHotspots = sqClient.SearchHotspots(projectKey, branch=mainBranch)
        cloudHotspots = scClient.SearchHotspots(projectKey, branch=mainBranch)
        
        matches, unmatched = matchHotspots(serverHotspots, cloudHotspots)
        
        pool = newWorkerPool(config.Performance.HotspotSync.Concurrency)
        for each match in matches:
            pool.submit(() => {
                syncHotspotStatus(match)
                syncHotspotComments(match)
            })
        pool.wait()
        
        result.HotspotSync = computeStats(matches, unmatched)
    
    if not config.SkipAllBranchSync:
        branches = sqClient.ListBranches(projectKey)
        for each branch in branches (excluding main):
            syncBranch(projectKey, branch, config, sqClient, scClient)
    
    return result
```

### Data Flow

1. User invokes `sonar-migration-tool sync-metadata --config migrate-config.json`.
2. Command parses flags and loads configuration file (reuses `migrate.LoadMigrateConfigFile`).
3. CLI flag overrides are applied (skip flags, concurrency, verbose).
4. Performance configuration is resolved (auto-tune if requested).
5. Structured logging is initialized with per-run log directory.
6. Connection to SQ Server and SC is established and validated.
7. Organization mappings are resolved from configuration.
8. For each organization:
   a. For each project in the organization:
      - Fetch issues from SQ Server (`/api/issues/search?componentKeys={key}&ps=500`).
      - Fetch issues from SC (`/api/issues/search?componentKeys={key}&ps=500`).
      - Match issues by composite key.
      - Sync statuses, comments, tags, assignments for matched issues.
      - Repeat for hotspots using `/api/hotspots/search`.
9. Sync report is generated with per-project statistics.
10. Exit code is set based on overall success/failure.

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/issues/search` | GET | Fetch issues from SQ Server and SC |
| `/api/issues/do_transition` | POST | Transition issue status in SC |
| `/api/issues/add_comment` | POST | Add comment to issue in SC |
| `/api/issues/set_tags` | POST | Set tags on issue in SC |
| `/api/issues/assign` | POST | Assign issue in SC |
| `/api/hotspots/search` | GET | Fetch hotspots from SQ Server and SC |
| `/api/hotspots/change_status` | POST | Change hotspot status in SC |
| `/api/hotspots/add_comment` | POST | Add comment to hotspot in SC (SQ 10.4+) |
| `/api/qualityprofiles/search` | GET | Fetch quality profiles for profile sync |
| `/api/rules/search` | GET | Fetch active rules per profile for profile sync |
| `/api/qualityprofiles/activate_rule` | POST | Activate a rule in SC profile |
| `/api/qualityprofiles/deactivate_rule` | POST | Deactivate a rule in SC profile |
| `/api/project_branches/list` | GET | List branches for multi-branch sync |
| `/api/system/status` | GET | Validate connectivity on startup |

## Acceptance Criteria

- [ ] AC-1: `sonar-migration-tool sync-metadata --config migrate-config.json` executes successfully and syncs issue metadata without re-uploading scanner reports.
- [ ] AC-2: `--skip-issue-metadata-sync` flag causes the command to skip all issue synchronization.
- [ ] AC-3: `--skip-hotspot-metadata-sync` flag causes the command to skip all hotspot synchronization.
- [ ] AC-4: `--skip-quality-profile-sync` flag causes the command to skip quality profile synchronization.
- [ ] AC-5: Issues are matched by composite key (rule + component + line) with at least 95% match rate on a test dataset.
- [ ] AC-6: Issue status transitions are correctly mapped from SQ Server to SC (CONFIRMED, FALSE_POSITIVE, WONTFIX tested).
- [ ] AC-7: Comments are synced with attribution prefix and no duplicates on re-run.
- [ ] AC-8: Hotspot statuses (TO_REVIEW, REVIEWED/SAFE, REVIEWED/FIXED) are correctly transitioned.
- [ ] AC-9: `--concurrency N` flag controls the number of parallel sync operations.
- [ ] AC-10: Sync report is generated with per-project matched/transitioned/failed/skipped counts.
- [ ] AC-11: Command exits with code 0 on full success, code 1 if any project failed.
- [ ] AC-12: Running the command twice on the same dataset is idempotent (no duplicate comments, no incorrect status transitions).
- [ ] AC-13: The command handles SonarCloud 429 rate limit responses with exponential backoff without crashing.

## CloudVoyager Reference

| Area | Path |
|------|------|
| Command registration | `src/commands/sync-metadata/index.js` |
| Action handler | `src/commands/sync-metadata/helpers/handle-sync-metadata-action.js` |
| Config loader | `src/shared/config/loader-migrate.js` |
| Config schema | `src/shared/config/schema-migrate.js` |
| Performance config | `src/shared/config/schema-shared/helpers/performance-schema.js` |
| Rate limit config | `src/shared/config/schema-shared/helpers/rate-limit-schema.js` |
| Issue sync pipeline | `src/pipelines/migrate/stages/sync-issue-metadata/` |
| Hotspot sync pipeline | `src/pipelines/migrate/stages/sync-hotspot-metadata/` |
| Profile sync pipeline | `src/pipelines/migrate/stages/sync-quality-profiles/` |
| Version router | `src/version-router.js` |

## Known Limitations

- SonarCloud's issue transition API does not support all SQ Server status transitions. For example, transitioning directly from OPEN to RESOLVED/FIXED may require an intermediate CONFIRMED state depending on the SC instance's workflow configuration. The sync must handle multi-step transitions.
- Comment attribution is approximate: the SC API does not allow setting the author of a comment to a different user. Comments are prefixed with the original author's login, but the SC comment will show as authored by the migration token's user.
- Issue assignment requires that user login mappings exist between SQ Server and SC. If a SQ Server user does not have a corresponding SC account, the assignment is skipped and logged as a warning.
- Hotspot review comments via API (`/api/hotspots/add_comment`) are only available in SonarQube 10.4+ and SonarCloud. For older SQ Server versions, hotspot comments may not be extractable.
- The composite key matching algorithm may produce false matches for generic rules (e.g., `common-java:DuplicatedBlocks`) that trigger on many files at similar line numbers. The fuzzy matching window (+/- 3 lines) mitigates but does not eliminate this.

## Open Questions

- Should the sync-metadata command support a `--dry-run` mode that reports what changes would be made without applying them?
- Should the command support incremental sync (only sync issues modified after a given timestamp) to reduce API calls on subsequent runs?
- Should the command attempt to sync issue changelog entries (not just current status) to preserve the full audit trail?
- How should the command handle issues that exist in SC but not in SQ Server (i.e., issues found by SC's own analysis that do not have a SQ Server counterpart)?
- Should quality profile sync support parameter-level updates (changing a rule's parameter value) or only activation/deactivation?
