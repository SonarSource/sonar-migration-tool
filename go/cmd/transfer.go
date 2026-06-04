package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report/summary"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/spf13/cobra"
)

const (
	// Display names used throughout the transfer command's help text and
	// error messages. Defining them once keeps user-facing wording in sync.
	sqServerName = "SonarQube Server"
	scCloudName  = "SonarQube Cloud"

	flagConfig             = "config"
	flagSourceURL          = "source-url"
	flagSourceToken        = "source-token"
	flagProjectKey         = "project-key"
	flagTargetURL          = "target-url"
	flagTargetToken        = "target-token"
	flagEnterpriseKey      = "enterprise_key"
	flagDefaultOrg         = "default_organization"
	flagExportDir          = "export-dir"
	flagIncludeScanHistory = "include-scan-history"
	flagConcurrency        = "concurrency"
	flagTimeout            = "timeout"
	flagPEMFilePath        = "pem_file_path"
	flagKeyFilePath        = "key_file_path"
	flagCertPassword       = "cert_password"
	flagDebug              = "debug"
)

var transferCmd = &cobra.Command{
	Use:   "transfer",
	Short: "Transfer a single project from " + sqServerName + " to " + scCloudName,
	Long: `Transfer migrates a ` + sqServerName + ` project to ` + scCloudName + ` in one command.

It chains extract → structure → mappings → migrate automatically, eliminating
the manual CSV-editing step. Credentials for both sides are required.

Example (flags):
  sonar-migration-tool transfer \
    --source-url https://sonarqube.example.com \
    --source-token sqp_xxx \
    --project-key my-project \
    --target-token squ_xxx \
    --default_organization my-org

Example (config file):
  sonar-migration-tool transfer -c config.json

config.json uses the common unified shape (same loader as extract /
migrate), so every shared setting carries over — including
concurrency, timeout, export_directory, pem_file_path, key_file_path,
and cert_password:
  {
    "export_directory": "./migration-files",
    "concurrency": 10,
    "timeout": 60,
    "source": {
      "url": "https://sonarqube.example.com",
      "token": "sqp_xxx",
      "pem_file_path": "...", "key_file_path": "...", "cert_password": "..."
    },
    "target": {
      "url": "https://sonarcloud.io/",
      "token": "squ_xxx",
      "default_organization": "my-org",
      "enterprise_key": "my-org"
    }
  }

The target.url defaults to https://sonarcloud.io/ when omitted.

The target.enterprise_key is required for portfolio migration; for
projects/gates/profiles it can be omitted and defaults to the
default_organization value.

CLI flags always take precedence over values from the config file.`,
	RunE: runTransfer,
}

func init() {
	f := transferCmd.Flags()
	f.StringP(flagConfig, "c", "", "Path to JSON configuration file (common shape with source / target sections)")
	f.String(flagSourceURL, "", sqServerName+" URL (maps to source.url)")
	f.String(flagSourceToken, "", sqServerName+" token (maps to source.token)")
	f.String(flagProjectKey, "", "Project key to transfer (omit to transfer all projects)")
	f.String(flagTargetURL, "", scCloudName+" URL (maps to target.url, default: https://sonarcloud.io/)")
	f.String(flagTargetToken, "", scCloudName+" token (maps to target.token)")
	f.String(flagDefaultOrg, "", scCloudName+" organization key (maps to target.default_organization)")
	f.String(flagEnterpriseKey, "", scCloudName+" enterprise key (maps to target.enterprise_key, defaults to --"+flagDefaultOrg+")")
	f.String(flagExportDir, "./migration-files/", "Working directory for intermediate files (maps to export_directory)")
	f.Bool(flagIncludeScanHistory, false, "Extract and import full issue/hotspot scan history (maps to include_scan_history)")
	f.Int(flagConcurrency, 0, "Max concurrent requests (default: 25) (maps to concurrency)")
	f.Int(flagTimeout, 0, "HTTP request timeout in seconds (maps to timeout)")
	f.String(flagPEMFilePath, "", "Path to client mTLS PEM file for the source server (maps to source.pem_file_path)")
	f.String(flagKeyFilePath, "", "Path to client mTLS key file for the source server (maps to source.key_file_path)")
	f.String(flagCertPassword, "", "Password for the source server mTLS client certificate (maps to source.cert_password)")
}

