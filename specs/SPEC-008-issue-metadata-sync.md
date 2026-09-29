---
spec_id: SPEC-008
title: "Issue Metadata Synchronization"
status: draft
priority: P0
epic: "Scale & Reliability"
depends_on: [SPEC-002, SPEC-006, SPEC-010]
estimated_effort: XL
cloudvoyager_ref: "src/pipelines/*/transfer/sync-issues/, src/shared/utils/issue-sync/, src/shared/utils/parallel-sync/"
---

# SPEC-008: Issue Metadata Synchronization
<!-- updated: 2026-05-26_01:00:00 -->

## Overview
<!-- updated: 2026-05-26_01:00:00 -->

After issues are uploaded to SonarQube Cloud via scanner reports, they exist with default metadata: `OPEN` status, no comments, no tags, and no assignments. The issue metadata synchronization phase is responsible for matching each SonarQube Cloud issue back to its SonarQube Server counterpart and replicating the original status, comments, tags, and user assignments. This is the most complex and API-intensive operation in the entire migration pipeline.

The matching algorithm uses a composite key of `rule + component + line` to pair SonarQube Server issues with their SonarQube Cloud counterparts. Once matched, the system applies status transitions via the `/api/issues/do_transition` endpoint, copies comments via `/api/issues/add_comment`, sets tags via `/api/issues/set_tags`, and assigns users via `/api/issues/assign`. Each of these operations must account for API rate limits, transient failures, and the eventual consistency of SonarQube Cloud's Elasticsearch indexing.

A critical pre-filtering step examines each SonarQube Server issue's changelog to determine whether it has any "manual changes" (human-authored status transitions, comments, custom tags, or assignments). Issues that have never been touched by a human since creation are skipped, dramatically reducing the number of API calls needed. For a typical enterprise project, 60-80% of issues have no manual changes and can be safely skipped.

## Problem Statement
<!-- updated: 2026-05-26_01:00:00 -->

After scanner report upload, all migrated issues in SonarQube Cloud appear as `OPEN` with no metadata. Users who have invested significant effort in triaging, commenting on, and assigning issues in SonarQube Server expect that metadata to be preserved in SonarQube Cloud. Without metadata synchronization:

- Issues marked as `FALSE_POSITIVE` or `WONTFIX` in SonarQube Server reappear as `OPEN` in SonarQube Cloud, requiring re-triage.
- Comments containing investigation notes, fix plans, and audit evidence are lost.
- Custom tags used for categorization and filtering are not transferred.
- Issue assignments are lost, breaking team workflows and accountability.

This represents the difference between a migration that requires hours of post-migration cleanup and one that preserves the full issue lifecycle history.

## User Stories
<!-- updated: 2026-05-26_01:00:00 -->

- **As a** development team lead, **I want** issue statuses (CONFIRMED, FALSE_POSITIVE, WONTFIX, etc.) to be preserved during migration, **so that** my team doesn't have to re-triage thousands of issues.
- **As a** security engineer, **I want** all issue comments and investigation notes to be migrated, **so that** audit trails are preserved for compliance.
- **As a** project manager, **I want** issue assignments to be mapped to the correct SonarQube Cloud users, **so that** team accountability is maintained after migration.
- **As a** migration operator, **I want** the tool to skip issues with no manual changes (never triaged), **so that** migration time is minimized for large projects.
- **As a** migration operator, **I want** detailed statistics on matched, synced, skipped, and failed issues, **so that** I can verify migration completeness.

