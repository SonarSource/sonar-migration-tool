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

// TaskCategory buckets a task for run-wide progress weighting (#520).
// Projects dominate a migration's duration, so "project" work is split
// into config/data/issue-sync sub-buckets while everything else (server,
// users, rules, profiles, gates, ...) shares the General bucket.
type TaskCategory int

const (
	CategoryGeneral TaskCategory = iota
	CategoryProjectConfig
	CategoryProjectData
	CategoryIssueSync
)

// CategoryWeights are the percentage points (summing to 100) assigned to
// each category when every category is active. A category with zero tasks
// in the resolved plan (e.g. project-data migration turned off) drops out
// and the rest is renormalized to 100% — see Tracker.snapshot.
//
// Tuned empirically per #520; adjust freely as real-run timings refine the
// split between project config, project data, and issue sync.
type CategoryWeights struct {
	General       float64
	ProjectConfig float64
	ProjectData   float64
	IssueSync     float64
}

// DefaultCategoryWeights is the split recalibrated from real observed
// run data (#564) — a complete migrate run's task-by-task durations
// (migrate-fast.log) split roughly General 13% / ProjectConfig 13% /
// ProjectData 26% / IssueSync 48%; rounded here and nudged slightly to
// leave ProjectData room for #554's project-history replay. Supersedes
// the original #520 guess of 5/20/25/50.
var DefaultCategoryWeights = CategoryWeights{
	General:       12,
	ProjectConfig: 14,
	ProjectData:   27,
	IssueSync:     47,
}

func (w CategoryWeights) forCategory(cat TaskCategory) float64 {
	switch cat {
	case CategoryProjectConfig:
		return w.ProjectConfig
	case CategoryProjectData:
		return w.ProjectData
	case CategoryIssueSync:
		return w.IssueSync
	default:
		return w.General
	}
}

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
// The plan is phase-based, not project-based: a task's total item count
// (e.g. number of projects) usually isn't known until the task starts
// reading its dependency's output, so Tracker treats every task as a unit
// of work within its category, weighted by the category's percentage, and
// blends in live item-level fractions from ProgressRegistry for whichever
// tasks are currently running.
type Tracker struct {
	logger        *slog.Logger
	start         time.Time
	weights       CategoryWeights
	categoryTasks map[TaskCategory][]string
	registry      *ProgressRegistry
	expected      func(string) time.Duration

	mu        sync.Mutex
	completed map[string]bool
	running   map[string]time.Time
	overrides map[string]time.Duration

	onUpdate func(percent float64, eta time.Duration, known bool)

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// NewTracker builds a Tracker from the fully-resolved execution plan
// (flattened phases → task names), a package-specific categorizer, the
// category weights, and a per-task expected-duration function (#564) used
// both to weight tasks within a category by their real relative cost and
// to credit partial progress on a long-running task that has no
// item-level ProgressRegistry entry (see snapshot/taskFraction).
func NewTracker(logger *slog.Logger, plan [][]string, categorize func(string) TaskCategory, weights CategoryWeights, expected func(string) time.Duration) *Tracker {
	categoryTasks := make(map[TaskCategory][]string)
	for _, phase := range plan {
		for _, name := range phase {
			cat := categorize(name)
			categoryTasks[cat] = append(categoryTasks[cat], name)
		}
	}
	return &Tracker{
		logger:        logger,
		start:         time.Now(),
		weights:       weights,
		categoryTasks: categoryTasks,
		registry:      NewProgressRegistry(),
		expected:      expected,
		completed:     make(map[string]bool),
		running:       make(map[string]time.Time),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
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
func (t *Tracker) MarkTaskComplete(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.completed[name] = true
}

// MarkTaskStarted records when a task's Run function began, so snapshot
// can credit a still-running task with no item-level ProgressRegistry
// entry proportionally to elapsed/expected duration instead of leaving it
// at 0% until it completes (#564). A nil receiver is a no-op, matching
// every other Tracker method's contract.
func (t *Tracker) MarkTaskStarted(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running[name] = time.Now()
}

// AddPseudoTask adds a task name to a category's weighting set after
// construction, for work that never appears in the resolved execution
// plan handed to NewTracker — e.g. migrate's project-history replay
// (#554), which runs inline inside importProjectData rather than as its
// own TaskDef. The pseudo-task's progress comes from whatever
// ProgressLogger gets registered under the same name via Registry(); no
// further special-casing is needed. A nil receiver is a no-op.
func (t *Tracker) AddPseudoTask(cat TaskCategory, name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.categoryTasks[cat] = append(t.categoryTasks[cat], name)
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

// taskFraction returns how complete one task is, 0-1 (#564):
//   - 1 once MarkTaskComplete recorded it, or the item-level
//     ProgressRegistry fraction itself reaches exactly 1 (every item
//     genuinely done).
//   - otherwise, for a task with BOTH a partial item-level fraction and a
//     recorded start time, whichever of the item-count fraction and the
//     time-based inFlightCredit is smaller — see below for why.
//   - the item-count fraction alone, for a task with partial progress but
//     no recorded start (shouldn't happen in production; every runPhase
//     calls MarkTaskStarted, but tests may register progress directly).
//   - inFlightCredit alone, for a running task with no item-level counter
//     at all (syncIssueMetadata's own INTERNAL granularity aside, tasks
//     like getProjectSourceCode never register one).
//   - 0 for a task that hasn't started at all.
//
// Why min(), not the item-count fraction directly: a real run showed
// percent jump 52%→98% in under a minute right as syncIssueMetadata
// started. It has a per-PROJECT ProgressLogger (78 items), but almost all
// projects had zero/few issues and finished in milliseconds while the one
// or two projects with hundreds of issues — which actually determined the
// remaining wall-clock time — hadn't. Item count said "90% done" a few
// seconds in; barely any real time had elapsed. Capping the item-count
// fraction at what elapsed/expected alone would justify prevents a
// skewed item-cost distribution from making a task look far more
// complete than the clock does, without ever contradicting the registry
// once it genuinely reports 1.0 (that check always wins outright, no
// matter how little time has elapsed — a task can legitimately finish
// faster than its seed).
func (t *Tracker) taskFraction(name string, completed map[string]bool, running map[string]time.Time, overrides map[string]time.Duration) float64 {
	if completed[name] {
		return 1
	}
	itemFrac := t.registry.Fraction(name)
	if itemFrac >= 1 {
		return 1
	}
	startedAt, started := running[name]
	exp := t.expectedDuration(name, overrides)
	if !started || exp <= 0 {
		return itemFrac
	}
	timeFrac := inFlightCredit(time.Since(startedAt), exp)
	if itemFrac == 0 {
		return timeFrac
	}
	return math.Min(itemFrac, timeFrac)
}

// inFlightCredit estimates how "done" a running task is from elapsed vs.
// its seeded expected duration, asymptotically: elapsed/(elapsed+expected).
// This is 0 at start, exactly 0.5 right when elapsed reaches expected, and
// keeps climbing toward (but never reaching) 1 for as long as the task
// keeps running past its seed — deliberately NOT a linear ramp with a hard
// cap (#564's first attempt): a real run's syncIssueMetadata took 30m04s
// against a 492s (8m12s) seed, so a linear-then-clamp formula hit its cap
// after ~7.8 minutes and then sat frozen there for the remaining ~22
// minutes — the exact "stuck" symptom this whole feature exists to fix,
// just relocated to a different task. An always-still-climbing curve
// tolerates a seed being wrong by any factor: it just creeps more slowly,
// never freezes, and is provably always < 1 until real completion signals
// it (MarkTaskComplete or the registry reaching its total).
func inFlightCredit(elapsed, expected time.Duration) float64 {
	e, x := elapsed.Seconds(), expected.Seconds()
	return e / (e + x)
}

// snapshot computes the current overall percentage (0-100) and ETA. known
// is false until the run has made enough progress to extrapolate an ETA
// (percent > 0). Pure and side-effect-free — the shape unit tests exercise
// directly against the issue's worked examples.
//
// Within each category, a task's contribution is weighted by its real
// expected duration rather than counted equally against its siblings
// (#564) — e.g. IssueSync's syncIssueMetadata (~492s observed) now
// dominates syncHotspotMetadata (~54s observed) instead of each counting
// for half of the category, regardless of actual relative cost.
func (t *Tracker) snapshot() (percent float64, eta time.Duration, known bool) {
	t.mu.Lock()
	completedSnapshot := make(map[string]bool, len(t.completed))
	for k, v := range t.completed {
		completedSnapshot[k] = v
	}
	runningSnapshot := make(map[string]time.Time, len(t.running))
	for k, v := range t.running {
		runningSnapshot[k] = v
	}
	overridesSnapshot := make(map[string]time.Duration, len(t.overrides))
	for k, v := range t.overrides {
		overridesSnapshot[k] = v
	}
	t.mu.Unlock()

	var weighted, activeWeight float64
	for cat, tasks := range t.categoryTasks {
		if len(tasks) == 0 {
			continue
		}
		weight := t.weights.forCategory(cat)
		activeWeight += weight

		var doneExpected, totalExpected float64
		for _, name := range tasks {
			exp := t.expectedDuration(name, overridesSnapshot).Seconds()
			if exp <= 0 {
				exp = DefaultTaskDuration.Seconds()
			}
			totalExpected += exp
			doneExpected += exp * t.taskFraction(name, completedSnapshot, runningSnapshot, overridesSnapshot)
		}
		if totalExpected <= 0 {
			continue
		}
		categoryFraction := doneExpected / totalExpected
		weighted += categoryFraction * weight
	}

	if activeWeight <= 0 {
		return 0, 0, false
	}
	percent = 100 * weighted / activeWeight

	if percent <= 0 {
		return percent, 0, false
	}
	elapsed := time.Since(t.start)
	total := time.Duration(float64(elapsed) * (100 / percent))
	if total < elapsed {
		total = elapsed
	}
	return percent, total - elapsed, true
}
