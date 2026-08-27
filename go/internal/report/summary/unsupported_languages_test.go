// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// #474 — a Compute Engine rejection caused by a file whose language has no
// target quality profile must be explained, not framed as a generic API error.
// The old wording ("API error when migrating project data: CE task failed: ...")
// gave the operator nothing to act on.
func TestProjectDataFailureReasonUnsupportedLanguage(t *testing.T) {
	got := projectDataFailureReason(
		"main branch CE failed for org_proj: CE task failed: Report contains a file with " +
			"language 'lua' but no matching quality profile")

	for _, want := range []string{
		"No issues or branches were migrated",
		"'lua'",
		"no quality profile in the target organization",
		"3rd-party SonarQube Server plugin",
		"--unsupported_languages=exclude",
		"--unsupported_languages=skip",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reason missing %q\ngot: %s", want, got)
		}
	}
	if strings.Contains(got, "API error") {
		t.Errorf("must not frame a report rejection as an API error\ngot: %s", got)
	}
}

// Unrelated failures must keep the existing framing so #474 does not change
// how every other project-data failure reads.
func TestProjectDataFailureReasonUnchangedForOtherErrors(t *testing.T) {
	got := projectDataFailureReason("CE task failed: HTTP 500")
	if want := "API error when migrating project data: CE task failed: HTTP 500"; got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
	if got := projectDataFailureReason(""); got != "API error when migrating project data" {
		t.Errorf("empty error reason = %q", got)
	}
}

// Projects whose report had unsupported-language files excluded import
// successfully, so without this collector they would be reported as full
// migrations despite having lost files and every issue on them.
func TestCollectUnsupportedLanguageExclusions(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "importProjectData", []map[string]any{
		// Two branches of the same project drop the SAME file — the
		// per-project count must be the worst branch, not the sum.
		{"cloud_project_key": "org_excluded", "branch": "main", "status": "success",
			"unsupported_languages": []string{"lua"}, "excluded_files": 1},
		{"cloud_project_key": "org_excluded", "branch": "develop", "status": "success",
			"unsupported_languages": []string{"lua"}, "excluded_files": 1},
		// Multiple languages across branches must be unioned and sorted.
		{"cloud_project_key": "org_multi", "branch": "main", "status": "success",
			"unsupported_languages": []string{"lua"}, "excluded_files": 3},
		{"cloud_project_key": "org_multi", "branch": "develop", "status": "success",
			"unsupported_languages": []string{"delphi", "lua"}, "excluded_files": 5},
		// A clean project must not be routed anywhere.
		{"cloud_project_key": "org_clean", "branch": "main", "status": "success"},
		// A project skipped by --unsupported_languages=skip excluded nothing;
		// the |scan: marker already carries its reason.
		{"cloud_project_key": "org_skipped", "branch": "main", "status": "skipped",
			"unsupported_languages": []string{"lua"}, "excluded_files": 0,
			"error": "Project data not migrated: ..."},
	})

	got := collectUnsupportedLanguageExclusions(common.NewDataStore(dir))

	if len(got) != 2 {
		t.Fatalf("expected 2 routed projects, got %d: %+v", len(got), got)
	}
	byKey := map[string]projectFailure{}
	for _, f := range got {
		byKey[f.CloudProjectKey] = f
	}

	excluded, ok := byKey["org_excluded"]
	if !ok {
		t.Fatal("org_excluded not routed")
	}
	if excluded.Bucket != projectBucketPartial {
		t.Errorf("bucket = %v, want Partial", excluded.Bucket)
	}
	if want := "lua (1 file)"; excluded.Detail != want {
		t.Errorf("detail = %q, want %q (worst branch, not the sum)", excluded.Detail, want)
	}
	if excluded.Operation == "" {
		t.Error("Operation must be set so the report renders an Issues line")
	}
	if !strings.Contains(excluded.Error, "3rd-party SonarQube Server plugins") {
		t.Errorf("Error must explain the cause, got %q", excluded.Error)
	}

	multi := byKey["org_multi"]
	if want := "delphi, lua (5 files)"; multi.Detail != want {
		t.Errorf("multi detail = %q, want %q", multi.Detail, want)
	}

	if _, routed := byKey["org_clean"]; routed {
		t.Error("a project with no exclusions must not be routed")
	}
	if _, routed := byKey["org_skipped"]; routed {
		t.Error("a skipped project excluded no files and must not be routed here")
	}
}

func TestCollectUnsupportedLanguageExclusionsNoData(t *testing.T) {
	if got := collectUnsupportedLanguageExclusions(common.NewDataStore(t.TempDir())); got != nil {
		t.Errorf("expected nil with no importProjectData, got %+v", got)
	}
}

func TestJSONStrSlice(t *testing.T) {
	raw := []byte(`{"langs":["lua","delphi"],"n":3,"s":"x"}`)
	if got := jsonStrSlice(raw, "langs"); strings.Join(got, ",") != "lua,delphi" {
		t.Errorf("got %v", got)
	}
	if got := jsonStrSlice(raw, "missing"); got != nil {
		t.Errorf("missing key = %v, want nil", got)
	}
	// Wrong type must degrade to nil rather than panicking.
	if got := jsonStrSlice(raw, "n"); got != nil {
		t.Errorf("non-array = %v, want nil", got)
	}
	if got := jsonStrSlice([]byte(`not json`), "langs"); got != nil {
		t.Errorf("invalid json = %v, want nil", got)
	}
}
