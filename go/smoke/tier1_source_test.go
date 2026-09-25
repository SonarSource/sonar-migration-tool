//go:build smoke

// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package smoke

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// Tier 1 exercises the read-only, source-side half of the CLI pipeline:
// extract, structure, mappings, the migration/maturity reports, and the
// predictive report. It needs only a reachable source SonarQube Server —
// unlike Tier 2, it never migrates, creates, or resets anything on the
// SonarQube Cloud target, and predictive-report is specifically checked
// below to prove it makes zero SonarQube Cloud API calls at all.

// TestTier1_SourcePipeline drives extract -> structure -> mappings ->
// report -> predictive-report against a real source SonarQube Server,
// using ordered subtests that share a single export directory. Later
// subtests depend on artifacts produced by earlier ones; when an earlier
// subtest fails to produce what a later one needs, the later one is
// skipped with a clear reason rather than failing on a missing file.
func TestTier1_SourcePipeline(t *testing.T) {
	cfg := requireConfig(t)
	preflight(t, cfg.sourceURL, "source SonarQube Server")

	exportDir := t.TempDir()

	projectKey := strings.TrimSpace(os.Getenv("SMOKE_PROJECT_KEY"))
	if projectKey == "" {
		projectKey = discoverProjectKey(t, cfg.sourceURL)
	}
	if projectKey == "" {
		t.Skipf("no project key available: SMOKE_PROJECT_KEY is unset and auto-discovery via "+
			"%s/api/projects/search failed or found no projects (the harness holds no tokens by "+
			"design, so an unauthenticated discovery request will likely 401) — set SMOKE_PROJECT_KEY "+
			"to a real project key on the source instance to run Tier 1", cfg.sourceURL)
	}

	// extractRunID is set by the "extract" subtest and used as a guard by
	// every later subtest: if it's empty, extract did not succeed and
	// nothing downstream can work either.
	var extractRunID string

	t.Run("extract", func(t *testing.T) {
		defer track(t, "1", "extract")()

		res := runCLI(t, "extract",
			"--config", cfg.path,
			"--export_directory", exportDir,
			"--project_key", projectKey)
		requireExit(t, res, 0, "extract")
		assertNoPanics(t, res.combined())

		runDir := latestRunDir(t, exportDir)
		extractRunID = filepath.Base(runDir)

		if !fileExists(filepath.Join(runDir, "extract.json")) {
			t.Errorf("expected extract.json under %q", runDir)
		}

		foundResults := false
		walkErr := filepath.WalkDir(runDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, "results.") && strings.HasSuffix(name, ".jsonl") {
				info, infoErr := d.Info()
				if infoErr != nil {
					return infoErr
				}
				if info.Size() > 0 {
					foundResults = true
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walking run dir %q: %v", runDir, walkErr)
		}
		if !foundResults {
			t.Errorf("no non-empty results.*.jsonl file found under %q", runDir)
		}
	})

	t.Run("extract_resume", func(t *testing.T) {
		defer track(t, "1", "extract_resume")()
		if extractRunID == "" {
			t.Skip("skipping: the extract subtest did not produce a run directory")
		}

		res := runCLI(t, "extract",
			"--config", cfg.path,
			"--export_directory", exportDir,
			"--project_key", projectKey,
			"--extract_id", extractRunID)
		requireExit(t, res, 0, "extract_resume")
		logf(t, "extract_resume output:\n%s\n", res.combined())

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
		if len(runs) != 1 {
			t.Errorf("expected exactly one run directory after resuming, found %d: %v", len(runs), runs)
		}
	})

	t.Run("structure", func(t *testing.T) {
		defer track(t, "1", "structure")()
		if extractRunID == "" {
			t.Skip("skipping: the extract subtest did not produce data to structure")
		}

		res := runCLI(t, "structure", "--config", cfg.path, "--export_directory", exportDir)
		requireExit(t, res, 0, "structure")

		// structure emits sonarcloud_org_key EMPTY by design when the config
		// names no single target org — the operator maps it to a Cloud org
		// afterwards. Assert the column exists, not that it has a value, or
		// this fails on a correct run.
		assertCSVColumns(t, exportDir, "organizations.csv", "sonarcloud_org_key")
		assertCSV(t, exportDir, "projects.csv")

		// #566: the one case where the column IS pre-populated — the config
		// carries target.default_organization, so structure --config stamps
		// it on every row instead of leaving the mapping blank and having
		// every downstream report call the entities "Organization skipped".
		if cfg.org == "" {
			t.Log("config has no target.default_organization; skipping the #566 pre-population check")
			return
		}
		assertOrgMapping(t, exportDir, cfg.org)
	})

	t.Run("mappings", func(t *testing.T) {
		defer track(t, "1", "mappings")()
		if extractRunID == "" {
			t.Skip("skipping: the extract subtest did not produce data to map")
		}

		res := runCLI(t, "mappings", "--config", cfg.path, "--export_directory", exportDir)
		requireExit(t, res, 0, "mappings")

		// These CSVs can legitimately be empty on a minimal instance (no
		// permission templates, no portfolios, ...), so they only get a
		// tolerant existence + row-count check rather than assertCSV's
		// hard failure on zero rows.
		for _, name := range []string{"templates.csv", "profiles.csv", "gates.csv", "portfolios.csv", "groups.csv"} {
			path := filepath.Join(exportDir, name)
			if !fileExists(path) {
				t.Errorf("expected %s to exist under %q", name, exportDir)
				continue
			}
			rows, err := structure.LoadCSV(exportDir, name)
			if err != nil {
				t.Errorf("loading %s: %v", name, err)
				continue
			}
			logf(t, "csv: %s has %d rows (may legitimately be empty)\n", name, len(rows))
		}
	})

	t.Run("report_migration", func(t *testing.T) {
		defer track(t, "1", "report_migration")()
		if extractRunID == "" {
			t.Skip("skipping: no extract data to report on")
		}

		res := runCLI(t, "report", "--report_type", "migration", "--export_directory", exportDir)
		requireExit(t, res, 0, "report_migration")
		assertFileContains(t, filepath.Join(exportDir, "migration.md"), 1024)
	})

	t.Run("report_maturity", func(t *testing.T) {
		defer track(t, "1", "report_maturity")()
		if extractRunID == "" {
			t.Skip("skipping: no extract data to report on")
		}

		res := runCLI(t, "report", "--report_type", "maturity", "--export_directory", exportDir)
		requireExit(t, res, 0, "report_maturity")
		assertFileContains(t, filepath.Join(exportDir, "maturity.md"), 1024)
	})

	t.Run("report_invalid_type", func(t *testing.T) {
		defer track(t, "1", "report_invalid_type")()
		if extractRunID == "" {
			t.Skip("skipping: no extract data on disk, so report_type is never reached")
		}

		res := runCLI(t, "report", "--report_type", "bogus", "--export_directory", exportDir)
		requireExit(t, res, 1, "report_invalid_type")

		const want = "unsupported report type: bogus (available: migration, maturity)"
		if !strings.Contains(res.combined(), want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, res.combined())
		}
	})

	t.Run("predictive_report", func(t *testing.T) {
		defer track(t, "1", "predictive_report")()
		if extractRunID == "" {
			t.Skip("skipping: no extract data to predict from")
		}

		res := runCLI(t, "predictive-report", "--config", cfg.path, "--export_directory", exportDir)
		requireExit(t, res, 0, "predictive_report")
		assertPDF(t, filepath.Join(exportDir, "predictive_migration_summary.pdf"))

		// #566: predictive-report applies the same default organization
		// migrate would, so the mapping is filled in rather than read as a
		// deliberate org skip. Still no Cloud call — asserted below.
		if cfg.org != "" {
			assertOrgMapping(t, exportDir, cfg.org)
		}
	})

	t.Run("predictive_report_makes_no_cloud_calls", func(t *testing.T) {
		defer track(t, "1", "predictive_report_makes_no_cloud_calls")()
		if extractRunID == "" {
			t.Skip("skipping: no extract data to predict from")
		}

		// Any request landing on this server proves predictive-report
		// broke its documented "no SonarQube Cloud API calls" contract.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("predictive-report made a Cloud API call: %s %s", r.Method, r.URL.Path)
			_, _ = w.Write([]byte("{}"))
		}))
		defer srv.Close()

		raw, err := os.ReadFile(cfg.path)
		if err != nil {
			t.Fatalf("reading config %q: %v", cfg.path, err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("parsing config %q: %v", cfg.path, err)
		}
		target, ok := parsed["target"].(map[string]any)
		if !ok {
			t.Fatalf("config %q has no \"target\" object", cfg.path)
		}
		target["url"] = srv.URL
		parsed["target"] = target

		patched, err := json.Marshal(parsed)
		if err != nil {
			t.Fatalf("marshaling patched config: %v", err)
		}

		// The patched config still carries the real source/target tokens
		// verbatim, so it must live only inside t.TempDir() (auto-cleaned
		// at the end of the test) and its path/content must never be
		// logged.
		noCloudConfigPath := filepath.Join(t.TempDir(), "no-cloud-config.json")
		if err := os.WriteFile(noCloudConfigPath, patched, 0o600); err != nil {
			t.Fatalf("writing patched config: %v", err)
		}

		// predictive-report reads extract data + mapping CSVs from disk;
		// give it an isolated copy of exportDir so this run neither
		// shares nor mutates the state the other subtests rely on.
		freshExportDir := copyExportDir(t, exportDir)

		res := runCLI(t, "predictive-report", "--config", noCloudConfigPath, "--export_directory", freshExportDir)
		requireExit(t, res, 0, "predictive_report_makes_no_cloud_calls")
	})
}

