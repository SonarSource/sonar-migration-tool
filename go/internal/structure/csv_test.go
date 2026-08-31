// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExportCSVEmpty(t *testing.T) {
	dir := t.TempDir()
	err := ExportCSV(dir, "empty", []Organization{})
	if err != nil {
		t.Fatal(err)
	}
	// Should create an empty file.
	_, err = os.Stat(filepath.Join(dir, "empty.csv"))
	if err != nil {
		t.Errorf("expected file to exist: %v", err)
	}
}

func TestLoadCSVMissing(t *testing.T) {
	dir := t.TempDir()
	result, err := LoadCSV(dir, "nonexistent.csv")
	if err != nil {
		t.Errorf("expected nil error for missing file, got %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestCSVRoundTripProjects(t *testing.T) {
	dir := t.TempDir()
	projects := []Project{
		{
			Key: "proj1", Name: "Project 1", GateName: "Custom",
			Profiles:               []any{map[string]any{"key": "prof1", "language": "java"}},
			ServerURL:              "https://sq.example.com/",
			SonarQubeOrgKey:        "org1",
			MainBranch:             "main",
			IsCloudBinding:         true,
			NewCodeDefinitionType:  "days",
			NewCodeDefinitionValue: 30,
			ALM:                    "github",
			Repository:             "org/repo",
			Monorepo:               false,
		},
	}

	if err := ExportCSV(dir, "projects", projects); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadCSV(dir, "projects.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 row, got %d", len(loaded))
	}

	row := loaded[0]
	if row["key"] != "proj1" {
		t.Errorf("expected key=proj1, got %v", row["key"])
	}
	if row["is_cloud_binding"] != true {
		t.Errorf("expected is_cloud_binding=true, got %v (%T)", row["is_cloud_binding"], row["is_cloud_binding"])
	}
	if row["main_branch"] != "main" {
		t.Errorf("expected main_branch=main, got %v", row["main_branch"])
	}
}

func TestCSVRoundTripTemplates(t *testing.T) {
	dir := t.TempDir()
	templates := []Template{
		{
			UniqueKey: "org1tpl1", SourceTemplateKey: "tpl1", Name: "Default",
			ProjectKeyPattern: "proj.*", ServerURL: "https://sq.example.com/",
			IsDefault: true, SonarQubeOrgKey: "org1",
		},
	}

	if err := ExportCSV(dir, "templates", templates); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadCSV(dir, "templates.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 row, got %d", len(loaded))
	}
	if loaded[0]["is_default"] != true {
		t.Errorf("expected is_default=true, got %v", loaded[0]["is_default"])
	}
}

func TestCoerceCSVValue(t *testing.T) {
	tests := []struct {
		input    string
		expected any
	}{
		{"true", true},
		{"false", false},
		{`["a","b"]`, []any{"a", "b"}},
		{`{"key":"val"}`, map[string]any{"key": "val"}},
		{"hello", "hello"},
		{"", ""},
		{"null", nil},
		{"12345", float64(12345)},
	}
	for _, tt := range tests {
		// "project_count" is a plain data column (not "key"/"*_key"), so
		// it still gets the normal numeric/JSON coercion behavior.
		got := coerceCSVValue("project_count", tt.input)
		switch expected := tt.expected.(type) {
		case nil:
			if got != nil {
				t.Errorf("coerceCSVValue(%q) = %v, want nil", tt.input, got)
			}
		case bool:
			if got != expected {
				t.Errorf("coerceCSVValue(%q) = %v, want %v", tt.input, got, expected)
			}
		case string:
			if got != expected {
				t.Errorf("coerceCSVValue(%q) = %v, want %v", tt.input, got, expected)
			}
		case float64:
			if got != expected {
				t.Errorf("coerceCSVValue(%q) = %v (%T), want %v", tt.input, got, got, expected)
			}
		default:
			// For slices/maps, just check type.
			if got == nil {
				t.Errorf("coerceCSVValue(%q) = nil, want %v", tt.input, expected)
			}
		}
	}
}

// Issue #550: identifier columns (the bare "key" column, and any column
// whose header ends in "_key") must never be numeric-coerced, even when
// their value happens to look like a number. Otherwise a purely-numeric
// sonarcloud_org_key such as "12345" would silently become float64(12345),
// and downstream code doing `val, _ := row["sonarcloud_org_key"].(string)`
// would read back "" instead of the real key.
func TestCoerceCSVValue_IdentifierColumnsNeverCoerced(t *testing.T) {
	tests := []struct {
		header string
		input  string
	}{
		{"key", "12345"},
		{"sonarcloud_org_key", "12345"},
		{"sonarqube_org_key", "999"},
		{"cloud_project_key", "42"},
		{"binding_key", "true"},
		{"binding_key", `["a","b"]`},
	}
	for _, tt := range tests {
		got := coerceCSVValue(tt.header, tt.input)
		if got != tt.input {
			t.Errorf("coerceCSVValue(%q, %q) = %v (%T), want raw string %q", tt.header, tt.input, got, got, tt.input)
		}
	}
}

// Issue #550: LoadCSV must preserve a numeric-looking sonarcloud_org_key
// as a string, not coerce it to float64, or downstream code relying on a
// string type assertion silently drops the organization.
func TestLoadCSV_NumericIdentifierColumnStaysString(t *testing.T) {
	dir := t.TempDir()
	contents := "sonarqube_org_key,sonarcloud_org_key,project_count\norg-a,12345,7\n"
	if err := os.WriteFile(filepath.Join(dir, "organizations.csv"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	rows, err := LoadCSV(dir, "organizations.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	orgKey := rows[0]["sonarcloud_org_key"]
	s, ok := orgKey.(string)
	if !ok {
		t.Fatalf("expected sonarcloud_org_key to be a string, got %v (%T)", orgKey, orgKey)
	}
	if s != "12345" {
		t.Errorf("expected sonarcloud_org_key = %q, got %q", "12345", s)
	}

	// Non-identifier numeric columns keep their existing coercion.
	count := rows[0]["project_count"]
	if f, ok := count.(float64); !ok || f != 7 {
		t.Errorf("expected project_count = float64(7), got %v (%T)", count, count)
	}
}
