// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
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

When organizations.csv carries no sonarcloud_org_key at all, the target
organization is taken from --default_organization or the config file's
target.default_organization and stamped onto every row first, the same
way migrate does it, so the prediction matches the migration it
predicts (issue #566). An already-mapped organizations.csv always wins.

The PDF is written to <export_directory>/predictive_migration_summary.pdf.`,
	RunE: runPredictiveReport,
}

func init() {
	f := predictiveReportCmd.Flags()
	f.String("config", "", "Path to JSON configuration file (same shape as extract --config); export_directory and default_organization are read from it")
	f.String("export_directory", "", "Root directory containing extract data and the mapping CSVs")
	f.String("default_organization", "", "SonarQube Cloud organization key assumed for every project when organizations.csv has no mapping defined; ignored (with a WARN) when any row already carries a sonarcloud_org_key")
}

func runPredictiveReport(cmd *cobra.Command, args []string) error {
	defer common.LogCommandDuration(slog.Default(), "predictive-report", time.Now())

	exportDir, err := resolvePredictiveReportExportDir(cmd)
	if err != nil {
		return err
	}
	defaultOrg, err := resolvePredictiveReportDefaultOrg(cmd)
	if err != nil {
		return err
	}
	pdfPath, err := predict.GeneratePredictiveReport(exportDir, defaultOrg)
	if err != nil {
		return err
	}
	fmt.Printf("Predictive PDF report: %s\n", pdfPath)
	printExportDirNotice(exportDir)
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

	// Default the export directory when neither config nor flag supplied
	// one (issue #247).
	if exportDir == "" {
		exportDir = DefaultExportDirectory
	}
	return exportDir, nil
}

// resolvePredictiveReportDefaultOrg returns the organization key to
// assume for unmapped organizations.csv rows, using migrate's own
// precedence: the CLI flag wins, otherwise the config file's
// target.default_organization (#566).
//
// An empty result is normal and simply means no default is available;
// predict then warns rather than silently reporting nothing migrated.
func resolvePredictiveReportDefaultOrg(cmd *cobra.Command) (string, error) {
	if cmd.Flags().Changed("default_organization") {
		v, _ := cmd.Flags().GetString("default_organization")
		return v, nil
	}

	configFile, _ := cmd.Flags().GetString("config")
	if configFile == "" {
		return "", nil
	}
	org, err := migrate.LoadDefaultOrganizationFromConfigFile(configFile)
	if err != nil {
		return "", fmt.Errorf("loading config %s: %w", configFile, err)
	}
	return org, nil
}
