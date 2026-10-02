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

// fixedDuration returns an expected-duration function that reports d for
// every task name — lets tests pin an exact, known "expected" cost
// instead of depending on the real seed table (#564).
func fixedDuration(d time.Duration) func(string) time.Duration {
	return func(string) time.Duration { return d }
}

// seconds returns an expected-duration function from a name -> seconds
// table, falling back to DefaultTaskDuration like ExpectedTaskDuration.
func seconds(table map[string]float64) func(string) time.Duration {
	return func(name string) time.Duration {
		if s, ok := table[name]; ok {
			return time.Duration(s * float64(time.Second))
		}
		return DefaultTaskDuration
	}
}

// virtualTracker builds a Tracker whose clock only moves when the test
// calls advance, so every snapshot below is exact and free of sleeps.
func virtualTracker(plan [][]string, expected func(string) time.Duration) (tr *Tracker, advance func(time.Duration)) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tr = NewTracker(testLogger(), plan, expected)
	tr.start = now
	tr.now = func() time.Time { return now }
	return tr, func(d time.Duration) { now = now.Add(d) }
}

// TestClockRemaining pins the curve a running task is counted down on
// (#621): one second per second for the first half of its seed, then a
// tail that keeps shrinking and never reaches zero.
func TestClockRemaining(t *testing.T) {
	cases := []struct{ e, want float64 }{
		{0, 100},
		{30, 70},
		{50, 50},    // the two pieces meet here
		{100, 25},   // on seed: a quarter of the seed still shown
		{200, 12.5}, // overrunning: still shrinking, never zero
		{1000, 2.5},
	}
	for _, c := range cases {
		if got := clockRemaining(c.e, 100); !almostEqual(got, c.want, 1e-9) {
			t.Errorf("clockRemaining(%v, 100) = %v, want %v", c.e, got, c.want)
		}
	}
	// Same slope either side of the join, so the ETA has no kink there.
	left := clockRemaining(50, 100) - clockRemaining(49.999, 100)
	right := clockRemaining(50.001, 100) - clockRemaining(50, 100)
	if !almostEqual(left, right, 1e-6) {
		t.Errorf("slope changes at e = x/2: %v then %v", left/0.001, right/0.001)
	}
}

// TestTrackerETASumsTheSlowestTaskOfEachPhase: phases run one after
// another and a phase's tasks run together, so with nothing started the
// ETA is the slowest seed of each phase, added up (#621).
func TestTrackerETASumsTheSlowestTaskOfEachPhase(t *testing.T) {
	plan := [][]string{{"a", "b"}, {"c"}}
	tr, _ := virtualTracker(plan, seconds(map[string]float64{"a": 10, "b": 30, "c": 20}))

	percent, eta, known := tr.snapshot()
	if !known {
		t.Fatal("known = false, want true — a plan with seeds has an ETA from the start")
	}
	if eta != 50*time.Second {
		t.Errorf("eta = %v, want 50s (max(10,30) + 20)", eta)
	}
	if percent != 0 {
		t.Errorf("percent = %v, want 0 before any time has passed", percent)
	}
}

// TestTrackerETAFallsWithTheClockAndPercentFollowsIt is #621's ask in its
// simplest form: a task running on seed makes the ETA drop one second per
// second, and the percentage is the share of the estimated run elapsed.
func TestTrackerETAFallsWithTheClockAndPercentFollowsIt(t *testing.T) {
	tr, advance := virtualTracker([][]string{{"a"}}, fixedDuration(100*time.Second))
	tr.MarkTaskStarted("a")
	advance(25 * time.Second)

	percent, eta, _ := tr.snapshot()
	if eta != 75*time.Second {
		t.Errorf("eta = %v, want 75s", eta)
	}
	if !almostEqual(percent, 25, 1e-9) {
		t.Errorf("percent = %v, want 25", percent)
	}
}

// TestTrackerETANeverClimbsWhileATaskOverrunsItsSeed is the regression
// guard for #621's main symptom. A 3s-seeded task that really runs for
// minutes, with a big task still to come, made the old ETA climb about
// two minutes for every ten seconds, because it multiplied elapsed time
// by remaining/consumed seeded work. Counting real seconds left instead,
// the ETA may hold level but must never rise.
func TestTrackerETANeverClimbsWhileATaskOverrunsItsSeed(t *testing.T) {
	plan := [][]string{{"small"}, {"big"}}
	tr, advance := virtualTracker(plan, seconds(map[string]float64{"small": 3, "big": 300}))
	tr.MarkTaskStarted("small")

	_, prev, _ := tr.snapshot()
	for i := 0; i < 20; i++ {
		advance(10 * time.Second)
		_, eta, known := tr.snapshot()
		if !known {
			t.Fatalf("tick %d: known = false", i+1)
		}
		if eta > prev {
			t.Fatalf("tick %d: eta rose from %v to %v while small overran its seed", i+1, prev, eta)
		}
		if eta < 300*time.Second {
			t.Fatalf("tick %d: eta = %v, want at least big's 300s, which has not started", i+1, eta)
		}
		prev = eta
	}
}

// TestTrackerPercentNeverGoesBackwards: when a finished task shows the run
// is slower than seeded, the ETA for the rest grows, and with it the
// estimated total. The percentage must hold, not fall.
func TestTrackerPercentNeverGoesBackwards(t *testing.T) {
	plan := [][]string{{"a"}, {"b"}}
	tr, advance := virtualTracker(plan, seconds(map[string]float64{"a": 10, "b": 100}))
	tr.MarkTaskStarted("a")
	advance(100 * time.Second) // a runs 10x over its seed
	before, _, _ := tr.snapshot()

	tr.MarkTaskComplete("a") // banks 100s against 10s: b's seed scales up
	after, eta, _ := tr.snapshot()
	if raw := 100 * 100 / (100 + eta.Seconds()); raw >= before {
		t.Fatalf("test setup: the uncapped percentage %v should be below %v, or nothing is under test", raw, before)
	}
	if after < before {
		t.Errorf("percent fell from %v to %v", before, after)
	}
}

// TestTrackerItemProgressEndsATaskButIsNotExtrapolated: 9 of 10 items done
// a moment after start says nothing reliable about the time left, because
// items are not equal cost (#621 — the history replay's last 196 of 496
// points took longer than the first 300). So the ETA keeps counting the
// task down on its seed. Once every item is done, though, the task is
// done, even before MarkTaskComplete.
func TestTrackerItemProgressEndsATaskButIsNotExtrapolated(t *testing.T) {
	tr, advance := virtualTracker([][]string{{"sync"}}, fixedDuration(492*time.Second))
	tr.MarkTaskStarted("sync")
	advance(2 * time.Second)

	registerPartial(tr, "sync", 9, 10)
	if _, eta, _ := tr.snapshot(); eta != 490*time.Second {
		t.Errorf("eta at 9/10 items = %v, want 490s — the seed, counted down, not the item rate", eta)
	}

	registerPartial(tr, "sync", 10, 10)
	if percent, eta, _ := tr.snapshot(); eta != 0 || percent != 100 {
		t.Errorf("at 10/10 items: percent=%v eta=%v, want 100 and 0", percent, eta)
	}
}

// TestTrackerPseudoTaskRunsAlongsideItsHost covers #554's history replay:
// it runs inside importProjectData, never gets a start mark of its own,
// and must count as running alongside that task rather than after it.
func TestTrackerPseudoTaskRunsAlongsideItsHost(t *testing.T) {
	plan := [][]string{{"importProjectData"}, {"syncIssueMetadata"}}
	tr, advance := virtualTracker(plan, seconds(map[string]float64{"importProjectData": 100, "syncIssueMetadata": 10}))
	tr.AddPseudoTask("migrateProjectHistory", "importProjectData")
	tr.SetExpectedDuration("migrateProjectHistory", 900*time.Second)

	if _, eta, _ := tr.snapshot(); eta != 910*time.Second {
		t.Errorf("eta before start = %v, want 910s (max(100, 900) + 10)", eta)
	}

	tr.MarkTaskStarted("importProjectData")
	advance(100 * time.Second)
	if _, eta, _ := tr.snapshot(); eta != 810*time.Second {
		t.Errorf("eta 100s in = %v, want 810s — the replay borrows its host's start", eta)
	}

	// A host that is not in the plan puts the pseudo-task in its own
	// phase at the end, so it still counts.
	alone, _ := virtualTracker([][]string{{"x"}}, fixedDuration(10*time.Second))
	alone.AddPseudoTask("migrateProjectHistory", "importProjectData")
	alone.SetExpectedDuration("migrateProjectHistory", 900*time.Second)
	if _, eta, _ := alone.snapshot(); eta != 910*time.Second {
		t.Errorf("eta with an absent host = %v, want 910s", eta)
	}
}

