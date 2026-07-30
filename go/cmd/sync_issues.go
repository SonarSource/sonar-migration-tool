// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/spf13/cobra"
)

const flagProjectKeys = "project_key"

var syncIssuesCmd = &cobra.Command{
	Use:   "sync-issues",
	Short: "Sync issue/hotspot triage state from " + sqServerName + " to " + scCloudName,
	Long: `sync-issues synchronises issue and Security Hotspot triage state (status,
comments, tags) from a ` + sqServerName + ` instance to already-migrated ` + scCloudName + `
projects, without running a full migrate/transfer.

Use it after a migrate/transfer run with --skip_project_data_migration, once
a real ` + scCloudName + ` scan has populated the project's issues, or as a
standalone periodic re-sync of triage state.

It runs its own lightweight extract from the source (issues and hotspots
carrying any manual triage signal: non-default status, custom tags, manual
severity, or comments) — no prior extract is required.

By default every project visible on the source is synced; pass one or more
--project_key flags to narrow the scope. Target project keys are resolved
via the same project_key_pattern + organizations.csv convention migrate/
transfer use.

Example (flags):
  sonar-migration-tool sync-issues \
    --source_url https://sonarqube.example.com \
    --source_token sqp_xxx \
    --target_token squ_xxx \
    --default_organization my-org

Example (config file):
  sonar-migration-tool sync-issues -c config.json

config.json uses the common unified shape (same loader as extract / migrate /
transfer):
  {
    "export_directory": "./migration-files",
    "concurrency": 10,
    "timeout": 60,
    "source": {
      "url": "https://sonarqube.example.com",
      "token": "sqp_xxx"
    },
    "target": {
      "url": "https://sonarcloud.io/",
      "token": "squ_xxx",
      "default_organization": "my-org"
    }
  }

CLI flags always take precedence over values from the config file.`,
	RunE: runSyncIssuesCmd,
}

func init() {
	f := syncIssuesCmd.Flags()
	f.StringP(flagConfig, "c", "", "Path to JSON configuration file (common shape with source / target sections)")
	f.String(flagSourceURL, "", sqServerName+" URL (maps to source.url)")
	f.String(flagSourceToken, "", sqServerName+" token (maps to source.token)")
	f.StringSlice(flagProjectKeys, nil, "Project key to sync (repeatable; omit to sync every project on the source)")
	f.String(flagTargetURL, "", scCloudName+" URL (maps to target.url, default: https://sonarcloud.io/)")
	f.String(flagTargetToken, "", scCloudName+" token (maps to target.token)")
	f.String(flagDefaultOrg, "", scCloudName+" organization key (maps to target.default_organization)")
	f.String(flagProjectKeyPattern, "", "Template used to resolve each project's already-migrated target key, built from <ORIGINAL_PROJECT_KEY> and <ORGANIZATION_KEY> (maps to target.project_key_pattern; default: <ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>) — must match the pattern used when the projects were created")
	f.String(flagEnterpriseKey, "", scCloudName+" enterprise key (maps to target.enterprise_key, defaults to --"+flagDefaultOrg+")")
	f.String(flagExportDir, "./migration-files/", "Working directory for intermediate files (maps to export_directory)")
	f.Int(flagConcurrency, 0, "Max concurrent requests (default: 25) (maps to concurrency)")
	f.Int(flagTimeout, 0, "HTTP request timeout in seconds (maps to timeout; default: 60)")
	f.String(flagPEMFilePath, "", "Path to client mTLS PEM file for the source server (maps to source.pem_file_path)")
	f.String(flagKeyFilePath, "", "Path to client mTLS key file for the source server (maps to source.key_file_path)")
	f.String(flagCertPassword, "", "Password for the source server mTLS client certificate (maps to source.cert_password)")
	// --debug is inherited from the persistent root flag; see cmd/root.go.
}

// syncIssuesConfig holds the resolved configuration after merging file and
// flag values. Mirrors transferConfig, minus the skip-flags (this command's
// entire job is the sync) and the mandatory single-project requirement.
type syncIssuesConfig struct {
	sourceURL           string
	sourceToken         string
	projectKeys         []string
	targetURL           string
	targetToken         string
	defaultOrganization string
	projectKeyPattern   string
	enterpriseKey       string
	exportDir           string
	concurrency         int
	timeout             int
	pemFilePath         string
	keyFilePath         string
	certPassword        string
	debug               bool
}

// loadSyncIssuesFileDefaults reads the shared --config file via the same
// loaders extract / migrate use, so sync-issues accepts every supported
// config shape.
func loadSyncIssuesFileDefaults(path string) (syncIssuesConfig, error) {
	var cfg syncIssuesConfig
	extractCfg, err := extract.LoadExtractConfigFile(path)
	if err != nil {
		return cfg, err
	}
	migrateCfg, err := migrate.LoadMigrateConfigFile(path)
	if err != nil {
		return cfg, err
	}

	cfg.sourceURL = extractCfg.URL
	cfg.sourceToken = extractCfg.Token
	cfg.targetURL = migrateCfg.URL
	cfg.targetToken = migrateCfg.Token
	cfg.enterpriseKey = migrateCfg.EnterpriseKey
	cfg.defaultOrganization = migrateCfg.DefaultOrganization
	cfg.projectKeyPattern = migrateCfg.ProjectKeyPattern

	cfg.exportDir = extractCfg.ExportDirectory
	if cfg.exportDir == "" {
		cfg.exportDir = migrateCfg.ExportDirectory
	}

	switch {
	case extractCfg.Concurrency != 0:
		cfg.concurrency = extractCfg.Concurrency
	case migrateCfg.Concurrency != 0:
		cfg.concurrency = migrateCfg.Concurrency
	}

	cfg.timeout = extractCfg.Timeout
	cfg.pemFilePath = extractCfg.PEMFilePath
	cfg.keyFilePath = extractCfg.KeyFilePath
	cfg.certPassword = extractCfg.CertPassword

	cfg.debug = migrateCfg.Debug
	return cfg, nil
}

