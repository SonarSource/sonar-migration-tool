// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"fmt"
	"os"

	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/spf13/cobra"
)

var extractCmd = &cobra.Command{
	Use:   "extract",
	Short: "Extract data from a SonarQube Server instance",
	Long:  "Extracts data from a SonarQube Server instance and stores it in the export directory as new line delimited json files.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := buildExtractConfig(cmd, args)
		if err != nil {
			return err
		}
		if cfg.URL == "" || cfg.Token == "" {
			return fmt.Errorf("URL and TOKEN are required (--source_url/--source_token flags or in config file)")
		}
		skipped, err := extract.RunExtract(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		if len(skipped) > 0 {
			fmt.Fprintf(os.Stderr, "\n%d project(s) skipped (insufficient privileges):\n", len(skipped))
			for _, key := range skipped {
				fmt.Fprintf(os.Stderr, "  - %s\n", key)
			}
		}
		printExportDirNotice(cfg.ExportDirectory)
		return nil
	},
}

func init() {
	f := extractCmd.Flags()
	f.String("config", "", "Path to JSON configuration file")
	f.String(flagSourceURL, "", "URL of the SonarQube Server")
	f.String(flagSourceToken, "", "Authentication token for the SonarQube Server")
	// Deprecated aliases (#406): kept so existing scripts keep working.
	// MarkDeprecated hides the flag from --help and prints a warning when used.
	f.String("url", "", "")
	f.String("token", "", "")
	_ = f.MarkDeprecated("url", "use --source_url instead")
	_ = f.MarkDeprecated("token", "use --source_token instead")
	f.String("pem_file_path", "", "Path to client certificate pem file")
	f.String("key_file_path", "", "Path to client certificate key file")
	f.String("cert_password", "", "Password for client certificate")
	f.String("export_directory", DefaultExportDirectory, "Root directory to output the export")
	f.String("extract_type", "", "Type of extract to run")
	f.Int("concurrency", 0, "Maximum number of concurrent requests")
	f.Int("timeout", 0, "Number of seconds before a request will timeout")
	f.String("extract_id", "", "ID of an extract to resume in case of failures")
	f.String("target_task", "", "Target task to complete; all dependent tasks will be included")
	f.Bool(flagSkipProjectDataMigration, false, "Skip extracting project data (issues, hotspots, source code, SCM blame). Defaults to false — project data is extracted by default. #303.")
	f.Bool(flagSkipIssueSync, false, "Skip extracting per-issue and per-hotspot sync metadata (comments, changelog, hotspot detail). Pair with migrate-side --skip_issue_sync. Defaults to false. #398.")
	f.Bool(flagMigrateHistory, false, "PoC: also extract a bounded set of historical analysis snapshots (date + project-level measures) per project+branch, for --migrate_history on the migrate/transfer side (#554). Defaults to false — no extra API calls unless set.")
	f.Int(flagHistoryMaxPoints, 0, "Max historical snapshots selected per project+branch when --migrate_history is set (default 10).")
	f.Int(flagHistoryMinIntervalDays, 0, "Minimum spacing, in days, enforced between two selected historical snapshots when --migrate_history is set (default 30).")
}

func buildExtractConfig(cmd *cobra.Command, args []string) (extract.ExtractConfig, error) {
	var cfg extract.ExtractConfig

	// Load config file if specified. Supports flat, command-sectioned,
	// and side-sectioned shapes — issue #158.
	configFile, _ := cmd.Flags().GetString("config")
	if configFile != "" {
		loaded, err := extract.LoadExtractConfigFile(configFile)
		if err != nil {
			return cfg, err
		}
		cfg = loaded
	}

	// Flags override config file. Apply the deprecated --url/--token
	// aliases first so the primary --source_url/--source_token wins when
	// both are passed (#406).
	overrideString(cmd, "url", &cfg.URL)
	overrideString(cmd, "token", &cfg.Token)
	overrideString(cmd, flagSourceURL, &cfg.URL)
	overrideString(cmd, flagSourceToken, &cfg.Token)
	overrideString(cmd, "pem_file_path", &cfg.PEMFilePath)
	overrideString(cmd, "key_file_path", &cfg.KeyFilePath)
	overrideString(cmd, "cert_password", &cfg.CertPassword)
	overrideString(cmd, "export_directory", &cfg.ExportDirectory)
	overrideString(cmd, "extract_type", &cfg.ExtractType)
	overrideString(cmd, "extract_id", &cfg.ExtractID)
	overrideString(cmd, "target_task", &cfg.TargetTask)
	overrideInt(cmd, "concurrency", &cfg.Concurrency)
	overrideInt(cmd, "timeout", &cfg.Timeout)
	// Project data is extracted by default. The only opt-out is
	// SkipProjectDataMigration (CLI --skip_project_data_migration or
	// config "skip_project_data_migration": true). CLI flag wins over
	// config; one-way (passing the flag forces opt-out).
	if cmd.Flags().Changed(flagSkipProjectDataMigration) {
		v, _ := cmd.Flags().GetBool(flagSkipProjectDataMigration)
		if v {
			cfg.SkipProjectDataMigration = true
		}
	}
	// --skip_issue_sync is one-way: passing the flag forces opt-out,
	// CLI false does NOT undo a config-file skip_issue_sync: true. #398.
	if cmd.Flags().Changed(flagSkipIssueSync) {
		v, _ := cmd.Flags().GetBool(flagSkipIssueSync)
		if v {
			cfg.SkipIssueSync = true
		}
	}
	// --migrate_history is one-way, same semantics as --skip_issue_sync. #554.
	if cmd.Flags().Changed(flagMigrateHistory) {
		v, _ := cmd.Flags().GetBool(flagMigrateHistory)
		if v {
			cfg.MigrateHistory = true
		}
	}
	overrideInt(cmd, flagHistoryMaxPoints, &cfg.HistoryMaxPoints)
	overrideInt(cmd, flagHistoryMinIntervalDays, &cfg.HistoryMinIntervalDays)
	cfg.IncludeProjectData = !cfg.SkipProjectDataMigration
	// --debug is a persistent flag on rootCmd; pick it up here so the
	// SDK can install the HTTP request/response logger.
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

func overrideString(cmd *cobra.Command, flag string, target *string) {
	if cmd.Flags().Changed(flag) {
		val, _ := cmd.Flags().GetString(flag)
		*target = val
	}
}

func overrideInt(cmd *cobra.Command, flag string, target *int) {
	if cmd.Flags().Changed(flag) {
		val, _ := cmd.Flags().GetInt(flag)
		*target = val
	}
}
