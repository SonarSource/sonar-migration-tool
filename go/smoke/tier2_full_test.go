//go:build smoke

// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package smoke

// TestTier2_PathA_Transfer and TestTier2_PathB_FullPipeline drive the real
// prebuilt binary against a real SonarQube Server AND a real staging
// SonarQube Cloud organization. This tier is DESTRUCTIVE: Path B's "reset"
// subtest calls `reset --yes`, which deletes migrated entities from the
// target organization. It only runs when the operator opts in via
// SMOKE_ALLOW_DESTRUCTIVE=1 (see requireDestructive), and requireDestructive
// additionally enforces the staging host allowlist in assertHostAllowed so
// this suite can never point at a production SonarQube Cloud host.
//
// Both tests migrate project data (issues, hotspots, source code, SCM
// blame) rather than passing --skip_project_data_migration: regtest's Issues
// and Measures categories compare source vs. target project data, so
// skipping that migration here would make those categories fail every run
// regardless of whether the tool actually works. This costs real wall-clock
// time: PollCETask (go/internal/scanreport/submit.go:170) polls the Compute
// Engine task queue on a hardcoded 5-second interval that is not injectable
// from tests.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tier2Setup performs the guardrails and reachability checks shared by both
// Tier 2 tests: it loads the unified config, enforces the destructive-tier
// opt-in and staging host allowlist, and confirms both instances are
// reachable before any CLI invocation runs.
func tier2Setup(t *testing.T) smokeConfig {
	t.Helper()
	cfg := requireConfig(t)
	requireDestructive(t, cfg)
	preflight(t, cfg.sourceURL, "source SonarQube Server")
	preflight(t, cfg.targetURL, "target SonarQube Cloud")
	if cfg.org == "" {
		t.Fatalf("config has no target.default_organization set — the smoke suite needs it to scope the destructive reset")
	}
	return cfg
}

// regtestConfigPath copies the smoke config with target.export_directory
// overridden to exportDir. Unlike extract/migrate/transfer, `regtest` has no
// --export_directory (or --export_dir) flag at all (see cmd/regtest.go) — it
// only reads the field from the file passed to --config, via
// regtest.LoadConfigFile. Passing this path as a later --config to runRegtest
// overrides the earlier one: pflag applies repeated string flags in order, so
// the last one set wins.
func regtestConfigPath(t *testing.T, cfg smokeConfig, exportDir string) string {
	t.Helper()
	raw, err := os.ReadFile(cfg.path)
	if err != nil {
		t.Fatalf("reading %s: %v", cfg.path, err)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parsing %s: %v", cfg.path, err)
	}
	var target map[string]json.RawMessage
	if raw, ok := parsed["target"]; ok {
		if err := json.Unmarshal(raw, &target); err != nil {
			t.Fatalf("parsing %s target section: %v", cfg.path, err)
		}
	} else {
		target = map[string]json.RawMessage{}
	}
	dirJSON, err := json.Marshal(exportDir)
	if err != nil {
		t.Fatalf("marshaling export dir: %v", err)
	}
	target["export_directory"] = dirJSON
	targetJSON, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("marshaling target section: %v", err)
	}
	parsed["target"] = targetJSON
	out, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("marshaling scoped config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "regtest-config.json")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// tier2ProjectKey resolves the source project key the destructive tier
// operates on. The harness deliberately holds no tokens (see smokeConfig), so
// it cannot discover a real project key on its own; the operator must supply
// one that exists on the source SonarQube Server.
func tier2ProjectKey(t *testing.T) string {
	t.Helper()
	key := strings.TrimSpace(os.Getenv("SMOKE_PROJECT_KEY"))
	if key == "" {
		t.Skipf("SMOKE_PROJECT_KEY not set — set it to a real project key on the source SonarQube Server to run this destructive tier")
	}
	return key
}

// TestTier2_PathA_Transfer exercises the transfer command's single-call
// extract -> structure -> mappings -> migrate chain for one project (see
// docs/REGRESSION-TESTING-PLAN.md Path A). Subtests run in order and share
// one export directory.
func TestTier2_PathA_Transfer(t *testing.T) {
	cfg := tier2Setup(t)
	projectKey := tier2ProjectKey(t)
	exportDir := t.TempDir()

	var transferDuration time.Duration

	t.Run("reset_dry_run", func(t *testing.T) {
		defer track(t, "2A", "reset_dry_run")()

		res := runCLI(t, "reset",
			"--config", cfg.path,
			"--dry-run",
			"--organization", cfg.org,
			"--export_directory", exportDir,
		)
		if res.exitCode != 0 {
			// A dry run against a brand-new export directory has no
			// organizations.csv to scope against yet, so this can fail
			// legitimately on the very first subtest of a fresh run.
			if strings.Contains(res.combined(), "organizations.csv") {
				logf(t, "reset_dry_run: fresh export dir has no organizations.csv yet, skipping: %s\n", res.combined())
				t.Skipf("dry run against a fresh export dir has no organizations.csv yet:\n%s", res.combined())
			}
			t.Fatalf("reset --dry-run: got exit %d, want 0\n--- output ---\n%s", res.exitCode, res.combined())
		}
		if !strings.Contains(res.stdout, "Dry run: no organizations will be modified.") {
			t.Errorf("reset --dry-run stdout does not contain the expected banner:\n%s", res.stdout)
		}
		if !strings.Contains(res.stdout, cfg.org) {
			t.Errorf("reset --dry-run stdout does not mention the target organization %q:\n%s", cfg.org, res.stdout)
		}
	})

	t.Run("transfer", func(t *testing.T) {
		defer track(t, "2A", "transfer")()

		// CAUTION: transfer uses --export_dir, not --export_directory.
		res := runCLI(t, "transfer",
			"--config", cfg.path,
			"--project_key", projectKey,
			"--export_dir", exportDir,
		)
		requireExit(t, res, 0, "transfer")
		assertNoPanics(t, res.combined())
		transferDuration = res.duration
	})

	t.Run("transfer_artifacts", func(t *testing.T) {
		defer track(t, "2A", "transfer_artifacts")()

		assertPDF(t, filepath.Join(exportDir, "migration_summary.pdf"))
		assertFileContains(t, filepath.Join(exportDir, "migration_summary.md"), 512)

		runDir := latestRunDir(t, exportDir)
		for _, name := range []string{"plan.json", "run_meta.json", "run_events.jsonl", "requests.log"} {
			p := filepath.Join(runDir, name)
			if !fileExists(p) {
				t.Errorf("expected %q under the latest run directory, but it is missing", p)
			}
		}
	})

	t.Run("regtest", func(t *testing.T) {
		defer track(t, "2A", "regtest")()
		// Scoped to the one project this run actually transferred and to
		// the export dir it used — unscoped, regtest checks every project
		// on the source against the default "./migration-files" and fails
		// on projects this run never touched.
		runRegtest(t, cfg, "--project_key", projectKey, "--config", regtestConfigPath(t, cfg, exportDir))
	})

	t.Run("report_accuracy", func(t *testing.T) {
		defer track(t, "2A", "report_accuracy")()
		assertReportAccuracy(t, filepath.Join(exportDir, "migration_summary.md"), transferDuration)
	})
}

// TestTier2_PathB_FullPipeline exercises the multi-step extract -> structure
// -> mappings -> reset -> migrate flow (see docs/REGRESSION-TESTING-PLAN.md
// Path B). Both paths are required because they exercise different code
// paths. Subtests run in order and share one export directory, distinct from
// Path A's.
func TestTier2_PathB_FullPipeline(t *testing.T) {
	cfg := tier2Setup(t)
	projectKey := tier2ProjectKey(t)
	exportDir := t.TempDir()

	t.Run("extract", func(t *testing.T) {
		defer track(t, "2B", "extract")()

		res := runCLI(t, "extract",
			"--config", cfg.path,
			"--export_directory", exportDir,
			"--project_key", projectKey,
			// #583 — a deliberately permissive cutoff: exercises the
			// --branch_analyzed_after code path live without excluding this
			// project's actual branches.
			"--branch_analyzed_after", "2000-01-01",
		)
		requireExit(t, res, 0, "extract")
	})

	t.Run("structure", func(t *testing.T) {
		defer track(t, "2B", "structure")()

		res := runCLI(t, "structure",
			"--config", cfg.path,
			"--export_directory", exportDir,
		)
		requireExit(t, res, 0, "structure")

		// structure emits sonarcloud_org_key EMPTY by design: mapping a source
		// org to a Cloud org is the operator's decision. Assert the column
		// exists rather than that it holds a value.
		assertCSVColumns(t, exportDir, "organizations.csv", "sonarcloud_org_key")

		// Now perform the edit the operator would make by hand. This is
		// required, not cosmetic: `reset` derives its target organization list
		// from this column (loadResetTargetOrgs in go/cmd/reset.go), so in an
		// isolated export dir it would otherwise find no orgs and refuse.
		setOrgMapping(t, exportDir, cfg.org)
	})

	t.Run("mappings", func(t *testing.T) {
		defer track(t, "2B", "mappings")()

		res := runCLI(t, "mappings",
			"--config", cfg.path,
			"--export_directory", exportDir,
		)
		requireExit(t, res, 0, "mappings")
	})

	t.Run("reset", func(t *testing.T) {
		defer track(t, "2B", "reset")()

		// This is the suite's one destructive call: it deletes migrated
		// entities from the SonarCloud organization(s) scoped by
		// --organization. It runs after "structure" (not before) so
		// organizations.csv already exists on disk for org scoping.
		res := runCLI(t, "reset",
			"--config", cfg.path,
			"--yes",
			"--organization", cfg.org,
			"--export_directory", exportDir,
		)
		requireExit(t, res, 0, "reset")
	})

	t.Run("migrate", func(t *testing.T) {
		defer track(t, "2B", "migrate")()

		res := runCLI(t, "migrate",
			"--config", cfg.path,
			"--export_directory", exportDir,
			"--project_key", projectKey,
			// #583 — same permissive cutoff as extract; exercises the
			// migrate-side filter live.
			"--branch_analyzed_after", "2000-01-01",
		)
		requireExit(t, res, 0, "migrate")
		assertNoPanics(t, res.combined())
	})

	t.Run("regtest", func(t *testing.T) {
		defer track(t, "2B", "regtest")()
		// Scoped to the one project this run actually migrated and to the
		// export dir it used — unscoped, regtest checks every project on
		// the source against the default "./migration-files" and fails on
		// projects this run never touched.
		runRegtest(t, cfg, "--project_key", projectKey, "--config", regtestConfigPath(t, cfg, exportDir))
	})

	t.Run("migrate_idempotent", func(t *testing.T) {
		defer track(t, "2B", "migrate_idempotent")()

		// Re-running migrate against already-migrated entities exercises the
		// already-exists code paths (#550): groups, templates, gates, and
		// profiles that already exist on the target must be recognised as
		// "no action needed" rather than counted as new failures.
		res := runCLI(t, "migrate",
			"--config", cfg.path,
			"--export_directory", exportDir,
			"--project_key", projectKey,
		)
		requireExit(t, res, 0, "migrate (idempotent re-run)")
		assertNoPanics(t, res.combined())
		runRegtest(t, cfg, "--project_key", projectKey, "--config", regtestConfigPath(t, cfg, exportDir))
	})

	t.Run("sync_issues", func(t *testing.T) {
		defer track(t, "2B", "sync_issues")()

		// CAUTION: sync-issues shares transfer's flagExportDir constant
		// (go/cmd), so it takes --export_dir, not --export_directory.
		res := runCLI(t, "sync-issues",
			"--config", cfg.path,
			"--project_key", projectKey,
			"--export_dir", exportDir,
		)
		requireExit(t, res, 0, "sync-issues")
	})

	t.Run("analysis_report", func(t *testing.T) {
		defer track(t, "2B", "analysis_report")()

		runDir := latestRunDir(t, exportDir)
		runID := filepath.Base(runDir)

		res := runCLI(t, "analysis_report", runID, "--export_directory", exportDir)
		requireExit(t, res, 0, "analysis_report")

		// This pipeline migrates project data, so requests.log normally holds
		// POST requests to report on. If the test project happens to have no
		// issues/hotspots to push, requests.log holds none either:
		// cmd/analysis_report.go prints this exact message and exits 0
		// without writing the CSV. That is the tool working as designed,
		// not a failure.
		if strings.Contains(res.stdout, "No POST requests found in requests.log") {
			return
		}

		reportPath := filepath.Join(runDir, "final_analysis_report.csv")
		raw, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatalf("expected %q to exist: %v", reportPath, err)
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		if len(lines) < 2 {
			t.Errorf("%q has fewer than 2 lines (want a header plus at least one data row): got %d", reportPath, len(lines))
		}
	})
}

// mdSeparatorCellRE matches one cell of a Markdown table's header-separator
// row, e.g. the "---" or ":---:" pieces of "|---|:---:|---|".
var mdSeparatorCellRE = regexp.MustCompile(`^:?-+:?$`)

// assertReportAccuracy checks migration_summary.md against the defect
// classes fixed by commit 4a76134 ("make the migration report's numbers and
// outcomes trustworthy"). The exact Markdown layout is NOT pinned by this
// test: every check here is regex-based and logs + skips itself rather than
// failing when its pattern finds nothing, so a future layout change degrades
// this test to a no-op instead of a false failure.
func assertReportAccuracy(t *testing.T, mdPath string, observedWallClock time.Duration) {
	t.Helper()

	raw, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("reading %q: %v", mdPath, err)
	}
	lines := strings.Split(string(raw), "\n")

	// Assertion A guards the defect where skipped items whose reason had no
	// entry in skipReasonOrder were counted in a section's total but never
	// rendered, so a row's own parts stopped summing to its own whole (e.g.
	// "284 skipped" above a table that only listed 274 rows).
	assertAttemptedRowsBalance(t, lines)

	// Assertion B guards the defect where the retry ledger's Last Status
	// column was blank because an HTTP status logged as a number was read
	// back as a string; that class of bug renders status codes as quoted
	// strings (e.g. "404") instead of plain numbers.
	quotedStatusRE := regexp.MustCompile(`"\d{3}"`)
	if matches := quotedStatusRE.FindAllString(string(raw), -1); len(matches) > 0 {
		t.Errorf("migration_summary.md renders an HTTP status code as a quoted string (found %v) — status codes must render as plain numbers, not quoted strings (4a76134)", matches)
	}

	// Assertion C guards the defect where the four coarse phase durations
	// summed per-task times of tasks that run concurrently (reporting, per
	// the fix's own example, 47.9s of work inside a 26.7s run) instead of
	// measuring the wall-clock union.
	assertTotalDurationSane(t, lines, observedWallClock)

	// Assertion D (#583) is best-effort: this run's --branch_analyzed_after
	// cutoff (2000-01-01, see TestTier2_PathB_FullPipeline) is deliberately
	// permissive, so the test project's real branches are not expected to
	// trigger a force-included main branch. It only checks that IF the
	// section is present, it renders the exact heading the report package
	// emits — never fails when the section is absent.
	assertForcedMainBranchSectionWellFormed(t, string(raw))
}

