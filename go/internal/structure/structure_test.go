// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunStructure(t *testing.T) {
	dir := setupTestExtract(t)

	err := RunStructure(dir)
	if err != nil {
		t.Fatalf("RunStructure failed: %v", err)
	}

	// Verify CSVs were created.
	for _, name := range []string{"organizations.csv", "projects.csv"} {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			t.Errorf("expected %s to exist", name)
		}
	}

	// Verify projects.csv content.
	projects, err := LoadCSV(dir, "projects.csv")
	if err != nil {
		t.Fatalf("loading projects.csv: %v", err)
	}
	if len(projects) != 2 {
		t.Errorf("expected 2 projects in CSV, got %d", len(projects))
	}
}

func TestRunMappings(t *testing.T) {
	dir := setupTestExtract(t)

	// First run structure to create projects.csv.
	if err := RunStructure(dir); err != nil {
		t.Fatalf("RunStructure failed: %v", err)
	}

	// Then run mappings.
	if err := RunMappings(dir); err != nil {
		t.Fatalf("RunMappings failed: %v", err)
	}

	// Verify mapping CSVs were created.
	for _, name := range []string{"profiles.csv", "gates.csv", "groups.csv", "templates.csv", "portfolios.csv"} {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			t.Errorf("expected %s to exist", name)
		}
	}

	// Verify gates.csv has the custom gate.
	gates, err := LoadCSV(dir, "gates.csv")
	if err != nil {
		t.Fatalf("loading gates.csv: %v", err)
	}
	if len(gates) < 1 {
		t.Errorf("expected at least 1 gate, got %d", len(gates))
	}
}

func TestRunStructureNoExtracts(t *testing.T) {
	dir := t.TempDir()
	err := RunStructure(dir)
	if err == nil {
		t.Error("expected error for empty directory")
	}
}

func TestRunMappingsNoProjects(t *testing.T) {
	dir := setupTestExtract(t)
	// Don't run structure first — should fail.
	err := RunMappings(dir)
	if err == nil {
		t.Error("expected error when projects.csv missing")
	}
}

func TestMapOrganizationStructure(t *testing.T) {
	bindings := []Binding{
		{Key: "org1", ALM: "github", URL: "https://api.github.com", ServerURL: testSQURL, IsCloud: true, ProjectCount: 5},
		{Key: "org2", ALM: "", URL: "", ServerURL: "https://sq2.example.com/", IsCloud: false, ProjectCount: 0},
	}
	orgs := MapOrganizationStructure(bindings)
	if len(orgs) != 1 {
		t.Fatalf("expected 1 org (count>0), got %d", len(orgs))
	}
	if orgs[0].SonarQubeOrgKey != "org1" {
		t.Errorf("expected org1, got %q", orgs[0].SonarQubeOrgKey)
	}
}

func TestMapProfiles(t *testing.T) {
	dir := setupTestExtract(t)
	mapping := ExtractMapping{testSQURL: testExtractID}

	// Build project org mapping from test data.
	projectOrgMapping := map[string]string{
		testProjOrgKey:                 "org1",
		"https://sq.example.com/proj2": "org1",
	}

	profiles := MapProfiles(projectOrgMapping, mapping, dir)
	// Should have the "Custom" profile (non-builtIn) referenced by proj2.
	found := false
	for _, p := range profiles {
		if p.Name == "Custom" {
			found = true
			if p.ParentName != "Sonar way" {
				t.Errorf("expected parent 'Sonar way', got %q", p.ParentName)
			}
		}
	}
	if !found {
		t.Error("expected 'Custom' profile in results")
	}
}

func TestMapGates(t *testing.T) {
	dir := setupTestExtract(t)
	mapping := ExtractMapping{testSQURL: testExtractID}
	projectOrgMapping := map[string]string{
		testProjOrgKey:                 "org1",
		"https://sq.example.com/proj2": "org1",
	}

	gates := MapGates(projectOrgMapping, mapping, dir)
	if len(gates) < 1 {
		t.Fatalf("expected at least 1 gate, got %d", len(gates))
	}
	found := false
	for _, g := range gates {
		if g.Name == "Custom Gate" {
			found = true
		}
	}
	if !found {
		t.Error("expected 'Custom Gate' in results")
	}
}