func resolveSyncIssuesConfig(cmd *cobra.Command) (syncIssuesConfig, error) {
	var cfg syncIssuesConfig

	configFile, _ := cmd.Flags().GetString(flagConfig)
	if configFile != "" {
		loaded, err := loadSyncIssuesFileDefaults(configFile)
		if err != nil {
			return syncIssuesConfig{}, err
		}
		cfg = loaded
	}

	applyFlagString(cmd, flagSourceURL, &cfg.sourceURL)
	applyFlagString(cmd, flagSourceToken, &cfg.sourceToken)
	if cmd.Flags().Changed(flagProjectKeys) {
		cfg.projectKeys, _ = cmd.Flags().GetStringSlice(flagProjectKeys)
	}
	applyFlagString(cmd, flagTargetURL, &cfg.targetURL)
	applyFlagString(cmd, flagTargetToken, &cfg.targetToken)
	applyFlagString(cmd, flagDefaultOrg, &cfg.defaultOrganization)
	applyFlagString(cmd, flagProjectKeyPattern, &cfg.projectKeyPattern)
	applyFlagString(cmd, flagEnterpriseKey, &cfg.enterpriseKey)
	applyFlagString(cmd, flagExportDir, &cfg.exportDir)
	applyFlagInt(cmd, flagConcurrency, &cfg.concurrency)
	applyFlagInt(cmd, flagTimeout, &cfg.timeout)
	applyFlagString(cmd, flagPEMFilePath, &cfg.pemFilePath)
	applyFlagString(cmd, flagKeyFilePath, &cfg.keyFilePath)
	applyFlagString(cmd, flagCertPassword, &cfg.certPassword)
	applyFlagBool(cmd, flagDebug, &cfg.debug)

	if cfg.exportDir == "" {
		cfg.exportDir = "./migration-files/"
	}
	if cfg.enterpriseKey == "" {
		cfg.enterpriseKey = cfg.defaultOrganization
	}

	return cfg, nil
}

func validateSyncIssuesConfig(cfg syncIssuesConfig) error {
	if cfg.sourceURL == "" || cfg.sourceToken == "" {
		return fmt.Errorf("%s URL and token are required (--%s / --%s or source.url / source.token in config file)", sqServerName, flagSourceURL, flagSourceToken)
	}
	if cfg.targetToken == "" || cfg.defaultOrganization == "" {
		return fmt.Errorf("%s token and organization key are required (--%s / --%s or target.token / target.default_organization in config file)", scCloudName, flagTargetToken, flagDefaultOrg)
	}
	return nil
}

func runSyncIssuesCmd(cmd *cobra.Command, _ []string) error {
	defer common.LogCommandDuration(slog.Default(), "sync-issues", time.Now())

	cfg, err := resolveSyncIssuesConfig(cmd)
	if err != nil {
		return err
	}
	if err := validateSyncIssuesConfig(cfg); err != nil {
		return err
	}

	ctx := cmd.Context()

	printPhase(1, 3, "Extracting from "+sqServerName+"...")
	skipped, err := extract.RunExtract(ctx, extract.ExtractConfig{
		URL:                cfg.sourceURL,
		Token:              cfg.sourceToken,
		ExportDirectory:    cfg.exportDir,
		ProjectKeys:        cfg.projectKeys,
		Concurrency:        cfg.concurrency,
		Timeout:            cfg.timeout,
		PEMFilePath:        cfg.pemFilePath,
		KeyFilePath:        cfg.keyFilePath,
		CertPassword:       cfg.certPassword,
		IncludeProjectData: true,
		Debug:              cfg.debug,
	})
	if err != nil {
		return fmt.Errorf("extract failed: %w", err)
	}
	warnSkippedProjects(skipped)

	printPhase(2, 3, "Building organization structure...")
	if err := structure.RunStructure(cfg.exportDir, cfg.defaultOrganization); err != nil {
		return fmt.Errorf("structure failed: %w", err)
	}

	printPhase(3, 3, "Syncing issues and hotspots to "+scCloudName+"...")
	summary, err := migrate.RunSyncIssues(ctx, migrate.SyncIssuesConfig{
		URL:                 cfg.targetURL,
		Token:               cfg.targetToken,
		EnterpriseKey:       cfg.enterpriseKey,
		ExportDirectory:     cfg.exportDir,
		Concurrency:         cfg.concurrency,
		Timeout:             cfg.timeout,
		ProjectKeyPattern:   cfg.projectKeyPattern,
		DefaultOrganization: cfg.defaultOrganization,
		ProjectKeys:         cfg.projectKeys,
		Debug:               cfg.debug,
	})
	if err != nil {
		return fmt.Errorf("sync-issues failed: %w", err)
	}

	fmt.Printf("Projects synced: %d\n", summary.ProjectsSynced)
	fmt.Printf("Issues:   %d actionable, %d synced, %d skipped (ambiguous or not found)\n",
		summary.IssuesActionable, summary.IssuesSynced, summary.IssuesLineMismatch+summary.IssuesNotFound)
	fmt.Printf("Hotspots: %d actionable, %d synced, %d acknowledged->to_review, %d skipped (ambiguous or not found)\n",
		summary.HotspotsActionable, summary.HotspotsSynced, summary.HotspotsAckDemoted, summary.HotspotsLineMismatch+summary.HotspotsNotFound)
	fmt.Println("Sync complete.")
	return nil
}
