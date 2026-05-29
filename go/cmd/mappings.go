package cmd

import (
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/spf13/cobra"
)

var mappingsCmd = &cobra.Command{
	Use:   "mappings",
	Short: "Map entities to organizations",
	Long:  "Maps groups, permission templates, quality profiles, quality gates, and portfolios to relevant organizations. Outputs CSVs for each entity type.",
	RunE: func(cmd *cobra.Command, args []string) error {
		exportDir, _ := cmd.Flags().GetString("export_directory")
		if err := structure.RunMappings(exportDir); err != nil {
			return err
		}
		printExportDirNotice(exportDir)
		return nil
	},
}

func init() {
	mappingsCmd.Flags().String("export_directory", DefaultExportDirectory, "Root directory containing all SonarQube exports")
}
