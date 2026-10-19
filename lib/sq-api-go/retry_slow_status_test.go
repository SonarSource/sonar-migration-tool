// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// waitThenSend stands in for throttleTransport, which sits below the
// retry transport: it waits before handing the request on, as waiting
// for a rate-limit slot does.
type waitThenSend struct {
	wait time.Duration
	next http.RoundTripper
}

func (w waitThenSend) RoundTrip(r *http.Request) (*http.Response, error) {
	time.Sleep(w.wait)
	return w.next.RoundTrip(r)
}

// A fast 504 that arrives after a long wait for a rate-limit slot is a
// proxy blip, not a slow response: the slow-status clock starts when the
// request is sent, so it keeps its normal retry.
func TestRetryTransportSlowStatusTimedFromSend(t *testing.T) {
	const threshold = 100 * time.Millisecond
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
		Inner:   waitThenSend{wait: 2 * threshold, next: http.DefaultTransport},
		Backoff: []time.Duration{0, 0},
	})
	ctx := sqapi.WithoutRetryOnSlowStatus(context.Background(), threshold, http.StatusGatewayTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL, nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "the fast 504 must be retried")
	assert.Equal(t, int32(2), attempts.Load())
}

// The other side of the same clock: a 504 the server itself took long
// to send is still handed back at once.
func TestRetryTransportSlowStatusFromServerIsHandedBack(t *testing.T) {
	const threshold = 100 * time.Millisecond
	ts, attempts := alwaysFailingServerAfter(t, 2*threshold, http.StatusGatewayTimeout)

	transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
		Inner:   http.DefaultTransport,
		Backoff: []time.Duration{0, 0},
	})
	ctx := sqapi.WithoutRetryOnSlowStatus(context.Background(), threshold, http.StatusGatewayTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL, nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
	assert.Equal(t, int32(1), attempts.Load())
}

// A request that was throttled with a 429 and then fails with a slow
// status that is handed back early did not get through, so the
// rate-limit recovery callback must not report that it did.
func TestRetryTransportRecoveryNotFiredForHandedBackStatus(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":[{"msg":"rate limit exceeded"}]}`))
			return
		}
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	t.Cleanup(ts.Close)

	var fired atomic.Int32
	transport := sqapi.NewRetryTransportFull(sqapi.RetryTransportConfig{
		Inner:      http.DefaultTransport,
		Backoff:    []time.Duration{0},
		SQCBackoff: []time.Duration{5 * time.Millisecond, 5 * time.Millisecond},
		Recovery: func(_, _ string, _ int, _ time.Duration) {
			fired.Add(1)
		},
	})
	ctx := sqapi.WithoutRetryOnSlowStatus(context.Background(), 0, http.StatusGatewayTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL, nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
	assert.Equal(t, int32(2), attempts.Load(), "the 429 is retried, the 504 handed back")
	assert.Equal(t, int32(0), fired.Load(), "a request that failed must not be reported as recovered")
}

// alwaysFailingServerAfter answers every request with status after
// delay, and counts the requests.
func alwaysFailingServerAfter(t *testing.T, delay time.Duration, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(ts.Close)
	return ts, &attempts
}
