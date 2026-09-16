---
spec_id: SPEC-007
title: "Issue Batch Distribution (ES Visibility Optimization)"
status: deprecated
priority: P0
epic: "Scale & Reliability"
depends_on: [SPEC-001, SPEC-002]
estimated_effort: L
cloudvoyager_ref: "src/shared/utils/batch-distributor/"
---

# SPEC-007: Issue Batch Distribution (ES Visibility Optimization)
<!-- updated: 2026-05-26_01:00:00 -->

## Overview
<!-- updated: 2026-05-26_01:00:00 -->

> **DEPRECATED:** Multi-analysis batching was disabled in CloudVoyager because SonarQube Cloud's CE closes issues from prior analyses when a new analysis arrives, causing data corruption. The recommended approach is single-analysis with changeset backdating (SPEC-004). This spec is retained as informational reference only. Implementation is opt-in via `--batch-issues` flag and NOT recommended for production use.

SonarQube Cloud's UI is backed by Elasticsearch, which enforces a visibility limit per date bucket. When more than 5,000 issues land in a single analysis, the Elasticsearch index cannot surface all of them in the UI, causing issues to appear "missing" even though they exist in the database. This is a presentation-layer limitation, not a storage limitation, but it severely undermines user confidence in migration completeness.

This spec defines an automatic batch distribution system that detects when a project's issue count exceeds 5,000 and splits the scanner report submission into multiple sequential batches. Each batch receives a unique `scmRevisionId` (to prevent CE duplicate rejection), a synthetically backdated `analysis_date` (to spread issues across date buckets), and optimized payloads for non-final batches (stripped of sources, changesets, and duplications to reduce upload size by ~70%).

The Go implementation leverages the existing `scanreport` package's `BuildMetadata`, `PackageReport`, `SubmitReport`, and `PollCETask` functions. The batch distribution logic is a new orchestration layer that sits between the issue extraction output and the report submission step, transparently handling the split when needed.

**Important caveat from CloudVoyager**: Multi-analysis batching was ultimately **disabled** in CloudVoyager because the CE's issue tracker closes issues from prior analyses when a new analysis arrives. Instead, CloudVoyager uses `BackdateChangesets()` to spread creation dates within a **single** analysis via SCM blame data. The Go codebase already implements `BackdateChangesets` in `go/internal/scanreport/backdate.go`. This spec documents the batch distribution approach for completeness and as a potential opt-in strategy, but the **default behavior** should be single-analysis with changeset backdating (matching CloudVoyager's final design).

## Problem Statement
<!-- updated: 2026-05-26_01:00:00 -->

When migrating projects with more than 5,000 issues to SonarQube Cloud, all issues submitted in a single analysis land in the same Elasticsearch date bucket. SonarQube Cloud's UI pagination and facet queries are bounded by Elasticsearch's `index.max_result_window`, causing issues beyond the 5,000-issue threshold to be invisible in the web interface. Users see fewer issues than expected and may conclude the migration failed.

Additionally, submitting a very large scanner report (100K+ issues) in a single CE task can cause the Compute Engine to time out or consume excessive memory, leading to task failures that require manual intervention.

## User Stories
<!-- updated: 2026-05-26_01:00:00 -->

- **As a** migration operator, **I want** all migrated issues to be visible in SonarQube Cloud's UI after migration, **so that** I can verify completeness and trust the migration results.
- **As a** migration operator, **I want** the tool to automatically handle large projects without manual intervention, **so that** I don't need to understand SonarQube Cloud's internal ES limitations.
- **As a** migration operator, **I want to** see progress for each batch submission and CE task completion, **so that** I can monitor multi-hour migrations of very large projects.
- **As a** migration operator, **I want** the option to disable batch distribution and use single-analysis changeset backdating, **so that** I can choose the approach that best fits my environment.