## Requirements
<!-- updated: 2026-05-26_01:00:00 -->

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Wait for SonarQube Cloud indexing before attempting issue sync (exponential backoff, max 10 retries, initial delay 10s, max delay 60s) | Must |
| FR-2 | Fetch all SonarQube Server issues via search slicer (SPEC-006) | Must |
| FR-3 | Fetch all SonarQube Cloud issues for the same project via paginated search | Must |
| FR-4 | Match SQ Server issues to SC issues by composite key: `ruleKey + componentPath + line` | Must |
| FR-5 | Pre-filter: fetch changelogs for all SQ Server issues, skip those with no manual changes | Must |
| FR-6 | Apply status transitions via `/api/issues/do_transition` according to the transition mapping table | Must |
| FR-7 | Copy all comments from SQ Server to SC via `/api/issues/add_comment`, prepending `[Migrated from SonarQube Server]` | Must |
| FR-8 | Preserve original comment author in text: `[Migrated from SonarQube Server - @author_login]` | Must |
| FR-8a | If sync is re-run, comments should be deduplicated by checking if a comment with the `[Migrated from SonarQube Server]` prefix already exists before adding | Must |
| FR-8b | Comments must be added in chronological order to preserve conversation flow | Must |
| FR-9 | Copy custom tags from SQ Server issues via `/api/issues/set_tags` | Must |
| FR-10 | Map SQ Server assignees to SC users via `users.csv` (SPEC-010) and assign via `/api/issues/assign` | Must |
| FR-11 | For >= 500 matched pairs, use a worker pool with configurable concurrency (default: 20 workers) | Must |
| FR-12 | For < 500 matched pairs, use a simpler concurrent map | Must |
| FR-13 | Exponential backoff on 429/transient errors: 3 retries with 1s/2s/4s delays | Must |
| FR-14 | Round-robin partition matched pairs across workers for even distribution | Should |
| FR-15 | Report sync statistics: total, matched, unmatched, synced, skipped (no manual changes), failed | Must |
| FR-16 | Log `IN_SANDBOX` status as a warning with no transition attempt | Must |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Sync throughput for large projects (10K+ matched pairs) | >= 50 transitions/second |
| NFR-2 | Memory: do not load all changelogs into memory simultaneously | Stream processing |
| NFR-3 | API error rate tolerance: continue syncing other issues if one fails | >= 99% completion |
| NFR-4 | SC indexing wait: detect readiness within 2 minutes of analysis completion | 10s initial + exponential backoff |
| NFR-5 | Pre-filter efficiency: reduce API calls by 60-80% for typical enterprise projects | Measured |

## Technical Design
<!-- updated: 2026-05-26_01:00:00 -->

### Architecture

The issue metadata sync is implemented as a new package `go/internal/issuesync/` with the following structure:

```
go/internal/issuesync/
    sync.go              # SyncIssues() orchestrator
    matcher.go           # matchIssues() composite key matching
    prefilter.go         # applyPreFilter() changelog-based filtering
    transitions.go       # applyTransition() status mapping and API calls
    comments.go          # syncComments() comment migration
    tags.go              # syncTags() tag migration
    assignments.go       # syncAssignments() user mapping + assignment
    indexing.go          # waitForSCIndexing() backoff loop
    worker.go            # workerPool for high-concurrency sync
    types.go             # MatchedPair, SyncStats, SyncConfig
    sync_test.go         # Unit tests
```

This package is called by a new migrate task registered in `go/internal/migrate/tasks_issues.go`.

### Key Algorithms

#### Issue Matching (Composite Key)

```go
// compositeKey builds a unique matching key for an issue.
// Uses the project-relative component path (not the full key) because
// SQ Server and SC use different component key prefixes.
func compositeKey(ruleKey, componentPath string, line int) string {
    return fmt.Sprintf("%s:%s:%d", ruleKey, componentPath, line)
}

type MatchedPair struct {
    SQIssue   types.Issue  // SonarQube Server issue
    SCIssue   types.Issue  // SonarQube Cloud issue
}

func matchIssues(sqIssues, scIssues []types.Issue, projectKey string) ([]MatchedPair, []types.Issue, []types.Issue) {
    // Build SC lookup map
    scByKey := make(map[string]types.Issue, len(scIssues))
    for _, sc := range scIssues {
        path := extractComponentPath(sc.Component, projectKey)
        key := compositeKey(sc.Rule, path, sc.Line)
        scByKey[key] = sc
    }

    var matched []MatchedPair
    var unmatchedSQ []types.Issue

    for _, sq := range sqIssues {
        path := extractComponentPath(sq.Component, projectKey)
        key := compositeKey(sq.Rule, path, sq.Line)
        if sc, ok := scByKey[key]; ok {
            matched = append(matched, MatchedPair{SQIssue: sq, SCIssue: sc})
            delete(scByKey, key) // prevent double-matching
        } else {
            unmatchedSQ = append(unmatchedSQ, sq)
        }
    }

    // Remaining SC issues are unmatched
    unmatchedSC := make([]types.Issue, 0, len(scByKey))
    for _, sc := range scByKey {
        unmatchedSC = append(unmatchedSC, sc)
    }

    return matched, unmatchedSQ, unmatchedSC
}

// extractComponentPath strips the project key prefix from a component key.
// "my-project:src/main/Foo.java" -> "src/main/Foo.java"
func extractComponentPath(component, projectKey string) string {
    prefix := projectKey + ":"
    if strings.HasPrefix(component, prefix) {
        return component[len(prefix):]
    }
    return component
}
```

#### Pre-Filter (Manual Change Detection)

```go
// HasManualChanges determines whether an issue has human-authored changes
// that need synchronization. Returns true if any of:
// 1. Changelog contains entries authored by a human user
// 2. Issue has non-migration comments
// 3. Issue has custom tags
// 4. Issue has a manual assignee
// 5. UpdateDate differs from CreationDate (catch-all safety net)
func HasManualChanges(issue types.Issue, changelog []ChangelogEntry) bool {
    if hasHumanChangelog(changelog) { return true }
    if hasManualComments(issue)     { return true }
    if hasCustomTags(issue)         { return true }
    if hasAssignee(issue)           { return true }
    if wasUpdatedAfterCreation(issue) { return true }
    return false
}

type ChangelogEntry struct {
    User    string             `json:"user"`
    Date    string             `json:"creationDate"`
    Diffs   []ChangelogDiff    `json:"diffs"`
}

type ChangelogDiff struct {
    Key      string `json:"key"`
    NewValue string `json:"newValue"`
    OldValue string `json:"oldValue"`
}

const migratedCommentPrefix = "[Migrated from SonarQube Server]"

func hasHumanChangelog(changelog []ChangelogEntry) bool {
    for _, entry := range changelog {
        if entry.User != "" {
            return true
        }
    }
    return false
}

func hasManualComments(issue types.Issue) bool {
    for _, c := range issue.Comments {
        if c.Markdown != "" && !strings.HasPrefix(c.Markdown, migratedCommentPrefix) {
            return true
        }
    }
    return false
}

func hasCustomTags(issue types.Issue) bool {
    return len(issue.Tags) > 0
}

func hasAssignee(issue types.Issue) bool {
    return issue.Assignee != ""
}

func wasUpdatedAfterCreation(issue types.Issue) bool {
    return issue.UpdateDate != "" && issue.CreationDate != "" && issue.UpdateDate != issue.CreationDate
}
```

#### Status Transition Mapping

