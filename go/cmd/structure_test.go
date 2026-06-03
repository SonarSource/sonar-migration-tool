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
