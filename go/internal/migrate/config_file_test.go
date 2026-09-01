// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// The legacy config shapes no longer ship as example files (the examples/
// directory was consolidated in #408), but the parser still supports them.
// These fixtures keep one representative document per shape inline so the
// parser is exercised independently of the docs examples.
const (
	flatShapeJSON = `{
  "token": "YOUR_SONARCLOUD_TOKEN_HERE",
  "enterprise_key": "YOUR_ENTERPRISE_KEY",
  "url": "https://sonarcloud.io/",
  "export_directory": "./files",
  "concurrency": 10
}`
	commandSectionedShapeJSON = `{
  "extract": { "url": "http://localhost:9000", "token": "x" },
  "migrate": {
    "token": "YOUR_SONARCLOUD_TOKEN_HERE",
    "enterprise_key": "YOUR_ENTERPRISE_KEY",
    "url": "https://sonarcloud.io/",
    "edition": "enterprise",
    "export_directory": "./files",
    "concurrency": 10
  }
}`
	sideSectionedShapeJSON = `{
  "sonarqube": { "url": "http://localhost:9000", "token": "x" },
  "sonarcloud": {
    "url": "https://sonarcloud.io/",
    "token": "YOUR_SONARCLOUD_ADMIN_TOKEN_HERE",
    "enterprise_key": "YOUR_ENTERPRISE_KEY_HERE",
    "org_key": "YOUR_TARGET_ORGANIZATION_KEY_HERE"
  },
  "settings": { "export_directory": "./files", "concurrency": 10, "timeout": 60 }
}`
)

// writeConfigFixture writes content to a temp file and returns its path.
func writeConfigFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing config fixture: %v", err)
	}
	return path
}

func TestLoadMigrateConfigFileShapes(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    MigrateConfig
	}{
		{
			name:    "shape 1 - flat",
			content: flatShapeJSON,
			want: MigrateConfig{
				Token:           "YOUR_SONARCLOUD_TOKEN_HERE",
				EnterpriseKey:   "YOUR_ENTERPRISE_KEY",
				URL:             "https://sonarcloud.io/",
				ExportDirectory: "./files",
				Concurrency:     10,
			},
		},
		{
			name:    "shape 2 - command-sectioned",
			content: commandSectionedShapeJSON,
			want: MigrateConfig{
				Token:           "YOUR_SONARCLOUD_TOKEN_HERE",
				EnterpriseKey:   "YOUR_ENTERPRISE_KEY",
				URL:             "https://sonarcloud.io/",
				Edition:         "enterprise",
				ExportDirectory: "./files",
				Concurrency:     10,
			},
		},
		{
			name:    "shape 3 - side-sectioned",
			content: sideSectionedShapeJSON,
			want: MigrateConfig{
				Token:           "YOUR_SONARCLOUD_ADMIN_TOKEN_HERE",
				EnterpriseKey:   "YOUR_ENTERPRISE_KEY_HERE",
				URL:             "https://sonarcloud.io/",
				ExportDirectory: "./files",
				Concurrency:     10,
				// #383: settings.timeout now propagates to MigrateConfig.
				Timeout: 60,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LoadMigrateConfigFile(writeConfigFixture(t, tc.content))
			if err != nil {
				t.Fatalf("LoadMigrateConfigFile(%s): %v", tc.name, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("MigrateConfig mismatch\n got=%+v\nwant=%+v", got, tc.want)
			}
		})
	}
}

