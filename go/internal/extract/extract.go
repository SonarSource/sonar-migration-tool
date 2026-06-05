// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	smtver "github.com/sonar-solutions/sonar-migration-tool/internal/version"
	sqapi "github.com/sonar-solutions/sq-api-go"
	"golang.org/x/sync/errgroup"
)

// ExtractConfig holds all parameters for an extract run.
type ExtractConfig struct {
	URL             string
	Token           string
	ExportDirectory string
	ExtractType     string // "all" or report type
	PEMFilePath     string
	KeyFilePath     string
	CertPassword    string
	Concurrency     int
	Timeout         int
	ExtractID       string
	TargetTask               string
	IncludeScanHistory       bool
	SkipProjectDataMigration bool // #303. Set true to skip project-data tasks (issues, source, SCM blame).
	Debug                    bool // Enable HTTP request/response logging via SDK debug transport
	// ProjectKeys, when non-empty, limits extraction to these project keys.
	// The /api/projects/search endpoint filters server-side, so only the
	// requested projects are fetched and all downstream per-project tasks
	// naturally scope to the same set.
	ProjectKeys []string
}

// Executor is the runtime context passed to every task function.
type Executor struct {
	Raw         *RawClient
	Store       *DataStore
	ServerURL   string
	Edition     Edition
	Version     common.Version
	Sem         chan struct{}
	Logger      *slog.Logger
	ProjectKeys []string // non-empty → limit extraction to these project keys

	mu              sync.Mutex
	skippedProjects map[string]bool
}

// RecordSkipped marks a project as skipped due to insufficient privileges.
func (e *Executor) RecordSkipped(projectKey string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.skippedProjects == nil {
		e.skippedProjects = make(map[string]bool)
	}
	e.skippedProjects[projectKey] = true
}

// IsSkipped returns true if the project has been marked as skipped.
func (e *Executor) IsSkipped(projectKey string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.skippedProjects[projectKey]
}

// SkippedProjectKeys returns a sorted list of project keys that were skipped.
func (e *Executor) SkippedProjectKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	keys := make([]string, 0, len(e.skippedProjects))
	for k := range e.skippedProjects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RunExtract is the main entry point for the extract command.
// It returns the list of project keys that were skipped due to insufficient privileges.
func RunExtract(ctx context.Context, cfg ExtractConfig) ([]string, error) {
	cfg.applyDefaults()

	client, raw, version, edition, err := initClient(ctx, cfg)
	if err != nil {
		return nil, err
	}

	extractID, extractDir, err := prepareExtractDir(cfg)
	if err != nil {
		return nil, err
	}

	registry, plan, targets, err := buildPlan(cfg, edition)
	if err != nil {
		return nil, err
	}

	meta := extractMeta{
		Plan: plan, RunID: extractID, Version: version,
		Edition: edition, URL: cfg.URL, Targets: targets, Registry: registry,
	}
	if err := writeMetadataFile(extractDir, meta); err != nil {
		return nil, err
	}

	store := NewDataStore(extractDir)
	plan = filterCompleted(plan, store)

	executor := newExecutor(raw, store, client.BaseURL(), edition, version, cfg.Concurrency)
	executor.ProjectKeys = cfg.ProjectKeys
	if err := executePhases(ctx, executor, plan, registry, store); err != nil {
		return nil, err
	}

	fmt.Printf("%s v%s - Extract Complete: %s\n", smtver.ToolName, smtver.Version, extractID)
	return executor.SkippedProjectKeys(), nil
}

func initClient(ctx context.Context, cfg ExtractConfig) (*sqapi.Client, *RawClient, common.Version, Edition, error) {
	opts := baseSDKOptions(cfg)

	version, err := detectVersion(ctx, cfg)
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("detecting server version: %w", err)
	}

	// The SDK still picks Bearer vs Basic auth from a float; major.minor
	// is sufficient for that gate (10.x+ = Bearer).
	client := sqapi.NewServerClient(cfg.URL, cfg.Token, version.LegacyFloat(), opts...)
	if client.CertErr() != nil {
		return nil, nil, nil, "", fmt.Errorf("certificate error: %w", client.CertErr())
	}

	raw := NewRawClient(client.HTTPClient(), client.BaseURL())
	edition, err := detectEdition(ctx, raw)
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("detecting edition: %w", err)
	}

	return client, raw, version, edition, nil
}

func prepareExtractDir(cfg ExtractConfig) (string, string, error) {
	extractID := cfg.ExtractID
	if extractID == "" {
		extractID = generateRunID(cfg.ExportDirectory)
	}
	extractDir := filepath.Join(cfg.ExportDirectory, extractID)
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return "", "", fmt.Errorf("creating extract dir: %w", err)
	}
	return extractID, extractDir, nil
}

func buildPlan(cfg ExtractConfig, edition Edition) (map[string]*TaskDef, [][]string, []string, error) {
	allDefs := RegisterAll()
	registry := BuildRegistry(allDefs)
	registry = FilterByEdition(registry, edition)

	var targets []string
	if cfg.IncludeScanHistory {
		targets = TargetTasksWithScanHistory(registry, cfg.TargetTask, cfg.ExtractType)
	} else {
		targets = TargetTasks(registry, cfg.TargetTask, cfg.ExtractType)
	}
	taskSet := ResolveDependencies(targets, registry)
	if taskSet == nil {
		return nil, nil, nil, fmt.Errorf("cannot resolve dependencies for target tasks")
	}

	plan, err := PlanPhases(taskSet, registry)
	if err != nil {
		return nil, nil, nil, err
	}
	return registry, plan, targets, nil
}

