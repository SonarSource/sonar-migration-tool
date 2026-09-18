//go:build smoke

// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

// Tier 0 covers the CLI contract: every subcommand still exists, every flag is
// still registered, and every up-front validation error still fires with a
// recognisable message and a non-zero exit code.
//
// This tier needs NO network and NO credentials. It is the cheapest guard
// against the regression that bites hardest when features land: a command or
// flag silently disappearing.
//
// Every expectation in this file was verified by running the real binary —
// none of the exit codes or message substrings are guesses.
package smoke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/version"
)

// allCommands is every subcommand the tool registers, excluding Cobra's
// built-in "completion" and "help".
//
// Derived from `sonar-migration-tool --help`. Keep it in sync: the length
// assertion in TestTier0_CommandInventory exists so that adding a command
// without updating this suite fails loudly.
var allCommands = []string{
	"analysis_report",
	"extract",
	"gui",
	"mappings",
	"migrate",
	"predictive-report",
	"regtest",
	"report",
	"reset",
	"structure",
	"sync-issues",
	"transfer",
	"wizard",
}

// expectedCommandCount guards against silent additions or removals.
const expectedCommandCount = 13

// commandFlags is the set of flags each command must still register, taken
// from each command's own `--help` output.
//
// Note the deliberate inconsistency preserved here: transfer and sync-issues
// take --export_dir, while every other command takes --export_directory.
// Do not "fix" one to match the other in this table — it documents reality.
//
// --help and --debug are registered on every command and are asserted
// separately rather than repeated in each row.
var commandFlags = map[string][]string{
	"analysis_report": {"--export_directory"},
	"extract": {
		"--cert_password", "--concurrency", "--config", "--export_directory",
		"--extract_id", "--extract_type", "--history_max_points",
		"--history_min_interval_days", "--key_file_path", "--migrate_history",
		"--objects", "--pem_file_path", "--project_key", "--skip_issue_sync",
		"--skip_project_data_migration", "--source_token", "--source_url",
		"--target_task", "--timeout",
	},
	"gui":      {"--addr", "--config", "--export_directory", "--no-browser"},
	"mappings": {"--config", "--export_directory"},
	"migrate": {
		"--api_max_rate_per_min", "--concurrency", "--config",
		"--default_organization", "--edition",
		"--enterprise_key", "--exclude_branches", "--export_directory",
		"--fast_sync", "--migrate_history", "--objects",
		"--project_data_build_concurrency", "--project_key",
		"--project_key_pattern", "--run_id", "--skip_issue_sync",
		"--skip_profiles", "--skip_project_data_migration", "--target_task",
		"--target_token", "--target_url", "--timeout",
	},
	"predictive-report": {"--config", "--export_directory"},
	"regtest":           {"--concurrency", "--config", "--format", "--project_key", "--verbose"},
	"report":            {"--export_directory", "--filename", "--report_type"},
	"reset": {
		"--api_max_rate_per_min", "--concurrency", "--config", "--dry-run",
		"--edition", "--export_directory", "--organization", "--target_url",
		"--yes",
	},
	"structure": {"--config", "--export_directory"},
	"sync-issues": {
		"--api_max_rate_per_min", "--cert_password", "--concurrency",
		"--config", "--default_organization",
		"--enterprise_key", "--export_dir", "--fast_sync", "--key_file_path",
		"--pem_file_path", "--project_key", "--project_key_pattern",
		"--source_token", "--source_url", "--target_token", "--target_url",
		"--timeout",
	},
	"transfer": {
		"--api_max_rate_per_min", "--cert_password", "--concurrency",
		"--config", "--default_organization",
		"--edition", "--enterprise_key", "--exclude_branches", "--export_dir",
		"--fast_sync", "--history_max_points", "--history_min_interval_days",
		"--key_file_path", "--migrate_history", "--pem_file_path",
		"--project_key", "--project_key_pattern", "--skip_issue_sync",
		"--skip_project_data_migration", "--source_token", "--source_url",
		"--target_token", "--target_url", "--timeout", "--unsupported_languages",
	},
	"wizard": {"--export_directory"},
}

// scratchConfig writes a throwaway unified config into a temp dir. Its URLs
// point at 127.0.0.1:1 so that any command which slips past validation and
// attempts a network call fails instantly rather than hanging.
//
// The real config.json is never used by Tier 0.
func scratchConfig(t *testing.T) string {
	t.Helper()
	const body = `{
  "source": {"url": "http://127.0.0.1:1/", "token": "not-a-real-token"},
  "target": {
    "url": "http://127.0.0.1:1/",
    "token": "not-a-real-token",
    "enterprise_key": "smoke-test-enterprise",
    "default_organization": "smoke-test-org"
  }
}`
	path := filepath.Join(t.TempDir(), "scratch-config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing scratch config: %v", err)
	}
	return path
}

