// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// #588 — a re-run finds every branch already on the target and submits
// nothing. The project is fully migrated, so it must still report as
// Succeeded; "up_to_date" must not fall through to the unrecognised-state
// default, which degrades the project to Skipped.
func TestCollectProjectData_UpToDateBranchesReportSuccess(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "importProjectData", []map[string]any{
		{"cloud_project_key": "proj-a", "branch": "main", "status": "up_to_date"},
		{"cloud_project_key": "proj-a", "branch": "develop", "status": "up_to_date"},
	})

	outcome, ok := collectProjectData(common.NewDataStore(dir))["proj-a"]
	if !ok {
		t.Fatalf("expected an outcome for proj-a")
	}
	if outcome.State != "success" {
		t.Errorf("expected State=success, got %+v", outcome)
	}
}

// A partial re-run: one branch was already on the target, another was newly
// imported. Mixing the two must not degrade the project either.
func TestCollectProjectData_MixedUpToDateAndSuccess(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "importProjectData", []map[string]any{
		{"cloud_project_key": "proj-a", "branch": "main", "status": "up_to_date"},
		{"cloud_project_key": "proj-a", "branch": "develop", "status": "success"},
	})

	if got := collectProjectData(common.NewDataStore(dir))["proj-a"].State; got != "success" {
		t.Errorf("expected State=success, got %q", got)
	}
}

// A real failure still wins over an up-to-date branch: worst-outcome-wins
// must keep working.
func TestCollectProjectData_FailureBeatsUpToDate(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "importProjectData", []map[string]any{
		{"cloud_project_key": "proj-a", "branch": "main", "status": "up_to_date"},
		{"cloud_project_key": "proj-a", "branch": "develop", "status": "failed", "error": "CE task failed"},
	})

	if got := collectProjectData(common.NewDataStore(dir))["proj-a"].State; got != "failed" {
		t.Errorf("expected State=failed, got %q", got)
	}
}
