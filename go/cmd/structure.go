package cmd

import (
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/spf13/cobra"
)

var structureCmd = &cobra.Command{
	Use:   "structure",
	Short: "Group projects into organizations",
	Long:  "Groups projects into organizations based on DevOps Bindings and Server Urls. Outputs organizations and projects as CSVs.",
	RunE: func(cmd *cobra.Command, args []string) error {
		exportDir, _ := cmd.Flags().GetString("export_directory")

		// When a config file is provided, pre-populate sonarcloud_org_key if
		// exactly one SonarCloud organization is defined.
		configFile, _ := cmd.Flags().GetString("config")
		if configFile != "" {
			orgs, err := migrate.LoadSonarCloudOrgsFromConfigFile(configFile)
			if err != nil {
				return err
			}
			if len(orgs) == 1 {
				return structure.RunStructure(exportDir, orgs[0].Key)
			}
		}

		return structure.RunStructure(exportDir)
	},
}

func init() {
	structureCmd.Flags().String("export_directory", "/app/files/", "Root directory containing all SonarQube exports")
	structureCmd.Flags().String("config", "", "Path to JSON configuration file (pre-populates sonarcloud_org_key when one org is defined)")
}
