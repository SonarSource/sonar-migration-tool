package cmd

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
)

func newMappingsTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mappings"}
	f := cmd.Flags()
	f.String("config", "", "")
	f.String("export_directory", "", "")
	return cmd
}

func writeMappingsConfigFile(t *testing.T, contents string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "mappings-cfg-*.json")
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
func TestMappings_ConfigFileSuppliesExportDirectory(t *testing.T) {
	path := writeMappingsConfigFile(t, `{
		"url": "https://sqs.example.com",
		"token": "ignored",
		"export_directory": "/cfg/files"
	}`)

	cmd := newMappingsTestCmd()
	_ = cmd.Flags().Set("config", path)

	got, err := resolveMappingsExportDir(cmd)
	if err != nil {
		t.Fatalf("resolveMappingsExportDir: %v", err)
	}
	if got != "/cfg/files" {
		t.Errorf("ExportDirectory: got %q, want /cfg/files", got)
	}
}

// Issue #275: --export_directory on the CLI takes precedence over the
// value in --config.
func TestMappings_FlagOverridesConfigFile(t *testing.T) {
	path := writeMappingsConfigFile(t, `{
		"export_directory": "/cfg/files"
	}`)

	cmd := newMappingsTestCmd()
	_ = cmd.Flags().Set("config", path)
	_ = cmd.Flags().Set("export_directory", "/cli/files")

	got, err := resolveMappingsExportDir(cmd)
	if err != nil {
		t.Fatalf("resolveMappingsExportDir: %v", err)
	}
	if got != "/cli/files" {
		t.Errorf("ExportDirectory: CLI flag should win, got %q", got)
	}
}

// Without --config and without --export_directory, the command falls
// back to the implicit default.
func TestMappings_DefaultsExportDir(t *testing.T) {
	cmd := newMappingsTestCmd()
	got, err := resolveMappingsExportDir(cmd)
	if err != nil {
		t.Fatalf("resolveMappingsExportDir: %v", err)
	}
	if got != DefaultExportDirectory {
		t.Errorf("got %q, want %q", got, DefaultExportDirectory)
	}
}

// Pointing --config at a non-existent file should produce a wrapped
// error so the operator can act on it.
func TestMappings_MissingConfigFileError(t *testing.T) {
	cmd := newMappingsTestCmd()
	_ = cmd.Flags().Set("config", "/path/that/does/not/exist.json")

	if _, err := resolveMappingsExportDir(cmd); err == nil {
		t.Error("expected error when --config points at a missing file")
	}
}
