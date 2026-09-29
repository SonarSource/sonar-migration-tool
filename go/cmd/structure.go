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
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/spf13/cobra"
)

var structureCmd = &cobra.Command{
	Use:   "structure",
	Short: "Group projects into organizations",
	Long: `Groups projects into organizations based on DevOps Bindings and Server Urls. Outputs organizations and projects as CSVs.

The export directory can be supplied directly via --export_directory or
read from the same JSON config file the extract / migrate commands use
via --config (issue #275). When --config names a single target
organization — either exactly one entry under sonarcloud.organizations,
or target.default_organization in the unified shape — its key is
pre-populated as sonarcloud_org_key (issue #566).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		defer common.LogCommandDuration(slog.Default(), "structure", time.Now())

		exportDir, err := resolveStructureExportDir(cmd)
		if err != nil {
			return err
		}

		orgKey, err := resolveStructureOrgKey(cmd)
		if err != nil {
			return err
		}

		// An empty orgKey leaves sonarcloud_org_key blank, exactly as
		// a run without --config does.
		if err := structure.RunStructure(exportDir, orgKey); err != nil {
			return err
		}
		printExportDirNotice(exportDir)
		return nil
	},
}

// resolveStructureOrgKey returns the SonarQube Cloud organization key to
// pre-populate into every organizations.csv row, or "" when the config
// file names no single target organization.
//
// Both documented ways of naming one org are honoured (#566):
//
//   - shape 3: exactly one entry under sonarcloud.organizations.
//   - shape 4: target.default_organization, which is also what a later
//     `migrate --config` with the same file would stamp on the CSV
//     itself (#281).
//
// Several entries under sonarcloud.organizations means the mapping is
// genuinely per-server, so the operator still has to fill the CSV in by
// hand and nothing is pre-populated.
func resolveStructureOrgKey(cmd *cobra.Command) (string, error) {
	configFile, _ := cmd.Flags().GetString("config")
	if configFile == "" {
		return "", nil
	}

	orgs, err := migrate.LoadSonarCloudOrgsFromConfigFile(configFile)
	if err != nil {
		return "", err
	}
	switch {
	case len(orgs) == 1:
		return orgs[0].Key, nil
	case len(orgs) > 1:
		return "", nil
	}

	return migrate.LoadDefaultOrganizationFromConfigFile(configFile)
}

func init() {
	structureCmd.Flags().String("export_directory", "", "Root directory containing all SonarQube exports")
	structureCmd.Flags().String("config", "", "Path to JSON configuration file (same shape as extract --config); export_directory is read from it, and sonarcloud_org_key is pre-populated when the file names a single target organization (sonarcloud.organizations or target.default_organization)")
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
