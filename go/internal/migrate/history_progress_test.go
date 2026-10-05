// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// TestProjectHistoryPointTotal covers #564's history-migration ETA
// integration: the upfront point count RunMigrate uses to give
// migrateProjectHistory its own tracked unit of work must sum every
// extracted project's eligible branches' history points (every branch,
// not just main — #625), purely from already-extracted data
// (getProjects/getBranches/getProjectAnalysisHistory) — no API calls,
// safe to call before a single migrate task has run.
func TestProjectHistoryPointTotal(t *testing.T) {
	dir := t.TempDir()
	writeExtractMetaJSON(t, dir, extractRun, testServerURL)
	writeJSONL(filepath.Join(dir, extractRun, "getProjects"), []map[string]any{
		{"key": projMain},
		{"key": "proj2"},
	})
	writeJSONL(filepath.Join(dir, extractRun, "getBranches"), []map[string]any{
		{"projectKey": projMain, "name": "main", "isMain": true, "type": "BRANCH"},
		{"projectKey": "proj2", "name": "main", "isMain": true, "type": "BRANCH"},
		// A non-main branch's history must be counted too (#625) — it
		// gets replayed exactly like main's.
		{"projectKey": "proj2", "name": "feature", "isMain": false, "type": "BRANCH"},
	})
	writeJSONL(filepath.Join(dir, extractRun, "getProjectAnalysisHistory"), []map[string]any{
		histRecord(projMain, "main", "2025-01-01T00:00:00+0000", "1.0", "100"),
		histRecord(projMain, "main", "2025-02-01T00:00:00+0000", "1.1", "110"),
		histRecord("proj2", "main", "2025-01-01T00:00:00+0000", "2.0", "50"),
		histRecord("proj2", "feature", "2025-01-15T00:00:00+0000", "2.0-dev", "55"),
	})

	cloudSrv := newMockCloudServer()
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	e.MigrateHistory = false
	if got := projectHistoryPointTotal(e); got != 0 {
		t.Errorf("with MigrateHistory off: got %d, want 0", got)
	}

	e.MigrateHistory = true
	if got := projectHistoryPointTotal(e); got != 4 {
		t.Errorf("total = %d, want 4 (2 for %s/main + 1 for proj2/main + 1 for proj2/feature)", got, projMain)
	}
}

// TestProjectHistoryPointTotalNoHistoryExtracted: when extract never ran
// with --migrate_history, getProjectAnalysisHistory has no records at
// all — the total must be 0, keeping the feature a true no-op.
func TestProjectHistoryPointTotalNoHistoryExtracted(t *testing.T) {
	dir := t.TempDir()
	writeExtractMetaJSON(t, dir, extractRun, testServerURL)
	writeJSONL(filepath.Join(dir, extractRun, "getProjects"), []map[string]any{
		{"key": projMain},
	})
	writeJSONL(filepath.Join(dir, extractRun, "getBranches"), []map[string]any{
		{"projectKey": projMain, "name": "main", "isMain": true, "type": "BRANCH"},
	})

	cloudSrv := newMockCloudServer()
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	e.MigrateHistory = true

	if got := projectHistoryPointTotal(e); got != 0 {
		t.Errorf("total = %d, want 0 (no getProjectAnalysisHistory extract data)", got)
	}
}

// --- the single-pass pre-count (#625) --------------------------------------
//
// projectHistoryPointTotal used to call loadExtractedAnalysisHistory once per
// eligible (project, branch), and every call streamed the whole
// getProjectAnalysisHistory extract: O(pairs x corpus). It now reads that
// extract once (countReplayableHistoryPoints). Everything below pins that this
// is the same number, reached the cheap way.

const (
	histTaskName       = "getProjectAnalysisHistory"
	histOtherServerURL = "https://sq.other.test/"
	histOtherRun       = "extract-02"

	// histCountFixtureTotal is what projectHistoryPointTotal must return for
	// newHistoryCountExecutor's corpus with every branch filter on; the
	// table in that function's doc comment is where it comes from.
	histCountFixtureTotal = 34
)

// histCountDated returns n replayable history records for one project+branch,
// alternating the two spellings of a date SonarQube emits (RFC3339 and the
// legacy "+0000" offset) so both parse paths are exercised.
func histCountDated(project, branch string, n int) []map[string]any {
	base := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	recs := make([]map[string]any, 0, n)
	for i := range n {
		layout := time.RFC3339
		if i%2 == 1 {
			layout = "2006-01-02T15:04:05-0700"
		}
		recs = append(recs, histRecord(project, branch, base.AddDate(0, 0, i).Format(layout), "1."+strconv.Itoa(i), "100"))
	}
	return recs
}

