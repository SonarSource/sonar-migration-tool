// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// The legacy config shapes are no longer shipped as example files (the
// examples/ directory was consolidated in #408), but the parser still
// supports them. These fixtures keep one representative document per shape
// inline so the parser is exercised independently of the docs examples.
const (
	flatExtractJSON = `{
  "url": "http://localhost:9000",
  "token": "YOUR_SONARQUBE_TOKEN_HERE",
  "export_directory": "./files",
  "concurrency": 10,
  "timeout": 60
}`
	commandSectionedExtractJSON = `{
  "extract": {
    "url": "http://localhost:9000",
    "token": "YOUR_SONARQUBE_TOKEN_HERE",
    "export_directory": "./files",
    "extract_type": "all",
    "concurrency": 10,
    "timeout": 60
  },
  "migrate": { "url": "https://sonarcloud.io/", "token": "y" }
}`
	sideSectionedExtractJSON = `{
  "sonarqube": { "url": "http://localhost:9000", "token": "YOUR_SONARQUBE_ADMIN_TOKEN_HERE" },
  "sonarcloud": { "url": "https://sonarcloud.io/", "token": "y" },
  "settings": { "export_directory": "./files", "concurrency": 10, "timeout": 60 }
}`
)

func TestLoadExtractConfigFileShapes(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    ExtractConfig
	}{
		{
			name:    "shape 1 - flat",
			content: flatExtractJSON,
			want: ExtractConfig{
				URL:             "http://localhost:9000",
				Token:           "YOUR_SONARQUBE_TOKEN_HERE",
				ExportDirectory: "./files",
				Concurrency:     10,
				Timeout:         60,
				// #554: an absent history_min_interval_days loads as the
				// "caller said nothing" sentinel, not 0 (0 means "no spacing").
				HistoryMinIntervalDays: HistoryUnset,
			},
		},
		{
			name:    "shape 2 - command-sectioned",
			content: commandSectionedExtractJSON,
			want: ExtractConfig{
				URL:             "http://localhost:9000",
				Token:           "YOUR_SONARQUBE_TOKEN_HERE",
				ExportDirectory: "./files",
				ExtractType:     "all",
				Concurrency:     10,
				Timeout:         60,
				// #554: an absent history_min_interval_days loads as the
				// "caller said nothing" sentinel, not 0 (0 means "no spacing").
				HistoryMinIntervalDays: HistoryUnset,
			},
		},
		{
			name:    "shape 3 - side-sectioned",
			content: sideSectionedExtractJSON,
			want: ExtractConfig{
				URL:             "http://localhost:9000",
				Token:           "YOUR_SONARQUBE_ADMIN_TOKEN_HERE",
				ExportDirectory: "./files",
				Concurrency:     10,
				Timeout:         60,
				// #554: an absent history_min_interval_days loads as the
				// "caller said nothing" sentinel, not 0 (0 means "no spacing").
				HistoryMinIntervalDays: HistoryUnset,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}
			got, err := LoadExtractConfigFile(path)
			if err != nil {
				t.Fatalf("LoadExtractConfigFile(%s): %v", tc.name, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ExtractConfig mismatch\n got=%+v\nwant=%+v", got, tc.want)
			}
		})
	}
}

func TestLoadExtractConfigFileSnakeCaseFields(t *testing.T) {
	// Regression for issue #158: every snake_case field in the flat
	// shape must round-trip into the corresponding CamelCase struct
	// field. Before the fix, json.Unmarshal silently dropped these
	// because ExtractConfig had no json: tags, so users had to type
	// "exportDirectory" / "extractType" / etc. instead of the
	// documented snake_case keys.
	f, err := os.CreateTemp(t.TempDir(), "extract-cfg-*.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{
		"url": "http://sq.example.com:9000",
		"token": "tok",
		"export_directory": "/data/files",
		"extract_type": "all",
		"pem_file_path": "/certs/client.pem",
		"key_file_path": "/certs/client.key",
		"cert_password": "secret",
		"concurrency": 25,
		"timeout": 90,
		"extract_id": "resume-me",
		"target_task": "getRules",
		"skip_project_data_migration": true,
		"skip_issue_sync": true
	}`)
	f.Close()

	got, err := LoadExtractConfigFile(f.Name())
	if err != nil {
		t.Fatalf("LoadExtractConfigFile: %v", err)
	}

	want := ExtractConfig{
		URL:                      "http://sq.example.com:9000",
		Token:                    "tok",
		ExportDirectory:          "/data/files",
		ExtractType:              "all",
		PEMFilePath:              "/certs/client.pem",
		KeyFilePath:              "/certs/client.key",
		CertPassword:             "secret",
		Concurrency:              25,
		Timeout:                  90,
		ExtractID:                "resume-me",
		TargetTask:               "getRules",
		SkipProjectDataMigration: true,
		SkipIssueSync:            true,
		// #554: an absent history_min_interval_days loads as the
		// "caller said nothing" sentinel, not 0 (0 means "no spacing").
		HistoryMinIntervalDays: HistoryUnset,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snake_case round-trip mismatch\n got=%+v\nwant=%+v", got, want)
	}
}

func TestLoadExtractConfigFileErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		_, err := LoadExtractConfigFile("/nonexistent/path/does-not-exist.json")
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "bad-*.json")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("{not valid")
		f.Close()
		_, err = LoadExtractConfigFile(f.Name())
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
		_, err = LoadExtractConfigFile(f.Name())
		if err == nil {
			t.Fatal("expected error for empty file, got nil")
		}
	})
}

// #266 unified shape: extract pulls from "source", with top-level
// concurrency / timeout / export_directory as defaults.
func TestLoadExtractConfigFileUnifiedShape(t *testing.T) {
	body := `{
  "concurrency": 12,
  "timeout": 90,
  "export_directory": "./out",
  "source": {
    "url": "https://sq.example.com",
    "token": "squ_token",
    "extract_type": "all",
    "pem_file_path": "/pem",
    "extract_id": "extract-7",
    "target_task": "getProjects"
  },
  "target": {
    "url": "ignored-by-extract",
    "token": "ignored"
  }
}`
	dir := t.TempDir()
	path := dir + "/unified.json"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadExtractConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.URL != "https://sq.example.com" || cfg.Token != "squ_token" {
		t.Errorf("URL/Token: %+v", cfg)
	}
	if cfg.ExportDirectory != "./out" {
		t.Errorf("ExportDirectory: %q", cfg.ExportDirectory)
	}
	if cfg.Concurrency != 12 || cfg.Timeout != 90 {
		t.Errorf("top-level defaults: concurrency=%d timeout=%d", cfg.Concurrency, cfg.Timeout)
	}
	if cfg.ExtractType != "all" || cfg.PEMFilePath != "/pem" ||
		cfg.ExtractID != "extract-7" || cfg.TargetTask != "getProjects" {
		t.Errorf("source-block fields: %+v", cfg)
	}
}

// Source block overrides top-level concurrency / timeout when set.
func TestLoadExtractConfigFileUnifiedShape_SourceOverridesGlobals(t *testing.T) {
	body := `{
  "concurrency": 10,
  "timeout": 60,
  "source": {
    "url": "u", "token": "t",
    "concurrency": 25,
    "timeout": 120
  }
}`
	dir := t.TempDir()
	path := dir + "/unified.json"
	os.WriteFile(path, []byte(body), 0o644)
	cfg, _ := LoadExtractConfigFile(path)
	if cfg.Concurrency != 25 || cfg.Timeout != 120 {
		t.Errorf("override: concurrency=%d timeout=%d", cfg.Concurrency, cfg.Timeout)
	}
}

// #536: "objects" and "project_key" are top-level-only fields, present in
// every documented shape. These tests round-trip them the same way the
// TestLoadExtractConfigFileUnifiedShape* tests above round-trip the rest
// of the unified shape's fields, plus the flat and command-sectioned
// shapes where the "global level" placement matters most.
// The following TestLoadExtractConfigFileObjectsAndProjectKey_* functions
// were originally one function with a t.Run per shape; split into
// independent top-level tests to keep cognitive complexity low (each
// covers exactly one config-file shape).

func TestLoadExtractConfigFileObjectsAndProjectKey_FlatShape(t *testing.T) {
	body := `{
  "url": "http://sq.example.com",
  "token": "tok",
  "objects": ["quality_gates", "qp"],
  "project_key": "BANKING_.+"
}`
	path := filepath.Join(t.TempDir(), "flat.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadExtractConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Objects[common.ObjectQualityGates] || !cfg.Objects[common.ObjectQualityProfiles] {
		t.Errorf("expected quality_gates + quality_profiles (via qp alias), got %+v", cfg.Objects)
	}
	if len(cfg.Objects) != 2 {
		t.Errorf("expected exactly 2 categories, got %+v", cfg.Objects)
	}
	if cfg.ProjectKey != "BANKING_.+" {
		t.Errorf("ProjectKey: got %q", cfg.ProjectKey)
	}
}

func TestLoadExtractConfigFileObjectsAndProjectKey_UnifiedShape(t *testing.T) {
	body := `{
  "objects": ["groups"],
  "project_key": "my-project",
  "source": { "url": "u", "token": "t" }
}`
	path := filepath.Join(t.TempDir(), "unified.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadExtractConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Objects[common.ObjectGroups] || len(cfg.Objects) != 1 {
		t.Errorf("Objects: got %+v", cfg.Objects)
	}
	if cfg.ProjectKey != "my-project" {
		t.Errorf("ProjectKey: got %q", cfg.ProjectKey)
	}
}

func TestLoadExtractConfigFileObjectsAndProjectKey_SideSectionedShape(t *testing.T) {
	body := `{
  "sonarqube": { "url": "http://sq.example.com", "token": "tok" },
  "objects": ["portfolios"],
  "project_key": "PORTFOLIO_PROJ"
}`
	path := filepath.Join(t.TempDir(), "side.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadExtractConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Objects[common.ObjectPortfolios] || len(cfg.Objects) != 1 {
		t.Errorf("Objects: got %+v", cfg.Objects)
	}
	if cfg.ProjectKey != "PORTFOLIO_PROJ" {
		t.Errorf("ProjectKey: got %q", cfg.ProjectKey)
	}
}

func TestLoadExtractConfigFileObjectsAndProjectKey_CommandSectionedGlobalWins(t *testing.T) {
	body := `{
  "objects": ["settings"],
  "project_key": "GLOBAL_PROJ",
  "extract": {
    "url": "http://sq.example.com",
    "token": "tok",
    "objects": ["projects"],
    "project_key": "NESTED_PROJ"
  }
}`
	path := filepath.Join(t.TempDir(), "sectioned.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadExtractConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Objects[common.ObjectSettings] || len(cfg.Objects) != 1 {
		t.Errorf("expected global-level objects (settings) to win, got %+v", cfg.Objects)
	}
	if cfg.ProjectKey != "GLOBAL_PROJ" {
		t.Errorf("expected global-level project_key to win, got %q", cfg.ProjectKey)
	}
}

func TestLoadExtractConfigFileObjectsAndProjectKey_CommandSectionedFallsBackToNested(t *testing.T) {
	body := `{
  "extract": {
    "url": "http://sq.example.com",
    "token": "tok",
    "objects": ["projects"],
    "project_key": "NESTED_PROJ"
  }
}`
	path := filepath.Join(t.TempDir(), "sectioned-fallback.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadExtractConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Objects[common.ObjectProjects] || len(cfg.Objects) != 1 {
		t.Errorf("expected fallback to nested objects (projects), got %+v", cfg.Objects)
	}
	if cfg.ProjectKey != "NESTED_PROJ" {
		t.Errorf("expected fallback to nested project_key, got %q", cfg.ProjectKey)
	}
}

func TestLoadExtractConfigFileObjectsAndProjectKey_NoObjectsMeansEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-objects.json")
	if err := os.WriteFile(path, []byte(`{"url": "u", "token": "t"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadExtractConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Objects != nil {
		t.Errorf("expected nil Objects, got %+v", cfg.Objects)
	}
}

func TestLoadExtractConfigFileObjectsAndProjectKey_InvalidObjectsValueErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-objects.json")
	if err := os.WriteFile(path, []byte(`{"url": "u", "token": "t", "objects": ["not_a_real_category"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadExtractConfigFile(path)
	if err == nil {
		t.Fatal("expected an error for an invalid objects value")
	}
	if !strings.Contains(err.Error(), "not_a_real_category") {
		t.Errorf("expected error to name the invalid token, got: %v", err)
	}
}
