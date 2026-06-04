package cmd

import (
	"context"
	"encoding/json"
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
	flagSQURL              = "sq-url"
	flagSQToken            = "sq-token"
	flagProjectKey         = "project-key"
	flagSCURL              = "sc-url"
	flagSCToken            = "sc-token"
	flagSCOrg              = "sc-org"
	flagSCEnterpriseKey    = "sc-enterprise-key"
	flagExportDir          = "export-dir"
	flagIncludeScanHistory = "include-scan-history"
	flagConcurrency        = "concurrency"
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
    --sq-url https://sonarqube.example.com \
    --sq-token sqp_xxx \
    --project-key my-project \
    --sc-token squ_xxx \
    --sc-org my-org

Example (config file):
  sonar-migration-tool transfer -c config.json

config.json format:
  {
    "sonarqube": { "url": "...", "token": "...", "projectKey": "..." },
    "sonarcloud": { "url": "...", "token": "...", "organization": "...", "enterpriseKey": "..." }
  }

The sonarcloud.url defaults to https://sonarcloud.io/ when omitted.

The enterpriseKey is required for portfolio migration; for projects/gates/profiles
it can be omitted and defaults to the organization key.`,
	RunE: runTransfer,
}

func init() {
	f := transferCmd.Flags()
	f.StringP(flagConfig, "c", "", "Path to transfer config file")
	f.String(flagSQURL, "", sqServerName+" URL")
	f.String(flagSQToken, "", sqServerName+" token")
	f.String(flagProjectKey, "", "Project key to transfer (omit to transfer all projects)")
	f.String(flagSCURL, "", scCloudName+" URL (default: https://sonarcloud.io/)")
	f.String(flagSCToken, "", scCloudName+" token")
	f.String(flagSCOrg, "", scCloudName+" organization key")
	f.String(flagSCEnterpriseKey, "", scCloudName+" enterprise key (defaults to --sc-org)")
	f.String(flagExportDir, "./migration-files/", "Working directory for intermediate files")
	f.Bool(flagIncludeScanHistory, false, "Extract and import full issue/hotspot scan history")
	f.Int(flagConcurrency, 0, "Max concurrent requests (default: 25)")
}

// transferConfig holds the resolved configuration after merging file and flag values.
type transferConfig struct {
	sqURL              string
	sqToken            string
	projectKey         string
	scURL              string
	scToken            string
	scOrg              string
	scEnterpriseKey    string
	exportDir          string
	concurrency        int
	includeScanHistory bool
	debug              bool
}

// transferFileConfig is the transfer-specific config file shape (CloudVoyager-compatible).
// This is intentionally separate from the existing extract/migrate config shapes to avoid
// field-name conflicts with the existing side-sectioned parser.
type transferFileConfig struct {
	SonarQube struct {
		URL        string `json:"url"`
		Token      string `json:"token"`
		ProjectKey string `json:"projectKey"`
	} `json:"sonarqube"`
	SonarCloud struct {
		URL           string `json:"url"`
		Token         string `json:"token"`
		Organization  string `json:"organization"`
		EnterpriseKey string `json:"enterpriseKey"`
	} `json:"sonarcloud"`
	Settings struct {
		ExportDirectory    string `json:"exportDirectory"`
		Concurrency        int    `json:"concurrency"`
		IncludeScanHistory bool   `json:"includeScanHistory"`
		Debug              bool   `json:"debug"`
	} `json:"settings"`
}

func loadTransferConfigFile(path string) (transferFileConfig, error) {
	var cfg transferFileConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("reading transfer config %q: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing transfer config %q: %w", path, err)
	}
	return cfg, nil
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

func resolveTransferConfig(cmd *cobra.Command) (transferConfig, error) {
	var fileCfg transferFileConfig

	configFile, _ := cmd.Flags().GetString(flagConfig)
	if configFile != "" {
		loaded, err := loadTransferConfigFile(configFile)
		if err != nil {
			return transferConfig{}, err
		}
		fileCfg = loaded
	}

	cfg := transferConfig{
		sqURL:              fileCfg.SonarQube.URL,
		sqToken:            fileCfg.SonarQube.Token,
		projectKey:         fileCfg.SonarQube.ProjectKey,
		scURL:              fileCfg.SonarCloud.URL,
		scToken:            fileCfg.SonarCloud.Token,
		scOrg:              fileCfg.SonarCloud.Organization,
		scEnterpriseKey:    fileCfg.SonarCloud.EnterpriseKey,
		exportDir:          fileCfg.Settings.ExportDirectory,
		concurrency:        fileCfg.Settings.Concurrency,
		includeScanHistory: fileCfg.Settings.IncludeScanHistory,
		debug:              fileCfg.Settings.Debug,
	}

	applyFlagString(cmd, flagSQURL, &cfg.sqURL)
	applyFlagString(cmd, flagSQToken, &cfg.sqToken)
	applyFlagString(cmd, flagProjectKey, &cfg.projectKey)
	applyFlagString(cmd, flagSCURL, &cfg.scURL)
	applyFlagString(cmd, flagSCToken, &cfg.scToken)
	applyFlagString(cmd, flagSCOrg, &cfg.scOrg)
	applyFlagString(cmd, flagSCEnterpriseKey, &cfg.scEnterpriseKey)
	applyFlagString(cmd, flagExportDir, &cfg.exportDir)
	applyFlagInt(cmd, flagConcurrency, &cfg.concurrency)
	applyFlagBool(cmd, flagIncludeScanHistory, &cfg.includeScanHistory)
	applyFlagBool(cmd, flagDebug, &cfg.debug)

	if cfg.exportDir == "" {
		cfg.exportDir = "./migration-files/"
	}
	if cfg.scEnterpriseKey == "" {
		cfg.scEnterpriseKey = cfg.scOrg
	}

	return cfg, nil
}

func validateTransferConfig(cfg transferConfig) error {
	if cfg.sqURL == "" || cfg.sqToken == "" {
		return fmt.Errorf("%s URL and token are required (--%s / --%s or config file)", sqServerName, flagSQURL, flagSQToken)
	}
	if cfg.scToken == "" || cfg.scOrg == "" {
		return fmt.Errorf("%s token and organization key are required (--%s / --%s or config file)", scCloudName, flagSCToken, flagSCOrg)
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
		URL:                cfg.sqURL,
		Token:              cfg.sqToken,
		ExportDirectory:    cfg.exportDir,
		ProjectKeys:        projectKeys,
		Concurrency:        cfg.concurrency,
		IncludeScanHistory: cfg.includeScanHistory,
	})
	if err != nil {
		return nil, fmt.Errorf("extract failed: %w", err)
	}
	return skipped, nil
}

func runTransferStructure(cfg transferConfig) error {
	printPhase(2, 4, "Building organization structure...")
	if err := structure.RunStructure(cfg.exportDir, cfg.scOrg); err != nil {
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
	runID, err := migrate.RunMigrate(ctx, migrate.MigrateConfig{
		URL:                cfg.scURL,
		Token:              cfg.scToken,
		EnterpriseKey:      cfg.scEnterpriseKey,
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
