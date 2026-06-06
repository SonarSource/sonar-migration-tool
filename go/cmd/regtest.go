// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/regtest"
	"github.com/spf13/cobra"
)

var regtestCmd = &cobra.Command{
	Use:   "regtest",
	Short: "Run exhaustive regression verification of a completed migration",
	Long: `Programmatically verifies that ALL data from SonarQube Server was correctly
migrated to SonarCloud. Connects to both SQS and SC APIs, runs 70+ parallel
checks across all entity types (projects, issues, hotspots, quality profiles,
quality gates, groups, permissions, settings, measures, etc.), and produces
a detailed pass/fail report.

This is the automated equivalent of Phase 4 in the regression testing protocol.
The stop condition is not "the tool ran without errors" — it is "EVERY piece of
data from SonarQube Server exists and is correct in SonarCloud."`,
	RunE: func(cmd *cobra.Command, args []string) error {
		defer common.LogCommandDuration(slog.Default(), "regtest", time.Now())

		cfg, err := buildRegtestConfig(cmd)
		if err != nil {
			return err
		}
		if cfg.SQSURL == "" || cfg.SQSToken == "" {
			return fmt.Errorf("SonarQube Server URL and token are required (provide via config file)")
		}
		if cfg.SCURL == "" || cfg.SCToken == "" || cfg.SCOrg == "" {
			return fmt.Errorf("SonarCloud URL, token, and org key are required (provide via config file)")
		}

		suite, err := regtest.NewSuite(cfg)
		if err != nil {
			return fmt.Errorf("initializing suite: %w", err)
		}

		report, err := suite.Run(cmd.Context())
		if err != nil {
			return fmt.Errorf("running suite: %w", err)
		}

		if err := regtest.FormatReport(os.Stdout, report, cfg.Format); err != nil {
			return fmt.Errorf("formatting report: %w", err)
		}

		if report.Verdict == "FAIL" {
			return fmt.Errorf("regression test FAILED: %d failures, %d errors out of %d checks",
				report.Failed, report.Errors, report.TotalChecks)
		}

		fmt.Fprintf(os.Stderr, "\nRegression test PASSED: %d/%d checks passed\n",
			report.Passed, report.TotalChecks)
		return nil
	},
}

func init() {
	f := regtestCmd.Flags()
	f.String("config", "", "Path to JSON configuration file (same format as extract/migrate)")
	f.String("format", "table", "Output format: table, json, markdown")
	f.Int("concurrency", 20, "Maximum number of parallel checks")
	f.Bool("verbose", false, "Enable verbose output")
}

func buildRegtestConfig(cmd *cobra.Command) (regtest.Config, error) {
	configFile, _ := cmd.Flags().GetString("config")
	if configFile == "" {
		return regtest.Config{}, fmt.Errorf("--config is required (path to migration config.json)")
	}

	cfg, err := regtest.LoadConfigFile(configFile)
	if err != nil {
		return cfg, err
	}

	if cmd.Flags().Changed("format") {
		cfg.Format, _ = cmd.Flags().GetString("format")
	}
	if cmd.Flags().Changed("concurrency") {
		cfg.Concurrency, _ = cmd.Flags().GetInt("concurrency")
	}
	if cmd.Flags().Changed("verbose") {
		cfg.Verbose, _ = cmd.Flags().GetBool("verbose")
	}

	return cfg, nil
}