// histCountUndated returns one record per way a history point can fail to
// carry a usable date. loadExtractedAnalysisHistory drops every one of them,
// so none of them may be counted.
func histCountUndated(project, branch string) []map[string]any {
	var recs []map[string]any
	for _, date := range []any{
		"",                     // empty
		"yesterday",            // unparseable
		"0001-01-01T00:00:00Z", // parses, but to the zero time
		20250101,               // not a string
		nil,                    // JSON null
	} {
		rec := histRecord(project, branch, "", "1.0", "100")
		rec["date"] = date
		recs = append(recs, rec)
	}
	missing := histRecord(project, branch, "", "1.0", "100")
	delete(missing, "date")
	return append(recs, missing)
}

// histTotalPerBranch is the pre-count as it was before the single pass: one
// loadExtractedAnalysisHistory, a full pass over the history extract, for
// every eligible branch. It is the oracle the single-pass total is compared
// with, so it deliberately does not go through countReplayableHistoryPoints.
func histTotalPerBranch(e *Executor) int {
	projects, err := readExtractItems(e, "getProjects")
	if err != nil {
		return 0
	}
	total := 0
	for _, p := range projects {
		key := extractField(p.Data, "key")
		if key == "" {
			continue
		}
		for _, b := range historyEligibleBranches(e, p.ServerURL, key) {
			total += len(loadExtractedAnalysisHistory(e, p.ServerURL, key, b.Name))
		}
	}
	return total
}

