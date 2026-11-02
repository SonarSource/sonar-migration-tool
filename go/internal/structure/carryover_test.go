// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"encoding/csv"
	"os"
	"path/filepath"
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
