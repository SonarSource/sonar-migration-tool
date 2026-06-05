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
	Use:   "extract [url] [token]",
	Short: "Extract data from a SonarQube Server instance",
	Long:  "Extracts data from a SonarQube Server instance and stores it in the export directory as new line delimited json files.",
	Args:  cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := buildExtractConfig(cmd, args)
		if err != nil {
			return err
		}
		if cfg.URL == "" || cfg.Token == "" {
			return fmt.Errorf("URL and TOKEN are required (either as arguments or in config file)")
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

	// CLI args override config file.
	if len(args) > 0 && args[0] != "" {
		cfg.URL = args[0]
	}
	if len(args) > 1 && args[1] != "" {
		cfg.Token = args[1]
	}

	// Flags override everything.
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
	cfg.IncludeScanHistory = !cfg.SkipProjectDataMigration
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