// newHistoryCountExecutor builds the corpus the pre-count tests share: two
// source servers with one project key on both, a filter for each of
// --exclude_branches, --branch_regexp and --branch_analyzed_after that drops a
// branch still holding replayable history, the per-project branch cap, a
// project with no getBranches rows, and records that must never be counted
// (unusable dates, a project nobody listed, branches that do not exist,
// another server's records, lines that are not records at all) or that only
// the loader's own way of reading a date counts (see histOddDateLines).
//
// The branch filters start ON. The replayable history projectHistoryPointTotal
// must sum, by scope:
//
//	server A  proj1/main 3, +2 hand-edited records the loader reads,
//	            proj1/release/1.0 2, proj1/develop 1                   =  8
//	          proj2/main 2, +1 from a second chunk file                =  3
//	          wide/main 2, wide/release/03 .. release/11 1 each        = 11
//	          bare/main 2 (no getBranches rows: falls back to main)    =  2
//	server B  proj1/main 5, proj1/develop 4                            =  9
//	          projB1/main 1                                            =  1
//	                                                            total  = 34
//
// Holding history but dropped by a filter: proj1/feature/x 4
// (--exclude_branches), proj1/hotfix-1 5 (--branch_regexp), proj1/stale 6
// (--branch_analyzed_after), proj1/pr-12 7 (a SHORT branch), and wide's
// release/01 and release/02, 5 each (MaxBranchesPerProject keeps main and the
// nine most recently analyzed of its eleven release branches).
func newHistoryCountExecutor(t *testing.T) *Executor {
	t.Helper()
	dir := t.TempDir()
	e := newProjectDataExecutor(t, dir)
	e.MigrateHistory = true
	e.Mapping = structure.ExtractMapping{testServerURL: extractRun, histOtherServerURL: histOtherRun}
	e.ExcludeBranches = []string{"feature/*"}
	e.BranchRe = regexp.MustCompile(`^(release|develop|stale)`)
	cutoff := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	e.BranchAnalyzedAfter = &cutoff

	const recent = "2025-06-01T00:00:00+0000"
	branch := func(project, name string, isMain bool, branchType, analyzed string) map[string]any {
		return map[string]any{
			"projectKey": project, "name": name, "isMain": isMain, "type": branchType, "analysisDate": analyzed,
		}
	}
	taskDir := func(run, task string) string { return filepath.Join(dir, run, task) }
	type scopeCount struct {
		project, branch string
		n               int
	}
	datedHistory := func(scopes []scopeCount) []map[string]any {
		var recs []map[string]any
		for _, s := range scopes {
			recs = append(recs, histCountDated(s.project, s.branch, s.n)...)
		}
		return recs
	}

	// Server A.
	writeJSONL(taskDir(extractRun, "getProjects"), []map[string]any{
		{"key": projMain}, {"key": "proj2"}, {"key": "wide"}, {"key": "bare"},
		{"key": ""}, // no key: skipped, however much history is filed under an empty projectKey
	})
	branchesA := []map[string]any{
		branch(projMain, "main", true, "BRANCH", recent),
		branch(projMain, "release/1.0", false, "BRANCH", recent),
		branch(projMain, "develop", false, "BRANCH", recent),
		branch(projMain, "feature/x", false, "BRANCH", recent),                 // --exclude_branches
		branch(projMain, "hotfix-1", false, "BRANCH", recent),                  // --branch_regexp
		branch(projMain, "stale", false, "BRANCH", "2019-01-01T00:00:00+0000"), // --branch_analyzed_after
		branch(projMain, "pr-12", false, "SHORT", recent),                      // never migrated
		branch("proj2", "main", true, "BRANCH", recent),
		branch("wide", "main", true, "BRANCH", recent),
	}
	// wide has main plus eleven release branches: two over MaxBranchesPerProject.
	for i := 1; i <= 11; i++ {
		analyzed := fmt.Sprintf("2025-05-%02dT00:00:00+0000", i)
		branchesA = append(branchesA, branch("wide", fmt.Sprintf("release/%02d", i), false, "BRANCH", analyzed))
	}
	writeJSONL(taskDir(extractRun, "getBranches"), branchesA)

	histA := datedHistory([]scopeCount{
		{projMain, "main", 3}, {projMain, "release/1.0", 2}, {projMain, "develop", 1},
		{projMain, "feature/x", 4}, {projMain, "hotfix-1", 5}, {projMain, "stale", 6}, {projMain, "pr-12", 7},
		// A branch getBranches never listed, and a branch proj2 does not have.
		{projMain, "orphan", 8}, {"proj2", "release/1.0", 3},
		{"proj2", "main", 2}, {"wide", "main", 2},
		{"bare", "main", 2}, {"bare", "other", 4},
		// Filed under no project at all, under a project getProjects does not
		// list, and under a project only server B has.
		{"", "main", 3}, {"gone", "main", 6}, {"projB1", "main", 7},
	})
	for i := 1; i <= 11; i++ {
		n := 1
		if i <= 2 {
			n = 5 // dropped by the branch cap
		}
		histA = append(histA, histCountDated("wide", fmt.Sprintf("release/%02d", i), n)...)
	}
	for _, s := range [][2]string{{projMain, "main"}, {projMain, "release/1.0"}, {projMain, "develop"}, {"bare", "main"}} {
		histA = append(histA, histCountUndated(s[0], s[1])...)
	}
	writeJSONL(taskDir(extractRun, histTaskName), histA)

	// A second chunk file: one replayable point for proj2/main, the hand-edited
	// records (two the loader counts, three it does not), then every kind of
	// line that is not a record. None of those may stop the pass.
	oddCounted, oddDropped := histOddDateLines()
	hostile := strings.Join(slices.Concat(
		[]string{`{"projectKey":"proj2","branch":"main","date":"2025-09-09T00:00:00Z","measures":[]}`},
		oddCounted, oddDropped, histNonRecordLines(),
	), "\n") + "\n"
	if err := os.WriteFile(filepath.Join(taskDir(extractRun, histTaskName), "results.2.jsonl"), []byte(hostile), 0o644); err != nil {
		t.Fatal(err)
	}

	// Server B.
	writeJSONL(taskDir(histOtherRun, "getProjects"), []map[string]any{{"key": projMain}, {"key": "projB1"}})
	writeJSONL(taskDir(histOtherRun, "getBranches"), []map[string]any{
		branch(projMain, "main", true, "BRANCH", recent),
		branch(projMain, "develop", false, "BRANCH", recent),
		branch("projB1", "main", true, "BRANCH", recent),
	})
	// histRecord stamps every record with serverUrl = testServerURL, these
	// included. Which server a record belongs to is the extract it lives in,
	// not that field, and a count that trusted the field would move all of
	// these onto server A.
	histB := datedHistory([]scopeCount{
		{projMain, "main", 5}, {projMain, "develop", 4}, {"projB1", "main", 1},
		{"projB1", "release/1.0", 3}, // projB1 has no such branch
		{"wide", "main", 9},          // server B never listed this project
		{"proj2", "main", 9},         // nor this one
	})
	histB = append(histB, histCountUndated("projB1", "main")...)
	writeJSONL(taskDir(histOtherRun, histTaskName), histB)
	return e
}

