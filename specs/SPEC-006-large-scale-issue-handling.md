---
spec_id: SPEC-006
title: "Large-Scale Issue Handling (Date-Window Bisection)"
status: draft
priority: P0
epic: "Scale & Reliability"
depends_on: [SPEC-002]
depended_on_by: [SPEC-008, SPEC-009]
estimated_effort: L
cloudvoyager_ref: "src/shared/utils/search-slicer/"
---

# SPEC-006: Large-Scale Issue Handling (Date-Window Bisection)
<!-- updated: 2026-05-26_01:00:00 -->

## Overview
<!-- updated: 2026-05-26_01:00:00 -->

SonarQube Server's `/api/issues/search` and `/api/hotspots/search` endpoints enforce a hard ceiling of 10,000 results per query. This is a backend limitation rooted in Elasticsearch's `index.max_result_window` default. For enterprise projects with tens or hundreds of thousands of issues, a single paginated fetch will silently truncate results at the 10K boundary, leading to data loss during migration.

This spec defines a date-window bisection algorithm (referred to as "search slicing" in CloudVoyager) that automatically detects when a query hits the 10K cap and recursively subdivides the date range until every sub-window returns fewer than 10,000 results. The algorithm is implemented as a reusable Go package that wraps the existing `sq-api-go` Paginator, providing transparent large-scale extraction for both issues and hotspots.

The Go implementation must be zero-allocation-friendly for the common case (projects under 10K issues) and only engage the bisection machinery when the cap is actually hit. It integrates with the existing `DataStore` and `ChunkWriter` pipeline so that downstream tasks (scanner report building, metadata sync) receive a complete, deduplicated issue set regardless of project size.

## Problem Statement
<!-- updated: 2026-05-26_01:00:00 -->

Users migrating large SonarQube Server projects (common in enterprise environments with 50K-500K+ issues per project) cannot extract all issues using the standard paginated API. The SonarQube Server API returns at most 10,000 results for any single search query, even when paginating. Projects exceeding this threshold silently lose issues during extraction, causing incomplete migrations that are difficult to detect until post-migration validation.

Without this feature, users must manually segment their extraction by date ranges or accept data loss. Neither option is acceptable for production migrations where issue history integrity is critical for compliance and audit trails.

## User Stories
<!-- updated: 2026-05-26_01:00:00 -->

- **As a** migration operator, **I want to** extract all issues from a SonarQube Server project regardless of project size, **so that** I can ensure a complete migration with no data loss.
- **As a** migration operator, **I want to** see clear progress logging when date-window slicing is active, **so that** I can monitor the extraction of large projects and estimate completion time.
- **As a** migration operator, **I want** the tool to automatically detect and handle the 10K limit without manual configuration, **so that** I don't need to know about SonarQube API internals.
- **As a** migration operator, **I want to** extract all security hotspots from large projects that exceed 10K hotspots, **so that** hotspot data is also fully migrated.

## Requirements
<!-- updated: 2026-05-26_01:00:00 -->

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Probe the total result count before fetching by issuing a `ps=1&p=1` request and reading `paging.total` | Must |
| FR-2 | If total < 10,000, use the standard `Paginator.All()` path (zero overhead for small projects) | Must |
| FR-3 | If total >= 10,000, activate date-window bisection using `createdAfter` and `createdBefore` query parameters | Must |
| FR-4 | Initial window span: `2006-01-01T00:00:00+0000` (SonarQube epoch) to current UTC time | Must |
| FR-5 | Split the initial span into 12 equal-duration windows as a first pass | Must |
| FR-6 | For each window, probe the total; if >= 10,000, recursively bisect at the midpoint | Must |
| FR-7 | Terminate recursion when the window cannot be split further (start == end at second granularity) and fetch directly despite exceeding the limit | Must |
| FR-8 | Deduplicate results across all windows by issue key (or hotspot key) | Must |
| FR-9 | Support both `/api/issues/search` and `/api/hotspots/search` endpoints via a generic interface | Must |
| FR-10 | Log each window's result count and any splits for debugging | Must |
| FR-11 | Support a configurable result limit (default 10,000) for future-proofing | Should |
| FR-12 | Emit per-window progress events consumable by the wizard UI | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Zero overhead for projects under 10K issues (single extra probe request) | < 50ms added latency |
| NFR-2 | Memory efficiency: stream results through ChunkWriter, don't hold all issues in memory | Peak RSS < 2x single-page size |
| NFR-3 | Total API calls minimized: only split windows that exceed the limit | <= 2x theoretical minimum |
| NFR-4 | Deduplication must handle millions of issue keys efficiently | O(n) via map lookup |
| NFR-5 | Date formatting must use SonarQube's expected format (`+0000` suffix, no `.000Z` milliseconds) | 100% API compatibility |
| NFR-6 | Probe requests should respect the rate limiting configuration from SPEC-016 to avoid triggering 429 responses during recursive window splitting | Zero 429s under normal operation |

## Technical Design
<!-- updated: 2026-05-26_01:00:00 -->

### Architecture

The search slicer is implemented as a standalone package `go/internal/searchslicer/` that wraps the existing `sq-api-go` Paginator. It exposes a single high-level function that the extract tasks call instead of `Paginator.All()`.

```
go/internal/searchslicer/
    slicer.go          # FetchAll() entry point, probe logic
    window.go          # DateWindow type, buildDateWindows(), splitMidpoint()
    fetch.go           # fetchWindow() recursive bisection
    dedup.go           # deduplicateByKey()
    slicer_test.go     # Unit tests with mock API
    window_test.go     # Date arithmetic tests
    fetch_test.go      # Recursive split tests
```

Integration point: `go/internal/extract/tasks_issues.go` calls `searchslicer.FetchAll()` instead of `paginator.All()` when extracting issues and hotspots.

### Key Algorithms

#### Date-Window Bisection (Pseudocode)

```
function FetchAll(ctx, probeTotal, fetchPage, resultLimit) -> ([]Item, error):
    total := probeTotal(ctx, fullDateRange)
    if total < resultLimit:
        return fetchPage(ctx, fullDateRange)  // standard paginator path

    log.Warn("Result limit hit (%d >= %d), activating date-window slicing", total, resultLimit)

    windows := buildDateWindows(SONARQUBE_EPOCH, now(), INITIAL_WINDOW_COUNT=12)
    allResults := []
    seen := map[string]bool{}  // dedup by key

    for i, window in windows:
        results := fetchWindow(ctx, probeTotal, fetchPage, window, resultLimit, seen)
        allResults = append(allResults, results...)
        log.Info("Window %d/%d: fetched %d (total so far: %d)", i+1, len(windows), len(results), len(allResults))

    return allResults, nil


function fetchWindow(ctx, probeTotal, fetchPage, window, limit, seen) -> []Item:
    total := probeTotal(ctx, window)

    if total < limit:
        items := fetchPage(ctx, window)
        return deduplicate(items, seen)

    midpoint := splitMidpoint(window.Start, window.End)

    // Guard: unsplittable window (same second boundary)
    if midpoint == window.Start || midpoint == window.End:
        log.Warn("Unsplittable window at %s, fetching %d results directly", midpoint, total)
        items := fetchPage(ctx, window)
        return deduplicate(items, seen)

    log.Warn("Window %s..%s has %d results, splitting at %s", window.Start, window.End, total, midpoint)

    leftItems  := fetchWindow(ctx, probeTotal, fetchPage, {window.Start, midpoint}, limit, seen)
    rightItems := fetchWindow(ctx, probeTotal, fetchPage, {midpoint+1s, window.End}, limit, seen)

    return append(leftItems, rightItems...)

// NOTE: All date arithmetic should use second-level granularity to match the
// SonarQube API date format (2006-01-02T15:04:05+0000). Do not use millisecond
// precision.
```

#### Date Window Construction

```
function buildDateWindows(startISO, endISO, count) -> []DateWindow:
    startMs := parseTime(startISO).UnixMilli()
    endMs   := parseTime(endISO).UnixMilli()
    spanMs  := endMs - startMs
    stepMs  := spanMs / count

    windows := []
    for i := 0; i < count; i++:
        wStart := startMs + i * stepMs
        wEnd   := startMs + (i+1) * stepMs
        if i == count - 1:
            wEnd = endMs  // last window absorbs rounding remainder
        windows = append(windows, {formatSQDate(wStart), formatSQDate(wEnd)})

    return windows
```

#### SonarQube Date Formatting

SonarQube Server rejects ISO 8601 with milliseconds (`.000Z`). Dates must use the `+0000` timezone offset format:

```go
func formatSQDate(t time.Time) string {
    return t.UTC().Format("2006-01-02T15:04:05+0000")
}
```

### Data Flow

```
1. Extract task calls searchslicer.FetchAll(ctx, probeFunc, fetchFunc, 10000)
2. probeFunc sends GET /api/issues/search?components=X&ps=1&p=1 -> reads paging.total
3. If total < 10000: fetchFunc paginates normally via Paginator.All()
4. If total >= 10000:
   a. Build 12 initial date windows spanning 2006-01-01 to now
   b. For each window, set createdAfter/createdBefore params
   c. Probe each window's total
   d. If window total >= 10000, recursively bisect
   e. Fetch all pages within each sub-window
   f. Deduplicate by issue key across all windows
5. Results written to ChunkWriter as JSONL
6. Downstream tasks (scanner report builder) consume deduplicated issue set
```

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/issues/search` | GET | Fetch issues with `createdAfter`, `createdBefore`, `components`, `p`, `ps` params |
| `/api/hotspots/search` | GET | Fetch hotspots with `createdAfter`, `createdBefore`, `projectKey`, `p`, `ps` params |

> **Note:** `/api/issues/search` uses the `components` parameter to scope by project, while `/api/hotspots/search` uses the `projectKey` parameter. The generic slicer interface (`ProbeTotalFunc` / `FetchPageFunc`) must account for this parameter divergence — the caller is responsible for building the correct query parameters for each endpoint.

### Go Type Definitions

```go
// DateWindow represents a time range for search slicing.
type DateWindow struct {
    Start time.Time
    End   time.Time
}

// ProbeTotalFunc probes the total result count for a date-windowed query.
// Returns the total count from the API's paging metadata.
type ProbeTotalFunc func(ctx context.Context, window *DateWindow) (int, error)

// FetchPageFunc fetches all paginated results within a date window.
// Returns the items and any error. Must handle pagination internally.
type FetchPageFunc[T any] func(ctx context.Context, window *DateWindow) ([]T, error)

// KeyFunc extracts a unique deduplication key from an item.
type KeyFunc[T any] func(T) string

// SlicerConfig holds configuration for the search slicer.
type SlicerConfig struct {
    ResultLimit        int           // Default: 10000
    InitialWindowCount int           // Default: 12
    EpochStart         time.Time     // Default: 2006-01-01T00:00:00Z
    Logger             *slog.Logger
}

// FetchAll extracts all items, automatically engaging date-window
// bisection when the result count exceeds the configured limit.
func FetchAll[T any](
    ctx context.Context,
    cfg SlicerConfig,
    probeTotal ProbeTotalFunc,
    fetchPage FetchPageFunc[T],
    keyFn KeyFunc[T],
) ([]T, error)
```

### Concurrency Considerations

- Date windows are fetched **sequentially** (not concurrently) to avoid overwhelming the SonarQube Server API with parallel requests. The server is often the bottleneck, and concurrent large queries can cause 429 rate limiting or OOM on the server side.
- Within each window, the standard `Paginator` handles page-level fetching sequentially (existing behavior).
- The `seen` deduplication map is not concurrent-safe and does not need to be, since windows are processed sequentially.

### Error Handling

| Scenario | Behavior |
|----------|----------|
| Probe request fails (network error, 5xx) | Retry via existing `retryTransport` (3 attempts with backoff) |
| Probe returns 0 for a non-empty window | Skip window (empty date range) |
| Fetch fails mid-window | Return error, abort extraction for this project |
| Unsplittable window (>10K on single second) | Log warning, fetch directly (accepts potential truncation) |
| Context cancellation | Propagate immediately, return partial results with error |

## Acceptance Criteria
<!-- updated: 2026-05-26_01:00:00 -->

- [ ] AC-1: Projects with < 10,000 issues are extracted via standard pagination with exactly 1 extra probe request.
- [ ] AC-2: Projects with > 10,000 issues activate date-window slicing and extract all issues without data loss.
- [ ] AC-3: Results are deduplicated by issue key; no duplicate issues appear in the output JSONL.
- [ ] AC-4: Log output includes window split decisions, per-window counts, and total issue count.
- [ ] AC-5: The slicer works for both `/api/issues/search` and `/api/hotspots/search` via the generic type parameter.
- [ ] AC-6: Dates are formatted in SonarQube's `+0000` format, not ISO 8601 `.000Z`.
- [ ] AC-7: Unsplittable windows (single-second boundary) are handled gracefully with a warning log.
- [ ] AC-8: Unit tests cover: small project (no slicing), medium project (one level of slicing), large project (recursive slicing), unsplittable window, empty window, deduplication.
- [ ] AC-9: Integration test with httptest mock server validates end-to-end extraction of >10K issues.
- [ ] AC-10: No memory leak: results are streamed to ChunkWriter during extraction, not accumulated in a single slice for the final write.

## CloudVoyager Reference
<!-- updated: 2026-05-26_01:00:00 -->

| Area | Path |
|------|------|
| Search slicer entry point | `src/shared/utils/search-slicer/index.js` |
| Date-window bisection | `src/shared/utils/search-slicer/helpers/slice-by-creation-date.js` |
| Recursive window fetch | `src/shared/utils/search-slicer/helpers/fetch-window.js` |
| Midpoint calculation | `src/shared/utils/search-slicer/helpers/split-midpoint.js` |
| Date window builder | `src/shared/utils/search-slicer/helpers/build-date-windows.js` |
| Date formatting | `src/shared/utils/search-slicer/helpers/format-sonarqube-date.js` |
| Deduplication | `src/shared/utils/search-slicer/helpers/deduplicate-results.js` |
| Constants (10K limit) | `src/shared/utils/search-slicer/helpers/constants.js` |

### Key Differences from CloudVoyager

1. **Language**: CloudVoyager uses async/await JavaScript; Go implementation uses goroutines and context.
2. **Generics**: Go version uses type parameters (`FetchAll[T any]`) instead of dynamic `dataKey` string lookup.
3. **Paginator reuse**: Go version delegates within-window pagination to the existing `sq-api-go.Paginator` instead of reimplementing pagination.
4. **Memory model**: Go version should write results to `ChunkWriter` incrementally per window rather than accumulating all results in a single slice.

## Known Limitations
<!-- updated: 2026-05-26_01:00:00 -->

- If a single second has >10,000 issues (theoretically impossible but not enforced), the algorithm cannot split further and will fetch directly, potentially truncating results. A warning is logged.
- The algorithm uses sequential window processing. For SonarQube Server instances with very fast response times, parallel window fetching could improve throughput but risks rate limiting.
- The `createdAfter` / `createdBefore` parameters are based on issue creation date, not update date. Issues that were created outside the window but updated within it are correctly captured by their creation date.
- SonarQube Server versions prior to 7.x may not support the `createdAfter` / `createdBefore` parameters. The tool requires SonarQube Server 9.9+ (see system requirements).

## Open Questions
<!-- updated: 2026-05-26_01:00:00 -->

- **Q1**: Should we support concurrent window fetching behind a feature flag for high-throughput SonarQube Server instances? CloudVoyager processes windows sequentially.
- **Q2**: Should the initial window count (12) be configurable, or is the hardcoded default sufficient? Larger initial counts reduce recursion depth but increase probe requests.
- **Q3**: For the unsplittable window edge case, should we attempt secondary dimension slicing (e.g., by severity or file path) before falling back to direct fetch? CloudVoyager does not implement this.
- **Q4**: Should the slicer emit structured progress events (e.g., via a channel) for the wizard UI to display a progress bar?

## References
<!-- updated: 2026-05-26_01:00:00 -->

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt
