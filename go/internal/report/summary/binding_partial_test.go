// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// writeBindingRecords seeds a task output directory with JSONL records.
func writeBindingRecords(t *testing.T, dir, task string, recs ...map[string]any) {
	t.Helper()
	taskDir := filepath.Join(dir, task)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(taskDir, "results.1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCollectProjectBindingOutcomes_OrgNotBound is the issue #122
// acceptance test: a source project that WAS bound on SonarQube Server,
// migrated into a target organization that is NOT bound to a DevOps
// platform, must be reported as a partial migration whose Details column
// explains that the binding failed because the org itself is not bound.
func TestCollectProjectBindingOutcomes_OrgNotBound(t *testing.T) {
	dir := t.TempDir()
	writeBindingRecords(t, dir, "setProjectBinding", map[string]any{
		"cloud_project_key":  "cloud-1",
		"sonarcloud_org_key": "org1",
		"binding_skipped":    true,
		"skip_reason":        "org_not_bound",
		"skip_detail":        "project binding was not possible because the org itself is not bound",
		"alm":                "gitlab",
	})

	failures := collectProjectBindingOutcomes(common.NewDataStore(dir))
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d: %+v", len(failures), failures)
	}
	f := failures[0]
	if f.Bucket != projectBucketPartial {
		t.Errorf("bucket = %v, want Partial", f.Bucket)
	}
	const want = "project binding was not possible because the org itself is not bound"
	if f.Operation != want {
		t.Errorf("operation = %q, want %q", f.Operation, want)
	}

	// End-to-end through the report router: the project must move out of
	// Succeeded into Partial, carrying the explanation as an Issues line.
	succeeded := []EntityItem{{Name: "proj1", Organization: "org1", Detail: "cloud-1"}}
	keep, np, partial := applyProjectFailures(succeeded, nil, nil, failures)
	if len(keep) != 0 {
		t.Errorf("expected the project to leave Succeeded, got %+v", keep)
	}
	if len(np) != 0 {
		t.Errorf("expected nothing in NearPerfect, got %+v", np)
	}
	if len(partial) != 1 || partial[0].Name != "proj1" {
		t.Fatalf("expected proj1 in Partial, got %+v", partial)
	}
	if len(partial[0].Issues) != 1 || partial[0].Issues[0] != want {
		t.Fatalf("Issues = %v, want [%q]", partial[0].Issues, want)
	}

	// And it renders into the Details column verbatim, under the head line.
	details := partialDetails(partial[0], false, false, true)
	if !strings.Contains(details, want) {
		t.Errorf("Details column %q does not contain %q", details, want)
	}

	// The row's Outcome label is the report's partial-migration status.
	rows := buildUnifiedRows(Section{Name: "Projects", Partial: partial}, false)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].outcome != outcomePartial {
		t.Errorf("outcome = %q, want %q", rows[0].outcome, outcomePartial)
	}
	if !strings.Contains(rows[0].details, want) {
		t.Errorf("row details %q does not contain %q", rows[0].details, want)
	}
}

// TestCollectProjectBindingOutcomes_OrgBindingLookupFailed is the issue
// #505 report-honesty test. When the target org's DevOps binding could
// not be READ, the Details column must say exactly that — quoting the API
// error — and must NOT claim the org is not bound, which the tool never
// observed.
func TestCollectProjectBindingOutcomes_OrgBindingLookupFailed(t *testing.T) {
	dir := t.TempDir()
	const apiErr = "HTTP 502 GET https://sonarcloud.io/api/alm_integration/" +
		"show_bound_organization?organization=org1 - Bad gateway"
	writeBindingRecords(t, dir, "setProjectBinding", map[string]any{
		"cloud_project_key":  "cloud-1",
		"sonarcloud_org_key": "org1",
		"binding_skipped":    true,
		"skip_reason":        "org_binding_unknown",
		"skip_detail": "project binding was not possible because the target " +
			"organization's DevOps platform binding could not be read",
		"skip_error": apiErr,
		"alm":        "github",
	})

	failures := collectProjectBindingOutcomes(common.NewDataStore(dir))
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d: %+v", len(failures), failures)
	}
	const want = "project binding was not possible because the target " +
		"organization's DevOps platform binding could not be read"
	if failures[0].Operation != want {
		t.Errorf("operation = %q, want %q", failures[0].Operation, want)
	}
	if failures[0].Error != apiErr {
		t.Errorf("error = %q, want the API error %q", failures[0].Error, apiErr)
	}
	if failures[0].Bucket != projectBucketPartial {
		t.Errorf("bucket = %v, want Partial", failures[0].Bucket)
	}

	// End to end: the project lands in Partial with the honest sentence
	// and the API error, and never the org-not-bound claim.
	succeeded := []EntityItem{{Name: "proj1", Organization: "org1", Detail: "cloud-1"}}
	_, _, partial := applyProjectFailures(succeeded, nil, nil, failures)
	if len(partial) != 1 {
		t.Fatalf("expected proj1 in Partial, got %+v", partial)
	}
	details := partialDetails(partial[0], false, false, true)
	if !strings.Contains(details, want) {
		t.Errorf("Details column %q does not contain %q", details, want)
	}
	if !strings.Contains(details, apiErr) {
		t.Errorf("Details column %q does not quote the API error %q", details, apiErr)
	}
	if strings.Contains(details, "the org itself is not bound") {
		t.Errorf("Details column must not claim the org is not bound: %q", details)
	}
}

