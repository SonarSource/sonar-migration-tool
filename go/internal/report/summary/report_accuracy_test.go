// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// runDirWithEvents writes records to a fresh run directory's
// run_events.jsonl (shared contract A) and returns the directory.
func runDirWithEvents(t *testing.T, records []map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	writeRunEvents(t, dir, records)
	return dir
}

func event(message string, attrs map[string]any) map[string]any {
	return map[string]any{"level": "INFO", "message": message, "attrs": attrs}
}

// findBranch returns the row for one project+branch, or fails the test.
func findBranch(t *testing.T, branches []BranchStat, project, branch string) BranchStat {
	t.Helper()
	for _, b := range branches {
		if b.Project == project && b.Branch == branch {
			return b
		}
	}
	t.Fatalf("no row for project %q branch %q; got %+v", project, branch, branches)
	return BranchStat{}
}

// Two projects that both have a "main" must produce two rows.
//
// Keyed on the bare branch name they collapsed into one, and whichever
// project's events were processed last overwrote the other's counts — a
// two-project run published one project's numbers and silently dropped
// the other's.
func TestBranchRowsAreKeyedByProjectAndBranch(t *testing.T) {
	dir := runDirWithEvents(t, []map[string]any{
		event("report packaged", map[string]any{
			"project": "org_alpha", "sourceBranch": "main", "targetBranch": "main",
			"issues": 72.0, "components": 12.0, "activeRules": 824.0, "zipSizeBytes": 88958.0,
		}),
		event("report packaged", map[string]any{
			"project": "org_beta", "sourceBranch": "main", "targetBranch": "main",
			"issues": 222.0, "components": 132.0, "activeRules": 877.0, "zipSizeBytes": 722519.0,
		}),
	})

	var rt runtimeData
	collectRunEvents(dir, &rt)

	if len(rt.Branches) != 2 {
		t.Fatalf("got %d branch rows, want 2 (one per project); rows: %+v", len(rt.Branches), rt.Branches)
	}
	alpha := findBranch(t, rt.Branches, "org_alpha", "main")
	if alpha.Issues != 72 || alpha.Components != 12 || alpha.ZipBytes != 88958 {
		t.Errorf("org_alpha row = %+v, want issues=72 components=12 zip=88958", alpha)
	}
	beta := findBranch(t, rt.Branches, "org_beta", "main")
	if beta.Issues != 222 || beta.Components != 132 || beta.ZipBytes != 722519 {
		t.Errorf("org_beta row = %+v, want issues=222 components=132 zip=722519", beta)
	}

	// Throughput sums every branch, so the collision undercounted it too.
	if rt.Throughput.TotalIssues != 294 {
		t.Errorf("TotalIssues = %d, want 294 (72+222)", rt.Throughput.TotalIssues)
	}
	if rt.Throughput.BranchesPackaged != 2 {
		t.Errorf("BranchesPackaged = %d, want 2", rt.Throughput.BranchesPackaged)
	}
}

// The main branch is packaged under its source name and submitted under
// the target name, then renamed to match the source. Both names describe
// one branch and must land on one row — they used to produce two, one
// holding the metrics and the other only the CE task id.
func TestRenamedMainBranchCollapsesToOneRow(t *testing.T) {
	packaged := event("report packaged", map[string]any{
		"project": "org_alpha", "sourceBranch": "main", "targetBranch": "master",
		"issues": 72.0, "components": 12.0,
	})
	submitted := event("CE task submitted", map[string]any{
		"project": "org_alpha", "targetBranch": "master", "taskId": "task-abc",
	})

	// Both orders, because events are not guaranteed to arrive in one.
	for _, tc := range []struct {
		name   string
		events []map[string]any
	}{
		{"packaged first", []map[string]any{packaged, submitted}},
		{"submitted first", []map[string]any{submitted, packaged}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rt runtimeData
			collectRunEvents(runDirWithEvents(t, tc.events), &rt)
			assertOneMergedMainBranch(t, rt.Branches)
		})
	}
}

// assertOneMergedMainBranch checks that the packaged metrics and the CE
// task id ended up on a single row carrying the post-rename branch name.
func assertOneMergedMainBranch(t *testing.T, branches []BranchStat) {
	t.Helper()
	if len(branches) != 1 {
		t.Fatalf("got %d rows, want 1; rows: %+v", len(branches), branches)
	}
	got := branches[0]
	if got.Branch != "main" {
		t.Errorf("Branch = %q, want %q (the post-rename name)", got.Branch, "main")
	}
	if got.Issues != 72 || got.Components != 12 {
		t.Errorf("metrics lost in merge: %+v", got)
	}
	if got.TaskID != "task-abc" {
		t.Errorf("TaskID = %q, want task-abc", got.TaskID)
	}
	if got.Status != "packaged" {
		t.Errorf("Status = %q, want packaged (the stronger statement)", got.Status)
	}
}

