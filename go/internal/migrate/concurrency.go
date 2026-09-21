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

	// maxGrowthFactor bounds how much Current() may increase in a single
	// recalculation tick, regardless of what desiredConcurrency computes.
	//
	// Rising average latency is not proof that more concurrency would
	// help — it can just as easily be a SYMPTOM of the backend already
	// being overloaded by the current concurrency, in which case
	// following the raw formula upward makes the overload worse, not
	// better. Verified live against a real migrate run: two ticks took
	// concurrency from 25 (previous=25, current=78, avg_latency_ms=3100)
	// to the ceiling (previous=78, current=100, avg_latency_ms=4163),
	// where it stayed pegged for 10 minutes while latency climbed to
	// 30s — then the moment it finally dropped, ~75 projects that had
	// been queued on DynamicGate.Acquire the whole time were all
	// admitted within milliseconds of each other, dumping a burst of
	// simultaneous submissions onto SonarQube Cloud's CE task queue (a
	// shared backend resource this algorithm has no visibility into —
	// a poll request is cheap to answer regardless of how backed up the
	// actual analysis behind it is). That burst is what made the
	// migration look permanently stuck long after concurrency itself
	// had already self-corrected back down.
	//
	// Decreases are NOT capped by this — backing off fast when latency
	// degrades is exactly the safety behavior worth keeping quick;
	// only the climb needs slowing down, "slow start" style.
	maxGrowthFactor = 1.5
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
//
// Deliberately NOT divided by maxConcurrentTasksPerPhase: an automated
// review pass (Gitar) proposed dividing the result by that constant on
// the theory that up to that many tasks in one phase could each run
// their own fan-out at Current() simultaneously, letting aggregate
// in-flight demand run several times higher than the target rate. That
// reasoning doesn't hold up:
//   - SlidingWindowLimiter is what actually enforces the real cap —
//     calls beyond api_max_rate_per_min queue inside Wait() regardless
//     of how much concurrency requests them, so this was never needed
//     to avoid exceeding the configured rate.
//   - In practice only one task tends to dominate a phase's fan-out at
//     any given time (importProjectData, the long pole in every real
//     run analyzed for #573) — dividing by up to 6 systematically
//     under-drives concurrency in exactly that common case, working
//     against the whole point of the feature.
//   - It silently contradicted the issue's own worked examples
//     (200ms→5, 2s→50 at 1500/min) and shipped with no test updates;
//     TestDesiredConcurrency's existing cases caught it immediately.
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
// returns n. Production code no longer constructs one this way —
// newConcurrencyLimiter is always dynamic (#573 follow-up) — so this
// exists for test fixtures that want a stable, non-recalculating pool.
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
// observed this interval, INFO-logs that concurrency was left unchanged
// and skips recalculation (it does not reset to some default). Otherwise
// it computes desiredConcurrency(l.target, avg), applies capGrowth (an
// increase is capped to at most maxGrowthFactor times the previous
// value; a decrease is applied in full), stores the result, and
// INFO-logs the previous value, the new (possibly capped) value, the
// uncapped desired value, avg latency (as milliseconds), and target
// rate — this log line fires on every tick that had samples, even when
// the value didn't change.
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
// observed, updates l.current and logs the transition. An increase is
// capped by capGrowth (see maxGrowthFactor); a decrease is applied in
// full, immediately.
func (l *ConcurrencyLimiter) recalculate() {
	avg, ok := l.window.drainAverage()
	if !ok {
		l.logger.Info("concurrency left unchanged: no latency samples observed this interval")
		return
	}
	prev := l.Current()
	desired := desiredConcurrency(l.target, avg)
	next := capGrowth(prev, desired)
	l.current.Store(int32(next))
	l.logger.Info("concurrency recalculated",
		"previous", prev,
		"current", next,
		"desired", desired,
		"avg_latency_ms", avg.Milliseconds(),
		"target_rate_per_min", l.target,
	)
}

// capGrowth bounds how far prev may climb toward desired in a single
// tick, to at most maxGrowthFactor times prev (see its doc comment for
// why). A decrease (desired <= prev) is never capped. Always makes at
// least +1 progress toward desired when growing, so a very small prev
// (e.g. 1) is never stuck unable to grow at all.
func capGrowth(prev, desired int) int {
	if desired <= prev {
		return desired
	}
	capped := int(math.Ceil(float64(prev) * maxGrowthFactor))
	if capped <= prev {
		capped = prev + 1
	}
	if capped > desired {
		return desired
	}
	return capped
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

// dynamicGatePollInterval is how often a blocked Acquire re-checks
// Current(). Current() only changes every 30s (or is static), so this
// coarse a poll costs nothing meaningful while staying responsive.
const dynamicGatePollInterval = 25 * time.Millisecond

// DynamicGate bounds concurrent work to limiter.Current() at the moment
// each unit is admitted, rather than a value fixed once for the
// lifetime of the fan-out — which is what errgroup.Group.SetLimit gives
// you, since its documented contract forbids changing the limit after
// any Go() call. A fan-out whose errgroup lives longer than one 30s
// recalculation tick would otherwise never see an updated value: verified
// live against a real migrate run where a single 50-minute
// importProjectData task stayed locked at whatever Current() was the
// instant it started, even though the background recalculation kept
// computing new values the entire time (#573).
//
// Each independent fan-out site must construct its own gate, mirroring
// today's independent per-site Current() reads. Never share one gate
// across nested fan-outs — that would reintroduce exactly the deadlock
// Executor.ConcurrencyLimiter's own doc comment warns against: nested
// fan-outs (e.g. runSyncIssueMetadata's forEachMigrateItem holding a
// slot for each of its workers, each of which calls runProjectSyncLoop)
// would have the outer holders take every slot, leaving no room for
// inner work to ever acquire.
type DynamicGate struct {
	limiter *ConcurrencyLimiter
	active  atomic.Int32
	freed   chan struct{} // buffered(1), signalled by Release
}

// NewDynamicGate constructs a gate bounded by limiter's live Current().
func NewDynamicGate(limiter *ConcurrencyLimiter) *DynamicGate {
	return &DynamicGate{limiter: limiter, freed: make(chan struct{}, 1)}
}

// Acquire blocks until fewer than limiter.Current() units are currently
// admitted, then admits one. Returns ctx.Err() if ctx is done first.
func (g *DynamicGate) Acquire(ctx context.Context) error {
	for {
		if g.tryAcquire() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-g.freed:
		case <-time.After(dynamicGatePollInterval):
		}
	}
}

// tryAcquire admits one unit via CAS when under the current limit,
// racing safely against concurrent Acquire/Release calls.
func (g *DynamicGate) tryAcquire() bool {
	limit := int32(g.limiter.Current())
	for {
		cur := g.active.Load()
		if cur >= limit {
			return false
		}
		if g.active.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// Release frees one admitted unit. Must be called exactly once per
// successful Acquire.
func (g *DynamicGate) Release() {
	g.active.Add(-1)
	select {
	case g.freed <- struct{}{}:
	default:
	}
}
