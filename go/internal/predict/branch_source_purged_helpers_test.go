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

func TestCollectBranchSourceCounts(t *testing.T) {
	exportDir := t.TempDir()
	extractDir := filepath.Join(exportDir, helperExtractID)
	writeFile(t, extractDir, "extract.json", `{"url":"`+testServerURL+`"}`)
	writeJSONL(t, filepath.Join(extractDir, "getProjectSourceCode", "s.jsonl"), []map[string]any{
		{"projectKey": "p", "branch": "main", "source": "abc"},
		{"projectKey": "p", "branch": "main", "source": "de"},
		{"projectKey": "p", "branch": "old", "source": ""},
	})
	bytes, recs := collectBranchSourceCounts(exportDir, helperMapping())
	main := branchSourcePurgedBranchKey{testServerURL, "p", "main"}
	old := branchSourcePurgedBranchKey{testServerURL, "p", "old"}
	if bytes[main] != 5 || recs[main] != 2 {
		t.Errorf("main: bytes=%d recs=%d", bytes[main], recs[main])
	}
	if bytes[old] != 0 || recs[old] != 1 {
		t.Errorf("old: bytes=%d recs=%d", bytes[old], recs[old])
	}
}

func TestCollectBranchComponentCounts(t *testing.T) {
	exportDir := t.TempDir()
	extractDir := filepath.Join(exportDir, helperExtractID)
	writeFile(t, extractDir, "extract.json", `{"url":"`+testServerURL+`"}`)
	writeJSONL(t, filepath.Join(extractDir, "getProjectComponentTree", "c.jsonl"), []map[string]any{
		{"projectKey": "p", "branch": "main"},
		{"projectKey": "p", "branch": "main"},
		{"projectKey": "p", "branch": "dev"},
	})
	recs, byProject := collectBranchComponentCounts(exportDir, helperMapping())
	if recs[branchSourcePurgedBranchKey{testServerURL, "p", "main"}] != 2 ||
		recs[branchSourcePurgedBranchKey{testServerURL, "p", "dev"}] != 1 {
		t.Errorf("records = %v", recs)
	}
	if byProject[branchSourcePurgedProjectKey{testServerURL, "p"}] != 3 {
		t.Errorf("byProject = %v", byProject)
	}
}

func TestCollectProjectBranches(t *testing.T) {
	exportDir := t.TempDir()
	extractDir := filepath.Join(exportDir, helperExtractID)
	writeFile(t, extractDir, "extract.json", `{"url":"`+testServerURL+`"}`)
	writeJSONL(t, filepath.Join(extractDir, "getBranches", "b.jsonl"), []map[string]any{
		{"projectKey": "p", "name": "main", "type": "LONG", "isMain": true},
		{"projectKey": "p", "name": "rel", "type": "LONG"},
		{"projectKey": "p", "name": "feat", "type": "short"},
		{"projectKey": "p", "name": "", "type": "LONG"},
	})
	branches, mainBranch := collectProjectBranches(exportDir, helperMapping())
	pk := branchSourcePurgedProjectKey{testServerURL, "p"}
	if got := branches[pk]; len(got) != 2 || got[0] != "main" || got[1] != "rel" {
		t.Errorf("branches = %v", got)
	}
	if mainBranch[pk] != "main" {
		t.Errorf("mainBranch = %v", mainBranch)
	}
}

func projectRow(t *testing.T, key, cloud string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"key": key, "server_url": testServerURL, "cloud_project_key": cloud})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assertBranchSourcePurgedRow writes a single branchSourcePurgedRow for
// branch and checks it against wantStatus/wantPurged. wantStatus == ""
// means no row should have been written.
func assertBranchSourcePurgedRow(t *testing.T, pk branchSourcePurgedProjectKey, branch, wantStatus string, wantPurged bool, lookups branchSourcePurgedLookups) {
	t.Helper()
	w, read := newHelperWriter(t, "importProjectData")
	if err := writeBranchSourcePurgedRow(w, pk, branch, "cloud", lookups); err != nil {
		t.Fatal(err)
	}
	rows := read()
	if wantStatus == "" {
		if len(rows) != 0 {
			t.Fatalf("expected no row, got %s", rows)
		}
		return
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if common.ExtractField(rows[0], "branch") != branch || common.ExtractField(rows[0], "cloud_project_key") != "cloud" {
		t.Errorf("row = %s", rows[0])
	}
	if got := common.ExtractField(rows[0], "status"); got != wantStatus {
		t.Errorf("status = %q, want %q", got, wantStatus)
	}
	if common.ExtractBool(rows[0], "source_purged") != wantPurged {
		t.Errorf("source_purged mismatch in %s", rows[0])
	}
}

func TestWriteBranchSourcePurgedRow(t *testing.T) {
	pk := branchSourcePurgedProjectKey{testServerURL, "p"}
	bk := func(b string) branchSourcePurgedBranchKey { return branchSourcePurgedBranchKey{testServerURL, "p", b} }
	lookups := branchSourcePurgedLookups{
		mainBranch:          map[branchSourcePurgedProjectKey]string{pk: "main"},
		componentsByProject: map[branchSourcePurgedProjectKey]int{pk: 3},
		componentRecords:    map[branchSourcePurgedBranchKey]int{bk("main"): 3},
		sourceBytes:         map[branchSourcePurgedBranchKey]int{bk("main"): 10},
		sourceRecords:       map[branchSourcePurgedBranchKey]int{bk("main"): 2, bk("purged"): 1, bk("full"): 1},
	}
	lookups.componentRecords[bk("purged")] = 1
	lookups.componentRecords[bk("full")] = 1
	lookups.sourceBytes[bk("full")] = 7

	cases := []struct {
		branch     string
		wantStatus string // "" = no row
		wantPurged bool
	}{
		{"nodata", "no_data", false},
		{"purged", "success", true},
		{"full", "", false},
		{"main", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.branch, func(t *testing.T) {
			assertBranchSourcePurgedRow(t, pk, tc.branch, tc.wantStatus, tc.wantPurged, lookups)
		})
	}

	// Write errors propagate from both row kinds.
	if err := writeBranchSourcePurgedRow(brokenWriter(t), pk, "nodata", "cloud", lookups); err == nil {
		t.Error("expected error for no-data row")
	}
	if err := writeBranchSourcePurgedRow(brokenWriter(t), pk, "purged", "cloud", lookups); err == nil {
		t.Error("expected error for purged row")
	}
}

func TestWriteBranchSourcePurgedRows(t *testing.T) {
	pk := branchSourcePurgedProjectKey{testServerURL, "p"}
	bk := func(b string) branchSourcePurgedBranchKey { return branchSourcePurgedBranchKey{testServerURL, "p", b} }
	lookups := branchSourcePurgedLookups{
		branchesByProject: map[branchSourcePurgedProjectKey][]string{pk: {"main", "rel"}},
		mainBranch:        map[branchSourcePurgedProjectKey]string{pk: "main"},
		sourceBytes:       map[branchSourcePurgedBranchKey]int{},
		sourceRecords:     map[branchSourcePurgedBranchKey]int{bk("rel"): 1},
	}
	projects := []json.RawMessage{
		projectRow(t, "p", "cloud-p"),
		projectRow(t, "nocloud", ""),
		projectRow(t, "", "cloud-x"),
		projectRow(t, "q", "cloud-q"), // no branches recorded -> falls back to "main"
	}
	w, read := newHelperWriter(t, "importProjectData")
	if err := writeBranchSourcePurgedRows(w, projects, lookups); err != nil {
		t.Fatal(err)
	}
	rows := read()
	if len(rows) != 1 || common.ExtractField(rows[0], "branch") != "rel" || common.ExtractField(rows[0], "cloud_project_key") != "cloud-p" {
		t.Fatalf("rows = %s", rows)
	}

	if err := writeBranchSourcePurgedRows(brokenWriter(t), projects, lookups); err == nil {
		t.Error("expected write error to propagate")
	}
}

func TestWriteJSONRow(t *testing.T) {
	w, read := newHelperWriter(t, "t")
	if err := writeJSONRow(w, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if n := len(read()); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}

	// Unmarshalable value: silently skipped, nil returned, nothing written.
	w2, read2 := newHelperWriter(t, "t")
	if err := writeJSONRow(w2, map[string]any{"bad": make(chan int)}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if n := len(read2()); n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}

	// WriteOne error propagates.
	if err := writeJSONRow(brokenWriter(t), map[string]any{"a": 1}); err == nil {
		t.Error("expected WriteOne error")
	}
}
