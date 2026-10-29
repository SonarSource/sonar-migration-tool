// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const (
	testServerID  = "SQ-ABC123"
	testServerURL = "https://sq.example.com/"
)

func TestParseJSONObjectValid(t *testing.T) {
	raw := json.RawMessage(`{"key": "value", "num": 42}`)
	obj := ParseJSONObject(raw)
	if obj["key"] != "value" {
		t.Errorf("key: got %v", obj["key"])
	}
}

func TestParseJSONObjectInvalid(t *testing.T) {
	raw := json.RawMessage(`not json`)
	obj := ParseJSONObject(raw)
	if len(obj) != 0 {
		t.Errorf("expected empty map, got %v", obj)
	}
}

func TestParseJSONObjectNested(t *testing.T) {
	raw := json.RawMessage(`{"System": {"Version": "9.9"}}`)
	obj := ParseJSONObject(raw)
	sys, ok := obj["System"].(map[string]any)
	if !ok {
		t.Fatal("expected nested map")
	}
	if sys["Version"] != "9.9" {
		t.Errorf("Version: got %v", sys["Version"])
	}
}

func TestServerIDFromURL(t *testing.T) {
	idMap := map[string]string{
		testServerURL: testServerID,
	}

	if ServerIDFromURL(idMap, testServerURL) != testServerID {
		t.Error("expected mapped ID")
	}
	if ServerIDFromURL(idMap, "https://unknown.com/") != "https://unknown.com/" {
		t.Error("expected URL fallback")
	}
}

func TestBuildServerIDMapping(t *testing.T) {
	dir := t.TempDir()

	// Create extract directory structure.
	extractID := "test-extract-01"
	taskDir := filepath.Join(dir, extractID, "getServerInfo")
	os.MkdirAll(taskDir, 0o755)

	// Write extract.json metadata.
	meta := `{"url": testServerURL, "version": 10.7, "edition": "enterprise", "run_id": "test-extract-01"}`
	os.WriteFile(filepath.Join(dir, extractID, "extract.json"), []byte(meta), 0o644)

	// Write server info JSONL.
	serverInfo := `{"System": {"Server ID": "` + testServerID + `", "Version": "10.7", "Edition": "enterprise"}}`
	os.WriteFile(filepath.Join(taskDir, "results.1.jsonl"), []byte(serverInfo+"\n"), 0o644)

	// Build the mapping (uses GetUniqueExtracts internally).
	mapping := structure.ExtractMapping{testServerURL: {extractID}}
	idMap := BuildServerIDMapping(dir, mapping)

	if idMap[testServerURL] != testServerID {
		t.Errorf("expected SQ-ABC123, got %q", idMap[testServerURL])
	}
}

func TestBuildServerIDMappingEmpty(t *testing.T) {
	dir := t.TempDir()
	idMap := BuildServerIDMapping(dir, nil)
	if len(idMap) != 0 {
		t.Errorf("expected empty map, got %v", idMap)
	}
}
