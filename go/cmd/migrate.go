// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"fmt"
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
		printExportDirNotice(cfg.ExportDirectory)
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
	f.Bool(flagSkipIssueSync, false, "Skip the final per-issue and per-hotspot metadata sync (#299). Same semantics as the skip_issue_sync config-file field — defaults to false (sync happens); pass the flag to skip.")
	f.Bool(flagSkipProjectDataMigration, false, "Skip the entire project-data migration: importScanHistory and the trailing per-issue/per-hotspot sync (#303). Defaults to false (data is migrated); pass the flag to skip.")
	f.String("default_organization", "", "SonarQube Cloud organization to migrate every project into when organizations.csv has no mapping defined. Ignored if any mapping is present.")
	f.StringSlice("exclude_branches", nil, "Glob patterns for non-main branches to skip during scan history import (e.g. feature/*,bugfix/*)")
}

func buildMigrateConfig(cmd *cobra.Command, args []string) (migrate.MigrateConfig, error) {
	var cfg migrate.MigrateConfig

	// Load config file if specified. Supports flat, command-sectioned, and
	// side-sectioned shapes (issue #176).
	configFile, _ := cmd.Flags().GetString("config")
	if configFile != "" {
		loaded, err := migrate.LoadMigrateConfigFile(configFile)
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
	overrideString(cmd, "run_id", &cfg.RunID)
	overrideString(cmd, "export_directory", &cfg.ExportDirectory)
	overrideString(cmd, "target_task", &cfg.TargetTask)
	overrideString(cmd, "default_organization", &cfg.DefaultOrganization)
	overrideInt(cmd, "concurrency", &cfg.Concurrency)
	if cmd.Flags().Changed("skip_profiles") {
		cfg.SkipProfiles, _ = cmd.Flags().GetBool("skip_profiles")
	}
	// --skip_issue_sync explicitly turns off the trailing sync. The
	// flag always wins over the config-file skip_issue_sync field.
	// One-way: --skip_issue_sync=false on the CLI does NOT undo a
	// config-file skip_issue_sync: true.
	if cmd.Flags().Changed(flagSkipIssueSync) {
		v, _ := cmd.Flags().GetBool(flagSkipIssueSync)
		if v {
			cfg.SkipIssueSync = true
		}
	}
	// --skip_project_data_migration is the wider opt-out: it covers
	// importScanHistory AND the trailing sync pair. Same one-way
	// override semantics. #303.
	if cmd.Flags().Changed(flagSkipProjectDataMigration) {
		v, _ := cmd.Flags().GetBool(flagSkipProjectDataMigration)
		if v {
			cfg.SkipProjectDataMigration = true
		}
	}
	if cmd.Flags().Changed("debug") {
		cfg.Debug, _ = cmd.Flags().GetBool("debug")
	}
	if cmd.Flags().Changed("exclude_branches") {
		cfg.ExcludeBranches, _ = cmd.Flags().GetStringSlice("exclude_branches")
	}

	// Default the export directory when neither config nor flag supplied
	// one (issue #247).
	if cfg.ExportDirectory == "" {
		cfg.ExportDirectory = DefaultExportDirectory
	}

	// Project-data migration is on by default; the only opt-out is
	// SkipProjectDataMigration. Derive the internal IncludeScanHistory
	// field so the planner's existing scan-history gate keeps working
	// without forcing every caller to set both fields.
	cfg.IncludeScanHistory = !cfg.SkipProjectDataMigration

	return cfg, nil
}