// TestCollectProjectBindingOutcomes_ReposLookupFailed is the sibling of
// the above for the repository listing (issue #505): a listing that never
// succeeded must not be reported as "the repository was not found".
func TestCollectProjectBindingOutcomes_ReposLookupFailed(t *testing.T) {
	dir := t.TempDir()
	const apiErr = "HTTP 503 GET https://sonarcloud.io/api/alm_integration/" +
		"list_repositories?organization=org1 - Service Unavailable"
	writeBindingRecords(t, dir, "matchProjectRepos", map[string]any{
		"cloud_project_key": "cloud-1", "binding_skipped": true,
		"skip_reason": "repos_unknown", "skip_error": apiErr,
	})

	failures := collectProjectBindingOutcomes(common.NewDataStore(dir))
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %+v", failures)
	}
	want := "project binding was not possible because the repositories of the " +
		"bound DevOps organization could not be listed"
	if failures[0].Operation != want {
		t.Errorf("operation = %q, want %q", failures[0].Operation, want)
	}
	if failures[0].Error != apiErr {
		t.Errorf("error = %q, want the API error %q", failures[0].Error, apiErr)
	}
}

// TestCollectProjectBindingOutcomes_SuccessAndFailure verifies that a
// successfully created binding produces no report entry, while a rejected
// binding write is surfaced as a partial migration with the API error.
func TestCollectProjectBindingOutcomes_SuccessAndFailure(t *testing.T) {
	dir := t.TempDir()
	writeBindingRecords(t, dir, "setProjectBinding",
		map[string]any{
			"cloud_project_key": "cloud-ok", "status": "success",
			"repository_id": "Acme/repo", "alm": "github",
		},
		map[string]any{
			"cloud_project_key": "cloud-bad", "status": "failed",
			"error": "HTTP 400 - repository not found", "alm": "github",
		},
	)

	failures := collectProjectBindingOutcomes(common.NewDataStore(dir))
	if len(failures) != 1 {
		t.Fatalf("expected only the failed binding, got %+v", failures)
	}
	if failures[0].CloudProjectKey != "cloud-bad" {
		t.Errorf("key = %q, want cloud-bad", failures[0].CloudProjectKey)
	}
	if failures[0].Bucket != projectBucketPartial {
		t.Errorf("bucket = %v, want Partial", failures[0].Bucket)
	}
	if failures[0].Error != "HTTP 400 - repository not found" {
		t.Errorf("error = %q", failures[0].Error)
	}
}

// TestCollectProjectBindingOutcomes_FallsBackToMatchTask covers the case
// where the binding write task never ran (for example the run stopped
// early): the skip records written by matchProjectRepos must still be
// reported.
func TestCollectProjectBindingOutcomes_FallsBackToMatchTask(t *testing.T) {
	dir := t.TempDir()
	writeBindingRecords(t, dir, "matchProjectRepos", map[string]any{
		"cloud_project_key": "cloud-1", "binding_skipped": true,
		"skip_reason": "repo_not_found",
	})

	failures := collectProjectBindingOutcomes(common.NewDataStore(dir))
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %+v", failures)
	}
	want := "project binding was not possible because the repository was not found in the bound DevOps organization"
	if failures[0].Operation != want {
		t.Errorf("operation = %q, want %q", failures[0].Operation, want)
	}
}

// TestCollectProjectBindingOutcomes_NoRecords: a run where no project was
// bound on the source produces no report entries at all.
func TestCollectProjectBindingOutcomes_NoRecords(t *testing.T) {
	if got := collectProjectBindingOutcomes(common.NewDataStore(t.TempDir())); len(got) != 0 {
		t.Errorf("expected no failures, got %+v", got)
	}
}
