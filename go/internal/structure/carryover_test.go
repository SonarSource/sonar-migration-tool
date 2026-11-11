// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// setCSVColumn fills column col of every data row of dir/name.
func setCSVColumn(t *testing.T, dir, name, col, value string) {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	idx := -1
	for i, h := range rows[0] {
		if h == col {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("%s has no column %s", name, col)
	}
	for _, r := range rows[1:] {
		r[idx] = value
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := csv.NewWriter(out).WriteAll(rows); err != nil {
		t.Fatal(err)
	}
}

// #664: the operator's sonarcloud_org_key survives regenerating the CSVs.
func TestRunStructureCarryOver_KeepsOperatorEdits(t *testing.T) {
	dir := setupTestExtract(t)
	if err := RunStructure(dir); err != nil {
		t.Fatal(err)
	}
	setCSVColumn(t, dir, "organizations.csv", "sonarcloud_org_key", "my-org")
	setCSVColumn(t, dir, "projects.csv", "sonarcloud_org_key", "other-org")

	carry, err := ReadCarriedKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	// An explicit default must not override the operator's edits.
	stats, err := RunStructureCarryOver(dir, carry, "default-org")
	if err != nil {
		t.Fatal(err)
	}
	if stats.NeedsReview() {
		t.Errorf("nothing was lost, got %+v", stats)
	}
	orgs, _ := LoadCSV(dir, "organizations.csv")
	if len(orgs) == 0 {
		t.Fatal("no organizations")
	}
	for _, o := range orgs {
		if o["sonarcloud_org_key"] != "my-org" {
			t.Errorf("org key = %v, want my-org", o["sonarcloud_org_key"])
		}
	}
	projects, _ := LoadCSV(dir, "projects.csv")
	for _, p := range projects {
		if p["sonarcloud_org_key"] != "other-org" {
			t.Errorf("project key = %v, want other-org", p["sonarcloud_org_key"])
		}
	}
	if want := len(orgs) + len(projects); stats.Carried != want {
		t.Errorf("carried = %d, want %d", stats.Carried, want)
	}
}

func TestRunStructureCarryOver_DefaultFillsBlankRows(t *testing.T) {
	dir := setupTestExtract(t)
	carry, err := ReadCarriedKeys(dir) // nothing on disk yet
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunStructureCarryOver(dir, carry, "default-org"); err != nil {
		t.Fatal(err)
	}
	orgs, _ := LoadCSV(dir, "organizations.csv")
	for _, o := range orgs {
		if o["sonarcloud_org_key"] != "default-org" {
			t.Errorf("org key = %v, want default-org", o["sonarcloud_org_key"])
		}
	}
}

func TestCarriedKeysApply_ReportsDroppedAndNewRows(t *testing.T) {
	c := &CarriedKeys{
		hadOrgs:  true,
		orgs:     map[string]string{orgRowKey("u", "gone", ""): "x", orgRowKey("u", "kept", ""): "y"},
		projects: map[string]string{projectRowKey("u", "p1"): "z"},
	}
	orgs := []Organization{{ServerURL: "u", SonarQubeOrgKey: "kept"}, {ServerURL: "u", SonarQubeOrgKey: "new"}}
	projects := []Project{{ServerURL: "u", Key: "p1"}, {ServerURL: "u", Key: "p2"}}
	stats := c.Apply(orgs, projects)
	if stats.Carried != 2 || stats.Dropped != 1 || stats.OrgsWithoutKey != 1 || !stats.NeedsReview() {
		t.Errorf("stats = %+v", stats)
	}
	var nilCarry *CarriedKeys
	if got := nilCarry.Apply(orgs, projects); got != (CarryStats{}) {
		t.Errorf("nil carry = %+v", got)
	}
}

// #664: an empty entity list must not erase an existing mapping CSV.
func TestRunMappings_DoesNotClobberExistingCSVWithEmpty(t *testing.T) {
	dir := setupTestExtract(t)
	if err := RunStructure(dir); err != nil {
		t.Fatal(err)
	}
	if err := RunMappings(dir); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "gates.csv"))
	if err != nil || len(before) == 0 {
		t.Fatalf("gates.csv missing or empty: %v", err)
	}
	// Simulate a projects-only extract: the gate data is gone from the set.
	if err := os.RemoveAll(filepath.Join(dir, testExtractID, "getGates")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, testExtractID, "getProjectDetails")); err != nil {
		t.Fatal(err)
	}
	if !keepExistingCSV(dir, "gates", []Gate(nil)) {
		t.Error("keepExistingCSV should keep a non-empty file when the list is empty")
	}
	if keepExistingCSV(dir, "gates", []Gate{{}}) {
		t.Error("a non-empty list must be written")
	}
	if keepExistingCSV(dir, "nosuchfile", []Gate(nil)) {
		t.Error("a missing file is written (empty) as before")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "gates.csv"))
	if string(after) != string(before) {
		t.Error("gates.csv changed")
	}
}

