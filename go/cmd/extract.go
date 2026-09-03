// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
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
		// #536: resolve --project_key (or the config file's top-level
		// "project_key") into concrete ProjectKeys now that URL/Token are
		// known to be set. Skipped entirely when an active --objects
		// filter excludes the "projects" category — the pattern is
		// harmless but unused in that combination, matching the issue's
		// checklist (no error, just a no-op).
		if cfg.ProjectKey != "" && (cfg.Objects == nil || cfg.Objects[common.ObjectProjects]) {
			keys, err := extract.ResolveProjectKeys(cmd.Context(), cfg, cfg.ProjectKey)
			if err != nil {
				return err
			}
			cfg.ProjectKeys = keys
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
	f.String("objects", "", "Comma-separated list of object categories to extract: "+strings.Join(common.AllObjects, ", ")+" (aliases: qp, qg, pt, lp). Omit to extract everything (default).")
	f.String("project_key", "", "Regexp pattern of project keys to extract (only applies when the projects category is selected). A plain key matches only itself.")
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
	applyOneWayBoolFlag(cmd, flagSkipProjectDataMigration, &cfg.SkipProjectDataMigration)
	// --skip_issue_sync is one-way: passing the flag forces opt-out,
	// CLI false does NOT undo a config-file skip_issue_sync: true. #398.
	applyOneWayBoolFlag(cmd, flagSkipIssueSync, &cfg.SkipIssueSync)
	cfg.IncludeProjectData = !cfg.SkipProjectDataMigration
	// --debug is a persistent flag on rootCmd; pick it up here so the
	// SDK can install the HTTP request/response logger.
	if cmd.Flags().Changed("debug") {
		cfg.Debug, _ = cmd.Flags().GetBool("debug")
	}

	if err := applyObjectsFlag(cmd, &cfg.Objects); err != nil {
		return cfg, err
	}
	warnIfLicenseProfilesSelected(cfg.Objects)
	// --project_key is resolved into cfg.ProjectKeys by the caller (RunE),
	// once URL/Token are known to be valid — see extractCmd.RunE. Just
	// capture the pattern here, same precedence as every other flag
	// (CLI overrides config file).
	overrideString(cmd, "project_key", &cfg.ProjectKey)

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

// applyOneWayBoolFlag sets *target true when flag was passed with value
// true. Passing the flag with a false value, or not passing it at all,
// never turns an already-true *target back off — this backs one-way
// CLI opt-outs like --skip_project_data_migration/--skip_issue_sync,
// shared by cmd/extract.go and cmd/migrate.go.
func applyOneWayBoolFlag(cmd *cobra.Command, flag string, target *bool) {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetBool(flag)
		if v {
			*target = true
		}
	}
}

// applyObjectsFlag parses --objects into *objects when the flag was
// passed, overriding whatever the config file resolved (CLI wins).
// Shared by cmd/extract.go and cmd/migrate.go (#536).
func applyObjectsFlag(cmd *cobra.Command, objects *map[string]bool) error {
	if !cmd.Flags().Changed("objects") {
		return nil
	}
	raw, _ := cmd.Flags().GetString("objects")
	parsed, err := common.ParseObjects(common.SplitObjectsCSV(raw))
	if err != nil {
		return err
	}
	*objects = parsed
	return nil
}

// warnIfLicenseProfilesSelected logs a one-time warning when the
// resolved objects selection includes license_profiles — accepted as a
// valid --objects value but not yet implemented on either extract or
// migrate (#536).
func warnIfLicenseProfilesSelected(objects map[string]bool) {
	if objects != nil && objects[common.ObjectLicenseProfiles] {
		slog.Default().Warn("license_profiles migration is not yet supported; ignoring")
	}
}