func TestMapTemplates(t *testing.T) {
	dir := setupTestExtract(t)
	mapping := ExtractMapping{testSQURL: testExtractID}
	projectOrgMapping := map[string]string{
		testProjOrgKey: "org1",
	}

	templates := MapTemplates(projectOrgMapping, mapping, dir)
	if len(templates) < 1 {
		t.Fatalf("expected at least 1 template, got %d", len(templates))
	}
	if templates[0].Name != "Default Template" {
		t.Errorf("expected 'Default Template', got %q", templates[0].Name)
	}
}

func TestMapGroups(t *testing.T) {
	dir := setupTestExtract(t)
	mapping := ExtractMapping{testSQURL: testExtractID}
	projectOrgMapping := map[string]string{
		testProjOrgKey: "org1",
	}

	groups := MapGroups(projectOrgMapping, mapping, nil, nil, dir)
	// Should have sonar-users (has permissions) but not Anyone.
	found := false
	for _, g := range groups {
		if g.Name == "sonar-users" {
			found = true
		}
		if g.Name == "Anyone" {
			t.Error("should not include 'Anyone' group")
		}
	}
	if !found {
		t.Error("expected 'sonar-users' in results")
	}
}

func TestMapPortfolios(t *testing.T) {
	dir := setupTestExtract(t)
	mapping := ExtractMapping{testSQURL: testExtractID}

	portfolios := MapPortfolios(dir, mapping)
	// Test data has no portfolio projects, so should be empty.
	if len(portfolios) != 0 {
		t.Errorf("expected 0 portfolios, got %d", len(portfolios))
	}
}

func TestCSVRoundTrip(t *testing.T) {
	dir := t.TempDir()
	orgs := []Organization{
		{SonarQubeOrgKey: "org1", SonarCloudOrgKey: "", ServerURL: testSQURL,
			ALM: "github", URL: "https://api.github.com", IsCloud: true, ProjectCount: 5},
	}
	if err := ExportCSV(dir, "test_orgs", orgs); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadCSV(dir, "test_orgs.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 row, got %d", len(loaded))
	}
	if loaded[0]["sonarqube_org_key"] != "org1" {
		t.Errorf("expected org1, got %v", loaded[0]["sonarqube_org_key"])
	}
	if loaded[0]["is_cloud"] != true {
		t.Errorf("expected is_cloud=true, got %v", loaded[0]["is_cloud"])
	}
}

func TestGetUniqueExtracts(t *testing.T) {
	dir := setupTestExtract(t)
	mapping, err := GetUniqueExtracts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(mapping) != 1 {
		t.Fatalf("expected 1 extract, got %d", len(mapping))
	}
	if mapping[testSQURL] != testExtractID {
		t.Errorf("expected %q, got %q", testExtractID, mapping[testSQURL])
	}
}

func TestGenerateHashID(t *testing.T) {
	// Same input should produce same hash.
	h1 := generateHashID([]string{"a", "b", "c"})
	h2 := generateHashID([]string{"c", "a", "b"}) // different order, same sorted
	if h1 != h2 {
		t.Errorf("expected same hash for same sorted input, got %q and %q", h1, h2)
	}
	// Should be UUID-formatted (36 chars with dashes).
	if len(h1) != 36 {
		t.Errorf("expected UUID length 36, got %d: %q", len(h1), h1)
	}
}

// Issue #314: readTaskDir must keep going when a single JSONL file
// in the task dir fails to parse. Previously it returned (nil, err)
// on the first failure, which ReadExtractData then turned into a
// silent skip of the entire task. With the resilient version,
// well-formed files still contribute their records and the bad file
// only produces a warning.
func TestReadTaskDirSkipsUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	// Two files: one well-formed, one with an unreadable line (binary
	// garbage that bufio.Reader.ReadBytes will read fine but consumer
	// can later choke on — for this test we just need NotExist on one
	// path to force an error from ReadJSONLFile via a non-jsonl
	// extension would be skipped, so use a permission flip instead).
	good := filepath.Join(dir, "good.jsonl")
	if err := os.WriteFile(good, []byte(`{"k":"v1"}`+"\n"+`{"k":"v2"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(bad, []byte(`{"k":"v3"}`+"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	got, err := readTaskDir(dir)
	if err != nil {
		t.Fatalf("readTaskDir should not error on a per-file failure: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 records from the readable file, got %d", len(got))
	}
}

// When the task directory itself doesn't exist (e.g. extract didn't
// run the task), readTaskDir still returns an error so ReadExtractData
// can `continue` past that task — same as before.
func TestReadTaskDirMissingDirReturnsError(t *testing.T) {
	_, err := readTaskDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Error("expected error for missing dir")
	}
}
