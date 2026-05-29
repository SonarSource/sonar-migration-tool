package cmd

import (
	"fmt"

	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/spf13/cobra"
)

var resetCmd = &cobra.Command{
	Use:   "reset [token] [enterprise_key]",
	Short: "Reset a SonarQube Cloud Enterprise",
	Long:  "Resets a SonarQube Cloud Enterprise back to its original state. Warning: this will delete everything in every organization within the enterprise.",
	Args:  cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := buildResetConfig(cmd, args)
		if err != nil {
			return err
		}
		if cfg.Token == "" || cfg.EnterpriseKey == "" {
			return fmt.Errorf("TOKEN and ENTERPRISE_KEY are required (either as arguments or in config file)")
		}

		fmt.Println("WARNING: This will delete everything in every organization within the enterprise.")
		if err := migrate.RunReset(cmd.Context(), cfg); err != nil {
			return err
		}
		printExportDirNotice(cfg.ExportDirectory)
		return nil
	},
}

func init() {
	f := resetCmd.Flags()
	f.String("config", "", "Path to JSON configuration file (same format as migrate --config)")
	f.String("edition", "enterprise", "SonarQube Cloud license edition")
	f.String("url", "https://sonarcloud.io/", "URL of SonarQube Cloud")
	f.Int("concurrency", 25, "Maximum number of concurrent requests")
	f.String("export_directory", DefaultExportDirectory, "Directory to place all interim files")
	f.Bool("debug", false, "Enable debug-level logging (verbose request payloads, more detail per task)")
}

func buildResetConfig(cmd *cobra.Command, args []string) (migrate.ResetConfig, error) {
	var cfg migrate.ResetConfig

	configFile, _ := cmd.Flags().GetString("config")
	if configFile != "" {
		loaded, err := migrate.LoadResetConfigFile(configFile)
		if err != nil {
			return cfg, err
		}
		cfg = loaded
	}

	// CLI args override config file.
	if len(args) > 0 && args[0] != "" {
		cfg.Token = args[0]
	}
	if len(args) > 1 && args[1] != "" {
		cfg.EnterpriseKey = args[1]
	}

	// Flags override everything.
	overrideString(cmd, "edition", &cfg.Edition)
	overrideString(cmd, "url", &cfg.URL)
	overrideString(cmd, "export_directory", &cfg.ExportDirectory)
	overrideInt(cmd, "concurrency", &cfg.Concurrency)
	if cmd.Flags().Changed("debug") {
		cfg.Debug, _ = cmd.Flags().GetBool("debug")
	}

	// Default the export directory when neither config nor flag supplied
	// one (issue #247).
	if cfg.ExportDirectory == "" {
		cfg.ExportDirectory = DefaultExportDirectory
	}

	return cfg, nil
}
