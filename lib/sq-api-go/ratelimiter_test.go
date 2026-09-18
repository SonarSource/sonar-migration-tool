// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// assertSlidingWindowInvariant fails the test if any point in ts has more
// than max other entries of ts within the trailing window ending at that
// point — i.e. it re-checks, after the fact, that the limiter never
// granted more than max calls in any trailing window-length interval.
func assertSlidingWindowInvariant(t *testing.T, ts []time.Time, window time.Duration, max int) {
	t.Helper()
	sorted := append([]time.Time(nil), ts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })

	for i, cur := range sorted {
		cutoff := cur.Add(-window)
		count := 0
		for _, other := range sorted[:i+1] {
			if other.After(cutoff) {
				count++
			}
		}
		assert.LessOrEqualf(t, count, max, "window ending at %v (index %d) contains %d grants, want <= %d", cur, i, count, max)
	}
}

// TestSlidingWindowLimiterCapsGrantsWithinWindow fires more concurrent
// Wait calls than the limiter allows and asserts that, once every call
// has been granted, no trailing window-length interval ever contained
// more than maxPerMinute grants. The window is shrunk via the test-only
// constructor so the test runs in milliseconds instead of minutes.
func TestSlidingWindowLimiterCapsGrantsWithinWindow(t *testing.T) {
	const (
		max      = 5
		window   = 120 * time.Millisecond
		requests = 15
	)
	limiter := sqapi.NewSlidingWindowLimiterWithWindow(max, window)

	var (
		mu    sync.Mutex
		grant []time.Time
	)

	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := limiter.Wait(ctx)
			require.NoError(t, err)

			mu.Lock()
			grant = append(grant, time.Now())
			mu.Unlock()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for all Wait calls to be granted")
	}

	require.Len(t, grant, requests)
	assertSlidingWindowInvariant(t, grant, window, max)
}

// TestSlidingWindowLimiterContextCancellation checks that Wait returns
// promptly with the context's error when the limiter is saturated and
// the caller's context is already canceled, or has a very short
// deadline — rather than blocking until a slot would naturally open.
func TestSlidingWindowLimiterContextCancellation(t *testing.T) {
	// window is deliberately much longer than the timeouts used below so
	// a slot cannot open naturally during the test.
	limiter := sqapi.NewSlidingWindowLimiterWithWindow(1, time.Minute)

	// Saturate the single slot.
	require.NoError(t, limiter.Wait(context.Background()))

	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		err := limiter.Wait(ctx)
		elapsed := time.Since(start)

		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled))
		assert.Less(t, elapsed, 500*time.Millisecond, "Wait should return promptly, not block for the full window")
	})

	t.Run("short deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		start := time.Now()
		err := limiter.Wait(ctx)
		elapsed := time.Since(start)

		require.Error(t, err)
		assert.True(t, errors.Is(err, context.DeadlineExceeded))
		assert.Less(t, elapsed, 500*time.Millisecond, "Wait should return shortly after the deadline, not block for the full window")
	})
}

// TestThrottleTransportLatencyAndRateLimit drives a throttleTransport
// against a real httptest server whose handler sleeps a small
// configurable duration before responding. It asserts that the
// LatencyObserver receives durations at least as large as the simulated
// server-side sleep, and that the rate limiter caps how many requests
// can be in flight to the server within any trailing window.
func TestThrottleTransportLatencyAndRateLimit(t *testing.T) {
	const sleep = 20 * time.Millisecond

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(sleep)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	const (
		max      = 3
		window   = 150 * time.Millisecond
		requests = 9
	)
	limiter := sqapi.NewSlidingWindowLimiterWithWindow(max, window)

	var (
		mu          sync.Mutex
		durations   []time.Duration
		grantedAtMu sync.Mutex
		grantedAt   []time.Time
	)
	observer := func(d time.Duration) {
		mu.Lock()
		durations = append(durations, d)
		mu.Unlock()

		// The observer fires right after the inner RoundTrip completes,
		// so subtracting its own duration recovers (approximately) the
		// wall-clock moment the limiter granted this request its slot.
		grantedAtMu.Lock()
		grantedAt = append(grantedAt, time.Now().Add(-d))
		grantedAtMu.Unlock()
	}

	transport := sqapi.NewThrottleTransport(http.DefaultTransport, limiter, observer)
	client := &http.Client{Transport: transport}

	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
			require.NoError(t, err)
			resp, err := client.Do(req)
			require.NoError(t, err)
			resp.Body.Close()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for all requests to complete")
	}

	mu.Lock()
	gotDurations := append([]time.Duration(nil), durations...)
	mu.Unlock()
	require.Len(t, gotDurations, requests)
	for _, d := range gotDurations {
		assert.GreaterOrEqualf(t, d, sleep, "observed duration %v should be at least the simulated server sleep %v", d, sleep)
	}

	grantedAtMu.Lock()
	gotGrants := append([]time.Time(nil), grantedAt...)
	grantedAtMu.Unlock()
	require.Len(t, gotGrants, requests)
	assertSlidingWindowInvariant(t, gotGrants, window, max)
}
