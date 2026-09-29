---
spec_id: SPEC-009
title: "Hotspot Metadata Synchronization"
status: draft
priority: P0
epic: "Scale & Reliability"
depends_on: [SPEC-003, SPEC-006, SPEC-008]
estimated_effort: L
cloudvoyager_ref: "src/pipelines/*/transfer/sync-hotspots/, src/pipelines/*/pipeline/project-migration/helpers/sync-project-hotspots.js"
---

# SPEC-009: Hotspot Metadata Synchronization
<!-- updated: 2026-05-26_01:00:00 -->

## Overview
<!-- updated: 2026-05-26_01:00:00 -->

After security hotspots are uploaded to SonarQube Cloud via scanner reports, they all appear with the default `TO_REVIEW` status. The hotspot metadata synchronization phase matches each SonarQube Cloud hotspot to its SonarQube Server counterpart and replicates the original review status and resolution. Unlike issue metadata sync (SPEC-008), hotspot sync is simpler: it only handles status transitions and review comments, with no tag or assignment sync (SC API limitation).

The matching algorithm uses a composite key of `ruleKey + componentPath + line` to pair SonarQube Server hotspots with their SonarQube Cloud counterparts. Once matched, reviewed hotspots have their status changed via `/api/hotspots/change_status`, and review comments are copied via `/api/hotspots/add_comment`.

Hotspot sync is designed as a non-critical operation: individual failures are logged but do not abort the overall migration. This reflects the lower severity of hotspot status loss compared to issue status loss, and the fact that hotspot assignments are not syncable via the SC API.

## Problem Statement
<!-- updated: 2026-05-26_01:00:00 -->

After scanner report upload, all migrated security hotspots in SonarQube Cloud appear as `TO_REVIEW`. Security teams that have already reviewed and resolved hotspots in SonarQube Server (marking them as `SAFE`, `FIXED`, or `ACKNOWLEDGED`) must re-review every hotspot after migration. For projects with thousands of hotspots, this represents a significant waste of security engineering time and creates a gap in the security review workflow.

Without hotspot metadata sync, the migration effectively resets the security review state, forcing teams to re-evaluate hotspots that were already triaged. This undermines confidence in the migration process and can delay the cutover from SonarQube Server to SonarQube Cloud.

## User Stories
<!-- updated: 2026-05-26_01:00:00 -->

- **As a** security engineer, **I want** hotspot review statuses (SAFE, FIXED, ACKNOWLEDGED) to be preserved during migration, **so that** I don't have to re-review thousands of already-triaged hotspots.
- **As a** security engineer, **I want** review comments and notes to be migrated, **so that** the rationale for each review decision is preserved.
- **As a** migration operator, **I want** hotspot sync failures to be logged but not abort the migration, **so that** a single failed hotspot doesn't block the entire project migration.
- **As a** migration operator, **I want to** see statistics on how many hotspots were matched, synced, and failed, **so that** I can verify the migration quality.

## Requirements
<!-- updated: 2026-05-26_01:00:00 -->

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Wait for SonarQube Cloud indexing before attempting hotspot sync (reuse `waitForSCIndexing` from SPEC-008) | Must |
| FR-2 | Fetch all SonarQube Server hotspots for the project via `/api/hotspots/search` (with search slicing from SPEC-006 if > 10K) | Must |
| FR-3 | Fetch all SonarQube Cloud hotspots for the same project via `/api/hotspots/search` | Must |
| FR-4 | Match SQ Server hotspots to SC hotspots by composite key: `ruleKey + componentPath + line` | Must |
| FR-5 | For matched hotspots with `REVIEWED` status, change SC hotspot status via `/api/hotspots/change_status` | Must |
| FR-6 | Map review resolutions: SAFE, FIXED, ACKNOWLEDGED | Must |
| FR-7 | Skip hotspots with `TO_REVIEW` status (no action needed, already the default) | Must |
| FR-8 | Copy review comments from SQ Server to SC via `/api/hotspots/add_comment`, prepending `[Migrated from SonarQube Server]` | Must |
| FR-9 | Configurable concurrency for hotspot sync operations (default: 3 concurrent) | Must |
| FR-10 | Individual hotspot sync failures are logged as warnings and do not abort the migration | Must |
| FR-11 | Report sync statistics: total SQ hotspots, matched, unmatched, status_changed, comments_copied, failed | Must |
| FR-12 | Document hotspot assignments as a known unsyncable limitation | Must |
| FR-13 | Support `--skip-hotspot-sync` flag to disable hotspot sync entirely | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Hotspot sync throughput | >= 10 status changes/second at concurrency 3 |
| NFR-2 | Memory: do not load all hotspots into memory if using search slicing | Streaming via ChunkWriter |
| NFR-3 | Individual failure tolerance | 100% (never abort on single hotspot failure) |
| NFR-4 | Total sync time for 5K hotspots at concurrency 3 | < 30 minutes |

