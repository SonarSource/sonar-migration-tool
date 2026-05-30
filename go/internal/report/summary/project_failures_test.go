package summary

import (
	"reflect"
	"testing"
)

func newProjectItem(name, cloudKey string) EntityItem {
	return EntityItem{Name: name, Detail: cloudKey}
}

func TestApplyProjectFailures_TagsYellow(t *testing.T) {
	succeeded := []EntityItem{
		newProjectItem("proj1", "cloud-1"),
		newProjectItem("proj2", "cloud-2"),
	}
	failures := []projectFailure{
		{CloudProjectKey: "cloud-1", Bucket: projectBucketNearPerfect,
			Operation: "Project tags not migrated", Detail: "tags: java,backend"},
	}
	keep, np, partial := applyProjectFailures(succeeded, nil, nil, failures)
	if len(keep) != 1 || keep[0].Name != "proj2" {
		t.Errorf("expected proj2 to stay in Succeeded, got %+v", keep)
	}
	if len(np) != 1 || np[0].Name != "proj1" {
		t.Fatalf("expected proj1 in NearPerfect, got %+v", np)
	}
	if len(partial) != 0 {
		t.Errorf("expected no partial, got %+v", partial)
	}
	if !reflect.DeepEqual(np[0].Issues, []string{"Project tags not migrated: tags: java,backend"}) {
		t.Errorf("issues: %v", np[0].Issues)
	}
}

func TestApplyProjectFailures_GroupPermsOrange(t *testing.T) {
	succeeded := []EntityItem{newProjectItem("proj1", "cloud-1")}
	failures := []projectFailure{
		{CloudProjectKey: "cloud-1", Bucket: projectBucketPartial,
			Operation: "Group permission not migrated", Detail: "developers → admin"},
	}
	_, _, partial := applyProjectFailures(succeeded, nil, nil, failures)
	if len(partial) != 1 || partial[0].Name != "proj1" {
		t.Fatalf("expected proj1 in Partial, got %+v", partial)
	}
}

// Orange dominates yellow: a project that hit both kinds of failures
// lands in Partial.
func TestApplyProjectFailures_OrangeDominatesYellow(t *testing.T) {
	succeeded := []EntityItem{newProjectItem("proj1", "cloud-1")}
	failures := []projectFailure{
		{CloudProjectKey: "cloud-1", Bucket: projectBucketNearPerfect,
			Operation: "Project tags not migrated", Detail: "tags: x"},
		{CloudProjectKey: "cloud-1", Bucket: projectBucketPartial,
			Operation: "Group permission not migrated", Detail: "g → user"},
	}
	_, np, partial := applyProjectFailures(succeeded, nil, nil, failures)
	if len(np) != 0 {
		t.Errorf("expected no NearPerfect, got %+v", np)
	}
	if len(partial) != 1 {
		t.Fatalf("expected proj1 in Partial, got %+v", partial)
	}
	// Both Issues lines should be carried over.
	if len(partial[0].Issues) != 2 {
		t.Errorf("expected 2 issues, got %v", partial[0].Issues)
	}
}

// Details for the same operation are deduplicated and joined.
func TestApplyProjectFailures_DedupAndJoinDetails(t *testing.T) {
	succeeded := []EntityItem{newProjectItem("proj1", "cloud-1")}
	failures := []projectFailure{
		{CloudProjectKey: "cloud-1", Bucket: projectBucketNearPerfect,
			Operation: "Project setting not migrated", Detail: "sonar.foo = bar"},
		{CloudProjectKey: "cloud-1", Bucket: projectBucketNearPerfect,
			Operation: "Project setting not migrated", Detail: "sonar.foo = bar"}, // dup
		{CloudProjectKey: "cloud-1", Bucket: projectBucketNearPerfect,
			Operation: "Project setting not migrated", Detail: "sonar.baz = qux"},
	}
	_, np, _ := applyProjectFailures(succeeded, nil, nil, failures)
	if len(np) != 1 {
		t.Fatalf("expected 1 NearPerfect entry, got %+v", np)
	}
	want := "Project setting not migrated: sonar.baz = qux, sonar.foo = bar"
	if np[0].Issues[0] != want {
		t.Errorf("Issues[0]: got %q, want %q", np[0].Issues[0], want)
	}
}

// Project keys carry a |scan: suffix when the project has scan history
// (#240). The matcher must look at the bare cloud key.
func TestApplyProjectFailures_StripsScanSuffix(t *testing.T) {
	succeeded := []EntityItem{newProjectItem("proj1", "cloud-1|scan:OK")}
	failures := []projectFailure{
		{CloudProjectKey: "cloud-1", Bucket: projectBucketNearPerfect,
			Operation: "Project tags not migrated", Detail: "tags: x"},
	}
	keep, np, _ := applyProjectFailures(succeeded, nil, nil, failures)
	if len(keep) != 0 {
		t.Errorf("expected proj1 to leave Succeeded, got %+v", keep)
	}
	if len(np) != 1 {
		t.Errorf("expected proj1 in NearPerfect, got %+v", np)
	}
}

func TestClassifyProjectFailure_TagsSet(t *testing.T) {
	entry := map[string]any{
		"process_type": "request_completed",
		"status":       "failure",
		"payload": map[string]any{
			"method": "POST",
			"url":    "https://sc/api/project_tags/set",
			"status": float64(400),
			"data":   map[string]any{"project": "cloud-1", "tags": "java,backend"},
			"response": map[string]any{
				"errors": []any{map[string]any{"msg": "Project not found"}},
			},
		},
	}
	pf, ok := classifyProjectFailure(entry)
	if !ok {
		t.Fatal("expected match")
	}
	if pf.CloudProjectKey != "cloud-1" {
		t.Errorf("project: %q", pf.CloudProjectKey)
	}
	if pf.Bucket != projectBucketNearPerfect {
		t.Errorf("bucket: %v", pf.Bucket)
	}
	if pf.Detail != "tags: java,backend" {
		t.Errorf("detail: %q", pf.Detail)
	}
	if pf.Error != "Project not found" {
		t.Errorf("error: %q", pf.Error)
	}
}

func TestClassifyProjectFailure_AddGroup(t *testing.T) {
	entry := map[string]any{
		"process_type": "request_completed",
		"status":       "failure",
		"payload": map[string]any{
			"method": "POST",
			"url":    "https://sc/api/permissions/add_group",
			"status": float64(400),
			"data": map[string]any{
				"projectKey": "cloud-1", "groupName": "developers", "permission": "admin",
			},
		},
	}
	pf, ok := classifyProjectFailure(entry)
	if !ok {
		t.Fatal("expected match")
	}
	if pf.Bucket != projectBucketPartial {
		t.Errorf("bucket: %v", pf.Bucket)
	}
	if pf.Detail != "developers → admin" {
		t.Errorf("detail: %q", pf.Detail)
	}
}

func TestClassifyProjectFailure_IgnoresNonProjectEndpoints(t *testing.T) {
	entry := map[string]any{
		"process_type": "request_completed",
		"status":       "failure",
		"payload": map[string]any{
			"method": "POST",
			"url":    "https://sc/api/qualitygates/create_condition",
			"status": float64(400),
			"data":   map[string]any{},
		},
	}
	if _, ok := classifyProjectFailure(entry); ok {
		t.Error("non-project endpoint must not match")
	}
}
