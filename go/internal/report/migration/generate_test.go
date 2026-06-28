// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const (
	testServerURL = "https://sq.example.com/"
	testExtractID = "test-extract-01"
	testServerID  = "SQ-TEST"
)

func setupExtract(t *testing.T, tasks map[string][]map[string]any) (string, structure.ExtractMapping) {
	t.Helper()
	dir := t.TempDir()
	extractDir := filepath.Join(dir, testExtractID)
	meta := map[string]any{"url": testServerURL, "version": 10.7, "edition": "enterprise", "run_id": testExtractID}
	metaData, _ := json.Marshal(meta)
	os.MkdirAll(extractDir, 0o755)
	os.WriteFile(filepath.Join(extractDir, "extract.json"), metaData, 0o644)

	for taskName, entries := range tasks {
		taskDir := filepath.Join(extractDir, taskName)
		os.MkdirAll(taskDir, 0o755)
		f, _ := os.Create(filepath.Join(taskDir, "results.1.jsonl"))
		for _, entry := range entries {
			data, _ := json.Marshal(entry)
			f.Write(data)
			f.Write([]byte("\n"))
		}
		f.Close()
	}
	return dir, structure.ExtractMapping{testServerURL: testExtractID}
}

// emptyTaskMap returns the standard set of task names used by both migration
// report tests, all pre-populated with empty record slices.
func emptyTaskMap() map[string][]map[string]any {
	return map[string][]map[string]any{
		"getServerInfo":          {},
		"getProjectDetails":      {},
		"getProjectAnalyses":     {},
		"getProjectBindings":     {},
		"getBindings":            {},
		"getBranches":            {},
		"getProjectPullRequests": {},
		"getDefaultTemplates":    {},
		"getTemplates":           {},
		"getPlugins":             {},
		"getRules":               {},
		"getProfileRules":        {},
		"getProfiles":            {},
		"getPortfolioDetails":    {},
		"getPortfolioProjects":   {},
		"getGates":               {},
		"getApplicationDetails":  {},
		"getUsers":               {},
		"getProjectSettings":     {},
		"getUsage":               {},
	}
}

func TestFillTemplate(t *testing.T) {
	tmpl := "Hello {name}, welcome to {place}!"
	result := fillTemplate(tmpl, map[string]string{"name": "Alice", "place": "Go"})
	if result != "Hello Alice, welcome to Go!" {
		t.Errorf("got %q", result)
	}
}

func TestFillTemplateUnusedPlaceholder(t *testing.T) {
	tmpl := "{a} and {b}"
	result := fillTemplate(tmpl, map[string]string{"a": "X"})
	if !strings.Contains(result, "{b}") {
		t.Error("unreplaced placeholder should remain")
	}
}

func TestGenerateMigrationReport(t *testing.T) {
	tasks := emptyTaskMap()
	tasks["getServerInfo"] = []map[string]any{
		{"System": map[string]any{"Version": "10.7", "Server ID": testServerID, "Edition": "enterprise", "Lines of Code": float64(5000)}},
	}
	tasks["getProjectDetails"] = []map[string]any{
		{"key": "proj-1", "name": "Project 1", "qualityProfiles": []any{}, "qualityGate": map[string]any{"name": "Sonar way"}},
	}
	dir, mapping := setupExtract(t, tasks)

	md := GenerateMigrationReport(dir, mapping)

	if !strings.Contains(md, "SonarQube Utilization Assessment") {
		t.Error("missing report title")
	}
	if !strings.Contains(md, "Server Details") {
		t.Error("missing server details section")
	}
	if !strings.Contains(md, testServerID) {
		t.Errorf("missing server ID %s in report", testServerID)
	}
	if !strings.Contains(md, "Governance") {
		t.Error("missing Governance section")
	}
	if !strings.Contains(md, "Appendix") {
		t.Error("missing Appendix section")
	}
}

func TestGenerateMigrationReportEmpty(t *testing.T) {
	dir, mapping := setupExtract(t, emptyTaskMap())

	md := GenerateMigrationReport(dir, mapping)

	// Should still produce a valid report with empty tables.
	if !strings.Contains(md, "SonarQube Utilization Assessment") {
		t.Error("missing report title even with empty data")
	}
}
