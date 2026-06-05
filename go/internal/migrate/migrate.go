// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/sonar-solutions/sonar-migration-tool/internal/version"
	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/cloud"
	"golang.org/x/sync/errgroup"
)

// MigrateConfig holds all parameters for a migrate run.
type MigrateConfig struct {
	Token           string
	EnterpriseKey   string
	Edition         string // "enterprise", "developer", etc.
	URL             string // Cloud URL (default: https://sonarcloud.io/)
	RunID           string // Resume a prior run
	Concurrency     int
	ExportDirectory string
	TargetTask      string

	// TargetTasks, when non-empty, is an explicit list of leaf tasks to run;
	// their dependencies are resolved automatically. Used by the transfer
	// command for project-scoped migration. Takes precedence over TargetTask.
	TargetTasks []string

	SkipProfiles             bool
	IncludeScanHistory       bool
	SkipIssueSync            bool // Skip the final issue / hotspot metadata sync (#299).
	SkipProjectDataMigration bool // Skip importScanHistory + the trailing sync tasks (#303).
	Debug                    bool // Enable slog.LevelDebug + verbose request payload logs

	// DefaultOrganization, when set, is used as the SonarCloud org for
	// every row in organizations.csv if none have a sonarcloud_org_key.
	// If at least one mapping is defined, this is ignored with a Warn
	// log. Issue #281.
	DefaultOrganization string

	// ExcludeBranches holds glob patterns for non-main branches to skip
	// during scan history import. Main branch is never excluded.
	ExcludeBranches []string
}

// Executor is the runtime context passed to every migrate task function.
type Executor struct {
	Cloud           *cloud.Client     // Standard Cloud API (sonarcloud.io)
	CloudAPI        *cloud.Client     // Enterprise API (api.sonarcloud.io)
	Raw             *common.RawClient // For reading from Cloud standard API
	RawAPI          *common.RawClient // For reading from Cloud enterprise API
	Extract         *common.DataStore // Reads extract data (across all extract runs)
	Store           *common.DataStore // Writes migrate output to run directory
	CloudURL        string            // e.g. "https://sonarcloud.io/"
	APIURL          string            // e.g. "https://api.sonarcloud.io/"
	EntKey          string            // Enterprise key
	Edition         common.Edition
	ExportDir       string // Root export directory
	Mapping         structure.ExtractMapping
	Sem             chan struct{}
	Logger          *slog.Logger
	ExcludeBranches []string
}

// RunMigrate is the main entry point for the migrate command.
// Returns the run ID on success.
func RunMigrate(ctx context.Context, cfg MigrateConfig) (string, error) {
	cfg.applyDefaults()

	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// Validate the SQC org mapping and apply --default_organization
	// fallback if requested (issues #279 + #281). Done before any API
	// client setup so the failure or warning surfaces immediately.
	appliedDefault, err := applyOrgMapping(cfg.ExportDirectory, cfg.DefaultOrganization, logger)
	if err != nil {
		return "", err
	}

	cloudURL := cfg.URL
	apiURL := strings.Replace(cloudURL, "https://", "https://api.", 1)

	// Create Cloud clients with retry logging — and, when --debug is set,
	// full HTTP request/response logging.
	retryLog := func(method, url string, status, attempt, total int) {
		logger.Warn("retrying request",
			"method", method, "endpoint", url,
			"status", status, "attempt", attempt, "maxAttempts", total)
	}
	clientOpts := []sqapi.Option{sqapi.WithRetryLogger(retryLog)}
	if cfg.Debug {
		clientOpts = append(clientOpts, sqapi.WithDebugLogger(common.NewHTTPDebugLogger(logger)))
	}
	cloudClient := sqapi.NewCloudClient(cloudURL, cfg.Token, clientOpts...)
	apiClient := sqapi.NewCloudClient(apiURL, cfg.Token, clientOpts...)
	cc := cloud.New(cloudClient)
	apiCC := cloud.New(apiClient)

	// Verify every SQC organization the migration will touch exists and
	// is visible to the token (issue #283). Done early so a typo aborts
	// the run before any extract data is touched.
	if err := validateOrgsExist(ctx, cc.Organizations, cfg.ExportDirectory, cfg.EnterpriseKey, cfg.DefaultOrganization, appliedDefault); err != nil {
		return "", err
	}

	// Create RawClients for read operations.
	raw := common.NewRawClient(cloudClient.HTTPClient(), cloudClient.BaseURL())
	rawAPI := common.NewRawClient(apiClient.HTTPClient(), apiClient.BaseURL())

	// Resolve extract mapping.
	mapping, err := structure.GetUniqueExtracts(cfg.ExportDirectory)
	if err != nil {
		return "", fmt.Errorf("scanning extracts: %w", err)
	}

	// Determine edition.
	edition := common.Edition(cfg.Edition)

	// Generate or resume run ID.
	runID := cfg.RunID
	createPlan := runID == ""
	if createPlan {
		runID = generateRunID(cfg.ExportDirectory)
	}
	runDir := filepath.Join(cfg.ExportDirectory, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return "", fmt.Errorf("creating run dir: %w", err)
	}

	// Build task registry and plan.
	allDefs := RegisterAll()
	registry := BuildMigrateRegistry(allDefs)
	registry = FilterByEdition(registry, edition)

	// Project-data migration covers importScanHistory + the trailing
	// issue/hotspot sync pair. Skipping it necessarily skips the
	// sync as well — propagate the flag so the existing SkipIssueSync
	// logging surfaces both halves of the decision. #303.
	if cfg.SkipProjectDataMigration {
		cfg.SkipIssueSync = true
		logger.Info("project data migration disabled: skipping importScanHistory")
		logger.Info("project data migration disabled: issue + hotspot sync also skipped")
	}

	// Announce the skipped sync tasks explicitly so an operator who
	// passed --skip_issue_sync (or set skip_issue_sync: true in the
	// config) sees them named in the log alongside the rest of the
	// plan. The gating itself happens inside MigrateTargetTasks. #299.
	if cfg.SkipIssueSync {
		logger.Info("issue-sync disabled: skipping syncIssueMetadata")
		logger.Info("issue-sync disabled: skipping syncHotspotMetadata")
	}

	targets := MigrateTargetTasks(registry, cfg.TargetTask, cfg.SkipProfiles, cfg.IncludeScanHistory, cfg.SkipIssueSync, cfg.SkipProjectDataMigration, cfg.TargetTasks)
	taskSet := ResolveDependencies(targets, registry)
	if taskSet == nil {
		return "", fmt.Errorf("cannot resolve dependencies for target tasks")
	}

	plan, err := PlanPhases(taskSet, registry)
	if err != nil {
		return "", err
	}

	// Write or load plan metadata.
	if createPlan {
		if err := writeMigrateMeta(runDir, plan, runID, edition, cloudURL, targets, registry); err != nil {
			return "", err
		}
	}

	// Filter completed tasks for resumability.
	store := common.NewDataStore(runDir)
	plan = filterCompleted(plan, store)

	executor := &Executor{
		Cloud:           cc,
		CloudAPI:        apiCC,
		Raw:             raw,
		RawAPI:          rawAPI,
		Extract:         nil, // Will be set per-task based on extract mapping
		Store:           store,
		CloudURL:        cloudClient.BaseURL(),
		APIURL:          apiClient.BaseURL(),
		EntKey:          cfg.EnterpriseKey,
		Edition:         edition,
		ExportDir:       cfg.ExportDirectory,
		Mapping:         mapping,
		Sem:             make(chan struct{}, cfg.Concurrency),
		ExcludeBranches: cfg.ExcludeBranches,
		Logger:          logger,
	}

	// Execute phases.
	for i, phase := range plan {
		logger.Info("starting phase", "phase", i+1, "tasks", len(phase))
		if err := runPhase(ctx, executor, phase, registry); err != nil {
			return "", fmt.Errorf("phase %d: %w", i+1, err)
		}
		for _, taskName := range phase {
			store.MarkComplete(taskName)
		}
	}

	fmt.Printf("%s v%s - Migration Complete: %s\n", version.ToolName, version.Version, runID)
	return runID, nil
}

