// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package regtest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	sqapi "github.com/sonar-solutions/sq-api-go"
)

// Config holds the configuration for a regression test run.
type Config struct {
	SQSURL      string   // SonarQube Server URL
	SQSToken    string   // SonarQube Server token
	SCURL       string   // SonarCloud URL
	SCToken     string   // SonarCloud token
	SCOrg       string   // SonarCloud organization key
	ExportDir   string   // Export directory containing NDJSON files
	ProjectKeys []string // Specific project keys to verify (empty = all)
	Concurrency int      // Max parallel checks (default 20)
	Verbose     bool     // Print detailed output
	Format      string   // Output format: "table", "json", "markdown"
}

// CheckResult represents the result of a single verification check.
type CheckResult struct {
	ID        int    `json:"id"`
	Category  string `json:"category"`
	Name      string `json:"name"`
	SQSValue  string `json:"sqs_value"`
	SCValue   string `json:"sc_value"`
	Match     bool   `json:"match"`
	Tolerance string `json:"tolerance,omitempty"`
	Notes     string `json:"notes,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Report is the full output of a regression test run.
type Report struct {
	Timestamp    time.Time     `json:"timestamp"`
	SQSURL       string        `json:"sqs_url"`
	SCURL        string        `json:"sc_url"`
	SCOrg        string        `json:"sc_org"`
	TotalChecks  int           `json:"total_checks"`
	Passed       int           `json:"passed"`
	Failed       int           `json:"failed"`
	Errors       int           `json:"errors"`
	Skipped      int           `json:"skipped"`
	Results      []CheckResult `json:"results"`
	Duration     time.Duration `json:"duration"`
	Verdict      string        `json:"verdict"` // "PASS" or "FAIL"
}

// Suite runs all regression checks against SQS and SC.
type Suite struct {
	cfg     Config
	sqsRaw  *common.RawClient
	scRaw   *common.RawClient
	logger  *slog.Logger
	mu      sync.Mutex
	results []CheckResult
	nextID  int
	// cloudKeyByProject maps a source project key to the SonarCloud key it
	// was actually migrated to, read from the latest run's createProjects
	// output (#138 — target.project_key_pattern can differ from the
	// default "<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>" convention, so
	// scProjectKey must consult the real mapping rather than assume it).
	// Nil (or missing a given key) falls back to that default convention.
	cloudKeyByProject map[string]string
}

// checkFn is the signature for a single check function.
type checkFn struct {
	Category string
	Name     string
	Fn       func(ctx context.Context, s *Suite) []CheckResult
}

// NewSuite creates a new regression test suite from config.
func NewSuite(cfg Config) (*Suite, error) {
	cfg.applyDefaults()

	sqsClient := sqapi.NewServerClient(cfg.SQSURL, cfg.SQSToken, 10.0)
	scClient := sqapi.NewCloudClient(cfg.SCURL, cfg.SCToken)

	level := slog.LevelInfo
	if cfg.Verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cloudKeyByProject, err := loadCloudProjectKeyMap(cfg.ExportDir)
	if err != nil {
		logger.Warn("could not read the createProjects mapping from the latest run — "+
			"falling back to the default <org>_<key> convention, which is wrong if "+
			"target.project_key_pattern is customized (#138)",
			"export_dir", cfg.ExportDir, "err", err)
	}

	return &Suite{
		cfg:               cfg,
		sqsRaw:            common.NewRawClient(sqsClient.HTTPClient(), sqsClient.BaseURL()),
		scRaw:             common.NewRawClient(scClient.HTTPClient(), scClient.BaseURL()),
		logger:            logger,
		cloudKeyByProject: cloudKeyByProject,
	}, nil
}

// runIDPattern matches both the historical MM-DD-YYYY-NN and the current
// ISO YYYY-MM-DD-NN (#108) run directory naming conventions.
var runIDPattern = regexp.MustCompile(`^(\d{2}-\d{2}-\d{4}|\d{4}-\d{2}-\d{2})-\d+$`)

// latestRunDir returns the most recently modified run directory directly
// under exportDir, or "" if none exist.
func latestRunDir(exportDir string) (string, error) {
	entries, err := os.ReadDir(exportDir)
	if err != nil {
		return "", fmt.Errorf("reading export dir %s: %w", exportDir, err)
	}
	var latest string
	var latestMod time.Time
	for _, e := range entries {
		if !e.IsDir() || !runIDPattern.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if latest == "" || info.ModTime().After(latestMod) {
			latest = e.Name()
			latestMod = info.ModTime()
		}
	}
	return latest, nil
}

// loadCloudProjectKeyMap reads the createProjects task output from the
// latest run under exportDir and returns a source-key -> cloud-project-key
// map. Returns a nil map (not an error) when no run data exists yet, so
// callers can fall back gracefully — e.g. hand-built Suites in tests, or a
// config file pointed at an export directory that hasn't been migrated
// yet.
func loadCloudProjectKeyMap(exportDir string) (map[string]string, error) {
	runID, err := latestRunDir(exportDir)
	if err != nil || runID == "" {
		return nil, err
	}
	store := common.NewDataStore(filepath.Join(exportDir, runID))
	items, err := store.ReadAll("createProjects")
	if err != nil || len(items) == 0 {
		return nil, err
	}
	m := make(map[string]string, len(items))
	for _, raw := range items {
		key := common.ExtractField(raw, "key")
		cloudKey := common.ExtractField(raw, "cloud_project_key")
		if key != "" && cloudKey != "" {
			m[key] = cloudKey
		}
	}
	return m, nil
}

// Run executes all regression checks and returns a report.
func (s *Suite) Run(ctx context.Context) (*Report, error) {
	start := time.Now()
	checks := allChecks()

	s.logger.Info("starting regression test suite",
		"checks", len(checks), "concurrency", s.cfg.Concurrency)

	sem := make(chan struct{}, s.cfg.Concurrency)
	var wg sync.WaitGroup

	for _, check := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			s.logger.Debug("running check", "category", check.Category, "name", check.Name)
			results := check.Fn(ctx, s)
			s.addResults(results)
		}()
	}

	wg.Wait()

	report := s.buildReport(start)
	return report, nil
}

func (s *Suite) addResults(results []CheckResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range results {
		s.nextID++
		results[i].ID = s.nextID
	}
	s.results = append(s.results, results...)
}

func (s *Suite) buildReport(start time.Time) *Report {
	s.mu.Lock()
	defer s.mu.Unlock()

	sort.Slice(s.results, func(i, j int) bool {
		if s.results[i].Category != s.results[j].Category {
			return s.results[i].Category < s.results[j].Category
		}
		return s.results[i].Name < s.results[j].Name
	})

	// Re-number after sorting
	for i := range s.results {
		s.results[i].ID = i + 1
	}

	var passed, failed, errors, skipped int
	for _, r := range s.results {
		switch {
		case r.Error != "":
			errors++
		case r.Notes == "SKIPPED":
			skipped++
		case r.Match:
			passed++
		default:
			failed++
		}
	}

	verdict := "PASS"
	if failed > 0 || errors > 0 {
		verdict = "FAIL"
	}

	return &Report{
		Timestamp:   start,
		SQSURL:      s.cfg.SQSURL,
		SCURL:       s.cfg.SCURL,
		SCOrg:       s.cfg.SCOrg,
		TotalChecks: len(s.results),
		Passed:      passed,
		Failed:      failed,
		Errors:      errors,
		Skipped:     skipped,
		Results:     s.results,
		Duration:    time.Since(start),
		Verdict:     verdict,
	}
}

// getProjects returns the list of project keys to verify. If cfg.ProjectKeys
// is empty, it queries SQS for all projects, paginating transparently so
// instances with more than 500 projects are not silently truncated.
func (s *Suite) getProjects(ctx context.Context) ([]string, error) {
	if len(s.cfg.ProjectKeys) > 0 {
		return s.cfg.ProjectKeys, nil
	}
	items, err := s.sqsRaw.GetPaginated(ctx, common.PaginatedOpts{
		Path:      "api/projects/search",
		ResultKey: "components",
		TotalKey:  "paging.total",
	})
	if err != nil {
		return nil, fmt.Errorf("listing SQS projects: %w", err)
	}
	keys := make([]string, 0, len(items))
	for _, raw := range items {
		var c struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			// A malformed component means a project will be silently absent
			// from the regression list, so the suite would appear to pass
			// checks for a project it never examined. Warn loudly so the
			// caller knows the project list may be incomplete.
			s.logger.Warn("skipping SQS project with unparseable payload",
				"err", err, "payload", string(raw))
			continue
		}
		if c.Key != "" {
			keys = append(keys, c.Key)
		}
	}
	return keys, nil
}

// scProjectKey returns the SonarCloud project key for a given SQS project key.
func (s *Suite) scProjectKey(sqsKey string) string {
	if k, ok := s.cloudKeyByProject[sqsKey]; ok && k != "" {
		return k
	}
	return s.cfg.SCOrg + "_" + sqsKey
}

func (cfg *Config) applyDefaults() {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 20
	}
	if cfg.Format == "" {
		cfg.Format = "table"
	}
	if cfg.ExportDir == "" {
		cfg.ExportDir = "./migration-files"
	}
}

// LoadConfigFile reads a migration config.json — in any of the shapes
// extract/migrate/transfer accept (#266) — and extracts the fields needed
// for regression testing. It delegates to those packages' own config
// loaders rather than maintaining a third, independently-drifting parser.
func LoadConfigFile(path string) (Config, error) {
	sourceCfg, err := extract.LoadExtractConfigFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("parsing source config: %w", err)
	}
	targetCfg, err := migrate.LoadMigrateConfigFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("parsing target config: %w", err)
	}

	exportDir := targetCfg.ExportDirectory
	if exportDir == "" {
		exportDir = sourceCfg.ExportDirectory
	}
	if exportDir == "" {
		exportDir = "./migration-files"
	}

	scOrg, err := resolveSCOrg(targetCfg.DefaultOrganization, exportDir)
	if err != nil {
		return Config{}, err
	}

	return Config{
		SQSURL:      sourceCfg.URL,
		SQSToken:    sourceCfg.Token,
		SCURL:       targetCfg.URL,
		SCToken:     targetCfg.Token,
		SCOrg:       scOrg,
		ExportDir:   exportDir,
		ProjectKeys: sourceCfg.ProjectKeys,
	}, nil
}

// resolveSCOrg picks the single SonarCloud organization to verify against.
// regtest, like the tool's default-org path (#281), verifies one org per
// run: target.default_organization when set, otherwise the sole mapping
// found in exportDir/organizations.csv (mirrors loadResetTargetOrgs in
// go/cmd/reset.go). Multiple distinct mappings can't be resolved
// automatically since scProjectKey assumes one org prefix for the whole
// run.
func resolveSCOrg(defaultOrg, exportDir string) (string, error) {
	if defaultOrg != "" {
		return defaultOrg, nil
	}
	rows, err := structure.LoadCSV(exportDir, "organizations.csv")
	if err != nil {
		return "", fmt.Errorf("loading organizations.csv from %s: %w", exportDir, err)
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
	orgs := make([]string, 0, len(seen))
	for k := range seen {
		orgs = append(orgs, k)
	}
	sort.Strings(orgs)
	switch len(orgs) {
	case 0:
		return "", fmt.Errorf("no SonarCloud organization found: set target.default_organization in the config, or run extract/structure/migrate first so %s/organizations.csv is populated", exportDir)
	case 1:
		return orgs[0], nil
	default:
		return "", fmt.Errorf("multiple SonarCloud organizations found in %s/organizations.csv (%s) — regtest verifies one org per run; set target.default_organization to pick one", exportDir, strings.Join(orgs, ", "))
	}
}
