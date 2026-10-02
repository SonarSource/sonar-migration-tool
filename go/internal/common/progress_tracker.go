// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"
)

// TaskCategory buckets a task into one of the four stages the run report
// breaks a migration's duration into (#520). Projects dominate a
// migration's duration, so "project" work is split into config/data/
// issue-sync sub-buckets while everything else (server, users, rules,
// profiles, gates, ...) shares the General bucket. The overall progress
// percentage no longer weights by it (#621): it is derived from the ETA.
type TaskCategory int

const (
	CategoryGeneral TaskCategory = iota
	CategoryProjectConfig
	CategoryProjectData
	CategoryIssueSync
)

// ProgressRegistry is a run-wide lookup of the in-flight ProgressLogger for
// each currently-executing task, keyed by task name. Tasks register their
// logger once (at the same call site that already constructs it) so
// Tracker can read live item-level progress without any task Run function
// knowing about the tracker.
type ProgressRegistry struct {
	mu      sync.RWMutex
	loggers map[string]*ProgressLogger
}

// NewProgressRegistry returns an empty registry.
func NewProgressRegistry() *ProgressRegistry {
	return &ProgressRegistry{loggers: make(map[string]*ProgressLogger)}
}

// Register records the ProgressLogger driving task's item-level progress.
// A nil receiver is a no-op — lets call sites write
// e.Progress.Registry().Register(...) unconditionally even when Progress
// was never set (e.g. tests exercising a task in isolation, #520).
func (r *ProgressRegistry) Register(task string, pl *ProgressLogger) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loggers[task] = pl
}

// Fraction returns the registered logger's Fraction(), or 0 if task never
// registered one (not started yet, or a task with no item-level logger).
func (r *ProgressRegistry) Fraction(task string) float64 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if pl, ok := r.loggers[task]; ok {
		return pl.Fraction()
	}
	return 0
}

