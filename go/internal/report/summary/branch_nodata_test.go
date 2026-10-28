// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"strings"
	"testing"
)

// #565 — wording, singular and plural.
func TestRenderBranchNoDataLine(t *testing.T) {
	if got := renderBranchNoDataLine(""); got != "" {
		t.Errorf("empty payload: want empty, got %q", got)
	}
	one := renderBranchNoDataLine("develop")
	if !strings.HasPrefix(one, "Branch "+inlineBoldStart+"develop"+inlineBoldEnd+" will not be migrated because it was not analyzed for too long and the source code was purged on the source SQS") {
		t.Errorf("singular wording: %q", one)
	}
	many := renderBranchNoDataLine("x,y,z")
	if !strings.HasPrefix(many, "Branches ") || !strings.Contains(many, " will not be migrated because they were not analyzed for too long and the source code was purged on the source SQS") {
		t.Errorf("plural wording: %q", many)
	}
}

// #565 — shown in the predictive and the actual report alike, and the project
// is routed to Near Full Migration rather than Succeeded or Skipped.
func TestBranchNoData_NoteAndNearPerfect(t *testing.T) {
	items := []EntityItem{newProjectItem("proj1", "cloud-1"), newProjectItem("proj2", "cloud-2")}
	failures := []projectFailure{{CloudProjectKey: "cloud-1", Bucket: projectBucketNearPerfect}}
	keep, np, partial := applyProjectFailures(items, nil, nil, failures)
	if len(keep) != 1 || keep[0].Name != "proj2" || len(np) != 1 || np[0].Name != "proj1" || len(partial) != 0 {
		t.Fatalf("routing: keep=%+v np=%+v partial=%+v", keep, np, partial)
	}
	attachBranchNoData(np, map[string][]string{"cloud-1": {"develop"}})
	for _, predictive := range []bool{true, false} {
		rendered := successDetails(np[0], predictive, false, true)
		if !strings.Contains(rendered, "will not be migrated because it was not analyzed for too long") {
			t.Errorf("predictive=%v: missing note:\n%s", predictive, rendered)
		}
	}
}

func TestDegradesProjectData_NoData(t *testing.T) {
	if degradesProjectData(statusNoData) {
		t.Error("no_data must not degrade the project to Partial")
	}
}
