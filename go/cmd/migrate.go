package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report/summary"
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate [token] [enterprise_key]",
	Short: "Migrate configurations to SonarQube Cloud",
	Long: `Migrate SonarQube Server configurations to SonarQube Cloud.
User must run structure and mappings commands and add SonarQube Cloud
organization keys to organizations.csv.`,
	Args: cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := buildMigrateConfig(cmd, args)
		if err != nil {
			return err
		}
		if cfg.Token == "" || cfg.EnterpriseKey == "" {
			return fmt.Errorf("TOKEN and ENTERPRISE_KEY are required (either as arguments or in config file)")
		}
		runID, err := migrate.RunMigrate(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		runDir := filepath.Join(cfg.ExportDirectory, runID)
		if pdfPath, pdfErr := summary.GeneratePDFReport(runDir, cfg.ExportDirectory, cfg.ExportDirectory); pdfErr == nil {
			fmt.Printf("PDF summary report: %s\n", pdfPath)
		}
		return nil
	},
}

func init() {
	f := migrateCmd.Flags()
	f.String("config", "", "Path to JSON configuration file")
	f.String("edition", "", "SonarQube Cloud license edition")
	f.String("url", "", "URL of SonarQube Cloud")
	f.String("run_id", "", "ID of a run to resume in case of failures")
	f.Int("concurrency", 0, "Maximum number of concurrent requests")
	f.String("export_directory", "", "Root directory containing all SonarQube exports")
	f.String("target_task", "", "Name of a specific migration task to complete")
	f.Bool("skip_profiles", false, "Skip quality profile migration/provisioning in SonarQube Cloud")
	f.Bool("include_scan_history", false, "Import scan history (issues, metrics) into SonarQube Cloud projects")
}

func buildMigrateConfig(cmd *cobra.Command, args []string) (migrate.MigrateConfig, error) {
	var cfg migrate.MigrateConfig

	// Load config file if specified.
	configFile, _ := cmd.Flags().GetString("config")
	if configFile != "" {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return cfg, fmt.Errorf("reading config file: %w", err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parsing config file: %w", err)
		}
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
	overrideString(cmd, "run_id", &cfg.RunID)
	overrideString(cmd, "export_directory", &cfg.ExportDirectory)
	overrideString(cmd, "target_task", &cfg.TargetTask)
	overrideInt(cmd, "concurrency", &cfg.Concurrency)
	if cmd.Flags().Changed("skip_profiles") {
		cfg.SkipProfiles, _ = cmd.Flags().GetBool("skip_profiles")
	}
	if cmd.Flags().Changed("include_scan_history") {
		cfg.IncludeScanHistory, _ = cmd.Flags().GetBool("include_scan_history")
	}

	return cfg, nil
}
