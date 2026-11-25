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

const helperExtractID = "2026-08-20-0001"

// helperMapping builds a one-extract mapping matching helperExtractID.
func helperMapping() structure.ExtractMapping {
	return structure.ExtractMapping{testServerURL: {helperExtractID}}
}

// newHelperWriter returns a ChunkWriter plus a reader of everything it wrote.
func newHelperWriter(t *testing.T, task string) (*common.ChunkWriter, func() []json.RawMessage) {
	t.Helper()
	store := common.NewDataStore(t.TempDir())
	w, err := store.Writer(task)
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	return w, func() []json.RawMessage {
		rows, err := store.ReadAll(task)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		return rows
	}
}

func rawItem(t *testing.T, v map[string]any) structure.ExtractItem {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return structure.ExtractItem{ServerURL: testServerURL, Data: b}
}

func TestProcessCustomizedSetting(t *testing.T) {
	defaults := map[string]string{"custom.key": "def", "same.key": "def"}
	orgs := []string{"org"}

	cases := []struct {
		name          string
		item          map[string]any
		seen          map[string]bool
		wantAuth      bool
		wantRows      int
		wantSeenKey   string
		wantStatus    string
		wantNotInSeen string
	}{
		{name: "empty key", item: map[string]any{"value": "x"}, wantRows: 0},
		{name: "already seen", item: map[string]any{"key": "custom.key", "value": "x"}, seen: map[string]bool{"custom.key": true}, wantRows: 0},
		{name: "internal", item: map[string]any{"key": "sonar.core.id", "value": "x"}, wantRows: 0, wantNotInSeen: "sonar.core.id"},
		{name: "not customized", item: map[string]any{"key": "same.key", "value": "def"}, wantRows: 0, wantNotInSeen: "same.key"},
		{name: "auth with value", item: map[string]any{"key": "sonar.auth.github.enabled", "value": "true"}, wantAuth: true, wantRows: 0, wantSeenKey: "sonar.auth.github.enabled"},
		{name: "dbcleaner", item: map[string]any{"key": "sonar.dbcleaner.branchesToKeepWhenInactive", "values": []string{"main", "release-.*"}}, wantRows: 1, wantStatus: "applied"},
		{name: "silently skipped", item: map[string]any{"key": "sonar.core.serverBaseURL", "value": "http://x"}, wantRows: 0, wantSeenKey: "sonar.core.serverBaseURL"},
		{name: "sqs-only", item: map[string]any{"key": "sonar.technicalDebt.ratingGrid", "value": "0.1,0.2,0.3,0.4"}, wantRows: 1, wantStatus: "skipped"},
		{name: "applied", item: map[string]any{"key": "custom.key", "value": "other"}, wantRows: 1, wantStatus: "applied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, read := newHelperWriter(t, "setGlobalSettings")
			seen := tc.seen
			if seen == nil {
				seen = map[string]bool{}
			}
			got, err := processCustomizedSetting(rawItem(t, tc.item), defaults, orgs, w, seen)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.wantAuth {
				t.Errorf("consolidated = %v, want %v", got, tc.wantAuth)
			}
			rows := read()
			if len(rows) != tc.wantRows {
				t.Fatalf("rows = %d, want %d", len(rows), tc.wantRows)
			}
			if tc.wantStatus != "" {
				var rec struct {
					Outcomes []map[string]string `json:"outcomes"`
				}
				if err := json.Unmarshal(rows[0], &rec); err != nil || len(rec.Outcomes) != 1 {
					t.Fatalf("bad row %s: %v", rows[0], err)
				}
				if rec.Outcomes[0]["status"] != tc.wantStatus {
					t.Errorf("status = %q, want %q", rec.Outcomes[0]["status"], tc.wantStatus)
				}
			}
			if tc.wantSeenKey != "" && !seen[tc.wantSeenKey] {
				t.Errorf("expected %q marked seen", tc.wantSeenKey)
			}
			if tc.wantNotInSeen != "" && seen[tc.wantNotInSeen] {
				t.Errorf("%q must not be marked seen", tc.wantNotInSeen)
			}
		})
	}
}