// An HTTP status is logged as a number, so reading it as a string yielded
// "" and left the retry ledger's most useful column blank on every row.
func TestRetryLastStatusReadsNumericAttr(t *testing.T) {
	dir := runDirWithEvents(t, []map[string]any{
		event("retrying request", map[string]any{
			"method": "GET", "endpoint": "/api/x", "attempt": 1.0, "status": 500.0,
		}),
	})
	var rt runtimeData
	collectRunEvents(dir, &rt)

	if len(rt.Warnings.Retries) != 1 {
		t.Fatalf("got %d retry rows, want 1", len(rt.Warnings.Retries))
	}
	if got := rt.Warnings.Retries[0].LastStatus; got != "500" {
		t.Errorf("LastStatus = %q, want %q", got, "500")
	}
}

// Tasks in a phase run concurrently, so the coarse phase durations are
// the wall-clock union of their intervals. Summing them counted the same
// seconds once per overlapping task and reported more elapsed time than
// the run took.
func TestPhaseBreakdownUsesWallClockNotSumOfDurations(t *testing.T) {
	start := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	// Three fully-overlapping 10s tasks, all of them CategoryGeneral:
	// 10s of wall-clock, not 30s.
	tasks := []TaskTiming{
		{Task: "createGroups", StartedAt: start, Duration: 10 * time.Second},
		{Task: "createGates", StartedAt: start, Duration: 10 * time.Second},
		{Task: "createProfiles", StartedAt: start, Duration: 10 * time.Second},
	}
	got := breakdownFor(t, buildPhaseBreakdown(tasks), "Global objects provisioning")
	if got != 10*time.Second {
		t.Errorf("overlapping tasks = %s, want 10s (union, not the 30s sum)", got)
	}
}

// Disjoint intervals must still add up, and a gap between them must not
// be counted as time spent.
func TestPhaseBreakdownAddsDisjointIntervalsAndIgnoresGaps(t *testing.T) {
	start := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	tasks := []TaskTiming{
		{Task: "createGroups", StartedAt: start, Duration: 5 * time.Second},
		// Starts 20s in: 15s of idle between the two.
		{Task: "createGates", StartedAt: start.Add(20 * time.Second), Duration: 5 * time.Second},
	}
	got := breakdownFor(t, buildPhaseBreakdown(tasks), "Global objects provisioning")
	if got != 10*time.Second {
		t.Errorf("disjoint tasks = %s, want 10s (5+5, excluding the gap)", got)
	}
}

// Runs recorded before task start times existed have no intervals to
// union, so the breakdown falls back to summing — the best the data
// supports, and better than reporting zero.
func TestPhaseBreakdownFallsBackToSumWithoutStartTimes(t *testing.T) {
	tasks := []TaskTiming{
		{Task: "createGroups", Duration: 4 * time.Second},
		{Task: "createGates", Duration: 6 * time.Second},
	}
	got := breakdownFor(t, buildPhaseBreakdown(tasks), "Global objects provisioning")
	if got != 10*time.Second {
		t.Errorf("legacy run = %s, want the 10s sum", got)
	}
}

func breakdownFor(t *testing.T, entries []PhaseBreakdownEntry, name string) time.Duration {
	t.Helper()
	for _, e := range entries {
		if e.Name == name {
			return e.Duration
		}
	}
	t.Fatalf("no breakdown entry named %q in %+v", name, entries)
	return 0
}

// Failures the migration is content with must not be counted as
// failures. Migrating twice into one organization took a run's failure
// count from 3 to 6 while migrating exactly as well as the first time.
func TestSplitFailedSeparatesExpectedFromActionable(t *testing.T) {
	section := Section{
		Name: "Groups",
		Failed: []EntityItem{
			{Name: "migration-scanners", Cause: "already-done"},
			{Name: "sonar.authenticator.downcase", Cause: "by-design"},
			{Name: "SQS migrated project", Cause: "customer-environment-issue"},
			{Name: "devs", Cause: "bug"},
			// An unclassified failure must stay actionable: the report
			// must not call a failure benign on no evidence.
			{Name: "mystery", Cause: ""},
		},
	}
	actionable, expected := section.SplitFailed()

	if len(expected) != 2 {
		t.Errorf("expected bucket = %d rows, want 2 (already-done, by-design); got %+v", len(expected), expected)
	}
	if len(actionable) != 3 {
		t.Errorf("actionable bucket = %d rows, want 3 (environment, bug, unclassified); got %+v", len(actionable), actionable)
	}
}