// transferConfig holds the resolved configuration after merging file and flag values.
type transferConfig struct {
	sourceURL           string
	sourceToken         string
	projectKey          string
	targetURL           string
	targetToken         string
	defaultOrganization string
	enterpriseKey       string
	exportDir           string
	concurrency         int
	timeout             int
	pemFilePath         string
	keyFilePath         string
	certPassword        string
	includeScanHistory  bool
	debug               bool
}

func applyFlagString(cmd *cobra.Command, name string, target *string) {
	if cmd.Flags().Changed(name) {
		*target, _ = cmd.Flags().GetString(name)
	}
}

func applyFlagInt(cmd *cobra.Command, name string, target *int) {
	if cmd.Flags().Changed(name) {
		*target, _ = cmd.Flags().GetInt(name)
	}
}

func applyFlagBool(cmd *cobra.Command, name string, target *bool) {
	if cmd.Flags().Changed(name) {
		*target, _ = cmd.Flags().GetBool(name)
	}
}

// loadTransferFileDefaults reads the shared --config file via the same
// loaders extract / migrate use, so transfer accepts every supported
// shape (flat, command-sectioned, side-sectioned, and the unified
// source/target shape — #266). The transfer-specific dedicated shape
// from earlier releases has been retired (#295).
func loadTransferFileDefaults(path string) (transferConfig, error) {
	var cfg transferConfig
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

	cfg.includeScanHistory = extractCfg.IncludeScanHistory || migrateCfg.IncludeScanHistory
	cfg.debug = migrateCfg.Debug
	return cfg, nil
}

func resolveTransferConfig(cmd *cobra.Command) (transferConfig, error) {
	var cfg transferConfig

	configFile, _ := cmd.Flags().GetString(flagConfig)
	if configFile != "" {
		loaded, err := loadTransferFileDefaults(configFile)
		if err != nil {
			return transferConfig{}, err
		}
		cfg = loaded
	}

	applyFlagString(cmd, flagSourceURL, &cfg.sourceURL)
	applyFlagString(cmd, flagSourceToken, &cfg.sourceToken)
	applyFlagString(cmd, flagProjectKey, &cfg.projectKey)
	applyFlagString(cmd, flagTargetURL, &cfg.targetURL)
	applyFlagString(cmd, flagTargetToken, &cfg.targetToken)
	applyFlagString(cmd, flagDefaultOrg, &cfg.defaultOrganization)
	applyFlagString(cmd, flagEnterpriseKey, &cfg.enterpriseKey)
	applyFlagString(cmd, flagExportDir, &cfg.exportDir)
	applyFlagInt(cmd, flagConcurrency, &cfg.concurrency)
	applyFlagInt(cmd, flagTimeout, &cfg.timeout)
	applyFlagString(cmd, flagPEMFilePath, &cfg.pemFilePath)
	applyFlagString(cmd, flagKeyFilePath, &cfg.keyFilePath)
	applyFlagString(cmd, flagCertPassword, &cfg.certPassword)
	applyFlagBool(cmd, flagIncludeScanHistory, &cfg.includeScanHistory)
	applyFlagBool(cmd, flagDebug, &cfg.debug)

	if cfg.exportDir == "" {
		cfg.exportDir = "./migration-files/"
	}
	if cfg.enterpriseKey == "" {
		cfg.enterpriseKey = cfg.defaultOrganization
	}

	return cfg, nil
}

func validateTransferConfig(cfg transferConfig) error {
	if cfg.sourceURL == "" || cfg.sourceToken == "" {
		return fmt.Errorf("%s URL and token are required (--%s / --%s or source.url / source.token in config file)", sqServerName, flagSourceURL, flagSourceToken)
	}
	if cfg.targetToken == "" || cfg.defaultOrganization == "" {
		return fmt.Errorf("%s token and organization key are required (--%s / --%s or target.token / target.default_organization in config file)", scCloudName, flagTargetToken, flagDefaultOrg)
	}
	return nil
}