func TestProcessCustomizedSetting_AuthSilentWhenEmpty(t *testing.T) {
	w, read := newHelperWriter(t, "setGlobalSettings")
	// Customised (differs from default "d") but empty value -> not SQS-only.
	item := rawItem(t, map[string]any{"key": "sonar.auth.x", "value": "", "parentValue": "p"})
	got, err := processCustomizedSetting(item, map[string]string{}, nil, w, map[string]bool{})
	if err != nil || got {
		t.Fatalf("got=%v err=%v, want false,nil", got, err)
	}
	if n := len(read()); n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
}

func TestProcessCustomizedSetting_WriteError(t *testing.T) {
	w := brokenWriter(t)
	item := rawItem(t, map[string]any{"key": "custom.key", "value": "other"})
	if _, err := processCustomizedSetting(item, map[string]string{}, nil, w, map[string]bool{}); err == nil {
		t.Fatal("expected write error")
	}
	db := rawItem(t, map[string]any{"key": "sonar.dbcleaner.branchesToKeepWhenInactive", "values": []string{"a"}})
	if _, err := processCustomizedSetting(db, map[string]string{}, nil, w, map[string]bool{}); err == nil {
		t.Fatal("expected write error for dbcleaner")
	}
}

// brokenWriter returns a ChunkWriter whose directory has been removed, so
// every WriteOne fails.
func brokenWriter(t *testing.T) *common.ChunkWriter {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "task")
	w, err := common.NewChunkWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWriteCustomizedSettingRows(t *testing.T) {
	w, read := newHelperWriter(t, "setGlobalSettings")
	items := []structure.ExtractItem{
		rawItem(t, map[string]any{"key": "sonar.auth.github.enabled", "value": "true"}),
		rawItem(t, map[string]any{"key": "custom.key", "value": "v"}),
	}
	auth, err := writeCustomizedSettingRows(items, map[string]string{}, nil, w, map[string]bool{})
	if err != nil || !auth {
		t.Fatalf("auth=%v err=%v", auth, err)
	}
	if n := len(read()); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}

	if _, err := writeCustomizedSettingRows(items[1:], map[string]string{}, nil, brokenWriter(t), map[string]bool{}); err == nil {
		t.Error("expected write error to propagate")
	}
}

func TestBuildDefaultValueSweepRecord(t *testing.T) {
	cases := []struct {
		name string
		item map[string]any
		seen map[string]bool
		want bool
	}{
		{"empty key", map[string]any{"defaultValue": "x"}, nil, false},
		{"seen", map[string]any{"key": "a.b"}, map[string]bool{"a.b": true}, false},
		{"internal", map[string]any{"key": "sonaranalyzer-cs.x"}, nil, false},
		{"sqs-only non-default", map[string]any{"key": "sonar.issues.sandbox.default", "value": "true"}, nil, false},
		{"silently skipped", map[string]any{"key": "sonar.core.serverBaseURL"}, nil, false},
		{"emitted", map[string]any{"key": "a.b", "defaultValue": "x"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := tc.seen
			if seen == nil {
				seen = map[string]bool{}
			}
			rec, ok := buildDefaultValueSweepRecord(rawItem(t, tc.item), seen)
			if ok != tc.want {
				t.Fatalf("ok = %v, want %v", ok, tc.want)
			}
			if !ok {
				if rec != nil {
					t.Errorf("rec should be nil")
				}
				return
			}
			if rec["key"] != "a.b" || !seen["a.b"] {
				t.Errorf("rec=%v seen=%v", rec, seen)
			}
			oc := rec["outcomes"].([]map[string]string)
			if oc[0]["reason"] != "default-value" || oc[0]["status"] != "skipped" {
				t.Errorf("outcome = %v", oc[0])
			}
		})
	}
}

