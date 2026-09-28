// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"encoding/json"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// projectOrgsFromRun reads a synthesized task's JSONL and returns
// project key → sonarcloud_org_key.
func projectOrgsFromRun(t *testing.T, runDir, task string) map[string]string {
	t.Helper()
	items, err := common.NewDataStore(runDir).ReadAll(task)
	if err != nil {
		t.Fatalf("reading %s: %v", task, err)
	}
	out := make(map[string]string, len(items))
	for _, raw := range items {
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatalf("unmarshalling %s row: %v", task, err)
		}
		key, _ := row["key"].(string)
		org, _ := row["sonarcloud_org_key"].(string)
		out[key] = org
	}
	return out
}

// Issue #612: the predictive report must land a project in the same
// organization a real migrate would, per-project override included —
// otherwise it predicts the wrong destination for every dispatched
// project, and reports one as Skipped whenever its source org is
// unmapped but its override is filled.
func TestPredictiveRunHonoursProjectOrgOverride(t *testing.T) {
	exportDir := setupPredictiveFixture(t)

	// "skipme" stays unmapped in organizations.csv, so the second
	// project can only migrate through its override.
	writeFile(t, exportDir, "projects.csv",
		"name,key,sonarcloud_org_key,server_url,sonarqube_org_key,alm,is_cloud_binding\n"+
			"App,com.example:app,other-org,"+testServerURL+",default,,false\n"+
			"Dispatched,com.example:dispatched,dispatch-org,"+testServerURL+",skipme,,false\n"+
			"Plain,com.example:plain,,"+testServerURL+",default,,false\n")

	runDir, err := BuildPredictiveRun(exportDir)
	if err != nil {
		t.Fatalf("BuildPredictiveRun: %v", err)
	}

	got := projectOrgsFromRun(t, runDir, "generateProjectMappings")
	want := map[string]string{
		"com.example:app":        "other-org",
		"com.example:dispatched": "dispatch-org",
		"com.example:plain":      "target-org",
	}
	for key, wantOrg := range want {
		if got[key] != wantOrg {
			t.Errorf("generateProjectMappings: %q → org %q, want %q", key, got[key], wantOrg)
		}
	}

	// The create* synthesis is what the report actually counts, and an
	// overridden project must no longer be dropped as org-skipped.
	created := projectOrgsFromRun(t, runDir, "createProjects")
	if len(created) != 3 {
		t.Errorf("createProjects synthesized %d projects, want 3: %v", len(created), created)
	}
	if created["com.example:dispatched"] != "dispatch-org" {
		t.Errorf("createProjects: dispatched project → org %q, want %q",
			created["com.example:dispatched"], "dispatch-org")
	}
}

// A cloud-bound project keeps its binding-derived organization in the
// prediction too, so the report never promises a destination migrate
// would refuse.
func TestPredictiveRunRefusesOverrideOnBoundProject(t *testing.T) {
	exportDir := setupPredictiveFixture(t)

	writeFile(t, exportDir, "projects.csv",
		"name,key,sonarcloud_org_key,server_url,sonarqube_org_key,alm,is_cloud_binding\n"+
			"Bound,com.example:bound,sneaky-org,"+testServerURL+",default,gitlab,true\n")

	runDir, err := BuildPredictiveRun(exportDir)
	if err != nil {
		t.Fatalf("BuildPredictiveRun: %v", err)
	}

	got := projectOrgsFromRun(t, runDir, "generateProjectMappings")
	if got["com.example:bound"] != "target-org" {
		t.Errorf("bound project → org %q, want %q", got["com.example:bound"], "target-org")
	}
}
