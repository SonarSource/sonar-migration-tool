---
spec_id: SPEC-016
title: Rate Limiting & API Resilience
status: draft
priority: P1
epic: "Performance"
depends_on: []
estimated_effort: M
cloudvoyager_ref: "src/shared/utils/rate-limiter/, src/shared/utils/retry/"
---

# SPEC-016: Rate Limiting & API Resilience
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

Migrating data from SonarQube Server to SonarQube Cloud involves thousands of API calls across both platforms: reading issues, hotspots, metrics, rules, groups, and permissions from the source, and creating projects, submitting reports, transitioning issues, and updating metadata on the target. Each of these API calls can fail due to rate limiting (HTTP 429), transient server errors (5xx), network interruptions, DNS failures, or Compute Engine (CE) processing timeouts. A production-grade migration tool must handle all of these failure modes gracefully, with layered resilience that prevents data loss and provides actionable diagnostics.

CloudVoyager implements a four-layer resilience system: (1) exponential backoff retry for transient HTTP errors, (2) write request throttling to proactively avoid rate limits, (3) descriptive error recovery for common connection failures, and (4) CE task resilience with polling and re-submission. The sonar-migration-tool already has Layer 1 implemented in `lib/sq-api-go/retry.go` (retrying on 429/5xx with configurable backoff). This spec defines the remaining three layers and documents how they integrate with the existing retry infrastructure.

The existing retry transport in `lib/sq-api-go/retry.go` uses `retryableStatusCodes` (429, 500, 502, 503, 504) and a default backoff of 100ms, 200ms, 400ms with up to 50% random jitter. This is well-suited for read operations but insufficient for write-heavy migration phases where hundreds of concurrent POST requests can trigger SonarQube Cloud's rate limiter. The write throttling layer (Layer 2) addresses this by spacing write requests apart, and the CE task resilience layer (Layer 4) handles the unique challenges of the asynchronous report submission and processing pipeline.

## Problem Statement

The current sonar-migration-tool handles transient HTTP errors via the retry transport but has three critical gaps: (1) no proactive write throttling to prevent rate limit triggers during high-concurrency write phases, (2) no structured error recovery that translates opaque network errors into actionable diagnostics, and (3) no CE task resilience for handling submission timeouts, processing failures, and duplicate detection during the report upload phase. These gaps can cause migrations to fail partway through with unhelpful error messages, requiring manual intervention and re-runs.

## User Stories

- **As a** migration operator running a high-volume migration with 50 concurrent project syncs, **I want to** have write requests automatically throttled to avoid 429 rate limits, **so that** the migration completes without manual intervention.
- **As a** migration operator, **I want to** see clear diagnostic messages when the tool cannot connect to SQ Server or Cloud, **so that** I can fix the underlying network issue without reading raw HTTP error codes.
- **As a** migration operator, **I want to** have CE submission failures automatically retried with duplicate detection, **so that** a transient CE timeout does not cause missing or duplicated data in SonarQube Cloud.
- **As a** migration operator, **I want to** configure rate limiting parameters in the config file, **so that** I can adjust for my SonarQube Cloud plan's rate limits.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Maintain existing Layer 1 (exponential backoff retry) unchanged in `lib/sq-api-go/retry.go` | Must |
| FR-2 | Implement Layer 2: Write request throttling for POST/PUT/DELETE requests | Must |
| FR-3 | Layer 2 enforces `minRequestInterval` milliseconds between write requests to the same base URL | Must |
| FR-4 | Layer 2 throttler is shared across all goroutines targeting the same SonarQube Cloud organization | Must |
| FR-5 | Layer 2 default `minRequestInterval` = 0ms (disabled) — must be explicitly enabled | Must |
| FR-6 | Implement Layer 3: Connection error recovery with descriptive messages | Must |
| FR-7 | Layer 3 maps ECONNREFUSED to "Server not running or wrong port: {url}" | Must |
| FR-8 | Layer 3 maps ETIMEDOUT / context deadline exceeded to "Network timeout — possible firewall or proxy issue: {url}" | Must |
| FR-9 | Layer 3 maps ENOTFOUND / DNS resolution failure to "DNS resolution failure — check the URL: {url}" | Must |
| FR-10 | Layer 3 maps TLS handshake errors to "TLS/SSL error — check certificate configuration: {url}" | Must |
| FR-11 | Layer 3 preserves the original error as a wrapped inner error for debugging | Must |
| FR-12 | Implement Layer 4: CE task resilience for report submission | Must |
| FR-13 | Layer 4: After CE submit, poll `/api/ce/activity` for task status | Must |
| FR-14 | Layer 4: Poll up to 5 times at 3-second intervals on CE submit timeout | Must |
| FR-15 | Layer 4: If polling finds the task already exists (via `scm_revision_id`), skip re-submission | Must |
| FR-16 | Layer 4: If CE task status is FAILED, abort and report with task ID and error message | Must |
| FR-17 | Layer 4: If CE task status is CANCELLED, retry submission up to 3 times | Should |
| FR-18 | Support configuration via config file and CLI flags | Must |
| FR-19 | Log throttling events: "Write throttled: waiting Xms before POST to {path}" | Should |
| FR-20 | Log CE resilience events: "CE task {id} status: {status}, polling attempt {n}/{max}" | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Throttler overhead per request | < 1ms when minRequestInterval=0 (disabled path is near-zero cost) |
| NFR-2 | Throttler precision | Within 10% of configured interval (acceptable OS timer variance) |
| NFR-3 | Error message clarity | Non-technical operators can diagnose the issue without stack traces |
| NFR-4 | CE poll timeout | Total poll duration < 20 seconds (5 polls x 3s + overhead) |
| NFR-5 | Thread safety | All layers must be safe for concurrent use from multiple goroutines |
| NFR-6 | Zero dependency on external rate-limit tracking services | Entirely client-side |

