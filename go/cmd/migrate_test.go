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
	f.String("edition", "", "")
	f.String("url", "", "")
	f.String("run_id", "", "")
	f.Int("concurrency", 0, "")
	f.String("export_directory", "", "")
	f.String("target_task", "", "")
	f.Bool("skip_profiles", false, "")
	f.Bool("debug", false, "")
	f.String("default_organization", "", "")
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
	cfg, err := buildMigrateConfig(cmd, []string{"tok", "ent"})
	if err != nil {
		t.Fatalf("buildMigrateConfig: %v", err)
	}
	if cfg.DefaultOrganization != "" {
		t.Errorf("expected empty, got %q", cfg.DefaultOrganization)
	}
}
