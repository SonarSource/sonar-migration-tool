// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"context"
	"fmt"
	"log/slog"
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

// DefaultCategoryWeights is the split mandated by issue #520.
var DefaultCategoryWeights = CategoryWeights{
	General:       5,
	ProjectConfig: 20,
	ProjectData:   25,
	IssueSync:     50,
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

	mu        sync.Mutex
	completed map[string]bool

	onUpdate func(percent float64, eta time.Duration, known bool)

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// NewTracker builds a Tracker from the fully-resolved execution plan
// (flattened phases → task names) and a package-specific categorizer.
func NewTracker(logger *slog.Logger, plan [][]string, categorize func(string) TaskCategory, weights CategoryWeights) *Tracker {
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
		completed:     make(map[string]bool),
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

// snapshot computes the current overall percentage (0-100) and ETA. known
// is false until the run has made enough progress to extrapolate an ETA
// (percent > 0). Pure and side-effect-free — the shape unit tests exercise
// directly against the issue's worked examples.
func (t *Tracker) snapshot() (percent float64, eta time.Duration, known bool) {
	t.mu.Lock()
	completedSnapshot := make(map[string]bool, len(t.completed))
	for k, v := range t.completed {
		completedSnapshot[k] = v
	}
	t.mu.Unlock()

	var weighted, activeWeight float64
	for cat, tasks := range t.categoryTasks {
		if len(tasks) == 0 {
			continue
		}
		weight := t.weights.forCategory(cat)
		activeWeight += weight

		var sum float64
		for _, name := range tasks {
			if completedSnapshot[name] {
				sum++
				continue
			}
			sum += t.registry.Fraction(name)
		}
		categoryFraction := sum / float64(len(tasks))
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