## Technical Design

### Architecture

```
lib/sq-api-go/
├── retry.go              # EXISTING: Layer 1 (exponential backoff retry transport)
├── throttle.go           # NEW: Layer 2 (write request throttling)
├── throttle_test.go      # Throttle tests
├── errors.go             # EXTENDED: Layer 3 (connection error recovery messages)
└── ...

go/internal/scanreport/
├── submit.go             # EXTENDED: Layer 4 (CE task resilience)
├── submit_test.go        # CE resilience tests
└── ...
```

The layers compose as an HTTP transport chain:

```
Application code
    ↓
Layer 2: WriteThrottle (delays write requests to enforce minRequestInterval)
    ↓
Layer 1: retryTransport (retries on 429/5xx with exponential backoff)
    ↓
Layer 3: errorRecoveryTransport (translates connection errors to descriptive messages)
    ↓
http.DefaultTransport (actual HTTP connection)
```

Layer 4 operates at the application level (not as an HTTP transport) because it involves multi-step CE workflow logic (submit, poll, re-submit) that cannot be expressed as a single HTTP round-trip.

### Key Algorithms

#### Layer 2: Write Request Throttling

```go
package sqapi

import (
    "net/http"
    "sync"
    "time"
)

// writeThrottle is an http.RoundTripper that enforces a minimum interval
// between write requests (POST/PUT/DELETE) to the same base URL.
// Read requests (GET/HEAD/OPTIONS) pass through unthrottled.
//
// NOTE: The previous mutex+sleep implementation had a TOCTOU race condition:
// unlock -> sleep -> re-lock allows concurrent goroutines to both decide to
// sleep and then fire simultaneously. This implementation uses a channel-based
// rate gate instead, which is inherently safe for concurrent use.
type writeThrottle struct {
    inner    http.RoundTripper
    gate     chan struct{}
    interval time.Duration
}

// newWriteThrottle creates a throttle that allows one write request per interval.
// A background goroutine sends to the gate channel every interval, and each
// write request must receive from the gate before proceeding.
func newWriteThrottle(inner http.RoundTripper, interval time.Duration) *writeThrottle {
    t := &writeThrottle{
        inner:    inner,
        gate:     make(chan struct{}, 1),
        interval: interval,
    }
    if interval > 0 {
        // Seed the gate so the first request proceeds immediately
        t.gate <- struct{}{}
        go func() {
            ticker := time.NewTicker(interval)
            defer ticker.Stop()
            for range ticker.C {
                select {
                case t.gate <- struct{}{}:
                default:
                    // gate already has a token; skip to avoid blocking
                }
            }
        }()
    }
    return t
}

func (t *writeThrottle) RoundTrip(req *http.Request) (*http.Response, error) {
    // Fast path: disabled or read request
    if t.interval <= 0 || isReadMethod(req.Method) {
        return t.inner.RoundTrip(req)
    }

    // Acquire rate gate token — blocks until interval has passed
    select {
    case <-t.gate:
        log.Debug("Write throttled: acquired gate for %s to %s", req.Method, req.URL.Path)
    case <-req.Context().Done():
        return nil, req.Context().Err()
    }

    return t.inner.RoundTrip(req)
}

func isReadMethod(method string) bool {
    return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
```

