// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// #527: the predictive pipeline must apply the same sync-eligibility
// rule as real migrate (migrate.HotspotSyncEligibility) — a hotspot is
// actionable only if it is not TO_REVIEW, carries a user (non-technical)
// comment, or is ACKNOWLEDGED (inventoried but never state-synced).
func TestSynthesizeSyncHotspotMetadataEligibility(t *testing.T) {
	exportDir := t.TempDir()

	writeFile(t, exportDir, "organizations.csv",
		"sonarqube_org_key,sonarcloud_org_key,server_url\n"+
			"default,target-org,"+testServerURL+"\n")
	writeFile(t, exportDir, "projects.csv",
		"name,key,server_url,sonarqube_org_key\n"+
			"App,com.example:app,"+testServerURL+",default\n")
	writeFile(t, exportDir, "gates.csv",
		"name,server_url,source_gate_key,is_default,sonarqube_org_key\n")

	extractDir := filepath.Join(exportDir, "extract-0001")
	writeFile(t, extractDir, "extract.json", `{"url":"`+testServerURL+`"}`)

	writeJSONL(t, filepath.Join(extractDir, "getProjectHotspotsFull", "hotspots.jsonl"),
		[]map[string]any{
			// Excluded: TO_REVIEW, no comment at all.
			{"key": "h1", "project": "com.example:app", "status": "TO_REVIEW"},
			// Excluded: TO_REVIEW, only a technical (blank-login) comment.
			{"key": "h2", "project": "com.example:app", "status": "TO_REVIEW",
				"comment": []map[string]any{{"login": "", "markdown": "auto"}}},
			// Eligible: TO_REVIEW, but a real user commented on it.
			{"key": "h3", "project": "com.example:app", "status": "TO_REVIEW",
				"comment": []map[string]any{{"login": "alice", "markdown": "still worth a look"}}},
			// Eligible: REVIEWED+SAFE.
			{"key": "h4", "project": "com.example:app", "status": "REVIEWED", "resolution": "SAFE"},
			// Acknowledged: never state-synced, but inventoried.
			{"key": "h5", "project": "com.example:app", "status": "REVIEWED", "resolution": "ACKNOWLEDGED"},
			// Acknowledged, with a user comment — still just "acknowledged"
			// for the actionable/synced counts (comment sync isn't tracked
			// as a separate predictive count).
			{"key": "h6", "project": "com.example:app", "status": "REVIEWED", "resolution": "ACKNOWLEDGED",
				"comment": []map[string]any{{"login": "bob", "markdown": "seen, accepted"}}},
		})

	runDir, err := BuildPredictiveRun(exportDir)
	if err != nil {
		t.Fatalf("BuildPredictiveRun: %v", err)
	}

	rows, err := common.NewDataStore(runDir).ReadAll("syncHotspotMetadata")
	if err != nil {
		t.Fatalf("read syncHotspotMetadata: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 syncHotspotMetadata record, got %d: %s", len(rows), rows)
	}

	var rec struct {
		Synced              int `json:"synced"`
		Actionable          int `json:"actionable"`
		AcknowledgedDemoted int `json:"acknowledged_demoted"`
	}
	if err := json.Unmarshal(rows[0], &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// eligible = h3, h4 -> 2 ; acknowledged = h5, h6 -> 2.
	if rec.Synced != 2 {
		t.Errorf("synced = %d, want 2 (eligible, non-ACK hotspots)", rec.Synced)
	}
	if rec.AcknowledgedDemoted != 2 {
		t.Errorf("acknowledged_demoted = %d, want 2", rec.AcknowledgedDemoted)
	}
	if rec.Actionable != 4 {
		t.Errorf("actionable = %d, want 4 (eligible + acknowledged); h1/h2 must be excluded", rec.Actionable)
	}
}

// #527: jsonHasUserComment mirrors migrate.hotspotHasUserComment for the
// raw-JSON extract shape — a comment counts only when its login is
// non-empty.
func TestJsonHasUserComment(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"no comment field", `{}`, false},
		{"empty array", `{"comment":[]}`, false},
		{"blank login only", `{"comment":[{"login":"","markdown":"auto"}]}`, false},
		{"real login", `{"comment":[{"login":"alice","markdown":"hi"}]}`, true},
		{"mixed", `{"comment":[{"login":""},{"login":"bob"}]}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonHasUserComment(json.RawMessage(tc.raw)); got != tc.want {
				t.Errorf("jsonHasUserComment(%s) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
