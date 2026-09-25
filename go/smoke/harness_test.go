//go:build smoke

// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package smoke

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/regtest"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// ---------------------------------------------------------------------------
// Safety guardrails
//
// This suite drives the real binary against real instances and Tier 2 calls
// `reset --yes`, which deletes migrated entities. Everything in this section
// exists to make it impossible to do that to production by accident.
// ---------------------------------------------------------------------------

// deniedHosts are production SonarQube Cloud hosts. The smoke suite performs
// destructive resets, so these can never be allowlisted, by any env var.
var deniedHosts = map[string]bool{
	"sonarcloud.io":     true,
	"www.sonarcloud.io": true,
	"api.sonarcloud.io": true,
	"sonarqube.us":      true,
	"www.sonarqube.us":  true,
	"api.sonarqube.us":  true,
}

// assertHostAllowed fails the test unless rawURL points at an allowlisted
// staging host. The production denylist is checked first and is absolute:
// SMOKE_ALLOW_HOST cannot defeat it.
func assertHostAllowed(t *testing.T, rawURL string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("target URL %q is unparseable: %v", rawURL, err)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		t.Fatalf("target URL %q has no host", rawURL)
	}
	if deniedHosts[host] {
		t.Fatalf("REFUSING TO RUN: target host %q is a production SonarQube Cloud host. "+
			"The smoke suite performs destructive resets and must never point at production.", host)
	}
	allowed := "sc-staging.io"
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("SMOKE_ALLOW_HOST"))); v != "" {
		if deniedHosts[v] {
			t.Fatalf("REFUSING TO RUN: SMOKE_ALLOW_HOST=%q is a production host and cannot be allowlisted.", v)
		}
		allowed = v
	}
	if host != allowed && !strings.HasSuffix(host, "."+allowed) {
		t.Fatalf("REFUSING TO RUN: target host %q is not the allowlisted staging host %q. "+
			"Set SMOKE_ALLOW_HOST to override (production hosts can never be allowlisted).", host, allowed)
	}
}

// secretRE matches SonarQube token formats and Authorization headers.
//
// The authorization branch consumes to end-of-line ([^\n]*) rather than a
// single \S+ token. An earlier version used `authorization:\s*\S+`, which
// matched only "Authorization: Bearer" — stopping at the space and leaking the
// credential that followed it. TestTier0_SecretScrubbing covers that case.
var secretRE = regexp.MustCompile(`(?i)(squ_|sqa_|sqp_)[A-Za-z0-9._-]{8,}|authorization:[^\n]*|bearer\s+\S+`)

// literalSecrets holds the exact credential strings loaded from the config
// file, so they are redacted regardless of format.
//
// The pattern-based secretRE above only recognises SonarQube's squ_/sqa_/sqp_
// prefixes. A SonarQube Cloud token that is just an opaque random string would
// slip straight through it. Registering the literal values closes that gap:
// the harness reads the whole config file into memory anyway, so this adds no
// new exposure, and it makes "a real credential cannot reach a log file" a
// property of the code rather than a hope about token formats.
var (
	literalSecretsMu sync.RWMutex
	literalSecrets   []string
)

// registerSecret marks a literal string for redaction. Short values are
// ignored: redacting a 3-character string would mangle unrelated output.
func registerSecret(s string) {
	s = strings.TrimSpace(s)
	const minRedactableLen = 8
	if len(s) < minRedactableLen {
		return
	}
	literalSecretsMu.Lock()
	defer literalSecretsMu.Unlock()
	for _, existing := range literalSecrets {
		if existing == s {
			return
		}
	}
	literalSecrets = append(literalSecrets, s)
}

// scrubSecrets redacts credentials before any captured output is logged or
// written to disk. Every path that persists CLI output must go through this.
//
// Literal registered secrets are replaced first, then the format-based
// patterns catch anything not registered (for example a token that appears in
// output but never passed through requireConfig).
func scrubSecrets(s string) string {
	literalSecretsMu.RLock()
	secrets := make([]string, len(literalSecrets))
	copy(secrets, literalSecrets)
	literalSecretsMu.RUnlock()

	for _, secret := range secrets {
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	return secretRE.ReplaceAllString(s, "[REDACTED]")
}

// requireDestructive skips unless the operator has explicitly opted in to the
// destructive tier AND the target host is an allowlisted staging host.
func requireDestructive(t *testing.T, cfg smokeConfig) {
	t.Helper()
	if os.Getenv("SMOKE_ALLOW_DESTRUCTIVE") != "1" {
		t.Skipf("skipping destructive tier: set SMOKE_ALLOW_DESTRUCTIVE=1 to enable " +
			"(this tier deletes migrated entities from the target organization)")
	}
	assertHostAllowed(t, cfg.targetURL)
}

// ---------------------------------------------------------------------------
// Locating the repo and the binary under test
// ---------------------------------------------------------------------------

// repoRoot walks up from the test's working directory until it finds the
// directory holding both Makefile and go/.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if fileExists(filepath.Join(dir, "Makefile")) && dirExists(filepath.Join(dir, "go")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate the repository root (no ancestor of %q contains both Makefile and go/)", dir)
		}
		dir = parent
	}
}

// binaryPath returns the binary the suite drives.
//
// Search order matters. `make build` emits go/sonar-migration-tool, so that
// location is preferred: a stale copy often also sits at the repository root
// (docs/REGRESSION-TESTING-PLAN.md builds one there with `-o ../`), and
// silently smoke-testing a stale binary would make this whole suite lie.
func binaryPath(t *testing.T) string {
	t.Helper()
	if p := strings.TrimSpace(os.Getenv("SMOKE_BINARY")); p != "" {
		if !fileExists(p) {
			t.Fatalf("SMOKE_BINARY=%q does not exist", p)
		}
		return assertExecutable(t, p)
	}

	root := repoRoot(t)
	candidates := []string{
		filepath.Join(root, "go", "sonar-migration-tool"), // what `make build` produces
		filepath.Join(root, "sonar-migration-tool"),       // legacy / manually built
	}
	for _, p := range candidates {
		if fileExists(p) {
			return assertExecutable(t, p)
		}
	}
	t.Fatalf("no binary found at any of %v — run `make build` first (or set SMOKE_BINARY)", candidates)
	return ""
}

// assertExecutable fails unless path is executable by the owner.
func assertExecutable(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("binary at %q is not executable", path)
	}
	return path
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// ---------------------------------------------------------------------------
// Running the CLI
// ---------------------------------------------------------------------------

// cliResult is the outcome of one CLI invocation. stdout/stderr are already
// scrubbed of credentials.
type cliResult struct {
	stdout   string
	stderr   string
	exitCode int
	duration time.Duration
}

// combined returns stdout and stderr joined, for substring assertions that
// should not care which stream carried the message.
func (r cliResult) combined() string { return r.stdout + "\n" + r.stderr }

// cliTimeout is the per-invocation ceiling, overridable via SMOKE_CLI_TIMEOUT
// (any Go duration string, e.g. "45m").
func cliTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("SMOKE_CLI_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 15 * time.Minute
}

// runCLI executes the binary with args and returns the captured result. It
// never fails the test on a non-zero exit — callers assert the code they
// expect, since several cases deliberately expect failure.
func runCLI(t *testing.T, args ...string) cliResult {
	t.Helper()
	bin := binaryPath(t)

	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = repoRoot(t)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	start := time.Now()
	err := cmd.Run()
	res := cliResult{
		stdout:   scrubSecrets(outBuf.String()),
		stderr:   scrubSecrets(errBuf.String()),
		duration: time.Since(start),
	}

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.exitCode = 0
	case errors.As(err, &exitErr):
		res.exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("running %s %v: %v", bin, scrubArgs(args), err)
	}

	if ctx.Err() != nil {
		t.Fatalf("command %v exceeded the %s timeout (raise SMOKE_CLI_TIMEOUT if this is expected)",
			scrubArgs(args), cliTimeout())
	}

	logf(t, "$ sonar-migration-tool %s\nexit=%d duration=%s\n%s\n",
		strings.Join(scrubArgs(args), " "), res.exitCode, res.duration.Round(time.Millisecond), res.combined())
	return res
}

// scrubArgs redacts anything token-shaped in an argv slice before it is
// logged. Tokens should never be on argv in the first place, but a stray
// --token from a future test must not leak into a log file.
func scrubArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = scrubSecrets(a)
	}
	return out
}

// requireExit asserts an exact exit code, printing the captured output on
// mismatch so a failure is diagnosable without re-running.
func requireExit(t *testing.T, res cliResult, want int, what string) {
	t.Helper()
	if res.exitCode != want {
		t.Fatalf("%s: got exit %d, want %d\n--- output ---\n%s", what, res.exitCode, want, res.combined())
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// smokeConfig carries only the non-secret fields the harness needs for
// guardrails and assertions. Tokens are deliberately absent: the binary
// reads them from the config file itself, so they never enter the harness,
// argv, or a log.
type smokeConfig struct {
	path          string
	sourceURL     string
	targetURL     string
	org           string
	enterpriseKey string
}

// configPath returns the config file the suite should use.
func configPath(t *testing.T) string {
	t.Helper()
	if p := strings.TrimSpace(os.Getenv("SMOKE_CONFIG")); p != "" {
		return p
	}
	return filepath.Join(repoRoot(t), "config.json")
}

// requireConfig loads the unified config. It SKIPS (never fails) when the
// file is absent, so a fresh checkout without credentials stays green.
func requireConfig(t *testing.T) smokeConfig {
	t.Helper()
	p := configPath(t)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("no config at %q — the live smoke suite needs real credentials; skipping", p)
	}

	// Minimal shape: only the fields the harness itself needs. The binary
	// tolerates four config shapes; the harness only supports the unified
	// one described by schemas/config.schema.json.
	var parsed struct {
		Source struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		} `json:"source"`
		Target struct {
			URL                 string `json:"url"`
			Token               string `json:"token"`
			EnterpriseKey       string `json:"enterprise_key"`
			DefaultOrganization string `json:"default_organization"`
		} `json:"target"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parsing %s: %v (the smoke suite requires the unified config shape — see schemas/config.schema.json)", p, err)
	}
	if parsed.Target.URL == "" {
		t.Fatalf("%s has no target.url — the smoke suite requires the unified "+
			`{"source":{...},"target":{...}} shape described by schemas/config.schema.json`, p)
	}

	// Register the real credentials for redaction, then drop them. They are
	// parsed for this purpose only — smokeConfig deliberately has no token
	// field, and tokens are never placed on argv (argv is world-readable via
	// ps); the binary reads them from the config file itself.
	registerSecret(parsed.Source.Token)
	registerSecret(parsed.Target.Token)

	// Never log the parsed struct: adjacent fields in the file hold tokens.
	return smokeConfig{
		path:          p,
		sourceURL:     parsed.Source.URL,
		targetURL:     parsed.Target.URL,
		org:           parsed.Target.DefaultOrganization,
		enterpriseKey: parsed.Target.EnterpriseKey,
	}
}

// ---------------------------------------------------------------------------
// Preflight
// ---------------------------------------------------------------------------

// preflight fails fast when an instance is unreachable, so a downstream
// assertion failure is never mistaken for a code regression. Any HTTP
// response proves reachability — including 401/403, since /api/system/status
// may require auth and the harness holds no tokens by design. Only a
// transport error, or an explicit non-UP status, is fatal.
func preflight(t *testing.T, baseURL, label string) {
	t.Helper()
	if baseURL == "" {
		t.Fatalf("%s has no URL configured", label)
	}
	statusURL := strings.TrimSuffix(baseURL, "/") + "/api/system/status"

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(statusURL)
	if err != nil {
		t.Fatalf("%s at %s is not reachable: %v — start it before running the smoke suite", label, baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		logf(t, "preflight: %s reachable (status endpoint requires auth: HTTP %d)\n", label, resp.StatusCode)
		return
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s at %s returned HTTP %d from /api/system/status", label, baseURL, resp.StatusCode)
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		// A 200 that isn't the expected JSON still proves reachability.
		logf(t, "preflight: %s returned HTTP 200 with an unexpected body\n", label)
		return
	}
	if body.Status != "" && body.Status != "UP" {
		t.Fatalf("%s at %s reports status %q, want \"UP\" — start it before running the smoke suite",
			label, baseURL, body.Status)
	}
	logf(t, "preflight: %s is UP\n", label)
}

// ---------------------------------------------------------------------------
// Artifact assertions
// ---------------------------------------------------------------------------

// runDirRE matches the run-directory naming scheme produced by
// common.GenerateRunID: YYYY-MM-DD-NNNN.
var runDirRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{4}$`)

// latestRunDir returns the newest run directory under exportDir. Mirrors
// firstExtractDir in go/internal/extract/extract_test.go.
func latestRunDir(t *testing.T, exportDir string) string {
	t.Helper()
	entries, err := os.ReadDir(exportDir)
	if err != nil {
		t.Fatalf("reading export dir %q: %v", exportDir, err)
	}
	var runs []string
	for _, e := range entries {
		if e.IsDir() && runDirRE.MatchString(e.Name()) {
			runs = append(runs, e.Name())
		}
	}
	if len(runs) == 0 {
		t.Fatalf("no run directory (YYYY-MM-DD-NNNN) found under %q", exportDir)
	}
	sort.Strings(runs) // lexicographic == chronological for this format
	return filepath.Join(exportDir, runs[len(runs)-1])
}

// assertPDF checks a file is a non-trivial PDF.
func assertPDF(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected PDF at %q: %v", path, err)
	}
	const minPDFBytes = 10 * 1024
	if info.Size() < minPDFBytes {
		t.Errorf("PDF %q is only %d bytes, want >= %d — it is probably truncated or empty",
			path, info.Size(), minPDFBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %q: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	magic := make([]byte, 5)
	if _, err := f.Read(magic); err != nil {
		t.Fatalf("reading magic bytes of %q: %v", path, err)
	}
	if string(magic) != "%PDF-" {
		t.Errorf("%q does not start with %%PDF- (got %q) — not a valid PDF", path, string(magic))
	}
}

// assertFileContains checks a text artifact exists, is non-trivial, and holds
// every required substring.
func assertFileContains(t *testing.T, path string, minBytes int64, required ...string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected file at %q: %v", path, err)
	}
	if info.Size() < minBytes {
		t.Errorf("%q is only %d bytes, want >= %d", path, info.Size(), minBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	body := string(raw)
	for _, want := range required {
		if !strings.Contains(body, want) {
			t.Errorf("%q does not contain %q", path, want)
		}
	}
}

// assertCSV reuses the production CSV loader (structure.LoadCSV) so the test
// reads these files exactly the way the tool does, then checks the file has
// rows and that every required column is present and non-empty.
func assertCSV(t *testing.T, exportDir, name string, requiredCols ...string) {
	t.Helper()
	rows, err := structure.LoadCSV(exportDir, name)
	if err != nil {
		t.Fatalf("loading %s from %q: %v", name, exportDir, err)
	}
	if len(rows) == 0 {
		t.Fatalf("%s has no rows", name)
	}
	for _, col := range requiredCols {
		filled := 0
		for _, r := range rows {
			if v, ok := r[col].(string); ok && strings.TrimSpace(v) != "" {
				filled++
			}
		}
		if filled == 0 {
			t.Errorf("%s: column %q is missing or empty in all %d rows", name, col, len(rows))
		}
	}
	logf(t, "csv: %s has %d rows\n", name, len(rows))
}

// assertCSVColumns checks a CSV exists, has at least one data row, and
// declares every named column — WITHOUT requiring the column to hold a value.
//
// organizations.csv is the motivating case. `structure` emits
// sonarcloud_org_key EMPTY whenever the config names no single target
// organization, because mapping a source organization to a Cloud
// organization is then the operator's decision. (Step B3 of
// docs/REGRESSION-TESTING-PLAN.md — "confirm sonarcloud_org_key is populated"
// — is a human review step performed AFTER editing the file, not a property of
// structure's output.) Asserting non-emptiness there fails on a correct run.
//
// A config carrying target.default_organization is the exception: since
// #566 structure pre-populates the column from it, and tier 1 asserts
// that separately.
func assertCSVColumns(t *testing.T, exportDir, name string, cols ...string) {
	t.Helper()
	rows, err := structure.LoadCSV(exportDir, name)
	if err != nil {
		t.Fatalf("loading %s from %q: %v", name, exportDir, err)
	}
	if len(rows) == 0 {
		t.Fatalf("%s has no rows", name)
	}
	for _, col := range cols {
		if _, ok := rows[0][col]; !ok {
			t.Errorf("%s does not declare the column %q", name, col)
		}
	}
	logf(t, "csv: %s has %d rows, columns present: %v\n", name, len(rows), cols)
}

// assertOrgMapping checks every organizations.csv row maps to org. Used
// for the one case where the tool fills the column itself: a config
// carrying target.default_organization (#566).
func assertOrgMapping(t *testing.T, exportDir, org string) {
	t.Helper()
	rows, err := structure.LoadCSV(exportDir, "organizations.csv")
	if err != nil {
		t.Fatalf("loading organizations.csv from %q: %v", exportDir, err)
	}
	if len(rows) == 0 {
		t.Fatal("organizations.csv has no rows")
	}
	for i, r := range rows {
		got, _ := r["sonarcloud_org_key"].(string)
		if got != org {
			t.Errorf("organizations.csv row %d: sonarcloud_org_key = %q, want %q", i, got, org)
		}
	}
	logf(t, "csv: organizations.csv maps all %d rows to %s\n", len(rows), org)
}

// setOrgMapping fills in the sonarcloud_org_key column of organizations.csv,
// simulating the manual edit an operator makes between `structure` and
// `migrate`/`reset`.
//
// This is not a convenience. `reset` builds its target organization list by
// reading this column (loadResetTargetOrgs in go/cmd/reset.go), so in an
// isolated export directory it would otherwise find no organizations and
// refuse to run.
func setOrgMapping(t *testing.T, exportDir, org string) {
	t.Helper()
	path := filepath.Join(exportDir, "organizations.csv")

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %q: %v", path, err)
	}
	records, err := csv.NewReader(f).ReadAll()
	_ = f.Close()
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	if len(records) < 2 {
		t.Fatalf("%q has a header but no data rows", path)
	}

	col := -1
	for i, h := range records[0] {
		if strings.TrimSpace(h) == "sonarcloud_org_key" {
			col = i
			break
		}
	}
	if col < 0 {
		t.Fatalf("%q has no sonarcloud_org_key column (header: %v)", path, records[0])
	}
	for r := 1; r < len(records); r++ {
		for len(records[r]) <= col {
			records[r] = append(records[r], "")
		}
		records[r][col] = org
	}

	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("rewriting %q: %v", path, err)
	}
	defer func() { _ = out.Close() }()
	w := csv.NewWriter(out)
	if err := w.WriteAll(records); err != nil {
		t.Fatalf("writing %q: %v", path, err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatalf("flushing %q: %v", path, err)
	}
	logf(t, "org mapping: set sonarcloud_org_key=%q for %d row(s)\n", org, len(records)-1)
}

// panicRE matches the silent-failure signatures the manual protocol greps for
// (docs/REGRESSION-TESTING-PLAN.md steps A4 and B7).
var panicRE = regexp.MustCompile(`(?i)\b(panic|fatal)\b`)

// assertNoPanics fails when captured output contains a panic or fatal line.
func assertNoPanics(t *testing.T, outputs ...string) {
	t.Helper()
	for _, out := range outputs {
		for _, line := range strings.Split(out, "\n") {
			if panicRE.MatchString(line) {
				t.Errorf("found a panic/fatal line in CLI output: %s", strings.TrimSpace(line))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// regtest oracle
// ---------------------------------------------------------------------------

// runRegtest runs the regtest subcommand and decodes its JSON into the
// production regtest.Report type. regtest compares the source instance
// against the target org across ~70 checks and is the suite's oracle for
// "did the migration actually work".
func runRegtest(t *testing.T, cfg smokeConfig, extraArgs ...string) regtest.Report {
	t.Helper()
	args := append([]string{"regtest", "--config", cfg.path, "--format", "json"}, extraArgs...)
	res := runCLI(t, args...)

	var report regtest.Report
	if err := json.Unmarshal([]byte(res.stdout), &report); err != nil {
		t.Fatalf("decoding regtest JSON: %v\n--- stdout ---\n%s\n--- stderr ---\n%s",
			err, res.stdout, res.stderr)
	}

	// YELLOW means every mismatch in this run is a SQS_AND_SQC_FEATURE_DIVERGENCE
	// (regtest.CheckResult.SqsAndSqcFeatureDivergence) — a known, permanent
	// SonarQube Server vs. SonarQube Cloud difference, e.g. Cloud's
	// differently-named built-in default profiles, its independently-versioned
	// rule catalog, or this tool's documented decision to force every migrated
	// project private. That is the product working as designed, not a
	// migration bug, so it must not fail the suite — but it is logged so the
	// SQS_AND_SQC_FEATURE_DIVERGENCE results stay visible. Only FAIL (a
	// mismatch that is NOT a SQS_AND_SQC_FEATURE_DIVERGENCE) fails the test.
	if report.Verdict == "FAIL" {
		var failures []string
		for _, c := range report.Results {
			if !c.Match && !c.SqsAndSqcFeatureDivergence {
				detail := fmt.Sprintf("  [%s] %s: source=%q target=%q", c.Category, c.Name, c.SQSValue, c.SCValue)
				if c.Error != "" {
					detail += " error=" + c.Error
				}
				if c.Notes != "" {
					detail += " notes=" + c.Notes
				}
				failures = append(failures, detail)
			}
		}
		t.Errorf("regtest verdict %s (%d passed, %d failed, %d errors, %d skipped, %d sqs_and_sqc_feature_divergence of %d):\n%s",
			report.Verdict, report.Passed, report.Failed, report.Errors, report.Skipped, report.SqsAndSqcFeatureDivergence,
			report.TotalChecks, strings.Join(failures, "\n"))
	} else if report.Verdict == "YELLOW" {
		var sqsAndSqcFeatureDivergences []string
		for _, c := range report.Results {
			if c.SqsAndSqcFeatureDivergence {
				sqsAndSqcFeatureDivergences = append(sqsAndSqcFeatureDivergences, fmt.Sprintf("  [%s] %s: source=%q target=%q — %s",
					c.Category, c.Name, c.SQSValue, c.SCValue, c.Notes))
			}
		}
		logf(t, "regtest: verdict=YELLOW — %d SQS_AND_SQC_FEATURE_DIVERGENCE(s), not migration bugs:\n%s\n",
			report.SqsAndSqcFeatureDivergence, strings.Join(sqsAndSqcFeatureDivergences, "\n"))
	}
	requireExit(t, res, 0, "regtest")
	logf(t, "regtest: verdict=%s passed=%d failed=%d errors=%d skipped=%d sqs_and_sqc_feature_divergence=%d\n",
		report.Verdict, report.Passed, report.Failed, report.Errors, report.Skipped, report.SqsAndSqcFeatureDivergence)
	return report
}

// ---------------------------------------------------------------------------
// Logging and the run summary
// ---------------------------------------------------------------------------

// smokeDir is the gitignored directory holding logs and the summary report.
func smokeDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(repoRoot(t), ".smoke")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("creating %q: %v", d, err)
	}
	return d
}

// logSanitizeRE strips characters that are unsafe in a filename (subtest
// names contain "/").
var logSanitizeRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// logf appends scrubbed text to this test's log under .smoke/. Logging is
// best-effort: a logging failure must never fail a smoke test.
func logf(t *testing.T, format string, args ...any) {
	t.Helper()
	name := logSanitizeRE.ReplaceAllString(t.Name(), "_")
	path := filepath.Join(smokeDir(t), name+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(scrubSecrets(fmt.Sprintf(format, args...)))
}

// outcome is one row of the end-of-run summary table.
type outcome struct {
	Tier    string
	Command string
	Status  string // PASS | FAIL | SKIP
	Detail  string
}

var (
	outcomesMu sync.Mutex
	outcomes   []outcome
)

// recordOutcome registers a row for the summary report. Call it from a
// deferred closure so the status reflects the test's final state.
func recordOutcome(tier, command, status, detail string) {
	outcomesMu.Lock()
	defer outcomesMu.Unlock()
	outcomes = append(outcomes, outcome{Tier: tier, Command: command, Status: status, Detail: detail})
}

// track records the outcome of a subtest automatically. Usage:
//
//	defer track(t, "1", "extract")()
func track(t *testing.T, tier, command string) func() {
	return func() {
		status := "PASS"
		switch {
		case t.Skipped():
			status = "SKIP"
		case t.Failed():
			status = "FAIL"
		}
		recordOutcome(tier, command, status, "")
	}
}

// The summary writer lives in zzz_summary_test.go. Go runs tests in
// source-file order, so a "sorts last" test must live in a file that sorts
// last — a ZZZ name inside this file would run BEFORE every tier test and
// observe no outcomes at all.
