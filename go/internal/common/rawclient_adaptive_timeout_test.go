// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// These tests drive RawClient through the real SDK client stack, where
// retryTransport sits underneath http.Client.Timeout, rather than the
// plain httptest client the other adaptive tests use. The stack matters:
// Client.Timeout bounds every SDK retry of one request together, so
// same-size retries of a slow 504 can spend the whole budget before the
// page-size fallback ever sees the 504.
//
// The contract under test is the agreed one: only HTTP 5xx steps the
// page size down, with no user configuration. A paginated attempt waits
// up to twice the client's timeout, so a gateway that answers 504 just
// after the default timeout is heard; a client timeout itself still
// reaches the caller unchanged.

// testSlowGatewayTimeout stands in for slowGatewayTimeout, scaled down
// with the test's load-balancer delays. An immediate 504 stays well
// under it even on a loaded CI runner.
const testSlowGatewayTimeout = 100 * time.Millisecond

// sdkRawClient builds a RawClient on a real sqapi server client, with
// the timeout shortened so the test runs in milliseconds. It also scales
// slowGatewayTimeout down for the duration of the test.
func sdkRawClient(t *testing.T, serverURL string, timeout time.Duration) *RawClient {
	t.Helper()
	prev := slowGatewayTimeout
	slowGatewayTimeout = testSlowGatewayTimeout
	t.Cleanup(func() { slowGatewayTimeout = prev })

	client := sqapi.NewServerClient(serverURL, "test-token", 9.9)
	client.HTTPClient().Timeout = timeout
	return NewRawClient(client.HTTPClient(), client.BaseURL())
}

// pageSizeCounter records how many requests the server saw per ps value.
type pageSizeCounter struct {
	mu     sync.Mutex
	counts map[int]int
}

// hit records one request and returns its page size and how many
// requests, this one included, have used that size.
func (c *pageSizeCounter) hit(r *http.Request) (size, nth int) {
	size, _ = strconv.Atoi(r.URL.Query().Get("ps"))
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[size]++
	return size, c.counts[size]
}

func (c *pageSizeCounter) count(size int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[size]
}

// countingServer starts a test server that counts every request by page
// size before handing it to respond.
func countingServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request, size, nth int)) (*httptest.Server, *pageSizeCounter) {
	t.Helper()
	counter := &pageSizeCounter{counts: make(map[int]int)}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size, nth := counter.hit(r)
		respond(w, r, size, nth)
	}))
	t.Cleanup(ts.Close)
	return ts, counter
}

// delayedStatus answers with status after delay, or stops early when the
// client gives up: a load balancer timing out on a slow backend.
func delayedStatus(w http.ResponseWriter, r *http.Request, delay time.Duration, status int) {
	select {
	case <-time.After(delay):
		http.Error(w, http.StatusText(status), status)
	case <-r.Context().Done():
	}
}

func projectSearchOpts() PaginatedOpts {
	return PaginatedOpts{Path: "api/projects/search", ResultKey: "items"}
}

// assertCompleteUniqueKeys checks that items carry exactly want distinct
// keys and no key twice. writeAdaptiveTestPage numbers keys 0..total-1,
// so that is the same as "every item, once".
func assertCompleteUniqueKeys(t *testing.T, items []json.RawMessage, want int) {
	t.Helper()
	seen := make(map[string]bool, len(items))
	for _, raw := range items {
		var item struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("decoding item: %v", err)
		}
		if seen[item.Key] {
			t.Errorf("duplicate key %q", item.Key)
		}
		seen[item.Key] = true
	}
	if len(items) != want || len(seen) != want {
		t.Errorf("got %d items with %d unique keys, want %d of each", len(items), len(seen), want)
	}
}