func TestLoadResetConfigFileShapes(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    ResetConfig
	}{
		{
			name:    "shape 1 - flat",
			content: flatShapeJSON,
			want: ResetConfig{
				Token:           "YOUR_SONARCLOUD_TOKEN_HERE",
				EnterpriseKey:   "YOUR_ENTERPRISE_KEY",
				URL:             "https://sonarcloud.io/",
				ExportDirectory: "./files",
				Concurrency:     10,
			},
		},
		{
			name:    "shape 2 - command-sectioned",
			content: commandSectionedShapeJSON,
			want: ResetConfig{
				Token:           "YOUR_SONARCLOUD_TOKEN_HERE",
				EnterpriseKey:   "YOUR_ENTERPRISE_KEY",
				URL:             "https://sonarcloud.io/",
				Edition:         "enterprise",
				ExportDirectory: "./files",
				Concurrency:     10,
			},
		},
		{
			name:    "shape 3 - side-sectioned",
			content: sideSectionedShapeJSON,
			want: ResetConfig{
				Token:           "YOUR_SONARCLOUD_ADMIN_TOKEN_HERE",
				EnterpriseKey:   "YOUR_ENTERPRISE_KEY_HERE",
				URL:             "https://sonarcloud.io/",
				ExportDirectory: "./files",
				Concurrency:     10,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LoadResetConfigFile(writeConfigFixture(t, tc.content))
			if err != nil {
				t.Fatalf("LoadResetConfigFile(%s): %v", tc.name, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ResetConfig mismatch\n got=%+v\nwant=%+v", got, tc.want)
			}
		})
	}
}

func TestLoadConfigFileErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		_, err := LoadMigrateConfigFile("/nonexistent/path/does-not-exist.json")
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "bad-*.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("{not valid json"); err != nil {
			t.Fatal(err)
		}
		f.Close()
		_, err = LoadMigrateConfigFile(f.Name())
		if err == nil {
			t.Fatal("expected error for malformed JSON, got nil")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "empty-*.json")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		_, err = LoadResetConfigFile(f.Name())
		if err == nil {
			t.Fatal("expected error for empty file, got nil")
		}
	})
}

// #266 unified shape: migrate pulls from "target", with top-level
// concurrency / export_directory as defaults. "source" is ignored.
func TestLoadMigrateConfigFileUnifiedShape(t *testing.T) {
	body := `{
  "concurrency": 15,
  "timeout": 90,
  "export_directory": "./out",
  "source": {
    "url": "ignored-by-migrate",
    "token": "ignored"
  },
  "target": {
    "url": "https://sonarcloud.io/",
    "token": "sqc_token",
    "enterprise_key": "ent-key",
    "edition": "enterprise",
    "run_id": "2026-05-31-01",
    "target_task": "createProjects"
  }
}`
	dir := t.TempDir()
	path := dir + "/unified.json"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadMigrateConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.URL != "https://sonarcloud.io/" || cfg.Token != "sqc_token" {
		t.Errorf("URL/Token: %+v", cfg)
	}
	if cfg.EnterpriseKey != "ent-key" || cfg.Edition != "enterprise" {
		t.Errorf("ent/edition: %+v", cfg)
	}
	if cfg.ExportDirectory != "./out" || cfg.Concurrency != 15 {
		t.Errorf("globals: %+v", cfg)
	}
	if cfg.RunID != "2026-05-31-01" || cfg.TargetTask != "createProjects" {
		t.Errorf("target fields: %+v", cfg)
	}
}

// Issue #299: top-level `skip_issue_sync` parses into
// MigrateConfig.SkipIssueSync one-for-one (no inversion). Defaults to
// false (sync happens). Verifies every accepted alias from the
// FlexibleBool type plus case variations.
func TestLoadMigrateConfigFile_SkipIssueSync(t *testing.T) {
	cases := []struct {
		name      string
		bodyField string
		wantSkip  bool
	}{
		{"absent (default)", "", false},
		{"true", `"skip_issue_sync": true,`, true},
		{"false", `"skip_issue_sync": false,`, false},
		{"string on", `"skip_issue_sync": "on",`, true},
		{"string off", `"skip_issue_sync": "OFF",`, false},
		{"string yes", `"skip_issue_sync": "Yes",`, true},
		{"string no", `"skip_issue_sync": "no",`, false},
		{"numeric 1", `"skip_issue_sync": 1,`, true},
		{"numeric 0", `"skip_issue_sync": 0,`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{
  ` + c.bodyField + `
  "target": {
    "url": "https://sonarcloud.io/",
    "token": "t"
  }
}`
			dir := t.TempDir()
			path := dir + "/skip_issue_sync.json"
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadMigrateConfigFile(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.SkipIssueSync != c.wantSkip {
				t.Errorf("SkipIssueSync: got %v, want %v", cfg.SkipIssueSync, c.wantSkip)
			}
		})
	}
}

// Issue #527: top-level `fast_sync` parses into MigrateConfig.FastSync
// one-for-one. Defaults to false (every hotspot is tagged and back-linked).
func TestLoadMigrateConfigFile_FastSync(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantFast bool
	}{
		{"absent (default)", `{"target": {"url": "u", "token": "t"}}`, false},
		{"top-level true", `{"fast_sync": true, "target": {"url": "u", "token": "t"}}`, true},
		{"top-level false", `{"fast_sync": false, "target": {"url": "u", "token": "t"}}`, false},
		{"top-level string on", `{"fast_sync": "on", "target": {"url": "u", "token": "t"}}`, true},
		{
			"target overrides top-level (target true, top false)",
			`{"fast_sync": false, "target": {"url": "u", "token": "t", "fast_sync": true}}`,
			true,
		},
		{
			"target overrides top-level (target false, top true)",
			`{"fast_sync": true, "target": {"url": "u", "token": "t", "fast_sync": false}}`,
			false,
		},
		{
			"target unset falls back to top-level",
			`{"fast_sync": true, "target": {"url": "u", "token": "t"}}`,
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := dir + "/fast_sync.json"
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadMigrateConfigFile(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.FastSync != c.wantFast {
				t.Errorf("FastSync: got %v, want %v", cfg.FastSync, c.wantFast)
			}
		})
	}
}

// Issue #527: fast_sync also parses in the "migrate"-sectioned shape, with
// the outer (command-sectioned) field winning when both are set.
func TestLoadMigrateConfigFile_FastSync_MigrateSectionedShape(t *testing.T) {
	body := `{
  "fast_sync": true,
  "migrate": {
    "url": "u", "token": "t",
    "fast_sync": false
  }
}`
	dir := t.TempDir()
	path := dir + "/fast_sync_sectioned.json"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadMigrateConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.FastSync {
		t.Errorf("FastSync: got false, want true (outer wins)")
	}
}

// Issue #281: target.default_organization parses into
// MigrateConfig.DefaultOrganization.
func TestLoadMigrateConfigFileUnifiedShape_DefaultOrganization(t *testing.T) {
	body := `{
  "target": {
    "url": "https://sonarcloud.io/",
    "token": "t",
    "default_organization": "my-single-org"
  }
}`
	dir := t.TempDir()
	path := dir + "/unified.json"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadMigrateConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DefaultOrganization != "my-single-org" {
		t.Errorf("DefaultOrganization: got %q, want my-single-org", cfg.DefaultOrganization)
	}
}

// Target block overrides top-level concurrency when set.
func TestLoadMigrateConfigFileUnifiedShape_TargetOverridesGlobals(t *testing.T) {
	body := `{
  "concurrency": 10,
  "target": {
    "url": "u", "token": "t",
    "concurrency": 25
  }
}`
	dir := t.TempDir()
	path := dir + "/unified.json"
	os.WriteFile(path, []byte(body), 0o644)
	cfg, _ := LoadMigrateConfigFile(path)
	if cfg.Concurrency != 25 {
		t.Errorf("override: concurrency=%d", cfg.Concurrency)
	}
}

// #383: Timeout must flow into MigrateConfig from every documented
// config-file shape so the migrate phase honors the operator's value
// instead of falling back to the SDK default (60s). The unified
// shape's top-level timeout supplies a default; an explicit
// target.timeout overrides it (same precedence as concurrency).
func TestLoadMigrateConfigFile_TimeoutAllShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			name: "flat",
			body: `{"url":"u","token":"t","timeout":15}`,
			want: 15,
		},
		{
			name: "command-sectioned (migrate block)",
			body: `{"migrate":{"url":"u","token":"t","timeout":20}}`,
			want: 20,
		},
		{
			name: "side-sectioned (sonarcloud + settings)",
			body: `{"sonarcloud":{"url":"u","token":"t"},"settings":{"timeout":30}}`,
			want: 30,
		},
		{
			name: "unified — top-level only",
			body: `{"timeout":45,"target":{"url":"u","token":"t"}}`,
			want: 45,
		},
		{
			name: "unified — target overrides top-level",
			body: `{"timeout":10,"target":{"url":"u","token":"t","timeout":99}}`,
			want: 99,
		},
		{
			name: "unified — target only",
			body: `{"target":{"url":"u","token":"t","timeout":55}}`,
			want: 55,
		},
		{
			name: "unified — missing leaves Timeout at zero (applyDefaults will fill 60)",
			body: `{"target":{"url":"u","token":"t"}}`,
			want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := dir + "/cfg.json"
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadMigrateConfigFile(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.Timeout != c.want {
				t.Errorf("Timeout: got %d, want %d (body=%s)", cfg.Timeout, c.want, c.body)
			}
		})
	}
}

// #383: applyDefaults must fill Timeout with the SDK default (60s)
// when the config left it at zero, so callers that previously relied
// on the SDK fallback keep working.
func TestMigrateConfig_ApplyDefaultsFillsTimeout(t *testing.T) {
	cfg := MigrateConfig{}
	cfg.applyDefaults()
	if cfg.Timeout != 60 {
		t.Errorf("Timeout default: got %d, want 60", cfg.Timeout)
	}

	// Explicit value is preserved.
	cfg = MigrateConfig{Timeout: 5}
	cfg.applyDefaults()
	if cfg.Timeout != 5 {
		t.Errorf("Timeout preservation: got %d, want 5", cfg.Timeout)
	}
}

// LoadResetConfigFile must also recognise the unified shape and pull
// from "target".
func TestLoadResetConfigFileUnifiedShape(t *testing.T) {
	body := `{
  "export_directory": "./out",
  "target": {
    "url": "https://sonarcloud.io/",
    "token": "sqc_token",
    "enterprise_key": "ent-key"
  }
}`
	dir := t.TempDir()
	path := dir + "/unified.json"
	os.WriteFile(path, []byte(body), 0o644)
	cfg, err := LoadResetConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.URL != "https://sonarcloud.io/" || cfg.Token != "sqc_token" ||
		cfg.EnterpriseKey != "ent-key" || cfg.ExportDirectory != "./out" {
		t.Errorf("reset cfg: %+v", cfg)
	}
}

// #550: toResetConfig must populate ResetConfig.ConfirmedOrgs from a
// flat config file's top-level confirmed_orgs field — an additive path
// for config-driven / programmatic callers that don't go through
// cmd/reset.go's interactive confirmation prompt.
func TestLoadResetConfigFile_ConfirmedOrgsFlatShape(t *testing.T) {
	content := `{
  "token": "tok",
  "enterprise_key": "ent",
  "confirmed_orgs": ["cloud-a", "cloud-b"]
}`
	cfg, err := LoadResetConfigFile(writeConfigFixture(t, content))
	if err != nil {
		t.Fatalf("LoadResetConfigFile: %v", err)
	}
	want := []string{"cloud-a", "cloud-b"}
	if !reflect.DeepEqual(cfg.ConfirmedOrgs, want) {
		t.Errorf("ConfirmedOrgs: got %+v, want %+v", cfg.ConfirmedOrgs, want)
	}
}

// confirmed_orgs is also honored inside a shape-2 "migrate" section.
func TestLoadResetConfigFile_ConfirmedOrgsFromNestedMigrateSection(t *testing.T) {
	content := `{
  "migrate": {
    "token": "tok",
    "enterprise_key": "ent",
    "confirmed_orgs": ["inner-org"]
  }
}`
	cfg, err := LoadResetConfigFile(writeConfigFixture(t, content))
	if err != nil {
		t.Fatalf("LoadResetConfigFile: %v", err)
	}
	if !reflect.DeepEqual(cfg.ConfirmedOrgs, []string{"inner-org"}) {
		t.Errorf("ConfirmedOrgs: got %+v, want [inner-org]", cfg.ConfirmedOrgs)
	}
}

// An outer-level confirmed_orgs wins over a nested "migrate" section's
// value, mirroring skip_issue_sync's outer-wins-else-inner precedence.
func TestLoadResetConfigFile_ConfirmedOrgsOuterWinsOverNestedMigrate(t *testing.T) {
	content := `{
  "confirmed_orgs": ["outer-org"],
  "migrate": {
    "token": "tok",
    "enterprise_key": "ent",
    "confirmed_orgs": ["inner-org"]
  }
}`
	cfg, err := LoadResetConfigFile(writeConfigFixture(t, content))
	if err != nil {
		t.Fatalf("LoadResetConfigFile: %v", err)
	}
	if !reflect.DeepEqual(cfg.ConfirmedOrgs, []string{"outer-org"}) {
		t.Errorf("ConfirmedOrgs: got %+v, want [outer-org]", cfg.ConfirmedOrgs)
	}
}

// Absent confirmed_orgs leaves ResetConfig.ConfirmedOrgs nil — RunReset
// fails closed on that (#550), which this parser-level test does not
// itself exercise, but the zero value must round-trip as nil, not an
// empty-but-non-nil slice, so callers can distinguish "unset" cleanly.
func TestLoadResetConfigFile_ConfirmedOrgsAbsentIsNil(t *testing.T) {
	cfg, err := LoadResetConfigFile(writeConfigFixture(t, flatShapeJSON))
	if err != nil {
		t.Fatalf("LoadResetConfigFile: %v", err)
	}
	if cfg.ConfirmedOrgs != nil {
		t.Errorf("ConfirmedOrgs: got %+v, want nil", cfg.ConfirmedOrgs)
	}
}

// Issue #303: top-level `skip_project_data_migration` parses into
// MigrateConfig.SkipProjectDataMigration one-for-one (no inversion).
// Defaults to false (data is migrated). Every FlexibleBool alias is
// accepted, case-insensitive.
func TestLoadMigrateConfigFile_SkipProjectDataMigration(t *testing.T) {
	cases := []struct {
		name      string
		bodyField string
		wantSkip  bool
	}{
		{"absent (default)", "", false},
		{"true", `"skip_project_data_migration": true,`, true},
		{"false", `"skip_project_data_migration": false,`, false},
		{"string on", `"skip_project_data_migration": "on",`, true},
		{"string OFF", `"skip_project_data_migration": "OFF",`, false},
		{"string Yes", `"skip_project_data_migration": "Yes",`, true},
		{"numeric 1", `"skip_project_data_migration": 1,`, true},
		{"numeric 0", `"skip_project_data_migration": 0,`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{
  ` + c.bodyField + `
  "target": {
    "url": "https://sonarcloud.io/",
    "token": "t"
  }
}`
			dir := t.TempDir()
			path := dir + "/skip-project-data.json"
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadMigrateConfigFile(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.SkipProjectDataMigration != c.wantSkip {
				t.Errorf("SkipProjectDataMigration: got %v, want %v", cfg.SkipProjectDataMigration, c.wantSkip)
			}
		})
	}
}

// #536: "objects" and "project_key" are top-level-only fields, present in
// every documented shape (mirrors extract's TestLoadExtractConfigFileObjectsAndProjectKey).
// These tests round-trip them through LoadMigrateConfigFile into
// MigrateConfig.Objects (validated + alias-resolved via common.ParseObjects)
// and MigrateConfig.ProjectKeyFilter, following the same
// TestLoadMigrateConfigFileUnifiedShape_TargetOverridesGlobals-style
// per-shape coverage used elsewhere in this file.
func TestLoadMigrateConfigFileObjectsAndProjectKey(t *testing.T) {
	t.Run("flat shape", func(t *testing.T) {
		body := `{
  "token": "tok",
  "url": "https://sonarcloud.io/",
  "enterprise_key": "ent",
  "objects": ["quality_gates", "qp"],
  "project_key": "BANKING_.+"
}`
		cfg, err := LoadMigrateConfigFile(writeConfigFixture(t, body))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if !cfg.Objects[common.ObjectQualityGates] || !cfg.Objects[common.ObjectQualityProfiles] {
			t.Errorf("expected quality_gates + quality_profiles (via qp alias), got %+v", cfg.Objects)
		}
		if len(cfg.Objects) != 2 {
			t.Errorf("expected exactly 2 categories, got %+v", cfg.Objects)
		}
		if cfg.ProjectKeyFilter != "BANKING_.+" {
			t.Errorf("ProjectKeyFilter: got %q", cfg.ProjectKeyFilter)
		}
	})

	t.Run("unified shape (top-level, sibling of target)", func(t *testing.T) {
		body := `{
  "objects": ["groups"],
  "project_key": "my-project",
  "target": { "url": "u", "token": "t" }
}`
		cfg, err := LoadMigrateConfigFile(writeConfigFixture(t, body))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if !cfg.Objects[common.ObjectGroups] || len(cfg.Objects) != 1 {
			t.Errorf("Objects: got %+v", cfg.Objects)
		}
		if cfg.ProjectKeyFilter != "my-project" {
			t.Errorf("ProjectKeyFilter: got %q", cfg.ProjectKeyFilter)
		}
	})

	t.Run("command-sectioned shape: outer wins over nested migrate", func(t *testing.T) {
		body := `{
  "objects": ["portfolios"],
  "project_key": "outer-pattern",
  "migrate": {
    "token": "tok", "url": "https://sonarcloud.io/", "enterprise_key": "ent",
    "objects": ["groups"],
    "project_key": "inner-pattern"
  }
}`
		cfg, err := LoadMigrateConfigFile(writeConfigFixture(t, body))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if !cfg.Objects[common.ObjectPortfolios] || len(cfg.Objects) != 1 {
			t.Errorf("expected outer objects to win, got %+v", cfg.Objects)
		}
		if cfg.ProjectKeyFilter != "outer-pattern" {
			t.Errorf("expected outer project_key to win, got %q", cfg.ProjectKeyFilter)
		}
	})

	t.Run("command-sectioned shape: falls back to nested migrate when outer absent", func(t *testing.T) {
		body := `{
  "migrate": {
    "token": "tok", "url": "https://sonarcloud.io/", "enterprise_key": "ent",
    "objects": ["groups"],
    "project_key": "inner-pattern"
  }
}`
		cfg, err := LoadMigrateConfigFile(writeConfigFixture(t, body))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if !cfg.Objects[common.ObjectGroups] || len(cfg.Objects) != 1 {
			t.Errorf("expected nested objects to be used, got %+v", cfg.Objects)
		}
		if cfg.ProjectKeyFilter != "inner-pattern" {
			t.Errorf("expected nested project_key to be used, got %q", cfg.ProjectKeyFilter)
		}
	})

	t.Run("side-sectioned shape", func(t *testing.T) {
		body := `{
  "sonarcloud": { "url": "https://sonarcloud.io/", "token": "tok", "enterprise_key": "ent" },
  "objects": ["license_profiles"],
  "project_key": "sc-pattern"
}`
		cfg, err := LoadMigrateConfigFile(writeConfigFixture(t, body))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if !cfg.Objects[common.ObjectLicenseProfiles] || len(cfg.Objects) != 1 {
			t.Errorf("Objects: got %+v", cfg.Objects)
		}
		if cfg.ProjectKeyFilter != "sc-pattern" {
			t.Errorf("ProjectKeyFilter: got %q", cfg.ProjectKeyFilter)
		}
	})

	t.Run("invalid objects value errors", func(t *testing.T) {
		body := `{"token":"tok","url":"https://sonarcloud.io/","enterprise_key":"ent","objects":["bogus"]}`
		if _, err := LoadMigrateConfigFile(writeConfigFixture(t, body)); err == nil {
			t.Error("expected an error for an unrecognized objects value")
		}
	})

	t.Run("absent objects means nil (everything)", func(t *testing.T) {
		body := `{"token":"tok","url":"https://sonarcloud.io/","enterprise_key":"ent"}`
		cfg, err := LoadMigrateConfigFile(writeConfigFixture(t, body))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Objects != nil {
			t.Errorf("expected nil Objects (everything) when absent, got %+v", cfg.Objects)
		}
	})
}
