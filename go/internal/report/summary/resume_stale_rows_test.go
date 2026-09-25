// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// writeImportAttempt appends one migration attempt's importProjectData
// rows to runDir, through a real common.ChunkWriter.
//
// Going through the writer rather than hand-placing a results.1.jsonl is
// the point of these tests: it reproduces what a --run_id resume actually
// leaves on disk, including the chunk numbering that #604 got wrong. Call
// it once per attempt.
func writeImportAttempt(t *testing.T, runDir string, rows []map[string]any) {
	t.Helper()
	w, err := common.NewDataStore(runDir).Writer("importProjectData")
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("marshal %v: %v", row, err)
		}
		if err := w.WriteOne(b); err != nil {
			t.Fatalf("WriteOne: %v", err)
		}
	}
}

// #604 — the headline case. A run is cancelled after the first project
// finishes, so the second project records "skipped: migration cancelled".
// The resume retries only that project and succeeds.
//
// Before the fix the resume's single row restarted at results.1 and
// truncated the first project's success row, while the second project's
// stale cancellation row survived untouched in results.2. The report then
// bucketed a project the resume had fully migrated as Skipped, and lost
// the first project's row on top of it.
func TestCollectProjectData_ResumeSupersedesStaleCancelledRow(t *testing.T) {
	dir := t.TempDir()

	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-one", "branch": "main", "status": "success"},
		{"cloud_project_key": "proj-two", "branch": "main", "status": "skipped", "error": "skipped: migration cancelled"},
	})
	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-two", "branch": "main", "status": "success"},
	})

	got := collectProjectData(common.NewDataStore(dir))

	if state := got["proj-one"].State; state != "success" {
		t.Errorf("proj-one: the first attempt's success row must survive the resume, got State=%q", state)
	}
	outcome, ok := got["proj-two"]
	if !ok {
		t.Fatal("proj-two: expected an outcome")
	}
	if outcome.State != "success" {
		t.Errorf("proj-two: the resume migrated it, want State=success, got %+v", outcome)
	}
}

// The same mechanism with a failure rather than a cancellation. The issue
// reports this as the more severe form, because a stale "failed" row
// renders the project Failed and quotes a CE error that no longer applies.
func TestCollectProjectData_ResumeSupersedesStaleFailedRow(t *testing.T) {
	dir := t.TempDir()

	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-one", "branch": "main", "status": "success"},
		{"cloud_project_key": "proj-two", "branch": "main", "status": "failed", "error": "API error when migrating project data: CE task failed"},
	})
	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-two", "branch": "main", "status": "success"},
	})

	outcome := collectProjectData(common.NewDataStore(dir))["proj-two"]
	if outcome.State != "success" {
		t.Errorf("want State=success after a successful retry, got %+v", outcome)
	}
	if outcome.Reason != "" {
		t.Errorf("want no reason once the failure is superseded, got %q", outcome.Reason)
	}
}

// A retry that fails again must still report the failure: last-row-wins
// resolves to the newest row whatever it says, it does not prefer good
// news.
func TestCollectProjectData_ResumeKeepsFreshFailure(t *testing.T) {
	dir := t.TempDir()

	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-one", "branch": "main", "status": "skipped", "error": "skipped: migration cancelled"},
	})
	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-one", "branch": "main", "status": "failed", "error": "API error when migrating project data: CE task failed"},
	})

	outcome := collectProjectData(common.NewDataStore(dir))["proj-one"]
	if outcome.State != "failed" {
		t.Errorf("want State=failed, got %+v", outcome)
	}
}

// Other branches of the same project keep their own rows: resolution is
// per (project, branch), not per project.
func TestCollectProjectData_ResumeResolvesPerBranch(t *testing.T) {
	dir := t.TempDir()

	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-one", "branch": "main", "status": "success"},
		{"cloud_project_key": "proj-one", "branch": "develop", "status": "skipped", "error": "skipped: migration cancelled"},
		{"cloud_project_key": "proj-one", "branch": "release", "status": "failed", "error": "CE task failed"},
	})
	// Only develop is retried. release stays failed, and must still win.
	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-one", "branch": "develop", "status": "success"},
	})

	outcome := collectProjectData(common.NewDataStore(dir))["proj-one"]
	if outcome.State != "failed" {
		t.Errorf("release is still failed, so the project must be failed; got %+v", outcome)
	}
}