// A 504 that arrives inside the client timeout must step the page size
// down after ONE request. Each same-size SDK retry would cost another
// full load-balancer delay plus backoff.
func TestAdaptivePaginationDelayed504StepsDownAfterOneRequest(t *testing.T) {
	const lbDelay = 150 * time.Millisecond
	ts, counter := countingServer(t, func(w http.ResponseWriter, r *http.Request, size, _ int) {
		if size > 200 {
			delayedStatus(w, r, lbDelay, http.StatusGatewayTimeout)
			return
		}
		writeAdaptiveTestPage(t, w, r, 1200)
	})

	start := time.Now()
	res, err := sdkRawClient(t, ts.URL, 5*time.Second).GetPaginatedResult(context.Background(), projectSearchOpts())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("GetPaginatedResult: %v", err)
	}
	if got := counter.count(500); got != 1 {
		t.Errorf("ps=500 requests = %d, want 1 (no same-size retries of a 504)", got)
	}
	if res.PageSize != 200 {
		t.Errorf("PageSize = %d, want 200", res.PageSize)
	}
	assertCompleteUniqueKeys(t, res.Items, 1200)
	// One load-balancer delay plus six fast pages takes ~0.2s. Same-size
	// retries would add three more delays and the SDK backoff on top,
	// never less than 1.3s, so this bound has room for a slow runner.
	if limit := time.Second; elapsed > limit {
		t.Errorf("took %v, want under %v", elapsed, limit)
	}
}

// The reported load-balancer configuration, scaled down, with no user
// configuration: the client keeps its default 60s timeout and the load
// balancer answers 504 after ~64s. A paginated attempt that can still
// fall back waits up to twice the timeout, so the 504 arrives, and it is
// handed straight to the fallback instead of being retried at ps=500.
func TestAdaptivePaginationDefaultTimeoutHearsGateway504(t *testing.T) {
	const (
		clientTimeout = 300 * time.Millisecond // the default 60s, scaled
		lbDelay       = 320 * time.Millisecond // the gateway's ~64s, scaled
	)
	ts, counter := countingServer(t, func(w http.ResponseWriter, r *http.Request, size, _ int) {
		if size > 200 {
			delayedStatus(w, r, lbDelay, http.StatusGatewayTimeout)
			return
		}
		writeAdaptiveTestPage(t, w, r, 450)
	})

	res, err := sdkRawClient(t, ts.URL, clientTimeout).GetPaginatedResult(context.Background(), projectSearchOpts())

	if err != nil {
		t.Fatalf("GetPaginatedResult: %v (ps=500 requests: %d)", err, counter.count(500))
	}
	if res.PageSize != 200 {
		t.Errorf("PageSize = %d, want 200", res.PageSize)
	}
	assertCompleteUniqueKeys(t, res.Items, 450)
}

// A client timeout is not a page-size signal: only HTTP 5xx is. The
// timeout must reach the caller promptly, with no smaller-size attempts.
func TestAdaptivePaginationClientTimeoutIsNotAPageSizeSignal(t *testing.T) {
	ts, counter := countingServer(t, func(w http.ResponseWriter, r *http.Request, _, _ int) {
		delayedStatus(w, r, 2*time.Second, http.StatusGatewayTimeout)
	})

	start := time.Now()
	_, err := sdkRawClient(t, ts.URL, 150*time.Millisecond).GetPaginatedResult(context.Background(), projectSearchOpts())
	elapsed := time.Since(start)

	var httpErr *HTTPError
	var netErr net.Error
	switch {
	case err == nil:
		t.Fatal("expected the client timeout, got success")
	case errors.As(err, &httpErr):
		t.Errorf("got HTTP error %v, want the client timeout", err)
	case !errors.As(err, &netErr) || !netErr.Timeout():
		t.Errorf("err = %v, want a timeout", err)
	}
	if got := counter.count(500); got != 1 {
		t.Errorf("ps=500 requests = %d, want 1", got)
	}
	if got := counter.count(200); got != 0 {
		t.Errorf("ps=200 requests = %d, want 0: a timeout must not trigger fallback", got)
	}
	if elapsed > time.Second {
		t.Errorf("took %v, want the timeout reported promptly", elapsed)
	}
}

