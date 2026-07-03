// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"path/filepath"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// #425 — BuildPredictiveRun flags branches whose source was purged
// (findings present, zero source bytes, but source records exist) by
// emitting importProjectData rows with source_purged=true and
// status=success. Branches that still carry source are not flagged.
func TestSynthesizeBranchSourcePurged(t *testing.T) {
	exportDir := t.TempDir()

	writeFile(t, exportDir, "organizations.csv",
		"sonarqube_org_key,sonarcloud_org_key,server_url\n"+
			"default,target-org,"+testServerURL+"\n")
	writeFile(t, exportDir, "projects.csv",
		"name,key,server_url,sonarqube_org_key\n"+
			"App,com.example:app,"+testServerURL+",default\n")

	extractDir := filepath.Join(exportDir, "extract-0001")
	writeFile(t, extractDir, "extract.json", `{"url":"`+testServerURL+`"}`)

	// Two LONG branches: main (has source) and release-1.0 (purged).
	writeJSONL(t, filepath.Join(extractDir, "getBranches", "b.jsonl"),
		[]map[string]any{
			{"projectKey": "com.example:app", "name": "main", "type": "LONG", "isMain": true},
			{"projectKey": "com.example:app", "name": "release-1.0", "type": "LONG"},
		})
	// Source: main carries text; release-1.0 has a record but it is empty
	// (purged — the extract still wrote a per-file record).
	writeJSONL(t, filepath.Join(extractDir, "getProjectSourceCode", "s.jsonl"),
		[]map[string]any{
			{"projectKey": "com.example:app", "branch": "main", "key": "f1", "source": "a\nb\nc"},
			{"projectKey": "com.example:app", "branch": "release-1.0", "key": "f1", "source": ""},
		})
	// Both branches have a finding.
	writeJSONL(t, filepath.Join(extractDir, "getProjectIssuesFull", "i.jsonl"),
		[]map[string]any{
			{"projectKey": "com.example:app", "branch": "main", "key": "AX1", "status": "OPEN", "rule": "java:S100"},
			{"projectKey": "com.example:app", "branch": "release-1.0", "key": "AX2", "status": "OPEN", "rule": "java:S100"},
		})

	runDir, err := BuildPredictiveRun(exportDir)
	if err != nil {
		t.Fatalf("BuildPredictiveRun: %v", err)
	}

	rows, err := common.NewDataStore(runDir).ReadAll("importProjectData")
	if err != nil {
		t.Fatalf("read importProjectData: %v", err)
	}

	purgedBranches := map[string]bool{}
	for _, r := range rows {
		if common.ExtractField(r, "branch") != "" && common.ExtractBool(r, "source_purged") {
			if common.ExtractField(r, "status") != "success" {
				t.Errorf("purged row status: want success, got %q", common.ExtractField(r, "status"))
			}
			if common.ExtractField(r, "cloud_project_key") == "" {
				t.Errorf("purged row missing cloud_project_key: %s", string(r))
			}
			purgedBranches[common.ExtractField(r, "branch")] = true
		}
	}

	if !purgedBranches["release-1.0"] {
		t.Errorf("expected release-1.0 flagged as source-purged, got %v", purgedBranches)
	}
	if purgedBranches["main"] {
		t.Errorf("main carries source and must NOT be flagged as purged")
	}
}