#### Layer 3: Connection Error Recovery

```go
package sqapi

import (
    "errors"
    "fmt"
    "net"
    "net/http"
    "crypto/tls"
    "strings"
)

// ConnectionError wraps a network error with a human-readable diagnostic message.
type ConnectionError struct {
    Message string   // Human-readable diagnostic
    URL     string   // Target URL that failed
    Inner   error    // Original error for debugging
}

func (e *ConnectionError) Error() string { return e.Message }
func (e *ConnectionError) Unwrap() error { return e.Inner }

// classifyConnectionError translates a raw network error into a diagnostic.
func classifyConnectionError(err error, url string) error {
    if err == nil {
        return nil
    }
    
    var opErr *net.OpError
    var dnsErr *net.DNSError
    var tlsErr *tls.CertificateVerificationError
    
    switch {
    case errors.As(err, &dnsErr):
        return &ConnectionError{
            Message: fmt.Sprintf("DNS resolution failure - check the URL: %s", url),
            URL:     url,
            Inner:   err,
        }
    case errors.As(err, &opErr):
        if opErr.Op == "dial" {
            if strings.Contains(err.Error(), "connection refused") {
                return &ConnectionError{
                    Message: fmt.Sprintf("Server not running or wrong port: %s", url),
                    URL:     url,
                    Inner:   err,
                }
            }
            if opErr.Timeout() {
                return &ConnectionError{
                    Message: fmt.Sprintf("Network timeout - possible firewall or proxy issue: %s", url),
                    URL:     url,
                    Inner:   err,
                }
            }
        }
    case errors.As(err, &tlsErr):
        return &ConnectionError{
            Message: fmt.Sprintf("TLS/SSL error - check certificate configuration: %s", url),
            URL:     url,
            Inner:   err,
        }
    case strings.Contains(err.Error(), "context deadline exceeded"):
        return &ConnectionError{
            Message: fmt.Sprintf("Request timeout - the server took too long to respond: %s", url),
            URL:     url,
            Inner:   err,
        }
    }
    
    // Unknown network error: wrap but preserve original
    return &ConnectionError{
        Message: fmt.Sprintf("Network error connecting to %s: %v", url, err),
        URL:     url,
        Inner:   err,
    }
}
```

#### Layer 4: CE Task Resilience