// TestTrackerSpeedFactorScalesSeedsButNotSizedOverrides: a run measured
// slower than its seeds stretches every plain seed still ahead, but not a
// SetExpectedDuration override, which is already sized from this run's
// own item counts (#621).
func TestTrackerSpeedFactorScalesSeedsButNotSizedOverrides(t *testing.T) {
	plan := [][]string{{"done"}, {"plain"}, {"sized"}}
	tr, advance := virtualTracker(plan, fixedDuration(200*time.Second))
	tr.SetExpectedDuration("sized", 200*time.Second)
	tr.MarkTaskStarted("done")
	advance(2000 * time.Second) // 10x its seed, on enough work for full confidence
	tr.MarkTaskComplete("done")

	if _, eta, _ := tr.snapshot(); eta != 2200*time.Second {
		t.Errorf("eta = %v, want 2200s (plain 200s x 10, sized 200s as is)", eta)
	}
}

func TestTrackerSnapshotEmptyPlan(t *testing.T) {
	tr := NewTracker(testLogger(), nil, ExpectedTaskDuration)
	percent, eta, known := tr.snapshot()
	if percent != 0 || eta != 0 || known {
		t.Errorf("empty plan: got percent=%v eta=%v known=%v, want 0,0,false", percent, eta, known)
	}
}

func TestTrackerSnapshotFullyComplete(t *testing.T) {
	plan := [][]string{{"general-1", "config-1", "data-1", "sync-1"}}
	tr := NewTracker(testLogger(), plan, ExpectedTaskDuration)
	for _, n := range []string{"general-1", "config-1", "data-1", "sync-1"} {
		tr.MarkTaskComplete(n)
	}
	percent, eta, known := tr.snapshot()
	if percent != 100 || eta != 0 || !known {
		t.Errorf("got percent=%v eta=%v known=%v, want 100, 0, true", percent, eta, known)
	}
}

