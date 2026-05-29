package cmd

import (
	"fmt"

	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/sonar-solutions/sonar-migration-tool/internal/predict"
	"github.com/spf13/cobra"
)

var predictiveReportCmd = &cobra.Command{
	Use:   "predictive-report",
	Short: "Generate a predictive migration PDF report without running migrate",
	Long: `Generates a PDF migration summary from the data produced by extract,
structure, and the user-edited mapping CSVs alone — no calls to SonarQube
Cloud, no actual migration. Two classes of outcomes from a real migrate
run cannot be predicted and are omitted (#235):
  - SonarQube Cloud API errors / rate limiting.
  - Global settings (SQC-support detection is dynamic).

The export directory can be supplied directly via --export_directory or
read from the same JSON config file the extract / migrate commands use
via --config (issue #246).

The PDF is written to <export_directory>/predictive_migration_summary.pdf.`,
	RunE: runPredictiveReport,
}

func init() {
	f := predictiveReportCmd.Flags()
	f.String("config", "", "Path to JSON configuration file (same shape as extract --config); export_directory is read from it")
	f.String("export_directory", "", "Root directory containing extract data and the mapping CSVs")
}

func runPredictiveReport(cmd *cobra.Command, args []string) error {
	exportDir, err := resolvePredictiveReportExportDir(cmd)
	if err != nil {
		return err
	}
	pdfPath, err := predict.GeneratePredictiveReport(exportDir)
	if err != nil {
		return err
	}
	fmt.Printf("Predictive PDF report: %s\n", pdfPath)
	return nil
}

// resolvePredictiveReportExportDir decides where to read the migration
// data from, applying the same config-vs-flag precedence the extract
// and migrate commands use:
//
//   - --config <path> loads export_directory from the JSON file (#246).
//   - --export_directory always wins when explicitly set on the CLI.
//   - Empty result → returns an error so the caller surfaces a clear
//     message.
func resolvePredictiveReportExportDir(cmd *cobra.Command) (string, error) {
	exportDir, _ := cmd.Flags().GetString("export_directory")
	configFile, _ := cmd.Flags().GetString("config")

	if configFile != "" {
		cfg, err := extract.LoadExtractConfigFile(configFile)
		if err != nil {
			return "", fmt.Errorf("loading config %s: %w", configFile, err)
		}
		if !cmd.Flags().Changed("export_directory") {
			exportDir = cfg.ExportDirectory
		}
	}

	if exportDir == "" {
		return "", fmt.Errorf("--export_directory is required (either as a CLI flag or via --config)")
	}
	return exportDir, nil
}