// TestProjectHistoryPointTotalMatchesPerBranchLoader is the equivalence
// guarantee behind the single pass: for every combination of the three branch
// filters, projectHistoryPointTotal equals the sum of
// len(loadExtractedAnalysisHistory) over the same historyEligibleBranches, the
// way it was computed before #625 made that too slow.
func TestProjectHistoryPointTotalMatchesPerBranchLoader(t *testing.T) {
	e := newHistoryCountExecutor(t)
	cutoff := *e.BranchAnalyzedAfter
	filters := []string{"exclude_branches", "branch_regexp", "branch_analyzed_after"}

	totals := make([]int, 1<<len(filters))
	for mask := range totals {
		e.ExcludeBranches, e.BranchRe, e.BranchAnalyzedAfter = nil, nil, nil
		var on []string
		if mask&1 != 0 {
			e.ExcludeBranches = []string{"feature/*"}
			on = append(on, filters[0])
		}
		if mask&2 != 0 {
			e.BranchRe = regexp.MustCompile(`^(release|develop|stale)`)
			on = append(on, filters[1])
		}
		if mask&4 != 0 {
			e.BranchAnalyzedAfter = &cutoff
			on = append(on, filters[2])
		}
		name := strings.Join(on, "+")
		if name == "" {
			name = "no branch filters"
		}
		t.Run(name, func(t *testing.T) {
			want := histTotalPerBranch(e)
			if got := projectHistoryPointTotal(e); got != want {
				t.Errorf("projectHistoryPointTotal = %d, per-branch loader total = %d", got, want)
			}
			totals[mask] = want
		})
	}

	// The fixture has to bite: each filter on its own must remove history, or
	// the table above says nothing about that filter. And the hand count pins
	// the branch cap too (without it the all-filters total would be 42).
	for i, name := range filters {
		if totals[1<<i] >= totals[0] {
			t.Errorf("%s alone left the total at %d (unfiltered %d): the fixture does not exercise it",
				name, totals[1<<i], totals[0])
		}
	}
	if got := totals[len(totals)-1]; got != histCountFixtureTotal {
		t.Errorf("all filters on: total = %d, want %d (hand-counted from newHistoryCountExecutor's table)",
			got, histCountFixtureTotal)
	}
}

// TestHistoryCountPointDateRule pins the one rule loadExtractedAnalysisHistory
// and countReplayableHistoryPoints share: a history point is replayable exactly
// when its date parses to a non-zero time.
func TestHistoryCountPointDateRule(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"RFC3339", "2025-03-01T10:00:00Z", true},
		{"RFC3339 with an offset", "2025-03-01T10:00:00+02:00", true},
		{"legacy offset without a colon", "2025-03-01T10:00:00+0000", true},
		{"empty", "", false},
		{"unparseable", "yesterday", false},
		{"a date with no time", "2025-03-01", false},
		{"the zero time", "0001-01-01T00:00:00Z", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			date, ok := historyPointDate(tc.raw)
			if ok != tc.want {
				t.Errorf("historyPointDate(%q) ok = %v, want %v", tc.raw, ok, tc.want)
			}
			if ok == date.IsZero() {
				t.Errorf("historyPointDate(%q) = (%v, %v): ok must mean a non-zero date", tc.raw, date, ok)
			}
		})
	}
}

