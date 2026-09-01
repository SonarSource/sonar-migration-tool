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

func newMigrateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "migrate"}
	f := cmd.Flags()
	f.String("config", "", "")
	f.String(flagTargetToken, "", "")
	f.String(flagTargetURL, "", "")
	f.String("enterprise_key", "", "")
	f.String("edition", "", "")
	f.String("run_id", "", "")
	f.Int("concurrency", 0, "")
	f.String("export_directory", "", "")
	f.String("target_task", "", "")
	f.Bool("skip_profiles", false, "")
	f.Bool("debug", false, "")
	f.Bool(flagFastSync, false, "")
	f.String("default_organization", "", "")
	f.String("project_key_pattern", "", "")
	f.String("objects", "", "")
	f.String(flagProjectKey, "", "")
	// Deprecated aliases — registered so tests can exercise back-compat (#406).
	f.String("token", "", "")
	f.String("url", "", "")
	return cmd
}

func writeMigrateConfig(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Issue #281: CLI --default_organization overrides the config file
// value.
func TestBuildMigrateConfig_DefaultOrganization_CLIOverridesConfig(t *testing.T) {
	path := writeMigrateConfig(t, `{
		"target": {
			"url": "https://sonarcloud.io/",
			"token": "t",
			"enterprise_key": "ent",
			"default_organization": "config-default"
		}
	}`)
	cmd := newMigrateTestCmd()
	_ = cmd.Flags().Set("config", path)
	_ = cmd.Flags().Set("default_organization", "cli-default")

	cfg, err := buildMigrateConfig(cmd, nil)
	if err != nil {
		t.Fatalf("buildMigrateConfig: %v", err)
	}
	if cfg.DefaultOrganization != "cli-default" {
		t.Errorf("CLI flag should override config, got %q", cfg.DefaultOrganization)
	}
}

// Issue #527: --fast_sync overrides the config file's fast_sync value.
func TestBuildMigrateConfig_FastSync_CLIOverridesConfig(t *testing.T) {
	path := writeMigrateConfig(t, `{
		"fast_sync": false,
		"target": {
			"url": "https://sonarcloud.io/",
			"token": "t"
		}
	}`)
	cmd := newMigrateTestCmd()
	_ = cmd.Flags().Set("config", path)
	_ = cmd.Flags().Set(flagFastSync, "true")

	cfg, err := buildMigrateConfig(cmd, nil)
	if err != nil {
		t.Fatalf("buildMigrateConfig: %v", err)
	}
	if !cfg.FastSync {
		t.Error("CLI --fast_sync should override config file's false, got FastSync=false")
	}
}

// Config file value is used when --fast_sync is absent.
func TestBuildMigrateConfig_FastSync_ConfigOnly(t *testing.T) {
	path := writeMigrateConfig(t, `{
		"fast_sync": true,
		"target": {
			"url": "https://sonarcloud.io/",
			"token": "t"
		}
	}`)
	cmd := newMigrateTestCmd()
	_ = cmd.Flags().Set("config", path)

	cfg, err := buildMigrateConfig(cmd, nil)
	if err != nil {
		t.Fatalf("buildMigrateConfig: %v", err)
	}
	if !cfg.FastSync {
		t.Error("expected config file's fast_sync=true to be used, got FastSync=false")
	}
}

// Config file value is used when --default_organization is absent.
func TestBuildMigrateConfig_DefaultOrganization_ConfigOnly(t *testing.T) {
	path := writeMigrateConfig(t, `{
		"target": {
			"url": "https://sonarcloud.io/",
			"token": "t",
			"enterprise_key": "ent",
			"default_organization": "config-default"
		}
	}`)
	cmd := newMigrateTestCmd()
	_ = cmd.Flags().Set("config", path)

	cfg, err := buildMigrateConfig(cmd, nil)
	if err != nil {
		t.Fatalf("buildMigrateConfig: %v", err)
	}
	if cfg.DefaultOrganization != "config-default" {
		t.Errorf("expected config value, got %q", cfg.DefaultOrganization)
	}
}

// Neither config nor CLI → empty.
func TestBuildMigrateConfig_DefaultOrganization_Unset(t *testing.T) {
	cmd := newMigrateTestCmd()
	_ = cmd.Flags().Set(flagTargetToken, "tok")
	_ = cmd.Flags().Set("enterprise_key", "ent")
	cfg, err := buildMigrateConfig(cmd, nil)
	if err != nil {
		t.Fatalf("buildMigrateConfig: %v", err)
	}
	if cfg.DefaultOrganization != "" {
		t.Errorf("expected empty, got %q", cfg.DefaultOrganization)
	}
	if cfg.Token != "tok" || cfg.EnterpriseKey != "ent" {
		t.Errorf("expected token/enterprise_key from flags, got %q / %q", cfg.Token, cfg.EnterpriseKey)
	}
}

// Issue #406: the deprecated --url/--token flags must still work so existing
// scripts don't break. The new --target_url/--target_token wins when both
// are passed.
func TestBuildMigrateConfig_DeprecatedFlagsStillWork(t *testing.T) {
	cmd := newMigrateTestCmd()
	_ = cmd.Flags().Set("token", "legacy-tok")
	_ = cmd.Flags().Set("url", "https://legacy.example.com/")
	_ = cmd.Flags().Set("enterprise_key", "ent")
	cfg, err := buildMigrateConfig(cmd, nil)
	if err != nil {
		t.Fatalf("buildMigrateConfig: %v", err)
	}
	if cfg.Token != "legacy-tok" {
		t.Errorf("deprecated --token should still populate cfg.Token, got %q", cfg.Token)
	}
	if cfg.URL != "https://legacy.example.com/" {
		t.Errorf("deprecated --url should still populate cfg.URL, got %q", cfg.URL)
	}
}

func TestBuildMigrateConfig_NewFlagsWinOverDeprecated(t *testing.T) {
	cmd := newMigrateTestCmd()
	_ = cmd.Flags().Set("token", "legacy-tok")
	_ = cmd.Flags().Set("url", "https://legacy.example.com/")
	_ = cmd.Flags().Set(flagTargetToken, "new-tok")
	_ = cmd.Flags().Set(flagTargetURL, "https://new.example.com/")
	_ = cmd.Flags().Set("enterprise_key", "ent")
	cfg, err := buildMigrateConfig(cmd, nil)
	if err != nil {
		t.Fatalf("buildMigrateConfig: %v", err)
	}
	if cfg.Token != "new-tok" {
		t.Errorf("--target_token should win over deprecated --token, got %q", cfg.Token)
	}
	if cfg.URL != "https://new.example.com/" {
		t.Errorf("--target_url should win over deprecated --url, got %q", cfg.URL)
	}
}

// #536, mirrors cmd/extract_test.go's TestBuildExtractConfigObjectsFlag.
func TestBuildMigrateConfigObjectsFlag(t *testing.T) {
	t.Run("valid list", func(t *testing.T) {
		cmd := newMigrateTestCmd()
		if err := cmd.Flags().Set("objects", "quality_gates,groups"); err != nil {
			t.Fatal(err)
		}
		cfg, err := buildMigrateConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildMigrateConfig: %v", err)
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
		cmd := newMigrateTestCmd()
		if err := cmd.Flags().Set("objects", "qg,qp"); err != nil {
			t.Fatal(err)
		}
		cfg, err := buildMigrateConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildMigrateConfig: %v", err)
		}
		if !cfg.Objects[common.ObjectQualityGates] || !cfg.Objects[common.ObjectQualityProfiles] {
			t.Errorf("expected aliases resolved to quality_gates/quality_profiles, got %+v", cfg.Objects)
		}
	})

	t.Run("invalid value names the bad token", func(t *testing.T) {
		cmd := newMigrateTestCmd()
		if err := cmd.Flags().Set("objects", "quality_gates,bogus_category"); err != nil {
			t.Fatal(err)
		}
		_, err := buildMigrateConfig(cmd, nil)
		if err == nil {
			t.Fatal("expected an error for an invalid --objects value")
		}
		if !strings.Contains(err.Error(), "bogus_category") {
			t.Errorf("expected error to name the invalid token %q, got: %v", "bogus_category", err)
		}
	})

	t.Run("not passed leaves Objects nil (everything)", func(t *testing.T) {
		cmd := newMigrateTestCmd()
		cfg, err := buildMigrateConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildMigrateConfig: %v", err)
		}
		if cfg.Objects != nil {
			t.Errorf("expected nil Objects when --objects isn't passed, got %+v", cfg.Objects)
		}
	})

	t.Run("license_profiles warns but does not error", func(t *testing.T) {
		cmd := newMigrateTestCmd()
		if err := cmd.Flags().Set("objects", "license_profiles"); err != nil {
			t.Fatal(err)
		}
		cfg, err := buildMigrateConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildMigrateConfig: %v", err)
		}
		if !cfg.Objects[common.ObjectLicenseProfiles] {
			t.Errorf("expected license_profiles retained in Objects, got %+v", cfg.Objects)
		}
	})

	t.Run("CLI overrides config file", func(t *testing.T) {
		path := writeMigrateConfig(t, `{
			"target": { "url": "https://sonarcloud.io/", "token": "t" },
			"objects": ["quality_gates"]
		}`)
		cmd := newMigrateTestCmd()
		_ = cmd.Flags().Set("config", path)
		_ = cmd.Flags().Set("objects", "groups")
		cfg, err := buildMigrateConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildMigrateConfig: %v", err)
		}
		if cfg.Objects[common.ObjectQualityGates] || !cfg.Objects[common.ObjectGroups] {
			t.Errorf("expected CLI --objects=groups to override config file's quality_gates, got %+v", cfg.Objects)
		}
	})
}

// #536, mirrors cmd/extract_test.go's TestBuildExtractConfigProjectKeyFlag,
// plus migrate-specific behavior: the pattern is validated eagerly (unlike
// extract, which only resolves it later against a live connection), and
// it's a no-op — not an error — when an active --objects selection
// excludes the "projects" category.
func TestBuildMigrateConfigProjectKeyFlag(t *testing.T) {
	t.Run("captured from CLI when no objects filter", func(t *testing.T) {
		cmd := newMigrateTestCmd()
		if err := cmd.Flags().Set(flagProjectKey, "BANKING_.+"); err != nil {
			t.Fatal(err)
		}
		cfg, err := buildMigrateConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildMigrateConfig: %v", err)
		}
		if cfg.ProjectKeyFilter != "BANKING_.+" {
			t.Errorf("expected ProjectKeyFilter %q, got %q", "BANKING_.+", cfg.ProjectKeyFilter)
		}
	})

	t.Run("CLI overrides config file", func(t *testing.T) {
		path := writeMigrateConfig(t, `{
			"target": { "url": "https://sonarcloud.io/", "token": "t" },
			"project_key": "FROM_CONFIG"
		}`)
		cmd := newMigrateTestCmd()
		_ = cmd.Flags().Set("config", path)
		_ = cmd.Flags().Set(flagProjectKey, "FROM_CLI")
		cfg, err := buildMigrateConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildMigrateConfig: %v", err)
		}
		if cfg.ProjectKeyFilter != "FROM_CLI" {
			t.Errorf("expected CLI project_key to win, got %q", cfg.ProjectKeyFilter)
		}
	})

	t.Run("invalid regex aborts before any API call", func(t *testing.T) {
		cmd := newMigrateTestCmd()
		if err := cmd.Flags().Set(flagProjectKey, "["); err != nil {
			t.Fatal(err)
		}
		if _, err := buildMigrateConfig(cmd, nil); err == nil {
			t.Fatal("expected an error for an invalid --project_key regexp")
		}
	})

	t.Run("no-op (not an error) when objects excludes projects", func(t *testing.T) {
		cmd := newMigrateTestCmd()
		_ = cmd.Flags().Set("objects", "settings")
		_ = cmd.Flags().Set(flagProjectKey, "BANKING_.+")
		cfg, err := buildMigrateConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildMigrateConfig: %v", err)
		}
		if cfg.ProjectKeyFilter != "" {
			t.Errorf("expected ProjectKeyFilter to stay empty when objects excludes projects, got %q", cfg.ProjectKeyFilter)
		}
	})

	t.Run("applied when objects includes projects", func(t *testing.T) {
		cmd := newMigrateTestCmd()
		_ = cmd.Flags().Set("objects", "projects,settings")
		_ = cmd.Flags().Set(flagProjectKey, "BANKING_.+")
		cfg, err := buildMigrateConfig(cmd, nil)
		if err != nil {
			t.Fatalf("buildMigrateConfig: %v", err)
		}
		if cfg.ProjectKeyFilter != "BANKING_.+" {
			t.Errorf("expected ProjectKeyFilter to be set when projects is selected, got %q", cfg.ProjectKeyFilter)
		}
	})
}
