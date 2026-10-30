// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

func TestNCDOverrideRecord(t *testing.T) {
	seenFallback := map[string]bool{}
	seenBranch := map[string]bool{}
	rec := func(data string, slbHint bool) map[string]any {
		return ncdOverrideRecord(json.RawMessage(data), "cloud", slbHint, seenFallback, seenBranch)
	}

	// A project-level type SonarQube Cloud supports needs no record.
	if got := rec(`{"type":"NUMBER_OF_DAYS"}`, false); got != nil {
		t.Errorf("supported project-level type: got %v, want nil", got)
	}
	// An unsupported one is flagged once.
	got := rec(`{"type":"REFERENCE_BRANCH"}`, true)
	if got == nil || got["ncd_fallback"] != true || got["slb_hint"] != true || got["source_ncd_type"] != "REFERENCE_BRANCH" {
		t.Errorf("unsupported project-level type: got %v", got)
	}
	if got := rec(`{"type":"REFERENCE_BRANCH"}`, true); got != nil {
		t.Errorf("second project-level record must be dropped, got %v", got)
	}

	// A branch that inherits the project value needs no record.
	if got := rec(`{"branchKey":"dev","type":"PREVIOUS_VERSION","inherited":true}`, false); got != nil {
		t.Errorf("inherited branch: got %v, want nil", got)
	}
	// An explicit branch override is flagged once per project.
	got = rec(`{"branchKey":"dev","type":"PREVIOUS_VERSION","inherited":false}`, false)
	if got == nil || got["ncd_branch_override"] != true || got["branch"] != "dev" || got["slb_hint"] != false {
		t.Errorf("branch override: got %v", got)
	}
	if got := rec(`{"branchKey":"rel","type":"PREVIOUS_VERSION","inherited":false}`, false); got != nil {
		t.Errorf("second branch override must be dropped, got %v", got)
	}
}

func TestSynthesizeSetNewCodePeriods(t *testing.T) {
	exportDir, runDir := t.TempDir(), t.TempDir()
	const server = "https://sq.example"
	writeJSONL(t, filepath.Join(exportDir, "ext1", "getNewCodePeriods", "results.1.jsonl"), []map[string]any{
		{"projectKey": "ref", "type": "REFERENCE_BRANCH"},
		{"projectKey": "ref", "branchKey": "dev", "type": "NUMBER_OF_DAYS", "inherited": false},
		{"projectKey": "plain", "type": "NUMBER_OF_DAYS"},
		{"projectKey": "unmapped", "type": "REFERENCE_BRANCH"},
	})
	mapping := structure.ExtractMapping{server: {"ext1"}}

	// No createProjects rows yet: nothing to join, nothing written.
	if err := synthesizeSetNewCodePeriods(exportDir, runDir, mapping, false); err != nil {
		t.Fatal(err)
	}

	store := common.NewDataStore(runDir)
	w, err := store.Writer("createProjects")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []map[string]any{
		{"key": "ref", "server_url": server, "cloud_project_key": "org_ref"},
		{"key": "plain", "server_url": server, "cloud_project_key": "org_plain"},
	} {
		b, _ := json.Marshal(p)
		if err := w.WriteOne(b); err != nil {
			t.Fatal(err)
		}
	}

	for _, useSLB := range []bool{false, true} {
		if err := synthesizeSetNewCodePeriods(exportDir, runDir, mapping, useSLB); err != nil {
			t.Fatal(err)
		}
		rows, err := store.ReadAll("setNewCodePeriods")
		if err != nil {
			t.Fatal(err)
		}
		// ref: project-level fallback + branch override; plain: supported type; unmapped: skipped.
		if len(rows) != 2 {
			t.Fatalf("useSLB=%v: got %d records, want 2: %s", useSLB, len(rows), rows)
		}
		for _, r := range rows {
			if jsonStringField(r, "cloud_project_key") != "org_ref" {
				t.Errorf("unexpected project in %s", r)
			}
			if got := jsonBoolField(r, "slb_hint"); got == useSLB {
				t.Errorf("useSLB=%v: slb_hint=%v in %s", useSLB, got, r)
			}
		}
		if err := os.RemoveAll(filepath.Join(runDir, "setNewCodePeriods")); err != nil {
			t.Fatal(err)
		}
	}
}
