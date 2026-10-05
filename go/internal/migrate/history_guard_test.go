// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
	"github.com/sonar-solutions/sq-api-go/types"
)

// --- the replay window of migrateBranchHistory (#625) ----------------------
//
// migrateBranchHistory may only submit points strictly older than the date the
// branch's regular current-snapshot import is stamped with, and strictly newer
// than whatever the target already holds for the branch: the Compute Engine
// refuses a report dated at or before a branch's newest analysis. Extract
// cannot promise the upper bound (it reads the branch's analysisDate before it
// lists the branch's analyses, so a branch re-analysed in between leaves a
// point on exactly the regular import's date in the list), and a resumed or
// re-run migration can leave the target holding the lower one. The tests below
// pin the pure filter (replayableHistory), then the same behaviour end to end
// through migrateBranchHistory on a non-main branch, and finally four things
// the per-point replay (#625) had no test for yet: the handshake request, the
// analysis id each report carries, the order of the calls, and a failing
// handshake.

// histSnaps builds one snapshot per RFC3339 date, in the order given, versioned
// "v1", "v2", ... by position, so a test can tell WHICH points survived a
// filter and not just how many.
func histSnaps(t *testing.T, dates ...string) []historySnapshot {
	t.Helper()
	out := make([]historySnapshot, len(dates))
	for i, d := range dates {
		out[i] = historySnapshot{Date: mustTime(t, d), ProjectVersion: "v" + strconv.Itoa(i+1)}
	}
	return out
}

// histSnapDates lists snaps' dates as RFC3339 strings, in order.
func histSnapDates(snaps []historySnapshot) []string {
	out := make([]string, len(snaps))
	for i, s := range snaps {
		out[i] = s.Date.Format(time.RFC3339)
	}
	return out
}

// histOptTime parses an RFC3339 date, or returns the zero time for "" — how a
// table row says "no bound".
func histOptTime(t *testing.T, s string) time.Time {
	t.Helper()
	if s == "" {
		return time.Time{}
	}
	return mustTime(t, s)
}

// TestReplayableHistory pins the filter itself: both bounds are exclusive (a
// point on a bound is dropped, one second inside it is kept), a zero bound
// means "no bound" on that side, and what survives is the input's own points
// in the input's own order.
func TestReplayableHistory(t *testing.T) {
	const (
		d1 = "2022-01-01T00:00:00Z"
		d2 = "2022-02-01T00:00:00Z"
		d3 = "2022-03-01T00:00:00Z"
		d4 = "2022-04-01T00:00:00Z"
		d5 = "2022-05-01T00:00:00Z"
	)
	tests := []struct {
		name    string
		points  []string // dates of the extracted points, in the order handed over
		regular string   // regularImportDate; "" = zero
		target  string   // targetLastAnalysis; "" = zero
		want    []string // dates that must survive, in order
	}{
		{
			name:   "no bounds keeps every point",
			points: []string{d1, d2, d3},
			want:   []string{d1, d2, d3},
		},
		{
			// A never-analyzed source branch has its regular import stamped
			// "now", which is later than any extracted point.
			name:   "zero regular date puts no upper bound",
			points: []string{"2099-12-31T23:59:59Z"},
			target: d1,
			want:   []string{"2099-12-31T23:59:59Z"},
		},
		{
			// Documents the contract: for any real date a zero bound is inert
			// anyway (every date is after it), so unlike the zero regular date
			// this row would also pass without replayableHistory's IsZero guard.
			name:    "zero target date puts no lower bound",
			points:  []string{"1971-01-01T00:00:00Z"},
			regular: d1,
			want:    []string{"1971-01-01T00:00:00Z"},
		},
		{
			name:    "a point equal to the regular import date is dropped",
			points:  []string{d1, d2, d3},
			regular: d3,
			want:    []string{d1, d2},
		},
		{
			name:    "a point one second before the regular import date is kept",
			points:  []string{"2022-02-28T23:59:59Z"},
			regular: d3,
			want:    []string{"2022-02-28T23:59:59Z"},
		},
		{
			name:    "a point one second after the regular import date is dropped",
			points:  []string{"2022-03-01T00:00:01Z"},
			regular: d3,
		},
		{
			// The branch was re-analysed twice between extract's two reads.
			name:    "every point past the regular import date goes, not just the equal one",
			points:  []string{d1, d2, d3, d4, d5},
			regular: d3,
			want:    []string{d1, d2},
		},
		{
			name:   "a point equal to the target's last analysis is dropped",
			points: []string{d1, d2, d3},
			target: d2,
			want:   []string{d3},
		},
		{
			name:   "a point one second after the target's last analysis is kept",
			points: []string{"2022-02-01T00:00:01Z"},
			target: d2,
			want:   []string{"2022-02-01T00:00:01Z"},
		},
		{
			name:   "a point one second before the target's last analysis is dropped",
			points: []string{"2022-01-31T23:59:59Z"},
			target: d2,
		},
		{
			name:    "both bounds keep only the points strictly between them",
			points:  []string{d1, d2, d3, d4, d5},
			regular: d4,
			target:  d2,
			want:    []string{d3},
		},
		{
			name:    "the bounds can leave nothing",
			points:  []string{d1, d2, d3},
			regular: d4,
			target:  d3,
		},
		{
			name:    "empty input stays empty",
			regular: d3,
			target:  d1,
		},
		{
			// loadExtractedAnalysisHistory already sorted the points; the
			// filter must neither reorder nor re-sort what it keeps.
			name:    "survivors keep the order they came in",
			points:  []string{d4, d2, d3, d1},
			regular: d5,
			want:    []string{d4, d2, d3, d1},
		},
		{
			// parseISODate keeps each date's own UTC offset, and the
			// getBranches and getProjectAnalysisHistory dates need not agree on
			// one. 01:00+01:00 is the same instant as d3.
			name:    "bounds compare as instants, not as wall-clock strings",
			points:  []string{d3, "2022-02-28T23:59:59Z", "2022-03-01T00:59:59+01:00"},
			regular: "2022-03-01T01:00:00+01:00",
			want:    []string{"2022-02-28T23:59:59Z", "2022-03-01T00:59:59+01:00"},
		},
		{
			name:   "a target bound in another zone compares as an instant too",
			points: []string{d2, "2022-02-01T00:00:01Z"},
			target: "2022-02-01T01:00:00+01:00",
			want:   []string{"2022-02-01T00:00:01Z"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := histSnaps(t, tc.points...)

			got := replayableHistory(in, histOptTime(t, tc.regular), histOptTime(t, tc.target))

			if dates := histSnapDates(got); !slices.Equal(dates, tc.want) {
				t.Errorf("replayableHistory kept %v, want %v", dates, tc.want)
			}
		})
	}
}