// #604 + #605 — the live shape of the fix: a resume against a target that
// already holds the branch records up_to_date (#588), which must both
// supersede the stale cancellation row AND count as success.
func TestCollectProjectData_ResumeUpToDateSupersedesStaleRow(t *testing.T) {
	dir := t.TempDir()

	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-one", "branch": "main", "status": "success"},
		{"cloud_project_key": "proj-two", "branch": "main", "status": "skipped", "error": "skipped: migration cancelled"},
	})
	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-two", "branch": "main", "status": statusUpToDate},
	})

	if state := collectProjectData(common.NewDataStore(dir))["proj-two"].State; state != "success" {
		t.Errorf("want State=success for an up_to_date retry, got %q", state)
	}
}

// #604 — a project whose every branch was dropped by the #584 cap used to
// fall through to the unrecognised-status default, which blames the
// source: "Source project was provisioned but never analyzed". Nothing
// was migrated, so Skipped is right, but the reason must name the cap.
func TestCollectProjectData_CappedOnlyProjectNamesTheCap(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "importProjectData", []map[string]any{
		{"cloud_project_key": "proj-a", "branch": "feature/x", "status": statusCapped, "branch_limit_exceeded": true},
		{"cloud_project_key": "proj-a", "branch": "feature/y", "status": statusCapped, "branch_limit_exceeded": true},
	})

	outcome, ok := collectProjectData(common.NewDataStore(dir))["proj-a"]
	if !ok {
		t.Fatal("expected an outcome for proj-a")
	}
	if outcome.State != "skipped" {
		t.Errorf("nothing was migrated, want State=skipped, got %+v", outcome)
	}
	if !strings.Contains(outcome.Reason, "branch limit") {
		t.Errorf("reason must name the branch limit, got %q", outcome.Reason)
	}
	if strings.Contains(outcome.Reason, "never analyzed") {
		t.Errorf("reason must not blame the source for the run's own cap, got %q", outcome.Reason)
	}
	if outcome.NeverAnalyzed {
		t.Error("NeverAnalyzed must stay false — the source was analyzed, this run just capped it")
	}
}

// #604 finding — collectProjectSyncSkips routed every non-"success" row
// to Partial, which demoted two healthy outcomes: an up_to_date re-run
// (#588) and a capped branch (#584, whose writer explicitly requires it
// not to degrade the project).
func TestCollectProjectSyncSkips_HealthyStatusesDoNotDegrade(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "importProjectData", []map[string]any{
		{"cloud_project_key": "proj-uptodate", "branch": "main", "status": statusUpToDate},
		{"cloud_project_key": "proj-capped", "branch": "main", "status": "success"},
		{"cloud_project_key": "proj-capped", "branch": "feature/x", "status": statusCapped, "branch_limit_exceeded": true},
		{"cloud_project_key": "proj-broken", "branch": "main", "status": "failed", "error": "CE task failed"},
	})
	store := common.NewDataStore(dir)

	var routed []string
	for _, f := range collectProjectSyncSkips(store, collectProjectData(store)) {
		routed = append(routed, f.CloudProjectKey)
	}

	for _, key := range []string{"proj-uptodate", "proj-capped"} {
		for _, got := range routed {
			if got == key {
				t.Errorf("%s was routed to Partial but it migrated fine; routed=%v", key, routed)
			}
		}
	}
	found := false
	for _, got := range routed {
		if got == "proj-broken" {
			found = true
		}
	}
	if !found {
		t.Errorf("a genuinely failed project must still be routed to Partial; routed=%v", routed)
	}
}

