// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi

import (
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// defaultBackoff is the base wait duration between retries triggered by
// 5xx responses or network errors. Attempt 1 fails → wait
// defaultBackoff[0] before attempt 2, etc.
var defaultBackoff = []time.Duration{
	100 * time.Millisecond,
	200 * time.Millisecond,
	400 * time.Millisecond,
}

// sqc429Backoff is the long retry schedule applied when a 429 response
// is classified as KindSQCRateLimit. SonarQube Cloud's documented
// guidance is to "wait a few minutes before retrying" — the per-attempt
// values escalate so a busy-cluster blip recovers quickly while a
// sustained limit gives the bucket time to refill.
var sqc429Backoff = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
	60 * time.Second,
	120 * time.Second,
	300 * time.Second,
}

// nonSQC429Backoff is the short schedule applied when a 429 response is
// classified as KindCloudflareRateLimit or KindUnknown429. The first
// (and only) retry happens quickly so a transient mis-classification
// recovers, but the transport refuses to pause for minutes against an
// upstream WAF block that may need operator intervention.
var nonSQC429Backoff = []time.Duration{
	2 * time.Second,
}

// retryAfterCap bounds the wall-clock pause induced by an honored
// Retry-After header. Without this, a misconfigured proxy could ask us
// to wait hours — well past any reasonable migration tolerance.
const retryAfterCap = 300 * time.Second

// retryableStatusCodes are HTTP status codes that warrant a retry.
// 429 (rate limit) and 5xx (server errors) are retried.
// 4xx client errors (except 429) are not retried — they indicate a caller mistake.
var retryableStatusCodes = map[int]bool{
	http.StatusTooManyRequests:     true,
	http.StatusInternalServerError: true,
	http.StatusBadGateway:          true,
	http.StatusServiceUnavailable:  true,
	http.StatusGatewayTimeout:      true,
}

// RetryLogFunc is called when a request is about to be retried.
// It receives the HTTP method, URL path, status code (0 if network error),
// current attempt number (1-based), and total attempts.
type RetryLogFunc func(method, url string, status, attempt, total int)

// rateLimitGate is a shared barrier that holds new requests when an
// SQC-classified 429 has been observed, until the chosen backoff has
// elapsed. Concurrent workers all park on the same deadline instead of
// each independently burning through retries against an exhausted
// bucket. Only Sonar-classified 429s extend the gate; Cloudflare/unknown
// 429s fail fast and do not pause sibling requests.
type rateLimitGate struct {
	mu      sync.Mutex
	blocked time.Time
}

func (g *rateLimitGate) waitIfBlocked() {
	g.mu.Lock()
	until := g.blocked
	g.mu.Unlock()
	if d := time.Until(until); d > 0 {
		time.Sleep(d)
	}
}

func (g *rateLimitGate) extend(until time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until.After(g.blocked) {
		g.blocked = until
	}
}

// retryTransport is an http.RoundTripper that retries failed requests
// with classification-aware backoff. 5xx and network errors use the
// default schedule. 429 responses are classified by classify429 and use
// the longer sqc429Backoff for application-layer SQC rate limits or the
// shorter nonSQC429Backoff for Cloudflare/unknown 429s.
//
// sqcBackoff and nonSQCBackoff are optional overrides. When nil, the
// transport falls back to backoff — preserving the single-schedule
// behavior expected by existing tests built on NewRetryTransport.
type retryTransport struct {
	inner         http.RoundTripper
	backoff       []time.Duration
	sqcBackoff    []time.Duration
	nonSQCBackoff []time.Duration
	logFn         RetryLogFunc
	observer      RateLimitObserver
	gate          *rateLimitGate
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var (
		resp *http.Response
		err  error
	)

	for attempt := 0; ; attempt++ {
		t.waitOnGate()

		resp, err = t.inner.RoundTrip(req)
		if !shouldRetry(resp, err) {
			return resp, err
		}

		rl := t.observeRateLimit(resp)
		drainAndClose(resp)

		wait, total, more := t.nextWait(rl, attempt)
		t.fireObserver(rl, chosenWaitOrZero(wait, more))
		if !more {
			return resp, err
		}

		t.logAttempt(req, resp, attempt, total)
		if rl.isSonarRateLimit() {
			t.extendGate(time.Now().Add(wait))
		}
		time.Sleep(jitter(wait))
	}
}

// chosenWaitOrZero returns wait when a retry will follow, or zero when
// the transport is about to give up. The observer records the zero so
// the report can distinguish "this 429 cost N seconds of pause" from
// "this 429 killed the task outright."
func chosenWaitOrZero(wait time.Duration, more bool) time.Duration {
	if more {
		return wait
	}
	return 0
}