func runPhase(ctx context.Context, e *Executor, taskNames []string, registry map[string]*TaskDef) error {
	g, ctx := errgroup.WithContext(ctx)
	for _, name := range taskNames {
		def := registry[name]
		e.Logger.Info("running task", "task", name)
		g.Go(func() error {
			if err := def.Run(ctx, e); err != nil {
				e.Logger.Error("task failed", "task", name, "err", err)
				return fmt.Errorf("task %s: %w", name, err)
			}
			return nil
		})
	}
	return g.Wait()
}

func (cfg *MigrateConfig) applyDefaults() {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 25
	}
	if cfg.ExportDirectory == "" {
		cfg.ExportDirectory = "/app/files/"
	}
	if cfg.URL == "" {
		cfg.URL = "https://sonarcloud.io/"
	}
	if cfg.Edition == "" {
		cfg.Edition = "enterprise"
	}
	// Ensure trailing slash.
	if cfg.URL != "" && cfg.URL[len(cfg.URL)-1] != '/' {
		cfg.URL += "/"
	}
}

// generateRunID returns an ISO-date-prefixed run ID (issue #108).
// Format: "YYYY-MM-DD-NN" where NN is the next sequence number for
// the current day in the given directory. The ISO date keeps the
// IDs internationally readable and sorts chronologically when run
// directories are listed alphabetically. Older MM-DD-YYYY-NN dirs
// from previous releases are ignored by the prefix scan (their
// names don't start with today's ISO date) — they don't collide,
// just no longer participate in the count.
func generateRunID(directory string) string {
	today := time.Now().UTC().Format("2006-01-02")
	entries, _ := os.ReadDir(directory)
	count := 0
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) > len(today) && e.Name()[:len(today)] == today {
			count++
		}
	}
	return fmt.Sprintf("%s-%02d", today, count+1)
}

func writeMigrateMeta(dir string, plan [][]string, runID string, edition common.Edition, url string, targets []string, registry map[string]*TaskDef) error {
	configs := make([]string, 0, len(registry))
	for name := range registry {
		configs = append(configs, name)
	}
	meta := map[string]any{
		"plan":              plan,
		"version":           "cloud",
		"edition":           string(edition),
		"url":               url,
		"target_tasks":      targets,
		"available_configs": configs,
		"run_id":            runID,
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "plan.json"), data, 0o644)
}

func filterCompleted(plan [][]string, store *common.DataStore) [][]string {
	var filtered [][]string
	for _, phase := range plan {
		var fp []string
		for _, task := range phase {
			if !store.TaskDirExists(task) {
				fp = append(fp, task)
			}
		}
		if len(fp) > 0 {
			filtered = append(filtered, fp)
		}
	}
	return filtered
}
