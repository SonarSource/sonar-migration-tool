// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

func TestClassify429(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		body    []byte
		want    sqapi.RateLimitKind
	}{
		{
			name:    "sonar json body",
			headers: http.Header{},
			body:    []byte(`{"errors":[{"msg":"Rate limit exceeded"}]}`),
			want:    sqapi.KindSQCRateLimit,
		},
		{
			name:    "html body with cf-ray header",
			headers: http.Header{"Cf-Ray": []string{"abc123-IAD"}},
			body:    []byte("<html><body>Error 1015</body></html>"),
			want:    sqapi.KindCloudflareRateLimit,
		},
		{
			name:    "html body mentioning cloudflare, no header",
			headers: http.Header{},
			body:    []byte("<html><head><title>Cloudflare</title></head></html>"),
			want:    sqapi.KindCloudflareRateLimit,
		},
		{
			name:    "server cloudflare header",
			headers: http.Header{"Server": []string{"cloudflare"}},
			body:    nil,
			want:    sqapi.KindCloudflareRateLimit,
		},
		{
			name:    "empty body, no signals",
			headers: http.Header{},
			body:    nil,
			want:    sqapi.KindUnknown429,
		},
		{
			name:    "plain text body, no signals",
			headers: http.Header{},
			body:    []byte("Too Many Requests"),
			want:    sqapi.KindUnknown429,
		},
		{
			name:    "cf-mitigated header takes precedence",
			headers: http.Header{"Cf-Mitigated": []string{"challenge"}},
			body:    []byte(`{"errors":[{"msg":"x"}]}`),
			want:    sqapi.KindCloudflareRateLimit,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sqapi.Classify429(tc.headers, tc.body)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRateLimitKindString(t *testing.T) {
	assert.Equal(t, "sqc-rate-limit", sqapi.KindSQCRateLimit.String())
	assert.Equal(t, "cloudflare-rate-limit", sqapi.KindCloudflareRateLimit.String())
	assert.Equal(t, "unknown-429", sqapi.KindUnknown429.String())
}

func TestClassify429LongBody(t *testing.T) {
	long := append([]byte(`{"errors":[{"msg":"`), make([]byte, 1000)...)
	for i := range long[len(`{"errors":[{"msg":"`):] {
		long[len(`{"errors":[{"msg":"`)+i] = 'x'
	}
	got := sqapi.Classify429(http.Header{}, long)
	assert.Equal(t, sqapi.KindSQCRateLimit, got,
		"long bodies must still classify correctly via the bounded prefix")
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		name    string
		header  string
		wantMin time.Duration
		wantMax time.Duration
	}{
		{"absent", "", 0, 0},
		{"seconds", "42", 42 * time.Second, 42 * time.Second},
		{"zero seconds", "0", 0, 0},
		{"negative ignored", "-5", 0, 0},
		{"garbage ignored", "soon", 0, 0},
		{"http date in future", time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat), 25 * time.Second, 31 * time.Second},
		{"http date in past", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.header != "" {
				h.Set("Retry-After", tc.header)
			}
			got := sqapi.ParseRetryAfterHeader(h)
			assert.GreaterOrEqual(t, got, tc.wantMin)
			assert.LessOrEqual(t, got, tc.wantMax)
		})
	}
}

// doGet performs a GET to url using client, closes the response body, and
// returns the response. It fatals if the request fails.
func doGet(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := client.Get(url)
	require.NoError(t, err)
	resp.Body.Close()
	return resp
}

func TestRetryTransportSQCBackoff(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":[{"msg":"rate limit exceeded"}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	var observed sqapi.RateLimitEvent
	transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
		Inner:      http.DefaultTransport,
		Backoff:    []time.Duration{0},
		SQCBackoff: []time.Duration{0, 0, 0},
		Observer: func(e sqapi.RateLimitEvent) {
			observed = e
		},
	})

	client := &http.Client{Transport: transport}
	resp := doGet(t, client, ts.URL)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(2), attempts.Load(), "should retry once after the 429")
	assert.Equal(t, sqapi.KindSQCRateLimit, observed.Kind)
	assert.Contains(t, observed.BodySnippet, "rate limit exceeded")
}

func TestRetryTransportFailFastForCloudflare(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("CF-Ray", "abc-DFW")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("<html>Cloudflare error 1015</html>"))
	}))
	defer ts.Close()

	transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
		Inner:         http.DefaultTransport,
		Backoff:       []time.Duration{0, 0, 0, 0, 0},
		SQCBackoff:    []time.Duration{0, 0, 0, 0, 0, 0},
		NonSQCBackoff: []time.Duration{0},
	})

	client := &http.Client{Transport: transport}
	resp := doGet(t, client, ts.URL)

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, int32(2), attempts.Load(),
		"Cloudflare-classified 429 should fall under the short fail-fast schedule (1 retry)")
}

func TestRetryTransportRetryAfterHonored(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":[{"msg":"slow down"}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
		Inner:      http.DefaultTransport,
		Backoff:    []time.Duration{0},
		SQCBackoff: []time.Duration{0, 0},
	})

	start := time.Now()
	client := &http.Client{Transport: transport}
	resp := doGet(t, client, ts.URL)
	elapsed := time.Since(start)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.GreaterOrEqual(t, elapsed, 1*time.Second,
		"Retry-After: 1 should produce at least one second of wall-clock pause")
}

// recoveryCall captures one invocation of the RecoveryLogFunc.
type recoveryCall struct {
	retries int
	waited  time.Duration
}

func TestRetryTransportRecoveryFiresAfter429(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":[{"msg":"rate limit exceeded"}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	var (
		mu    sync.Mutex
		calls []recoveryCall
	)
	transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
		Inner:      http.DefaultTransport,
		Backoff:    []time.Duration{0},
		SQCBackoff: []time.Duration{5 * time.Millisecond, 5 * time.Millisecond},
		Recovery: func(_, _ string, retries int, waited time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, recoveryCall{retries: retries, waited: waited})
		},
	})

	client := &http.Client{Transport: transport}
	resp := doGet(t, client, ts.URL)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, calls, 1, "recovery must fire exactly once when a 429 clears")
	assert.Equal(t, 1, calls[0].retries, "one retry preceded the recovery")
	assert.Greater(t, calls[0].waited, time.Duration(0), "recovery must report the time spent paused")
}

func TestRetryTransportRecoveryNotFiredFor5xx(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	var fired atomic.Int32
	transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
		Inner:   http.DefaultTransport,
		Backoff: []time.Duration{0},
		Recovery: func(_, _ string, _ int, _ time.Duration) {
			fired.Add(1)
		},
	})

	client := &http.Client{Transport: transport}
	resp := doGet(t, client, ts.URL)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(0), fired.Load(), "5xx retries are not rate limiting — recovery must not fire")
}

func TestRetryTransportRecoveryNotFiredWhenScheduleExhausted(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":[{"msg":"still limited"}]}`))
	}))
	defer ts.Close()

	var fired atomic.Int32
	transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
		Inner:      http.DefaultTransport,
		Backoff:    []time.Duration{0},
		SQCBackoff: []time.Duration{0, 0},
		Recovery: func(_, _ string, _ int, _ time.Duration) {
			fired.Add(1)
		},
	})

	client := &http.Client{Transport: transport}
	resp := doGet(t, client, ts.URL)

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, int32(0), fired.Load(), "a request that never clears the 429 has not recovered")
}

func TestRetryTransportRecoveryNotFiredOnImmediateSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	var fired atomic.Int32
	transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
		Inner:   http.DefaultTransport,
		Backoff: []time.Duration{0},
		Recovery: func(_, _ string, _ int, _ time.Duration) {
			fired.Add(1)
		},
	})

	client := &http.Client{Transport: transport}
	resp := doGet(t, client, ts.URL)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(0), fired.Load(), "a request that was never throttled has not recovered")
}