// #688: extracting slices one after another only extends organizations.csv.
// A later slice rebinds every project of the server-wide organization to
// GitHub: the GitHub organization is appended, and the server-wide row, which
// no extracted project uses any more, keeps its place and its mapping.
func TestRunStructureCarryOver_SliceNeverRemovesOrganizations(t *testing.T) {
	dir := setupTestExtract(t)
	if err := RunStructure(dir); err != nil {
		t.Fatal(err)
	}
	setCSVColumn(t, dir, "organizations.csv", "sonarcloud_org_key", "server-org")

	const sliceID = "2026-08-21-0001"
	sliceDir := filepath.Join(dir, sliceID)
	writeTestJSON(t, filepath.Join(sliceDir, "extract.json"), map[string]any{
		"url": testSQURL, "partial": true, "project_keys": []string{"proj1", "proj2"},
	})
	writeTestJSONL(t, filepath.Join(sliceDir, "getBindings"), []map[string]any{
		{"key": "gh", "alm": "github", "url": "https://api.github.com", "serverUrl": testSQURL},
	})
	writeTestJSONL(t, filepath.Join(sliceDir, "getProjectDetails"), []map[string]any{
		{"key": "proj1", "name": "Project 1", "projectKey": "proj1", "branch": "main", "serverUrl": testSQURL},
		{"key": "proj2", "name": "Project 2", "projectKey": "proj2", "branch": "main", "serverUrl": testSQURL},
	})
	writeTestJSONL(t, filepath.Join(sliceDir, "getProjectBindings"), []map[string]any{
		{"key": "gh", "alm": "github", "repository": "acme/proj1", "projectKey": "proj1", "serverUrl": testSQURL},
		{"key": "gh", "alm": "github", "repository": "acme/proj2", "projectKey": "proj2", "serverUrl": testSQURL},
	})

	carry, err := ReadCarriedKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := RunStructureCarryOver(dir, carry)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := LoadCSV(dir, "organizations.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("organizations.csv has %d rows, want 2 (the kept server-wide row and the new GitHub one): %v", len(rows), rows)
	}
	if got := str(rows[0], "sonarqube_org_key"); got != testSQURL {
		t.Errorf("row 1 sonarqube_org_key = %q, want the server-wide row %q kept first", got, testSQURL)
	}
	if got := str(rows[0], "sonarcloud_org_key"); got != "server-org" {
		t.Errorf("kept row sonarcloud_org_key = %q, want server-org", got)
	}
	if got, _ := rows[0]["project_count"].(float64); got != 0 {
		t.Errorf("kept row project_count = %v, want 0", got)
	}
	if got := str(rows[1], "alm"); got != "github" {
		t.Errorf("row 2 alm = %q, want the new GitHub organization appended", got)
	}
	if stats.Kept != 1 || stats.Dropped != 0 {
		t.Errorf("stats Kept=%d Dropped=%d, want Kept=1 Dropped=0", stats.Kept, stats.Dropped)
	}
}

// #688: regenerated rows replace their previous version in place, previous
// rows not regenerated are kept with a project_count of 0, new rows follow in
// a stable order, and a kept mapped row is not reported as dropped.
func TestExtendOrganizations_KeepsOrderAndAppendsNewRows(t *testing.T) {
	carry := &CarriedKeys{prevOrgs: []Organization{
		{SonarQubeOrgKey: "b", ServerURL: "s", SonarCloudOrgKey: "org-b", ProjectCount: 3},
		{SonarQubeOrgKey: "a", ServerURL: "s", ProjectCount: 1},
		{SonarQubeOrgKey: "z", ServerURL: "s", SonarCloudOrgKey: "org-z", ProjectCount: 5},
	}}
	regenerated := []Organization{
		{SonarQubeOrgKey: "d", ServerURL: "s", ProjectCount: 1},
		{SonarQubeOrgKey: "a", ServerURL: "s", ProjectCount: 2},
		{SonarQubeOrgKey: "c", ServerURL: "s", ProjectCount: 1},
	}
	stats := CarryStats{Dropped: 2}

	got := carry.ExtendOrganizations(regenerated, &stats)

	var order []string
	for _, o := range got {
		order = append(order, o.SonarQubeOrgKey)
	}
	if want := []string{"b", "a", "z", "c", "d"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if got[0].SonarCloudOrgKey != "org-b" || got[0].ProjectCount != 0 {
		t.Errorf("kept row b = %+v, want its mapping and a project_count of 0", got[0])
	}
	if got[1].ProjectCount != 2 {
		t.Errorf("regenerated row a project_count = %d, want the new count 2", got[1].ProjectCount)
	}
	if stats.Kept != 2 || stats.Dropped != 0 {
		t.Errorf("stats Kept=%d Dropped=%d, want Kept=2 Dropped=0", stats.Kept, stats.Dropped)
	}
}
