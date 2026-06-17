// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