// TestTrackerStartLogsPeriodically exercises the real ticker goroutine
// end-to-end: Start must emit "Overall progress: X% - ETA: ..." lines on
// the given interval and Stop must cleanly halt it.
func TestTrackerStartLogsPeriodically(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	plan := [][]string{{"general-1", "config-1"}}
	tr := NewTracker(logger, plan, ExpectedTaskDuration)
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
	tr := NewTracker(logger, plan, ExpectedTaskDuration)
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
	tr := NewTracker(logger, plan, ExpectedTaskDuration)
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
	tr := NewTracker(testLogger(), plan, ExpectedTaskDuration)
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
	tr := NewTracker(testLogger(), plan, ExpectedTaskDuration)
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

// completeWithDuration drives the same path runPhase does — MarkTaskStarted
// then MarkTaskComplete — but backdates the start so the banked observed
// duration is exactly took, with no real sleep.
func completeWithDuration(tr *Tracker, name string, took time.Duration) {
	tr.mu.Lock()
	tr.running[name] = time.Now().Add(-took)
	tr.mu.Unlock()
	tr.MarkTaskComplete(name)
}

// TestTrackerMarkTaskCompleteRecordsObservedDuration: the observed
// duration of a finished task is what every calibration below is built
// on, so MarkTaskComplete must bank it whenever a start was recorded, and
// bank nothing when one wasn't (#564).
func TestTrackerMarkTaskCompleteRecordsObservedDuration(t *testing.T) {
	plan := [][]string{{"general-1", "general-2"}}
	tr := NewTracker(testLogger(), plan, fixedDuration(time.Second))

	completeWithDuration(tr, "general-1", 7*time.Second)
	if got := tr.actual["general-1"]; got < 7*time.Second || got > 8*time.Second {
		t.Errorf("observed duration = %v, want ~7s", got)
	}

	// Never started (tests that exercise a task's Run directly), so there
	// is no honest duration to bank — and a zero must not be invented,
	// since it would drag the speed factor toward "infinitely fast".
	tr.MarkTaskComplete("general-2")
	if got, ok := tr.actual["general-2"]; ok {
		t.Errorf("observed duration for a task that never started = %v, want none recorded", got)
	}
}

// TestTrackerObservedSpeedFactorAtFullConfidence: once enough seeded work
// has completed, the factor is simply real-over-seeded — 200s of seeded
// work that really took 20s means this run is running at a tenth of the
// mined seeds (#564).
func TestTrackerObservedSpeedFactorAtFullConfidence(t *testing.T) {
	plan := [][]string{{"general-1"}}
	tr := NewTracker(testLogger(), plan, fixedDuration(200*time.Second))

	completeWithDuration(tr, "general-1", 20*time.Second)

	if got := tr.observedSpeedFactor(tr.actual, nil); !almostEqual(got, 0.1, 0.02) {
		t.Errorf("speed factor = %v, want ~0.1", got)
	}
}

// TestTrackerObservedSpeedFactorBlendsWhileWorkIsThin: a run's first
// completions are all tiny orchestration tasks, and their ratio says
// little about what importProjectData will cost. Until fullCalibrationWork
// seconds of seeded work have finished, the factor must be blended toward
// 1 in proportion rather than applied outright.
func TestTrackerObservedSpeedFactorBlendsWhileWorkIsThin(t *testing.T) {
	plan := [][]string{{"general-1"}}
	tr := NewTracker(testLogger(), plan, fixedDuration(30*time.Second))

	completeWithDuration(tr, "general-1", 3*time.Second)

	// raw 0.1, confidence 30/120 = 0.25 -> 1 + 0.25*(0.1-1) = 0.775
	if got := tr.observedSpeedFactor(tr.actual, nil); !almostEqual(got, 0.775, 0.02) {
		t.Errorf("speed factor = %v, want ~0.775 (blended toward 1 on thin evidence)", got)
	}
}

// TestTrackerObservedSpeedFactorNoObservations: before anything has
// finished there is nothing to calibrate against, and the factor must be
// exactly 1 so behaviour falls back to the raw seeds.
func TestTrackerObservedSpeedFactorNoObservations(t *testing.T) {
	plan := [][]string{{"general-1"}}
	tr := NewTracker(testLogger(), plan, fixedDuration(200*time.Second))

	if got := tr.observedSpeedFactor(tr.actual, nil); got != 1 {
		t.Errorf("speed factor with no completions = %v, want exactly 1", got)
	}
}

// TestTrackerObservedSpeedFactorClamped: a single pathological
// observation must not be able to rewrite every remaining estimate by an
// arbitrary factor in either direction.
func TestTrackerObservedSpeedFactorClamped(t *testing.T) {
	t.Run("absurdly fast", func(t *testing.T) {
		plan := [][]string{{"general-1"}}
		tr := NewTracker(testLogger(), plan, fixedDuration(200*time.Second))
		completeWithDuration(tr, "general-1", time.Millisecond)

		if got := tr.observedSpeedFactor(tr.actual, nil); !almostEqual(got, minSpeedFactor, 0.001) {
			t.Errorf("speed factor = %v, want clamped to %v", got, minSpeedFactor)
		}
	})

	t.Run("absurdly slow", func(t *testing.T) {
		plan := [][]string{{"general-1"}}
		tr := NewTracker(testLogger(), plan, fixedDuration(200*time.Second))
		completeWithDuration(tr, "general-1", 100*time.Hour)

		if got := tr.observedSpeedFactor(tr.actual, nil); !almostEqual(got, maxSpeedFactor, 0.001) {
			t.Errorf("speed factor = %v, want clamped to %v", got, maxSpeedFactor)
		}
	})
}

// TestTrackerETAIsZeroWhenNoWorkRemains: with every task complete there
// is no remaining work, so the ETA must be exactly zero rather than a
// leftover extrapolation.
func TestTrackerETAIsZeroWhenNoWorkRemains(t *testing.T) {
	plan := [][]string{{"general-1", "sync-1"}}
	tr := NewTracker(testLogger(), plan, ExpectedTaskDuration)
	tr.start = time.Now().Add(-10 * time.Second)

	completeWithDuration(tr, "general-1", 5*time.Second)
	completeWithDuration(tr, "sync-1", 5*time.Second)

	percent, eta, known := tr.snapshot()
	if percent != 100 || eta != 0 || !known {
		t.Errorf("got percent=%v eta=%v known=%v, want 100, 0, true", percent, eta, known)
	}
}

// TestTrackerETAIncludesProjectHistoryReplay covers issue #564's fourth
// ask directly: with --migrate_history on, the replay's per-run expected
// duration must reach the remaining work the ETA is built from, not only
// the reported percentage. The replay is a pseudo-task — it runs inline
// inside importProjectData rather than as its own TaskDef — so its cost
// arrives via SetExpectedDuration rather than the static seed table.
func TestTrackerETAIncludesProjectHistoryReplay(t *testing.T) {
	plan := [][]string{{"data-1"}}
	newTracker := func() *Tracker {
		tr := NewTracker(testLogger(), plan, fixedDuration(100*time.Second))
		tr.start = time.Now().Add(-100 * time.Second)
		completeWithDuration(tr, "data-1", 100*time.Second)
		return tr
	}

	// History off: data-1 is the whole plan and it is finished, so
	// nothing is left to wait for.
	_, off, _ := newTracker().snapshot()
	if off != 0 {
		t.Fatalf("eta with history off = %v, want 0 — the only task in the plan completed", off)
	}

	tr := newTracker()
	tr.AddPseudoTask("migrateProjectHistory", "importProjectData")
	tr.SetExpectedDuration("migrateProjectHistory", 900*time.Second)

	_, on, known := tr.snapshot()
	if !known {
		t.Fatal("known = false, want true")
	}
	// 100s of elapsed time bought 100s of seeded work; 900s of replay is
	// still ahead of us.
	if !almostEqual(on.Seconds(), 900, 30) {
		t.Errorf("eta with history on = %v, want ~900s", on)
	}
}

// TestTrackerETAReportsUnknownRatherThanNonsense: a snapshot taken in the
// first instants of a task divides by a near-zero consumed-work figure,
// and an unbounded result overflows time.Duration's int64 into a NEGATIVE
// duration — an operator would read a nonsense clock. Past
// maxReportableETASeconds the tracker must report "calculating..." (known
// false, eta zero) instead.
func TestTrackerETAReportsUnknownRatherThanNonsense(t *testing.T) {
	plan := [][]string{{"general-1", "general-2"}}
	tr := NewTracker(testLogger(), plan, fixedDuration(400*24*time.Hour))
	tr.start = time.Now().Add(-time.Hour)

	// Microseconds of task time against a seed of well over a year.
	tr.MarkTaskStarted("general-1")

	_, eta, known := tr.snapshot()
	if known {
		t.Errorf("known = true with eta %v, want false — an unbounded estimate must read as calculating, not as a clock", eta)
	}
	if eta != 0 {
		t.Errorf("eta = %v, want 0 alongside known=false", eta)
	}
}

// TestTrackerETAGrowsWhenTheRunIsSlowerThanSeeded: the calibration has to
// work in both directions. A run whose tasks over-run their seeds must
// report a longer ETA for the same remaining task list than one whose
// tasks came in on seed.
func TestTrackerETAGrowsWhenTheRunIsSlowerThanSeeded(t *testing.T) {
	plan := [][]string{{"general-1", "general-2"}}
	expected := fixedDuration(100 * time.Second)

	// Elapsed time is held identical across both cases on purpose, and
	// general-2 is left in flight. The ONLY thing that differs is how long
	// the finished task was observed to take, so the difference in ETA can
	// only come from the calibration: an earlier version of this test also
	// varied elapsed, which moved the ETA on its own and let the test pass
	// with calibration disabled entirely.
	etaFor := func(observed time.Duration) time.Duration {
		tr := NewTracker(testLogger(), plan, expected)
		tr.start = time.Now().Add(-200 * time.Second)
		completeWithDuration(tr, "general-1", observed)
		tr.mu.Lock()
		tr.running["general-2"] = time.Now().Add(-50 * time.Second)
		tr.mu.Unlock()
		_, eta, _ := tr.snapshot()
		return eta
	}

	onSeed := etaFor(100 * time.Second) // factor ~1: general-2 credited against its raw 100s seed
	slow := etaFor(400 * time.Second)   // factor ~3.5: general-2 credited against ~350s, so less of it is done

	if slow <= onSeed {
		t.Errorf("slow run eta = %v, on-seed run eta = %v — a run measured slower than its seeds must credit in-flight work more slowly, and so report a longer ETA", slow, onSeed)
	}
}
