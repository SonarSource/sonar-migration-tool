// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
)

func newStructureTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "structure"}
	f := cmd.Flags()
	f.String("config", "", "")
	f.String("export_directory", "", "")
	return cmd
}

func writeStructureConfigFile(t *testing.T, contents string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "structure-cfg-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(contents); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

// Issue #275: --config should populate export_directory from the JSON.
func TestStructure_ConfigFileSuppliesExportDirectory(t *testing.T) {
	path := writeStructureConfigFile(t, `{
		"url": "https://sqs.example.com",
		"token": "ignored",
		"export_directory": "/cfg/files"
	}`)

	cmd := newStructureTestCmd()
	_ = cmd.Flags().Set("config", path)

	got, err := resolveStructureExportDir(cmd)
	if err != nil {
		t.Fatalf("resolveStructureExportDir: %v", err)
	}
	if got != "/cfg/files" {
		t.Errorf("ExportDirectory: got %q, want /cfg/files", got)
	}
}

// Issue #275: --export_directory on the CLI takes precedence over the
// value in --config.
func TestStructure_FlagOverridesConfigFile(t *testing.T) {
	path := writeStructureConfigFile(t, `{
		"export_directory": "/cfg/files"
	}`)

	cmd := newStructureTestCmd()
	_ = cmd.Flags().Set("config", path)
	_ = cmd.Flags().Set("export_directory", "/cli/files")

	got, err := resolveStructureExportDir(cmd)
	if err != nil {
		t.Fatalf("resolveStructureExportDir: %v", err)
	}
	if got != "/cli/files" {
		t.Errorf("ExportDirectory: CLI flag should win, got %q", got)
	}
}

// Without --config and without --export_directory, the command falls
// back to the implicit default.
func TestStructure_DefaultsExportDir(t *testing.T) {
	cmd := newStructureTestCmd()
	got, err := resolveStructureExportDir(cmd)
	if err != nil {
		t.Fatalf("resolveStructureExportDir: %v", err)
	}
	if got != DefaultExportDirectory {
		t.Errorf("got %q, want %q", got, DefaultExportDirectory)
	}
}

// Pointing --config at a non-existent file should produce a wrapped
// error so the operator can act on it.
func TestStructure_MissingConfigFileError(t *testing.T) {
	cmd := newStructureTestCmd()
	_ = cmd.Flags().Set("config", "/path/that/does/not/exist.json")

	if _, err := resolveStructureExportDir(cmd); err == nil {
		t.Error("expected error when --config points at a missing file")
	}
}

// ---------------------------------------------------------------------------
// sonarcloud_org_key pre-population (#566)
// ---------------------------------------------------------------------------

// The branch that pre-populates sonarcloud_org_key had no cmd-level test,
// which is how #566 shipped: structure --config honoured only the
// side-sectioned shape, so a unified config left the column empty and
// every downstream report called it "Organization skipped".
func TestStructure_ResolveOrgKey(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "side_sectioned_single_org_is_prepopulated",
			content: `{
				"sonarcloud": {
					"organizations": [{ "key": "only-org", "token": "t", "url": "https://sonarcloud.io/" }]
				}
			}`,
			want: "only-org",
		},
		{
			name: "side_sectioned_multiple_orgs_stay_unmapped",
			content: `{
				"sonarcloud": {
					"organizations": [
						{ "key": "org-a", "token": "t", "url": "https://sonarcloud.io/" },
						{ "key": "org-b", "token": "t", "url": "https://sonarcloud.io/" }
					]
				}
			}`,
			want: "",
		},
		{
			name: "unified_target_default_organization_is_prepopulated",
			content: `{
				"export_directory": "/cfg/files",
				"source": { "url": "https://sqs.example.com", "token": "s" },
				"target": { "url": "https://sonarcloud.io/", "token": "t", "default_organization": "my-org" }
			}`,
			want: "my-org",
		},
		{
			name: "unified_without_default_organization_stays_unmapped",
			content: `{
				"source": { "url": "https://sqs.example.com", "token": "s" },
				"target": { "url": "https://sonarcloud.io/", "token": "t" }
			}`,
			want: "",
		},
		{
			name:    "flat_shape_stays_unmapped",
			content: `{"url": "https://sonarcloud.io/", "token": "t", "export_directory": "/cfg/files"}`,
			want:    "",
		},
		{
			name: "side_sectioned_org_list_wins_over_a_stray_target_default",
			content: `{
				"sonarcloud": {
					"organizations": [{ "key": "only-org", "token": "t", "url": "https://sonarcloud.io/" }]
				},
				"target": { "url": "https://sonarcloud.io/", "token": "t", "default_organization": "ignored-org" }
			}`,
			want: "only-org",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newStructureTestCmd()
			_ = cmd.Flags().Set("config", writeStructureConfigFile(t, tc.content))

			got, err := resolveStructureOrgKey(cmd)
			if err != nil {
				t.Fatalf("resolveStructureOrgKey: %v", err)
			}
			if got != tc.want {
				t.Errorf("org key: got %q, want %q", got, tc.want)
			}
		})
	}
}

// Without --config nothing is pre-populated and no file is read.
func TestStructure_ResolveOrgKeyWithoutConfig(t *testing.T) {
	got, err := resolveStructureOrgKey(newStructureTestCmd())
	if err != nil {
		t.Fatalf("resolveStructureOrgKey: %v", err)
	}
	if got != "" {
		t.Errorf("org key: got %q, want empty", got)
	}
}

func TestStructure_ResolveOrgKeyMissingConfigFileError(t *testing.T) {
	cmd := newStructureTestCmd()
	_ = cmd.Flags().Set("config", "/path/that/does/not/exist.json")

	if _, err := resolveStructureOrgKey(cmd); err == nil {
		t.Error("expected error when --config points at a missing file")
	}
}