// TestHistoryCountKeysByServerProjectBranch: the count is keyed by the exact
// (server, project, branch), so the same project and branch on two servers are
// two entries, and every entry is what the per-branch loader the replay uses
// returns for that scope.
func TestHistoryCountKeysByServerProjectBranch(t *testing.T) {
	dir := t.TempDir()
	e := newProjectDataExecutor(t, dir)
	e.Mapping = structure.ExtractMapping{testServerURL: extractRun, histOtherServerURL: histOtherRun}
	writeJSONL(filepath.Join(dir, extractRun, histTaskName), slices.Concat(
		histCountDated(projMain, branchMain, 2),
		histCountDated(projMain, branchDev, 1),
		histCountDated("proj2", branchMain, 1),
	))
	// Same project and branch as the first scope, on the other server.
	writeJSONL(filepath.Join(dir, histOtherRun, histTaskName), histCountDated(projMain, branchMain, 3))

	want := map[extractScope]int{
		{ServerURL: testServerURL, ProjectKey: projMain, Branch: branchMain}:      2,
		{ServerURL: testServerURL, ProjectKey: projMain, Branch: branchDev}:       1,
		{ServerURL: testServerURL, ProjectKey: "proj2", Branch: branchMain}:       1,
		{ServerURL: histOtherServerURL, ProjectKey: projMain, Branch: branchMain}: 3,
	}
	got := countReplayableHistoryPoints(e)
	if !maps.Equal(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
	for scope, n := range got {
		if loaded := len(loadExtractedAnalysisHistory(e, scope.ServerURL, scope.ProjectKey, scope.Branch)); loaded != n {
			t.Errorf("%+v: counted %d, loadExtractedAnalysisHistory returns %d", scope, n, loaded)
		}
	}
}

// TestHistoryCountDropsUnreplayableDates: a record whose date is missing,
// empty, unparseable, the zero time, or not a string is not replayable, so it
// must not be counted, exactly as loadExtractedAnalysisHistory drops it.
func TestHistoryCountDropsUnreplayableDates(t *testing.T) {
	dir := t.TempDir()
	e := newProjectDataExecutor(t, dir)
	writeJSONL(filepath.Join(dir, extractRun, histTaskName), slices.Concat(
		histCountDated(projMain, branchMain, 2),
		histCountUndated(projMain, branchMain),
		histCountUndated(projMain, branchDev), // nothing replayable on this branch at all
	))

	got := countReplayableHistoryPoints(e)
	want := map[extractScope]int{{ServerURL: testServerURL, ProjectKey: projMain, Branch: branchMain}: 2}
	if !maps.Equal(got, want) {
		t.Errorf("counts = %v, want %v (a branch with no replayable point gets no entry)", got, want)
	}
	if loaded := len(loadExtractedAnalysisHistory(e, testServerURL, projMain, branchMain)); loaded != 2 {
		t.Errorf("loadExtractedAnalysisHistory returned %d points, want 2: the fixture is not what the count is compared against", loaded)
	}
}

// histOddDateLines are proj1/main records only a hand-edited extract could
// hold (json.Marshal, how extract writes every record, emits one exact-case
// "date" key and nothing else), split by what loadExtractedAnalysisHistory does
// with them. The loader reads the date with extractField: the one key spelled
// exactly "date", the last occurrence winning, whatever it holds. A struct
// decode of the same record reads it differently — keys match
// case-insensitively, a null leaves an earlier value alone, and a non-string
// fails the whole record — so each of these is one a struct-based count
// would get wrong.
func histOddDateLines() (counted, dropped []string) {
	counted = []string{
		`{"projectKey":"proj1","branch":"main","date":true,"date":"2025-07-01T00:00:00Z"}`, // a non-string before the good date
		`{"projectKey":"proj1","branch":"main","date":"2025-07-02T00:00:00Z","Date":5}`,    // a non-string under another spelling
	}
	dropped = []string{
		`{"projectKey":"proj1","branch":"main","Date":"2025-07-03T00:00:00Z"}`,             // no key spelled "date"
		`{"projectKey":"proj1","branch":"main","DATE":"2025-07-04T00:00:00Z"}`,             // ditto
		`{"projectKey":"proj1","branch":"main","date":"2025-07-05T00:00:00Z","date":null}`, // a null after the good date
	}
	return counted, dropped
}

// histNonRecordLines are lines a history chunk file could hold that
// scopedExtractItems cannot turn into a record: not valid JSON, not a JSON
// object, or an object a field of recordHeader cannot be decoded from. The
// per-branch loader skips every one of them, so the single pass must too.
// Most would only ever be miscounted into a scope nobody looks up; the line
// whose "key" is a number is routed to proj1/main with a good date, so a
// decoder that shrugged off recordHeader's decode error would add it to a
// total.
func histNonRecordLines() []string {
	return []string{
		`{"projectKey":"proj1","branch":"main","date":"2025-02-01T00:00:00Z"`, // truncated
		`not json at all`,
		`[1,2,3]`,
		`"a string"`,
		`null`,
		`{"projectKey":7,"branch":"main","date":"2025-03-01T00:00:00Z"}`,                // projectKey of the wrong type
		`{"key":5,"projectKey":"proj1","branch":"main","date":"2025-06-01T00:00:00Z"}`,  // key, which the count never uses, of the wrong type
		`{"projectKey":"proj1","branch":"main","date":"2025-04-01T00:00:00Z"} trailing`, // text after the object
		``,
	}
}

// TestHistoryCountSkipsUndecodableRecords: a line that is not a record, or
// whose header cannot be decoded, is skipped like scopedExtractItems skips it,
// and does not stop the pass from reaching the records after it.
func TestHistoryCountSkipsUndecodableRecords(t *testing.T) {
	dir := t.TempDir()
	e := newProjectDataExecutor(t, dir)
	taskDir := filepath.Join(dir, extractRun, histTaskName)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := strings.Join(slices.Concat(
		[]string{`{"projectKey":"proj1","branch":"main","date":"2025-01-01T00:00:00Z","measures":[{"metric":"ncloc","value":"1"}]}`},
		histNonRecordLines(),
		[]string{`{"projectKey":"proj1","branch":"main","date":"2025-05-01T00:00:00+0000"}`},
	), "\n") + "\n"
	if err := os.WriteFile(filepath.Join(taskDir, "results.1.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	got := countReplayableHistoryPoints(e)
	want := map[extractScope]int{{ServerURL: testServerURL, ProjectKey: projMain, Branch: branchMain}: 2}
	if !maps.Equal(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
	if loaded := len(loadExtractedAnalysisHistory(e, testServerURL, projMain, branchMain)); loaded != 2 {
		t.Errorf("loadExtractedAnalysisHistory returned %d points, want 2", loaded)
	}
}

// TestHistoryCountNothingExtracted: no history extract, or one that belongs to
// a server outside the mapping, counts nothing.
func TestHistoryCountNothingExtracted(t *testing.T) {
	t.Run("task directory absent", func(t *testing.T) {
		e := newProjectDataExecutor(t, t.TempDir())
		if got := countReplayableHistoryPoints(e); len(got) != 0 {
			t.Errorf("counts = %v, want none", got)
		}
	})

	t.Run("extract run outside the mapping", func(t *testing.T) {
		dir := t.TempDir()
		e := newProjectDataExecutor(t, dir)
		writeJSONL(filepath.Join(dir, histOtherRun, histTaskName), histCountDated(projMain, branchMain, 3))
		if got := countReplayableHistoryPoints(e); len(got) != 0 {
			t.Errorf("counts = %v, want none: %s is not in the mapping", got, histOtherRun)
		}
	})
}

// newHistoryPassProbe wires an executor over the given getProjects,
// getBranches and getProjectAnalysisHistory records, with MigrateHistory on,
// and returns it with a counter of how many times each extract task was read.
//
// Every task directory also holds a dangling results.99.jsonl symlink. Each
// pass over a task opens it, fails, and structure.ExtractItems logs a warning
// naming the file, so counting those warnings counts the passes — an
// observation of "how many times was this corpus read" that needs no hook in
// production code. Skips the test where symlinks cannot be created.
func newHistoryPassProbe(t *testing.T, projects, branches, history []map[string]any) (*Executor, func(task string) int) {
	t.Helper()
	dir := t.TempDir()
	e := newProjectDataExecutor(t, dir)
	e.MigrateHistory = true

	logs := &syncBuffer{}
	prevLogger, prevOut, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevOut) // slog.SetDefault redirected the std logger too
		log.SetFlags(prevFlags)
	})

	markers := map[string]string{}
	for task, recs := range map[string][]map[string]any{
		"getProjects": projects, "getBranches": branches, histTaskName: history,
	} {
		dir := filepath.Join(dir, extractRun, task)
		writeJSONL(dir, recs)
		marker := filepath.Join(dir, "results.99.jsonl")
		if err := os.Symlink(filepath.Join(dir, "does-not-exist"), marker); err != nil {
			t.Skipf("cannot create a symlink to observe extract reads: %v", err)
		}
		markers[task] = marker
	}
	return e, func(task string) int {
		n := 0
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, markers[task]) {
				n++
			}
		}
		return n
	}
}