## Technical Design
<!-- updated: 2026-05-26_01:00:00 -->

### Architecture

Hotspot sync is implemented in a new package `go/internal/hotspotsync/` or as part of the broader `issuesync` package since it shares the matching and indexing-wait logic.

```
go/internal/hotspotsync/
    sync.go              # SyncHotspots() orchestrator
    matcher.go           # matchHotspots() composite key matching
    transitions.go       # applyHotspotStatus() status change API calls
    comments.go          # syncHotspotComments() comment migration
    types.go             # MatchedHotspotPair, HotspotSyncStats, HotspotSyncConfig
    sync_test.go         # Unit tests
```

This package is called by a new migrate task registered in `go/internal/migrate/tasks_hotspots.go`. It depends on the `searchslicer` package (SPEC-006) and the `waitForSCIndexing` function from `issuesync` (SPEC-008).

> **Note:** The `waitForSCIndexing` function from SPEC-008 must be generic or use `any` type parameter to support both `[]types.Issue` and `[]types.Hotspot` return types. The SPEC-006 search slicer is already generic via `FetchAll[T any]`.

### Key Algorithms

#### Hotspot Matching (Composite Key)

```go
// hotspotCompositeKey builds a unique matching key for a hotspot.
func hotspotCompositeKey(ruleKey, componentPath string, line int) string {
    return fmt.Sprintf("%s:%s:%d", ruleKey, componentPath, line)
}

type MatchedHotspotPair struct {
    SQHotspot types.Hotspot // SonarQube Server hotspot
    SCHotspot types.Hotspot // SonarQube Cloud hotspot
}

func matchHotspots(sqHotspots, scHotspots []types.Hotspot, projectKey string) ([]MatchedHotspotPair, []types.Hotspot, []types.Hotspot) {
    // Build SC lookup map
    scByKey := make(map[string]types.Hotspot, len(scHotspots))
    for _, sc := range scHotspots {
        path := extractComponentPath(sc.Component, projectKey)
        key := hotspotCompositeKey(sc.RuleKey, path, sc.Line)
        scByKey[key] = sc
    }

    var matched []MatchedHotspotPair
    var unmatchedSQ []types.Hotspot

    for _, sq := range sqHotspots {
        path := extractComponentPath(sq.Component, projectKey)
        key := hotspotCompositeKey(sq.RuleKey, path, sq.Line)
        if sc, ok := scByKey[key]; ok {
            matched = append(matched, MatchedHotspotPair{SQHotspot: sq, SCHotspot: sc})
            delete(scByKey, key)
        } else {
            unmatchedSQ = append(unmatchedSQ, sq)
        }
    }

    unmatchedSC := make([]types.Hotspot, 0, len(scByKey))
    for _, sc := range scByKey {
        unmatchedSC = append(unmatchedSC, sc)
    }

    return matched, unmatchedSQ, unmatchedSC
}

// extractComponentPath strips the project key prefix from a component key.
func extractComponentPath(component, projectKey string) string {
    prefix := projectKey + ":"
    if strings.HasPrefix(component, prefix) {
        return component[len(prefix):]
    }
    return component
}
```

#### Hotspot Status Transition

```go
// HotspotTransition describes a status change for a hotspot.
type HotspotTransition struct {
    Status     string // "REVIEWED" or "TO_REVIEW"
    Resolution string // "SAFE", "FIXED", "ACKNOWLEDGED" (only when Status == "REVIEWED")
}

// statusRequiresSync returns true if the SQ Server hotspot needs a status change in SC.
func statusRequiresSync(sqStatus, sqResolution string) bool {
    // TO_REVIEW is the default SC status, no action needed
    return sqStatus == "REVIEWED"
}

func applyHotspotStatus(ctx context.Context, client *cloud.Client, scHotspotKey, sqStatus, sqResolution string) error {
    if !statusRequiresSync(sqStatus, sqResolution) {
        return nil // TO_REVIEW is already the default
    }

    // Validate resolution
    validResolutions := map[string]bool{
        "SAFE":         true,
        "FIXED":        true,
        "ACKNOWLEDGED": true,
    }
    if !validResolutions[sqResolution] {
        return fmt.Errorf("unknown hotspot resolution %q for hotspot %s", sqResolution, scHotspotKey)
    }

    return client.Hotspots().ChangeStatus(ctx, scHotspotKey, "REVIEWED", sqResolution)
}
```