func newExecutor(raw *RawClient, store *DataStore, baseURL string, edition Edition, version common.Version, concurrency int) *Executor {
	// Delegate to slog.Default() so the global --debug persistent flag
	// (configured in cmd.root.PersistentPreRun) controls visibility of
	// e.Logger.Debug calls here. Hardcoding LevelInfo previously hid
	// every Debug entry the extract pipeline emits.
	return &Executor{
		Raw:       raw,
		Store:     store,
		ServerURL: baseURL,
		Edition:   edition,
		Version:   version,
		Sem:       make(chan struct{}, concurrency),
		Logger:    slog.Default(),
	}
}

func executePhases(ctx context.Context, executor *Executor, plan [][]string, registry map[string]*TaskDef, store *DataStore) error {
	for i, phase := range plan {
		executor.Logger.Info("starting phase", "phase", i+1, "tasks", len(phase))
		if err := runPhase(ctx, executor, phase, registry); err != nil {
			return fmt.Errorf("phase %d: %w", i+1, err)
		}
		for _, taskName := range phase {
			store.MarkComplete(taskName)
		}
	}
	return nil
}

func runPhase(ctx context.Context, e *Executor, taskNames []string, registry map[string]*TaskDef) error {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))
	for _, name := range taskNames {
		def := registry[name]
		e.Logger.Info("running task", "task", name)
		g.Go(func() error {
			if err := def.Run(ctx, e); err != nil {
				return fmt.Errorf("task %s: %w", name, err)
			}
			return nil
		})
	}
	return g.Wait()
}

func (cfg *ExtractConfig) applyDefaults() {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 25
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60
	}
	if cfg.ExportDirectory == "" {
		cfg.ExportDirectory = "/app/files/"
	}
	if cfg.ExtractType == "" {
		cfg.ExtractType = "all"
	}
	// Ensure trailing slash on URL.
	if cfg.URL != "" && cfg.URL[len(cfg.URL)-1] != '/' {
		cfg.URL += "/"
	}
}

func detectVersion(ctx context.Context, cfg ExtractConfig) (common.Version, error) {
	// Temporary client with version 10 (bearer auth) to fetch the raw
	// version string. Parsing into common.Version preserves all components
	// so 9.9.3 can be compared against 9.9.12 without precision loss.
	tmpClient := sqapi.NewServerClient(cfg.URL, cfg.Token, 10.0, baseSDKOptions(cfg)...)
	raw := NewRawClient(tmpClient.HTTPClient(), tmpClient.BaseURL())
	body, err := raw.GetRaw(ctx, "api/server/version", nil)
	if err != nil {
		return nil, err
	}
	v := common.ParseVersion(string(body))
	if v == nil {
		return nil, fmt.Errorf("could not parse server version %q", string(body))
	}
	return v, nil
}

// baseSDKOptions assembles the SDK option set shared by every extract API
// client: timeout, optional mTLS, and (when --debug is set) the HTTP
// request/response debug logger that surfaces every API call as a Debug
// slog entry.
func baseSDKOptions(cfg ExtractConfig) []sqapi.Option {
	opts := []sqapi.Option{sqapi.WithTimeout(cfg.Timeout)}
	if cfg.PEMFilePath != "" {
		opts = append(opts, sqapi.WithClientCert(cfg.PEMFilePath, cfg.KeyFilePath, cfg.CertPassword))
	}
	if cfg.Debug {
		opts = append(opts, sqapi.WithDebugLogger(common.NewHTTPDebugLogger(slog.Default())))
	}
	return opts
}

func detectEdition(ctx context.Context, raw *RawClient) (Edition, error) {
	body, err := raw.Get(ctx, "api/system/info", nil)
	if err != nil {
		// /api/system/info requires admin; fall back to /api/navigation/global
		// which returns the same "edition" field without elevated privileges.
		if common.IsHTTPError(err, 403) {
			body, err = raw.Get(ctx, "api/navigation/global", nil)
			if err != nil {
				return EditionCommunity, err
			}
			return ParseEdition(body), nil
		}
		return EditionCommunity, err
	}
	return ParseEdition(body), nil
}

// generateRunID returns an ISO-date-prefixed extract ID (issue
// #108). Format: "YYYY-MM-DD-NN". See migrate.generateRunID for the
// rationale — the two helpers are deliberately kept in sync.
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

// extractMeta groups the parameters for writeMetadata. Version stays as
// the parsed tuple internally; it is downcast to a float in the on-disk
// JSON for backwards compatibility with reports written before #278.
type extractMeta struct {
	Plan     [][]string
	RunID    string
	Version  common.Version
	Edition  Edition
	URL      string
	Targets  []string
	Registry map[string]*TaskDef
}

func writeMetadataFile(dir string, m extractMeta) error {
	configs := make([]string, 0, len(m.Registry))
	for name := range m.Registry {
		configs = append(configs, name)
	}

	meta := map[string]any{
		"plan":              m.Plan,
		"version":           m.Version.LegacyFloat(),
		"version_string":    m.Version.String(),
		"edition":           string(m.Edition),
		"url":               m.URL,
		"target_tasks":      m.Targets,
		"available_configs": configs,
		"run_id":            m.RunID,
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "extract.json"), data, 0o644)
}

func filterCompleted(plan [][]string, store *DataStore) [][]string {
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