// TestProjectHistoryPointTotalReadsExtractsOnlyAsNeeded pins the cost of the
// pre-count by observing reads rather than timing them: the history extract is
// read once however many branches there are, and not at all when there is
// nothing for it to be summed against.
func TestProjectHistoryPointTotalReadsExtractsOnlyAsNeeded(t *testing.T) {
	branchRow := func(project, name string, isMain bool) map[string]any {
		return map[string]any{"projectKey": project, "name": name, "isMain": isMain, "type": "BRANCH"}
	}
	projects := []map[string]any{{"key": projMain}, {"key": "proj2"}}
	branches := []map[string]any{
		branchRow(projMain, "main", true), branchRow(projMain, "develop", false), branchRow(projMain, "release/1.0", false),
		branchRow("proj2", "main", true), branchRow("proj2", "develop", false),
	}
	history := slices.Concat(
		histCountDated(projMain, "main", 2), histCountDated(projMain, "develop", 2), histCountDated(projMain, "release/1.0", 2),
		histCountDated("proj2", "main", 2), histCountDated("proj2", "develop", 2),
	)

	t.Run("history extract is read once for any number of branches", func(t *testing.T) {
		e, passes := newHistoryPassProbe(t, projects, branches, history)
		if got := projectHistoryPointTotal(e); got != 10 {
			t.Errorf("total = %d, want 10", got)
		}
		if n := passes(histTaskName); n != 1 {
			t.Errorf("history extract read %d times for 5 (project, branch) pairs, want exactly 1: a pass per pair is the O(pairs x corpus) cost of #625", n)
		}
		if n := passes("getProjects"); n != 1 {
			t.Errorf("getProjects read %d times, want 1", n)
		}
		if passes("getBranches") == 0 {
			t.Error("getBranches was never read: the probe observes nothing, so the checks below prove nothing")
		}
	})

	t.Run("migrate_history off reads nothing", func(t *testing.T) {
		e, passes := newHistoryPassProbe(t, projects, branches, history)
		e.MigrateHistory = false
		if got := projectHistoryPointTotal(e); got != 0 {
			t.Errorf("total = %d, want 0", got)
		}
		for _, task := range []string{"getProjects", "getBranches", histTaskName} {
			if n := passes(task); n != 0 {
				t.Errorf("%s read %d times with history migration off, want 0", task, n)
			}
		}
	})

	t.Run("no projects: the history extract is not read", func(t *testing.T) {
		e, passes := newHistoryPassProbe(t, nil, branches, history)
		if got := projectHistoryPointTotal(e); got != 0 {
			t.Errorf("total = %d, want 0", got)
		}
		if n := passes("getProjects"); n != 1 {
			t.Errorf("getProjects read %d times, want 1: the probe does not observe reads", n)
		}
		for _, task := range []string{"getBranches", histTaskName} {
			if n := passes(task); n != 0 {
				t.Errorf("%s read %d times with no projects to sum over, want 0", task, n)
			}
		}
	})

	t.Run("nothing replayable: branches are not resolved", func(t *testing.T) {
		undated := slices.Concat(histCountUndated(projMain, "main"), histCountUndated("proj2", "main"))
		e, passes := newHistoryPassProbe(t, projects, branches, undated)
		if got := projectHistoryPointTotal(e); got != 0 {
			t.Errorf("total = %d, want 0", got)
		}
		if n := passes(histTaskName); n != 1 {
			t.Errorf("history extract read %d times, want 1", n)
		}
		if n := passes("getBranches"); n != 0 {
			t.Errorf("getBranches read %d times though no point is replayable, want 0: the total is 0 whatever the branches are", n)
		}
	})
}