## Requirements
<!-- updated: 2026-05-26_01:00:00 -->

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Count total issues for a project after extraction; if <= 5,000, submit as single analysis (existing flow) | Must |
| FR-2 | If > 5,000 issues and batch mode enabled, compute a batch plan splitting into chunks of 5,000 | Must |
| FR-3 | Each batch gets a unique `scmRevisionId` (random hex) to prevent CE duplicate rejection | Must |
| FR-4 | Backdate `analysis_date` for non-final batches: oldest batch gets the earliest synthetic date, final batch gets the real analysis date | Must |
| FR-5 | Non-final batches strip `Sources`, `Changesets`, and `Duplications` from the scanner report ZIP (~70% payload reduction) | Must |
| FR-6 | Final batch includes full source code, changesets, and duplications for the most recent state | Must |
| FR-7 | Batches are submitted sequentially; each batch waits for CE task completion (SUCCESS) before submitting the next | Must |
| FR-8 | If any batch's CE task fails, abort remaining batches and report the error | Must |
| FR-9 | Default mode is single-analysis with changeset backdating (not batch distribution) | Must |
| FR-10 | Batch distribution is opt-in via `--batch-issues` flag or config file `batchIssues: true` | Must |
| FR-11 | Log batch plan (count, sizes, dates) before starting submission | Must |
| FR-12 | Emit per-batch progress events: "Batch 3/7: submitting...", "Batch 3/7: CE task AX_abc pending...", "Batch 3/7: SUCCESS" | Should |
| FR-13 | Issues within each batch are ordered by component key to keep file-level grouping intact | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Batch submission overhead: < 30 seconds per batch for CE task polling setup | Measured |
| NFR-2 | CE task polling interval: 5 seconds (matching existing `PollCETask`) | Fixed |
| NFR-3 | Memory: only one batch's `ReportData` in memory at a time | Peak RSS proportional to single batch |
| NFR-4 | Non-final batch ZIP size: ~30% of full batch due to stripping sources/changesets | Measured |
| NFR-5 | Total migration time for 100K issues: < 2 hours (assuming healthy CE) | Target |

## Technical Design
<!-- updated: 2026-05-26_01:00:00 -->

### Architecture

The batch distributor is implemented as a new file `go/internal/scanreport/batch.go` within the existing `scanreport` package. It orchestrates multiple calls to the existing `PackageReport` and `SubmitReport` functions.

```
go/internal/scanreport/
    batch.go           # NEW: BatchPlan, ComputeBatchPlan, SubmitBatched
    batch_test.go      # NEW: Unit tests
    backdate.go        # EXISTING: BackdateChangesets (default single-analysis path)
    builder.go         # EXISTING: BuildMetadata, BuildComponents, BuildIssues
    packager.go        # EXISTING: PackageReport
    submit.go          # EXISTING: SubmitReport, PollCETask
```

### Key Algorithms

#### Batch Plan Computation

```go
const (
    DefaultBatchSize      = 5000
    DaysBetweenBatches    = 30
)

// BatchDescriptor describes a single batch within a batch plan.
type BatchDescriptor struct {
    Index      int       // 0-based batch index
    StartIdx   int       // Start index into the sorted issue slice (inclusive)
    EndIdx     int       // End index into the sorted issue slice (exclusive)
    IsLast     bool      // True for the final batch
    AnalysisDate time.Time // Synthetic or real analysis date
    ScmRevisionId string  // Unique per batch
}

// BatchPlan holds the complete plan for batched submission.
type BatchPlan struct {
    Batches    []BatchDescriptor
    TotalIssues int
    BatchSize  int
}

func ComputeBatchPlan(totalIssues int, realAnalysisDate time.Time) *BatchPlan {
    if totalIssues <= DefaultBatchSize {
        return nil // no batching needed
    }

    batchCount := (totalIssues + DefaultBatchSize - 1) / DefaultBatchSize
    batches := make([]BatchDescriptor, batchCount)

    for i := 0; i < batchCount; i++ {
        startIdx := i * DefaultBatchSize
        endIdx := min((i+1)*DefaultBatchSize, totalIssues)
        isLast := i == batchCount-1

        analysisDate := computeBatchDate(realAnalysisDate, i, batchCount)

        batches[i] = BatchDescriptor{
            Index:         i,
            StartIdx:      startIdx,
            EndIdx:        endIdx,
            IsLast:        isLast,
            AnalysisDate:  analysisDate,
            ScmRevisionId: randomHex(20),
        }
    }

    return &BatchPlan{Batches: batches, TotalIssues: totalIssues, BatchSize: DefaultBatchSize}
}
```

#### Batch Date Computation

