// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// issue621Timeline is the 33m37s, 78-project migrate run with
// --migrate_history that #621 reported, reduced by
// testdata/extract_timeline.py to task names and timings only. Unlike
// recordedMigrateRun it also carries item-level progress (interpolated to
// one-second steps between the logged 20-item marks) and what the tool
// itself printed at every tick, so a replay can be checked against the
// original output.
const issue621TimelinePath = "testdata/issue621_migrate_timeline.tsv"

type timelineEvent struct {
	atMs  int
	kind  string // start, end, frac
	task  string
	done  int64
	total int
}

type timeline struct {
	plan    [][]string
	pseudo  map[string]int // pseudo-task name -> item total
	events  []timelineEvent
	logged  []replayTick
	totalMs int
}

func loadTimeline(t *testing.T, path string) timeline {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	tl := timeline{pseudo: map[string]int{}}
	phases := map[int][]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parseTimelineRow(t, strings.Split(sc.Text(), "\t"), &tl, phases)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	keys := make([]int, 0, len(phases))
	for k := range phases {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		tl.plan = append(tl.plan, phases[k])
	}
	sort.SliceStable(tl.events, func(i, j int) bool { return tl.events[i].atMs < tl.events[j].atMs })
	return tl
}

func parseTimelineRow(t *testing.T, cols []string, tl *timeline, phases map[int][]string) {
	t.Helper()
	atoi := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("bad number %q in timeline row %v", s, cols)
		}
		return n
	}
	switch cols[0] {
	case "plan":
		ph := atoi(cols[1])
		phases[ph] = append(phases[ph], cols[2])
	case "pseudo":
		tl.pseudo[cols[2]] = atoi(cols[3])
	case "start", "end":
		tl.events = append(tl.events, timelineEvent{atMs: atoi(cols[1]), kind: cols[0], task: cols[2]})
	case "frac":
		parts := strings.SplitN(cols[3], "/", 2)
		tl.events = append(tl.events, timelineEvent{atMs: atoi(cols[1]), kind: "frac", task: cols[2],
			done: int64(atoi(parts[0])), total: atoi(parts[1])})
	case "logged":
		tl.logged = append(tl.logged, replayTick{elapsed: float64(atoi(cols[1])) / 1000,
			percent: float64(atoi(cols[2])), eta: float64(atoi(cols[3]))})
	case "total":
		tl.totalMs = atoi(cols[1])
	default:
		t.Fatalf("unknown timeline row kind %q", cols[0])
	}
}

// replayTimeline drives tl through a Tracker set up the way RunMigrate
// sets it up for that run, on a virtual clock, and samples it every
// interval. projects is the in-scope project count RunMigrate scales
// syncIssueMetadata by.
func replayTimeline(t *testing.T, tl timeline, interval time.Duration, projects int) []replayTick {
	t.Helper()
	origin := time.Date(2026, 9, 29, 13, 8, 4, 0, time.UTC)
	vnow := origin

	tr := NewTracker(testLogger(), tl.plan, ExpectedTaskDuration)
	tr.start = origin
	tr.now = func() time.Time { return vnow }
	for name, total := range tl.pseudo {
		tr.AddPseudoTask(name, "importProjectData")
		tr.SetExpectedDuration(name, time.Duration(float64(total)*SecondsPerHistoryPoint*float64(time.Second)))
	}
	if scaled := time.Duration(float64(projects) * SecondsPerProjectIssueSync * float64(time.Second)); scaled > ExpectedTaskDuration("syncIssueMetadata") {
		tr.SetExpectedDuration("syncIssueMetadata", scaled)
	}

	loggers := map[string]*ProgressLogger{}
	total := time.Duration(tl.totalMs) * time.Millisecond
	var ticks []replayTick
	nextTick := origin.Add(interval)
	record := func() {
		percent, eta, known := tr.snapshot()
		if !known {
			return
		}
		ticks = append(ticks, replayTick{
			elapsed:       vnow.Sub(origin).Seconds(),
			percent:       percent,
			eta:           eta.Seconds(),
			trueRemaining: (total - vnow.Sub(origin)).Seconds(),
		})
	}
	for _, ev := range tl.events {
		at := origin.Add(time.Duration(ev.atMs) * time.Millisecond)
		for !at.Before(nextTick) {
			vnow = nextTick
			record()
			nextTick = nextTick.Add(interval)
		}
		vnow = at
		switch ev.kind {
		case "start":
			tr.MarkTaskStarted(ev.task)
		case "end":
			tr.MarkTaskComplete(ev.task)
		case "frac":
			pl, ok := loggers[ev.task]
			if !ok {
				pl = NewProgressLoggerWithInterval(testLogger(), ev.task, ev.total, 1<<62)
				loggers[ev.task] = pl
				tr.Registry().Register(ev.task, pl)
			}
			pl.done.Store(ev.done)
		}
	}
	return ticks
}