// BenchmarkProjectHistoryPointTotal runs the ETA pre-count over a corpus laid
// out the way extract writes it — ChunkWriter.WriteOne gives every history
// point its own results.N.jsonl (and getBranches gets one file per project),
// and each point carries the 22 project-level measures a real one does, which
// is most of a record's bytes — at three instance sizes. ns/op should grow with
// the size of the corpus, not with its square, which is what the per-branch
// loader this replaced did:
//
//	go test ./internal/migrate/ -run '^$' -bench ProjectHistoryPointTotal -benchmem
func BenchmarkProjectHistoryPointTotal(b *testing.B) {
	const branches, points = 3, 30
	measures := make([]map[string]any, 22)
	for i := range measures {
		measures[i] = map[string]any{"metric": "metric_" + strconv.Itoa(i), "value": strconv.Itoa(1000 + i)}
	}
	first := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)

	// writeChunk writes rows as results.n.jsonl in taskDir, one row per line,
	// the way ChunkWriter.WriteChunk does.
	writeChunk := func(taskDir string, n int, rows ...map[string]any) {
		var chunk []byte
		for _, row := range rows {
			line, err := json.Marshal(row)
			if err != nil {
				b.Fatal(err)
			}
			chunk = append(append(chunk, line...), '\n')
		}
		if err := os.WriteFile(filepath.Join(taskDir, fmt.Sprintf("results.%d.jsonl", n)), chunk, 0o644); err != nil {
			b.Fatal(err)
		}
	}

	for _, projects := range []int{10, 20, 40} {
		dir := b.TempDir()
		branchDir := filepath.Join(dir, extractRun, "getBranches")
		histDir := filepath.Join(dir, extractRun, histTaskName)
		for _, d := range []string{branchDir, histDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				b.Fatal(err)
			}
		}
		var projectRows []map[string]any
		file := 0
		for p := range projects {
			project := fmt.Sprintf("proj-%03d", p)
			projectRows = append(projectRows, map[string]any{"key": project})
			var branchRows []map[string]any
			for br := range branches {
				name := "main"
				if br > 0 {
					name = "br-" + strconv.Itoa(br)
				}
				branchRows = append(branchRows, map[string]any{
					"projectKey": project, "name": name, "isMain": br == 0, "type": "BRANCH",
				})
				for pt := range points {
					rec := histRecord(project, name, first.AddDate(0, 0, pt).Format(time.RFC3339), "1.0", "")
					rec["measures"] = measures
					file++
					writeChunk(histDir, file, rec)
				}
			}
			writeChunk(branchDir, p+1, branchRows...)
		}
		writeJSONL(filepath.Join(dir, extractRun, "getProjects"), projectRows)
		e := &Executor{ExportDir: dir, Mapping: structure.ExtractMapping{testServerURL: extractRun}, MigrateHistory: true}

		b.Run(fmt.Sprintf("projects=%d", projects), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := projectHistoryPointTotal(e); got != file {
					b.Fatalf("total = %d, want %d", got, file)
				}
			}
		})
	}
}