```
FUNCTION SubmitReportWithResilience(ctx context.Context, client *sqapi.Client, 
                                      projectKey string, report []byte, 
                                      scmRevisionID string) (string, error):
    
    // Step 1: Check for existing task with same scmRevisionID (dedup)
    existingTask = findExistingTask(ctx, client, projectKey, scmRevisionID)
    IF existingTask != nil AND existingTask.Status == "SUCCESS":
        log.Info("Report already processed (task %s), skipping submission", existingTask.ID)
        RETURN existingTask.ID, nil
    
    // Step 2: Submit the report
    taskID, err = submitReport(ctx, client, projectKey, report, scmRevisionID)
    
    IF err != nil AND isTimeoutError(err):
        // Step 3: Timeout — poll for task that may have been accepted
        log.Warn("CE submit timed out, polling for task status")
        FOR attempt = 1; attempt <= 5; attempt++:
            time.Sleep(3 * time.Second)
            task = findExistingTask(ctx, client, projectKey, scmRevisionID)
            IF task != nil:
                log.Info("Found CE task %s (status: %s) after submit timeout", task.ID, task.Status)
                RETURN waitForTaskCompletion(ctx, client, task.ID)
        
        // Task not found after polling — re-submit
        log.Warn("CE task not found after 5 polls, re-submitting report")
        taskID, err = submitReport(ctx, client, projectKey, report, scmRevisionID)
        IF err != nil:
            RETURN "", fmt.Errorf("CE re-submission failed: %w", err)
    ELSE IF err != nil:
        RETURN "", fmt.Errorf("CE submission failed: %w", err)
    
    // Step 4: Wait for task completion
    RETURN waitForTaskCompletion(ctx, client, taskID)


FUNCTION waitForTaskCompletion(ctx context.Context, client *sqapi.Client, 
                                 taskID string) (string, error):
    maxPolls = 120          // 10 minutes total (120 x 5s)
    pollInterval = 5s
    
    FOR poll = 1; poll <= maxPolls; poll++:
        task = getTaskStatus(ctx, client, taskID)
        
        SWITCH task.Status:
            CASE "SUCCESS":
                log.Info("CE task %s completed successfully", taskID)
                RETURN taskID, nil
            CASE "FAILED":
                RETURN "", fmt.Errorf("CE task %s failed: %s (error: %s)", 
                    taskID, task.ErrorMessage, task.ErrorStacktrace)
            CASE "CANCELLED":
                RETURN "", fmt.Errorf("CE task %s was cancelled", taskID)
            CASE "PENDING", "IN_PROGRESS":
                IF poll % 10 == 0:
                    log.Info("CE task %s still %s (poll %d/%d)", 
                        taskID, task.Status, poll, maxPolls)
        
        SELECT:
            CASE <-ctx.Done():
                RETURN "", ctx.Err()
            CASE <-time.After(pollInterval):
                // continue polling
    
    RETURN "", fmt.Errorf("CE task %s did not complete within %v", 
        taskID, time.Duration(maxPolls) * pollInterval)


FUNCTION findExistingTask(ctx context.Context, client *sqapi.Client,
                           projectKey string, scmRevisionID string) *CETask:
    // Query CE activity for tasks matching the project and scm_revision_id
    tasks = GET /api/ce/activity?component={projectKey}&ps=10&status=SUCCESS,FAILED,PENDING,IN_PROGRESS
    
    FOR EACH task IN tasks:
        IF task.SCMRevisionID == scmRevisionID:
            RETURN task
    
    RETURN nil
```

### Data Flow

1. **Request Initiation**: Application code calls `client.Post()` or `client.Get()`.
2. **Layer 2 (Write Throttle)**: If the request is a write (POST/PUT/DELETE) and `minRequestInterval > 0`, the throttle enforces spacing. Read requests bypass.
3. **Layer 1 (Retry)**: The retry transport executes the request. On 429/5xx, retries with exponential backoff up to 3 times.
4. **Layer 3 (Error Recovery)**: If the underlying transport returns a network error (connection refused, timeout, DNS failure, TLS error), the error recovery layer translates it to a human-readable `ConnectionError`.
5. **Application Layer**: The caller receives either a successful response or a descriptive error. For CE submissions, Layer 4 provides additional resilience with polling and re-submission.

### Configuration

