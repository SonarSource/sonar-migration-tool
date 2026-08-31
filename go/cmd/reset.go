// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/spf13/cobra"
)

const flagResetYes = "yes"

// flagResetOrganization narrows the candidate SonarCloud organizations
// considered for reset to those whose key fully matches the given
// pattern (#550). Applied before confirmResetOrgs's prompt / --yes
// branch, mirroring --project_key's anchored-regex semantics on
// transfer (see anchoredResetOrgPattern).
const flagResetOrganization = "organization"

// flagResetDryRun causes the reset command to build and print the
// reset plan (organizations in scope, tasks that would run) without
// making any destructive API call (#550).
const flagResetDryRun = "dry-run"

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
		orgPattern, _ := cmd.Flags().GetString(flagResetOrganization)
		dryRun, _ := cmd.Flags().GetBool(flagResetDryRun)

		fmt.Fprintln(os.Stdout, "WARNING: This will delete migrated entities from the listed SonarCloud organizations.")
		// cfg.ConfirmedOrgs may already carry a config-file-supplied
		// confirmed_orgs list (#550) — pass it through as the preset so
		// a --yes run without --organization can honor it instead of
		// defaulting to "every mapped org".
		confirmed, err := confirmResetOrgs(cfg.ExportDirectory, autoYes, orgPattern, cfg.ConfirmedOrgs, os.Stdin, os.Stdout)
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
		cfg.DryRun = dryRun

		if err := migrate.RunReset(cmd.Context(), cfg); err != nil {
			return err
		}
		if !dryRun {
			printExportDirNotice(cfg.ExportDirectory)
		}
		return nil
	},
}

