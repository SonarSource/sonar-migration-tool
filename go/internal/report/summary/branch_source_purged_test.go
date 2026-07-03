// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// #425 — collectBranchSourcePurged groups purged branches per project,
// de-duplicating repeats and preserving first-seen order. Only rows with
// source_purged=true contribute.
func TestCollectBranchSourcePurged(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "importProjectData", []map[string]any{
		{"cloud_project_key": "proj-a", "branch": "main", "status": "success"},
		{"cloud_project_key": "proj-a", "branch": "release-1.0", "status": "success", "source_purged": true},
		{"cloud_project_key": "proj-a", "branch": "release-2.0", "status": "success", "source_purged": true},
		// duplicate purged row must collapse to a single branch entry.
		{"cloud_project_key": "proj-a", "branch": "release-2.0", "status": "success", "source_purged": true},
		{"cloud_project_key": "proj-b", "branch": "feature/x", "status": "success", "source_purged": true},
		// proj-c has no purged branch — must be absent from the map.
		{"cloud_project_key": "proj-c", "branch": "main", "status": "success"},
	})

	got := collectBranchSourcePurged(common.NewDataStore(dir))

	if len(got["proj-a"]) != 2 || got["proj-a"][0] != "release-1.0" || got["proj-a"][1] != "release-2.0" {
		t.Errorf("proj-a: want [release-1.0 release-2.0], got %v", got["proj-a"])
	}
	if len(got["proj-b"]) != 1 || got["proj-b"][0] != "feature/x" {
		t.Errorf("proj-b: want [feature/x], got %v", got["proj-b"])
	}
	if _, ok := got["proj-c"]; ok {
		t.Errorf("proj-c must not be present, got %v", got["proj-c"])
	}
}

// #425 — the marker round-trips through attach + parse + render, producing
// the operator-facing line with the affected branches named once. Plural
// noun for multiple branches.
func TestAttachAndRenderBranchSourcePurged_Plural(t *testing.T) {
	items := []EntityItem{{Name: "Proj A", Detail: "proj-a"}}
	attachBranchSourcePurged(items, map[string][]string{"proj-a": {"release-1.0", "release-2.0"}})

	rendered := successDetails(items[0], false, false, true)
	for _, want := range []string{
		"Source code of branches",
		"release-1.0",
		"release-2.0",
		"is missing (likely purged in SQS). Migration is executed without the sources.",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in:\n%s", want, rendered)
		}
	}
}

// renderBranchSourcePurgedLine: singular noun for one branch, empty for no
// payload, and bold-wrapped branch names.
func TestRenderBranchSourcePurgedLine(t *testing.T) {
	if got := renderBranchSourcePurgedLine(""); got != "" {
		t.Errorf("empty payload: want empty, got %q", got)
	}
	one := renderBranchSourcePurgedLine("main")
	if !strings.Contains(one, "Source code of branch ") || strings.Contains(one, "branches") {
		t.Errorf("singular: unexpected wording: %q", one)
	}
	if !strings.Contains(one, inlineBoldStart+"main"+inlineBoldEnd) {
		t.Errorf("singular: branch name not bold-wrapped: %q", one)
	}
	many := renderBranchSourcePurgedLine("a,b,c")
	if !strings.Contains(many, "Source code of branches ") {
		t.Errorf("plural: unexpected wording: %q", many)
	}
}

// #425 — the predictive report is NOT suppressed: the purged-source line
// renders in predictive mode too (unlike sync/scan markers).
func TestBranchSourcePurged_RendersInPredictive(t *testing.T) {
	items := []EntityItem{{Name: "Proj A", Detail: "predict:createProjects:org1:proj-a"}}
	attachBranchSourcePurged(items, map[string][]string{"predict:createProjects:org1:proj-a": {"release-1.0"}})

	rendered := successDetails(items[0], true /* predictive */, false, true)
	if !strings.Contains(rendered, "is missing (likely purged in SQS)") {
		t.Errorf("predictive render missing purged line:\n%s", rendered)
	}
}