func TestRateLimitGateExtend(t *testing.T) {
	gate := sqapi.NewRateLimitGate()
	gate.Extend(time.Now().Add(50 * time.Millisecond))

	start := time.Now()
	gate.WaitIfBlocked(context.Background())
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond,
		"WaitIfBlocked must sleep at least until the deadline")
	assert.Less(t, elapsed, 200*time.Millisecond,
		"WaitIfBlocked must not sleep substantially past the deadline")
}

func TestRateLimitGateExtendDoesNotShorten(t *testing.T) {
	gate := sqapi.NewRateLimitGate()
	gate.Extend(time.Now().Add(100 * time.Millisecond))
	gate.Extend(time.Now().Add(10 * time.Millisecond)) // earlier — ignored

	start := time.Now()
	gate.WaitIfBlocked(context.Background())
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 80*time.Millisecond,
		"a later Extend with an earlier deadline must not shorten the wait")
}

func TestRateLimitGateConcurrent(t *testing.T) {
	gate := sqapi.NewRateLimitGate()
	gate.Extend(time.Now().Add(80 * time.Millisecond))

	var wg sync.WaitGroup
	elapsed := make([]time.Duration, 5)
	for i := range elapsed {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			start := time.Now()
			gate.WaitIfBlocked(context.Background())
			elapsed[idx] = time.Since(start)
		}(i)
	}
	wg.Wait()

	for i, d := range elapsed {
		assert.GreaterOrEqual(t, d, 60*time.Millisecond,
			"goroutine %d should have waited on the gate", i)
	}
}

// TestRateLimitGateExtendReturnsAddedDelta verifies that extend reports
// the wall-clock pause it actually contributed. The first extend returns
// the full wait; subsequent piggy-back extends to the same or earlier
// deadline return zero; later-extending calls return only the delta
// beyond the prior deadline.
func TestRateLimitGateExtendReturnsAddedDelta(t *testing.T) {
	gate := sqapi.NewRateLimitGate()

	first := gate.Extend(time.Now().Add(60 * time.Second))
	assert.InDelta(t, 60*time.Second, first, float64(time.Second),
		"first extend should return the full pause")

	piggy := gate.Extend(time.Now().Add(60 * time.Second))
	assert.LessOrEqual(t, piggy, 100*time.Millisecond,
		"piggy-back extend to same deadline should add near-zero")

	earlier := gate.Extend(time.Now().Add(30 * time.Second))
	assert.Equal(t, time.Duration(0), earlier,
		"extend to an earlier deadline should add zero")

	longer := gate.Extend(time.Now().Add(90 * time.Second))
	assert.InDelta(t, 30*time.Second, longer, float64(time.Second),
		"extending past the current deadline should add only the delta")
}

// TestRateLimitGateExtendAfterWindowExpired verifies that a new extend
// arriving after the previous window has ended counts only the new
// pause, not the gap of unpaused time in between.
func TestRateLimitGateExtendAfterWindowExpired(t *testing.T) {
	gate := sqapi.NewRateLimitGate()
	gate.Extend(time.Now().Add(20 * time.Millisecond))
	time.Sleep(40 * time.Millisecond) // let the window expire

	added := gate.Extend(time.Now().Add(50 * time.Millisecond))
	assert.GreaterOrEqual(t, added, 40*time.Millisecond,
		"new window after expiry should report its own pause")
	assert.LessOrEqual(t, added, 60*time.Millisecond,
		"new window after expiry should exclude the unpaused gap")
}

// TestRateLimitGateContextCancellation verifies that WaitIfBlocked aborts
// promptly when the caller's context is cancelled, instead of blocking
// for the full remaining duration of the gate.
func TestRateLimitGateContextCancellation(t *testing.T) {
	gate := sqapi.NewRateLimitGate()
	gate.Extend(time.Now().Add(5 * time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	gate.WaitIfBlocked(ctx)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 500*time.Millisecond,
		"WaitIfBlocked must return promptly when context is cancelled")
}