```go
func computeBatchDate(realDate time.Time, batchIndex, totalBatches int) time.Time {
    if totalBatches <= 1 {
        return realDate
    }
    // Last batch gets the real date; earlier batches are backdated
    daysBack := (totalBatches - 1 - batchIndex) * DaysBetweenBatches
    return realDate.AddDate(0, 0, -daysBack)
}
```

Example for 25,000 issues (5 batches) with real date 2026-05-26:
| Batch | Issues | Analysis Date | Days Back |
|-------|--------|--------------|-----------|
| 0 | 1-5000 | 2026-01-26 | 120 |
| 1 | 5001-10000 | 2026-02-25 | 90 |
| 2 | 10001-15000 | 2026-03-27 | 60 |
| 3 | 15001-20000 | 2026-04-26 | 30 |
| 4 (final) | 20001-25000 | 2026-05-26 | 0 |

#### Batched Submission Orchestration

```
function SubmitBatched(ctx, client, cfg, reportData, plan) -> error:
    sort issues by component key (keeps file grouping)

    for each batch in plan.Batches:
        log.Info("Batch %d/%d: preparing %d issues (date: %s)",
            batch.Index+1, len(plan.Batches), batch.EndIdx-batch.StartIdx, batch.AnalysisDate)

        batchReport := cloneReportForBatch(reportData, batch)
        zipBytes := PackageReport(batchReport)

        result := SubmitReport(ctx, client, cfg, zipBytes)
        log.Info("Batch %d/%d: CE task %s submitted", batch.Index+1, len(plan.Batches), result.TaskID)

        err := PollCETask(ctx, client, cfg.CloudURL, result.TaskID, logger)
        if err != nil:
            return fmt.Errorf("batch %d/%d CE task failed: %w", batch.Index+1, len(plan.Batches), err)

        log.Info("Batch %d/%d: CE task SUCCESS", batch.Index+1, len(plan.Batches))

    return nil
```

#### Report Cloning for Batches

```go
func cloneReportForBatch(original *ReportData, batch BatchDescriptor) *ReportData {
    clone := &ReportData{
        Metadata:       cloneMetadata(original.Metadata, batch),
        RootComponent:  original.RootComponent,
        FileComponents: original.FileComponents,
        ActiveRules:    original.ActiveRules,
        AdHocRules:     original.AdHocRules,
    }

    // Slice issues for this batch
    clone.Issues = sliceIssuesByBatch(original.Issues, batch)

    if batch.IsLast {
        // Final batch: include full sources, changesets, duplications
        clone.Sources    = original.Sources
        clone.Changesets = original.Changesets
        clone.Measures   = original.Measures
    } else {
        // Non-final batch: strip heavy payload
        clone.Sources    = nil
        clone.Changesets = nil
        clone.Measures   = nil
    }

    return clone
}

func cloneMetadata(md *pb.Metadata, batch BatchDescriptor) *pb.Metadata {
    clone := proto.Clone(md).(*pb.Metadata)
    clone.AnalysisDate = batch.AnalysisDate.UnixMilli()
    clone.ScmRevisionId = batch.ScmRevisionId
    return clone
}
```

### Data Flow