func runTransfer(cmd *cobra.Command, _ []string) error {
	cfg, err := resolveTransferConfig(cmd)
	if err != nil {
		return err
	}
	if err := validateTransferConfig(cfg); err != nil {
		return err
	}

	ctx := cmd.Context()

	skipped, err := runTransferExtract(ctx, cfg)
	if err != nil {
		return err
	}
	warnSkippedProjects(skipped)

	if err := runTransferStructure(cfg); err != nil {
		return err
	}
	if err := runTransferMappings(cfg); err != nil {
		return err
	}

	runID, err := runTransferMigrate(ctx, cfg)
	if err != nil {
		return err
	}
	emitPDFReport(cfg.exportDir, runID)

	fmt.Println("Transfer complete.")
	return nil
}

// printPhase writes the "[N/4] <description>" banner used at the start of
// each transfer phase so the literal strings live in one place.
func printPhase(step int, total int, description string) {
	fmt.Printf("[%d/%d] %s\n", step, total, description)
}

// warnSkippedProjects prints a warning listing project keys that the
// extract phase reported as skipped (typically due to insufficient
// permissions) so users notice and can re-run with elevated token.
func warnSkippedProjects(skipped []string) {
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "Warning: %d project(s) skipped (insufficient privileges):\n", len(skipped))
	for _, k := range skipped {
		fmt.Fprintf(os.Stderr, "  - %s\n", k)
	}
}

func runTransferExtract(ctx context.Context, cfg transferConfig) ([]string, error) {
	printPhase(1, 4, "Extracting from "+sqServerName+"...")
	var projectKeys []string
	if cfg.projectKey != "" {
		projectKeys = []string{cfg.projectKey}
	}
	skipped, err := extract.RunExtract(ctx, extract.ExtractConfig{
		URL:                cfg.sourceURL,
		Token:              cfg.sourceToken,
		ExportDirectory:    cfg.exportDir,
		ProjectKeys:        projectKeys,
		Concurrency:        cfg.concurrency,
		Timeout:            cfg.timeout,
		PEMFilePath:        cfg.pemFilePath,
		KeyFilePath:        cfg.keyFilePath,
		CertPassword:       cfg.certPassword,
		IncludeScanHistory: cfg.includeScanHistory,
		Debug:              cfg.debug,
	})
	if err != nil {
		return nil, fmt.Errorf("extract failed: %w", err)
	}
	return skipped, nil
}

func runTransferStructure(cfg transferConfig) error {
	printPhase(2, 4, "Building organization structure...")
	if err := structure.RunStructure(cfg.exportDir, cfg.defaultOrganization); err != nil {
		return fmt.Errorf("structure failed: %w", err)
	}
	return nil
}

func runTransferMappings(cfg transferConfig) error {
	printPhase(3, 4, "Generating entity mappings...")
	if err := structure.RunMappings(cfg.exportDir); err != nil {
		return fmt.Errorf("mappings failed: %w", err)
	}
	return nil
}

func runTransferMigrate(ctx context.Context, cfg transferConfig) (string, error) {
	printPhase(4, 4, "Migrating to "+scCloudName+"...")
	// DefaultOrganization is intentionally left unset: structure has
	// already pre-populated sonarcloud_org_key for every row using
	// cfg.defaultOrganization, so passing it again would trigger the
	// "mapping defined, default ignored" WARN in applyOrgMapping.
	runID, err := migrate.RunMigrate(ctx, migrate.MigrateConfig{
		URL:                cfg.targetURL,
		Token:              cfg.targetToken,
		EnterpriseKey:      cfg.enterpriseKey,
		ExportDirectory:    cfg.exportDir,
		Concurrency:        cfg.concurrency,
		IncludeScanHistory: cfg.includeScanHistory,
		Debug:              cfg.debug,
	})
	if err != nil {
		return "", fmt.Errorf("migrate failed: %w", err)
	}
	return runID, nil
}

func emitPDFReport(exportDir, runID string) {
	runDir := filepath.Join(exportDir, runID)
	pdfPath, pdfErr := summary.GeneratePDFReport(runDir, exportDir, exportDir)
	if pdfErr != nil {
		return
	}
	fmt.Printf("PDF summary report: %s\n", pdfPath)
}