#### Hotspot Comment Sync

```go
const hotspotMigrationPrefix = "[Migrated from SonarQube Server]"

func syncHotspotComments(ctx context.Context, client *cloud.Client, scHotspotKey string, sqComments []HotspotComment) (int, error) {
    copied := 0
    for _, c := range sqComments {
        text := fmt.Sprintf("%s\n\n%s", hotspotMigrationPrefix, c.Markdown)
        if c.Login != "" {
            text = fmt.Sprintf("%s (originally by @%s)\n\n%s", hotspotMigrationPrefix, c.Login, c.Markdown)
        }

        if err := client.Hotspots().AddComment(ctx, scHotspotKey, text); err != nil {
            slog.Warn("Failed to add hotspot comment",
                "hotspotKey", scHotspotKey, "err", err)
            continue // non-critical, continue with other comments
        }
        copied++
    }
    return copied, nil
}

type HotspotComment struct {
    Key      string `json:"key"`
    Login    string `json:"login"`
    Markdown string `json:"markdown"`
    HtmlText string `json:"htmlText"`
    Date     string `json:"createdAt"`
}
```

#### Orchestrator

```go
type HotspotSyncConfig struct {
    Concurrency int           // default 3
    MaxRetries  int           // default 3
    RetryBackoff []time.Duration // default: 1s, 2s, 4s
}

type HotspotSyncStats struct {
    TotalSQ       int
    TotalSC       int
    Matched       int
    UnmatchedSQ   int
    UnmatchedSC   int
    StatusChanged int
    CommentsCopied int
    Failed        int
}

func SyncHotspots(ctx context.Context, sqClient *server.Client, scClient *cloud.Client,
    sqProjectKey, scProjectKey string, cfg HotspotSyncConfig) (*HotspotSyncStats, error) {

    stats := &HotspotSyncStats{}

    // 1. Fetch SQ Server hotspots (via search slicer if needed)
    sqHotspots, err := fetchSQHotspots(ctx, sqClient, sqProjectKey)
    if err != nil {
        return nil, fmt.Errorf("fetching SQ hotspots: %w", err)
    }
    stats.TotalSQ = len(sqHotspots)

    if len(sqHotspots) == 0 {
        slog.Info("No SQ hotspots to sync", "project", sqProjectKey)
        return stats, nil
    }

    // 2. Wait for SC indexing, then fetch SC hotspots
    scHotspots, err := waitForSCIndexing(ctx, func() ([]types.Hotspot, error) {
        return scClient.Hotspots().Search(ctx, scProjectKey).All(ctx)
    }, len(sqHotspots), WaitOpts{
        Label:        "hotspots",
        ProjectKey:   scProjectKey,
        InitialDelay: 10 * time.Second,
        MaxDelay:     60 * time.Second,
        MaxRetries:   10,
    })
    if err != nil {
        return nil, fmt.Errorf("waiting for SC hotspot indexing: %w", err)
    }
    stats.TotalSC = len(scHotspots)

    // 3. Match hotspots
    matched, unmatchedSQ, unmatchedSC := matchHotspots(sqHotspots, scHotspots, scProjectKey)
    stats.Matched = len(matched)
    stats.UnmatchedSQ = len(unmatchedSQ)
    stats.UnmatchedSC = len(unmatchedSC)

    if len(unmatchedSQ) > 0 {
        slog.Warn("Unmatched SQ hotspots (not found in SC)",
            "count", len(unmatchedSQ), "project", sqProjectKey)
    }

    // 4. Sync matched hotspots with concurrency limit
    sem := make(chan struct{}, cfg.Concurrency)
    var mu sync.Mutex
    var wg sync.WaitGroup

    for _, pair := range matched {
        pair := pair
        wg.Add(1)
        sem <- struct{}{}

        go func() {
            defer func() { <-sem; wg.Done() }()

            // Apply status transition
            if statusRequiresSync(pair.SQHotspot.Status, pair.SQHotspot.Resolution) {
                err := retryWithBackoff(ctx, cfg.RetryBackoff, func() error {
                    return applyHotspotStatus(ctx, scClient, pair.SCHotspot.Key,
                        pair.SQHotspot.Status, pair.SQHotspot.Resolution)
                })
                if err != nil {
                    slog.Warn("Failed to sync hotspot status",
                        "sqKey", pair.SQHotspot.Key,
                        "scKey", pair.SCHotspot.Key,
                        "err", err)
                    mu.Lock()
                    stats.Failed++
                    mu.Unlock()
                    return
                }
                mu.Lock()
                stats.StatusChanged++
                mu.Unlock()
            }

            // Copy review comments
            sqComments, err := fetchHotspotComments(ctx, sqClient, pair.SQHotspot.Key)
            if err != nil {
                slog.Warn("Failed to fetch SQ hotspot comments",
                    "hotspotKey", pair.SQHotspot.Key, "err", err)
            } else if len(sqComments) > 0 {
                copied, _ := syncHotspotComments(ctx, scClient, pair.SCHotspot.Key, sqComments)
                mu.Lock()
                stats.CommentsCopied += copied
                mu.Unlock()
            }
        }()
    }

    wg.Wait()

    slog.Info("Hotspot sync complete",
        "project", sqProjectKey,
        "matched", stats.Matched,
        "statusChanged", stats.StatusChanged,
        "commentsCopied", stats.CommentsCopied,
        "failed", stats.Failed)

    return stats, nil
}
```

