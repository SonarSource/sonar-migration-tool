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

// DefaultBuildConcurrency bounds how many scanner reports are built at
// once. Report construction is where importProjectData's memory goes: the
// branch's full source text, the protobuf messages built from it, and the
// packaged ZIP are all live simultaneously.
//
// It is much lower than the default request concurrency of 25 on purpose.
// The phase's wall clock is dominated by PollCETask, which polls every few
// seconds for minutes, so throttling construction costs little while
// cutting peak memory by roughly the ratio between the two.
const DefaultBuildConcurrency = 4

// MigrateConfig holds all parameters for a migrate run.
type MigrateConfig struct {
	Token         string
	EnterpriseKey string
	Edition       string // "enterprise", "developer", etc.
	URL           string // Cloud URL (default: https://sonarcloud.io/)
	RunID         string // Resume a prior run
	Concurrency   int
	// BuildConcurrency bounds concurrent scanner-report CONSTRUCTION during
	// importProjectData, independently of Concurrency. See Executor.BuildSem.
	BuildConcurrency int
	// Timeout is the per-HTTP-request timeout in seconds applied to
	// every SonarQube Cloud call the migrate phase makes (#383). When
	// <= 0, applyDefaults sets it to 60 — matching the SDK default
	// and what the extract pipeline uses (extract.go).
	Timeout         int
	ExportDirectory string
	TargetTask      string

	// TargetTasks, when non-empty, is an explicit list of leaf tasks to run;
	// their dependencies are resolved automatically. Used by the transfer
	// command for project-scoped migration. Takes precedence over TargetTask.
	TargetTasks []string

	SkipProfiles             bool
	IncludeProjectData       bool
	SkipIssueSync            bool // Skip the final issue / hotspot metadata sync (#299).
	SkipProjectDataMigration bool // Skip importProjectData + the trailing sync tasks (#303).
	Debug                    bool // Enable slog.LevelDebug + verbose request payload logs

	// DefaultOrganization, when set, is used as the SonarCloud org for
	// every row in organizations.csv if none have a sonarcloud_org_key.
	// If at least one mapping is defined, this is ignored with a Warn
	// log. Issue #281.
	DefaultOrganization string

	// ExcludeBranches holds glob patterns for non-main branches to skip
	// during project data import. Main branch is never excluded.
	ExcludeBranches []string

	// UnsupportedLanguages selects how a project whose files use a language
	// with no quality profile on the target organization is handled (#474):
	// "exclude" (default) drops those files from the scanner report so the
	// rest of the project migrates, "skip" declines to migrate the project's
	// data at all, "warn" submits the report unchanged. Empty resolves to
	// DefaultUnsupportedLanguages.
	UnsupportedLanguages string

	// FastSync skips tagging and back-linking hotspots (and issues) that
	// have zero user changes on the source — original state (TO_REVIEW /
	// OPEN), no user comment, no custom tags (#527). Defaults to false:
	// every hotspot is tagged and back-linked, the pre-#527 behavior.
	FastSync bool

	// ProjectKeyPattern is the template used to derive each target
	// SonarQube Cloud project key from the source key, the org key, and
	// the enterprise key. Defaults to DefaultProjectKeyPattern. Issue #138.
	ProjectKeyPattern string

	// ProgressCallback, when set, is invoked with the same run-wide
	// percent/ETA snapshot as the #520 log line, on every tick and once
	// more at completion. Nil for CLI callers (go/cmd/migrate.go); the
	// GUI wizard sets it to drive a progress bar (#519).
	ProgressCallback func(percent float64, eta time.Duration, known bool)
}

// Executor is the runtime context passed to every migrate task function.
type Executor struct {
	Cloud     *cloud.Client     // Standard Cloud API (sonarcloud.io)
	CloudAPI  *cloud.Client     // Enterprise API (api.sonarcloud.io)
	Raw       *common.RawClient // For reading from Cloud standard API
	RawAPI    *common.RawClient // For reading from Cloud enterprise API
	Extract   *common.DataStore // Reads extract data (across all extract runs)
	Store     *common.DataStore // Writes migrate output to run directory
	CloudURL  string            // e.g. "https://sonarcloud.io/"
	APIURL    string            // e.g. "https://api.sonarcloud.io/"
	EntKey    string            // Enterprise key
	Edition   common.Edition
	ExportDir string // Root export directory
	Mapping   structure.ExtractMapping
	// Sem is a capacity carrier, NOT a semaphore. Nothing in this package
	// ever sends to or receives from it; every reference reads cap(e.Sem)
	// to size a per-task errgroup limit. Each task therefore gets its own
	// independent limit rather than sharing one pool.
	//
	// Do not "fix" this by acquiring it. The fan-outs nest —
	// runSyncIssueMetadata's forEachMigrateItem holds a slot for each of
	// its 25 workers, and each of those calls runProjectSyncLoop, which
	// limits on the same capacity. On one shared counting semaphore the
	// outer holders would take every slot and no inner work could ever
	// acquire: a permanent deadlock. Making it real requires restructuring
	// the nested fan-outs first.
	Sem chan struct{}
	// BuildSem bounds concurrent scanner-report CONSTRUCTION, which is the
	// memory-heavy part of importProjectData: a branch's full source text,
	// its protobufs and the packaged ZIP are all live at once.
	//
	// Deliberately NOT the same bound as project fan-out. importProjectData
	// spends most of its wall clock in PollCETask, so 25 branches can stay
	// in flight against the CE while only a few are being built.
	//
	// May be nil (test fixtures); callers must nil-check.
	BuildSem        chan struct{}
	Logger          *slog.Logger
	ExcludeBranches []string
	Progress        *common.Tracker // run-wide progress/ETA estimator (#520)

	// UnsupportedLanguages is the resolved handling mode for files whose
	// language has no quality profile on the target organization (#474):
	// UnsupportedLanguagesExclude / Skip / Warn.
	UnsupportedLanguages string

	// FastSync — see MigrateConfig.FastSync (#527).
	FastSync bool

	// ProjectKeyPattern is the resolved target-key template (issue #138),
	// consumed by every task that derives a SonarQube Cloud project key
	// (createProjects, matchProjectRepos, permission templates, portfolios).
	ProjectKeyPattern string

	// ResetConfirmedOrgs is populated only by RunReset after the
	// operator has interactively confirmed which SonarCloud orgs to
	// wipe (#381). When set (non-nil), loadCSVToJSONL rewrites the
	// sonarcloud_org_key of every row whose org is NOT in this set to
	// the SKIPPED sentinel — the existing shouldSkipOrg path then
	// naturally excludes those orgs from every per-org delete/reset
	// task without per-task plumbing. Nil for migrate runs (no filter).
	ResetConfirmedOrgs map[string]bool
}

// RunMigrate is the main entry point for the migrate command.
// Returns the run ID on success.
func RunMigrate(ctx context.Context, cfg MigrateConfig) (runIDOut string, retErr error) {
	cfg.applyDefaults()

	tm := &RunTimings{StartedAt: time.Now()}

	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	collector := &eventCollector{}
	base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	logger := slog.New(newEventHandler(base, collector))

	appliedDefault, err := validateMigrateConfig(cfg, logger)
	if err != nil {
		return "", err
	}

	// Installed on the HTTP clients now and pointed at the run directory
	// once it exists; entries are buffered until then.
	reqLog := newRequestLogWriter()
	defer reqLog.Close()
	clients := newMigrateClients(cfg, logger, reqLog)

	// Verify every SQC organization the migration will touch exists and
	// is visible to the token, and that the project-key pattern doesn't
	// collide with one (issues #283, #138). Done early so a config error
	// aborts the run before any extract data is touched.
	if err := validateMigrateOrgs(ctx, clients.Cloud, cfg, appliedDefault); err != nil {
		return "", err
	}

	// Advisory pre-flight: surface unbound target organizations now rather
	// than once per affected project in phase 4.
	warnUnboundOrgs(ctx, clients.Raw, cfg, appliedDefault, logger)

	mp, err := prepareMigratePlan(cfg, logger)
	if err != nil {
		return "", err
	}
	runIDOut = mp.RunID

	// The run directory now exists: flush buffered request records and
	// stream the rest straight to requests.log.
	reqLog.Open(mp.RunDir, logger)

	// Best-effort run artifacts: written on every exit path (success or
	// error) without altering retErr or panicking.
	defer func() {
		tm.CompletedAt = time.Now()
		writeMigrateRunArtifacts(mp.RunDir, tm, retErr, cfg.ProjectKeyPattern, collector, logger)
		// End-of-command timing line (#311) — paired with the per-task
		// lines from runPhase so operators get a complete duration view.
		common.LogCommandDuration(logger, "migrate", tm.StartedAt)
	}()

	defer writeRateLimitArtifact(mp.RunDir, clients.RateLimitTracker, logger)

	// Filter completed tasks for resumability.
	store := common.NewDataStore(mp.RunDir)
	phases := filterCompleted(mp.Plan, store)

	executor := &Executor{
		Cloud:                clients.Cloud,
		CloudAPI:             clients.CloudAPI,
		Raw:                  clients.Raw,
		RawAPI:               clients.RawAPI,
		Extract:              nil, // Will be set per-task based on extract mapping
		Store:                store,
		CloudURL:             clients.CloudURL,
		APIURL:               clients.APIURL,
		EntKey:               cfg.EnterpriseKey,
		Edition:              mp.Edition,
		ExportDir:            cfg.ExportDirectory,
		Mapping:              mp.Mapping,
		Sem:                  make(chan struct{}, cfg.Concurrency),
		BuildSem:             make(chan struct{}, cfg.BuildConcurrency),
		ExcludeBranches:      cfg.ExcludeBranches,
		UnsupportedLanguages: cfg.UnsupportedLanguages,
		FastSync:             cfg.FastSync,
		ProjectKeyPattern:    cfg.ProjectKeyPattern,
		Logger:               logger,
	}

	// Overall progress/ETA logging (#520) — every 10s for the duration of
	// the run, stopped once phases finish (success or error).
	executor.Progress = common.NewTracker(logger, phases, CategorizeTask, common.DefaultCategoryWeights)
	executor.Progress.OnUpdate(cfg.ProgressCallback)
	executor.Progress.Start(ctx, 10*time.Second)
	defer executor.Progress.Stop()

	// Execute phases.
	for i, phase := range phases {
		logger.Info("starting phase", "phase", i+1, "tasks", len(phase))
		if err := runPhase(ctx, executor, phase, mp.Registry, i+1, tm); err != nil {
			return runIDOut, fmt.Errorf("phase %d: %w", i+1, err)
		}
		for _, taskName := range phase {
			store.MarkComplete(taskName)
		}
	}
	executor.Progress.LogFinal()

	fmt.Printf("%s v%s - Migration Complete: %s\n", version.ToolName, version.Version, runIDOut)
	return runIDOut, nil
}

// validateMigrateConfig validates the project-key renaming pattern syntax
// and the SQC org mapping, applying the --default_organization fallback if
// requested (issues #138, #279, #281). Both checks run before any API
// client setup so a config error surfaces immediately.
func validateMigrateConfig(cfg MigrateConfig, logger *slog.Logger) (appliedDefault bool, err error) {
	if err := ValidateProjectKeyPattern(cfg.ProjectKeyPattern); err != nil {
		return false, common.NewExitError(2, fmt.Errorf("invalid project_key_pattern: %w", err))
	}
	return applyOrgMapping(cfg.ExportDirectory, cfg.DefaultOrganization, logger)
}

// validateMigrateOrgs verifies every SQC organization the migration will
// touch exists and is visible to the token (issue #283), and that the
// project-key pattern's static prefix — used in place of
// <ORGANIZATION_KEY> — doesn't collide with a real org (issue #138).
func validateMigrateOrgs(ctx context.Context, cc *cloud.Client, cfg MigrateConfig, appliedDefault bool) error {
	if err := validateOrgsExist(ctx, cc.Organizations, cfg.ExportDirectory, cfg.EnterpriseKey, cfg.DefaultOrganization, appliedDefault); err != nil {
		return err
	}
	return validatePatternOrgCollision(ctx, cc.Organizations, cfg.ProjectKeyPattern)
}

// migrateClients bundles the Cloud API clients, raw readers, and
// rate-limit tracker a migrate run wires together before executing tasks.
type migrateClients struct {
	Cloud            *cloud.Client
	CloudAPI         *cloud.Client
	Raw              *common.RawClient
	RawAPI           *common.RawClient
	CloudURL         string
	APIURL           string
	RateLimitTracker *RateLimitTracker
}