// Tracker computes a single run-wide "-----> Overall progress: X% - ETA: hh:mm:ss"
// estimate for extract/migrate (#520) and logs it on a fixed interval.
//
// The estimate follows the plan's shape (#621): phases run one after
// another and the tasks in a phase run together, so the ETA is the sum,
// phase by phase, of the time left on each phase's slowest task, and the
// percentage is the share of that estimated total already elapsed. See
// remainingSeconds and snapshot.
type Tracker struct {
	logger *slog.Logger
	start  time.Time
	// phases is the plan in execution order. Phases run one after another
	// and the tasks inside one phase run concurrently, which is what the
	// ETA is computed from (#621 — see remainingSeconds).
	phases [][]string
	// pseudoHost maps a pseudo-task to the plan task it runs inside. A
	// pseudo-task is never handed to runPhase, so it has no start mark of
	// its own and borrows its host's (see taskRemaining).
	pseudoHost map[string]string
	registry   *ProgressRegistry
	expected   func(string) time.Duration
	// now is the clock. Always time.Now in production; replaced in tests
	// so a recorded real run's timeline can be replayed deterministically
	// against the estimator (#564).
	now func() time.Time

	mu        sync.Mutex
	completed map[string]bool
	running   map[string]time.Time
	actual    map[string]time.Duration
	overrides map[string]time.Duration
	// reportedPercent is the highest percentage snapshot has returned;
	// see raisePercent.
	reportedPercent float64

	onUpdate func(percent float64, eta time.Duration, known bool)

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// NewTracker builds a Tracker from the fully-resolved execution plan, in
// phase order, and a per-task expected-duration function (#564) that
// seeds how long each task should take (see taskRemaining).
func NewTracker(logger *slog.Logger, plan [][]string, expected func(string) time.Duration) *Tracker {
	return &Tracker{
		logger:    logger,
		start:     time.Now(),
		phases:    plan,
		registry:  NewProgressRegistry(),
		expected:  expected,
		now:       time.Now,
		completed: make(map[string]bool),
		running:   make(map[string]time.Time),
		actual:    make(map[string]time.Duration),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// Registry exposes the ProgressRegistry so task helpers can register the
// ProgressLogger they already construct for item-level progress. Safe to
// call on a nil Tracker (returns nil; ProgressRegistry's methods are
// themselves nil-safe) — see Register.
func (t *Tracker) Registry() *ProgressRegistry {
	if t == nil {
		return nil
	}
	return t.registry
}

// OnUpdate registers fn to be called with the same (percent, eta, known)
// values as each log line — from the periodic ticker (#520) and from
// LogFinal's closing 100% snapshot. Used by the GUI (#519) to drive a
// progress bar without touching CLI behavior: callers that never call
// OnUpdate get no extra work, and a nil receiver is a no-op so it's safe
// to call unconditionally on a Tracker built from an unset config field.
func (t *Tracker) OnUpdate(fn func(percent float64, eta time.Duration, known bool)) {
	if t == nil {
		return
	}
	t.onUpdate = fn
}

// MarkTaskComplete records that a task's Run function has returned
// successfully — it now counts as 100% of its category's per-task share
// regardless of whether it had a registered item-level logger. A nil
// receiver is a no-op (#520 — Progress is unset in tests that exercise a
// task's Run function directly rather than through RunExtract/RunMigrate).
//
// It also banks how long the task really took, when MarkTaskStarted
// recorded a start for it, which is what lets the rest of the run
// calibrate the static seeds against this instance and this run's size
// (#564 — see observedSpeedFactor).
func (t *Tracker) MarkTaskComplete(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.completed[name] = true
	if startedAt, ok := t.running[name]; ok {
		t.actual[name] = t.now().Sub(startedAt)
	}
}

// MarkTaskStarted records when a task's Run function began, so the ETA
// can count down a running task's expected duration instead of treating
// it as not started until it completes (#564, #621 — see clockRemaining). A nil receiver is a no-op, matching
// every other Tracker method's contract.
func (t *Tracker) MarkTaskStarted(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running[name] = t.now()
}

// AddPseudoTask adds a task to the plan after construction, for work that never appears in the resolved execution
// plan handed to NewTracker — e.g. migrate's project-history replay
// (#554), which runs inline inside importProjectData rather than as its
// own TaskDef. The pseudo-task's progress comes from whatever
// ProgressLogger gets registered under the same name via Registry(); no
// further special-casing is needed.
//
// host names the plan task the pseudo-task runs inside, so the ETA counts
// it as running alongside that task rather than after it (#621). A host
// that is not in the plan leaves the pseudo-task in its own phase at the
// end of the run. A nil receiver is a no-op.
func (t *Tracker) AddPseudoTask(name, host string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pseudoHost == nil {
		t.pseudoHost = make(map[string]string)
	}
	t.pseudoHost[name] = host
	phases := make([][]string, len(t.phases))
	copy(phases, t.phases)
	for i, phase := range phases {
		for _, task := range phase {
			if task == host {
				phases[i] = append(append([]string(nil), phase...), name)
				t.phases = phases
				return
			}
		}
	}
	t.phases = append(phases, []string{name})
}

// SetExpectedDuration overrides the seeded/default expected duration for
// one task name — used for pseudo-tasks whose real cost scales with a
// per-run quantity known only once the plan starts (e.g.
// migrateProjectHistory's total history-point count), rather than a
// fixed seed. A nil receiver is a no-op.
func (t *Tracker) SetExpectedDuration(name string, d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.overrides == nil {
		t.overrides = make(map[string]time.Duration)
	}
	t.overrides[name] = d
}

// expectedDuration resolves name's expected duration: an explicit
// SetExpectedDuration override (from the given snapshot copy) first, then
// the injected seed function. Pure — takes the overrides map as a
// parameter rather than reading t.overrides directly, so callers can
// snapshot it under lock once and then compute lock-free (see snapshot).
func (t *Tracker) expectedDuration(name string, overrides map[string]time.Duration) time.Duration {
	if d, ok := overrides[name]; ok {
		return d
	}
	if t.expected != nil {
		return t.expected(name)
	}
	return 0
}

// Start launches a goroutine that logs the overall progress/ETA line every
// interval until ctx is done or Stop is called. It also immediately pushes
// one snapshot through OnUpdate (log-free) so a GUI progress bar appears
// at 0% right away instead of staying hidden for the first interval — or,
// on a run shorter than interval, staying hidden until LogFinal (#519).
// The #520 log cadence itself is untouched: the first log line still
// waits for the first real tick.
func (t *Tracker) Start(ctx context.Context, interval time.Duration) {
	if t.onUpdate != nil {
		percent, eta, known := t.snapshot()
		t.onUpdate(percent, eta, known)
	}
	go func() {
		defer close(t.doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.stopCh:
				return
			case <-ticker.C:
				t.logOnce()
			}
		}
	}()
}

// Stop halts the ticker goroutine and waits for it to exit. Safe to call
// more than once and safe to call even if Start was never called... except
// Start must have been called, since doneCh only closes once the goroutine
// exits; callers use `defer tracker.Stop()` right after Start.
func (t *Tracker) Stop() {
	t.stopOnce.Do(func() { close(t.stopCh) })
	<-t.doneCh
}

func (t *Tracker) logOnce() {
	percent, eta, known := t.snapshot()
	etaStr := "calculating..."
	if known {
		etaStr = FormatHMS(eta)
	}
	t.logger.Info(fmt.Sprintf("-----> Overall progress: %d%% - ETA: %s", int(percent), etaStr))
	if t.onUpdate != nil {
		t.onUpdate(percent, eta, known)
	}
}

// LogFinal emits the closing "-----> Overall progress: 100% - ETA: 00:00:00"
// line. Call it once, explicitly, right after a run finishes all phases
// successfully — unlike logOnce (driven by the periodic ticker, and derived from
// the live snapshot), this always reports exactly 100%/00:00:00 rather than
// whatever the last snapshot happened to compute, so operators get an
// unambiguous "done" line even if the run completed between ticks. Do not
// call this on a failed/aborted run. A nil receiver is a no-op.
func (t *Tracker) LogFinal() {
	if t == nil {
		return
	}
	t.logger.Info(fmt.Sprintf("-----> Overall progress: 100%% - ETA: %s", FormatHMS(0)))
	if t.onUpdate != nil {
		t.onUpdate(100, 0, true)
	}
}

// Bounds on the observed speed factor (#564). SeedTaskDurations was mined
// from a handful of large runs, so a run against a different instance, or
// over a fraction of the projects, can be an order of magnitude off in
// either direction — but not further, and a factor derived from too
// little completed work is not yet worth trusting.
const (
	// fullCalibrationWork is how many seconds of *seeded* work must have
	// completed before the observed factor is applied at full strength.
	// Below it the factor is blended toward 1 in proportion (see
	// observedSpeedFactor): a run's first few completions are all tiny
	// orchestration tasks whose ratio says little about what
	// importProjectData or syncIssueMetadata will cost, so they nudge the
	// estimate rather than rewriting it.
	fullCalibrationWork = 120.0
	minSpeedFactor      = 0.05
	maxSpeedFactor      = 20.0
)

// maxReportableETASeconds bounds what snapshot will report as an ETA (30
// days). A snapshot taken in the first instants of a run divides by a
// near-zero consumed-work figure, which past roughly 292 years overflows
// time.Duration's int64 into a NEGATIVE duration — an operator would see
// a nonsense clock rather than an honest "calculating...". No real
// migration approaches this bound, so anything beyond it is noise.
const maxReportableETASeconds = 30 * 24 * 60 * 60

// observedSpeedFactor returns how much faster (<1) or slower (>1) this
// run's completed tasks actually were than their seeded durations (#564).
// It is the ratio of summed real durations to summed seeded durations, so
// a task with a big seed weighs proportionally more than a 3-second
// orchestration task, then blended toward 1 while little work has
// completed and clamped to the bounds above.
//
// Derived from COMPLETED tasks only, never from in-flight ones, which is
// what keeps it out of a feedback loop with taskRemaining: the factor
// scales the expected duration taskRemaining counts a running task down
// from, so letting running tasks feed the factor would let the estimate
// chase its own tail.
func (t *Tracker) observedSpeedFactor(actual, overrides map[string]time.Duration) float64 {
	var sumActual, sumExpected float64
	for name, d := range actual {
		exp := t.expectedDuration(name, overrides).Seconds()
		if exp <= 0 {
			exp = DefaultTaskDuration.Seconds()
		}
		sumActual += d.Seconds()
		sumExpected += exp
	}
	if sumExpected <= 0 {
		return 1
	}
	raw := math.Max(minSpeedFactor, math.Min(maxSpeedFactor, sumActual/sumExpected))
	confidence := math.Min(1, sumExpected/fullCalibrationWork)
	return 1 + confidence*(raw-1)
}

// taskRemaining estimates, in real seconds, how long one task still has to
// run (#621):
//   - 0 once it is complete.
//   - its expected duration while it has not started.
//   - while it runs: clockRemaining, from its elapsed time against x.
//
// Item-level progress only ever ends a task early (f reaching 1). Its rate
// is not used to extrapolate: replaying #621's run, every blend of
// e·(1-f)/f made the ETA worse, because items are not equal cost. The
// history replay's 496 points ran 300 in the first 11 minutes and the last
// 196 in 19, once only the few big projects were left.
//
// x is the seed scaled by this run's observed speed factor, except for a
// SetExpectedDuration override: those are already sized from this run's
// own item counts (history points, projects in scope), and the speed
// factor largely measures run size too, so scaling them again would count
// it twice. In #621's run the factor before importProjectData started was
// 2.7 and that task really ran 3.3x its seed; in the one-project juice-shop
// fixture the factor was 0.21 against a real 0.16.
func (t *Tracker) taskRemaining(name string, s trackerState, speed float64) float64 {
	if s.completed[name] {
		return 0
	}
	x := t.expectedDuration(name, s.overrides).Seconds()
	if x <= 0 {
		x = DefaultTaskDuration.Seconds()
	}
	if _, sized := s.overrides[name]; !sized {
		x *= speed
	}
	f := t.registry.Fraction(name)
	if f >= 1 {
		return 0
	}
	startedAt, started := s.running[name]
	if !started {
		startedAt, started = s.running[s.pseudoHost[name]]
	}
	if !started {
		return x
	}
	e := math.Max(0, t.now().Sub(startedAt).Seconds())
	return clockRemaining(e, x)
}

// clockRemaining is how long a running task with no better signal still
// has, from e seconds elapsed against x expected: x-e for the first half of
// the seed, so the ETA falls one second per wall-clock second, and x²/4e
// after it. The two meet with the same value and slope at e = x/2, and the
// tail keeps shrinking without reaching zero, so a task that overruns its
// seed holds the ETA nearly level instead of making it climb (#621) or
// letting it hit zero while the task is still running.
func clockRemaining(e, x float64) float64 {
	if e <= x/2 {
		return x - e
	}
	return x * x / (4 * e)
}

// remainingSeconds is the ETA's model of the plan (#621): phases run one
// after another and the tasks inside a phase run concurrently, so the
// time left is, phase by phase, the time left on that phase's slowest
// task. Every term is real seconds. #598 instead multiplied elapsed time by
// remaining/consumed seeded work, which amplifies every second a task
// spends past its seed by that ratio — in #621's run the ETA climbed two
// minutes for every ten seconds of a 3s-seeded task running 160s.
func (t *Tracker) remainingSeconds(s trackerState, speed float64) float64 {
	var total float64
	for _, phase := range s.phases {
		var slowest float64
		for _, name := range phase {
			slowest = math.Max(slowest, t.taskRemaining(name, s, speed))
		}
		total += slowest
	}
	return total
}

// trackerState is a point-in-time copy of everything snapshot needs out
// of the mutex-guarded maps, so the whole estimate is computed lock-free
// from one consistent view rather than re-reading fields that other
// goroutines are still mutating.
type trackerState struct {
	completed map[string]bool
	running   map[string]time.Time
	overrides map[string]time.Duration
	actual    map[string]time.Duration
	phases    [][]string
	// pseudoHost is shared, not copied: AddPseudoTask replaces neither
	// it nor phases once the run has started.
	pseudoHost map[string]string
}

func (t *Tracker) state() trackerState {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := trackerState{
		completed:  make(map[string]bool, len(t.completed)),
		running:    make(map[string]time.Time, len(t.running)),
		overrides:  make(map[string]time.Duration, len(t.overrides)),
		actual:     make(map[string]time.Duration, len(t.actual)),
		phases:     t.phases,
		pseudoHost: t.pseudoHost,
	}
	for k, v := range t.completed {
		s.completed[k] = v
	}
	for k, v := range t.running {
		s.running[k] = v
	}
	for k, v := range t.overrides {
		s.overrides[k] = v
	}
	for k, v := range t.actual {
		s.actual[k] = v
	}
	return s
}

// snapshot computes the current overall percentage (0-100) and ETA (#621).
// The ETA is remainingSeconds, the phase model's time left in real
// seconds. The percentage is the share of the run's estimated total length
// already behind it, elapsed/(elapsed+ETA), so the two always describe the
// same run, and one that is on schedule reports a percentage rising in a
// straight line with the clock. It is never allowed to fall: an ETA that
// grows because a task overran its seed holds the percentage where it is
// rather than taking ground back. known is false for an empty plan, and
// when the estimate is too large to be anything but noise.
func (t *Tracker) snapshot() (percent float64, eta time.Duration, known bool) {
	s := t.state()
	if len(s.phases) == 0 {
		return 0, 0, false
	}
	speed := t.observedSpeedFactor(s.actual, s.overrides)
	etaSeconds := t.remainingSeconds(s, speed)
	if etaSeconds > maxReportableETASeconds {
		return t.raisePercent(0), 0, false
	}
	elapsed := math.Max(0, t.now().Sub(t.start).Seconds())
	percent = 100
	if etaSeconds > 0 {
		percent = 100 * elapsed / (elapsed + etaSeconds)
	}
	return t.raisePercent(percent), time.Duration(etaSeconds * float64(time.Second)), true
}

// raisePercent returns the larger of p and every percentage reported so
// far, and records it, so the reported percentage never goes backwards.
func (t *Tracker) raisePercent(p float64) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p > t.reportedPercent {
		t.reportedPercent = p
	}
	return t.reportedPercent
}