```
1. Extract phase produces JSONL files with all issues (SPEC-006 ensures completeness)
2. Scanner report builder reads issues, builds ReportData
3. Batch distributor checks: len(issues) > DefaultBatchSize && batchMode enabled?
   a. YES (batch mode): ComputeBatchPlan() -> SubmitBatched() -> sequential CE submissions
   b. NO (default): BackdateChangesets() -> single PackageReport() -> single SubmitReport()
4. Each batch submission:
   a. Clone ReportData with batch slice
   b. Override metadata (analysisDate, scmRevisionId)
   c. Strip non-essential data for non-final batches
   d. PackageReport() -> ZIP bytes
   e. SubmitReport() -> CE task ID
   f. PollCETask() -> wait for SUCCESS
5. After all batches complete, proceed to metadata sync (SPEC-008)
```

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/ce/submit` | POST | Submit scanner report ZIP (multipart form) |
| `/api/ce/task` | GET | Poll CE task status by task ID |
| `/api/issues/search` | GET | Count issues for batch threshold detection |

### Configuration

```json
{
  "batchIssues": false,
  "batchSize": 5000,
  "daysBetweenBatches": 30
}
```

CLI flags:
- `--batch-issues` (bool, default false): Enable multi-analysis batch distribution
- `--batch-size` (int, default 5000): Number of issues per batch
- `--batch-interval-days` (int, default 30): Days between synthetic analysis dates

## Acceptance Criteria
<!-- updated: 2026-05-26_01:00:00 -->

- [ ] AC-1: Projects with <= 5,000 issues use the single-analysis path by default (no batching).
- [ ] AC-2: Projects with > 5,000 issues and `--batch-issues` enabled split into ceil(N/5000) batches.
- [ ] AC-3: Each batch has a unique `scmRevisionId` that differs from all other batches.
- [ ] AC-4: Non-final batches produce ZIP files ~70% smaller than full batches (no sources/changesets).
- [ ] AC-5: Final batch includes full sources, changesets, and duplications.
- [ ] AC-6: Batch analysis dates are evenly distributed with 30-day intervals, with the final batch using the real date.
- [ ] AC-7: Batches are submitted sequentially; batch N+1 does not start until batch N's CE task reports SUCCESS.
- [ ] AC-8: If batch N's CE task fails, batches N+1..M are not submitted and a clear error is reported.
- [ ] AC-9: Default behavior (no `--batch-issues` flag) uses single-analysis with `BackdateChangesets`.
- [ ] AC-10: Unit tests cover: batch plan computation, date computation, report cloning, non-final stripping.
- [ ] AC-11: Integration test with httptest mock CE validates sequential submission and polling.

## CloudVoyager Reference
<!-- updated: 2026-05-26_01:00:00 -->

| Area | Path |
|------|------|
| Batch distributor entry | `src/shared/utils/batch-distributor/index.js` |
| Should-batch threshold | `src/shared/utils/batch-distributor/helpers/should-batch.js` |
| Batch plan computation | `src/shared/utils/batch-distributor/helpers/compute-batch-plan.js` |
| Batch date computation | `src/shared/utils/batch-distributor/helpers/compute-batch-date.js` |
| Batch data cloning | `src/shared/utils/batch-distributor/helpers/create-batch-extracted-data.js` |
| Changeset backdating | `src/shared/utils/batch-distributor/helpers/backdate-changesets.js` |
| Transfer with batching | `src/pipelines/sq-9.9/transfer-pipeline/helpers/transfer-branch-batched.js` |

### Key Differences from CloudVoyager

1. **Batch distribution is disabled by default** in both CloudVoyager (returned `false` from `shouldBatch()`) and this Go implementation. The default path is single-analysis with changeset backdating.
2. **Go implementation** leverages the existing `BackdateChangesets()` in `go/internal/scanreport/backdate.go` which already handles the `issueBatchSize = 5000` safety split within a single analysis.
3. **The batch distribution path** is provided as an opt-in alternative for environments where the changeset backdating approach encounters CE limitations.
4. **Protobuf cloning** in Go uses `proto.Clone()` instead of JavaScript spread/Object.assign.

## Known Limitations
<!-- updated: 2026-05-26_01:00:00 -->

- Multi-analysis batching can cause the CE's issue tracker to close issues from prior analyses when the next analysis arrives. This is why CloudVoyager disabled this approach. The single-analysis changeset backdating path (default) avoids this issue.
- If CE task processing takes longer than the context timeout, the batch will appear to fail even if it eventually succeeds. The tool does not currently implement CE task recovery.
- Non-final batches lack source code, so code snippets in SonarQube Cloud's issue detail view will only show for issues in the final batch until the next real analysis.
- The 30-day interval between batch dates is hardcoded in CloudVoyager. Configuring a shorter interval risks hitting ES bucket density limits; a longer interval spreads data across more date facets.

## Open Questions
<!-- updated: 2026-05-26_01:00:00 -->

- **Q1**: Should batch distribution be removed entirely (matching CloudVoyager's `shouldBatch() -> false`) or kept as opt-in? Current design keeps it opt-in for flexibility.
- **Q2**: If CE task polling times out, should the tool retry the poll (not the submission) with a fresh timeout? The CE task may still be processing.
- **Q3**: Should we implement a "resume" capability that detects previously completed batches (by `scmRevisionId` in CE activity) and skips them on retry?
- **Q4**: Is the 5,000 threshold still correct for current SonarQube Cloud versions, or has the ES visibility limit been raised?

## References
<!-- updated: 2026-05-26_01:00:00 -->

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt
