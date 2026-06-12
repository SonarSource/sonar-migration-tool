// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/spf13/cobra"
)

const flagResetYes = "yes"

var resetCmd = &cobra.Command{
	Use:   "reset [token] [enterprise_key]",
	Short: "Reset a SonarQube Cloud Enterprise",
	Long:  "Resets a SonarQube Cloud Enterprise back to its original state. Warning: this will delete everything in every organization within the enterprise.",
	Args:  cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := buildResetConfig(cmd, args)
		if err != nil {
			return err
		}
		if cfg.Token == "" || cfg.EnterpriseKey == "" {
			return fmt.Errorf("TOKEN and ENTERPRISE_KEY are required (either as arguments or in config file)")
		}

		autoYes, _ := cmd.Flags().GetBool(flagResetYes)
		fmt.Fprintln(os.Stdout, "WARNING: This will delete migrated entities from the listed SonarCloud organizations.")
		confirmed, err := confirmResetOrgs(cfg.ExportDirectory, autoYes, os.Stdin, os.Stdout)
		if err != nil {
			return err
		}
		if confirmed == nil {
			// confirmResetOrgs already printed "Reset aborted."; exit
			// cleanly so a misclick / Ctrl-D in the prompt doesn't
			// look like a failure to the operator's shell.
			return nil
		}
		cfg.ConfirmedOrgs = confirmed

		if err := migrate.RunReset(cmd.Context(), cfg); err != nil {
			return err
		}
		printExportDirNotice(cfg.ExportDirectory)
		return nil
	},
}

func init() {
	f := resetCmd.Flags()
	f.String("config", "", "Path to JSON configuration file (same format as migrate --config)")
	f.String("edition", "enterprise", "SonarQube Cloud license edition")
	f.String("url", "https://sonarcloud.io/", "URL of SonarQube Cloud")
	f.Int("concurrency", 25, "Maximum number of concurrent requests")
	f.String("export_directory", DefaultExportDirectory, "Directory to place all interim files")
	f.Bool(flagResetYes, false, "Skip the interactive confirmation prompt and reset every listed organization (intended for non-interactive / scripted use). #381.")
}

func buildResetConfig(cmd *cobra.Command, args []string) (migrate.ResetConfig, error) {
	var cfg migrate.ResetConfig

	configFile, _ := cmd.Flags().GetString("config")
	if configFile != "" {
		loaded, err := migrate.LoadResetConfigFile(configFile)
		if err != nil {
			return cfg, err
		}
		cfg = loaded
	}

	// CLI args override config file.
	if len(args) > 0 && args[0] != "" {
		cfg.Token = args[0]
	}
	if len(args) > 1 && args[1] != "" {
		cfg.EnterpriseKey = args[1]
	}

	// Flags override everything.
	overrideString(cmd, "edition", &cfg.Edition)
	overrideString(cmd, "url", &cfg.URL)
	overrideString(cmd, "export_directory", &cfg.ExportDirectory)
	overrideInt(cmd, "concurrency", &cfg.Concurrency)
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

// confirmResetOrgs gates the destructive reset behind an interactive
// confirmation prompt (#381). It lists every mapped SonarCloud org
// with its project count and asks the operator to type the subset
// to reset (whitespace-separated). Returns:
//
//   - (confirmed-orgs, nil) on a valid non-empty selection.
//   - (nil, nil) when the operator aborts (empty line or EOF) —
//     callers exit cleanly without doing any destructive work.
//   - (nil, err) on a malformed selection (unknown org key, etc.)
//     or an underlying read / CSV failure.
//
// When autoYes is true, the helper prints the same list but skips
// the prompt and returns every org — for non-interactive callers
// (CI, scripts) that have already taken responsibility for the wipe.
func confirmResetOrgs(exportDir string, autoYes bool, in io.Reader, out io.Writer) ([]string, error) {
	orgs, err := loadResetTargetOrgs(exportDir)
	if err != nil {
		return nil, err
	}
	if len(orgs) == 0 {
		return nil, fmt.Errorf("no SonarCloud organizations found in %s/organizations.csv — nothing to reset", exportDir)
	}
	projCounts := loadProjectsPerOrg(exportDir)

	fmt.Fprintln(out, "The following SonarCloud organizations are targeted by this reset:")
	for _, o := range orgs {
		fmt.Fprintf(out, "  - %s (%d projects)\n", o, projCounts[o])
	}

	if autoYes {
		return orgs, nil
	}

	fmt.Fprint(out, "\nType the org keys to reset (whitespace-separated), or press [Enter] to abort: ")
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("reading confirmation: %w", err)
	}
	typed := strings.Fields(strings.TrimSpace(line))
	if len(typed) == 0 {
		fmt.Fprintln(out, "Reset aborted.")
		return nil, nil
	}

	known := make(map[string]bool, len(orgs))
	for _, o := range orgs {
		known[o] = true
	}

	var confirmed, unknown []string
	seen := make(map[string]bool, len(typed))
	for _, t := range typed {
		if seen[t] {
			continue
		}
		seen[t] = true
		if known[t] {
			confirmed = append(confirmed, t)
		} else {
			unknown = append(unknown, t)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown org key(s): %q — must be one of the listed orgs", strings.Join(unknown, ", "))
	}
	return confirmed, nil
}

// loadResetTargetOrgs reads organizations.csv and returns every unique
// non-empty, non-SKIPPED sonarcloud_org_key, sorted for deterministic
// display.
func loadResetTargetOrgs(exportDir string) ([]string, error) {
	rows, err := structure.LoadCSV(exportDir, "organizations.csv")
	if err != nil {
		return nil, fmt.Errorf("loading organizations.csv from %s: %w", exportDir, err)
	}
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		k, _ := r["sonarcloud_org_key"].(string)
		k = strings.TrimSpace(k)
		if k == "" || k == "SKIPPED" {
			continue
		}
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// loadProjectsPerOrg returns the per-cloud-org count of projects the
// migrate tool created — i.e., exactly what reset will delete (#381
// follow-up). It mirrors runGetCreatedProjects: a union over every
// prior migrate run's createProjects JSONL, deduped by
// cloud_project_key, grouped by sonarcloud_org_key.
//
// Returns an empty map when no migrate run has happened yet so
// callers can render "(0 projects)" without erroring out — running
// reset against a fresh export_dir is a legitimate state.
func loadProjectsPerOrg(exportDir string) map[string]int {
	counts, err := migrate.MigrateCreatedProjectCounts(exportDir)
	if err != nil || counts == nil {
		return map[string]int{}
	}
	return counts
}
