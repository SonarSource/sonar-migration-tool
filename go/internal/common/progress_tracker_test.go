// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

// categorizeByPrefix buckets synthetic test task names by a "general-",
// "config-", "data-", or "sync-" prefix — lets each example below spell
// out task names that read like the issue's own bullet points.
func categorizeByPrefix(name string) TaskCategory {
	switch {
	case len(name) >= 7 && name[:7] == "config-":
		return CategoryProjectConfig
	case len(name) >= 5 && name[:5] == "data-":
		return CategoryProjectData
	case len(name) >= 5 && name[:5] == "sync-":
		return CategoryIssueSync
	default:
		return CategoryGeneral
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

// registerPartial creates a ProgressLogger for name with done/total items
// completed and registers it on the tracker, simulating a task that's
// currently mid-execution.
func registerPartial(tr *Tracker, name string, done, total int) {
	prog := NewProgressLogger(testLogger(), name, total)
	for i := 0; i < done; i++ {
		prog.Increment()
	}
	tr.Registry().Register(name, prog)
}

func almostEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) <= tolerance
}

// TestTrackerSnapshotWorkedExamples pins Tracker.snapshot's math against
// the five worked examples in issue #520's description, including the two
// "a category is turned off" cases (4 and 5) where a zero-task category
// drops out of the weighted denominator instead of just contributing 0.
//
// The synthetic task names here (general-1, config-N, ...) are all
// unseeded, so ExpectedTaskDuration falls back to the same
// DefaultTaskDuration for every one of them — which makes the
// expected-duration-weighted math #564 introduced collapse back to the
// original equal-per-task averaging within a category. Only the outer
// DefaultCategoryWeights numbers changed (#564's recalibration from
// 5/20/25/50 to 12/14/27/47), so these percentages are recomputed against
// the same per-category fractions the pre-#564 comments already describe.
func TestTrackerSnapshotWorkedExamples(t *testing.T) {
	t.Run("example 1: config in progress, data/sync not started", func(t *testing.T) {
		plan := [][]string{{
			"general-1",
			"config-1", "config-2", "config-3", "config-4", "config-5", "config-6", "config-7",
			"config-8", "config-9", "config-10", "config-11",
			"data-1",
			"sync-1",
		}}
		tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
		tr.MarkTaskComplete("general-1")
		for _, n := range []string{"config-1", "config-2", "config-3", "config-4", "config-5", "config-6", "config-7"} {
			tr.MarkTaskComplete(n)
		}
		registerPartial(tr, "config-8", 87, 200)
		// config-9/10/11 and data-1/sync-1 stay unregistered (not started).

		percent, _, _ := tr.snapshot()
		if !almostEqual(percent, 21.47, 0.1) {
			t.Errorf("percent = %v, want ~21.47", percent)
		}
	})

	t.Run("example 2: config done, data in progress, sync not started", func(t *testing.T) {
		plan := [][]string{{"general-1", "config-1", "data-1", "sync-1"}}
		tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
		tr.MarkTaskComplete("general-1")
		tr.MarkTaskComplete("config-1")
		registerPartial(tr, "data-1", 153, 200)

		percent, _, _ := tr.snapshot()
		if !almostEqual(percent, 46.66, 0.1) {
			t.Errorf("percent = %v, want ~46.66", percent)
		}
	})

	t.Run("example 3: config+data done, sync in progress", func(t *testing.T) {
		plan := [][]string{{"general-1", "config-1", "data-1", "sync-1"}}
		tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
		tr.MarkTaskComplete("general-1")
		tr.MarkTaskComplete("config-1")
		tr.MarkTaskComplete("data-1")
		registerPartial(tr, "sync-1", 72, 200)

		percent, _, _ := tr.snapshot()
		if !almostEqual(percent, 69.92, 0.1) {
			t.Errorf("percent = %v, want ~69.92", percent)
		}
	})

	t.Run("example 4: project data (and issue sync) turned off", func(t *testing.T) {
		// No data-*/sync-* tasks at all in the plan — the categories have
		// zero tasks and must drop out of the weighted denominator rather
		// than just contribute 0 progress.
		plan := [][]string{{"general-1", "config-1"}}
		tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
		tr.MarkTaskComplete("general-1")
		registerPartial(tr, "config-1", 72, 200)

		percent, _, _ := tr.snapshot()
		if !almostEqual(percent, 65.54, 0.1) {
			t.Errorf("percent = %v, want ~65.54", percent)
		}
	})

	t.Run("example 5: issue sync turned off, project data on but not started", func(t *testing.T) {
		// data-1 present (project data is on) but untouched; no sync-*
		// task at all (issue sync is off).
		plan := [][]string{{"general-1", "config-1", "data-1"}}
		tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
		tr.MarkTaskComplete("general-1")
		registerPartial(tr, "config-1", 72, 200)
		// data-1 stays unregistered — 0 progress, but its 27% weight
		// still counts toward the denominator.

		percent, _, _ := tr.snapshot()
		if !almostEqual(percent, 32.15, 0.1) {
			t.Errorf("percent = %v, want ~32.15", percent)
		}
	})
}

// fixedDuration returns an expected-duration function that reports d for
// every task name — lets tests pin an exact, known "expected" cost
// instead of depending on the real seed table (#564).
func fixedDuration(d time.Duration) func(string) time.Duration {
	return func(string) time.Duration { return d }
}

// TestTrackerSnapshotInFlightCredit covers #564's core fix: a task with
// no item-level ProgressRegistry entry must contribute proportionally to
// elapsed/expected duration once MarkTaskStarted records it, instead of
// sitting at 0% until MarkTaskComplete — this is what caused real runs to
// freeze at 99%/"2 seconds left" for minutes while a single long task
// (syncIssueMetadata, no per-item counter) ran in the background.
func TestTrackerSnapshotInFlightCredit(t *testing.T) {
	plan := [][]string{{"general-1"}}
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, CategoryWeights{General: 100}, fixedDuration(100*time.Millisecond))

	// Before MarkTaskStarted: no signal at all, task contributes 0.
	percent, _, known := tr.snapshot()
	if percent != 0 || known {
		t.Fatalf("before start: percent=%v known=%v, want 0/false", percent, known)
	}

	tr.MarkTaskStarted("general-1")
	time.Sleep(100 * time.Millisecond) // elapsed == expected

	percent, _, known = tr.snapshot()
	if !known {
		t.Fatal("known = false once a task is in flight, want true")
	}
	// inFlightCredit(elapsed, expected) = elapsed/(elapsed+expected) = 0.5
	// exactly when elapsed reaches expected.
	if !almostEqual(percent, 50, 15) { // generous tolerance — real sleep/scheduling jitter
		t.Errorf("in-flight percent = %v, want ~50", percent)
	}
}

// TestTrackerSnapshotInFlightCreditNeverFreezes pins the real-world bug a
// hard cap on the linear elapsed/expected ramp produced (#564): a real
// run's syncIssueMetadata took 30m04s against a 492s seed — the linear
// ramp hit its cap after ~7.8 minutes and then sat frozen there for the
// remaining ~22 minutes, reproducing the exact "stuck" symptom this
// feature exists to fix. inFlightCredit's asymptotic curve must instead
// keep climbing, however slowly, for as long as an over-running task
// keeps running, while never claiming 100% before MarkTaskComplete or the
// registry itself reaches its total.
func TestTrackerSnapshotInFlightCreditNeverFreezes(t *testing.T) {
	plan := [][]string{{"general-1"}}
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, CategoryWeights{General: 100}, fixedDuration(1*time.Millisecond))

	tr.MarkTaskStarted("general-1")
	time.Sleep(20 * time.Millisecond) // wildly over the 1ms expected duration
	first, _, _ := tr.snapshot()
	if first >= 100 {
		t.Fatalf("percent = %v, want < 100 — the task never actually completed", first)
	}

	time.Sleep(40 * time.Millisecond) // let it keep running well past that
	second, _, _ := tr.snapshot()
	if second >= 100 {
		t.Errorf("percent = %v, want < 100 — the task never actually completed", second)
	}
	if second <= first {
		t.Errorf("percent went from %v to %v — must keep climbing the longer an over-running task runs, never freeze", first, second)
	}
}

// TestTrackerSnapshotItemCountNeverOutrunsElapsedTime pins the real-world
// bug found right after the freeze fix (#564): a real run's percent
// jumped 52%->98% in under a minute the moment syncIssueMetadata started.
// It has a per-PROJECT ProgressLogger (78 items), but almost all projects
// had zero/few issues and finished in milliseconds while the couple with
// hundreds of issues — which actually determined the remaining wall-clock
// time — hadn't. Item count alone said "~90% done" a few seconds in, with
// barely any real time elapsed. taskFraction must cap the item-count
// fraction at what elapsed/expected alone would justify whenever the two
// disagree, so a skewed item-cost distribution can't make a task look far
// more complete than the clock does.
func TestTrackerSnapshotItemCountNeverOutrunsElapsedTime(t *testing.T) {
	plan := [][]string{{"sync-1"}}
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, CategoryWeights{IssueSync: 100}, fixedDuration(492*time.Second))

	tr.MarkTaskStarted("sync-1")
	// 9 of 10 "projects" (items) already done, but essentially no real
	// time has passed — exactly the skewed-cost scenario from the log.
	registerPartial(tr, "sync-1", 9, 10)

	percent, _, _ := tr.snapshot()
	if percent >= 50 {
		t.Errorf("percent = %v, want well under 50 — 9/10 items done in ~0s must not outrun the elapsed-time signal", percent)
	}
}

// TestTrackerSnapshotItemCountWinsOnceRegistryTrulyComplete: once every
// item genuinely finishes (fraction reaches exactly 1), that must win
// outright regardless of elapsed time — a task can legitimately finish
// faster than its seeded expected duration, and the dampening above must
// never hold a truly-complete task back.
func TestTrackerSnapshotItemCountWinsOnceRegistryTrulyComplete(t *testing.T) {
	plan := [][]string{{"sync-1"}}
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, CategoryWeights{IssueSync: 100}, fixedDuration(492*time.Second))

	tr.MarkTaskStarted("sync-1")
	registerPartial(tr, "sync-1", 10, 10) // all 10 items done, ~0s elapsed

	percent, _, _ := tr.snapshot()
	if percent != 100 {
		t.Errorf("percent = %v, want 100 — the registry itself says every item is done", percent)
	}
}

// TestTrackerSnapshotWeightsWithinCategoryByExpectedDuration: two tasks in
// one category with very different expected durations (mirroring
// syncIssueMetadata ~492s vs syncHotspotMetadata ~54s, both IssueSync)
// must NOT split 50/50 just because there are two of them — finishing the
// short one alone should show far less than half the category done.
func TestTrackerSnapshotWeightsWithinCategoryByExpectedDuration(t *testing.T) {
	plan := [][]string{{"sync-long", "sync-short"}}
	expected := func(name string) time.Duration {
		if name == "sync-long" {
			return 492 * time.Second
		}
		return 54 * time.Second
	}
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, CategoryWeights{IssueSync: 100}, expected)
	tr.MarkTaskComplete("sync-short") // the short task alone, long one untouched

	percent, _, _ := tr.snapshot()
	want := 100 * 54.0 / (492.0 + 54.0) // ~9.9%, not 50%
	if !almostEqual(percent, want, 0.1) {
		t.Errorf("percent = %v, want ~%v (expected-duration-weighted, not equal split)", percent, want)
	}
}

// TestTrackerAddPseudoTaskAndSetExpectedDuration covers #554's
// integration: a task that never appears in the plan handed to
// NewTracker (migrateProjectHistory runs inline inside importProjectData,
// not as its own TaskDef) must still participate in snapshot() once added
// via AddPseudoTask and given progress via the same Registry every real
// task uses, with its expected duration coming from SetExpectedDuration
// rather than the static seed table.
func TestTrackerAddPseudoTaskAndSetExpectedDuration(t *testing.T) {
	plan := [][]string{{"data-1"}}
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, CategoryWeights{ProjectData: 100}, ExpectedTaskDuration)
	tr.MarkTaskComplete("data-1")

	// Before AddPseudoTask: the pseudo-task doesn't exist yet, data-1 alone is 100%.
	percent, _, _ := tr.snapshot()
	if percent != 100 {
		t.Fatalf("before AddPseudoTask: percent = %v, want 100", percent)
	}

	tr.AddPseudoTask(CategoryProjectData, "migrateProjectHistory")
	tr.SetExpectedDuration("migrateProjectHistory", 10*time.Second)
	tr.SetExpectedDuration("data-1", 10*time.Second) // put both on equal footing for a clean 50/50 check
	registerPartial(tr, "migrateProjectHistory", 5, 10)

	percent, _, _ = tr.snapshot()
	if !almostEqual(percent, 75, 0.1) { // data-1 100% + migrateProjectHistory 50%, equal weight each -> 75%
		t.Errorf("percent = %v, want ~75 (data-1 done, migrateProjectHistory half done, equal expected durations)", percent)
	}
}

func TestTrackerSnapshotEmptyPlan(t *testing.T) {
	tr := NewTracker(testLogger(), nil, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
	percent, eta, known := tr.snapshot()
	if percent != 0 || eta != 0 || known {
		t.Errorf("empty plan: got percent=%v eta=%v known=%v, want 0,0,false", percent, eta, known)
	}
}

func TestTrackerSnapshotFullyComplete(t *testing.T) {
	plan := [][]string{{"general-1", "config-1", "data-1", "sync-1"}}
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
	for _, n := range []string{"general-1", "config-1", "data-1", "sync-1"} {
		tr.MarkTaskComplete(n)
	}
	percent, eta, known := tr.snapshot()
	if percent != 100 {
		t.Errorf("percent = %v, want 100", percent)
	}
	if !known {
		t.Errorf("known = false, want true once percent > 0")
	}
	if eta < 0 {
		t.Errorf("eta = %v, want >= 0", eta)
	}
}

// TestTrackerStartLogsPeriodically exercises the real ticker goroutine
// end-to-end: Start must emit "Overall progress: X% - ETA: ..." lines on
// the given interval and Stop must cleanly halt it.
func TestTrackerStartLogsPeriodically(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	plan := [][]string{{"general-1", "config-1"}}
	tr := NewTracker(logger, plan, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
	registerPartial(tr, "config-1", 1, 2) // non-zero progress so ETA becomes known

	tr.Start(context.Background(), 10*time.Millisecond)
	time.Sleep(35 * time.Millisecond)
	tr.Stop()

	out := buf.String()
	if !strings.Contains(out, "-----> Overall progress:") {
		t.Errorf("expected at least one progress line, got: %s", out)
	}
	// Progress is non-zero from the first tick (config-1 registered at
	// 1/2 before Start), so the ETA should already be a duration, not
	// the pre-first-signal placeholder.
	if strings.Contains(out, "calculating...") {
		t.Errorf("expected a known ETA once progress > 0, got: %s", out)
	}
}

// LogFinal must emit the exact closing line regardless of the tracker's
// actual last-computed snapshot — it's called once a run has already
// finished successfully, so "100%/00:00:00" is asserted, not derived.
func TestTrackerLogFinal(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	plan := [][]string{{"general-1", "config-1"}}
	tr := NewTracker(logger, plan, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
	// Deliberately leave everything incomplete — LogFinal must still print
	// the fixed closing line, not a snapshot-derived one.
	registerPartial(tr, "config-1", 1, 2)

	tr.LogFinal()

	want := "-----> Overall progress: 100% - ETA: 00:00:00"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("expected closing line %q, got: %s", want, buf.String())
	}
}

// LogFinal must be a no-op on a nil *Tracker (same nil-safety contract as
// the rest of the type).
func TestTrackerLogFinalNilSafe(t *testing.T) {
	var tr *Tracker
	tr.LogFinal() // must not panic
}

// OnUpdate's callback (#519) must fire with the same values as the log
// line on every tick, so a GUI progress bar stays in sync with the #520
// log output without re-deriving the snapshot itself.
func TestTrackerOnUpdateFiresFromTicker(t *testing.T) {
	logger := testLogger()

	plan := [][]string{{"general-1", "config-1"}}
	tr := NewTracker(logger, plan, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
	registerPartial(tr, "config-1", 1, 2) // non-zero progress so ETA becomes known

	type update struct {
		percent float64
		eta     time.Duration
		known   bool
	}
	var mu sync.Mutex
	var got []update
	tr.OnUpdate(func(percent float64, eta time.Duration, known bool) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, update{percent, eta, known})
	})

	tr.Start(context.Background(), 10*time.Millisecond)
	time.Sleep(35 * time.Millisecond)
	tr.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("expected at least one OnUpdate call")
	}
	for _, u := range got {
		if !u.known {
			t.Errorf("update %+v: known = false, want true (progress > 0)", u)
		}
		if u.percent <= 0 {
			t.Errorf("update %+v: percent <= 0, want > 0", u)
		}
	}
}

// Start must push one OnUpdate snapshot immediately, without waiting for
// the first tick — time.NewTicker only fires after a full interval, so
// without this a GUI progress bar would stay hidden for the whole
// interval, or (on a run shorter than interval) never show anything
// before LogFinal's single closing call. Uses a long interval so only
// the immediate call, never a real tick, could produce a result (#519).
func TestTrackerOnUpdateFiresImmediatelyOnStart(t *testing.T) {
	plan := [][]string{{"general-1", "config-1"}}
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
	registerPartial(tr, "config-1", 1, 2)

	var mu sync.Mutex
	var calls int
	tr.OnUpdate(func(percent float64, eta time.Duration, known bool) {
		mu.Lock()
		defer mu.Unlock()
		calls++
	})

	tr.Start(context.Background(), time.Hour)
	defer tr.Stop()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("calls = %d, want exactly 1 (the immediate snapshot, no tick could have fired)", calls)
	}
}

// LogFinal must push the fixed 100%/0s closing snapshot through OnUpdate
// too, so the GUI snaps its bar to "done" immediately rather than waiting
// for the next tick.
func TestTrackerOnUpdateFiresFromLogFinal(t *testing.T) {
	plan := [][]string{{"general-1", "config-1"}}
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights, ExpectedTaskDuration)
	registerPartial(tr, "config-1", 1, 2)

	var gotPercent float64
	var gotETA time.Duration
	var gotKnown bool
	tr.OnUpdate(func(percent float64, eta time.Duration, known bool) {
		gotPercent, gotETA, gotKnown = percent, eta, known
	})

	tr.LogFinal()

	if gotPercent != 100 {
		t.Errorf("percent = %v, want 100", gotPercent)
	}
	if gotETA != 0 {
		t.Errorf("eta = %v, want 0", gotETA)
	}
	if !gotKnown {
		t.Error("known = false, want true")
	}
}

// OnUpdate must be a no-op on a nil *Tracker, same nil-safety contract as
// the rest of the type (production call sites set it unconditionally via
// e.Progress.OnUpdate(cfg.ProgressCallback) even when Progress is unset).
func TestTrackerOnUpdateNilSafe(t *testing.T) {
	var tr *Tracker
	tr.OnUpdate(func(percent float64, eta time.Duration, known bool) {
		t.Error("callback must never be invoked via a nil Tracker")
	}) // must not panic
}

// Registry/MarkTaskComplete must be safe to call on a nil *Tracker so
// production call sites (e.Progress.Registry().Register(...) /
// e.Progress.MarkTaskComplete(name)) don't need a nil check — several
// existing extract/migrate unit tests build an Executor directly without
// ever setting Progress.
func TestTrackerNilSafety(t *testing.T) {
	var tr *Tracker
	tr.MarkTaskComplete("anything") // must not panic

	reg := tr.Registry()
	if reg != nil {
		t.Errorf("Registry() on nil Tracker = %v, want nil", reg)
	}
	reg.Register("anything", nil) // must not panic
	if got := reg.Fraction("anything"); got != 0 {
		t.Errorf("Fraction() on nil registry = %v, want 0", got)
	}
}
