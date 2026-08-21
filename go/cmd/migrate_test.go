// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"os"
	"path/filepath"
	"testing"

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