```go
// transitionMap maps SQ Server statuses to the SC API transition name.
// SC issues start in OPEN state. Some target states require multi-step
// transitions from OPEN (see multiStepTransitions below).
var transitionMap = map[string]string{
    "OPEN":            "",              // skip — already default state
    "CONFIRMED":       "confirm",
    "REOPENED":        "",              // skip — maps to OPEN, already default
    "RESOLVED":        "resolve",       // requires resolution param (FIXED)
    "CLOSED":          "resolve",       // requires resolution param (FIXED)
    "FALSE_POSITIVE":  "falsepositive",
    "ACCEPTED":        "accept",
    "WONTFIX":         "wontfix",
}

// unsyncableStatuses are SQ Server statuses with no SC equivalent.
var unsyncableStatuses = map[string]bool{
    "IN_SANDBOX": true,
}

// NOTE on multi-step transitions:
// Some target states require multi-step transitions from the default OPEN state.
// For example, transitioning to FALSE_POSITIVE may require:
//   OPEN → confirm → falsepositive (two API calls)
// The implementation must check available transitions via `/api/issues/transitions`
// before applying, and chain transitions as needed.

// NOTE on resolve transition:
// The resolve transition requires specifying resolution (FIXED).
// Use `/api/issues/do_transition` with `transition=resolve`.

func applyTransition(ctx context.Context, client *cloud.Client, scIssueKey, sqStatus string) error {
    if unsyncableStatuses[sqStatus] {
        slog.Warn("Unsyncable status, skipping transition",
            "issueKey", scIssueKey, "sqStatus", sqStatus)
        return nil
    }

    // OPEN and REOPENED map to the default SC state; no transition needed
    if sqStatus == "OPEN" || sqStatus == "REOPENED" {
        return nil
    }

    transition, ok := transitionMap[sqStatus]
    if !ok {
        return fmt.Errorf("unknown SQ status %q for issue %s", sqStatus, scIssueKey)
    }

    if transition == "" {
        return nil // no-op statuses
    }

    // Check available transitions and chain if needed
    available, err := client.Issues().Transitions(ctx, scIssueKey)
    if err != nil {
        return fmt.Errorf("fetching transitions for %s: %w", scIssueKey, err)
    }

    if !containsTransition(available, transition) {
        // May need intermediate transition (e.g., confirm before falsepositive)
        slog.Warn("Direct transition not available, attempting multi-step",
            "issueKey", scIssueKey, "target", transition, "available", available)
    }

    return client.Issues().DoTransition(ctx, scIssueKey, transition)
}
```

#### SC Indexing Wait

```go
func waitForSCIndexing(ctx context.Context, fetchFn func() ([]types.Issue, error), sqCount int, opts WaitOpts) ([]types.Issue, error) {
    if sqCount == 0 {
        return nil, nil
    }

    delay := opts.InitialDelay  // default 10s
    maxDelay := opts.MaxDelay   // default 60s
    maxRetries := opts.MaxRetries // default 10

    for attempt := 1; attempt <= maxRetries; attempt++ {
        if attempt > 1 {
            slog.Warn("SC issues not yet indexed, retrying",
                "attempt", attempt, "maxRetries", maxRetries,
                "delay", delay)
            select {
            case <-ctx.Done():
                return nil, ctx.Err()
            case <-time.After(delay):
            }
            delay = min(delay*2, maxDelay)
        }

        items, err := fetchFn()
        if err != nil {
            slog.Warn("SC fetch error during indexing wait",
                "attempt", attempt, "err", err)
            continue
        }
        if len(items) > 0 {
            if attempt > 1 {
                slog.Info("SC issues now available", "attempt", attempt)
            }
            return items, nil
        }
    }

    slog.Warn("SC issues still empty after max retries, proceeding with 0 matches",
        "maxRetries", maxRetries)
    return nil, nil
}
```

#### Worker Pool for High-Concurrency Sync

```go
type SyncConfig struct {
    Workers       int  // default 20
    Concurrency   int  // per-worker concurrency, default 5 (total = Workers * Concurrency)
    MaxRetries    int  // default 3
    RetryBackoff  []time.Duration // default: 1s, 2s, 4s
}

func syncWithWorkerPool(ctx context.Context, pairs []MatchedPair, cfg SyncConfig, syncFn func(context.Context, MatchedPair) error) (*SyncStats, error) {
    // Round-robin partition pairs across workers
    partitions := make([][]MatchedPair, cfg.Workers)
    for i, pair := range pairs {
        partitions[i%cfg.Workers] = append(partitions[i%cfg.Workers], pair)
    }

    stats := &SyncStats{}
    var mu sync.Mutex
    g, gctx := errgroup.WithContext(ctx)

    for _, partition := range partitions {
        partition := partition
        g.Go(func() error {
            sem := make(chan struct{}, cfg.Concurrency)
            var wg sync.WaitGroup

            for _, pair := range partition {
                pair := pair
                sem <- struct{}{}
                wg.Add(1)

                go func() {
                    defer func() { <-sem; wg.Done() }()
                    err := syncFn(gctx, pair)
                    mu.Lock()
                    if err != nil {
                        stats.Failed++
                    } else {
                        stats.Synced++
                    }
                    mu.Unlock()
                }()
            }
            wg.Wait()
            return nil
        })
    }

    return stats, g.Wait()
}
```

