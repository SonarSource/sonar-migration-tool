// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi

import (
	"bytes"
	"context"
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

// RecoveryLogFunc is called once when a request that was paused by one or
// more 429 rate-limit responses finally completes with a non-429 outcome —
// i.e. the throttling cleared and this request got through. It receives the
// HTTP method, URL path, the number of retries that preceded the recovery,
// and the cumulative time this request spent waiting on rate-limit backoff.
// It is NOT called when a request exhausts its retry schedule still seeing
// 429 (that is a failure, not a resume), nor for 5xx/network-error retries.
type RecoveryLogFunc func(method, url string, retries int, waited time.Duration)

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

func (g *rateLimitGate) waitIfBlocked(ctx context.Context) {
	g.mu.Lock()
	until := g.blocked
	g.mu.Unlock()
	if d := time.Until(until); d > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(d):
		}
	}
}

// extend pushes the gate's deadline out to `until` (no-op if `until` is
// not later than the current deadline) and returns the wall-clock
// duration this call adds beyond what was already pending. When several
// concurrent requests park on the same window, only the first one
// reports the full wait; subsequent extends with the same or earlier
// deadlines return 0. When a new 429 arrives after the previous window
// expired, the prior unpaused gap is excluded from the returned delta.
// Callers use this to record wall-clock pause time accurately rather
// than summing per-request waits, which would over-count under high
// concurrency.
func (g *rateLimitGate) extend(until time.Time) (added time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	effectivePrev := g.blocked
	if effectivePrev.Before(now) {
		effectivePrev = now
	}
	if until.After(g.blocked) {
		g.blocked = until
	}
	added = g.blocked.Sub(effectivePrev)
	if added < 0 {
		added = 0
	}
	return added
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
	recoveryFn    RecoveryLogFunc
	observer      RateLimitObserver
	gate          *rateLimitGate
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// pausedForRateLimit records whether this request slept for at least one
	// 429 backoff; totalWaited accumulates that backoff. They drive the
	// recovery callback so we log a resume only for requests that were
	// genuinely throttled (not 5xx/network retries) and then got through.
	var (
		pausedForRateLimit bool
		totalWaited        time.Duration
	)

	for attempt := 0; ; attempt++ {
		t.waitOnGate(req.Context())

		resp, err := t.inner.RoundTrip(req)
		if !shouldRetry(resp, err) {
			if pausedForRateLimit {
				t.fireRecovery(req, attempt, totalWaited)
			}
			return resp, err
		}

		d := t.planRetry(req, resp, attempt)
		if !d.more {
			// Give-up path: planRetry left resp.Body readable so the
			// caller still sees the real status and payload.
			return resp, err
		}
		if d.is429 {
			pausedForRateLimit = true
			totalWaited += d.chosen
		}

		t.logAttempt(req, resp, attempt, d.total)
		select {
		case <-req.Context().Done():
			return resp, req.Context().Err()
		case <-time.After(jitter(d.wait)):
		}
	}
}

// retryDecision bundles everything RoundTrip needs to know once a
// retryable outcome has been fully processed by planRetry.
type retryDecision struct {
	wait   time.Duration // schedule value for this attempt, before jitter
	chosen time.Duration // wait when retrying, zero when giving up
	total  int           // total attempts in the applicable schedule
	more   bool          // whether another attempt will be made
	is429  bool          // whether this outcome was a rate limit
}

// planRetry processes one retryable outcome: it classifies the response,
// decides whether another attempt is both scheduled and physically
// possible, disposes of the response body accordingly, and notifies the
// rate-limit observer.
//
// The body disposition is the crux of issue #505. When retrying, the
// body is drained and closed so the connection returns to the pool.
// When giving up, the body must stay READABLE — the caller is about to
// receive this response with a nil error and needs the status and
// payload to build a meaningful HTTP error. Closing it there produced
// the opaque "http: read on closed response body" that masked the real
// upstream status.
func (t *retryTransport) planRetry(req *http.Request, resp *http.Response, attempt int) retryDecision {
	rl := t.observeRateLimit(resp)

	wait, total, more := t.nextWait(rl, attempt)
	// Decide before destroying anything: a retry is only real if the
	// schedule allows it AND the request body can be transmitted again.
	more = more && rewindRequestBody(req)

	if more {
		drainAndClose(resp)
	} else {
		restoreConsumedBody(resp, rl.body)
	}

	chosen := chosenWaitOrZero(wait, more)
	t.fireObserver(rl, chosen, t.wallClockAdded(rl, chosen, more))

	return retryDecision{wait: wait, chosen: chosen, total: total, more: more, is429: rl.is429}
}

