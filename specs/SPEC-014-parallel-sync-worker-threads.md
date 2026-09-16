---
spec_id: SPEC-014
title: Parallel Sync & Worker Goroutines
status: draft
priority: P1
epic: "Performance"
depends_on: [SPEC-008, SPEC-016]
estimated_effort: L
cloudvoyager_ref: "src/shared/utils/parallel-sync/"
---

# SPEC-014: Parallel Sync & Worker Goroutines
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

Large SonarQube Server instances can contain hundreds of thousands of issues across thousands of projects. When syncing issue metadata (matching source issues to target issues, updating statuses, transitions, and comments), the volume of API calls required can make sequential processing prohibitively slow. CloudVoyager addresses this with a worker thread pool that distributes issue pairs across multiple Node.js worker threads, each running its own bounded concurrency limiter, achieving up to 100 concurrent API calls for high-volume syncs.

In Go, this pattern maps naturally to goroutine worker pools with channels. Go's concurrency primitives (goroutines, channels, `sync.WaitGroup`, `golang.org/x/sync/errgroup`) are significantly more efficient than Node.js worker threads because goroutines share memory directly without serialization overhead, and the Go scheduler multiplexes thousands of goroutines across a small number of OS threads. This spec defines the Go implementation of the parallel sync system, including the worker pool architecture, partition strategy, per-worker resilience, result aggregation, and the threshold-based activation that avoids pool overhead for small workloads.

The sonar-migration-tool already has concurrency control infrastructure and retry with exponential backoff in `lib/sq-api-go/retry.go`. This spec builds on that foundation by adding a higher-level worker pool abstraction specifically designed for bulk issue metadata synchronization.

## Problem Statement

Synchronizing issue metadata between SQ Server and SonarQube Cloud involves per-issue API calls (status transitions, comment creation, resolution updates). For a project with 10,000 issues, sequential processing at 200ms per API call would take over 30 minutes. Even with basic concurrency, the tool needs an intelligent worker pool that distributes work evenly, handles per-request failures gracefully (without aborting the entire sync), and aggregates results for reporting.

The current sonar-migration-tool has no dedicated sync worker pool. The existing `--concurrency` flag controls general task parallelism but does not provide the per-issue resilience and round-robin partitioning needed for high-volume metadata sync.

## User Stories

- **As a** migration operator syncing 50,000 issues, **I want to** have the sync complete in minutes rather than hours, **so that** the migration does not block my team's cutover timeline.
- **As a** migration operator, **I want to** see real-time progress during sync (X/Y issues synced, N failures), **so that** I can monitor the migration and intervene if the failure rate is too high.
- **As a** migration operator with a rate-limited SonarQube Cloud instance, **I want to** configure the worker count and per-worker concurrency, **so that** I can balance speed against API rate limits.
- **As a** migration operator, **I want to** have individual issue sync failures not abort the entire sync, **so that** one bad issue does not prevent the other 49,999 from syncing.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Implement a goroutine worker pool for bulk issue metadata synchronization | Must |
| FR-2 | Activate worker pool only when issue pair count >= 500 (threshold configurable) | Must |
| FR-3 | For < 500 pairs, use simple bounded concurrency via `errgroup.SetLimit()` | Must |
| FR-4 | Default worker count: `GOMAXPROCS` (matching SPEC-015's auto-tune formula for issueSync). With 5 concurrent API calls per worker, `GOMAXPROCS` workers yields `GOMAXPROCS * 5` concurrent requests, which is a reasonable default. The previous `GOMAXPROCS * 3` was too aggressive (e.g., 48 workers x 5 = 240 concurrent requests). | Must |
| FR-5 | Default per-worker concurrency: 5 concurrent API calls | Must |
| FR-6 | Distribute issue pairs across workers using round-robin partitioning | Must |
| FR-7 | Each worker retries on HTTP 429 and transient 5xx errors with exponential backoff: 1s, 2s, 4s (3 retries) | Must |
| FR-8 | Individual issue sync failures do not abort the worker or the pool (settled/best-effort mode) | Must |
| FR-9 | Aggregate results from all workers: success count, failure count, skipped count | Must |
| FR-10 | Log per-worker completion: "Worker N: processed X/Y pairs (Z failures)" | Should |
| FR-11 | Log overall sync summary: "Sync complete: X succeeded, Y failed, Z skipped out of W total" | Must |
| FR-12 | Support graceful shutdown via context cancellation (e.g., SIGINT during sync) | Must |
| FR-13 | Support `--sync-workers` CLI flag to override default worker count | Should |
| FR-14 | Support `--sync-concurrency` CLI flag to override per-worker concurrency | Should |
| FR-15 | Emit progress updates at configurable intervals (default: every 100 completed pairs) | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Throughput for 10,000 issue pairs | < 5 minutes with default settings |
| NFR-2 | Memory overhead per worker | < 1 MB (goroutines are ~8 KB stack each) |
| NFR-3 | Pool startup time | < 100ms for 60 workers |
| NFR-4 | Graceful shutdown latency | < 5 seconds after context cancellation |
| NFR-5 | Zero data races | Verified by `go test -race` |
| NFR-6 | CPU utilization | < 10% when I/O-bound (most time should be waiting for API responses) |

## Technical Design

### Architecture

```
go/internal/syncpool/       # Named 'syncpool' to avoid collision with Go's stdlib 'sync' package
├── pool.go              # SyncWorkerPool: orchestrates workers, partitions, aggregation
├── pool_test.go         # Pool tests with mock API
├── worker.go            # Single worker: processes its partition with bounded concurrency
├── worker_test.go       # Worker tests
├── partition.go         # Round-robin partitioning algorithm
├── partition_test.go    # Partition distribution tests
├── result.go            # SyncResult: success/failure/skipped aggregation
└── result_test.go       # Result aggregation tests
```

### Key Algorithms

#### Worker Pool Orchestration

```go
package syncpool

import (
    "context"
    "runtime"
    "sync"
    "sync/atomic"
)

// SyncWorkerPool distributes issue pair synchronization across multiple
// goroutine workers, each with its own bounded concurrency limiter.
type SyncWorkerPool struct {
    workers       int            // Number of worker goroutines
    perWorker     int            // Concurrent API calls per worker
    threshold     int            // Minimum pairs to activate pool (default: 500)
    progressEvery int            // Log progress every N completed pairs
    results       chan WorkerResult
}

// DefaultPool returns a pool configured for the current system.
func DefaultPool() *SyncWorkerPool {
    cpus := runtime.GOMAXPROCS(0)
    return &SyncWorkerPool{
        workers:       cpus,
        perWorker:     5,
        threshold:     500,
        progressEvery: 100,
    }
}

// Run synchronizes all issue pairs using the worker pool if above
// threshold, or simple bounded concurrency otherwise.
func (p *SyncWorkerPool) Run(ctx context.Context, pairs []IssuePair, syncFn SyncFunc) (AggregateResult, error) {
    if len(pairs) < p.threshold {
        return p.runSimple(ctx, pairs, syncFn)
    }
    return p.runPooled(ctx, pairs, syncFn)
}
```

#### Round-Robin Partitioning

```
FUNCTION Partition(pairs []IssuePair, numWorkers int) [][]IssuePair:
    partitions = make([][]IssuePair, numWorkers)
    
    // Pre-allocate slices to avoid repeated growth
    perPartition = len(pairs) / numWorkers
    FOR i = 0; i < numWorkers; i++:
        partitions[i] = make([]IssuePair, 0, perPartition+1)
    
    // Round-robin distribution ensures even spread
    // This prevents clustering of heavy issues (e.g., issues with
    // many comments from the same project) in a single worker
    FOR i, pair IN enumerate(pairs):
        workerIdx = i % numWorkers
        partitions[workerIdx] = append(partitions[workerIdx], pair)
    
    RETURN partitions
```

The round-robin approach is intentionally chosen over simple chunking (first N to worker 1, next N to worker 2, etc.) because issues from the same project tend to be adjacent in the input list. Simple chunking would cluster all issues from large projects into a single worker, creating hotspots. Round-robin interleaves them, distributing the load evenly.

#### Worker Execution

```
FUNCTION RunWorker(ctx context.Context, id int, partition []IssuePair, 
                   perWorker int, syncFn SyncFunc) WorkerResult:
    sem = make(chan struct{}, perWorker)   // Bounded concurrency semaphore
    var wg sync.WaitGroup
    
    result = WorkerResult{WorkerID: id}
    var succeeded, failed, skipped atomic.Int64
    
    FOR EACH pair IN partition:
        // Check for cancellation before starting new work
        SELECT:
            CASE <-ctx.Done():
                skipped.Add(int64(remaining))
                BREAK
            CASE sem <- struct{}{}:
                // Acquired semaphore slot
        
        wg.Add(1)
        GO FUNC(pair):
            DEFER wg.Done()
            DEFER <-sem   // Release semaphore slot
            
            err = syncWithRetry(ctx, pair, syncFn)
            IF err == nil:
                succeeded.Add(1)
            ELSE IF errors.Is(err, ErrSkipped):
                skipped.Add(1)
            ELSE:
                failed.Add(1)
                log.Warn("Worker %d: failed to sync issue %s: %v", id, pair.SourceKey, err)
    
    wg.Wait()
    
    result.Succeeded = succeeded.Load()
    result.Failed = failed.Load()
    result.Skipped = skipped.Load()
    
    log.Info("Worker %d: processed %d/%d pairs (%d failures)",
        id, result.Succeeded+result.Failed, len(partition), result.Failed)
    
    RETURN result
```

#### Per-Request Retry with Backoff

```
FUNCTION syncWithRetry(ctx context.Context, pair IssuePair, syncFn SyncFunc) error:
    backoff = [1s, 2s, 4s]
    maxRetries = 3
    
    FOR attempt = 0; attempt <= maxRetries; attempt++:
        err = syncFn(ctx, pair)
        
        IF err == nil:
            RETURN nil
        
        IF !isRetryable(err):
            RETURN err   // Client error, no retry
        
        IF attempt < maxRetries:
            delay = backoff[attempt]
            // Add jitter: 0-50% of delay
            delay += randomDuration(0, delay/2)
            
            SELECT:
                CASE <-ctx.Done():
                    RETURN ctx.Err()
                CASE <-time.After(delay):
                    log.Debug("Retrying sync for %s (attempt %d/%d)", 
                        pair.SourceKey, attempt+2, maxRetries+1)
    
    RETURN fmt.Errorf("sync failed after %d attempts: %w", maxRetries+1, err)
```

#### Result Aggregation

```
FUNCTION AggregateResults(workerResults []WorkerResult) AggregateResult:
    agg = AggregateResult{}
    
    FOR EACH wr IN workerResults:
        agg.Succeeded += wr.Succeeded
        agg.Failed += wr.Failed
        agg.Skipped += wr.Skipped
        agg.FailedPairs = append(agg.FailedPairs, wr.FailedPairs...)
    
    agg.Total = agg.Succeeded + agg.Failed + agg.Skipped
    agg.Duration = time.Since(startTime)
    agg.Throughput = float64(agg.Total) / agg.Duration.Seconds()
    
    log.Info("Sync complete: %d succeeded, %d failed, %d skipped out of %d total (%.1f pairs/sec)",
        agg.Succeeded, agg.Failed, agg.Skipped, agg.Total, agg.Throughput)
    
    RETURN agg
```

### Data Flow

1. **Input**: List of `IssuePair` structs, each pairing a source issue (from SQ Server) with a target issue (in SQ Cloud).
2. **Threshold Check**: If `len(pairs) < 500`, route to simple bounded concurrency. Otherwise, activate worker pool.
3. **Partitioning**: Round-robin distribute pairs across `N` worker goroutines.
4. **Worker Execution**: Each worker processes its partition with bounded concurrency (`perWorker` simultaneous API calls). Each API call has its own retry loop.
5. **Progress Reporting**: A dedicated progress goroutine receives completion signals and logs every `progressEvery` completions.
6. **Aggregation**: Worker results are collected via channels and aggregated into a summary.
7. **Output**: `AggregateResult` with counts, duration, throughput, and list of failed pairs for optional retry or reporting.

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/issues/do_transition` | POST | Transition issue status (per-issue call) |
| `/api/issues/add_comment` | POST | Add comment to issue (per-issue call) |
| `/api/issues/set_severity` | POST | Update issue severity (per-issue call) |
| `/api/issues/assign` | POST | Assign issue to user (per-issue call) |
| `/api/issues/set_tags` | POST | Update issue tags (per-issue call) |

## Acceptance Criteria

- [ ] AC-1: Worker pool activates for >= 500 issue pairs and uses simple concurrency for < 500
- [ ] AC-2: Round-robin partitioning distributes N pairs across W workers with max 1 difference in partition sizes
- [ ] AC-3: Default worker count is `GOMAXPROCS(0)`; default per-worker concurrency is 5
- [ ] AC-4: Individual issue sync failure does not abort the worker or the pool
- [ ] AC-5: Transient errors (429, 503) trigger retry with exponential backoff (1s, 2s, 4s)
- [ ] AC-6: Client errors (400, 401, 403, 404) are not retried
- [ ] AC-7: Context cancellation (SIGINT) causes all workers to drain gracefully within 5 seconds
- [ ] AC-8: Aggregate result correctly sums succeeded + failed + skipped = total across all workers
- [ ] AC-9: Progress logged every 100 completed pairs (configurable)
- [ ] AC-10: Per-worker completion logged at INFO level
- [ ] AC-11: Overall sync summary logged at INFO level with throughput (pairs/sec)
- [ ] AC-12: `go test -race` passes with zero data races on all pool tests
- [ ] AC-13: 10,000 mock issue pairs sync in < 10 seconds with 20 workers (mock API with 10ms latency)
- [ ] AC-14: `--sync-workers` and `--sync-concurrency` CLI flags override defaults

## CloudVoyager Reference

| Area | Path |
|------|------|
| Parallel sync utility | `src/shared/utils/parallel-sync/` |
| Worker thread pool | `src/shared/utils/parallel-sync/worker-pool.js` |
| Worker implementation | `src/shared/utils/parallel-sync/sync-worker.js` |
| Round-robin partitioning | `src/shared/utils/parallel-sync/partitioner.js` |
| Result aggregation | `src/shared/utils/parallel-sync/result-collector.js` |

## Known Limitations

- The worker pool is designed for I/O-bound API call workloads. For CPU-bound operations (e.g., protobuf encoding), `GOMAXPROCS * 3` workers would oversubscribe the CPU. However, since sync is exclusively I/O-bound, this is not a concern for this spec.
- The round-robin partitioning does not consider individual issue complexity (e.g., an issue with 50 comments to sync vs one with 0). A more sophisticated approach would use weighted partitioning, but this adds complexity with marginal benefit since comment counts are typically normally distributed.
- The 500-pair threshold is a heuristic derived from CloudVoyager's empirical testing. The overhead of pool setup in Go is much lower than in Node.js (no thread creation, no serialization), so the threshold could potentially be lower. However, 500 provides a safe margin.
- Per-worker retry is independent of the global retry in `lib/sq-api-go/retry.go`. This means a request could be retried up to 4 times at the worker level (1 initial + 3 retries) and up to 4 times at the HTTP transport level (1 initial + 3 retries), for a maximum of 16 total HTTP requests per issue pair. This is intentional: the transport-level retry handles transient network errors, while the worker-level retry handles application-level errors (429 rate limiting).
- Memory usage scales linearly with the number of issue pairs held in partitions. For extremely large syncs (1M+ pairs), consider streaming partitions from disk instead of holding all pairs in memory.

### Race Condition Analysis

- Work items distributed via channel -- no shared iteration
- Each worker goroutine processes independently -- no shared mutable state
- Results collected via buffered channel -- safe concurrent writes
- errgroup manages goroutine lifecycle -- clean shutdown
- Retry counters are per-worker, not shared -- no atomic needed
- The aggregated SyncStats must be collected safely: each worker returns its own stats, aggregated after all complete (no concurrent accumulation)

## Sonar Documentation Reference

For full Sonar product documentation, see: https://docs.sonarsource.com/llms.txt

## Open Questions

- Q1: Should the worker pool support dynamic worker scaling based on observed API response times? If responses are consistently fast, add workers; if 429s are frequent, reduce workers.
- Q2: Should failed pairs be written to a retry file that can be re-processed in a subsequent run, rather than requiring the operator to re-run the entire sync?
- Q3: Is the 500-pair threshold appropriate for Go, or should it be lower given that goroutine overhead is negligible compared to Node.js worker threads?
- Q4: Should the pool integrate with SPEC-016's rate limiter (token bucket) at the pool level, in addition to per-worker retry? This would provide proactive rate limiting rather than reactive 429 handling.
