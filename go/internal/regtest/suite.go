package regtest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
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

	return &Suite{
		cfg:    cfg,
		sqsRaw: common.NewRawClient(sqsClient.HTTPClient(), sqsClient.BaseURL()),
		scRaw:  common.NewRawClient(scClient.HTTPClient(), scClient.BaseURL()),
		logger: logger,
	}, nil
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

// LoadConfigFile reads a migration config.json and extracts the fields
// needed for regression testing.
func LoadConfigFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config: %w", err)
	}

	var raw struct {
		SonarQube struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		} `json:"sonarqube"`
		SonarCloud struct {
			Organizations []struct {
				URL   string `json:"url"`
				Token string `json:"token"`
				Key   string `json:"key"`
			} `json:"organizations"`
		} `json:"sonarcloud"`
		Settings struct {
			ExportDirectory string   `json:"export_directory"`
			ProjectKeys     []string `json:"project_keys"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parsing config: %w", err)
	}

	cfg := Config{
		SQSURL:      raw.SonarQube.URL,
		SQSToken:    raw.SonarQube.Token,
		ExportDir:   raw.Settings.ExportDirectory,
		ProjectKeys: raw.Settings.ProjectKeys,
	}
	if len(raw.SonarCloud.Organizations) > 0 {
		org := raw.SonarCloud.Organizations[0]
		cfg.SCURL = org.URL
		cfg.SCToken = org.Token
		cfg.SCOrg = org.Key
	}
	return cfg, nil
}