#### Retry with Backoff

```go
func retryWithBackoff(ctx context.Context, backoff []time.Duration, fn func() error) error {
    var lastErr error
    for attempt := 0; attempt <= len(backoff); attempt++ {
        lastErr = fn()
        if lastErr == nil {
            return nil
        }

        if attempt < len(backoff) {
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-time.After(backoff[attempt]):
            }
        }
    }
    return lastErr
}
```

### Race Condition Analysis

- Hotspot matching map is built once before sync begins -- no concurrent writes
- Per-hotspot sync operations (status change, comment) are independent -- no shared mutable state between hotspots
- Semaphore channel controls concurrency -- no shared slice iteration
- SC API calls are stateless -- concurrent status changes on different hotspots cannot conflict
- Stats counters (`StatusChanged`, `CommentsCopied`, `Failed`) are protected by mutex

### Data Flow

```
1. Scanner report uploaded to SC (includes hotspot data)
2. Wait for SC hotspot indexing: poll SC /api/hotspots/search until results > 0
3. Fetch SQ Server hotspots via /api/hotspots/search (with SPEC-006 slicing if > 10K)
4. Fetch SC hotspots via /api/hotspots/search
5. Match SQ <-> SC hotspots by composite key (ruleKey + componentPath + line)
6. For each matched pair where SQ status == REVIEWED:
   a. Change SC hotspot status via /api/hotspots/change_status
   b. Fetch SQ hotspot comments
   c. Copy comments to SC via /api/hotspots/add_comment
7. Report statistics: total, matched, unmatched, status_changed, comments_copied, failed
```

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/hotspots/search` | GET | Fetch hotspots from SQ Server and SC |
| `/api/hotspots/change_status` | POST | Change SC hotspot review status and resolution |
| `/api/hotspots/add_comment` | POST | Add review comment to SC hotspot |
| `/api/hotspots/show` | GET | Fetch detailed hotspot data including comments (SQ Server) |

### Go Type Extensions Required

The existing `types.Hotspot` struct needs a `RuleKey` field for matching:

```go
type Hotspot struct {
    // ... existing fields ...
    RuleKey string `json:"ruleKey"` // NEW: needed for composite key matching
}
```

Additional API methods needed:

```go
// cloud/hotspots.go - new methods
func (h *HotspotsClient) ChangeStatus(ctx context.Context, hotspotKey, status, resolution string) error
func (h *HotspotsClient) AddComment(ctx context.Context, hotspotKey, text string) error

// server/hotspots.go - new methods
func (h *HotspotsClient) Show(ctx context.Context, hotspotKey string) (*types.HotspotDetail, error)
```

### Status Mapping Reference

| SQ Server Status | SQ Server Resolution | SC API Call | SC Result |
|------------------|---------------------|-------------|-----------|
| `TO_REVIEW` | N/A | No action | Already `TO_REVIEW` (default) |
| `REVIEWED` | `SAFE` | `POST /api/hotspots/change_status` with `status=REVIEWED&resolution=SAFE` | `REVIEWED/SAFE` |
| `REVIEWED` | `FIXED` | `POST /api/hotspots/change_status` with `status=REVIEWED&resolution=FIXED` | `REVIEWED/FIXED` |
| `REVIEWED` | `ACKNOWLEDGED` | `POST /api/hotspots/change_status` with `status=REVIEWED&resolution=ACKNOWLEDGED` | `REVIEWED/ACKNOWLEDGED` |

## Acceptance Criteria
<!-- updated: 2026-05-26_01:00:00 -->

- [ ] AC-1: SC hotspots with SQ Server status `REVIEWED/SAFE` are transitioned to `REVIEWED` with resolution `SAFE`.
- [ ] AC-2: SC hotspots with SQ Server status `REVIEWED/FIXED` are transitioned to `REVIEWED` with resolution `FIXED`.
- [ ] AC-3: SC hotspots with SQ Server status `REVIEWED/ACKNOWLEDGED` are transitioned to `REVIEWED` with resolution `ACKNOWLEDGED`.
- [ ] AC-4: SC hotspots with SQ Server status `TO_REVIEW` are left unchanged (no API call).
- [ ] AC-5: Review comments are copied with `[Migrated from SonarQube Server]` prefix and original author attribution.
- [ ] AC-6: Individual hotspot sync failures are logged as warnings; migration continues.
- [ ] AC-7: SC indexing wait retries with exponential backoff before matching.
- [ ] AC-8: Concurrency is configurable (default 3) and respects the configured limit.
- [ ] AC-9: Sync statistics are reported: total, matched, unmatched, status_changed, comments_copied, failed.
- [ ] AC-10: `--skip-hotspot-sync` flag disables hotspot sync entirely.
- [ ] AC-11: Hotspot assignments are documented as a known limitation (SC API does not support assignment).
- [ ] AC-12: Unit tests cover: composite key matching, status transition mapping, comment formatting, error resilience.
- [ ] AC-13: Integration test validates end-to-end hotspot sync with mock APIs.

## CloudVoyager Reference
<!-- updated: 2026-05-26_01:00:00 -->

| Area | Path |
|------|------|
| Hotspot sync orchestrator | `src/pipelines/sq-10.4/pipeline/project-migration/helpers/sync-project-hotspots.js` |
| Hotspot sync implementation | `src/pipelines/sq-10.4/sonarcloud/migrators/hotspot-sync.js` |
| Hotspot extraction | `src/pipelines/sq-10.4/sonarqube/extractors/hotspots.js` |
| SC hotspot verification | `src/shared/verification/checkers/hotspots.js` |

### Key Differences from CloudVoyager

1. **Non-critical design**: Both CloudVoyager and this Go implementation treat hotspot sync as non-critical (log and continue on failure). The Go version makes this explicit in the error handling design.
2. **Concurrency model**: CloudVoyager uses `mapConcurrent` with configurable concurrency. Go uses goroutines with a semaphore channel.
3. **Search slicing**: Go version reuses the generic `searchslicer.FetchAll` from SPEC-006 for hotspots that exceed 10K. CloudVoyager has a separate hotspot extraction path.
4. **Indexing wait**: Go version reuses the generic `waitForSCIndexing` function via type parameters, while CloudVoyager has separate wait implementations.

## Known Limitations
<!-- updated: 2026-05-26_01:00:00 -->

- **Hotspot assignments are not syncable**: The SonarQube Cloud API does not expose an endpoint to assign hotspots to users. This is a platform limitation, not a tool limitation. Users must reassign hotspots manually after migration.
- **Composite key collisions**: Same limitation as SPEC-008. If two hotspots share the same rule, component path, and line number, matching is ambiguous.
- **Comment author preservation**: Comments are created by the migration user account. The original author is preserved in the comment text only, not in the API metadata.
- **Hotspot detail API**: Fetching comments for each hotspot requires a per-hotspot `/api/hotspots/show` call, which is slower than a batch endpoint. For projects with many commented hotspots, this can be a bottleneck.
- **No pre-filtering**: Unlike SPEC-008, hotspot sync does not implement pre-filtering via changelogs. This is because the hotspot API does not expose changelogs, and hotspot counts are typically much lower than issue counts.

## Open Questions
<!-- updated: 2026-05-26_01:00:00 -->

- **Q1**: Should we implement pre-filtering for hotspots (skip `TO_REVIEW` hotspots that were never touched)? This would require checking the hotspot detail for each hotspot, which may negate the performance benefit.
- **Q2**: Should the default concurrency be higher than 3? CloudVoyager uses a configurable concurrency per pipeline, but the default appears conservative.
- **Q3**: Should hotspot sync be a separate migrate task or combined with issue sync into a single "metadata sync" task?
- **Q4**: Should we attempt to fetch hotspot comments in batch (e.g., via `/api/hotspots/search` with `additionalFields=comments`) to reduce API calls?

## References
<!-- updated: 2026-05-26_01:00:00 -->

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt
