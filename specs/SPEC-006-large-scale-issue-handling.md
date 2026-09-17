---
spec_id: SPEC-006
title: "Large-Scale Issue Handling (Date-Window Bisection)"
status: partially-implemented
priority: P0
epic: "Scale & Reliability"
depends_on: [SPEC-002]
depended_on_by: [SPEC-008, SPEC-009]
estimated_effort: L
cloudvoyager_ref: "src/shared/utils/search-slicer/"
---

# SPEC-006: Large-Scale Issue Handling (Date-Window Bisection)
<!-- updated: 2026-09-16_10:00:00 -->

## Implementation Status
<!-- updated: 2026-09-16_10:00:00 -->

Implemented for `/api/issues/search` by [#574](https://github.com/SonarSource/sonar-migration-tool/issues/574). Several requirements in this file were **wrong as written** and were corrected against live measurements taken on SonarQube Server 2026.4.1 (project `zvec`, 14,903 issues). Every correction below carries the response that produced it, so a future implementer can re-run the check rather than take this document's word for it.

> **Read this before touching hotspots.** FR-9 and AC-5 ("support `/api/hotspots/search` through the same generic interface") have been **removed, not deferred**. `/api/hotspots/search` declares no `createdAfter`/`createdBefore` parameters and **silently ignores unknown parameters**: it answers HTTP 200 with an unchanged total. Date slicing on hotspots therefore recurses forever over the same truncated result set while logging "slicing complete", and any acceptance test written against it passes. CloudVoyager shipped exactly this bug by reusing its issues path verbatim for hotspots. This is the single most dangerous instruction the original spec contained.

Summary of what changed:

| Item | Original | Corrected | Evidence |
|------|----------|-----------|----------|
| FR-3, FR-6 | Slice at `>= 10,000` | Slice at `> 10,000` | `p=20&ps=500` returns HTTP 200; exactly 10,000 is fetchable |
| FR-4, FR-5 | 2006 epoch, 12 first-pass windows | Superseded by **open outer edges** | An omitted bound is unbounded; the open-edge split is exact |
| FR-7 | Terminate at `start == end` | Terminate at `end - start <= 1s` | A zero-width window is HTTP 400 |
| FR-9, AC-5 | Generic slicer for issues **and** hotspots | **Removed** — issues only | Hotspots ignore unknown parameters silently |
| Pseudocode | Right window starts at `midpoint + 1s` | Right window starts at `midpoint` | Half-open `[after, before)` semantics |
| NFR-1 | One extra probe request | **Zero** extra requests | `paging.total` already arrives on page 1 |
| NFR-3 | <= 2x theoretical minimum | **Not met** (~85 requests vs 20) | Measured on a `zvec`-shaped project |
| Known Limitations | ">10K in one second is theoretically impossible" | It is the **normal** shape of a migrated project | 8,916 of `zvec`'s 14,903 issues share one second |

Deferred to a follow-up, with the seam reserved: the secondary (facet) partition for a window that cannot be subdivided by date. See **Q3** below for the measurements the follow-up should start from.

## Overview
<!-- updated: 2026-09-16_10:00:00 -->

SonarQube Server's `/api/issues/search` endpoint enforces a hard ceiling of 10,000 results per query. This is a backend limitation rooted in Elasticsearch's `index.max_result_window` default. For enterprise projects with tens or hundreds of thousands of issues, a single paginated fetch will silently truncate results at the 10K boundary, leading to data loss during migration.

The ceiling is on the **offset**, not on the total. `paging.total` is truthful above 10,000; what fails is asking for a row past the 10,000th: `p=21&ps=500` returns HTTP 400 `"Can return only the first 10000 results. 10500th result asked."`, while `p=20&ps=500` returns HTTP 200. This is why the split predicate is `total > 10000` and not `total >= 10000`.

This spec defines a date-window bisection algorithm (referred to as "search slicing" in CloudVoyager) that detects when a query exceeds the ceiling and recursively subdivides the creation-date range until every sub-window returns at most 10,000 results. It applies to issues only. `/api/hotspots/search`, `/api/measures/component_tree` and `/api/project_analyses/search` have no usable date axis and get a loud truncation warning instead of a slicer.

The common case — a project under the ceiling — must cost exactly what it costs today: the same requests, and no date parameters on any of them. The machinery engages only off the truncation signal that the existing pagination clamp already produces.

## Problem Statement
<!-- updated: 2026-05-26_01:00:00 -->

Users migrating large SonarQube Server projects (common in enterprise environments with 50K-500K+ issues per project) cannot extract all issues using the standard paginated API. The SonarQube Server API returns at most 10,000 results for any single search query, even when paginating. Projects exceeding this threshold silently lose issues during extraction, causing incomplete migrations that are difficult to detect until post-migration validation.

Without this feature, users must manually segment their extraction by date ranges or accept data loss. Neither option is acceptable for production migrations where issue history integrity is critical for compliance and audit trails.

## User Stories
<!-- updated: 2026-09-16_10:00:00 -->

- **As a** migration operator, **I want to** extract all issues from a SonarQube Server project regardless of project size, **so that** I can ensure a complete migration with no data loss.
- **As a** migration operator, **I want to** see clear progress logging when date-window slicing is active, **so that** I can monitor the extraction of large projects and estimate completion time.
- **As a** migration operator, **I want** the tool to automatically detect and handle the 10K limit without manual configuration, **so that** I don't need to know about SonarQube API internals.
- **As a** migration operator, **I want** any result set that *was* truncated to be named in the log, in an on-disk artefact and in the migration report, **so that** I never discover incomplete data after the migration instead of during it.

> The original fourth story ("extract all security hotspots from projects that exceed 10K hotspots") is **withdrawn**. It is not achievable through date slicing — see the Implementation Status banner — and writing it as a user story is what led CloudVoyager to ship a silent no-op.

## Requirements
<!-- updated: 2026-09-16_10:00:00 -->

### Functional Requirements

| ID | Requirement | Priority | Status |
|----|------------|----------|--------|
| FR-1 | ~~Probe the total result count before fetching by issuing a `ps=1&p=1` request~~ **Corrected:** take the total from `paging.total` on page 1 of the normal fetch. No separate probe at the top level. | Must | Done (corrected) |
| FR-2 | If the fetch was not truncated, return its items unchanged (zero overhead for small projects) | Must | Done |
| FR-3 | If total **> 10,000**, activate date-window bisection using `createdAfter` and `createdBefore` | Must | Done (corrected) |
| FR-4 | ~~Initial window span: `2006-01-01T00:00:00+0000` (SonarQube epoch) to current UTC time~~ **Superseded:** the root window sends **no date parameters at all**, and the outermost edge on each side stays open forever | Must | Superseded |
| FR-5 | ~~Split the initial span into 12 equal-duration windows as a first pass~~ **Superseded:** bisect from the open root; a coarse first pass is an optimisation, not a correctness requirement | Must | Superseded |
| FR-6 | For each window, take its total; if **> 10,000**, recursively bisect at the midpoint | Must | Done (corrected) |
| FR-7 | Terminate recursion when the window **cannot be narrowed below one second** (`end - start <= 1s`) and fetch directly despite exceeding the limit | Must | Done (corrected) |
| FR-8 | Deduplicate results across all windows by issue key | Must | Done |
| FR-9 | ~~Support both `/api/issues/search` and `/api/hotspots/search` via a generic interface~~ **REMOVED** — see the Implementation Status banner | Must | Removed |
| FR-10 | Log each window's result count and any splits for debugging | Must | Done |
| FR-11 | ~~Support a configurable result limit (default 10,000)~~ | Should | Not doing — 10,000 is an Elasticsearch default the operator cannot change from the client side |
| FR-12 | ~~Emit per-window progress events consumable by the wizard UI~~ | Should | Not doing — the progress tracker counts projects, not sub-project work |
| FR-13 | **After a sliced walk, reconcile.** Re-probe the unfiltered total, compare the final unique key count against the initial total, allow a tolerance band equal to the observed churn (`after - before`), subtract the loss already recorded by named degradations, and record any residual as its own truncation reason. Never book a degradation's own deficit into the residual, or the check becomes tautological. | Must | Done (new) |
| FR-14 | **Never truncate silently, anywhere.** Any paginated fetch that stops short of the reported total — the page-limit clamp, or a full page with no parseable total — records a truncation event carrying the endpoint, the reason, the project and branch, and the total / fetched / lost counts. Events are warned on the console, aggregated into a block printed before the "Extract Complete" line, persisted to `extract_truncation.json` in the extract directory, and rendered as Limitations bullets in the migration report. A deliberate sampling cap (for example the webhook delivery log) is exempt and must not produce a bullet. | Must | Done (new) |

> **FR-4 / FR-5, why open edges are better than an epoch.** An omitted date bound is unbounded, and the resulting split is exact. Measured on `zvec`: `createdBefore=M` alone returned 8,924 and `createdAfter=M` alone returned 5,979, summing to 14,903, which is `paging.total`. With the tool's real parameter set (`componentKeys` + `branch` + `issueStatuses`) at `M=2026-05-01`: 10,269 + 4,393 = 14,662, exactly the filtered total. So the root window sends no date parameters, a split at `M` gives a left child that keeps the parent's open lower edge and a right child that keeps the parent's open upper edge, and only interior seams carry both bounds. That removes, by construction and not by mitigation: the guessed epoch, any call to `time.Now()`, clock skew between the tool's host and the server, the pre-2006 blind spot for issues **this tool itself backdates** when migrating history, and the gap for issues created while a multi-hour run is in flight (they land in the always-open rightmost window). The partition is total, which is what makes FR-13's reconciliation exact rather than approximate.

> **FR-7, why `start == end` is unimplementable.** A zero-width window is rejected: HTTP 400 `"Start bound cannot be larger or equal to end bound"`. The narrowest legal window is one second, and sub-second bounds are also 400s rather than being truncated server-side. Recursion must therefore stop while the window is still one second wide, and the guard must be tested on the **formatted wire strings**, not on `time.Time` values.

### Non-Functional Requirements

| ID | Requirement | Target | Status |
|----|------------|--------|--------|
| NFR-1 | **Zero extra requests** for projects at or under the ceiling, and no `createdAfter` / `createdBefore` on any of their requests | 0 added requests, 0 added latency | Met. The original "single extra probe request" was redundant: page 1 of the ordinary fetch already carries `paging.total`, so the ceiling test is free. |
| NFR-2 | Memory efficiency: stream results through ChunkWriter, don't hold all issues in memory | Peak RSS < 2x single-page size | Partially met. Every window fetch keeps the 20-page limit, so no single fetch exceeds 10,000 items, and each window's chunk is written before the next window is fetched. The literal "< 2x single-page size" is not met — peak is one window plus the dedup key set (~40-60 bytes per key, ~30 MB at 500k issues) — but peak does not grow with project size beyond that key set. |
| NFR-3 | Total API calls minimized: only split windows that exceed the limit | <= 2x theoretical minimum | **NOT MET, and stated rather than dropped.** A `zvec`-shaped project+branch costs roughly **85 requests** against a theoretical minimum near 30 and this target's 60. The breakdown: 1 entry fetch (discarded), 2 creation-date range probes, 2 partition-check probes, ~25 internal-node probes, ~26 leaf probes, the data pages themselves, and 1 closing re-probe. Most of those are one-row `ps=1` probes, but they are still round trips. A coarse first pass (the withdrawn FR-5) is the available lever if field measurements justify it; the reconciliation record persists the request count so the cost is auditable per run. |
| NFR-4 | Deduplication must handle millions of issue keys efficiently | O(n) via map lookup | Met |
| NFR-5 | Date formatting must use SonarQube's expected format | 100% API compatibility | Met. Broader than the original note: the server accepts **only** `yyyy-MM-dd` and `yyyy-MM-ddTHH:mm:ss±hhmm`. It rejects milliseconds (`.000Z`), **a bare `Z`**, `+00:00`, an omitted offset, and a space separator. `time.RFC3339` emits `Z` for UTC and is therefore unusable. The one legal Go layout is `"2006-01-02T15:04:05-0700"` applied to `.UTC()`. Date-only bounds are legal but must never be emitted: they double-count, because `createdBefore=D` means `< D+1day` while `createdAfter=D` means `>= D 00:00` (measured: 14,904 against a true total of 14,903). |
| NFR-6 | Probe requests must not trigger 429 responses during recursive window splitting | Zero 429s under normal operation | Met, with **no dependency on SPEC-016**. That dependency was wrong: SPEC-016's Layer 2 throttles **writes** and is unbuilt, whereas probes are reads and already inherit the shared retry transport in `lib/sq-api-go/retry.go`. |

## Technical Design
<!-- updated: 2026-09-16_10:00:00 -->

### Architecture

The original design called for a standalone generic package, `go/internal/searchslicer/`, exposing `FetchAll[T any]` over `ProbeTotalFunc` / `FetchPageFunc` / `KeyFunc`. That shape was **rejected**, and the reason is the hotspots trap: a generic entry point parameterised by endpoint is precisely the API through which someone aims the slicer at `/api/hotspots/search`. The implemented slicer is unexported, lives in `package extract`, hardcodes the issues endpoint, takes no endpoint parameter and uses no type parameters. There is no call signature through which the mistake can be expressed.

Implemented layout:

```
go/internal/common/
    rawclient.go            # GetPaginatedResult: returns total/fetched/truncated/reason, not just items
    truncation.go           # TruncationReason, TruncationRecord, TruncationTracker, Reconciliation, the JSON artefact
    sqdate.go               # SQDateLayout + FormatSQDate/ParseSQDate, the only date formatter on the slicing path
go/internal/extract/
    slice.go                # issueWindow, windowParams, fetchProjectIssues, sliceProjectIssues, fetchAtomicWindow
    truncation_report.go    # the end-of-run console block
go/internal/report/summary/
    truncation.go           # reads extract_truncation.json, renders Limitations bullets
```

`sqdate.go` is the only date formatter *on the slicing path*, not in the tree. `extract/tasks_misc.go`, `extract/tasks_history.go`, `migrate/tasks_projectdata.go` and `report/common/types.go` each still hold their own copy of the `2006-01-02T15:04:05-0700` layout string, for parsing or for formatting unrelated to windows. Consolidating them is out of scope for #574; what this design guarantees is narrower and is the part that matters here: **no window bound is produced anywhere but `FormatSQDate`**.

Integration point: the issues fetch in `go/internal/extract/tasks_projectdata.go` calls `fetchProjectIssues` instead of `GetPaginated`. The existing parameter block is **inherited, never rebuilt** — window parameters are `CloneParams(base)` followed by setting or deleting the two date keys and nothing else, so `componentKeys` (not `components`, see [#400](https://github.com/SonarSource/sonar-migration-tool/issues/400)), `branch`, the version-gated `issueStatuses` / `statuses` choice and `additionalFields=_all` all survive at every depth.

### Key Algorithms

#### Date-Window Bisection (Pseudocode)

```
// A window is half-open: [start, end).  createdAfter is inclusive (>=),
// createdBefore is exclusive (<).  openStart / openEnd mean the corresponding
// parameter is NOT SENT, which the API treats as unbounded.

function fetchProjectIssues(ctx, params, sink) -> error:
    res := getPaginated(params, pageLimit=20, stopOnTruncation=true)
    if not res.Truncated:
        return sink(res.Items)              // NFR-1: zero extra requests, no date params

    root := window{openStart: true, openEnd: true}
    return sliceProjectIssues(ctx, params, root, res.Total, sink)


function fetchWindow(ctx, params, w, limit, seen, sink) -> error:
    total, ok := probeTotal(ctx, params, w)   // clone + p=1&ps=1 + the date keys; delete nothing
    if not ok:
        return error                          // an unparseable total is an ERROR, never "empty"
    if total == 0:
        return nil                            // genuinely empty window

    if total <= limit or not splittable(w):
        if total > limit:
            record(atomic_window, w, lost = total - limit)   // <- the reserved secondary-axis seam
        items := fetchAll(ctx, windowParams(params, w), pageLimit=20)
        return sink(dedup(items, seen))

    m := mid(w)                               // start + span/2, truncated to the second
    left  := window{start: w.start, end: m, openStart: w.openStart, openEnd: false}
    right := window{start: m, end: w.end, openStart: false, openEnd: w.openEnd}
    // NOTE: right.start == left.end, the SAME time.Time, formatted to the SAME
    // string on both sides.  There is no "+1" of any kind anywhere.
    fetchWindow(ctx, params, left,  limit, seen, sink)
    fetchWindow(ctx, params, right, limit, seen, sink)


function splittable(w) -> bool:
    return w.end.Sub(w.start) > 1 second       // NOT start == end: that is a 400
```

> **Correction — the right window starts at the midpoint, not `midpoint + 1s`.** The original pseudocode advanced the right window's start by one second. Under half-open `[createdAfter, createdBefore)` semantics, `createdBefore=M` and `createdAfter=M` already abut with no overlap and no gap, so the `+1` **drops an entire second of issues at every seam**. CloudVoyager carries the same defect in production as a `+1ms` (commit `e5fbecd0`, added under the mistaken premise that both edges were inclusive); when the midpoint's millisecond component is 999 the increment rolls into the next second and that second's issues are silently lost, at a rate of roughly one seam in a thousand. Do not port the `+1`. The regression test asserts that the left window's `createdBefore` and the right window's `createdAfter` are **byte-identical strings**.

#### Window Construction

There is no window-list builder. The root window carries no bounds at all, and bisection derives every other window from it. Each bound is truncated to the second at construction, and the midpoint is truncated again, so sub-second bounds cannot exist anywhere in the tree. Before any request, the window is checked for legality on its **formatted strings** — open on either side, or `FormatSQDate(start) != FormatSQDate(end)` — because that is the form the server actually parses.

#### SonarQube Date Formatting

```go
const SQDateLayout = "2006-01-02T15:04:05-0700"

func FormatSQDate(t time.Time) string {
    return t.UTC().Format(SQDateLayout)
}
```

The format is not merely "no milliseconds". See NFR-5: a bare `Z`, `+00:00`, a missing offset and a space separator are all HTTP 400, and date-only bounds double-count. The layout above is the only one that is both accepted and unambiguous.

### Data Flow

```
1. The issues task fetches normally, with the existing 20-page clamp and stop-on-truncation.
2. Not truncated (the common case) -> write the items, done.  No date parameter was ever sent.
3. Truncated -> the reported paging.total is the real total; slicing engages.
   a. Two ps=1 probes read the oldest and newest creation dates.  These feed midpoint
      arithmetic ONLY.  A failed or collapsed probe costs extra recursion levels and
      nothing else, because the outer edges stay open regardless.
   b. One depth-0 partition check: if both children report a total >= the parent's, the
      endpoint is ignoring the date parameters.  Warn, record dates_ignored, fall back to
      the clamped result, and return WITHOUT an error.
   c. Recurse.  Each internal node costs one ps=1 probe, not a full pagination.
   d. Each leaf fetches with the 20-page clamp and streams its chunk to the ChunkWriter
      before the next window is fetched.
   e. A window still over the ceiling at one second wide is an atomic window: fetch the
      ceiling's worth, record atomic_window with the exact second and the exact shortfall.
4. Reconcile (FR-13): re-probe the total, compute the churn band, record any residual drift.
5. Truncation events are flushed to extract_truncation.json and printed as a block on stderr
   before the "Extract Complete" line; the migration report renders them as Limitations bullets.
```

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `GET /api/issues/search` | GET | Fetch issues with `createdAfter`, `createdBefore`, `componentKeys`, `branch`, `issueStatuses`, `additionalFields`, `p`, `ps` |

> **`/api/hotspots/search` is deliberately absent from this table.** It declares no date parameters, and it does not reject the ones it does not know: it returns HTTP 200 with an unchanged total. Sending `createdAfter` / `createdBefore` to it produces a recursion that never narrows and never terminates on its own, while every log line claims success. The extract must never send a date parameter to it at any project size; that is an acceptance criterion (AC-7) with a test whose mock handler fails the test if a dated request arrives.

> **On `components` vs `componentKeys`.** The original version of this table named `components`. That parameter is wrong for this tool: on SonarQube Server 9.9 it returns the **global** issue set, and an empty `components=` returns the global total. Use `componentKeys`. See [#400](https://github.com/SonarSource/sonar-migration-tool/issues/400).

### Go Type Definitions

The generic `FetchAll[T any]` / `ProbeTotalFunc` / `FetchPageFunc` / `KeyFunc` / `SlicerConfig` surface described in the original spec was not built, for the reason given under Architecture. The implemented types are concrete and unexported:

```go
// issueWindow is half-open [start, end). openStart/openEnd mean the
// corresponding date parameter is NOT SENT, and an omitted bound is unbounded.
type issueWindow struct {
    start, end         time.Time // arithmetic bounds only, always second-aligned
    openStart, openEnd bool
}

func (w issueWindow) splittable() bool     // end.Sub(start) > time.Second
func (w issueWindow) safeToRequest() bool  // open on either side, or the formatted strings differ
func (w issueWindow) mid() time.Time       // start.Add(span/2).Truncate(time.Second)

// windowParams clones the caller's parameters and sets or deletes ONLY the two
// date keys, so every caller-supplied parameter survives at every depth.
func windowParams(base url.Values, w issueWindow) url.Values
```

The reserved seam for the deferred secondary axis is a single function, `fetchAtomicWindow`, called from exactly one place, whose contract is "return everything the API will give for this window, and record what it will not." Replacing its body is the whole of the follow-up.

### Concurrency Considerations

- Date windows are fetched **sequentially** (not concurrently) to avoid overwhelming the SonarQube Server API with parallel requests. The server is often the bottleneck, and concurrent large queries can cause 429 rate limiting or OOM on the server side. Projects are already walked in parallel one level up, so this is not the parallelism layer.
- Within each window, the standard paginated fetch handles page-level fetching sequentially (existing behavior).
- The `seen` deduplication map and the reconciliation counters are not concurrent-safe and do not need to be, since windows are processed sequentially.

### Error Handling

| Scenario | Behavior |
|----------|----------|
| Probe request fails (network error, 5xx) | Retry via the existing `retryTransport` (3 attempts with backoff) |
| Probe returns an **unparseable** total | Hard error for that window. Never treated as "empty" — a missing or malformed `paging.total` currently parses as 0, which is indistinguishable from a genuinely empty window and would silently drop everything in it. |
| Probe returns 0 with a parseable total | Skip the window (genuinely empty date range) |
| Fetch fails mid-slice | Keep what is already written, record `incomplete_slice` with the unaccounted count, and return the error. Never report the project as cleanly skipped. |
| The endpoint ignores the date parameters | Warn, record `dates_ignored`, fall back to the clamped fetch, and **return nil**. A diagnostic refusal must never abort a multi-hour extract — the existing non-fatal HTTP filter only covers 403/404, so a plain error here would kill the run. |
| Maximum depth reached | Same shape: warn, record `max_depth`, fall back, return nil |
| Window still over the ceiling at one second wide | Fetch the ceiling's worth, record `atomic_window` with the exact second and shortfall, continue |
| Context cancellation | Propagate immediately; stop issuing further requests |

## Acceptance Criteria
<!-- updated: 2026-09-16_10:00:00 -->

- [x] AC-1: Projects at or under 10,000 issues are extracted with **exactly the same number of HTTP requests as before this change**, and no request carries `createdAfter` or `createdBefore`. (Corrected: the original "exactly 1 extra probe request" was unnecessary.)
- [x] AC-2: Projects with **more than** 10,000 issues activate date-window slicing and extract all issues without data loss. A project with exactly 10,000 does **not** slice.
- [x] AC-3: Results are deduplicated by issue key; no duplicate issues appear in the output JSONL.
- [x] AC-4: Log output includes window split decisions, per-window counts, and total issue count.
- [ ] ~~AC-5: The slicer works for both `/api/issues/search` and `/api/hotspots/search` via the generic type parameter.~~ **REMOVED.** Hotspots cannot be date-sliced, and an acceptance test written against this criterion would have passed while the feature did nothing. See the Implementation Status banner.
- [x] AC-6: Dates are formatted as `2006-01-02T15:04:05-0700` on a UTC instant. No `Z`, no `+00:00`, no milliseconds, no date-only bound, no sub-second bound — at any depth.
- [x] AC-7: **No request is ever sent to `/api/hotspots/search` carrying a date parameter**, at any project size.
- [x] AC-8: Unsplittable windows (one second wide, still over the ceiling) fetch the ceiling's worth, record `atomic_window` naming the exact second and the exact shortfall, terminate in a bounded number of requests, and produce a Limitations bullet.
- [x] AC-9: Unit tests cover: small project (no slicing), exactly-10,000 project (no slicing), recursive slicing, atomic window, empty window, unparseable total, mid-slice failure, and an endpoint that ignores date parameters. **Amended on deduplication:** dedup is asserted only in the *negative*. The windows are a half-open exact partition, so the tests assert the recovered key set and a duplicate count of zero; no test forces an overlap, so the dedup-hit branch and its "the partition is not exact" warning are never executed. That is deliberate rather than an oversight — the dedup set is a tripwire for a future partition bug, and a test that manufactures an overlap would assert the tripwire works, not that the slicer is correct. Claiming the hit path as covered would have been false, so the criterion is worded for what the tests actually establish.
- [x] AC-10: Integration test with an `httptest` mock server validates end-to-end extraction of a `zvec`-shaped corpus (14,903 issues, 8,916 of them in one second), asserting the **set** of recovered keys, never the order.
- [x] AC-11: Results are streamed to the ChunkWriter per window during extraction, not accumulated into a single slice for a final write.
- [x] AC-12: **(FR-13)** After a sliced walk the unique count is reconciled against the initial total within the churn band, and the raw before / after / unique / duplicate / expected-loss numbers are persisted so a human can second-guess the verdict.
- [x] AC-13: **(FR-14)** A run in which nothing was truncated writes no `extract_truncation.json`, prints no truncation block, and adds no Limitations bullet. A run that truncated writes all three, with matching counts. Resuming with `--extract_id` merges both attempts' records instead of overwriting.
- [x] AC-14: **(FR-14)** A deliberate sampling cap — the webhook delivery log's 10-page limit — produces no truncation record and no report bullet at any delivery volume.

## CloudVoyager Reference
<!-- updated: 2026-09-16_10:00:00 -->

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

### Defects in the reference implementation — do not port

1. **The `+1ms` on every non-first window start** (commit `e5fbecd0`). Added under the mistaken premise that both date bounds are inclusive; they are not, the window is half-open. When the midpoint's millisecond component is 999 the increment rolls into the next second and a whole second of issues is silently dropped. It is a live, low-rate data-loss path in CloudVoyager, roughly one seam in a thousand, and it is invisible in the logs.
2. **Reusing the issues path verbatim for hotspots.** See the Implementation Status banner: the hotspots endpoint ignores the date parameters and returns 200, so the slicer reports success while recovering nothing new.
3. **No post-slicing reconciliation.** Neither CloudVoyager nor the original version of this spec compared the recovered unique count against the reported total. FR-13 adds it.
4. **`find-date-range.js` probing through the paginator** with `ps=1`, which produced one single-row page per issue and died past the 10,000th. It was deleted in commit `ef740f63`. A single non-paginated `ps=1&p=1` request is safe; a paginated one is not.

### Key Differences from CloudVoyager

1. **Language**: CloudVoyager uses async/await JavaScript; the Go implementation uses context-aware sequential recursion.
2. **No generics, no endpoint parameter**: deliberately narrower than CloudVoyager, so the hotspots mistake is unrepresentable rather than merely documented.
3. **Open outer edges instead of an epoch**: no `2006-01-01` guess, no `time.Now()`, no 12-window first pass.
4. **Paginator reuse**: within-window pagination is delegated to the existing client rather than reimplemented.
5. **Memory model**: results are written to the ChunkWriter per window rather than accumulated.
6. **Reconciliation and a truncation artefact**: the run states, in the log, on disk and in the migration report, exactly how many results it did not get.

## Known Limitations
<!-- updated: 2026-09-16_10:00:00 -->

- **More than 10,000 issues sharing a single creation second is the normal shape of a migrated project, not an exotic edge case.** The original text called it "theoretically impossible". Measured: **8,916 of `zvec`'s 14,903 issues carry the identical timestamp `2025-12-30T03:02:17`**, because a project's first analysis stamps its entire pre-existing backlog with one instant. Any project whose first analysis found more than 10,000 issues has this shape. Date bisection cannot subdivide below one second, so such a window is fetched up to the ceiling and the shortfall is recorded as `atomic_window` with the exact second and the exact count. This change makes that loss precise and visible; it does not yet recover it. See Q3.
- The algorithm uses sequential window processing. Parallel windows would improve throughput on fast instances but risk rate limiting, and the project walk above is already the parallel layer.
- The `createdAfter` / `createdBefore` parameters are based on issue creation date, not update date. Issues created outside a window but updated within it are correctly captured by their creation date.
- Request cost rises sharply on affected projects: roughly 85 requests instead of 20 for a `zvec`-shaped project+branch, most of them one-row probes. See NFR-3.
- `/api/measures/component_tree`, `/api/hotspots/search` and `/api/project_analyses/search` are not sliced. They get the FR-14 warning and a report bullet only. The component tree's natural partition is the component subtree, which is a different algorithm with a much wider blast radius (source, SCM blame and measures all hang off it).
- SonarQube Server versions prior to 7.x may not support the `createdAfter` / `createdBefore` parameters. The tool requires SonarQube Server 9.9+ (see system requirements).

## Open Questions
<!-- updated: 2026-09-16_10:00:00 -->

- **Q1**: Should we support concurrent window fetching behind a feature flag for high-throughput SonarQube Server instances? Still open, but low priority: the project walk is already parallel, and sequential windows keep the dedup set and the reconciliation counters lock-free.
- **Q2**: Should the initial window count be configurable? **Moot** — there is no initial window count any more (FR-5 superseded). A coarse first pass is available as a pure optimisation if NFR-3's measured cost proves unacceptable in the field.
- **Q3**: For the unsplittable window, should we attempt secondary-dimension slicing before falling back to a direct fetch? **Answered below.**
- **Q4**: Should the slicer emit structured progress events for the wizard UI? Not doing (FR-12): the progress tracker counts projects, not sub-project work.

### Q3 — answered, with measurements
<!-- updated: 2026-09-16_10:00:00 -->

Measured inside `zvec`'s dense second (8,916 issues, `2025-12-30T03:02:17`), composed with `componentKeys`, `branch` and the one-second date window:

- **`types` and `severities` are each an exact partition of the window.** `types`: 8,779 + 72 + 65 = 8,916. `severities`: 65 + 2,719 + 3,730 + 2,247 + 155 = 8,916.
- **The 15-cell `types` x `severities` cross product also sums to 8,916, with zero delta.** The filters genuinely partition; they do not merely produce facet counts that happen to add up.
- **The dominant cell is `CODE_SMELL` x `MAJOR` at 3,645, which is 40.9% of the window.** So the cross product runs out at roughly **24,500 issues in a single second** — one doubling of headroom, not an order of magnitude.
- **`rules` cannot serve as a third axis.** The `rules` facet listing in that same second returned **100 distinct values** — the facet listing is itself capped at 100 — summing to 8,742 of 8,916. You cannot enumerate the partition you would need to walk.

Conclusion: `types` x `severities` is worth building, and it is deferred to a follow-up rather than rejected. It is deferred because the engaged path cannot be exercised against any real server available to us (no project here has more than 10,000 issues in one second, so it is provable only against a mock), because the headroom it buys is finite and measurable, and because the base case above ~24,500 issues per second remains unsolved. The seam is reserved at `fetchAtomicWindow`, and every `atomic_window` record written by this change carries the exact per-second count, second and project — which is the field distribution nobody currently has, and the data the follow-up should be designed against.

**The better long-term axis is the component axis**, not facets: `/api/issues/search` accepts `files` / `componentKeys`, a project's files are enumerable, and the partition is unbounded in a way that a fixed 15-cell cross product is not. It is a larger change, and it should be the follow-up's second option rather than its first.

## References
<!-- updated: 2026-09-16_10:00:00 -->

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt
