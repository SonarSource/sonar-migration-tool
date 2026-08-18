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
func TestTrackerSnapshotWorkedExamples(t *testing.T) {
	t.Run("example 1: config in progress, data/sync not started", func(t *testing.T) {
		plan := [][]string{{
			"general-1",
			"config-1", "config-2", "config-3", "config-4", "config-5", "config-6", "config-7",
			"config-8", "config-9", "config-10", "config-11",
			"data-1",
			"sync-1",
		}}
		tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights)
		tr.MarkTaskComplete("general-1")
		for _, n := range []string{"config-1", "config-2", "config-3", "config-4", "config-5", "config-6", "config-7"} {
			tr.MarkTaskComplete(n)
		}
		registerPartial(tr, "config-8", 87, 200)
		// config-9/10/11 and data-1/sync-1 stay unregistered (not started).

		percent, _, _ := tr.snapshot()
		if !almostEqual(percent, 18.5, 0.1) {
			t.Errorf("percent = %v, want ~18.5", percent)
		}
	})

	t.Run("example 2: config done, data in progress, sync not started", func(t *testing.T) {
		plan := [][]string{{"general-1", "config-1", "data-1", "sync-1"}}
		tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights)
		tr.MarkTaskComplete("general-1")
		tr.MarkTaskComplete("config-1")
		registerPartial(tr, "data-1", 153, 200)

		percent, _, _ := tr.snapshot()
		if !almostEqual(percent, 44, 0.2) {
			t.Errorf("percent = %v, want ~44", percent)
		}
	})

	t.Run("example 3: config+data done, sync in progress", func(t *testing.T) {
		plan := [][]string{{"general-1", "config-1", "data-1", "sync-1"}}
		tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights)
		tr.MarkTaskComplete("general-1")
		tr.MarkTaskComplete("config-1")
		tr.MarkTaskComplete("data-1")
		registerPartial(tr, "sync-1", 72, 200)

		percent, _, _ := tr.snapshot()
		if !almostEqual(percent, 68, 0.1) {
			t.Errorf("percent = %v, want ~68", percent)
		}
	})

	t.Run("example 4: project data (and issue sync) turned off", func(t *testing.T) {
		// No data-*/sync-* tasks at all in the plan — the categories have
		// zero tasks and must drop out of the weighted denominator rather
		// than just contribute 0 progress.
		plan := [][]string{{"general-1", "config-1"}}
		tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights)
		tr.MarkTaskComplete("general-1")
		registerPartial(tr, "config-1", 72, 200)

		percent, _, _ := tr.snapshot()
		if !almostEqual(percent, 48.8, 0.1) {
			t.Errorf("percent = %v, want ~48.8", percent)
		}
	})

	t.Run("example 5: issue sync turned off, project data on but not started", func(t *testing.T) {
		// data-1 present (project data is on) but untouched; no sync-*
		// task at all (issue sync is off).
		plan := [][]string{{"general-1", "config-1", "data-1"}}
		tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights)
		tr.MarkTaskComplete("general-1")
		registerPartial(tr, "config-1", 72, 200)
		// data-1 stays unregistered — 0 progress, but its 25% weight
		// still counts toward the denominator.

		percent, _, _ := tr.snapshot()
		if !almostEqual(percent, 24.4, 0.1) {
			t.Errorf("percent = %v, want ~24.4", percent)
		}
	})
}

func TestTrackerSnapshotEmptyPlan(t *testing.T) {
	tr := NewTracker(testLogger(), nil, categorizeByPrefix, DefaultCategoryWeights)
	percent, eta, known := tr.snapshot()
	if percent != 0 || eta != 0 || known {
		t.Errorf("empty plan: got percent=%v eta=%v known=%v, want 0,0,false", percent, eta, known)
	}
}

func TestTrackerSnapshotFullyComplete(t *testing.T) {
	plan := [][]string{{"general-1", "config-1", "data-1", "sync-1"}}
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights)
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
	tr := NewTracker(logger, plan, categorizeByPrefix, DefaultCategoryWeights)
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
	tr := NewTracker(logger, plan, categorizeByPrefix, DefaultCategoryWeights)
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
	tr := NewTracker(logger, plan, categorizeByPrefix, DefaultCategoryWeights)
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
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights)
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
	tr := NewTracker(testLogger(), plan, categorizeByPrefix, DefaultCategoryWeights)
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