// Cancelling the caller's context (shutdown, Ctrl-C, a failed sibling
// task) must stop the fetch without trying smaller page sizes.
func TestAdaptivePaginationCallerCancellationIsNotAPageSizeSignal(t *testing.T) {
	ts, counter := countingServer(t, func(w http.ResponseWriter, r *http.Request, _, _ int) {
		delayedStatus(w, r, 2*time.Second, http.StatusGatewayTimeout)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)

	_, err := sdkRawClient(t, ts.URL, 5*time.Second).GetPaginatedResult(ctx, projectSearchOpts())

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := counter.count(500); got != 1 {
		t.Errorf("ps=500 requests = %d, want 1", got)
	}
	if got := counter.count(200); got != 0 {
		t.Errorf("ps=200 requests = %d, want 0: cancellation must not trigger fallback", got)
	}
}

// assertTimesOutAt checks that err is a client timeout reported after
// about timeout, not after the doubled paginated budget.
func assertTimesOutAt(t *testing.T, err error, elapsed, timeout time.Duration) {
	t.Helper()
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("err = %v, want a client timeout", err)
	}
	if elapsed >= paginatedTimeoutFactor*timeout {
		t.Errorf("timed out after %v, want the normal %v timeout, not the paginated %v",
			elapsed, timeout, paginatedTimeoutFactor*timeout)
	}
}

// Requests outside adaptive pagination keep the client's normal timeout:
// the doubled budget is only for paginated fetches.
func TestRawClientGetKeepsNormalTimeout(t *testing.T) {
	const clientTimeout = 300 * time.Millisecond
	ts, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request, _, _ int) {
		delayedStatus(w, r, 2*clientTimeout, http.StatusGatewayTimeout)
	})

	start := time.Now()
	_, err := sdkRawClient(t, ts.URL, clientTimeout).Get(context.Background(), "api/server/version", nil)

	assertTimesOutAt(t, err, time.Since(start), clientTimeout)
}

// At the minimum page size there is nothing smaller to fall back to, but
// the attempt still waits up to twice the client's timeout: a gateway
// that answers just after the client's own timeout is reported as the
// 504 it sent rather than as a client timeout that hides it. The SDK does
// not retry the slow 504; GetPaginatedResult tries twice more itself
// before giving up.
func TestAdaptivePaginationMinimumPageSizeHearsGateway504(t *testing.T) {
	const clientTimeout = 300 * time.Millisecond
	ts, counter := countingServer(t, func(w http.ResponseWriter, r *http.Request, _, _ int) {
		delayedStatus(w, r, clientTimeout*3/2, http.StatusGatewayTimeout)
	})
	opts := projectSearchOpts()
	opts.MaxPageSize = 50

	_, err := sdkRawClient(t, ts.URL, clientTimeout).GetPaginatedResult(context.Background(), opts)

	if !IsHTTPError(err, http.StatusGatewayTimeout) {
		t.Fatalf("err = %v, want the gateway's HTTP 504", err)
	}
	if got := counter.count(50); got != 3 {
		t.Errorf("ps=50 requests = %d, want 3 (one try and two retries)", got)
	}
}

// A server that is slow for a while at the minimum page size gets more
// tries there, and the fetch resumes from the page that failed.
func TestAdaptivePaginationMinimumPageSizeRetriesSlow5xx(t *testing.T) {
	var log pageLog
	var slowLeft atomic.Int32
	slowLeft.Store(2)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if page, _ := log.add(r); page == 2 && slowLeft.Add(-1) >= 0 {
			delayedStatus(w, r, 2*testSlowGatewayTimeout, http.StatusServiceUnavailable)
			return
		}
		writeAdaptiveTestPage(t, w, r, 120)
	}))
	t.Cleanup(ts.Close)
	opts := projectSearchOpts()
	opts.MaxPageSize = 50

	res, err := sdkRawClient(t, ts.URL, 5*time.Second).GetPaginatedResult(context.Background(), opts)

	if err != nil {
		t.Fatalf("GetPaginatedResult: %v", err)
	}
	want := []string{"1:50", "2:50", "2:50", "2:50", "3:50"}
	if got := log.get(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	assertCompleteUniqueKeys(t, res.Items, 120)
}