// newMigrateClients builds the standard and enterprise Cloud API clients
// for a migrate run, wiring retry, rate-limit, and (when cfg.Debug is set)
// full HTTP request/response logging into each one.
func newMigrateClients(cfg MigrateConfig, logger *slog.Logger, reqLog *requestLogWriter) *migrateClients {
	cloudURL := cfg.URL
	apiURL := strings.Replace(cloudURL, "https://", "https://api.", 1)

	retryLog := func(method, url string, status, attempt, total int) {
		logger.Warn("retrying request",
			"method", method, "endpoint", url,
			"status", status, "attempt", attempt, "maxAttempts", total)
	}
	rateLimitTracker := NewRateLimitTracker()
	rlEpisode := &rateLimitEpisodeLogger{logger: logger}
	rateLimitObs := func(event sqapi.RateLimitEvent) {
		// Observe feeds the run-wide stats persisted to rate_limit_events.json
		// for the PDF report; the first event of each kind also carries the
		// body snippet, which we log once for operator review.
		if rateLimitTracker.Observe(event) {
			logger.Warn("rate limiting detected",
				"kind", event.Kind.String(),
				"retryAfter", event.RetryAfter,
				"waitChosen", event.WaitChosen,
				"bodySnippet", event.BodySnippet)
		}
		// Edge-triggered per-episode "paused" log (deduplicated across
		// concurrent workers).
		rlEpisode.onHit(event)
	}
	rateLimitRecovery := func(_, _ string, retries int, waited time.Duration) {
		rlEpisode.onResume(retries, waited)
	}
	clientOpts := []sqapi.Option{
		sqapi.WithTimeout(cfg.Timeout),
		sqapi.WithRetryLogger(retryLog),
		sqapi.WithRateLimitObserver(rateLimitObs),
		sqapi.WithRateLimitRecoveryLogger(rateLimitRecovery),
	}
	if reqLog != nil {
		clientOpts = append(clientOpts, sqapi.WithRequestLogger(reqLog.Log))
	}
	if cfg.Debug {
		clientOpts = append(clientOpts, sqapi.WithDebugLogger(common.NewHTTPDebugLogger(logger)))
	}
	cloudClient := sqapi.NewCloudClient(cloudURL, cfg.Token, clientOpts...)
	apiClient := sqapi.NewCloudClient(apiURL, cfg.Token, clientOpts...)

	return &migrateClients{
		Cloud:            cloud.New(cloudClient),
		CloudAPI:         cloud.New(apiClient),
		Raw:              common.NewRawClient(cloudClient.HTTPClient(), cloudClient.BaseURL()),
		RawAPI:           common.NewRawClient(apiClient.HTTPClient(), apiClient.BaseURL()),
		CloudURL:         cloudClient.BaseURL(),
		APIURL:           apiClient.BaseURL(),
		RateLimitTracker: rateLimitTracker,
	}
}

// migratePlan bundles the resolved extract mapping, task registry, and
// dependency-resolved phase plan a migrate run executes.
type migratePlan struct {
	Mapping  structure.ExtractMapping
	Edition  common.Edition
	RunID    string
	RunDir   string
	Registry map[string]*TaskDef
	Plan     [][]string
}

// prepareMigratePlan resolves the extract mapping, creates the run
// directory, and builds the dependency-resolved phase plan for the
// requested target tasks — persisting the plan metadata for a fresh run.
func prepareMigratePlan(cfg MigrateConfig, logger *slog.Logger) (*migratePlan, error) {
	mapping, err := structure.GetUniqueExtracts(cfg.ExportDirectory)
	if err != nil {
		return nil, fmt.Errorf("scanning extracts: %w", err)
	}

	edition := common.Edition(cfg.Edition)

	// Generate or resume run ID.
	runID := cfg.RunID
	createPlan := runID == ""
	if createPlan {
		runID = common.GenerateRunID(cfg.ExportDirectory)
	}
	runDir := filepath.Join(cfg.ExportDirectory, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating run dir: %w", err)
	}

	registry := FilterByEdition(BuildMigrateRegistry(RegisterAll()), edition)

	// Project-data migration covers importProjectData + the trailing
	// issue/hotspot sync pair. Skipping it necessarily skips the
	// sync as well — propagate the flag so the existing SkipIssueSync
	// logging surfaces both halves of the decision. #303.
	if cfg.SkipProjectDataMigration {
		cfg.SkipIssueSync = true
		logger.Info("project data migration disabled: skipping importProjectData")
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

	targets := MigrateTargetTasks(registry, cfg.TargetTask, cfg.SkipProfiles, cfg.IncludeProjectData, cfg.SkipIssueSync, cfg.SkipProjectDataMigration, cfg.TargetTasks)
	taskSet := ResolveDependencies(targets, registry)
	if taskSet == nil {
		return nil, fmt.Errorf("cannot resolve dependencies for target tasks")
	}

	plan, err := PlanPhases(taskSet, registry)
	if err != nil {
		return nil, err
	}

	// Write plan metadata for a fresh run.
	if createPlan {
		if err := writeMigrateMeta(runDir, plan, runID, edition, cfg.URL, targets, registry); err != nil {
			return nil, err
		}
	}

	return &migratePlan{
		Mapping:  mapping,
		Edition:  edition,
		RunID:    runID,
		RunDir:   runDir,
		Registry: registry,
		Plan:     plan,
	}, nil
}

// writeMigrateRunArtifacts best-effort persists run_meta.json and the
// collected run-events log on every exit path (success or error) without
// altering retErr or panicking.
func writeMigrateRunArtifacts(runDir string, tm *RunTimings, retErr error, keyPattern string, collector *eventCollector, logger *slog.Logger) {
	meta := RunMeta{
		StartedAt:         tm.StartedAt,
		CompletedAt:       tm.CompletedAt,
		OverallStatus:     computeStatus(retErr, tm),
		Phases:            tm.phasesSnapshot(),
		Tasks:             tm.tasksSnapshot(),
		ProjectKeyPattern: keyPattern,
	}
	if b, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(runDir, "run_meta.json"), b, 0o644)
	}
	if err := writeRunEvents(runDir, collector); err != nil {
		logger.Warn("writing run events", "err", err)
	}
}

// writeRateLimitArtifact best-effort persists the rate-limit events
// collected during the run, for later PDF reporting.
func writeRateLimitArtifact(runDir string, tracker *RateLimitTracker, logger *slog.Logger) {
	if writeErr := tracker.WriteJSON(filepath.Join(runDir, RateLimitEventsFile)); writeErr != nil {
		logger.Warn("failed to write rate-limit events artefact", "err", writeErr)
	}
}

// maxConcurrentTasksPerPhase caps task-level fan-out within a phase.
// Combined with the per-task limit of cap(e.Sem) this bounds total
// in-flight requests at maxConcurrentTasksPerPhase * concurrency.
const maxConcurrentTasksPerPhase = 6

func runPhase(ctx context.Context, e *Executor, taskNames []string, registry map[string]*TaskDef, phaseIdx int, tm *RunTimings) error {
	phaseStart := time.Now()
	g, ctx := errgroup.WithContext(ctx)
	// Bound how many tasks in a phase run at once. Each task opens its
	// own errgroup limited to cap(e.Sem), so an unbounded phase
	// multiplies that by the task count — a 14-task phase at the default
	// concurrency of 25 puts up to 350 requests in flight against one
	// host. Tasks stay concurrent (they are few and mostly I/O bound),
	// just not unboundedly so.
	g.SetLimit(maxConcurrentTasksPerPhase)
	for _, name := range taskNames {
		def := registry[name]
		e.Logger.Info("running task", "task", name)
		g.Go(func() error {
			taskStart := time.Now()
			counter := NewTaskCounter(name)
			taskCtx := WithTaskCounter(ctx, counter)
			runErr := def.Run(taskCtx, e)
			elapsed := time.Since(taskStart)
			tm.addTask(TaskTiming{
				Phase:    phaseIdx,
				Name:     name,
				Duration: elapsed.Seconds(),
				OK:       runErr == nil,
				Err:      errString(runErr),
			})
			// Single end-of-task INFO log carrying counts + duration
			// (#311 + #333). When the task didn't record any per-
			// item outcomes the helper falls back to a plain
			// duration line.
			counter.LogSummary(e.Logger, elapsed)
			if runErr != nil {
				e.Logger.Error("task failed", "task", name, "err", runErr)
				return fmt.Errorf("task %s: %w", name, runErr)
			}
			e.Progress.MarkTaskComplete(name)
			return nil
		})
	}
	err := g.Wait()
	tm.addPhase(PhaseTiming{Index: phaseIdx, Tasks: len(taskNames), Duration: time.Since(phaseStart).Seconds()})
	return err
}

func (cfg *MigrateConfig) applyDefaults() {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 25
	}
	if cfg.BuildConcurrency <= 0 {
		cfg.BuildConcurrency = DefaultBuildConcurrency
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60
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
	if strings.TrimSpace(cfg.ProjectKeyPattern) == "" {
		cfg.ProjectKeyPattern = DefaultProjectKeyPattern
	}
	// #474 — normalise the unsupported-language handling mode. An invalid
	// value is rejected at the CLI layer (ValidateUnsupportedLanguages), so
	// here we only need to fill in the default for an absent value.
	if mode, err := ParseUnsupportedLanguageMode(cfg.UnsupportedLanguages); err == nil {
		cfg.UnsupportedLanguages = mode
	} else {
		cfg.UnsupportedLanguages = DefaultUnsupportedLanguages
	}
	// Ensure trailing slash.
	if cfg.URL != "" && cfg.URL[len(cfg.URL)-1] != '/' {
		cfg.URL += "/"
	}
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
			// importProjectData owns its own resume granularity:
			// loadCompletedBranches + shouldSkipBranch decide per
			// (project, branch) what to redo. Its output directory is
			// created by the first e.Store.Writer call — long before
			// every branch finishes — so the generic dir-existence
			// gate would silently drop the task on resume and never
			// re-run the unfinished branches. #393.
			if task == "importProjectData" {
				fp = append(fp, task)
				continue
			}
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
