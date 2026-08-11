// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// sonar500Body is verbatim what SonarQube Cloud returns from
// GET /api/alm_integration/show_bound_organization for an unbound
// organization — the response that triggered issue #505.
const sonar500Body = `{"errors":[{"msg":"An unexpected error occurred. Please try again later."}]}`

// longSonar429Body is a Sonar-shaped 429 payload deliberately longer than
// the classifier's peek window, so a test reading it back proves the
// peeked prefix was spliced in front of the untouched remainder rather
// than silently dropped or duplicated.
var longSonar429Body = `{"errors":[{"msg":"` + strings.Repeat("x", 3*sqapi.BodySnippetMax) + `"}]}`

// alwaysFailingServer returns a server that answers every request with
// the same status/headers/payload, plus a counter of requests received.
func alwaysFailingServer(t *testing.T, status int, headers map[string]string, payload string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(ts.Close)
	return ts, &attempts
}

// TestRetryTransportGiveUpReturnsReadableBody is the regression test for
// issue #505: when a retryable status survives the whole schedule, the
// transport must hand the caller a response it can actually read, with
// the real status intact. Before the fix the body had been drained and
// closed, so io.ReadAll returned "http: read on closed response body"
// and the upstream status was lost.
func TestRetryTransportGiveUpReturnsReadableBody(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		payload string
	}{
		{
			name:    "500 that never recovers",
			status:  http.StatusInternalServerError,
			payload: sonar500Body,
		},
		{
			name:    "503 that never recovers",
			status:  http.StatusServiceUnavailable,
			payload: `{"errors":[{"msg":"SonarQube is restarting"}]}`,
		},
		{
			name:    "502 with an empty body",
			status:  http.StatusBadGateway,
			payload: "",
		},
		{
			name:    "429 that never recovers",
			status:  http.StatusTooManyRequests,
			payload: `{"errors":[{"msg":"rate limit exceeded"}]}`,
		},
		{
			name:    "429 whose body is longer than the classifier peek",
			status:  http.StatusTooManyRequests,
			payload: longSonar429Body,
		},
		{
			name:    "429 from cloudflare",
			status:  http.StatusTooManyRequests,
			headers: map[string]string{"CF-Ray": "abc123-IAD"},
			payload: "<html><body>Error 1015: You are being rate limited</body></html>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, attempts := alwaysFailingServer(t, tc.status, tc.headers, tc.payload)

			var (
				mu     sync.Mutex
				events []sqapi.RateLimitEvent
			)
			transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
				Inner:   http.DefaultTransport,
				Backoff: []time.Duration{0, 0},
				Observer: func(e sqapi.RateLimitEvent) {
					mu.Lock()
					defer mu.Unlock()
					events = append(events, e)
				},
			})

			client := &http.Client{Transport: transport}
			resp, err := client.Get(ts.URL)

			// The transport gave up, so it reports no error of its own —
			// the caller is expected to inspect the response.
			require.NoError(t, err)
			require.NotNil(t, resp)
			defer resp.Body.Close()

			assert.Equal(t, tc.status, resp.StatusCode,
				"the real upstream status must survive the exhausted retry schedule")

			got, readErr := io.ReadAll(resp.Body)
			require.NoError(t, readErr,
				"issue #505: the give-up path must not hand back a closed body")
			assert.Equal(t, tc.payload, string(got),
				"the caller must see the payload byte-for-byte, including any bytes the 429 classifier peeked at")

			assert.Equal(t, int32(3), attempts.Load(),
				"schedule of 2 backoffs means 3 attempts before giving up")

			mu.Lock()
			defer mu.Unlock()
			if tc.status != http.StatusTooManyRequests {
				assert.Empty(t, events, "non-429 outcomes must not reach the rate-limit observer")
				return
			}
			require.NotEmpty(t, events, "every 429 must be reported to the observer")
			last := events[len(events)-1]
			assert.Zero(t, last.WaitChosen,
				"the final 429 killed the request outright, so it cost no pause")
			assert.Zero(t, last.WallClockAdded)
		})
	}
}

// TestRetryTransportRecoveredResponseBodyIntact verifies the retry path
// is unchanged: the transport still retries, still fires the observer and
// recovery callbacks for 429s only, and the final response body is
// readable in full.
func TestRetryTransportRecoveredResponseBodyIntact(t *testing.T) {
	const successBody = `{"organization":"acme","alm":"github"}`

	cases := []struct {
		name         string
		failStatus   int
		failPayload  string
		wantObserved bool
		wantRecovery bool
	}{
		{
			name:         "429 then 200",
			failStatus:   http.StatusTooManyRequests,
			failPayload:  `{"errors":[{"msg":"rate limit exceeded"}]}`,
			wantObserved: true,
			wantRecovery: true,
		},
		{
			name:        "500 then 200",
			failStatus:  http.StatusInternalServerError,
			failPayload: sonar500Body,
		},
		{
			name:        "503 then 200",
			failStatus:  http.StatusServiceUnavailable,
			failPayload: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if attempts.Add(1) == 1 {
					w.WriteHeader(tc.failStatus)
					_, _ = io.WriteString(w, tc.failPayload)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, successBody)
			}))
			defer ts.Close()

			var (
				mu        sync.Mutex
				events    []sqapi.RateLimitEvent
				recovered int
			)
			transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
				Inner:      http.DefaultTransport,
				Backoff:    []time.Duration{0, 0},
				SQCBackoff: []time.Duration{0, 0},
				Observer: func(e sqapi.RateLimitEvent) {
					mu.Lock()
					defer mu.Unlock()
					events = append(events, e)
				},
				Recovery: func(_, _ string, _ int, _ time.Duration) {
					mu.Lock()
					defer mu.Unlock()
					recovered++
				},
			})

			client := &http.Client{Transport: transport}
			resp, err := client.Get(ts.URL)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, int32(2), attempts.Load(), "the failure must still be retried")

			got, readErr := io.ReadAll(resp.Body)
			require.NoError(t, readErr)
			assert.Equal(t, successBody, string(got), "the recovered response body must be intact")

			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.wantObserved, len(events) == 1, "observer fires for 429s only")
			if tc.wantObserved {
				assert.Equal(t, sqapi.KindSQCRateLimit, events[0].Kind)
				assert.Contains(t, events[0].BodySnippet, "rate limit exceeded",
					"the classifier's peek must still reach the observer")
			}
			if tc.wantRecovery {
				assert.Equal(t, 1, recovered, "a cleared 429 must report recovery")
			} else {
				assert.Zero(t, recovered, "5xx retries are not rate limiting")
			}
		})
	}
}