// Skipped items whose reason has no entry in skipReasonOrder were counted
// in the section total but never rendered, so a section announced "284
// skipped" above a table listing 274.
func TestUnknownSkipReasonIsStillRenderedAndCounted(t *testing.T) {
	section := Section{
		Name: "Global Settings",
		Skipped: []EntityItem{
			{Name: "sonar.a", SkipReason: SkipReasonDefaultValue},
			{Name: "sonar.b", SkipReason: SkipReasonNotOnSQC},
			{Name: "sonar.c", SkipReason: "a-reason-nobody-has-a-label-for"},
		},
	}

	rows := buildUnifiedRows(section, false)
	if len(rows) != 3 {
		t.Fatalf("rendered %d rows for 3 skipped items, want 3; rows: %+v", len(rows), rows)
	}
	names := map[string]bool{}
	for _, r := range rows {
		names[r.name] = true
	}
	for _, want := range []string{"sonar.a", "sonar.b", "sonar.c"} {
		if !names[want] {
			t.Errorf("skipped item %q never reached the table", want)
		}
	}

	// The breakdown must account for the same 3 the total claims.
	line := sectionCountSummary(section)
	if !strings.Contains(line, "3 skipped") {
		t.Errorf("count summary = %q, want it to report 3 skipped", line)
	}
	if !strings.Contains(line, "a-reason-nobody-has-a-label-for") {
		t.Errorf("count summary = %q, want the unlabelled reason accounted for", line)
	}
}

// A task that returned no error while failing every item it touched must
// not render as OK, and the column beside it must say how much failed.
func TestFailedItemsColumnDistinguishesActionableFromExpected(t *testing.T) {
	for _, tc := range []struct {
		name string
		task TaskTiming
		want string
	}{
		{"clean task says nothing", TaskTiming{Succeeded: 2}, ""},
		{"all failures actionable", TaskTiming{Failed: 2, ActionableFailures: 2}, "2"},
		{"no failure actionable", TaskTiming{Failed: 2, ActionableFailures: 0}, "2 (none actionable)"},
		{"mixed", TaskTiming{Failed: 5, ActionableFailures: 2}, "5 (2 actionable)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fmtFailedItems(tc.task); got != tc.want {
				t.Errorf("fmtFailedItems = %q, want %q", got, tc.want)
			}
		})
	}
}

// The report's widest tables gained a column each, on a Letter page that
// was already using every millimetre of its 195mm content width. Render
// the fully-seeded summary — two projects sharing a main branch, failures
// carrying a project, tasks carrying failed-item counts, retries carrying
// a status — and assert a well-formed PDF still comes out.
//
// RenderPDF is the only consumer of the column widths, and nothing
// exercised it with runtime data before, so a table running off the page
// would have shipped unnoticed.
func TestRenderPDFWithWidenedRuntimeTables(t *testing.T) {
	seeded := fullySeededSummary()
	// The fixture's tasks carry no per-item tallies, so fmtFailedItems
	// returns "" for both and the new column is laid out empty — the
	// width it actually has to hold never gets exercised. Seed the
	// widest forms: "5 (2 actionable)" and "2 (none actionable)".
	seeded.Tasks[0].Succeeded, seeded.Tasks[0].Failed, seeded.Tasks[0].ActionableFailures = 3, 5, 2
	seeded.Tasks[1].Failed, seeded.Tasks[1].ActionableFailures = 2, 0

	pdfBytes, err := RenderPDF(seeded)
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if !bytes.HasPrefix(pdfBytes, []byte("%PDF-")) {
		t.Fatalf("output is not a PDF; first bytes: %q", pdfBytes[:min(8, len(pdfBytes))])
	}
	if !bytes.Contains(pdfBytes, []byte("%%EOF")) {
		t.Error("PDF is missing its EOF trailer, so it was truncated mid-write")
	}
	// A seeded report spans several pages; a collapse to one page would
	// mean whole sections silently stopped rendering.
	if pages := bytes.Count(pdfBytes, []byte("/Type /Page\n")); pages < 2 {
		t.Errorf("got %d pages, want at least 2 — sections appear to be missing", pages)
	}
	// A fixed byte floor proves nothing here: RenderPDF embeds fonts, so
	// a summary with no sections at all already clears any round number.
	// Measure against that empty rendering instead, so runtime sections
	// silently ceasing to render shows up as the gap collapsing.
	baseline, err := RenderPDF(&MigrationSummary{RunID: "baseline", GeneratedAt: time.Now()})
	if err != nil {
		t.Fatalf("RenderPDF(baseline): %v", err)
	}
	if len(pdfBytes) <= len(baseline)+10_000 {
		t.Errorf("seeded PDF is %d bytes against a %d-byte empty baseline — sections appear to be missing",
			len(pdfBytes), len(baseline))
	}
}