```json
{
  "rateLimiting": {
    "maxRetries": 3,
    "baseDelay": 1000,
    "minRequestInterval": 0,
    "ceTaskPollInterval": 5000,
    "ceTaskPollMaxAttempts": 120
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `maxRetries` | int | 3 | Maximum retry attempts for Layer 1 |
| `baseDelay` | int (ms) | 1000 | Base delay for Layer 2 write throttling. Note: Layer 1 (existing retry.go) retains its current 100ms base delay. The `rateLimiting.baseDelay` config applies to Layer 2 write throttling only. |
| `minRequestInterval` | int (ms) | 0 | Minimum time between write requests (0 = disabled) |
| `ceTaskPollInterval` | int (ms) | 5000 | Interval between CE task status polls |
| `ceTaskPollMaxAttempts` | int | 120 | Maximum CE task poll attempts |

### API Dependencies

| Endpoint | Method | Purpose | Layer |
|----------|--------|---------|-------|
| `/api/ce/submit` | POST | Submit scanner report to CE | Layer 4 |
| `/api/ce/activity` | GET | Poll CE task status | Layer 4 |
| `/api/ce/task` | GET | Get specific CE task details | Layer 4 |
| Any endpoint | Any | Retry on 429/5xx | Layer 1 |
| Write endpoints | POST/PUT/DELETE | Throttle between writes | Layer 2 |

## Acceptance Criteria

- [ ] AC-1: Existing Layer 1 retry behavior in `lib/sq-api-go/retry.go` is unchanged (backward compatible)
- [ ] AC-2: Layer 2 throttle enforces `minRequestInterval` between consecutive write requests
- [ ] AC-3: Layer 2 throttle allows concurrent read requests without delay
- [ ] AC-4: Layer 2 throttle with `minRequestInterval=0` adds negligible overhead (< 1ms per request)
- [ ] AC-5: Layer 2 throttle respects context cancellation during wait
- [ ] AC-6: Layer 3 maps connection refused to "Server not running or wrong port: {url}"
- [ ] AC-7: Layer 3 maps timeout to "Network timeout - possible firewall or proxy issue: {url}"
- [ ] AC-8: Layer 3 maps DNS failure to "DNS resolution failure - check the URL: {url}"
- [ ] AC-9: Layer 3 maps TLS error to "TLS/SSL error - check certificate configuration: {url}"
- [ ] AC-10: Layer 3 preserves original error via `errors.Unwrap()`
- [ ] AC-11: Layer 4 detects duplicate CE task via `scm_revision_id` and skips re-submission
- [ ] AC-12: Layer 4 polls `/api/ce/activity` up to 5 times at 3s intervals after submit timeout
- [ ] AC-13: Layer 4 re-submits report if polling does not find the task
- [ ] AC-14: Layer 4 reports CE task failure with task ID and error message
- [ ] AC-15: Configuration is loaded from config file and overridable via CLI flags
- [ ] AC-16: Throttle events are logged at DEBUG level
- [ ] AC-17: CE resilience events are logged at INFO/WARN level
- [ ] AC-18: All layers are safe for concurrent use from multiple goroutines (`go test -race`)

## CloudVoyager Reference

| Area | Path |
|------|------|
| Rate limiter | `src/shared/utils/rate-limiter/` |
| Retry utility | `src/shared/utils/retry/` |
| Write throttler | `src/shared/utils/rate-limiter/write-throttler.js` |
| CE submission | `src/pipelines/shared/upload/ce-submit.js` |
| CE task polling | `src/pipelines/shared/upload/ce-poll.js` |
| Error classifier | `src/shared/utils/error-classifier.js` |
| Configuration schema | `src/shared/config/rate-limiting-schema.js` |

## Known Limitations

- Layer 2 write throttling uses a channel-based rate gate (single-element buffered channel fed by a `time.Ticker`). This is simpler than a token bucket but less flexible: it cannot "burst" N requests and then throttle, which a token bucket can. If burst support is needed in the future, the implementation can be upgraded to a token bucket without changing the interface.
- Layer 3 error classification depends on Go's standard library error types (`net.OpError`, `net.DNSError`, `tls.CertificateVerificationError`). Custom proxy errors or cloud provider-specific errors may not match these types and will fall through to the generic "Network error" message.
- Layer 4 CE dedup relies on `scm_revision_id` matching. If the tool generates different `scm_revision_id` values for the same logical report (e.g., due to clock skew or metadata changes), dedup will not activate and duplicate reports may be submitted.
- The CE polling approach (fixed interval, fixed max attempts) is simple but not adaptive. A more sophisticated approach would use exponential backoff for polling or adjust the interval based on CE queue depth. However, the simple approach matches CloudVoyager's behavior and is sufficient for most migrations.
- Layer 2 throttling is per-client (per-base-URL). If multiple `sqapi.Client` instances target the same SonarQube Cloud organization, each has its own throttle and they do not coordinate. For single-client usage (the normal case), this is fine.

### Race Condition Analysis

- Write throttle uses channel-based rate gate -- inherently safe for concurrent use
- Per-org throttler: each organization gets its own throttler instance -- no cross-org contention
- Connection error diagnostics (Layer 3) are pure functions -- no state
- CE task polling (Layer 4) is sequential per task -- no concurrent access to task state

## Sonar Documentation Reference

For full Sonar product documentation, see: https://docs.sonarsource.com/llms.txt

## Open Questions

- Q1: Should Layer 2 use a token bucket instead of a simple time gate, to allow configurable burst sizes?
- Q2: Should Layer 4 CE polling use exponential backoff instead of fixed intervals, to reduce API load during long-running CE tasks?
- Q3: Should Layer 3 include HTTP response body parsing for SonarQube-specific error messages (e.g., "Insufficient privileges")?
- Q4: Should the tool track cumulative 429 count during a migration and warn the operator if it exceeds a threshold (e.g., "Received 50 rate limit responses — consider increasing minRequestInterval")?
- Q5: Should the write throttler coordinate across multiple `sqapi.Client` instances targeting the same SC organization, or is per-client throttling sufficient?
