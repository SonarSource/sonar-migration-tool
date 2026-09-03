// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/spf13/cobra"
)

// newExtractTestCmd mirrors extractCmd's flag set (cmd/extract.go's init())
// so buildExtractConfig can be exercised in isolation without invoking RunE
// or extractCmd's package-level singleton (which would leak flag state
// across tests).
func newExtractTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "extract"}
	f := cmd.Flags()
	f.String("config", "", "")
	f.String(flagSourceURL, "", "")
	f.String(flagSourceToken, "", "")
	f.String("url", "", "")
	f.String("token", "", "")
	f.String("pem_file_path", "", "")
	f.String("key_file_path", "", "")
	f.String("cert_password", "", "")
	f.String("export_directory", DefaultExportDirectory, "")
	f.String("extract_type", "", "")
	f.Int("concurrency", 0, "")
	f.Int("timeout", 0, "")
	f.String("extract_id", "", "")
	f.String("target_task", "", "")
	f.Bool(flagSkipProjectDataMigration, false, "")
	f.Bool(flagSkipIssueSync, false, "")
	f.String("objects", "", "")
	f.String("project_key", "", "")
	return cmd
}

func TestBuildExtractConfigObjectsFlag(t *testing.T) {
	t.Run("valid list", func(t *testing.T) {
		cmd := newExtractTestCmd()
		if err := cmd.Flags().Set("objects", "quality_gates,groups"); err != nil {
			t.Fatal(err)
		}
		cfg, err := buildExtractConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildExtractConfig: %v", err)
		}
		if cfg.Objects == nil {
			t.Fatal("expected non-nil Objects")
		}
		if !cfg.Objects[common.ObjectQualityGates] || !cfg.Objects[common.ObjectGroups] {
			t.Errorf("expected quality_gates and groups selected, got %+v", cfg.Objects)
		}
		if len(cfg.Objects) != 2 {
			t.Errorf("expected exactly 2 categories, got %+v", cfg.Objects)
		}
	})

	t.Run("alias", func(t *testing.T) {
		cmd := newExtractTestCmd()
		if err := cmd.Flags().Set("objects", "qg,qp"); err != nil {
			t.Fatal(err)
		}
		cfg, err := buildExtractConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildExtractConfig: %v", err)
		}
		if !cfg.Objects[common.ObjectQualityGates] || !cfg.Objects[common.ObjectQualityProfiles] {
			t.Errorf("expected aliases resolved to quality_gates/quality_profiles, got %+v", cfg.Objects)
		}
	})

	t.Run("invalid value names the bad token", func(t *testing.T) {
		cmd := newExtractTestCmd()
		if err := cmd.Flags().Set("objects", "quality_gates,bogus_category"); err != nil {
			t.Fatal(err)
		}
		_, err := buildExtractConfig(cmd, nil)
		if err == nil {
			t.Fatal("expected an error for an invalid --objects value")
		}
		if !strings.Contains(err.Error(), "bogus_category") {
			t.Errorf("expected error to name the invalid token %q, got: %v", "bogus_category", err)
		}
	})

	t.Run("not passed leaves Objects nil (everything)", func(t *testing.T) {
		cmd := newExtractTestCmd()
		cfg, err := buildExtractConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildExtractConfig: %v", err)
		}
		if cfg.Objects != nil {
			t.Errorf("expected nil Objects when --objects isn't passed, got %+v", cfg.Objects)
		}
	})

	t.Run("license_profiles warns but does not error", func(t *testing.T) {
		cmd := newExtractTestCmd()
		if err := cmd.Flags().Set("objects", "license_profiles"); err != nil {
			t.Fatal(err)
		}
		cfg, err := buildExtractConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildExtractConfig: %v", err)
		}
		if !cfg.Objects[common.ObjectLicenseProfiles] {
			t.Errorf("expected license_profiles retained in Objects, got %+v", cfg.Objects)
		}
	})

	t.Run("CLI overrides config file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cfg.json")
		if err := os.WriteFile(path, []byte(`{
			"url": "http://sq.example.com",
			"token": "tok",
			"objects": ["quality_gates"]
		}`), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := newExtractTestCmd()
		if err := cmd.Flags().Set("config", path); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Flags().Set("objects", "groups"); err != nil {
			t.Fatal(err)
		}
		cfg, err := buildExtractConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildExtractConfig: %v", err)
		}
		if cfg.Objects[common.ObjectQualityGates] || !cfg.Objects[common.ObjectGroups] {
			t.Errorf("expected CLI --objects=groups to override config file's quality_gates, got %+v", cfg.Objects)
		}
	})
}

func TestBuildExtractConfigProjectKeyFlag(t *testing.T) {
	t.Run("captured from CLI", func(t *testing.T) {
		cmd := newExtractTestCmd()
		if err := cmd.Flags().Set("project_key", "BANKING_.+"); err != nil {
			t.Fatal(err)
		}
		cfg, err := buildExtractConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildExtractConfig: %v", err)
		}
		if cfg.ProjectKey != "BANKING_.+" {
			t.Errorf("expected ProjectKey %q, got %q", "BANKING_.+", cfg.ProjectKey)
		}
		// Resolution into ProjectKeys happens later, in extractCmd.RunE
		// (needs a live connection) — buildExtractConfig itself must not
		// have attempted it.
		if cfg.ProjectKeys != nil {
			t.Errorf("expected ProjectKeys untouched by buildExtractConfig, got %v", cfg.ProjectKeys)
		}
	})

	t.Run("CLI overrides config file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cfg.json")
		if err := os.WriteFile(path, []byte(`{
			"url": "http://sq.example.com",
			"token": "tok",
			"project_key": "FROM_CONFIG"
		}`), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := newExtractTestCmd()
		if err := cmd.Flags().Set("config", path); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Flags().Set("project_key", "FROM_CLI"); err != nil {
			t.Fatal(err)
		}
		cfg, err := buildExtractConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildExtractConfig: %v", err)
		}
		if cfg.ProjectKey != "FROM_CLI" {
			t.Errorf("expected CLI project_key to win, got %q", cfg.ProjectKey)
		}
	})
}