// shouldRetry reports whether the (resp, err) pair from inner.RoundTrip
// warrants another attempt. Network errors (nil resp or non-nil err)
// always retry; otherwise the status code must be in retryableStatusCodes.
func shouldRetry(resp *http.Response, err error) bool {
	if err != nil || resp == nil {
		return true
	}
	return retryableStatusCodes[resp.StatusCode]
}

// drainAndClose empties the response body so the underlying connection
// can be reused, then closes it. Safe to call with a nil response.
func drainAndClose(resp *http.Response) {
	if resp == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// rateLimitInfo summarises what was learned from inspecting a 429
// response. For non-429 outcomes (5xx, network errors) all fields are
// zero values, signalling "use the default schedule, no observer call".
type rateLimitInfo struct {
	kind       RateLimitKind
	retryAfter time.Duration
	body       []byte
	headers    http.Header
	is429      bool
}

func (r rateLimitInfo) isSonarRateLimit() bool {
	return r.is429 && r.kind == KindSQCRateLimit
}

// observeRateLimit reads a 429 response's body and headers, classifies
// it, and returns a rateLimitInfo describing what was found. The body
// is consumed but not yet closed — drainAndClose handles the remainder
// and connection cleanup afterwards. For non-429 responses it returns
// the zero rateLimitInfo without touching the body.
func (t *retryTransport) observeRateLimit(resp *http.Response) rateLimitInfo {
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		return rateLimitInfo{}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, bodySnippetMax+1))
	return rateLimitInfo{
		kind:       classify429(resp.Header, body),
		retryAfter: parseRetryAfter(resp.Header),
		body:       body,
		headers:    resp.Header,
		is429:      true,
	}
}

// nextWait picks the duration to sleep before the next attempt. Returns
// (wait, totalAttempts, more) where `more` is false when no further
// retries are available — the caller should return the current response
// to its caller.
func (t *retryTransport) nextWait(rl rateLimitInfo, attempt int) (time.Duration, int, bool) {
	schedule := t.scheduleFor(rl)
	total := len(schedule) + 1
	if attempt >= len(schedule) {
		return 0, total, false
	}
	base := schedule[attempt]
	if rl.isSonarRateLimit() && rl.retryAfter > 0 {
		base = clampRetryAfter(rl.retryAfter, base)
	}
	return base, total, true
}

// scheduleFor returns the backoff schedule that applies to the current
// retryable outcome. 429s use the kind-specific schedule (with
// fallback to the default when the override is nil); 5xx and network
// errors use the default schedule.
func (t *retryTransport) scheduleFor(rl rateLimitInfo) []time.Duration {
	if !rl.is429 {
		return t.backoff
	}
	override := t.nonSQCBackoff
	if rl.kind == KindSQCRateLimit {
		override = t.sqcBackoff
	}
	if override != nil {
		return override
	}
	return t.backoff
}

// clampRetryAfter returns a duration not less than scheduled and not
// more than retryAfterCap. Honoring Retry-After lets the server's
// hint take precedence over our schedule when it's longer; the cap
// protects against pathological hints.
func clampRetryAfter(server, scheduled time.Duration) time.Duration {
	d := server
	if scheduled > d {
		d = scheduled
	}
	if d > retryAfterCap {
		d = retryAfterCap
	}
	return d
}

func (t *retryTransport) waitOnGate() {
	if t.gate != nil {
		t.gate.waitIfBlocked()
	}
}

func (t *retryTransport) extendGate(until time.Time) {
	if t.gate != nil {
		t.gate.extend(until)
	}
}

func (t *retryTransport) logAttempt(req *http.Request, resp *http.Response, attempt, total int) {
	if t.logFn == nil {
		return
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	t.logFn(req.Method, req.URL.Path, status, attempt+1, total)
}

// fireObserver delivers the rate-limit event to the configured callback.
// Called from observeRateLimit so subsequent body draining and retry
// decisions don't race with observer-side accounting.
func (t *retryTransport) fireObserver(rl rateLimitInfo, wait time.Duration) {
	if t.observer == nil || !rl.is429 {
		return
	}
	t.observer(RateLimitEvent{
		Kind:        rl.kind,
		RetryAfter:  rl.retryAfter,
		WaitChosen:  wait,
		BodySnippet: snapshotBody(rl.body),
		Headers:     snapshotHeaders(rl.headers),
		ObservedAt:  time.Now(),
	})
}

// jitter adds a random amount up to 50% of d to d.
func jitter(d time.Duration) time.Duration {
	return d + time.Duration(float64(d)*0.5*rand.Float64())
}
