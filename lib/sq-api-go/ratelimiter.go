// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// LatencyObserver is invoked once per physical HTTP round trip — every
// attempt including retries — with that attempt's wall-clock duration.
// Implementations must be safe for concurrent use: throttleTransport
// invokes it directly from RoundTrip, which can run on many goroutines.
type LatencyObserver func(d time.Duration)

// defaultSlidingWindow is the trailing duration NewSlidingWindowLimiter
// enforces maxPerMinute over.
const defaultSlidingWindow = 60 * time.Second

// SlidingWindowLimiter is a mutex-protected sliding-log rate limiter. It
// tracks the timestamps of calls granted within the trailing window
// (oldest-first) and blocks Wait until fewer than maxPerMinute of them
// remain within that window.
//
// The zero value is not usable; construct with NewSlidingWindowLimiter.
type SlidingWindowLimiter struct {
	mu           sync.Mutex
	maxPerMinute int
	window       time.Duration // always defaultSlidingWindow via the public constructor
	timestamps   []time.Time   // granted-call timestamps within window, oldest-first
}

// NewSlidingWindowLimiter constructs a SlidingWindowLimiter that admits
// at most maxPerMinute calls in any trailing 60-second window.
//
// maxPerMinute <= 0 is clamped to 1 rather than panicking: this limiter
// guards outbound calls to SonarQube Cloud, and a misconfigured caller
// (e.g. an unset config value parsed as 0) should degrade to "very slow"
// — one call per minute — rather than crash a long-running migration.
func NewSlidingWindowLimiter(maxPerMinute int) *SlidingWindowLimiter {
	return newSlidingWindowLimiter(maxPerMinute, defaultSlidingWindow)
}

// newSlidingWindowLimiter is the shared constructor behind
// NewSlidingWindowLimiter. It takes an explicit window so tests can
// shrink it well below 60s and stay fast without changing the public
// API's fixed one-minute contract.
func newSlidingWindowLimiter(maxPerMinute int, window time.Duration) *SlidingWindowLimiter {
	if maxPerMinute <= 0 {
		maxPerMinute = 1
	}
	return &SlidingWindowLimiter{maxPerMinute: maxPerMinute, window: window}
}

// Wait blocks until either a slot opens within the trailing window or
// ctx is done, returning ctx.Err() in the latter case. On success it
// records the new call's timestamp before returning.
//
// Wait never busy-loops: when it must wait, it sleeps until the oldest
// granted timestamp is due to age out of the window, then re-checks —
// recomputing from scratch, since a concurrent Wait may have granted (or
// aged out) timestamps in the meantime.
func (l *SlidingWindowLimiter) Wait(ctx context.Context) error {
	for {
		granted, wait := l.tryAcquire()
		if granted {
			return nil
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// tryAcquire purges timestamps that have aged out of the window and
// either grants a new slot (recording the timestamp and returning
// granted=true) or reports how long to sleep before the oldest
// surviving timestamp ages out and a slot should next be available.
func (l *SlidingWindowLimiter) tryAcquire() (granted bool, wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)

	// timestamps is oldest-first, so stale entries are a prefix.
	i := 0
	for i < len(l.timestamps) && l.timestamps[i].Before(cutoff) {
		i++
	}
	l.timestamps = l.timestamps[i:]

	if len(l.timestamps) < l.maxPerMinute {
		l.timestamps = append(l.timestamps, now)
		return true, 0
	}

	wait = l.timestamps[0].Add(l.window).Sub(now)
	if wait <= 0 {
		// Another goroutine's purge is due; retry almost immediately
		// rather than computing a zero/negative timer duration.
		wait = time.Millisecond
	}
	return false, wait
}

// throttleTransport proactively rate-limits and times physical HTTP
// round trips. It is deliberately placed innermost in the transport
// stack — wrapping only the base http.Transport — so it sees every
// physical attempt, including ones retryTransport re-issues after a
// 429/5xx: each attempt consumes SonarQube Cloud's rate budget and each
// attempt's real wall-clock cost is what callers of WithLatencyObserver
// want sampled.
//
// Both limiter and observer are optional (nil-safe). If both are nil,
// RoundTrip is a transparent passthrough to inner.
type throttleTransport struct {
	inner    http.RoundTripper
	limiter  *SlidingWindowLimiter
	observer LatencyObserver
}

func (t *throttleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.limiter != nil {
		if err := t.limiter.Wait(req.Context()); err != nil {
			return nil, fmt.Errorf("waiting for API rate limit slot: %w", err)
		}
	}

	start := time.Now()
	resp, err := t.inner.RoundTrip(req)
	if t.observer != nil {
		t.observer(time.Since(start))
	}
	return resp, err
}