// Predictive reports never populate the runtime fields, so every renderer
// this change touched has to no-op rather than divide by zero, index an
// empty slice, or emit a headerless table.
func TestRenderersDegradeOnAnEmptySummary(t *testing.T) {
	empty := &MigrationSummary{RunID: "empty", GeneratedAt: time.Now()}

	md, err := RenderMarkdown(empty)
	if err != nil {
		t.Fatalf("RenderMarkdown on an empty summary: %v", err)
	}
	for _, unwanted := range []string{"Branch Project Data", "Per-Branch CE", "Failure Ledger", "Retries"} {
		if strings.Contains(string(md), unwanted) {
			t.Errorf("empty summary still rendered the %q section", unwanted)
		}
	}
	if _, err := RenderPDF(empty); err != nil {
		t.Fatalf("RenderPDF on an empty summary: %v", err)
	}

	// A predictive summary omits Global Settings and has no runtime data,
	// which is the shape predictive-report produces.
	predictive := &MigrationSummary{
		RunID:        "predictive",
		GeneratedAt:  time.Now(),
		Predictive:   true,
		OmitSections: map[string]bool{"Global Settings": true},
		Sections: []Section{{
			Name:      "Projects",
			Succeeded: []EntityItem{{Name: "proj-a", Organization: "org1"}},
		}},
	}
	if _, err := RenderMarkdown(predictive); err != nil {
		t.Fatalf("RenderMarkdown on a predictive summary: %v", err)
	}
	if _, err := RenderPDF(predictive); err != nil {
		t.Fatalf("RenderPDF on a predictive summary: %v", err)
	}
}

// A project can carry both a "master" (its main branch) and a genuinely
// separate branch called "main". The main branch is packaged under its
// source name "master" but submitted under SonarQube Cloud's "main", so
// "main" is aliased onto "master" — and the real "main" branch then
// resolved through that alias, overwriting master's counts and emitting
// no row of its own. That is this change's own row collapse, one level
// down: a name a branch owns must never be aliased away.
func TestAliasDoesNotSwallowABranchThatOwnsTheName(t *testing.T) {
	renamedMain := event("report packaged", map[string]any{
		"project": "org_alpha", "sourceBranch": "master", "targetBranch": "main",
		"issues": 72.0, "components": 12.0,
	})
	// A non-main branch is never renamed, so it is packaged and
	// submitted under the single name it has.
	ownsTheName := event("report packaged", map[string]any{
		"project": "org_alpha", "sourceBranch": "main", "targetBranch": "main",
		"issues": 9.0, "components": 3.0,
	})

	for _, tc := range []struct {
		name   string
		events []map[string]any
	}{
		{"renamed main first", []map[string]any{renamedMain, ownsTheName}},
		{"real main first", []map[string]any{ownsTheName, renamedMain}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rt runtimeData
			collectRunEvents(runDirWithEvents(t, tc.events), &rt)

			if len(rt.Branches) != 2 {
				t.Fatalf("got %d rows, want 2 (master and main); rows: %+v", len(rt.Branches), rt.Branches)
			}
			if got := findBranch(t, rt.Branches, "org_alpha", "master"); got.Issues != 72 || got.Components != 12 {
				t.Errorf("master row = %+v, want issues=72 components=12", got)
			}
			if got := findBranch(t, rt.Branches, "org_alpha", "main"); got.Issues != 9 || got.Components != 3 {
				t.Errorf("main row = %+v, want issues=9 components=3", got)
			}
			if rt.Throughput.TotalIssues != 81 {
				t.Errorf("TotalIssues = %d, want 81 (72+9)", rt.Throughput.TotalIssues)
			}
		})
	}
}
