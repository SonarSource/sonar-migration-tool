// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"reflect"
	"testing"
)

// Issue #612: the per-project sonarcloud_org_key column overrides the
// organizations.csv mapping for any project SonarQube Cloud cannot bind,
// and is refused for one it can.
func TestResolveProjectOrg(t *testing.T) {
	cases := []struct {
		name        string
		row         map[string]any
		mappedOrg   string
		wantOrg     string
		wantRefused string
	}{
		{
			name:      "empty override falls back to the organizations.csv mapping",
			row:       map[string]any{"key": "p", ProjectOrgColumn: ""},
			mappedOrg: "mapped-org",
			wantOrg:   "mapped-org",
		},
		{
			name:      "absent column falls back to the organizations.csv mapping",
			row:       map[string]any{"key": "p"},
			mappedOrg: "mapped-org",
			wantOrg:   "mapped-org",
		},
		{
			name:      "whitespace-only override falls back",
			row:       map[string]any{"key": "p", ProjectOrgColumn: "   "},
			mappedOrg: "mapped-org",
			wantOrg:   "mapped-org",
		},
		{
			name:      "override wins for an unbound project",
			row:       map[string]any{"key": "p", ProjectOrgColumn: "override-org"},
			mappedOrg: "mapped-org",
			wantOrg:   "override-org",
		},
		{
			name:      "override is trimmed",
			row:       map[string]any{"key": "p", ProjectOrgColumn: "  override-org  "},
			mappedOrg: "mapped-org",
			wantOrg:   "override-org",
		},
		{
			name:      "override wins with no mapping at all",
			row:       map[string]any{"key": "p", ProjectOrgColumn: "override-org"},
			mappedOrg: "",
			wantOrg:   "override-org",
		},
		{
			name: "override refused for a cloud-bound project",
			row: map[string]any{
				"key": "p", ProjectOrgColumn: "override-org",
				"alm": "github", "is_cloud_binding": true,
			},
			mappedOrg:   "mapped-org",
			wantOrg:     "mapped-org",
			wantRefused: "override-org",
		},
		{
			// A GitHub Enterprise Server binding has no SonarQube Cloud
			// counterpart, so the project is free to be dispatched.
			name: "override wins for an on-premise-bound project",
			row: map[string]any{
				"key": "p", ProjectOrgColumn: "override-org",
				"alm": "github", "is_cloud_binding": false,
			},
			mappedOrg: "mapped-org",
			wantOrg:   "override-org",
		},
		{
			name: "is_cloud_binding as a hand-typed string still refuses",
			row: map[string]any{
				"key": "p", ProjectOrgColumn: "override-org",
				"alm": "gitlab", "is_cloud_binding": "TRUE",
			},
			mappedOrg:   "mapped-org",
			wantOrg:     "mapped-org",
			wantRefused: "override-org",
		},
		{
			name:      "a non-string cell is not an override",
			row:       map[string]any{"key": "p", ProjectOrgColumn: 12345.0},
			mappedOrg: "mapped-org",
			wantOrg:   "mapped-org",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			org, refused := ResolveProjectOrg(tc.row, tc.mappedOrg)
			if org != tc.wantOrg {
				t.Errorf("org = %q, want %q", org, tc.wantOrg)
			}
			if refused != tc.wantRefused {
				t.Errorf("refusedOverride = %q, want %q", refused, tc.wantRefused)
			}
		})
	}
}

// The column must be the second one in projects.csv, per #612, so an
// operator opening the file sees it right next to the project key.
func TestProjectCSVHeadersPutOverrideSecond(t *testing.T) {
	headers := structCSVHeaders(reflectTypeOfProject())
	if len(headers) < 2 {
		t.Fatalf("projects.csv has %d columns, want at least 2", len(headers))
	}
	if headers[0] != "key" {
		t.Errorf("first column = %q, want %q", headers[0], "key")
	}
	if headers[1] != ProjectOrgColumn {
		t.Errorf("second column = %q, want %q", headers[1], ProjectOrgColumn)
	}
}

// ExportCSV must write the override column empty for every project, so
// the mapping is inert until an operator edits the file.
func TestExportCSVLeavesProjectOrgOverrideEmpty(t *testing.T) {
	dir := t.TempDir()
	projects := []Project{
		{Key: "p1", Name: "P1", SonarQubeOrgKey: "https://sq.example.invalid"},
		{Key: "p2", Name: "P2", SonarQubeOrgKey: "https://sq.example.invalid"},
	}
	if err := ExportCSV(dir, "projects", projects); err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}
	rows, err := LoadCSV(dir, ProjectsCSVFileName)
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for i, row := range rows {
		v, ok := row[ProjectOrgColumn]
		if !ok {
			t.Fatalf("row %d does not declare the %q column", i, ProjectOrgColumn)
		}
		if s, _ := v.(string); s != "" {
			t.Errorf("row %d: %s = %q, want empty", i, ProjectOrgColumn, s)
		}
	}
}

// reflectTypeOfProject keeps the reflect import out of the test cases
// above.
func reflectTypeOfProject() reflect.Type { return reflect.TypeOf(Project{}) }