### Race Condition Analysis

- Issue matching map is built once before sync begins -- no concurrent writes
- Per-issue sync operations (transition, comment, tag, assign) are independent -- no shared mutable state between issues
- Worker pool uses channels for work distribution -- no shared slice iteration
- SC API calls are stateless -- concurrent transitions on different issues cannot conflict
- Comment deduplication: check for existing `[Migrated from SonarQube Server]` prefix before adding
- Comments must be added in chronological order to preserve conversation flow

### Data Flow

```
1. Scanner report uploaded to SC (SPEC-007 or single-analysis)
2. Wait for SC indexing: poll SC /api/issues/search until results > 0
3. Fetch SQ Server issues (via search slicer, SPEC-006)
4. Fetch SC issues (standard pagination)
5. Match SQ <-> SC issues by composite key (rule + component path + line)
6. Pre-filter: fetch SQ changelogs concurrently (10 concurrent)
   a. Filter issues with hasManualChanges()
   b. Log filtered count
7. For each matched pair with manual changes:
   a. Apply status transition (if SQ status != OPEN)
   b. Copy comments (prepend migration prefix + author)
   c. Set custom tags
   d. Assign to mapped SC user (via users.csv)
8. Report statistics: total, matched, unmatched, pre-filtered, synced, failed
```

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/issues/search` | GET | Fetch SQ Server and SC issues |
| `/api/issues/changelog` | GET | Fetch issue changelogs for pre-filtering (SQ Server) |
| `/api/issues/do_transition` | POST | Apply status transition on SC issue |
| `/api/issues/add_comment` | POST | Add migrated comment to SC issue |
| `/api/issues/set_tags` | POST | Set tags on SC issue |
| `/api/issues/assign` | POST | Assign SC issue to a user |

### Go Type Extensions Required

The existing `types.Issue` struct needs additional fields for metadata sync:

```go
// Additional fields needed on types.Issue
type Issue struct {
    // ... existing fields ...
    Line        int        `json:"line"`
    Assignee    string     `json:"assignee"`
    Comments    []Comment  `json:"comments"`
    Transitions []string   `json:"transitions"` // available transitions
}

