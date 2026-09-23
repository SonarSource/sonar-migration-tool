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

	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
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

	// maxPollBoundConcurrencyCeiling caps pollBoundConcurrency independently
	// of maxConcurrencyCeiling: that constant guards against runaway growth
	// from the latency-driven formula chasing degraded latency upward (see
	// maxGrowthFactor's comment) — a risk that doesn't apply here, since
	// pollBoundConcurrency is a one-time static computation from
	// already-known constants, not an adaptively-growing figure. 300 gives
	// generous headroom above today's natural maximum (25 at the top of the
	// valid --api_max_rate_per_min range; it was 250 back when PollCETask
	// polled on a fixed 10s cadence) for future poll-ladder tuning.
	maxPollBoundConcurrencyCeiling = 300

	// typicalCETaskDuration is the CE execution time pollBoundConcurrency
	// assumes when asking AveragePollInterval how often one polling slot
	// hits api/ce/task. Measured over 603 real CE tasks in a migrate run:
	// median 1.57s, p90 2.95s.
	//
	// The MEDIAN is used rather than a tail figure on purpose. A shorter
	// assumed duration keeps the slot on the ladder's early, short rungs,
	// which yields a shorter average poll interval and therefore a SMALLER
	// concurrency figure — so guessing wrong here errs toward staying
	// inside the rate budget rather than overrunning it.
	typicalCETaskDuration = 1570 * time.Millisecond

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

// pollBoundConcurrency sizes concurrency for a submit-then-poll fan-out —
// runImportProjectData's outer per-project gate, which also covers the
// nested #554 history replay (migrateBranchHistory/submitHistoricalSnapshot
// run inside the same held gate slot) — using Little's Law with the KNOWN
// poll cadence as the occupancy time per slot, instead of observed HTTP
// call latency the way desiredConcurrency does.
//
// A branch's gate slot is held for minutes across build -> SubmitReport ->
// PollCETask, issuing one cheap ~200-600ms call per poll and sleeping the
// rest of the time. Sizing off raw call latency (as ConcurrencyLimiter's
// network-latency-driven formula does) sees only the fast call and ignores
// the sleep entirely, drastically under-provisioning concurrency: measured
// live at avg_latency_ms ~250 and target_rate_per_min=1500,
// desiredConcurrency computed ~6-9, driving only ~37 actual calls/min —
// 2.5% of budget — because each of those few slots spends ~97% of its held
// time asleep between polls, not because the API itself was ever close to
// saturated.
//
// The occupancy figure comes from scanreport.AveragePollInterval rather
// than a single fixed cadence, because PollCETask now backs off
// exponentially instead of polling on a fixed 10s tick. This coupling is
// load-bearing and must not be replaced with a constant: a slot that
// discovers a typical task in ~3s has issued ~3 polls to do it, so it
// consumes roughly 10x more of the api/ce/task rate budget per unit of
// held time than the old fixed cadence did. Sizing the gate off the old
// 10s figure would let aggregate demand exceed api_max_rate_per_min by
// about an order of magnitude — not a correctness bug, since
// SlidingWindowLimiter still enforces the real cap inside Wait(), but
// exactly the recipe for the queue-then-burst pathology documented in
// maxGrowthFactor's comment.
//
// The practical consequence is that the figure is much smaller than it was
// (25 rather than 250 at --api_max_rate_per_min=1500), which is the
// intended trade: fewer branches in flight, each finishing several times
// sooner. Throughput in branches/minute is comparable, and the serial
// history chain — where nothing else can use the spare budget anyway —
// gets the full latency win.
//
// This is a pure function of values already known at startup (the
// configured rate target and the poll ladder's tunables), so — unlike the
// network-latency-driven limiter — it needs no background recalculation:
// callers wrap the result in a NewFixedConcurrencyLimiter once per run.
func pollBoundConcurrency(apiMaxRatePerMin int) int {
	avgPollInterval := scanreport.AveragePollInterval(typicalCETaskDuration)
	raw := float64(apiMaxRatePerMin) * avgPollInterval.Seconds() / 60.0
	n := int(math.Ceil(raw))
	if n < minConcurrency {
		return minConcurrency
	}
	if n > maxPollBoundConcurrencyCeiling {
		return maxPollBoundConcurrencyCeiling
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
// returns n. newConcurrencyLimiter (the network-latency-driven limiter used
// for most fan-outs) is always dynamic (#573 follow-up), but production
// code does legitimately construct a fixed one for pollBoundConcurrency's
// result — both its inputs are already known at startup, so there is
// nothing for a background recalculation loop to react to. Also used by
// test fixtures that want a stable, non-recalculating pool.
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
// slot for each of its workers, each of which calls syncProjectIssues,
// whose inner loop is bounded by nestedSyncLoopConcurrency, its own gate)
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
//
// A nil gate admits immediately (subject only to ctx). That is not
// defensive padding: Executor.BranchGate is legitimately nil in test
// fixtures and in Executors built by reset.go / sync_issues_standalone.go,
// and a nil-receiver method keeps every call site a plain
// Acquire/defer Release pair instead of an if-nil ladder that is easy to
// get wrong on the Release side.
func (g *DynamicGate) Acquire(ctx context.Context) error {
	if g == nil {
		return ctx.Err()
	}
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
// successful Acquire. A nil gate is a no-op, mirroring Acquire.
func (g *DynamicGate) Release() {
	if g == nil {
		return
	}
	g.active.Add(-1)
	select {
	case g.freed <- struct{}{}:
	default:
	}
}