// TestReplayableHistoryNeverMutatesOrAliasesItsInput pins the "new slice"
// contract. The caller keeps using its own list after filtering (it counts
// what was dropped by comparing lengths), and an in-place filter
// (out := snaps[:0]) would silently overwrite the dropped points with the
// survivors.
func TestReplayableHistoryNeverMutatesOrAliasesItsInput(t *testing.T) {
	const (
		d1 = "2022-01-01T00:00:00Z"
		d2 = "2022-02-01T00:00:00Z"
		d3 = "2022-03-01T00:00:00Z"
		d4 = "2022-04-01T00:00:00Z"
	)
	fixture := func(t *testing.T) (in, before []historySnapshot) {
		t.Helper()
		in = histSnaps(t, d1, d2, d3, d4)
		in[1].Measures = []scanreport.MeasureInput{{Component: projMain, MetricKey: "ncloc", Value: "100"}}
		return in, slices.Clone(in)
	}

	t.Run("points are dropped", func(t *testing.T) {
		in, before := fixture(t)

		// d1 is at or before the target, d3 and d4 at or after the regular
		// import: only the second point survives, and it sits at index 1, so
		// an in-place compaction would have moved it over in[0].
		got := replayableHistory(in, mustTime(t, d3), mustTime(t, d1))

		if want := []string{d2}; !slices.Equal(histSnapDates(got), want) {
			t.Fatalf("replayableHistory kept %v, want %v", histSnapDates(got), want)
		}
		if !reflect.DeepEqual(in, before) {
			t.Errorf("the input was modified: now %v, was %v", histSnapDates(in), histSnapDates(before))
		}
	})

	t.Run("nothing is dropped", func(t *testing.T) {
		in, before := fixture(t)

		got := replayableHistory(in, time.Time{}, time.Time{})

		if len(got) != len(in) {
			t.Fatalf("replayableHistory kept %d points, want all %d", len(got), len(in))
		}
		// Even when everything survives the result must be a copy: writing to
		// it may not reach the caller's list.
		got[0].ProjectVersion = "mutated"
		got[1].Measures = nil
		if !reflect.DeepEqual(in, before) {
			t.Errorf("the result aliases the input: writing to it changed the input to %+v", in)
		}
	})
}

// --- a fake target for the full-loop tests ----------------------------------

// histHandshakes answers POST /analysis/analyses like histHandshakeHandler,
// except that every call gets an analysis id of its own ("hist-analysis-1",
// "hist-analysis-2", ...) and every request body is kept, so a test can check
// both what the tool asked for and which answer ended up in which report.
// failAt, when positive, makes that 1-based call a plain HTTP 500 instead.
type histHandshakes struct {
	rec    *histRecorder
	failAt int

	mu     sync.Mutex
	bodies []map[string]string
	ids    []string // one entry per call: the id issued, "" for a failed one
}

