// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// minConcurrency is the lowest concurrency figure a ConcurrencyLimiter
	// will ever report, regardless of how low observed latency or the
	// target rate are.
	minConcurrency = 1
	// maxConcurrencyCeiling is the highest concurrency figure a
	// ConcurrencyLimiter will ever report, regardless of how high observed
	// latency or the target rate are.
	maxConcurrencyCeiling = 100
)

// latencyWindow is a 30s "tumbling" accumulator of observed API call
// latency samples: a mutex-protected running sum and count that resets
// every time it's drained. It is deliberately NOT a timestamped sliding
// log — callers drain it on a fixed tick (see ConcurrencyLimiter.Start)
// and the accumulator starts fresh for the next interval.
type latencyWindow struct {
	mu    sync.Mutex
	sum   time.Duration
	count int
}

// add records one latency sample.
func (w *latencyWindow) add(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sum += d
	w.count++
}

// drainAverage returns the average of all samples added since the last
// drain, and resets the accumulator. ok is false if no samples were added.
func (w *latencyWindow) drainAverage() (avg time.Duration, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.count == 0 {
		return 0, false
	}
	avg = w.sum / time.Duration(w.count)
	w.sum = 0
	w.count = 0
	return avg, true
}

// desiredConcurrency implements ceil(targetRatePerMin * avgLatencySeconds / 60),
// then clamps the result via clampConcurrency. The arithmetic is carried
// out in float64 seconds to avoid integer-division bugs (e.g. avgLatency
// values below one second would otherwise floor to zero).
func desiredConcurrency(targetRatePerMin int, avgLatency time.Duration) int {
	avgLatencySeconds := avgLatency.Seconds()
	raw := float64(targetRatePerMin) * avgLatencySeconds / 60.0
	return clampConcurrency(int(math.Ceil(raw)))
}

// clampConcurrency clamps n to [minConcurrency, maxConcurrencyCeiling].
func clampConcurrency(n int) int {
	if n < minConcurrency {
		return minConcurrency
	}
	if n > maxConcurrencyCeiling {
		return maxConcurrencyCeiling
	}
	return n
}

// ConcurrencyLimiter replaces the old Executor.Sem role: rather than a
// fixed-capacity channel whose cap() every fan-out site reads
// independently, it exposes a live Current() figure that a background
// goroutine recalculates periodically from recently-observed API call
// latency (#573), targeting a configured calls/min rate. Like the old
// cap(e.Sem) reads, Current() is just a capacity number read
// independently at each fan-out site — it is NOT a semaphore, and nothing
// in this package acquires/releases it.
type ConcurrencyLimiter struct {
	current atomic.Int32

	// fixed is true when this limiter was built via
	// NewFixedConcurrencyLimiter: Current() never changes and Start is a
	// complete no-op (no goroutine, no log noise).
	fixed bool

	window *latencyWindow
	target int // target rate per minute, dynamic mode only

	logger *slog.Logger

	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

// NewFixedConcurrencyLimiter returns a limiter whose Current() always
// returns n (used when --concurrency was explicitly set by the user, and
// by all existing test fixtures that construct a fixed-size pool today).
func NewFixedConcurrencyLimiter(n int) *ConcurrencyLimiter {
	l := &ConcurrencyLimiter{fixed: true}
	l.current.Store(int32(n))
	return l
}

// NewDynamicConcurrencyLimiter returns a limiter that starts at
// clampConcurrency(initial) and, once Start is called, recalculates
// Current() periodically from observed latency, aiming for
// targetRatePerMin calls/min. logger may be nil (falls back to
// slog.Default(), matching this package's convention).
func NewDynamicConcurrencyLimiter(initial, targetRatePerMin int, logger *slog.Logger) *ConcurrencyLimiter {
	if logger == nil {
		logger = slog.Default()
	}
	l := &ConcurrencyLimiter{
		window: &latencyWindow{},
		target: targetRatePerMin,
		logger: logger,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	l.current.Store(int32(clampConcurrency(initial)))
	return l
}

// Current returns the limiter's current concurrency figure. Lock-free, so
// it's safe to call from the many fan-out sites that used to read
// cap(e.Sem).
func (l *ConcurrencyLimiter) Current() int {
	return int(l.current.Load())
}

// Observe records one API call's latency sample. Safe to call on a fixed
// limiter (no-op effect on Current(), but harmless to record).
func (l *ConcurrencyLimiter) Observe(d time.Duration) {
	if l.fixed || l.window == nil {
		return
	}
	l.window.add(d)
}

// Start launches the recalculation loop at the given interval. It is a
// no-op for a fixed limiter (no goroutine is started at all, so there's
// no log noise). Safe to call once; the loop stops when ctx is done or
// Stop is called.
//
// On each tick: the latency window is drained. If no samples were
// observed this interval, DEBUG-logs that concurrency was left unchanged
// and skips recalculation (it does not reset to some default). Otherwise
// it computes desiredConcurrency(l.target, avg), stores it, and
// DEBUG-logs the previous value, new value, avg latency (as
// milliseconds), and target rate — this log line fires on every tick that
// had samples, even when the value didn't change.
func (l *ConcurrencyLimiter) Start(ctx context.Context, interval time.Duration) {
	if l.fixed {
		return
	}
	go func() {
		defer close(l.doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-l.stopCh:
				return
			case <-ticker.C:
				l.recalculate()
			}
		}
	}()
}

// recalculate drains the latency window and, if any samples were
// observed, updates l.current and logs the transition.
func (l *ConcurrencyLimiter) recalculate() {
	avg, ok := l.window.drainAverage()
	if !ok {
		l.logger.Debug("concurrency left unchanged: no latency samples observed this interval")
		return
	}
	prev := l.Current()
	next := desiredConcurrency(l.target, avg)
	l.current.Store(int32(next))
	l.logger.Debug("concurrency recalculated",
		"previous", prev,
		"current", next,
		"avg_latency_ms", avg.Milliseconds(),
		"target_rate_per_min", l.target,
	)
}

// Stop stops the recalculation loop. Idempotent, and safe to call even if
// Start was never called or the limiter is fixed (in which case stopCh is
// nil, since Start never starts a goroutine to stop) — in either of
// those cases Stop only closes stopCh, when there is one, and does not
// wait on doneCh, to avoid blocking or panicking.
func (l *ConcurrencyLimiter) Stop() {
	l.stopOnce.Do(func() {
		if l.stopCh != nil {
			close(l.stopCh)
		}
	})
}
