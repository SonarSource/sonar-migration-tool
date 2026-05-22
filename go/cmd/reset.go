package cmd

import (
	"fmt"

	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/spf13/cobra"
)

var resetCmd = &cobra.Command{
	Use:   "reset <token> <enterprise_key>",
	Short: "Reset a SonarQube Cloud Enterprise",
	Long:  "Resets a SonarQube Cloud Enterprise back to its original state. Warning: this will delete everything in every organization within the enterprise.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		edition, _ := cmd.Flags().GetString("edition")
		url, _ := cmd.Flags().GetString("url")
		concurrency, _ := cmd.Flags().GetInt("concurrency")
		exportDir, _ := cmd.Flags().GetString("export_directory")
		debug, _ := cmd.Flags().GetBool("debug")

		cfg := migrate.ResetConfig{
			Token:           args[0],
			EnterpriseKey:   args[1],
			Edition:         edition,
			URL:             url,
			Concurrency:     concurrency,
			ExportDirectory: exportDir,
			Debug:           debug,
		}

		fmt.Println("WARNING: This will delete everything in every organization within the enterprise.")
		return migrate.RunReset(cmd.Context(), cfg)
	},
}

func init() {
	f := resetCmd.Flags()
	f.String("edition", "enterprise", "SonarQube Cloud license edition")
	f.String("url", "https://sonarcloud.io/", "URL of SonarQube Cloud")
	f.Int("concurrency", 25, "Maximum number of concurrent requests")
	f.String("export_directory", "/app/files/", "Directory to place all interim files")
	f.Bool("debug", false, "Enable debug-level logging (verbose request payloads, more detail per task)")
}
