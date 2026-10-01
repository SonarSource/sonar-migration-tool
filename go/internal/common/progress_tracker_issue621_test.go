// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
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
// five-second steps between the logged 20-item marks) and what the tool
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
// directory. A no-op otherwise, so the test stays side-effect free in CI.
func writeReplayCSV(t *testing.T, name string, ticks, logged []replayTick, totalSec float64) {
	t.Helper()
	dir := os.Getenv("ETA_REPLAY_CSV")
	if dir == "" {
		return
	}
	path := filepath.Join(dir, name+".csv")
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

// linearBounds is what a replay must stay within, past a warm-up of the
// first tenth of the run, on average. A zero field is not checked.
type linearBounds struct {
	etaShare float64 // ETA error, as a share of the run's length
	pctError float64 // percentage points from the share of the run elapsed
}

// normalRun is the bar for a run whose target keeps a steady pace: the
// ETA within 15% of the run's length of the time really left, and the
// percentage within 10 points of the share of the run elapsed. A replay of
// the code before #621 misses both on every run below (445-447s off on
// 32-34 minute runs, 15 points off).
var normalRun = linearBounds{etaShare: 0.15, pctError: 10}

// assertCloseToLinear holds a replay to #621's ask. On top of b, the ETA
// must never rise by more than 5% of the run's length in one tick: a seed
// corrected by what the run has measured may move it once, but the code
// before #621 climbed 121-128s per tick for minutes on end.
func assertCloseToLinear(t *testing.T, ticks []replayTick, totalSec float64, b linearBounds) {
	t.Helper()
	q := scoreReplay(ticks, totalSec, 0.1*totalSec)
	t.Logf("ETA error %.0fs, percent error %.1f pts, worst climb %.0fs over %d ticks",
		q.meanETAError, q.meanPctError, q.worstClimb, q.ticksConsidered)
	if q.worstClimb > 0.05*totalSec {
		t.Errorf("the ETA rose %.0fs in one tick, over 5%% of the %.0fs run", q.worstClimb, totalSec)
	}
	if b.etaShare > 0 && q.meanETAError > b.etaShare*totalSec {
		t.Errorf("mean ETA error %.0fs is over %.0f%% of the %.0fs run", q.meanETAError, 100*b.etaShare, totalSec)
	}
	if b.pctError > 0 && q.meanPctError > b.pctError {
		t.Errorf("mean percent error %.1f points is over %.1f", q.meanPctError, b.pctError)
	}
}

// issue621Replays are the real runs the estimator is held to: the run
// #621 reported, and three repeats of it on 2026-10-01 against a copy of
// the same source, 80 projects each, to sc-staging.io. The repeats took
// 32, 63 and 96 minutes: the same data, with the target's Compute Engine
// queue two to three times slower in the later two.
//
// The slow runs are held to the climb bound only. Their project-data
// import ran 2.2x and 3.5x longer than in the 32-minute run, mostly late in
// the run, and nothing the tracker measures earlier predicts that: a task
// past its seed may be nearly done or far from done. On them this
// estimator and the one before #621 are both far off (1118s and 1303s on
// the 63-minute run; 2015s and 1895s on the 96-minute one); what this one
// guarantees is that the ETA does not climb.
var issue621Replays = []struct {
	name, path string
	projects   int
	bounds     linearBounds
}{
	{"reported", issue621TimelinePath, 78, normalRun},
	{"live-run1", "testdata/issue621_live_run1_timeline.tsv", 80, normalRun},
	{"live-run2-slow-target", "testdata/issue621_live_run2_timeline.tsv", 80, linearBounds{}},
	{"live-run3-slower-target", "testdata/issue621_live_run3_timeline.tsv", 80, linearBounds{}},
}

// TestTrackerIssue621ReplayIsCloseToLinear replays each run and holds the
// estimator to the issue's ask: after a warm-up, the ETA should fall
// roughly one second per second of wall clock, and the percentage should
// track the share of the run that has elapsed. ETA_REPLAY_CSV=<dir> writes
// each replay, and the tool's own logged numbers, out for plotting.
func TestTrackerIssue621ReplayIsCloseToLinear(t *testing.T) {
	for _, r := range issue621Replays {
		t.Run(r.name, func(t *testing.T) {
			tl := loadTimeline(t, r.path)
			totalSec := float64(tl.totalMs) / 1000
			ticks := replayTimeline(t, tl, 10*time.Second, r.projects)
			writeReplayCSV(t, r.name, ticks, tl.logged, totalSec)
			assertCloseToLinear(t, ticks, totalSec, r.bounds)
		})
	}
}
