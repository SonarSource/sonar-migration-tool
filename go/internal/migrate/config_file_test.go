// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const examplesDir = "../../../examples"

func TestLoadMigrateConfigFileShapes(t *testing.T) {
	cases := []struct {
		name string
		file string
		want MigrateConfig
	}{
		{
			name: "shape 1 - flat",
			file: "config-migrate.example.json",
			want: MigrateConfig{
				Token:           "YOUR_SONARCLOUD_TOKEN_HERE",
				EnterpriseKey:   "YOUR_ENTERPRISE_KEY",
				URL:             "https://sonarcloud.io/",
				ExportDirectory: "./files",
				Concurrency:     10,
			},
		},
		{
			name: "shape 2 - command-sectioned",
			file: "config.example.json",
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
			name: "shape 3 - side-sectioned",
			file: "migration-config.example.json",
			want: MigrateConfig{
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
			got, err := LoadMigrateConfigFile(filepath.Join(examplesDir, tc.file))
			if err != nil {
				t.Fatalf("LoadMigrateConfigFile(%s): %v", tc.file, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("MigrateConfig mismatch\n got=%+v\nwant=%+v", got, tc.want)
			}
		})
	}
}

func TestLoadResetConfigFileShapes(t *testing.T) {
	cases := []struct {
		name string
		file string
		want ResetConfig
	}{
		{
			name: "shape 1 - flat",
			file: "config-migrate.example.json",
			want: ResetConfig{
				Token:           "YOUR_SONARCLOUD_TOKEN_HERE",
				EnterpriseKey:   "YOUR_ENTERPRISE_KEY",
				URL:             "https://sonarcloud.io/",
				ExportDirectory: "./files",
				Concurrency:     10,
			},
		},
		{
			name: "shape 2 - command-sectioned",
			file: "config.example.json",
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
			name: "shape 3 - side-sectioned",
			file: "migration-config.example.json",
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
			got, err := LoadResetConfigFile(filepath.Join(examplesDir, tc.file))
			if err != nil {
				t.Fatalf("LoadResetConfigFile(%s): %v", tc.file, err)
			}
			if got != tc.want {
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

// Issue #299: top-level `skip-issue-sync` parses into
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
		{"true", `"skip-issue-sync": true,`, true},
		{"false", `"skip-issue-sync": false,`, false},
		{"string on", `"skip-issue-sync": "on",`, true},
		{"string off", `"skip-issue-sync": "OFF",`, false},
		{"string yes", `"skip-issue-sync": "Yes",`, true},
		{"string no", `"skip-issue-sync": "no",`, false},
		{"numeric 1", `"skip-issue-sync": 1,`, true},
		{"numeric 0", `"skip-issue-sync": 0,`, false},
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
			path := dir + "/skip-issue-sync.json"
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
