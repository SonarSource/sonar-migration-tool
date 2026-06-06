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
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/spf13/cobra"
)

var mappingsCmd = &cobra.Command{
	Use:   "mappings",
	Short: "Map entities to organizations",
	Long: `Maps groups, permission templates, quality profiles, quality gates, and portfolios to relevant organizations. Outputs CSVs for each entity type.

The export directory can be supplied directly via --export_directory or
read from the same JSON config file the extract / migrate commands use
via --config (issue #275).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		defer common.LogCommandDuration(slog.Default(), "mappings", time.Now())

		exportDir, err := resolveMappingsExportDir(cmd)
		if err != nil {
			return err
		}
		if err := structure.RunMappings(exportDir); err != nil {
			return err
		}
		printExportDirNotice(exportDir)
		return nil
	},
}

func init() {
	f := mappingsCmd.Flags()
	f.String("config", "", "Path to JSON configuration file (same shape as extract --config); export_directory is read from it")
	f.String("export_directory", "", "Root directory containing all SonarQube exports")
}

// resolveMappingsExportDir applies the same config-vs-flag precedence
// the extract, migrate, and predictive-report commands use:
//
//   - --config <path> loads export_directory from the JSON file (#275).
//   - --export_directory always wins when explicitly set on the CLI.
//   - Empty result falls back to DefaultExportDirectory.
func resolveMappingsExportDir(cmd *cobra.Command) (string, error) {
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
		exportDir = DefaultExportDirectory
	}
	return exportDir, nil
}