func init() {
	f := resetCmd.Flags()
	f.String("config", "", "Path to JSON configuration file (same format as migrate --config)")
	f.String("edition", "enterprise", "SonarQube Cloud license edition")
	f.String(flagTargetURL, "https://sonarcloud.io/", "URL of SonarQube Cloud")
	// Deprecated alias (#406): kept so existing scripts keep working.
	f.String("url", "https://sonarcloud.io/", "")
	_ = f.MarkDeprecated("url", "use --target_url instead")
	f.Int("concurrency", 25, "Maximum number of concurrent requests")
	f.String("export_directory", DefaultExportDirectory, "Directory to place all interim files")
	f.Bool(flagResetYes, false, "Skip the interactive confirmation prompt and reset every listed organization (intended for non-interactive / scripted use). #381.")
	f.String(flagResetOrganization, "", "Regexp (anchored full-match) narrowing the candidate organizations to reset to those whose sonarcloud_org_key matches, applied before the confirmation prompt / --yes. E.g. \"BANKING_.+\" matches every org key starting with BANKING_. #550.")
	f.Bool(flagResetDryRun, false, "Print which organizations are in scope and which tasks would run, without deleting anything. #550.")
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

	// Flags override everything. Apply the deprecated --url alias first
	// so the primary --target_url wins when both are passed (#406).
	overrideString(cmd, "edition", &cfg.Edition)
	overrideString(cmd, "url", &cfg.URL)
	overrideString(cmd, flagTargetURL, &cfg.URL)
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

// anchoredResetOrgPattern compiles pattern as a full-match regex,
// implicitly anchored with ^ and $ (#550, mirroring #529's
// anchoredProjectKeyPattern on transfer) — "BANKING_.+" matches only
// org keys starting with "BANKING_", never a key with that substring
// somewhere in the middle. A plain literal org key with no regex
// metacharacters matches only itself.
func anchoredResetOrgPattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile("^(?:" + pattern + ")$")
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
// orgPattern, when non-empty, is compiled as an anchored full-match
// regex (#550, see anchoredResetOrgPattern) and narrows the candidate
// org list down to matching keys BEFORE anything else in this function
// runs — the prompt, the --yes shortcut, and presetOrgs all only ever
// see the filtered set.
//
// presetOrgs is an optional config-file-supplied confirmed_orgs list
// (#550). When autoYes is true and orgPattern is empty, a non-empty
// presetOrgs is validated against the candidate list and used directly
// instead of "every candidate org" — letting a config-driven --yes run
// honor a scoped selection instead of defaulting to the whole
// enterprise. presetOrgs is ignored in every other case (interactive
// prompt, or when --organization already narrowed the list).
//
// When autoYes is true and neither orgPattern nor presetOrgs apply,
// the helper prints the candidate list but skips the prompt and
// returns every candidate org — for non-interactive callers (CI,
// scripts) that have already taken responsibility for the wipe.
func confirmResetOrgs(exportDir string, autoYes bool, orgPattern string, presetOrgs []string, in io.Reader, out io.Writer) ([]string, error) {
	orgs, err := loadResetTargetOrgs(exportDir)
	if err != nil {
		return nil, err
	}
	if orgPattern != "" {
		orgs, err = filterOrgsByPattern(orgs, orgPattern)
		if err != nil {
			return nil, err
		}
		if len(orgs) == 0 {
			return nil, fmt.Errorf("no SonarCloud organization key matches --%s %q in %s/organizations.csv", flagResetOrganization, orgPattern, exportDir)
		}
	}
	if len(orgs) == 0 {
		return nil, fmt.Errorf("no SonarCloud organizations found in %s/organizations.csv — nothing to reset", exportDir)
	}
	projCounts := loadProjectsPerOrg(exportDir)

	fmt.Fprintln(out, "The following SonarCloud organizations are targeted by this reset:")
	for _, o := range orgs {
		fmt.Fprintf(out, "  - %s (%d projects)\n", o, projCounts[o])
	}

	known := make(map[string]bool, len(orgs))
	for _, o := range orgs {
		known[o] = true
	}

	if autoYes {
		return confirmResetOrgsAutoYes(orgs, orgPattern, presetOrgs, known, out)
	}
	return confirmResetOrgsInteractive(known, in, out)
}

// filterOrgsByPattern narrows orgs to those whose key fully matches the
// anchored --organization regex pattern (#550).
func filterOrgsByPattern(orgs []string, orgPattern string) ([]string, error) {
	re, err := anchoredResetOrgPattern(orgPattern)
	if err != nil {
		return nil, fmt.Errorf("invalid --%s pattern %q: %w", flagResetOrganization, orgPattern, err)
	}
	var filtered []string
	for _, o := range orgs {
		if re.MatchString(o) {
			filtered = append(filtered, o)
		}
	}
	return filtered, nil
}

// resolveOrgSelection deduplicates candidates (first-seen order) and
// splits them into those present in known vs. not. Shared by the
// interactive-typed-input path and the config-file presetOrgs path,
// which otherwise repeat the identical dedupe-and-classify logic.
func resolveOrgSelection(candidates []string, known map[string]bool) (confirmed, unknown []string) {
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if seen[c] {
			continue
		}
		seen[c] = true
		if known[c] {
			confirmed = append(confirmed, c)
		} else {
			unknown = append(unknown, c)
		}
	}
	return confirmed, unknown
}

// confirmResetOrgsAutoYes implements the --yes shortcut: honor a
// config-file confirmed_orgs selection when present and --organization
// didn't already narrow the list, else return every candidate org.
func confirmResetOrgsAutoYes(orgs []string, orgPattern string, presetOrgs []string, known map[string]bool, out io.Writer) ([]string, error) {
	if orgPattern == "" && len(presetOrgs) > 0 {
		confirmed, unknown := resolveOrgSelection(presetOrgs, known)
		if len(unknown) > 0 {
			return nil, fmt.Errorf("confirmed_orgs from config file contains unknown org key(s): %q — must be one of the listed orgs", strings.Join(unknown, ", "))
		}
		sort.Strings(confirmed)
		fmt.Fprintf(out, "Using confirmed_orgs from config file: %s\n", strings.Join(confirmed, ", "))
		return confirmed, nil
	}
	return orgs, nil
}

// confirmResetOrgsInteractive prompts the operator to type the org keys
// to reset, aborting cleanly on an empty line/EOF and erroring on any
// unknown key.
func confirmResetOrgsInteractive(known map[string]bool, in io.Reader, out io.Writer) ([]string, error) {
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

	confirmed, unknown := resolveOrgSelection(typed, known)
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