// assertForcedMainBranchSectionWellFormed implements Assertion D (#583): if
// migration_summary.md contains a "Force-Included Main Branches" section
// (WarningLedger.ForcedMainBranches, go/internal/report/summary), it must
// use the exact heading text the report package renders. Logs and returns
// without failing when the section is absent — a live run's real branch
// dates aren't controlled by this test, so force-inclusion is not
// guaranteed to occur.
func assertForcedMainBranchSectionWellFormed(t *testing.T, raw string) {
	t.Helper()

	const heading = "Force-Included Main Branches"
	if !strings.Contains(raw, heading) {
		t.Logf("assertForcedMainBranchSectionWellFormed: no %q section found in migration_summary.md — this run's branches did not trigger --branch_analyzed_after force-inclusion, skipping", heading)
		return
	}
	if !strings.Contains(raw, "Analysis Date") || !strings.Contains(raw, "Cutoff") {
		t.Errorf("migration_summary.md has a %q section but is missing the expected Analysis Date / Cutoff columns", heading)
	}
}

// assertAttemptedRowsBalance implements Assertion A: it looks for a Markdown
// table header row naming attempted/succeeded/skipped/failed columns (in any
// order, case-insensitive) and, for every parseable data row under it,
// checks succeeded+skipped+failed == attempted. It logs and returns without
// failing when no such header is found anywhere in the report.
func assertAttemptedRowsBalance(t *testing.T, lines []string) {
	t.Helper()

	found := false
	for i := 0; i < len(lines); i++ {
		header := lines[i]
		if !strings.Contains(header, "|") {
			continue
		}
		lower := strings.ToLower(header)
		if !strings.Contains(lower, "attempted") || !strings.Contains(lower, "succeeded") ||
			!strings.Contains(lower, "skipped") || !strings.Contains(lower, "failed") {
			continue
		}
		found = true

		cols := splitMarkdownRow(header)
		idx := map[string]int{}
		for ci, c := range cols {
			cl := strings.ToLower(strings.TrimSpace(c))
			switch {
			case strings.Contains(cl, "attempted"):
				idx["attempted"] = ci
			case strings.Contains(cl, "succeeded"):
				idx["succeeded"] = ci
			case strings.Contains(cl, "skipped"):
				idx["skipped"] = ci
			case strings.Contains(cl, "failed"):
				idx["failed"] = ci
			}
		}
		if len(idx) != 4 {
			logf(t, "report_accuracy: header row %q named all four words but only %d resolved to distinct columns; skipping this table\n", header, len(idx))
			continue
		}

		for j := i + 1; j < len(lines) && strings.Contains(lines[j], "|"); j++ {
			row := splitMarkdownRow(lines[j])
			if isMarkdownSeparatorRow(row) {
				continue
			}
			attempted, okA := parseIntCell(row, idx["attempted"])
			succeeded, okS := parseIntCell(row, idx["succeeded"])
			skipped, okK := parseIntCell(row, idx["skipped"])
			failed, okF := parseIntCell(row, idx["failed"])
			if !okA || !okS || !okK || !okF {
				// Not every row under the header is numeric (e.g. a Totals
				// row phrased differently, or a non-numeric placeholder) —
				// skip rather than fail on a row this check cannot read.
				continue
			}
			if succeeded+skipped+failed != attempted {
				t.Errorf("report_accuracy: row does not balance (succeeded=%d + skipped=%d + failed=%d = %d, want attempted=%d): %q",
					succeeded, skipped, failed, succeeded+skipped+failed, attempted, lines[j])
			}
		}
	}

	if !found {
		logf(t, "report_accuracy: no table header naming attempted/succeeded/skipped/failed found; skipping Assertion A\n")
	}
}