// bodyRecordingServer records the request body of every attempt.
func bodyRecordingServer(t *testing.T, failFirst int) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []string
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, string(body))
		n := len(seen)
		mu.Unlock()
		if n <= failFirst {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return ts, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// TestRetryTransportRewindsRequestBodyOnRetry proves a retried POST
// re-sends its payload. The transport re-issues the same *http.Request,
// whose Body the previous attempt already consumed and closed, so
// without an explicit rewind the second attempt either fails with
// "ContentLength=N with Body length 0" (fresh connection) or silently
// sends nothing.
func TestRetryTransportRewindsRequestBodyOnRetry(t *testing.T) {
	const form = "name=Jenkins&url=https%3A%2F%2Fmy.jenkins.server%2Fsonar-webhook%2F&organization=acme"

	cases := []struct {
		name        string
		keepAlives  bool
		failFirst   int
		wantBodies  int
		wantRetries bool
	}{
		{name: "connection reused", keepAlives: true, failFirst: 1, wantBodies: 2, wantRetries: true},
		{name: "fresh connection per attempt", keepAlives: false, failFirst: 1, wantBodies: 2, wantRetries: true},
		{name: "two retries, fresh connections", keepAlives: false, failFirst: 2, wantBodies: 3, wantRetries: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, bodies := bodyRecordingServer(t, tc.failFirst)

			inner := &http.Transport{DisableKeepAlives: !tc.keepAlives}
			defer inner.CloseIdleConnections()
			transport := sqapi.NewRetryTransport(inner, []time.Duration{0, 0})

			req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/webhooks/create", strings.NewReader(form))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			client := &http.Client{Transport: transport}
			resp, err := client.Do(req)
			require.NoError(t, err, "a retried POST must not fail on an exhausted request body")
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)

			got := bodies()
			require.Len(t, got, tc.wantBodies)
			for i, b := range got {
				assert.Equal(t, form, b, "attempt %d must carry the full request body", i+1)
			}
		})
	}
}

// TestRetryTransportRefusesToRetryUnrewindableBody verifies the
// transport declines to replay a request whose body it cannot
// regenerate. Retrying it would transmit an empty payload and could turn
// a failed write into a silently different, apparently-successful call.
func TestRetryTransportRefusesToRetryUnrewindableBody(t *testing.T) {
	ts, bodies := bodyRecordingServer(t, 1)

	transport := sqapi.NewRetryTransport(http.DefaultTransport, []time.Duration{0, 0})

	// io.NopCloser hides the concrete reader type, so http.NewRequest
	// cannot populate GetBody.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/webhooks/create",
		io.NopCloser(strings.NewReader("name=Jenkins")))
	require.NoError(t, err)
	require.Nil(t, req.GetBody, "test premise: this request cannot be replayed")

	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
		"the real status must be surfaced instead of a silently-empty retry")
	assert.Len(t, bodies(), 1, "an unrewindable request must be attempted exactly once")

	_, readErr := io.ReadAll(resp.Body)
	assert.NoError(t, readErr, "the response must still be readable")
}

// TestRetryTransportGiveUpBodyClosesUnderlyingBody verifies the wrapper
// installed on the give-up path still releases the original body, so the
// connection is returned to the pool when the caller closes the response.
func TestRetryTransportGiveUpBodyClosesUnderlyingBody(t *testing.T) {
	inner := &countingCloserTransport{payload: `{"errors":[{"msg":"rate limit exceeded"}]}`}
	transport := sqapi.NewRetryTransport(inner, []time.Duration{0})

	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/api/test", nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)

	got, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	assert.Equal(t, inner.payload, string(got))

	require.NoError(t, resp.Body.Close())
	assert.Equal(t, int32(2), inner.closes.Load(),
		"one close per attempt: the drained retry plus the caller's close of the restored body")
}

// countingCloserTransport always answers 429 and counts body closes.
type countingCloserTransport struct {
	payload string
	closes  atomic.Int32
}

func (c *countingCloserTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{},
		Body:       &countingCloser{Reader: strings.NewReader(c.payload), closes: &c.closes},
		Request:    req,
	}, nil
}

type countingCloser struct {
	io.Reader
	closes *atomic.Int32
}

func (c *countingCloser) Close() error {
	c.closes.Add(1)
	return nil
}