// Requests outside adaptive pagination keep the SDK's normal 504 retries.
func TestRawClientGetStillRetries504(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 2 {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(ts.Close)

	if _, err := sdkRawClient(t, ts.URL, 5*time.Second).Get(context.Background(), "api/server/version", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("requests = %d, want 3 (two 504s retried by the SDK)", got)
	}
}

// At every page size, the minimum included, a fast 504 keeps the SDK's
// normal retries, and a slow one is not retried at the same size.
func TestAdaptivePaginationMinimumPageSizeKeepsSDKRetries(t *testing.T) {
	ts, counter := countingServer(t, func(w http.ResponseWriter, r *http.Request, size, nth int) {
		switch {
		case size > 50:
			delayedStatus(w, r, 150*time.Millisecond, http.StatusGatewayTimeout)
		case nth <= 2:
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
		default:
			writeAdaptiveTestPage(t, w, r, 120)
		}
	})

	res, err := sdkRawClient(t, ts.URL, 5*time.Second).GetPaginatedResult(context.Background(), projectSearchOpts())

	if err != nil {
		t.Fatalf("GetPaginatedResult: %v", err)
	}
	if res.PageSize != 50 {
		t.Errorf("PageSize = %d, want 50", res.PageSize)
	}
	assertCompleteUniqueKeys(t, res.Items, 120)
	for _, size := range []int{500, 200, 100} {
		if got := counter.count(size); got != 1 {
			t.Errorf("ps=%d requests = %d, want 1", size, got)
		}
	}
	if got := counter.count(50); got != 5 {
		t.Errorf("ps=50 requests = %d, want 5: two 504s retried by the SDK, then three pages", got)
	}
}

// A fast 504 is a proxy blip, not a page-size signal: the SDK retries it
// at the same size, so a burst shorter than about 1 second does not lower
// the endpoint's page size for the rest of the run.
func TestAdaptivePaginationFast504RetriedAtSameSize(t *testing.T) {
	ts, counter := countingServer(t, func(w http.ResponseWriter, r *http.Request, size, nth int) {
		if size == 500 && nth <= 2 {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		writeAdaptiveTestPage(t, w, r, 30)
	})

	res, err := sdkRawClient(t, ts.URL, 5*time.Second).GetPaginatedResult(context.Background(), projectSearchOpts())

	if err != nil {
		t.Fatalf("GetPaginatedResult: %v", err)
	}
	if res.PageSize != 500 {
		t.Errorf("PageSize = %d, want 500", res.PageSize)
	}
	if got := counter.count(500); got != 3 {
		t.Errorf("ps=500 requests = %d, want 3 (two fast 504s retried by the SDK)", got)
	}
	if got := counter.count(200); got != 0 {
		t.Errorf("ps=200 requests = %d, want 0", got)
	}
	assertCompleteUniqueKeys(t, res.Items, 30)
}

// A fast 500/502/503 is retried at the same size like a fast 504, so one
// blip does not lower the endpoint's page size. Only a slow one skips
// the same-size retry.
func TestAdaptivePaginationTransient503RetriedAtSameSize(t *testing.T) {
	ts, counter := countingServer(t, func(w http.ResponseWriter, r *http.Request, size, nth int) {
		if size == 500 && nth == 1 {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		writeAdaptiveTestPage(t, w, r, 30)
	})

	res, err := sdkRawClient(t, ts.URL, 5*time.Second).GetPaginatedResult(context.Background(), projectSearchOpts())

	if err != nil {
		t.Fatalf("GetPaginatedResult: %v", err)
	}
	if res.PageSize != 500 {
		t.Errorf("PageSize = %d, want 500", res.PageSize)
	}
	if got := counter.count(500); got != 2 {
		t.Errorf("ps=500 requests = %d, want 2 (one 503 retried by the SDK)", got)
	}
	if got := counter.count(200); got != 0 {
		t.Errorf("ps=200 requests = %d, want 0", got)
	}
	assertCompleteUniqueKeys(t, res.Items, 30)
}