type Comment struct {
    Key       string `json:"key"`
    Login     string `json:"login"`
    Markdown  string `json:"markdown"`
    HtmlText  string `json:"htmlText"`
    CreatedAt string `json:"createdAt"`
}
```

Additional API methods needed on the Cloud client:

```go
// cloud/issues.go - new methods
func (i *IssuesClient) DoTransition(ctx context.Context, issueKey, transition string) error
func (i *IssuesClient) AddComment(ctx context.Context, issueKey, text string) error
func (i *IssuesClient) SetTags(ctx context.Context, issueKey string, tags []string) error
func (i *IssuesClient) Assign(ctx context.Context, issueKey, assignee string) error
```

Additional API methods needed on the Server client:

```go
// server/issues.go - new methods
func (i *IssuesClient) Changelog(ctx context.Context, issueKey string) ([]ChangelogEntry, error)
func (i *IssuesClient) SearchWithDateWindow(ctx context.Context, projectKey string, after, before time.Time) *sqapi.Paginator[types.Issue]
```

## Acceptance Criteria
<!-- updated: 2026-05-26_01:00:00 -->

- [ ] AC-1: After sync, SC issues have the same status as their SQ Server counterparts (for all syncable statuses).
- [ ] AC-2: Comments are copied with `[Migrated from SonarQube Server - @author]` prefix.
- [ ] AC-3: Custom tags are copied verbatim from SQ Server to SC.
- [ ] AC-4: Issue assignments use the `users.csv` to map SQ logins to SC logins.
- [ ] AC-5: Unmapped users result in unassigned issues with a warning log.
- [ ] AC-6: Pre-filter skips issues with no manual changes; skipped count is logged.
- [ ] AC-7: `IN_SANDBOX` status is logged as a warning and not synced.
- [ ] AC-8: SC indexing wait retries up to 10 times with exponential backoff before proceeding.
- [ ] AC-9: For >= 500 matched pairs, worker pool achieves >= 50 transitions/second throughput.
- [ ] AC-10: Sync statistics are reported: total SQ issues, matched, unmatched, pre-filtered, synced (status/comments/tags/assignments), failed.
- [ ] AC-11: Transient errors (429, 5xx) are retried with 1s/2s/4s backoff.
- [ ] AC-12: Unit tests cover: composite key matching, pre-filter logic, transition mapping, comment formatting, worker pool distribution.
- [ ] AC-13: Integration test validates end-to-end sync flow with mock SQ Server and SC APIs.

## CloudVoyager Reference
<!-- updated: 2026-05-26_01:00:00 -->

| Area | Path |
|------|------|
| SC indexing wait | `src/shared/utils/issue-sync/wait-for-sc-indexing.js` |
| Pre-filter logic | `src/shared/utils/issue-sync/apply-pre-filter.js` |
| Manual change detection | `src/shared/utils/issue-sync/has-manual-changes.js` |
| Changelog fetching | `src/shared/utils/issue-sync/fetch-sq-changelogs.js` |
| Issue sync (per-pipeline) | `src/pipelines/sq-10.4/pipeline/project-migration/helpers/sync-project-issues.js` |
| Parallel sync utility | `src/shared/utils/parallel-sync/` |
| Metadata sync command | `src/commands/sync-metadata/` |

### Key Differences from CloudVoyager

1. **Worker pool model**: CloudVoyager uses Node.js `mapConcurrent` (Promise.all with concurrency limit). Go implementation uses goroutine worker pool with `errgroup` and semaphores.
2. **Changelog fetching**: CloudVoyager fetches changelogs via a custom `getIssueChangelog` method. Go implementation adds `Changelog()` to the existing `server.IssuesClient`.
3. **Type safety**: Go uses strongly-typed `MatchedPair` structs instead of dynamic objects.
4. **Composite key**: Both implementations use `rule + component + line`. Go version strips the project key prefix from component paths for cross-platform matching.
5. **Pre-filter**: Go version adds `wasUpdatedAfterCreation` as a catch-all safety net (matching CloudVoyager).

## Known Limitations
<!-- updated: 2026-05-26_01:00:00 -->

- **Composite key collisions**: If two issues have the same rule, component path, and line number, the matching is ambiguous. The first match wins. This is rare but possible with certain rule types.
- **Comment ordering**: Comments are added in the order they appear in the SQ Server response, but the SC API does not guarantee insertion order preservation in the UI.
- **`IN_SANDBOX` status** (SonarQube 2025+): No equivalent in SonarQube Cloud. These issues are logged as warnings and left in `OPEN` status.
- **Transition chains**: Some SQ Server statuses may require multi-step transitions in SC (e.g., `OPEN -> CONFIRMED -> RESOLVED`). The current design applies the direct transition; if SC rejects it, the error is logged and the issue is marked as failed.
- **Rate limiting**: SonarQube Cloud enforces undocumented rate limits. The 429 retry with exponential backoff handles this, but extremely large projects (500K+ issues) may require reduced concurrency.

## Open Questions
<!-- updated: 2026-05-26_01:00:00 -->

- **Q1**: Should we support multi-step transition chains (e.g., OPEN -> CONFIRMED -> FALSE_POSITIVE) for statuses that require intermediate states?
- **Q2**: Should comment timestamps be preserved in the comment text (e.g., `[Migrated - 2024-01-15 by @user]`)?
- **Q3**: Should we implement a "dry-run" mode that reports what would be synced without making changes?
- **Q4**: For composite key collisions, should we use additional fields (message, textRange hash) as tiebreakers?
- **Q5**: Should pre-filtering be optional (configurable) for cases where users want to sync all issues regardless of changelog?

## References
<!-- updated: 2026-05-26_01:00:00 -->

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt
