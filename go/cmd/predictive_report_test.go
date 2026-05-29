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

// Without --config and without --export_directory, the command should
// surface a clear error instead of silently picking an empty dir.
func TestPredictiveReport_RequiresExportDir(t *testing.T) {
	cmd := newPredictiveReportTestCmd()
	if _, err := resolvePredictiveReportExportDir(cmd); err == nil {
		t.Error("expected error when neither --export_directory nor --config supplied")
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