// wallClockAdded returns the gate-deduplicated wall-clock pause this
// event actually contributes. Sonar-classified 429s share the rate-limit
// gate, so only the first (or extending) event of a window reports real
// time; everything else contributes its own wait, and a request that is
// giving up contributes nothing at all.
func (t *retryTransport) wallClockAdded(rl rateLimitInfo, chosen time.Duration, more bool) time.Duration {
	if !more {
		return 0
	}
	if rl.isSonarRateLimit() {
		return t.extendGate(time.Now().Add(chosen))
	}
	return chosen
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

// rewindRequestBody restores req.Body so the request can be transmitted
// again, reporting whether that was possible.
//
// Every RoundTripper — including the inner http.Transport — consumes and
// closes req.Body. Re-issuing the same *http.Request without rewinding
// therefore sends an exhausted reader: with a known Content-Length the
// write fails outright ("ContentLength=N with Body length 0"), and with
// an unknown length it silently POSTs an EMPTY body, turning a failed
// write into a successful no-op call. http.Request.GetBody exists for
// exactly this and is populated automatically by http.NewRequest for
// *strings.Reader, *bytes.Reader and *bytes.Buffer — the three types
// every write in this client uses.
//
// A request that carries a body but has no GetBody cannot be replayed
// safely, so this reports false and the transport gives up rather than
// re-sending something different from what the caller asked for.
func rewindRequestBody(req *http.Request) bool {
	if req.Body == nil || req.Body == http.NoBody {
		return true // bodyless request (every GET) — nothing to rewind
	}
	if req.GetBody == nil {
		return false
	}
	body, err := req.GetBody()
	if err != nil {
		return false
	}
	// The previous body was already closed by the inner RoundTripper,
	// which the http.RoundTripper contract requires of every attempt.
	req.Body = body
	return true
}

// restoreConsumedBody puts back the bytes observeRateLimit peeked at so
// the caller reads the response exactly as the server sent it.
// Only 429s are peeked at (to classify them); for every other retryable
// status nothing was consumed and resp is handed back untouched.
func restoreConsumedBody(resp *http.Response, consumed []byte) {
	if resp == nil || len(consumed) == 0 {
		return
	}
	resp.Body = &rejoinedBody{
		Reader: io.MultiReader(bytes.NewReader(consumed), resp.Body),
		closer: resp.Body,
	}
}

// rejoinedBody re-presents bytes already read from a response body while
// still closing the original body, so the caller closing the response
// releases the underlying connection as usual.
type rejoinedBody struct {
	io.Reader
	closer io.Closer
}

func (b *rejoinedBody) Close() error { return b.closer.Close() }

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
// it, and returns a rateLimitInfo describing what was found. The bytes
// it consumes are returned in the `body` field: planRetry either
// discards them with the rest of the body (drainAndClose, when
// retrying) or splices them back in front of the remainder
// (restoreConsumedBody, when giving up). For non-429 responses it
// returns the zero rateLimitInfo without touching the body.
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

func (t *retryTransport) waitOnGate(ctx context.Context) {
	if t.gate != nil {
		t.gate.waitIfBlocked(ctx)
	}
}

func (t *retryTransport) extendGate(until time.Time) time.Duration {
	if t.gate == nil {
		return 0
	}
	return t.gate.extend(until)
}

// fireRecovery delivers a one-shot "throttling cleared, request got through"
// signal to the configured RecoveryLogFunc. retries is the number of failed
// attempts that preceded the successful one; waited is the cumulative
// rate-limit backoff this request slept through.
func (t *retryTransport) fireRecovery(req *http.Request, retries int, waited time.Duration) {
	if t.recoveryFn == nil {
		return
	}
	t.recoveryFn(req.Method, req.URL.Path, retries, waited)
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
// wait is what this request will sleep; wallClockAdded is the
// gate-deduplicated wall-clock pause this event actually contributed
// (equals wait for non-gated 429s, 0 for piggy-back gated waits, or the
// delta beyond the prior window for an extending gated wait).
func (t *retryTransport) fireObserver(rl rateLimitInfo, wait, wallClockAdded time.Duration) {
	if t.observer == nil || !rl.is429 {
		return
	}
	t.observer(RateLimitEvent{
		Kind:           rl.kind,
		RetryAfter:     rl.retryAfter,
		WaitChosen:     wait,
		WallClockAdded: wallClockAdded,
		BodySnippet:    snapshotBody(rl.body),
		Headers:        snapshotHeaders(rl.headers),
		ObservedAt:     time.Now(),
	})
}

// jitter adds a random amount up to 50% of d to d.
func jitter(d time.Duration) time.Duration {
	return d + time.Duration(float64(d)*0.5*rand.Float64())
}
