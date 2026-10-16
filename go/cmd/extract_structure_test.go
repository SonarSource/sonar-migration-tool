// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

var extractStructureTestTime = time.Date(2026, 10, 16, 14, 3, 12, 0, time.Local)

var structureCSVs = []string{
	"organizations.csv", "projects.csv",
	"gates.csv", "profiles.csv", "groups.csv", "templates.csv", "portfolios.csv",
}

// writeMinimalExtract writes one extract holding one unbound project:
// enough for structure and mappings to produce every CSV.
func writeMinimalExtract(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	extractDir := filepath.Join(dir, "2026-10-16-01")
	srvURL := "http://localhost:10000/"
	if err := os.MkdirAll(filepath.Join(extractDir, "getProjectDetails"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, extractDir, "extract.json", `{"url":"`+srvURL+`","edition":"enterprise"}`)
	writeFile(t, filepath.Join(extractDir, "getProjectDetails"), "results.1.jsonl",
		`{"key":"proj1","name":"Project 1","projectKey":"proj1","branch":"main","serverUrl":"`+srvURL+`"}`+"\n")
	return dir
}

// #503: a successful extract leaves every CSV migrate needs.
func TestRunExtractStructure_WritesAllCSVs(t *testing.T) {
	dir := writeMinimalExtract(t)

	if err := runExtractStructure(dir, nil, "", extractStructureTestTime); err != nil {
		t.Fatalf("runExtractStructure: %v", err)
	}
	for _, name := range structureCSVs {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}
}

// #503, #566: a single target org from --config pre-populates
// sonarcloud_org_key, as structure --config does.
func TestRunExtractStructure_PrePopulatesOrgKey(t *testing.T) {
	dir := writeMinimalExtract(t)

	if err := runExtractStructure(dir, nil, "my-cloud-org", extractStructureTestTime); err != nil {
		t.Fatalf("runExtractStructure: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "organizations.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "my-cloud-org") {
		t.Errorf("organizations.csv does not carry the org key:\n%s", data)
	}
}

// #503: a re-run backs up the edited organizations.csv before
// regenerating it.
func TestRunExtractStructure_BacksUpEditedCSV(t *testing.T) {
	dir := writeMinimalExtract(t)
	edited := "server_url,sonarcloud_org_key\nhttp://localhost:10000/,hand-edited-org\n"
	writeFile(t, dir, "organizations.csv", edited)

	if err := runExtractStructure(dir, nil, "", extractStructureTestTime); err != nil {
		t.Fatalf("runExtractStructure: %v", err)
	}
	backup, err := os.ReadFile(filepath.Join(dir, "organizations.csv.bak-20261016-140312"))
	if err != nil {
		t.Fatalf("expected a backup: %v", err)
	}
	if string(backup) != edited {
		t.Errorf("backup: got %q, want %q", backup, edited)
	}
	regenerated, _ := os.ReadFile(filepath.Join(dir, "organizations.csv"))
	if strings.Contains(string(regenerated), "hand-edited-org") {
		t.Error("organizations.csv was not regenerated")
	}
}

// #503: --objects without the projects category skips both steps and
// does not fail.
func TestRunExtractStructure_SkipsWithoutProjects(t *testing.T) {
	dir := writeMinimalExtract(t)
	objects := map[string]bool{common.ObjectProjects: false, "gates": true}

	if err := runExtractStructure(dir, objects, "", extractStructureTestTime); err != nil {
		t.Fatalf("runExtractStructure: %v", err)
	}
	for _, name := range structureCSVs {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s should not be written when projects were not extracted", name)
		}
	}
}

// #503: a failing step names itself and says the extract data is intact.
func TestRunExtractStructure_ErrorNamesStep(t *testing.T) {
	dir := t.TempDir()

	err := runExtractStructure(dir, nil, "", extractStructureTestTime)
	if err == nil {
		t.Fatal("expected an error with no extract in the directory")
	}
	for _, want := range []string{"structure step failed", "extract data in " + dir + " is intact"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}