// TestMigrateBranchHistoryIncrementsHistoryProgress covers #564's other
// half of the history-ETA wiring: every point migrateBranchHistory
// attempts — success or failure — must advance e.HistoryProgress, since
// either way real wall-clock time was spent on it.
func TestMigrateBranchHistoryIncrementsHistoryProgress(t *testing.T) {
	t.Run("all points succeed", func(t *testing.T) {
		rec := newHistRecorder()
		mux := histProfileMux(rec)
		mux.HandleFunc("POST /api/ce/submit", func(w http.ResponseWriter, r *http.Request) {
			rec.note(r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-hist"})
		})
		mux.HandleFunc("GET /api/ce/task", func(w http.ResponseWriter, r *http.Request) {
			rec.note(r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"status": "SUCCESS"}})
		})
		addDefaultCloudHandler(mux)

		e := newCustomCloudTest(t, mux)
		e.MigrateHistory = true
		e.HistoryProgress = common.NewProgressLogger(e.Logger, "migrateProjectHistory", 2)
		histSeed(e, []map[string]any{
			histRecord(projMain, branchMain, "2022-01-15T00:00:00Z", "1.0", "100"),
			histRecord(projMain, branchMain, "2023-06-01T00:00:00Z", "2.0", "200"),
		})

		migrateBranchHistory(context.Background(), e,
			histBranchContext(), branchInfo{Name: branchMain, IsMain: true}, branchMain)

		if got := e.HistoryProgress.Fraction(); got != 1 {
			t.Errorf("HistoryProgress.Fraction() = %v, want 1 (both points incremented)", got)
		}
	})

	t.Run("stops after first failure, only the attempted point counts", func(t *testing.T) {
		rec := newHistRecorder()
		mux := histProfileMux(rec)
		mux.HandleFunc("POST /api/ce/submit", func(w http.ResponseWriter, r *http.Request) {
			rec.note(r.URL.Path)
			http.Error(w, `{"errors":[{"msg":"nope"}]}`, http.StatusInternalServerError)
		})
		addDefaultCloudHandler(mux)

		e := newCustomCloudTest(t, mux)
		e.MigrateHistory = true
		e.HistoryProgress = common.NewProgressLogger(e.Logger, "migrateProjectHistory", 2)
		histSeed(e, []map[string]any{
			histRecord(projMain, branchMain, "2022-01-15T00:00:00Z", "1.0", "100"),
			histRecord(projMain, branchMain, "2023-06-01T00:00:00Z", "2.0", "200"),
		})

		migrateBranchHistory(context.Background(), e,
			histBranchContext(), branchInfo{Name: branchMain, IsMain: true}, branchMain)

		if got := e.HistoryProgress.Fraction(); got != 0.5 {
			t.Errorf("HistoryProgress.Fraction() = %v, want 0.5 (1 of 2 points attempted before giving up)", got)
		}
	})

	t.Run("nil HistoryProgress is a no-op, not a panic", func(t *testing.T) {
		rec := newHistRecorder()
		mux := histProfileMux(rec)
		mux.HandleFunc("POST /api/ce/submit", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-hist"})
		})
		mux.HandleFunc("GET /api/ce/task", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"status": "SUCCESS"}})
		})
		addDefaultCloudHandler(mux)

		e := newCustomCloudTest(t, mux)
		e.MigrateHistory = true
		// e.HistoryProgress deliberately left nil, mirroring a real run with
		// no history to replay for OTHER projects.
		histSeed(e, []map[string]any{
			histRecord(projMain, branchMain, "2022-01-15T00:00:00Z", "1.0", "100"),
		})

		migrateBranchHistory(context.Background(), e,
			histBranchContext(), branchInfo{Name: branchMain, IsMain: true}, branchMain) // must not panic
	})
}