func TestWriteConsolidatedAuthRow(t *testing.T) {
	w, read := newHelperWriter(t, "setGlobalSettings")
	if err := writeConsolidatedAuthRow(w); err != nil {
		t.Fatal(err)
	}
	rows := read()
	if len(rows) != 1 || common.ExtractField(rows[0], "key") != "sonar.auth.*" {
		t.Fatalf("rows = %v", rows)
	}
	if err := writeConsolidatedAuthRow(brokenWriter(t)); err == nil {
		t.Error("expected write error")
	}
}

func TestLoadSettingDefaults(t *testing.T) {
	exportDir := t.TempDir()
	extractDir := filepath.Join(exportDir, helperExtractID)
	writeFile(t, extractDir, "extract.json", `{"url":"`+testServerURL+`"}`)
	writeJSONL(t, filepath.Join(extractDir, "getServerSettingsDefinitions", "d.jsonl"), []map[string]any{
		{"key": "a", "defaultValue": "1"},
		{"key": "", "defaultValue": "ignored"},
		{"key": "b"},
	})
	got, err := loadSettingDefaults(exportDir, helperMapping())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["a"] != "1" || got["b"] != "" {
		t.Errorf("defaults = %v", got)
	}
}

func TestWriteDefaultValueSweepRows(t *testing.T) {
	exportDir := t.TempDir()
	extractDir := filepath.Join(exportDir, helperExtractID)
	writeFile(t, extractDir, "extract.json", `{"url":"`+testServerURL+`"}`)
	writeJSONL(t, filepath.Join(extractDir, "getServerSettingsDefinitions", "d.jsonl"), []map[string]any{
		{"key": "keep.me"},
		{"key": "already.seen"},
		{"key": "sonaranalyzer-x"},
	})
	w, read := newHelperWriter(t, "setGlobalSettings")
	seen := map[string]bool{"already.seen": true}
	if err := writeDefaultValueSweepRows(exportDir, helperMapping(), seen, w); err != nil {
		t.Fatal(err)
	}
	rows := read()
	if len(rows) != 1 || common.ExtractField(rows[0], "key") != "keep.me" {
		t.Fatalf("rows = %s", rows)
	}

	// Write error propagates.
	delete(seen, "keep.me")
	if err := writeDefaultValueSweepRows(exportDir, helperMapping(), map[string]bool{}, brokenWriter(t)); err == nil {
		t.Error("expected write error")
	}
}

func TestSynthesizeSetGlobalSettings_Sweep(t *testing.T) {
	exportDir := t.TempDir()
	runDir := t.TempDir()
	extractDir := filepath.Join(exportDir, helperExtractID)
	writeFile(t, extractDir, "extract.json", `{"url":"`+testServerURL+`"}`)
	writeJSONL(t, filepath.Join(extractDir, "getServerSettings", "s.jsonl"), []map[string]any{
		{"key": "custom.key", "value": "v"},
		{"key": "sonar.auth.github.enabled", "value": "true"},
	})
	writeJSONL(t, filepath.Join(extractDir, "getServerSettingsDefinitions", "d.jsonl"), []map[string]any{
		{"key": "custom.key", "defaultValue": "d"},
		{"key": "untouched.key", "defaultValue": "d"},
	})
	if err := synthesizeSetGlobalSettings(exportDir, runDir, helperMapping(), map[string]string{"default": "org1"}); err != nil {
		t.Fatal(err)
	}
	rows, err := common.NewDataStore(runDir).ReadAll("setGlobalSettings")
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, r := range rows {
		keys[common.ExtractField(r, "key")] = true
	}
	for _, k := range []string{"custom.key", "untouched.key", "sonar.auth.*"} {
		if !keys[k] {
			t.Errorf("missing row for %s; got %v", k, keys)
		}
	}
}
