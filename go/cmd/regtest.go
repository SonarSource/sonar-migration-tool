// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
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
		if pattern, _ := cmd.Flags().GetString(flagProjectKey); pattern != "" {
			keys, err := resolveRegtestProjectKeys(cmd.Context(), cfg, pattern)
			if err != nil {
				return err
			}
			cfg.ProjectKeys = keys
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
	f.String(flagProjectKey, "", "Project key (or regexp) to scope verification to, matching transfer's --project_key semantics (#529): always compiled as a full-match regex implicitly anchored with ^ and $, e.g. \"BANKING_.+\" matches every key starting with BANKING_. Use this to verify a project-scoped transfer instead of every project on the source.")
}

// resolveRegtestProjectKeys lists every project on the source and returns
// those whose key fully matches pattern as an anchored regex (#529) —
// mirrors resolveTransferProjectKeys so `regtest --project_key <PATTERN>`
// checks exactly the set `transfer --project_key <PATTERN>` migrated,
// instead of comparing every source project against a target that was
// deliberately only given a subset.
func resolveRegtestProjectKeys(ctx context.Context, cfg regtest.Config, pattern string) ([]string, error) {
	re, err := anchoredProjectKeyPattern(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid --%s pattern %q: %w", flagProjectKey, pattern, err)
	}
	allKeys, err := extract.ListAllProjectKeys(ctx, extract.ExtractConfig{
		URL:   cfg.SQSURL,
		Token: cfg.SQSToken,
	})
	if err != nil {
		return nil, fmt.Errorf("listing projects on %s: %w", cfg.SQSURL, err)
	}
	var matched []string
	for _, k := range allKeys {
		if re.MatchString(k) {
			matched = append(matched, k)
		}
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf(
			"no project on %s matches --%s %q (keys are case-sensitive; verify with GET /api/projects/search)",
			cfg.SQSURL, flagProjectKey, pattern)
	}
	sort.Strings(matched)
	fmt.Fprintf(os.Stderr, "Matched %d project(s) for --%s %q: %s\n",
		len(matched), flagProjectKey, pattern, strings.Join(matched, ", "))
	return matched, nil
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
