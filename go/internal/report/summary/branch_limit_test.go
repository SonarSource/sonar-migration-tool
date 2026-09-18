// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// #584 — collectBranchLimitSkips groups branches dropped by the hard
// per-project branch cap, de-duplicating repeats and preserving
// first-seen order. Only rows with branch_limit_exceeded=true contribute.
func TestCollectBranchLimitSkips(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "importProjectData", []map[string]any{
		{"cloud_project_key": "proj-a", "branch": "main", "status": "success"},
		{"cloud_project_key": "proj-a", "branch": "feature/x", "status": "capped", "branch_limit_exceeded": true},
		{"cloud_project_key": "proj-a", "branch": "feature/y", "status": "capped", "branch_limit_exceeded": true},
		// duplicate dropped row must collapse to a single branch entry.
		{"cloud_project_key": "proj-a", "branch": "feature/y", "status": "capped", "branch_limit_exceeded": true},
		{"cloud_project_key": "proj-b", "branch": "feature/z", "status": "capped", "branch_limit_exceeded": true},
		// proj-c hit no cap — must be absent from the map.
		{"cloud_project_key": "proj-c", "branch": "main", "status": "success"},
	})

	got := collectBranchLimitSkips(common.NewDataStore(dir))

	if len(got["proj-a"]) != 2 || got["proj-a"][0] != "feature/x" || got["proj-a"][1] != "feature/y" {
		t.Errorf("proj-a: want [feature/x feature/y], got %v", got["proj-a"])
	}
	if len(got["proj-b"]) != 1 || got["proj-b"][0] != "feature/z" {
		t.Errorf("proj-b: want [feature/z], got %v", got["proj-b"])
	}
	if _, ok := got["proj-c"]; ok {
		t.Errorf("proj-c must not be present, got %v", got["proj-c"])
	}
}

// #584 — a project that hit the cap on some branches but still migrated
// the rest successfully must not be reported as anything other than
// Succeeded: collectProjectData's worst-outcome-wins logic must not
// treat "capped" as a skip/failure signal.
func TestCollectProjectData_BranchCapDoesNotDegradeOutcome(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "importProjectData", []map[string]any{
		{"cloud_project_key": "proj-a", "branch": "main", "status": "success"},
		{"cloud_project_key": "proj-a", "branch": "develop", "status": "success"},
		{"cloud_project_key": "proj-a", "branch": "feature/dropped", "status": "capped", "branch_limit_exceeded": true},
	})

	got := collectProjectData(common.NewDataStore(dir))

	outcome, ok := got["proj-a"]
	if !ok {
		t.Fatalf("expected an outcome for proj-a")
	}
	if outcome.State != "success" {
		t.Errorf("expected State=success, got %+v", outcome)
	}
}

// #584 — the marker round-trips through attach + parse + render, producing
// the operator-facing line naming the dropped branches. Plural noun for
// multiple branches.
func TestAttachAndRenderBranchLimitSkips_Plural(t *testing.T) {
	items := []EntityItem{{Name: "Proj A", Detail: "proj-a"}}
	attachBranchLimitSkips(items, map[string][]string{"proj-a": {"feature/x", "feature/y"}})

	rendered := successDetails(items[0], false, false, true)
	for _, want := range []string{
		"Branches",
		"feature/x",
		"feature/y",
		"were not migrated: this project has more long-lived branches than the migration's hard limit of 10.",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in:\n%s", want, rendered)
		}
	}
}

// renderBranchLimitSkipLine: singular noun/verb for one branch, empty for
// no payload, and bold-wrapped branch names.
func TestRenderBranchLimitSkipLine(t *testing.T) {
	if got := renderBranchLimitSkipLine(""); got != "" {
		t.Errorf("empty payload: want empty, got %q", got)
	}
	one := renderBranchLimitSkipLine("feature/dropped")
	if !strings.Contains(one, "Branch ") || !strings.Contains(one, " was not migrated") || strings.Contains(one, "Branches") {
		t.Errorf("singular: unexpected wording: %q", one)
	}
	if !strings.Contains(one, inlineBoldStart+"feature/dropped"+inlineBoldEnd) {
		t.Errorf("singular: branch name not bold-wrapped: %q", one)
	}
	many := renderBranchLimitSkipLine("a,b,c")
	if !strings.Contains(many, "Branches ") || !strings.Contains(many, " were not migrated") {
		t.Errorf("plural: unexpected wording: %q", many)
	}
}

// #584 — like the #425 source-purged note, the branch-cap note is NOT
// suppressed in predictive reports.
func TestBranchLimitSkip_RendersInPredictive(t *testing.T) {
	items := []EntityItem{{Name: "Proj A", Detail: "predict:createProjects:org1:proj-a"}}
	attachBranchLimitSkips(items, map[string][]string{"predict:createProjects:org1:proj-a": {"feature/dropped"}})

	rendered := successDetails(items[0], true /* predictive */, false, true)
	if !strings.Contains(rendered, "was not migrated: this project has more long-lived branches") {
		t.Errorf("predictive render missing branch-limit line:\n%s", rendered)
	}
}