// splitMarkdownRow splits one Markdown table row into its cells, preserving
// empty cells so column indices stay aligned with the header row.
func splitMarkdownRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// isMarkdownSeparatorRow reports whether row is a Markdown table's header
// separator row (e.g. the cells of "|---|:---:|---|").
func isMarkdownSeparatorRow(row []string) bool {
	for _, c := range row {
		if c == "" {
			continue
		}
		if !mdSeparatorCellRE.MatchString(c) {
			return false
		}
	}
	return true
}

// parseIntCell parses row[idx] as an integer, tolerating thousands
// separators (commas) a human-readable report might use. It returns ok=false
// rather than failing on anything it cannot parse.
func parseIntCell(row []string, idx int) (int, bool) {
	if idx < 0 || idx >= len(row) {
		return 0, false
	}
	cell := strings.ReplaceAll(strings.TrimSpace(row[idx]), ",", "")
	if cell == "" {
		return 0, false
	}
	n, err := strconv.Atoi(cell)
	if err != nil {
		return 0, false
	}
	return n, true
}

// assertTotalDurationSane implements Assertion C: it looks for a line
// mentioning "duration" or "elapsed" (case-insensitive) that also carries a
// Go-style duration value (e.g. "1m2.5s", "350ms") and checks it does not
// exceed 2x the CLI's own observed wall-clock duration for the run — that
// gap is exactly what "summed per-task times" looks like from the outside.
// It logs and returns without failing when no such line is found.
func assertTotalDurationSane(t *testing.T, lines []string, observedWallClock time.Duration) {
	t.Helper()

	labelRE := regexp.MustCompile(`(?i)duration|elapsed`)
	// Ordered h, ms, m, s so the greedy repeated group prefers the two-letter
	// "ms" unit over matching a bare "m" and stranding the trailing "s".
	valueRE := regexp.MustCompile(`(?:\d+(?:\.\d+)?(?:h|ms|m|s))+`)

	for _, line := range lines {
		if !labelRE.MatchString(line) {
			continue
		}
		rawValue := valueRE.FindString(line)
		if rawValue == "" {
			continue
		}
		reported, err := time.ParseDuration(rawValue)
		if err != nil {
			logf(t, "report_accuracy: found duration-like text %q on line %q but could not parse it: %v\n", rawValue, line, err)
			continue
		}
		maxAllowed := observedWallClock * 2
		if reported > maxAllowed {
			t.Errorf("report_accuracy: reported duration %s (line %q) is more than 2x the observed wall-clock transfer duration %s — looks like durations were summed instead of measured as wall-clock (4a76134)",
				reported, strings.TrimSpace(line), observedWallClock)
		} else {
			logf(t, "report_accuracy: reported duration %s is within 2x observed wall-clock %s (line %q)\n", reported, observedWallClock, line)
		}
		return
	}
	logf(t, "report_accuracy: no duration-like line found near \"duration\"/\"elapsed\"; skipping Assertion C\n")
}
