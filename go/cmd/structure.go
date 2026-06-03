package cmd

import (
	"fmt"

	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/spf13/cobra"
)

var structureCmd = &cobra.Command{
	Use:   "structure",
	Short: "Group projects into organizations",
	Long: `Groups projects into organizations based on DevOps Bindings and Server Urls. Outputs organizations and projects as CSVs.

The export directory can be supplied directly via --export_directory or
read from the same JSON config file the extract / migrate commands use
via --config (issue #275). When --config defines exactly one SonarCloud
organization, its key is pre-populated as sonarcloud_org_key.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		exportDir, err := resolveStructureExportDir(cmd)
		if err != nil {
			return err
		}

		configFile, _ := cmd.Flags().GetString("config")
		if configFile != "" {
			orgs, err := migrate.LoadSonarCloudOrgsFromConfigFile(configFile)
			if err != nil {
				return err
			}
			if len(orgs) == 1 {
				if err := structure.RunStructure(exportDir, orgs[0].Key); err != nil {
					return err
				}
				printExportDirNotice(exportDir)
				return nil
			}
		}

		if err := structure.RunStructure(exportDir); err != nil {
			return err
		}
		printExportDirNotice(exportDir)
		return nil
	},
}

func init() {
	structureCmd.Flags().String("export_directory", "", "Root directory containing all SonarQube exports")
	structureCmd.Flags().String("config", "", "Path to JSON configuration file (same shape as extract --config); export_directory is read from it, and sonarcloud_org_key is pre-populated when one org is defined")
}

// resolveStructureExportDir applies the same config-vs-flag precedence
// the extract, mappings, and predictive-report commands use:
//
//   - --config <path> loads export_directory from the JSON file (#275).
//   - --export_directory always wins when explicitly set on the CLI.
//   - Empty result falls back to DefaultExportDirectory.
func resolveStructureExportDir(cmd *cobra.Command) (string, error) {
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