// TestTier0_CommandInventory asserts the registered command set has not
// drifted from allCommands.
func TestTier0_CommandInventory(t *testing.T) {
	defer track(t, "0", "command inventory")()

	if len(allCommands) != expectedCommandCount {
		t.Fatalf("allCommands has %d entries, expected %d — update expectedCommandCount "+
			"deliberately when adding or removing a command", len(allCommands), expectedCommandCount)
	}

	res := runCLI(t, "--help")
	requireExit(t, res, 0, "root --help")

	// Names in the "Available Commands:" block, minus Cobra's built-ins.
	var got []string
	if _, after, ok := strings.Cut(res.stdout, "Available Commands:"); ok {
		for _, line := range strings.Split(after, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				// The blank line right after the heading, and the one
				// separating the command list from "Flags:" below.
				continue
			}
			if len(fields) < 2 || strings.HasSuffix(fields[0], ":") {
				break
			}
			if name := fields[0]; name != "completion" && name != "help" {
				got = append(got, name)
			}
		}
	}
	if len(got) != expectedCommandCount {
		t.Fatalf("the binary registers %d commands %v, but this suite knows %d — update allCommands and expectedCommandCount", len(got), got, expectedCommandCount)
	}

	for _, cmd := range allCommands {
		if !strings.Contains(res.stdout, cmd) {
			t.Errorf("root --help does not list the %q command", cmd)
		}
	}
}

// TestTier0_Help asserts --help succeeds for the root and every subcommand.
func TestTier0_Help(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		defer track(t, "0", "root --help")()
		res := runCLI(t, "--help")
		requireExit(t, res, 0, "root --help")
		if strings.TrimSpace(res.stdout) == "" {
			t.Error("root --help produced no stdout")
		}
	})

	for _, cmd := range allCommands {
		cmd := cmd
		t.Run(cmd, func(t *testing.T) {
			defer track(t, "0", cmd+" --help")()
			res := runCLI(t, cmd, "--help")
			requireExit(t, res, 0, cmd+" --help")
			if strings.TrimSpace(res.stdout) == "" {
				t.Errorf("%s --help produced no stdout", cmd)
			}
		})
	}
}

// TestTier0_Version asserts --version reports the compiled-in version. It
// compares against the version package rather than a hard-coded string so a
// release bump does not break the suite.
func TestTier0_Version(t *testing.T) {
	defer track(t, "0", "--version")()

	res := runCLI(t, "--version")
	requireExit(t, res, 0, "--version")
	if !strings.Contains(res.stdout, version.Version) {
		t.Errorf("--version output %q does not contain the compiled version %q\n"+
			"(if the binary is stale, run `make build`)", strings.TrimSpace(res.stdout), version.Version)
	}
}

// TestTier0_FlagRegistration asserts every documented flag still appears in
// its command's help text.
func TestTier0_FlagRegistration(t *testing.T) {
	for _, cmd := range allCommands {
		cmd := cmd
		t.Run(cmd, func(t *testing.T) {
			defer track(t, "0", cmd+" flags")()

			res := runCLI(t, cmd, "--help")
			requireExit(t, res, 0, cmd+" --help")
			help := res.combined()

			// Registered on every command via the root persistent flags.
			for _, universal := range []string{"--help", "--debug"} {
				if !strings.Contains(help, universal) {
					t.Errorf("%s --help is missing the universal flag %s", cmd, universal)
				}
			}

			for _, flag := range commandFlags[cmd] {
				if !strings.Contains(help, flag) {
					t.Errorf("%s --help is missing the flag %s", cmd, flag)
				}
			}
		})
	}
}

// TestTier0_ExitCodeMatrix asserts each up-front validation error still fires.
//
// Every wantExit and wantContains below was observed by running the real
// binary — all of these paths exit 1. Exit codes 2 and 3 exist
// (common.NewExitError in internal/migrate/org_mapping.go) but are only
// reachable once org mapping is under way, which needs live data, so they are
// asserted in Tier 2 rather than here.
func TestTier0_ExitCodeMatrix(t *testing.T) {
	cfg := scratchConfig(t)
	exportDir := t.TempDir()

	cases := []struct {
		name         string
		args         []string
		wantExit     int
		wantContains string
	}{
		{
			name:         "transfer_without_project_key",
			args:         []string{"transfer", "--config", cfg},
			wantExit:     1,
			wantContains: "project key is required",
		},
		{
			name:         "regtest_without_config",
			args:         []string{"regtest"},
			wantExit:     1,
			wantContains: "--config is required",
		},
		{
			name:         "reset_without_token_or_enterprise_key",
			args:         []string{"reset"},
			wantExit:     1,
			wantContains: "TOKEN and ENTERPRISE_KEY are required",
		},
		{
			name:         "extract_with_invalid_objects_category",
			args:         []string{"extract", "--config", cfg, "--export_directory", exportDir, "--objects", "bogus"},
			wantExit:     1,
			wantContains: `invalid --objects value "bogus"`,
		},
		{
			name:         "extract_with_unparseable_project_key",
			args:         []string{"extract", "--config", cfg, "--export_directory", exportDir, "--project_key", "("},
			wantExit:     1,
			wantContains: `invalid project key pattern "("`,
		},
		{
			name:         "transfer_with_invalid_unsupported_languages",
			args:         []string{"transfer", "--config", cfg, "--project_key", "x", "--unsupported_languages", "bogus"},
			wantExit:     1,
			wantContains: `invalid unsupported_languages value "bogus"`,
		},
		{
			name:         "analysis_report_without_run_id",
			args:         []string{"analysis_report"},
			wantExit:     1,
			wantContains: "accepts 1 arg(s), received 0",
		},
		{
			// #573: --api_max_rate_per_min must abort outright when out of
			// its [100, 1500] range, not clamp silently. Checked before the
			// TOKEN/ENTERPRISE_KEY requirement, so it fires even without
			// credentials.
			name:         "migrate_with_out_of_range_api_max_rate_per_min",
			args:         []string{"migrate", "--target_token", "x", "--enterprise_key", "y", "--api_max_rate_per_min", "50"},
			wantExit:     1,
			wantContains: "--api_max_rate_per_min must be between 100 and 1500",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			defer track(t, "0", tc.name)()

			res := runCLI(t, tc.args...)
			if res.exitCode != tc.wantExit {
				t.Errorf("got exit %d, want %d\n--- output ---\n%s", res.exitCode, tc.wantExit, res.combined())
			}
			if !strings.Contains(res.combined(), tc.wantContains) {
				t.Errorf("output does not contain %q\n--- output ---\n%s", tc.wantContains, res.combined())
			}
		})
	}
}

// TestTier0_SecretScrubbing proves the credential scrubber actually redacts,
// rather than assuming it does.
//
// This test exists because a live run produced logs containing no [REDACTED]
// markers at all: the scrubber had simply never fired, so its correctness was
// unobserved. A safety property nobody has watched work is not yet a property.
func TestTier0_SecretScrubbing(t *testing.T) {
	defer track(t, "0", "secret scrubbing")()

	// An opaque token with no recognisable prefix — the case the regex alone
	// cannot catch, and the reason requireConfig registers literal values.
	const opaque = "9f3c1b7ae2d84f60b5c19e7d3a8f2461"
	registerSecret(opaque)

	cases := []struct {
		name  string
		input string
		leak  string
	}{
		{"sonarqube_user_token", "token=squ_0123456789abcdef0123", "squ_0123456789abcdef0123"},
		{"analysis_token", "sqa_fedcba98765432100987 in a log line", "sqa_fedcba98765432100987"},
		{"project_token", "using sqp_1122334455667788aabb now", "sqp_1122334455667788aabb"},
		{"authorization_header", "Authorization: Bearer abc123def456ghi", "abc123def456ghi"},
		{"bearer_alone", "sent bearer abc123def456ghi upstream", "abc123def456ghi"},
		{"opaque_registered_token", "target token " + opaque + " used", opaque},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := scrubSecrets(tc.input)
			if strings.Contains(got, tc.leak) {
				t.Errorf("scrubSecrets leaked the credential\n input: %q\noutput: %q\n leaked: %q",
					tc.input, got, tc.leak)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("scrubSecrets(%q) = %q — expected a [REDACTED] marker", tc.input, got)
			}
		})
	}

	t.Run("benign_text_untouched", func(t *testing.T) {
		const benign = "Structure complete: 1 organizations, 1 projects"
		if got := scrubSecrets(benign); got != benign {
			t.Errorf("scrubSecrets mangled benign output:\n want %q\n  got %q", benign, got)
		}
	})

	t.Run("short_values_not_registered", func(t *testing.T) {
		// Registering a very short string would mangle unrelated output.
		registerSecret("abc")
		const benign = "abc is a normal word in abcdef"
		if got := scrubSecrets(benign); got != benign {
			t.Errorf("a too-short secret was registered and mangled output:\n want %q\n  got %q", benign, got)
		}
	})
}
