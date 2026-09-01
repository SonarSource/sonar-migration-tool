// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report/summary"
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate configurations to SonarQube Cloud",
	Long: `Migrate SonarQube Server configurations to SonarQube Cloud.
User must run structure and mappings commands and add SonarQube Cloud
organization keys to organizations.csv.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := buildMigrateConfig(cmd, args)
		if err != nil {
			return err
		}
		if cfg.Token == "" || cfg.EnterpriseKey == "" {
			return fmt.Errorf("TOKEN and ENTERPRISE_KEY are required (--target_token/--enterprise_key flags or in config file)")
		}
		runID, err := migrate.RunMigrate(cmd.Context(), cfg)
		// Attempt the summary report whenever a run directory exists, even
		// if RunMigrate returned an error — a partial/failed run still has
		// useful timings, failures, and warnings to surface.
		if runID != "" {
			runDir := filepath.Join(cfg.ExportDirectory, runID)
			if pdfPath, mdPath, reportErr := summary.GenerateReports(runDir, cfg.ExportDirectory, cfg.ExportDirectory); reportErr == nil {
				fmt.Printf("PDF summary report: %s\n", pdfPath)
				fmt.Printf("Markdown summary report: %s\n", mdPath)
			}
		}
		if err != nil {
			return err
		}
		printExportDirNotice(cfg.ExportDirectory)
		return nil
	},
}

func init() {
	f := migrateCmd.Flags()
	f.String("config", "", "Path to JSON configuration file")
	f.String(flagTargetToken, "", "SonarQube Cloud authentication token")
	f.String("enterprise_key", "", "SonarQube Cloud enterprise key")
	f.String("edition", "", "SonarQube Cloud license edition")
	f.String(flagTargetURL, "", "URL of SonarQube Cloud")
	// Deprecated aliases (#406): kept so existing scripts keep working.
	f.String("url", "", "")
	f.String("token", "", "")
	_ = f.MarkDeprecated("url", "use --target_url instead")
	_ = f.MarkDeprecated("token", "use --target_token instead")
	f.String("run_id", "", "ID of a run to resume in case of failures")
	f.Int("concurrency", 0, "Maximum number of concurrent requests")
	f.Int("project_data_build_concurrency", 0, "Maximum number of scanner reports built at once during project-data migration (default 4). Lower this if the migration runs out of memory on a large instance; raise it toward --concurrency if report building is the bottleneck.")
	f.Int("timeout", 0, "Per-HTTP-request timeout in seconds (default: 60). Maps to the top-level timeout config field.")
	f.String("export_directory", "", "Root directory containing all SonarQube exports")
	f.String("target_task", "", "Name of a specific migration task to complete")
	f.Bool("skip_profiles", false, "Skip quality profile migration/provisioning in SonarQube Cloud")
	f.Bool(flagSkipIssueSync, false, "Skip the final per-issue and per-hotspot metadata sync (#299). Same semantics as the skip_issue_sync config-file field — defaults to false (sync happens); pass the flag to skip.")
	f.Bool(flagSkipProjectDataMigration, false, "Skip the entire project-data migration: importProjectData and the trailing per-issue/per-hotspot sync (#303). Defaults to false (data is migrated); pass the flag to skip.")
	f.Bool(flagFastSync, false, "Skip tagging and back-linking hotspots/issues with zero user changes on the source (original state, no comments, no custom tags). Defaults to false (every hotspot is tagged and back-linked). #527.")
	f.String("default_organization", "", "SonarQube Cloud organization to migrate every project into when organizations.csv has no mapping defined. Ignored if any mapping is present.")
	f.String("project_key_pattern", "", "Template for target project keys, built from <ORIGINAL_PROJECT_KEY> and <ORGANIZATION_KEY> (default: <ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>). #138")
	f.StringSlice("exclude_branches", nil, "Glob patterns for non-main branches to skip during project data import (e.g. feature/*,bugfix/*)")
	f.String("objects", "", "Comma-separated list of object categories to migrate: "+strings.Join(common.AllObjects, ", ")+" (aliases: qp, qg, pt, lp). Omit to migrate everything (default). #536")
	f.String(flagProjectKey, "", "Regexp pattern of source project keys to migrate (only applies when the projects category is selected via --objects). Always compiled as a full-match regex implicitly anchored with ^ and $, e.g. \"BANKING_.+\" matches every key starting with BANKING_, not just a key containing that substring. A plain key like \"my-project\" matches only itself. #536")
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

	// Flags override config file. Apply the deprecated --url/--token
	// aliases first so the primary --target_url/--target_token wins when
	// both are passed (#406).
	overrideString(cmd, "token", &cfg.Token)
	overrideString(cmd, "url", &cfg.URL)
	overrideString(cmd, flagTargetToken, &cfg.Token)
	overrideString(cmd, flagTargetURL, &cfg.URL)
	overrideString(cmd, "enterprise_key", &cfg.EnterpriseKey)
	overrideString(cmd, "edition", &cfg.Edition)
	overrideString(cmd, "run_id", &cfg.RunID)
	overrideString(cmd, "export_directory", &cfg.ExportDirectory)
	overrideString(cmd, "target_task", &cfg.TargetTask)
	overrideString(cmd, "default_organization", &cfg.DefaultOrganization)
	overrideString(cmd, "project_key_pattern", &cfg.ProjectKeyPattern)
	overrideInt(cmd, "concurrency", &cfg.Concurrency)
	overrideInt(cmd, "project_data_build_concurrency", &cfg.BuildConcurrency)
	overrideInt(cmd, "timeout", &cfg.Timeout)
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
	// importProjectData AND the trailing sync pair. Same one-way
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
	applyFlagBool(cmd, flagFastSync, &cfg.FastSync)

	// --objects overrides whatever the config file resolved (#536).
	if cmd.Flags().Changed("objects") {
		raw, _ := cmd.Flags().GetString("objects")
		objects, err := common.ParseObjects(common.SplitObjectsCSV(raw))
		if err != nil {
			return cfg, err
		}
		cfg.Objects = objects
	}
	if cfg.Objects != nil && cfg.Objects[common.ObjectLicenseProfiles] {
		slog.Default().Warn("license_profiles migration is not yet supported; ignoring")
	}
	// --project_key restricts migration to source project keys matching
	// the pattern (#536, mirrors #529's transfer-side flag and NOT to be
	// confused with --project_key_pattern, the target-key rendering
	// template). Unlike extract, migrate never calls the source API to
	// resolve it — createProjects filters the records it already read
	// locally from generateProjectMappings — so validate the pattern
	// compiles here (aborting before any API call) and pass it straight
	// through as ProjectKeyFilter.
	if cmd.Flags().Changed(flagProjectKey) {
		raw, _ := cmd.Flags().GetString(flagProjectKey)
		if _, err := extract.CompileProjectKeyPattern(raw); err != nil {
			return cfg, fmt.Errorf("invalid --%s pattern %q: %w", flagProjectKey, raw, err)
		}
		if cfg.Objects == nil || cfg.Objects[common.ObjectProjects] {
			cfg.ProjectKeyFilter = raw
		}
		// else: an active --objects selection excludes "projects" — the
		// pattern is harmless but unused in that combination, matching
		// the issue's checklist (no error, just a no-op).
	}

	// Default the export directory when neither config nor flag supplied
	// one (issue #247).
	if cfg.ExportDirectory == "" {
		cfg.ExportDirectory = DefaultExportDirectory
	}

	// Project-data migration is on by default; the only opt-out is
	// SkipProjectDataMigration. Derive the internal IncludeProjectData
	// field so the planner's existing project-data gate keeps working
	// without forcing every caller to set both fields.
	cfg.IncludeProjectData = !cfg.SkipProjectDataMigration

	return cfg, nil
}