// discoverProjectKey attempts to find a usable project key on the source
// instance via an unauthenticated /api/projects/search call. The harness
// holds no tokens by design (see requireConfig), so on most real instances
// this returns "" and the caller should skip with instructions to set
// SMOKE_PROJECT_KEY instead.
func discoverProjectKey(t *testing.T, sourceURL string) string {
	t.Helper()
	if sourceURL == "" {
		return ""
	}
	searchURL := strings.TrimSuffix(sourceURL, "/") + "/api/projects/search?ps=1"

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(searchURL)
	if err != nil {
		logf(t, "project discovery: GET %s: %v\n", searchURL, err)
		return ""
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		logf(t, "project discovery: GET %s returned HTTP %d (likely unauthenticated; the harness holds no tokens)\n",
			searchURL, resp.StatusCode)
		return ""
	}

	var body struct {
		Components []struct {
			Key string `json:"key"`
		} `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		logf(t, "project discovery: decoding response from %s: %v\n", searchURL, err)
		return ""
	}
	if len(body.Components) == 0 {
		logf(t, "project discovery: %s returned no projects\n", searchURL)
		return ""
	}
	return body.Components[0].Key
}

// copyExportDir makes an isolated copy of src's contents under a new
// t.TempDir() and returns its path.
func copyExportDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying export dir %q to %q: %v", src, dst, err)
	}
	return dst
}

// TestTier1_InsecureSkipsCertVerification covers the --insecure flag (#586):
// a locally-hosted SonarQube Server whose TLS certificate is not signed by a
// trusted CA. httptest.NewTLSServer reproduces that exactly — it presents a
// certificate from its own throwaway CA, which is in no system trust store.
//
// Unlike the rest of Tier 1 this test needs no real source server and no
// credentials: the fake server below answers the two endpoints extract calls
// before anything else (version, then edition), which is all that is needed
// to prove whether the TLS handshake succeeded.
//
// Both directions are asserted. The "rejected by default" half is the one
// that matters most: it proves --insecure cannot silently become the default
// and quietly disable certificate checking for every user.
func TestTier1_InsecureSkipsCertVerification(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/server/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("2025.1.0.0"))
	})
	mux.HandleFunc("GET /api/system/info", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"edition": "developer"})
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	// The token is a throwaway string, never a real credential: the fake
	// server above accepts any Authorization header.
	configPath := filepath.Join(t.TempDir(), "insecure-config.json")
	body := `{"source": {"url": "` + srv.URL + `", "token": "smoke-not-a-real-token"},
	          "target": {"url": "https://example.invalid", "token": "smoke-not-a-real-token",
	                     "default_organization": "smoke-org"}}`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	// versionStepFailure is how extract reports a source connection it could
	// not complete — initClient wraps detectVersion's error with it. Its
	// presence or absence is the signal this test reads.
	const versionStepFailure = "detecting server version"

	// insecureWarningText is the operator-facing warning --insecure emits.
	// It is matched on its own, and stripped before scanning for
	// certificate errors, because the warning legitimately contains the
	// word "certificate" and would otherwise match as a failure.
	const insecureWarningText = "TLS certificate verification is disabled"

	t.Run("rejected_by_default", func(t *testing.T) {
		defer track(t, "1", "insecure_rejected_by_default")()

		res := runCLI(t, "extract",
			"--config", configPath,
			"--export_directory", t.TempDir())
		if res.exitCode == 0 {
			t.Fatalf("extract against an untrusted certificate must fail without --insecure, got exit 0\n%s", res.combined())
		}
		out := res.combined()
		if !strings.Contains(out, versionStepFailure) {
			t.Errorf("expected the failure to come from the source connection (%q), got:\n%s", versionStepFailure, out)
		}
		// Either spelling is the stdlib's; assert on both rather than
		// pinning one exact Go error string.
		if !strings.Contains(out, "certificate") && !strings.Contains(out, "x509") {
			t.Errorf("expected a certificate-verification error, got:\n%s", out)
		}
	})

	t.Run("accepted_with_insecure", func(t *testing.T) {
		defer track(t, "1", "insecure_accepted_with_flag")()

		res := runCLI(t, "extract",
			"--config", configPath,
			"--export_directory", t.TempDir(),
			"--insecure")
		out := res.combined()
		// The run is not expected to complete: the fake server serves only
		// the first two endpoints. What must be true is that the source
		// connection itself is no longer the thing that fails.
		if strings.Contains(out, versionStepFailure) {
			t.Errorf("--insecure must let the source connection through, but it still failed at %q:\n%s", versionStepFailure, out)
		}
		if errs := withoutInsecureWarning(out, insecureWarningText); strings.Contains(errs, "certificate") || strings.Contains(errs, "x509") {
			t.Errorf("--insecure must suppress certificate verification, got:\n%s", out)
		}
		// The warning is the operator's only signal that verification is
		// off, so it is part of the contract, not incidental logging.
		if !strings.Contains(out, insecureWarningText) {
			t.Errorf("expected a warning that verification is disabled, got:\n%s", out)
		}
	})
}

// withoutInsecureWarning drops the lines carrying --insecure's own warning,
// so a scan for certificate errors is not satisfied by the warning text
// itself.
func withoutInsecureWarning(out, warning string) string {
	lines := strings.Split(out, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.Contains(line, warning) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
