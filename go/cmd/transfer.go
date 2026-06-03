package cmd

import (
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
	Short: "Transfer a single project from SonarQube Server to SonarQube Cloud",
	Long: `Transfer migrates a SonarQube Server project to SonarQube Cloud in one command.

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
	f.String(flagSQURL, "", "SonarQube Server URL")
	f.String(flagSQToken, "", "SonarQube Server token")
	f.String(flagProjectKey, "", "Project key to transfer (omit to transfer all projects)")
	f.String(flagSCURL, "", "SonarQube Cloud URL (default: https://sonarcloud.io/)")
	f.String(flagSCToken, "", "SonarQube Cloud token")
	f.String(flagSCOrg, "", "SonarQube Cloud organization key")
	f.String(flagSCEnterpriseKey, "", "SonarQube Cloud enterprise key (defaults to --sc-org)")
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
		return fmt.Errorf("SonarQube Server URL and token are required (--%s / --%s or config file)", flagSQURL, flagSQToken)
	}
	if cfg.scToken == "" || cfg.scOrg == "" {
		return fmt.Errorf("SonarQube Cloud token and organization key are required (--%s / --%s or config file)", flagSCToken, flagSCOrg)
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

	// Phase 1: Extract.
	fmt.Println("[1/4] Extracting from SonarQube Server...")
	var projectKeys []string
	if cfg.projectKey != "" {
		projectKeys = []string{cfg.projectKey}
	}
	extractCfg := extract.ExtractConfig{
		URL:                cfg.sqURL,
		Token:              cfg.sqToken,
		ExportDirectory:    cfg.exportDir,
		ProjectKeys:        projectKeys,
		Concurrency:        cfg.concurrency,
		IncludeScanHistory: cfg.includeScanHistory,
	}
	skipped, err := extract.RunExtract(ctx, extractCfg)
	if err != nil {
		return fmt.Errorf("extract failed: %w", err)
	}
	if len(skipped) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: %d project(s) skipped (insufficient privileges):\n", len(skipped))
		for _, k := range skipped {
			fmt.Fprintf(os.Stderr, "  - %s\n", k)
		}
	}

	// Phase 2: Structure.
	fmt.Println("[2/4] Building organization structure...")
	if err := structure.RunStructure(cfg.exportDir, cfg.scOrg); err != nil {
		return fmt.Errorf("structure failed: %w", err)
	}

	// Phase 3: Mappings.
	fmt.Println("[3/4] Generating entity mappings...")
	if err := structure.RunMappings(cfg.exportDir); err != nil {
		return fmt.Errorf("mappings failed: %w", err)
	}

	// Phase 4: Migrate.
	fmt.Println("[4/4] Migrating to SonarQube Cloud...")
	migrateCfg := migrate.MigrateConfig{
		URL:                cfg.scURL,
		Token:              cfg.scToken,
		EnterpriseKey:      cfg.scEnterpriseKey,
		ExportDirectory:    cfg.exportDir,
		Concurrency:        cfg.concurrency,
		IncludeScanHistory: cfg.includeScanHistory,
		Debug:              cfg.debug,
	}
	runID, err := migrate.RunMigrate(ctx, migrateCfg)
	if err != nil {
		return fmt.Errorf("migrate failed: %w", err)
	}

	// PDF summary report.
	runDir := filepath.Join(cfg.exportDir, runID)
	if pdfPath, pdfErr := summary.GeneratePDFReport(runDir, cfg.exportDir, cfg.exportDir); pdfErr == nil {
		fmt.Printf("PDF summary report: %s\n", pdfPath)
	}

	fmt.Println("Transfer complete.")
	return nil
}
