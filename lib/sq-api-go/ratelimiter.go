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
	window       time.Duration   // always defaultSlidingWindow via the public constructor
	timestamps   []time.Time     // granted-call timestamps within window, oldest-first
	queue        []chan struct{} // FIFO of waiters blocked in Wait, oldest-first
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
// Wait is FIFO-fair: a caller that finds no slot available joins a
// queue and is only ever admitted once it reaches the head, rather
// than every blocked waiter racing the mutex whenever any timestamp
// ages out. Without that, a "thundering herd" of waiters all wake on
// the same event and whichever happens to win the mutex race takes
// the slot — which lets a waiter that arrived later repeatedly jump
// ahead of one that has been waiting longer, starving it indefinitely
// under sustained load.
func (l *SlidingWindowLimiter) Wait(ctx context.Context) error {
	ch := l.enqueue()
	defer l.dequeue(ch)

	for {
		wait := l.tryAcquire(ch)
		if wait <= 0 {
			return nil
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-ch:
			timer.Stop()
			// Woken because we reached the head of the queue and a
			// slot is believed to be available; re-check to claim it.
		case <-timer.C:
			// Our own deadline elapsed; re-check from scratch, since
			// we may or may not be at the head yet.
		}
	}
}

// enqueue appends a new waiter to the tail of the FIFO queue and
// returns its notification channel. wake sends on this channel
// (non-blocking, buffered by 1) once the waiter reaches the head of
// the queue and a slot is believed to be free.
func (l *SlidingWindowLimiter) enqueue() chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch := make(chan struct{}, 1)
	l.queue = append(l.queue, ch)
	return ch
}

// tryAcquire purges timestamps that have aged out of the window and,
// if ch is at the head of the FIFO queue and a slot is free, grants
// it: pops ch off the queue, records the new call's timestamp, and
// returns wait<=0. Otherwise it wakes the (possibly new) head of the
// queue if a slot just freed up, and returns how long the caller
// should sleep before re-checking.
//
// Only the head of the queue may ever be granted a slot here — that
// is what enforces FIFO order and prevents later arrivals from
// jumping ahead of a waiter that has been queued longer.
func (l *SlidingWindowLimiter) tryAcquire(ch chan struct{}) (wait time.Duration) {
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

	if len(l.timestamps) < l.maxPerMinute && len(l.queue) > 0 && l.queue[0] == ch {
		l.timestamps = append(l.timestamps, now)
		l.queue = l.queue[1:]
		l.wakeHead()
		return 0
	}

	// If a slot is free but ch isn't (yet) at the head, make sure the
	// actual head gets a chance to notice and claim it, rather than
	// leaving it asleep until its own timer happens to fire.
	if len(l.timestamps) < l.maxPerMinute {
		l.wakeHead()
	}

	if len(l.timestamps) == 0 {
		return time.Millisecond
	}
	wait = l.timestamps[0].Add(l.window).Sub(now)
	if wait <= 0 {
		// Another goroutine's purge is due; retry almost immediately
		// rather than computing a zero/negative timer duration.
		wait = time.Millisecond
	}
	return wait
}

// wakeHead notifies the current head of the queue, if any, that it
// should re-check for a free slot. Must be called with l.mu held.
// Non-blocking: the channel is buffered by 1, and if it's already
// pending a notification this is a harmless no-op.
func (l *SlidingWindowLimiter) wakeHead() {
	if len(l.queue) == 0 {
		return
	}
	select {
	case l.queue[0] <- struct{}{}:
	default:
	}
}

// dequeue removes ch from the queue, e.g. because its Wait call is
// returning (granted or giving up via context cancellation). No-op if
// ch is already removed. Wakes the new head so a slot freed by this
// departure (relevant when ch was removed without being granted, i.e.
// on cancellation) isn't left unnoticed.
func (l *SlidingWindowLimiter) dequeue(ch chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, c := range l.queue {
		if c == ch {
			l.queue = append(l.queue[:i], l.queue[i+1:]...)
			l.wakeHead()
			return
		}
	}
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
