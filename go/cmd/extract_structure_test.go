// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	if err := runExtractStructure(dir, "", extractStructureTestTime); err != nil {
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

	if err := runExtractStructure(dir, "my-cloud-org", extractStructureTestTime); err != nil {
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

	if err := runExtractStructure(dir, "", extractStructureTestTime); err != nil {
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

// #688: an extract limited by --objects to other categories still
// regenerates every CSV, and organizations.csv lists the DevOps platform
// organizations from the bindings that extract always pulls.
func TestRunExtractStructure_GroupsOnlyExtractWritesOrganizations(t *testing.T) {
	dir := t.TempDir()
	extractDir := filepath.Join(dir, "2026-10-16-01")
	srvURL := "http://localhost:10000/"
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, extractDir, "extract.json", `{"url":"`+srvURL+`","partial":true,"objects":["groups"]}`)
	for task, record := range map[string]string{
		"getBindings":        `{"key":"gh","alm":"github","url":"https://api.github.com","serverUrl":"` + srvURL + `"}`,
		"getProjectBindings": `{"key":"gh","alm":"github","repository":"acme/proj1","projectKey":"proj1","serverUrl":"` + srvURL + `"}`,
		"getProjectDetails":  `{"key":"proj1","name":"Project 1","branch":"main","serverUrl":"` + srvURL + `"}`,
		"getGroups":          `{"name":"devs","serverUrl":"` + srvURL + `"}`,
	} {
		if err := os.MkdirAll(filepath.Join(extractDir, task), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(extractDir, task), "results.1.jsonl", record+"\n")
	}

	if err := runExtractStructure(dir, "", extractStructureTestTime); err != nil {
		t.Fatalf("runExtractStructure: %v", err)
	}
	for _, name := range structureCSVs {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "organizations.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "github") || !strings.Contains(string(data), "https://api.github.com") {
		t.Errorf("organizations.csv does not list the GitHub organization:\n%s", data)
	}
}

// #688: an extract that found no project (e.g. groups only, on a server
// whose projects the token cannot browse) still succeeds. organizations.csv
// and projects.csv are written, and mappings, having nothing to assign, is
// skipped instead of failing the extract.
func TestRunExtractStructure_NoProjectSkipsMappings(t *testing.T) {
	dir := t.TempDir()
	extractDir := filepath.Join(dir, "2026-10-16-01")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, extractDir, "extract.json", `{"url":"http://localhost:10000/","partial":true,"objects":["groups"]}`)

	if err := runExtractStructure(dir, "", extractStructureTestTime); err != nil {
		t.Fatalf("runExtractStructure: %v", err)
	}
	for _, name := range []string{"organizations.csv", "projects.csv"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "groups.csv")); err == nil {
		t.Error("groups.csv was written, want mappings skipped when no project was extracted")
	}
}

// #503: a failing step names itself and says the extract data is intact.
func TestRunExtractStructure_ErrorNamesStep(t *testing.T) {
	dir := t.TempDir()

	err := runExtractStructure(dir, "", extractStructureTestTime)
	if err == nil {
		t.Fatal("expected an error with no extract in the directory")
	}
	for _, want := range []string{"structure step failed", "extract data in " + dir + " is intact"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// #664: a second run into the same export directory keeps the
// sonarcloud_org_key values the operator filled in.
func TestRunExtractStructure_CarriesOverOperatorOrgKey(t *testing.T) {
	dir := writeMinimalExtract(t)
	if err := runExtractStructure(dir, "", extractStructureTestTime); err != nil {
		t.Fatalf("first run: %v", err)
	}
	path := filepath.Join(dir, "organizations.csv")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil || len(rows) < 2 {
		t.Fatalf("organizations.csv rows: %v, %v", len(rows), err)
	}
	col := -1
	for i, h := range rows[0] {
		if h == "sonarcloud_org_key" {
			col = i
		}
	}
	for _, r := range rows[1:] {
		r[col] = "hand-edited-org"
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := csv.NewWriter(out).WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	out.Close()

	if err := runExtractStructure(dir, "", extractStructureTestTime.Add(time.Minute)); err != nil {
		t.Fatalf("second run: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "hand-edited-org") {
		t.Errorf("operator org key was not carried over:\n%s", data)
	}
}
