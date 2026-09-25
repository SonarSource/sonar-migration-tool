// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
)

func newPredictiveReportTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "predictive-report"}
	f := cmd.Flags()
	f.String("config", "", "")
	f.String("export_directory", "", "")
	f.String("default_organization", "", "")
	return cmd
}

func writePredictiveConfigFile(t *testing.T, contents string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "predict-cfg-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(contents); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

// Issue #246: --config should populate export_directory from the JSON.
func TestPredictiveReport_ConfigFileSuppliesExportDirectory(t *testing.T) {
	path := writePredictiveConfigFile(t, `{
		"url": "https://sqs.example.com",
		"token": "ignored",
		"export_directory": "/cfg/files"
	}`)

	cmd := newPredictiveReportTestCmd()
	_ = cmd.Flags().Set("config", path)

	got, err := resolvePredictiveReportExportDir(cmd)
	if err != nil {
		t.Fatalf("resolvePredictiveReportExportDir: %v", err)
	}
	if got != "/cfg/files" {
		t.Errorf("ExportDirectory: got %q, want /cfg/files", got)
	}
}

// Issue #246: --export_directory on the CLI takes precedence over the
// value in --config — same precedence the extract / migrate commands use.
func TestPredictiveReport_FlagOverridesConfigFile(t *testing.T) {
	path := writePredictiveConfigFile(t, `{
		"export_directory": "/cfg/files"
	}`)

	cmd := newPredictiveReportTestCmd()
	_ = cmd.Flags().Set("config", path)
	_ = cmd.Flags().Set("export_directory", "/cli/files")

	got, err := resolvePredictiveReportExportDir(cmd)
	if err != nil {
		t.Fatalf("resolvePredictiveReportExportDir: %v", err)
	}
	if got != "/cli/files" {
		t.Errorf("ExportDirectory: CLI flag should win, got %q", got)
	}
}

// Without --config and without --export_directory, the command falls
// back to the implicit default "./migration-files" (issue #247).
func TestPredictiveReport_DefaultsExportDir(t *testing.T) {
	cmd := newPredictiveReportTestCmd()
	got, err := resolvePredictiveReportExportDir(cmd)
	if err != nil {
		t.Fatalf("resolvePredictiveReportExportDir: %v", err)
	}
	if got != DefaultExportDirectory {
		t.Errorf("got %q, want %q", got, DefaultExportDirectory)
	}
}

// Pointing --config at a non-existent file should produce a wrapped
// error so the operator can act on it.
func TestPredictiveReport_MissingConfigFileError(t *testing.T) {
	cmd := newPredictiveReportTestCmd()
	_ = cmd.Flags().Set("config", "/path/that/does/not/exist.json")

	if _, err := resolvePredictiveReportExportDir(cmd); err == nil {
		t.Error("expected error when --config points at a missing file")
	}
}

// ---------------------------------------------------------------------------
// default organization resolution (#566)
// ---------------------------------------------------------------------------

// #566: predictive-report never applied default_organization, so a
// unified config that named its target only that way produced a PDF
// claiming 0 entities would migrate. It now uses migrate's precedence:
// the CLI flag wins, otherwise target.default_organization.
func TestPredictiveReport_ResolveDefaultOrg(t *testing.T) {
	cases := []struct {
		name    string
		content string
		setFlag bool
		flag    string
		want    string
	}{
		{
			name:    "unified_config_supplies_it",
			content: `{"target": {"url": "https://sonarcloud.io/", "token": "t", "default_organization": "my-org"}}`,
			want:    "my-org",
		},
		{
			name:    "flag_wins_over_config",
			content: `{"target": {"url": "https://sonarcloud.io/", "token": "t", "default_organization": "cfg-org"}}`,
			setFlag: true,
			flag:    "cli-org",
			want:    "cli-org",
		},
		{
			name:    "explicitly_empty_flag_wins_over_config",
			content: `{"target": {"url": "https://sonarcloud.io/", "token": "t", "default_organization": "cfg-org"}}`,
			setFlag: true,
			flag:    "",
			want:    "",
		},
		{
			name:    "absent_in_config_is_empty",
			content: `{"export_directory": "/cfg/files"}`,
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newPredictiveReportTestCmd()
			_ = cmd.Flags().Set("config", writePredictiveConfigFile(t, tc.content))
			if tc.setFlag {
				_ = cmd.Flags().Set("default_organization", tc.flag)
			}

			got, err := resolvePredictiveReportDefaultOrg(cmd)
			if err != nil {
				t.Fatalf("resolvePredictiveReportDefaultOrg: %v", err)
			}
			if got != tc.want {
				t.Errorf("default organization: got %q, want %q", got, tc.want)
			}
		})
	}
}

// The flag alone works with no config file at all.
func TestPredictiveReport_ResolveDefaultOrgFlagWithoutConfig(t *testing.T) {
	cmd := newPredictiveReportTestCmd()
	_ = cmd.Flags().Set("default_organization", "cli-org")

	got, err := resolvePredictiveReportDefaultOrg(cmd)
	if err != nil {
		t.Fatalf("resolvePredictiveReportDefaultOrg: %v", err)
	}
	if got != "cli-org" {
		t.Errorf("default organization: got %q, want cli-org", got)
	}
}

// Neither flag nor config means no default, which is a valid state:
// predict warns instead of silently reporting nothing migrated.
func TestPredictiveReport_ResolveDefaultOrgAbsentIsEmpty(t *testing.T) {
	got, err := resolvePredictiveReportDefaultOrg(newPredictiveReportTestCmd())
	if err != nil {
		t.Fatalf("resolvePredictiveReportDefaultOrg: %v", err)
	}
	if got != "" {
		t.Errorf("default organization: got %q, want empty", got)
	}
}

func TestPredictiveReport_ResolveDefaultOrgMissingConfigFileError(t *testing.T) {
	cmd := newPredictiveReportTestCmd()
	_ = cmd.Flags().Set("config", "/path/that/does/not/exist.json")

	if _, err := resolvePredictiveReportDefaultOrg(cmd); err == nil {
		t.Error("expected error when --config points at a missing file")
	}
}