// etaQuality summarises how an estimator behaved over a replay, past the
// warm-up window: how far its ETA was from the truth, how far its
// percentage was from the share of wall-clock time elapsed, and the worst
// single-tick climb of the ETA. A perfect estimator scores 0 on all three;
// #621 is about the second and third.
type etaQuality struct {
	meanETAError    float64 // seconds
	meanPctError    float64 // percentage points
	worstClimb      float64 // seconds the ETA rose in one tick
	ticksConsidered int
}

func scoreReplay(ticks []replayTick, totalSec, warmupSec float64) etaQuality {
	var q etaQuality
	var prev *replayTick
	for i := range ticks {
		tk := ticks[i]
		if tk.elapsed < warmupSec {
			continue
		}
		q.ticksConsidered++
		q.meanETAError += math.Abs(tk.eta - (totalSec - tk.elapsed))
		q.meanPctError += math.Abs(tk.percent - 100*tk.elapsed/totalSec)
		if prev != nil {
			q.worstClimb = math.Max(q.worstClimb, tk.eta-prev.eta)
		}
		prev = &ticks[i]
	}
	if q.ticksConsidered > 0 {
		q.meanETAError /= float64(q.ticksConsidered)
		q.meanPctError /= float64(q.ticksConsidered)
	}
	return q
}

// writeReplayCSV dumps a replay for plotting when ETA_REPLAY_CSV names a
// file. A no-op otherwise, so the test stays side-effect free in CI.
func writeReplayCSV(t *testing.T, ticks, logged []replayTick, totalSec float64) {
	t.Helper()
	path := os.Getenv("ETA_REPLAY_CSV")
	if path == "" {
		return
	}
	var b strings.Builder
	b.WriteString("source,elapsed_s,percent,eta_s,true_remaining_s\n")
	for _, tk := range logged {
		fmt.Fprintf(&b, "logged,%.0f,%.2f,%.0f,%.0f\n", tk.elapsed, tk.percent, tk.eta, totalSec-tk.elapsed)
	}
	for _, tk := range ticks {
		fmt.Fprintf(&b, "replay,%.0f,%.2f,%.0f,%.0f\n", tk.elapsed, tk.percent, tk.eta, tk.trueRemaining)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// assertCloseToLinear holds a replay to #621's ask, past a warm-up of the
// first tenth of the run: the ETA never rises by a whole tick interval
// between two ticks, it stays within 15% of the run's length of the time
// really left on average, and the percentage stays within 10 points of the
// share of the run elapsed on average. The bounds are loose on purpose: a
// replay of the code before #621 misses all three on that issue's run
// (121s climbs, 447s off on a 2008s run, 15 points off).
func assertCloseToLinear(t *testing.T, ticks []replayTick, totalSec, intervalSec float64) {
	t.Helper()
	q := scoreReplay(ticks, totalSec, 0.1*totalSec)
	t.Logf("ETA error %.0fs, percent error %.1f pts, worst climb %.0fs over %d ticks",
		q.meanETAError, q.meanPctError, q.worstClimb, q.ticksConsidered)
	if q.worstClimb >= intervalSec {
		t.Errorf("the ETA rose %.0fs in one %.0fs tick; it must never climb faster than the clock", q.worstClimb, intervalSec)
	}
	if q.meanETAError > 0.15*totalSec {
		t.Errorf("mean ETA error %.0fs is over 15%% of the %.0fs run", q.meanETAError, totalSec)
	}
	if q.meanPctError > 10 {
		t.Errorf("mean percent error %.1f points is over 10 points from the share of the run elapsed", q.meanPctError)
	}
}

// TestTrackerIssue621ReplayIsCloseToLinear replays #621's run and holds
// the estimator to the issue's ask: after a warm-up, the ETA should fall
// roughly one second per second of wall clock, and the percentage should
// track the share of the run that has elapsed. ETA_REPLAY_CSV writes the
// replay, and the tool's own logged numbers, out for plotting.
func TestTrackerIssue621ReplayIsCloseToLinear(t *testing.T) {
	tl := loadTimeline(t, issue621TimelinePath)
	totalSec := float64(tl.totalMs) / 1000
	ticks := replayTimeline(t, tl, 10*time.Second, 78)
	writeReplayCSV(t, ticks, tl.logged, totalSec)
	assertCloseToLinear(t, ticks, totalSec, 10)
}