// A resume that finally migrates a branch the cap had dropped must stop
// listing it as dropped in the report's "|branchLimit:" note.
func TestCollectBranchLimitSkips_ResumeClearsAStaleCap(t *testing.T) {
	dir := t.TempDir()

	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-a", "branch": "main", "status": "success"},
		{"cloud_project_key": "proj-a", "branch": "feature/x", "status": statusCapped, "branch_limit_exceeded": true},
		{"cloud_project_key": "proj-a", "branch": "feature/y", "status": statusCapped, "branch_limit_exceeded": true},
	})
	// Resumed with a higher --max_branches_per_project: feature/x now
	// imports, feature/y is still over the cap.
	writeImportAttempt(t, dir, []map[string]any{
		{"cloud_project_key": "proj-a", "branch": "feature/x", "status": "success"},
	})

	got := collectBranchLimitSkips(common.NewDataStore(dir))
	if len(got["proj-a"]) != 1 || got["proj-a"][0] != "feature/y" {
		t.Errorf("want only the still-capped branch [feature/y], got %v", got["proj-a"])
	}
}

// resolveProjectDataRows keeps first-seen order (so the report still
// lists branches in the order the operator watched them go by) while
// taking the last value for each (project, branch).
func TestResolveProjectDataRows(t *testing.T) {
	raw := func(key, branch, status string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"cloud_project_key": key, "branch": branch, "status": status,
		})
		return b
	}
	resolved := resolveProjectDataRows([]json.RawMessage{
		raw("p1", "main", "success"),
		raw("p1", "develop", "skipped"),
		raw("p2", "main", "failed"),
		raw("p1", "develop", "success"),
		raw("p2", "main", "success"),
	})

	if len(resolved) != 3 {
		t.Fatalf("expected 3 resolved rows, got %d: %v", len(resolved), resolved)
	}
	want := []struct{ branch, status string }{
		{"main", "success"},
		{"develop", "success"},
		{"main", "success"},
	}
	for i, w := range want {
		if got := jsonStr(resolved[i], fieldBranch); got != w.branch {
			t.Errorf("row %d: branch = %q, want %q (first-seen order not preserved)", i, got, w.branch)
		}
		if got := jsonStr(resolved[i], "status"); got != w.status {
			t.Errorf("row %d: status = %q, want %q (last value did not win)", i, got, w.status)
		}
	}
}

// A row with no branch is still resolvable: the empty branch is a key
// like any other, and two attempts for the same project collapse.
func TestResolveProjectDataRowsHandlesMissingBranch(t *testing.T) {
	resolved := resolveProjectDataRows([]json.RawMessage{
		json.RawMessage(`{"cloud_project_key":"p1","status":"skipped"}`),
		json.RawMessage(`{"cloud_project_key":"p1","status":"success"}`),
	})
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved row, got %d: %v", len(resolved), resolved)
	}
	if got := jsonStr(resolved[0], "status"); got != "success" {
		t.Errorf("status = %q, want success", got)
	}
}

// #604 — ten chunks is where lexicographic file ordering starts lying:
// results.10.jsonl sorts before results.2.jsonl. With more than nine rows
// in the first attempt, a resume's row must still be read last.
func TestCollectProjectData_ResumeWinsPastTenChunks(t *testing.T) {
	dir := t.TempDir()

	first := []map[string]any{
		{"cloud_project_key": "proj-a", "branch": "main", "status": "success"},
	}
	for i := range 11 {
		first = append(first, map[string]any{
			"cloud_project_key": "proj-a",
			"branch":            "feature/" + string(rune('a'+i)),
			"status":            "skipped",
			"error":             "skipped: migration cancelled",
		})
	}
	writeImportAttempt(t, dir, first)

	retry := make([]map[string]any, 0, 11)
	for i := range 11 {
		retry = append(retry, map[string]any{
			"cloud_project_key": "proj-a",
			"branch":            "feature/" + string(rune('a'+i)),
			"status":            "success",
		})
	}
	writeImportAttempt(t, dir, retry)

	if state := collectProjectData(common.NewDataStore(dir))["proj-a"].State; state != "success" {
		t.Errorf("every branch was retried successfully, want State=success, got %q", state)
	}
}