func (h *histHandshakes) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.rec.note(r.URL.Path)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the create-analysis request body: %v", err)
		}
		id, ok := h.answer(body)
		if !ok {
			http.Error(w, `{"errors":[{"msg":"boom"}]}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "branchId": "hist-branch-id",
			"branchType": "long", "referenceBranchName": branchMain,
		})
	}
}

// answer records one call and decides its reply: a fresh id, or ok == false for
// the call failAt names.
func (h *histHandshakes) answer(body map[string]string) (id string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bodies = append(h.bodies, body)
	n := len(h.bodies)
	if n == h.failAt {
		h.ids = append(h.ids, "")
		return "", false
	}
	id = "hist-analysis-" + strconv.Itoa(n)
	h.ids = append(h.ids, id)
	return id, true
}

func (h *histHandshakes) requestBodies() []map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.bodies)
}

func (h *histHandshakes) issuedIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.ids)
}

// histTarget is a fake SonarQube Cloud target for the full-loop tests: it
// advertises one JS quality profile, answers each create-analysis handshake
// with an id of its own, accepts each upload under its own CE task id, and
// reports every task SUCCESS. Whatever else it is asked for is recorded in
// rec too (the catch-all notes it), so "made no network call at all" is
// checkable as rec.total() == 0.
type histTarget struct {
	e    *Executor
	rec  *histRecorder
	hs   *histHandshakes
	logs *bytes.Buffer // Info and above
}

// newHistTarget wires histTarget to a fresh Executor with --migrate_history on.
// failHandshakeAt makes that 1-based create-analysis call fail with HTTP 500;
// 0 means none does.
func newHistTarget(t *testing.T, failHandshakeAt int) *histTarget {
	t.Helper()
	rec := newHistRecorder()
	hs := &histHandshakes{rec: rec, failAt: failHandshakeAt}
	mux := histProfileMux(rec)
	mux.HandleFunc("POST /analysis/analyses", hs.handler(t))
	mux.HandleFunc("POST /api/ce/submit", func(w http.ResponseWriter, r *http.Request) {
		rec.note(r.URL.Path)
		rec.captureSubmit(t, r)
		_ = json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-hist-" + strconv.Itoa(rec.count(r.URL.Path))})
	})
	mux.HandleFunc("GET /api/ce/task", func(w http.ResponseWriter, r *http.Request) {
		rec.note(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"status": "SUCCESS"}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rec.note(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})

	e := newHistCloudTestWithHandshake(t, mux)
	e.MigrateHistory = true
	return &histTarget{e: e, rec: rec, hs: hs, logs: histLogBuf(e, slog.LevelInfo)}
}

// histSeedBranch writes one extracted history record per date for branch,
// versioned "1.0", "2.0", ... in the order given.
func histSeedBranch(e *Executor, branch string, dates ...string) {
	records := make([]map[string]any, len(dates))
	for i, d := range dates {
		records[i] = histRecord(projMain, branch, d, strconv.Itoa(i+1)+".0", strconv.Itoa(100*(i+1)))
	}
	histSeed(e, records)
}

// histUploadDates returns the analysis date stamped into each captured
// upload's metadata, in submission order.
func histUploadDates(t *testing.T, rec *histRecorder) []time.Time {
	t.Helper()
	uploads := rec.allUploads()
	out := make([]time.Time, len(uploads))
	for i, u := range uploads {
		out[i] = time.UnixMilli(histMetadata(t, u.report).GetAnalysisDate()).UTC()
	}
	return out
}

// histWantDates pins the dates got holds against RFC3339 strings, in order.
func histWantDates(t *testing.T, what string, got []time.Time, want ...string) {
	t.Helper()
	gotStr := make([]string, len(got))
	for i, d := range got {
		gotStr[i] = d.Format(time.RFC3339)
	}
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", what, gotStr, want)
	}
	for i := range want {
		if !got[i].Equal(mustTime(t, want[i])) {
			t.Errorf("%s: got %v, want %v (first difference at index %d)", what, gotStr, want, i)
			return
		}
	}
}

// histLogLine returns the first line of logged containing sub, or "" if none
// does.
func histLogLine(logged, sub string) string {
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}

// histWantDropLog pins the Info line dropUnreplayableHistory writes when it
// drops points — how many, out of how many extracted — and, when nothing was
// dropped, that the line is absent altogether.
func histWantDropLog(t *testing.T, logged string, dropped, of int) {
	t.Helper()
	line := histLogLine(logged, "history points not replayed")
	if dropped == 0 {
		if line != "" {
			t.Errorf("nothing was dropped, yet a drop line was logged: %s", line)
		}
		return
	}
	if line == "" {
		t.Fatalf("expected an Info line saying %d of %d points were dropped, got: %s", dropped, of, logged)
	}
	for _, want := range []string{"level=INFO", fmt.Sprintf("dropped=%d", dropped), fmt.Sprintf("of=%d", of)} {
		if !strings.Contains(line, want) {
			t.Errorf("drop line %q does not contain %q", line, want)
		}
	}
}

// --- the upper bound: the regular import's own date --------------------------

// TestMigrateBranchHistoryDropsPointsTheRegularImportCovers is the full-loop
// regression test for the extract-time race. The newest point in the extracted
// list carries exactly branch.LastAnalysisDate — the date the regular import
// is about to be stamped with — because the branch was re-analysed between
// extract reading its analysisDate and listing its analyses. Replaying that
// point would leave the regular import with an identical date, which the CE
// refuses, and the dummy placeholder it leaves as the branch's newest analysis
// would later make a re-run record the branch as up to date.
func TestMigrateBranchHistoryDropsPointsTheRegularImportCovers(t *testing.T) {
	const (
		d1 = "2022-01-15T00:00:00Z"
		d2 = "2022-06-01T00:00:00Z"
		d3 = "2023-06-01T00:00:00Z"
	)
	tests := []struct {
		name         string
		lastAnalysis string   // branch.LastAnalysisDate; "" = never analyzed
		wantReplayed []string // dates the target must receive, oldest first
	}{
		{
			name:         "newest point carries exactly the regular import's date",
			lastAnalysis: d3,
			wantReplayed: []string{d1, d2},
		},
		{
			name:         "branch re-analysed twice during extract: two points at or after the regular import",
			lastAnalysis: d2,
			wantReplayed: []string{d1},
		},
		{
			name:         "regular import one second after the newest point: nothing to drop",
			lastAnalysis: "2023-06-01T00:00:01Z",
			wantReplayed: []string{d1, d2, d3},
		},
		{
			name:         "never-analyzed branch is stamped now: no upper bound",
			lastAnalysis: "",
			wantReplayed: []string{d1, d2, d3},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tgt := newHistTarget(t, 0)
			histSeedBranch(tgt.e, branchDev, d1, d2, d3)
			branch := branchInfo{Name: branchDev, LastAnalysisDate: histOptTime(t, tc.lastAnalysis)}
			replayed := len(tc.wantReplayed)

			migrateBranchHistory(context.Background(), tgt.e, histBranchContext(), branch, branchDev)

			histWantReplayCounts(t, tgt.rec, replayed)
			dates := histUploadDates(t, tgt.rec)
			histWantDates(t, "submitted analysis dates", dates, tc.wantReplayed...)
			// Implied by the exact dates above; spelled out because it is the
			// property the Compute Engine enforces and this test exists for.
			histWantAllBefore(t, dates, branch.LastAnalysisDate)

			logged := tgt.logs.String()
			histWantDropLog(t, logged, 3-replayed, 3)
			histWantReplayLine(t, logged, replayed)
			if 3-replayed > 0 {
				// The date that decided it is in the line, and the target's
				// is not: this is a new project.
				histWantDropLineNames(t, logged, "source_last_analysis_date="+tc.lastAnalysis, "target_analysis_date")
			}
		})
	}
}

// histWantReplayCounts pins how many create-analysis handshakes and CE
// submissions the target saw: one of each per replayed point.
func histWantReplayCounts(t *testing.T, rec *histRecorder, replayed int) {
	t.Helper()
	if got := rec.count("/analysis/analyses"); got != replayed {
		t.Errorf("create-analysis handshakes = %d, want %d", got, replayed)
	}
	if got := rec.count("/api/ce/submit"); got != replayed {
		t.Errorf("submissions = %d, want %d", got, replayed)
	}
}

// histWantAllBefore fails for every date at or after limit; a zero limit means
// there is no upper bound.
func histWantAllBefore(t *testing.T, dates []time.Time, limit time.Time) {
	t.Helper()
	if limit.IsZero() {
		return
	}
	for _, d := range dates {
		if !d.Before(limit) {
			t.Errorf("submitted a point dated %s, at or after the regular import's %s",
				d.Format(time.RFC3339), limit.Format(time.RFC3339))
		}
	}
}

// histWantAllAfter fails for every date at or before floor.
func histWantAllAfter(t *testing.T, dates []time.Time, floor time.Time) {
	t.Helper()
	for _, d := range dates {
		if !d.After(floor) {
			t.Errorf("submitted a point dated %s, at or before the target's %s",
				d.Format(time.RFC3339), floor.Format(time.RFC3339))
		}
	}
}

// histWantReplayLine pins the points= field of the replay line: the points
// left after dropping.
func histWantReplayLine(t *testing.T, logged string, replayed int) {
	t.Helper()
	if want := "points=" + strconv.Itoa(replayed); !strings.Contains(logged, want) {
		t.Errorf("expected the replay line to report %s (the points left after dropping), got: %s", want, logged)
	}
}

// histWantDropLineNames pins that the drop line contains want and none of the
// unwanted fragments.
func histWantDropLineNames(t *testing.T, logged, want string, unwanted ...string) {
	t.Helper()
	line := histLogLine(logged, "history points not replayed")
	if !strings.Contains(line, want) {
		t.Errorf("drop line %q does not name %s", line, want)
	}
	for _, u := range unwanted {
		if strings.Contains(line, u) {
			t.Errorf("drop line %q contains %q, but the target holds nothing yet", line, u)
		}
	}
}

// --- the lower bound: what the target already holds -------------------------

// TestMigrateBranchHistoryReplaysOnlyPointsNewerThanTheTarget covers a resumed
// or re-run migration of a branch the target already holds: the CE refuses
// every point at or before the target's newest analysis, so those must not be
// submitted (the first one used to burn a handshake, an upload and a CE task
// and end the branch with a misleading "stopped early" warning), while the
// points between the target's date and the source's must still be replayed.
func TestMigrateBranchHistoryReplaysOnlyPointsNewerThanTheTarget(t *testing.T) {
	const (
		d1 = "2022-01-15T00:00:00Z"
		d2 = "2022-06-01T00:00:00Z"
		d3 = "2023-01-10T00:00:00Z"
		d4 = "2023-06-01T00:00:00Z"
	)
	tests := []struct {
		name         string
		target       string   // the target's newest analysis of the branch
		wantReplayed []string // dates the target must receive, oldest first
	}{
		{"target between the second and third point", "2022-09-01T00:00:00Z", []string{d3, d4}},
		{"target dated exactly at the second point: an equal date is refused too", d2, []string{d3, d4}},
		{"target one second before the third point", "2023-01-09T23:59:59Z", []string{d3, d4}},
		{"target dated exactly at the third point", d3, []string{d4}},
		{"target older than every point", "2021-01-01T00:00:00Z", []string{d1, d2, d3, d4}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tgt := newHistTarget(t, 0)
			histSeedBranch(tgt.e, branchDev, d1, d2, d3, d4)
			bctx := histBranchContext()
			bctx.TargetAnalysisDates = map[string]time.Time{
				branchDev: mustTime(t, tc.target),
				// Another branch's date must never leak into this one's window.
				"release-1.0": mustTime(t, "2030-01-01T00:00:00Z"),
			}

			migrateBranchHistory(context.Background(), tgt.e, bctx, branchInfo{Name: branchDev}, branchDev)

			replayed := len(tc.wantReplayed)
			histWantReplayCounts(t, tgt.rec, replayed)
			dates := histUploadDates(t, tgt.rec)
			histWantDates(t, "submitted analysis dates", dates, tc.wantReplayed...)
			// Implied by the exact dates above; spelled out because it is the
			// property the Compute Engine enforces and this test exists for.
			histWantAllAfter(t, dates, mustTime(t, tc.target))

			logged := tgt.logs.String()
			histWantDropLog(t, logged, 4-replayed, 4)
			if strings.Contains(logged, "stopped early") {
				t.Errorf("a resumed branch must not report stopping early, got: %s", logged)
			}
			if 4-replayed > 0 {
				histWantDropLineNames(t, logged, "target_analysis_date="+tc.target)
			}
		})
	}
}

// TestMigrateBranchHistoryMakesNoCallsWhenTargetIsPastEveryPoint pins the
// no-op case of the same resume: when every extracted point is at or before
// what the target already holds there is nothing to replay, and the function
// must not touch the network at all — not even the quality-profile lookup that
// otherwise precedes the first point.
func TestMigrateBranchHistoryMakesNoCallsWhenTargetIsPastEveryPoint(t *testing.T) {
	const (
		d1 = "2022-01-15T00:00:00Z"
		d2 = "2022-06-01T00:00:00Z"
		d3 = "2023-06-01T00:00:00Z"
	)
	tests := []struct{ name, target string }{
		{"target dated exactly at the newest point", d3},
		{"target newer than every point", "2024-01-01T00:00:00Z"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tgt := newHistTarget(t, 0)
			histSeedBranch(tgt.e, branchDev, d1, d2, d3)
			bctx := histBranchContext()
			bctx.TargetAnalysisDates = map[string]time.Time{branchDev: mustTime(t, tc.target)}

			migrateBranchHistory(context.Background(), tgt.e, bctx, branchInfo{Name: branchDev}, branchDev)

			if n := tgt.rec.total(); n != 0 {
				t.Errorf("expected no request at all (no profile lookup, handshake or submit), got %d: %v", n, tgt.rec.paths())
			}
			logged := tgt.logs.String()
			histWantDropLog(t, logged, 3, 3)
			for _, unwanted := range []string{"migrating project history", "stopped early", "no usable quality profile"} {
				if strings.Contains(logged, unwanted) {
					t.Errorf("expected a silent no-op after the drop line, but logged %q: %s", unwanted, logged)
				}
			}
		})
	}
}

// TestMigrateBranchHistoryLooksUpTheTargetDateByTargetBranchName pins which key
// the lower bound is read under. The target's branch dates are keyed by the
// SonarCloud branch name; for the main branch that is the target's own main
// name (the source's name only after #428's rename), not the source branch's
// name that the extracted history is filed under.
func TestMigrateBranchHistoryLooksUpTheTargetDateByTargetBranchName(t *testing.T) {
	const (
		d1 = "2022-01-15T00:00:00Z"
		d2 = "2022-06-01T00:00:00Z"
		d3 = "2023-06-01T00:00:00Z"
	)
	tgt := newHistTarget(t, 0)
	histSeedBranch(tgt.e, branchMain, d1, d2, d3) // history is filed under the SOURCE name
	bctx := histBranchContext()
	bctx.TargetAnalysisDates = map[string]time.Time{
		"master": mustTime(t, d1), // what the target holds for this very branch
		// A decoy under the source name. Looked up by mistake it would swallow
		// every point.
		branchMain: mustTime(t, d3),
	}

	migrateBranchHistory(context.Background(), tgt.e, bctx, branchInfo{Name: branchMain, IsMain: true}, "master")

	histWantDates(t, "submitted analysis dates", histUploadDates(t, tgt.rec), d2, d3)
	if n := tgt.rec.count("/analysis/analyses"); n != 0 {
		t.Errorf("the main branch needs no create-analysis handshake, got %d", n)
	}
	for i, u := range tgt.rec.allUploads() {
		if got := histMetadata(t, u.report).GetBranchName(); got != "master" {
			t.Errorf("upload %d branchName = %q, want the target's main branch %q", i+1, got, "master")
		}
	}
}

// --- ETA accounting ----------------------------------------------------------

// TestMigrateBranchHistoryCountsDroppedPointsTowardProgress covers the other
// half of the guard: projectHistoryPointTotal pre-counts every extracted point
// before any date is compared, so a dropped point — never attempted — has to
// be counted as done here, or the live percentage would stall short of 100%
// for the rest of the run.
func TestMigrateBranchHistoryCountsDroppedPointsTowardProgress(t *testing.T) {
	const (
		d1 = "2022-01-15T00:00:00Z"
		d2 = "2022-06-01T00:00:00Z"
		d3 = "2023-01-10T00:00:00Z"
		d4 = "2023-06-01T00:00:00Z"
	)

	t.Run("dropped and replayed points together complete the branch's share", func(t *testing.T) {
		tgt := newHistTarget(t, 0)
		tgt.e.HistoryProgress = common.NewProgressLogger(tgt.e.Logger, "migrateProjectHistory", 4)
		histSeedBranch(tgt.e, branchDev, d1, d2, d3, d4)
		// d3 and d4 sit at or after the regular import: 2 dropped, 2 replayed.
		branch := branchInfo{Name: branchDev, LastAnalysisDate: mustTime(t, d3)}

		migrateBranchHistory(context.Background(), tgt.e, histBranchContext(), branch, branchDev)

		if got := tgt.rec.count("/api/ce/submit"); got != 2 {
			t.Fatalf("submissions = %d, want 2", got)
		}
		if got := tgt.e.HistoryProgress.Fraction(); got != 1 {
			t.Errorf("HistoryProgress.Fraction() = %v, want 1 (2 replayed + 2 dropped of 4)", got)
		}
	})

	t.Run("a branch with every point dropped still completes its share", func(t *testing.T) {
		tgt := newHistTarget(t, 0)
		tgt.e.HistoryProgress = common.NewProgressLogger(tgt.e.Logger, "migrateProjectHistory", 3)
		histSeedBranch(tgt.e, branchDev, d1, d2, d3)
		bctx := histBranchContext()
		bctx.TargetAnalysisDates = map[string]time.Time{branchDev: mustTime(t, d4)}

		migrateBranchHistory(context.Background(), tgt.e, bctx, branchInfo{Name: branchDev}, branchDev)

		if n := tgt.rec.total(); n != 0 {
			t.Errorf("expected no request at all, got %d: %v", n, tgt.rec.paths())
		}
		if got := tgt.e.HistoryProgress.Fraction(); got != 1 {
			t.Errorf("HistoryProgress.Fraction() = %v, want 1 (all 3 points dropped, none left to count later)", got)
		}
	})

	t.Run("dropped points stay counted when the replay then stops early", func(t *testing.T) {
		tgt := newHistTarget(t, 1) // the first create-analysis call fails
		tgt.e.HistoryProgress = common.NewProgressLogger(tgt.e.Logger, "migrateProjectHistory", 4)
		histSeedBranch(tgt.e, branchDev, d1, d2, d3, d4)
		bctx := histBranchContext()
		bctx.TargetAnalysisDates = map[string]time.Time{branchDev: mustTime(t, d2)} // d1 and d2 dropped

		migrateBranchHistory(context.Background(), tgt.e, bctx, branchInfo{Name: branchDev}, branchDev)

		// 2 dropped + the 1 attempted point that failed; d4 was never tried.
		if got := tgt.e.HistoryProgress.Fraction(); got != 0.75 {
			t.Errorf("HistoryProgress.Fraction() = %v, want 0.75 (2 dropped + 1 attempted of 4)", got)
		}
	})

	t.Run("nil HistoryProgress is a no-op, not a panic", func(t *testing.T) {
		tgt := newHistTarget(t, 0)
		// tgt.e.HistoryProgress deliberately left nil.
		histSeedBranch(tgt.e, branchDev, d1, d2, d3)
		branch := branchInfo{Name: branchDev, LastAnalysisDate: mustTime(t, d3)}

		migrateBranchHistory(context.Background(), tgt.e, histBranchContext(), branch, branchDev) // must not panic

		if got := tgt.rec.count("/api/ce/submit"); got != 2 {
			t.Errorf("submissions = %d, want 2", got)
		}
	})
}

// --- the per-point replay of a non-main branch (#625) ---------------------------

// TestMigrateBranchHistoryHandshakeRequestBody pins what the tool asks the
// create-analysis endpoint for, once per point: the target project and
// organization, the SNAPSHOT's version (not the branch's current one), the
// branch itself, the main branch as its reference (never the branch itself —
// a self-reference is what made the CE abort the first analysis of a branch),
// and a long-lived branch type.
func TestMigrateBranchHistoryHandshakeRequestBody(t *testing.T) {
	tgt := newHistTarget(t, 0)
	histSeedBranch(tgt.e, branchDev, "2022-01-15T00:00:00Z", "2022-06-01T00:00:00Z")
	bctx := histBranchContext()
	if bctx.MainTargetName == "" || bctx.MainTargetName == branchDev {
		t.Fatalf("fixture needs a main branch name distinct from %q, got %q", branchDev, bctx.MainTargetName)
	}

	migrateBranchHistory(context.Background(), tgt.e, bctx, branchInfo{Name: branchDev}, branchDev)

	bodies := tgt.hs.requestBodies()
	if len(bodies) != 2 {
		t.Fatalf("create-analysis handshakes = %d, want 2", len(bodies))
	}
	for i, version := range []string{"1.0", "2.0"} {
		want := map[string]string{
			"organizationKey":  testCloudOrg,
			"projectKey":       histCloudKey,
			"projectVersion":   version,
			"branchName":       branchDev,
			"targetBranchName": bctx.MainTargetName,
			"branchType":       "long",
		}
		if !reflect.DeepEqual(bodies[i], want) {
			t.Errorf("handshake %d request body = %v, want %v", i+1, bodies[i], want)
		}
	}
}

// TestMigrateBranchHistoryEachReportCarriesItsOwnHandshakeID pins that every
// historical report is bound to the analysis ITS handshake created: a report
// stamped with another point's id (the first one, say, reused for all) is
// attached to the wrong analysis row, or refused outright.
func TestMigrateBranchHistoryEachReportCarriesItsOwnHandshakeID(t *testing.T) {
	tgt := newHistTarget(t, 0)
	histSeedBranch(tgt.e, branchDev, "2022-01-15T00:00:00Z", "2022-06-01T00:00:00Z", "2023-06-01T00:00:00Z")

	migrateBranchHistory(context.Background(), tgt.e, histBranchContext(), branchInfo{Name: branchDev}, branchDev)

	ids := tgt.hs.issuedIDs()
	uploads := tgt.rec.allUploads()
	if len(ids) != 3 || len(uploads) != 3 {
		t.Fatalf("handshakes = %d, uploads = %d, want 3 of each", len(ids), len(uploads))
	}
	// The fake always issues distinct ids, so this cannot fail on its own: it
	// keeps the per-report comparison below honest (an id shared between two
	// points would make that comparison pass for the wrong reason).
	seen := map[string]bool{}
	for i, id := range ids {
		if id == "" || seen[id] {
			t.Errorf("handshake %d issued id %q, want an id no other handshake issued (all: %v)", i+1, id, ids)
		}
		seen[id] = true
	}
	for i, u := range uploads {
		if got := histMetadata(t, u.report).GetAnalysisUuid(); got != ids[i] {
			t.Errorf("report %d analysisUuid = %q, want %q (the id returned by its own handshake)", i+1, got, ids[i])
		}
	}
}

// TestMigrateBranchHistoryRunsEachPointToCompletionBeforeTheNext pins the
// order of the calls within one branch: the profile lookup once, up front,
// then for each point the handshake, the upload and the CE poll — all three
// finished before the next point's handshake begins — with strictly ascending
// analysis dates, because the CE refuses an out-of-order report.
func TestMigrateBranchHistoryRunsEachPointToCompletionBeforeTheNext(t *testing.T) {
	tgt := newHistTarget(t, 0)
	// Written newest-first on purpose: the loader has to put them in order.
	histSeedBranch(tgt.e, branchDev, "2023-06-01T00:00:00Z", "2022-06-01T00:00:00Z", "2022-01-15T00:00:00Z")

	migrateBranchHistory(context.Background(), tgt.e, histBranchContext(), branchInfo{Name: branchDev}, branchDev)

	want := []string{"/api/qualityprofiles/search"}
	for range 3 {
		want = append(want, "/analysis/analyses", "/api/ce/submit", "/api/ce/task")
	}
	if got := tgt.rec.sequence(); !slices.Equal(got, want) {
		t.Errorf("request sequence =\n  %v\nwant\n  %v", got, want)
	}
	histWantDates(t, "submitted analysis dates", histUploadDates(t, tgt.rec),
		"2022-01-15T00:00:00Z", "2022-06-01T00:00:00Z", "2023-06-01T00:00:00Z")
}

// TestMigrateBranchHistoryHandshakeFailureStopsTheBranch pins the best-effort
// contract for a rejected create-analysis call at point k of 3: nothing is
// uploaded for that point or any later one, no further handshake is tried, a
// WARN names the point that failed, and migrateBranchHistory returns normally
// so the regular import that follows it still runs.
func TestMigrateBranchHistoryHandshakeFailureStopsTheBranch(t *testing.T) {
	dates := []string{"2022-01-15T00:00:00Z", "2022-06-01T00:00:00Z", "2023-06-01T00:00:00Z"}

	for k := 1; k <= len(dates); k++ {
		t.Run("handshake "+strconv.Itoa(k)+" of 3 fails", func(t *testing.T) {
			tgt := newHistTarget(t, k)
			histSeedBranch(tgt.e, branchDev, dates...)

			migrateBranchHistory(context.Background(), tgt.e, histBranchContext(), branchInfo{Name: branchDev}, branchDev)

			histWantStoppedAt(t, tgt, dates, k)
		})
	}
}

// histWantStoppedAt pins what the target saw and logged when the handshake of
// the k-th (1-based) point was rejected.
func histWantStoppedAt(t *testing.T, tgt *histTarget, dates []string, k int) {
	t.Helper()
	if got := tgt.rec.count("/analysis/analyses"); got != k {
		t.Errorf("create-analysis handshakes = %d, want %d (none after the failing one)", got, k)
	}
	if got := tgt.rec.count("/api/ce/submit"); got != k-1 {
		t.Errorf("submissions = %d, want %d (none for the failed point or later ones)", got, k-1)
	}
	if got := tgt.rec.count("/api/ce/task"); got != k-1 {
		t.Errorf("CE polls = %d, want %d", got, k-1)
	}
	histWantDates(t, "submitted analysis dates", histUploadDates(t, tgt.rec), dates[:k-1]...)

	line := histLogLine(tgt.logs.String(), "stopped early")
	if line == "" {
		t.Fatalf("expected a 'stopped early' warning, got: %s", tgt.logs.String())
	}
	for _, want := range []string{
		"level=WARN",
		"point=" + strconv.Itoa(k),
		"of=3",
		dates[k-1],
		"create-analysis handshake",
		"HTTP 500",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("warning %q does not contain %q", line, want)
		}
	}
}

// --- end to end: the regular import that follows the replay ------------------------

// TestImportProjectBranchesHistoryKeepsUploadsStrictlyAscending drives the whole
// per-branch path — the target's branch list, then migrateBranchHistory, then
// the regular current-snapshot import — for a non-main branch, and pins the
// property the Compute Engine enforces and this guard exists to protect:
// every report uploaded for the branch is dated strictly after the one before
// it (and after whatever the target already held), with the regular import,
// dated exactly branch.LastAnalysisDate, last. Both scenarios would break it
// without the guard: the replay would end on the regular import's own date,
// or open with a point the target already holds.
func TestImportProjectBranchesHistoryKeepsUploadsStrictlyAscending(t *testing.T) {
	const (
		d1 = "2022-01-15T00:00:00Z"
		d2 = "2022-06-01T00:00:00Z"
		d3 = "2023-06-01T00:00:00Z"
		d4 = "2023-09-01T00:00:00Z"
	)
	tests := []histStrictlyAscendingCase{
		{
			name:         "extract-time race: the newest history point is the regular import's own date",
			points:       []string{d1, d2, d3},
			lastAnalysis: d3,
			want:         []string{d1, d2, d3},
		},
		{
			name:         "re-run: the target already holds the first two points",
			heldByTarget: d2,
			points:       []string{d1, d2, d3},
			lastAnalysis: d4,
			want:         []string{d3, d4},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runStrictlyAscendingCase(t, tc) })
	}
}

type histStrictlyAscendingCase struct {
	name         string
	heldByTarget string   // the target's newest analysis of the branch ("" = new branch)
	points       []string // the extracted history list
	lastAnalysis string   // the source's last analysis, which the regular import is dated
	want         []string // every upload, in order: replayed points, then the regular import
}

func runStrictlyAscendingCase(t *testing.T, tc histStrictlyAscendingCase) {
	tgt := newHistTarget(t, 0)
	e := tgt.e
	histSeedBranch(e, branchDev, tc.points...)
	// What the regular import is built from.
	writeJSONL(filepath.Join(e.ExportDir, extractRun, "getProjectComponentTree"), []map[string]any{{
		"key": projMain + ":src/app.js", "name": "app.js", "path": "src/app.js",
		"language": "js", "lines": 10,
		"projectKey": projMain, "branch": branchDev, "serverUrl": testServerURL,
	}})
	writeJSONL(filepath.Join(e.ExportDir, extractRun, "getProjectSourceCode"), []map[string]any{{
		"key": projMain + ":src/app.js", "source": "console.log(1)\n",
		"projectKey": projMain, "branch": branchDev, "serverUrl": testServerURL,
	}})

	scBranches := []types.Branch{{Name: branchMain, IsMain: true}, {Name: branchDev}}
	if tc.heldByTarget != "" {
		scBranches[1].AnalysisDate = mustTime(t, tc.heldByTarget).Format("2006-01-02T15:04:05-0700")
	}
	w, err := e.Store.Writer("importProjectData")
	if err != nil {
		t.Fatalf("opening the importProjectData writer: %v", err)
	}
	branches := []branchInfo{{Name: branchDev, LastAnalysisDate: mustTime(t, tc.lastAnalysis)}}

	if err := importProjectBranches(context.Background(), e, upToDateTestProject(t), branches, scBranches, nil, w); err != nil {
		t.Fatalf("importProjectBranches: %v", err)
	}

	dates := histUploadDates(t, tgt.rec)
	histWantDates(t, "uploaded analysis dates", dates, tc.want...)
	histWantStrictlyAscending(t, dates)
	if tc.heldByTarget != "" && !dates[0].After(mustTime(t, tc.heldByTarget)) {
		t.Errorf("first upload is dated %s, at or before what the target already holds (%s)",
			dates[0].Format(time.RFC3339), tc.heldByTarget)
	}
	histWantFileComponents(t, tgt.rec)

	items, _ := e.Store.ReadAll("importProjectData")
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 branch record, got %d", len(items))
	}
	if got := extractField(items[0], "status"); got != "success" {
		t.Errorf("branch status = %q, want %q", got, "success")
	}
}

func histWantStrictlyAscending(t *testing.T, dates []time.Time) {
	t.Helper()
	for i := 1; i < len(dates); i++ {
		if !dates[i].After(dates[i-1]) {
			t.Errorf("upload %d is dated %s, not after upload %d (%s)",
				i+1, dates[i].Format(time.RFC3339), i, dates[i-1].Format(time.RFC3339))
		}
	}
}

// histWantFileComponents pins that the regular import is the last upload, and
// a real report: the replayed points hang off the placeholder file, it hangs
// off the branch's own.
func histWantFileComponents(t *testing.T, rec *histRecorder) {
	t.Helper()
	uploads := rec.allUploads()
	for i, u := range uploads {
		want := histFileName
		if i == len(uploads)-1 {
			want = "src/app.js"
		}
		if got := histFileComponent(t, u.report).GetProjectRelativePath(); got != want {
			t.Errorf("upload %d carries file %q, want %q", i+1, got, want)
		}
	}
}
