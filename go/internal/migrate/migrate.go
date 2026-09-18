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
	"regexp"
	"strings"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
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
//
// This is also the floor and the non-Linux/undetectable fallback for
// AdaptiveBuildConcurrency — the value used when memory can't be
// detected, or is too small to safely justify going higher (#541).
const DefaultBuildConcurrency = 4

const (
	// buildMemoryBudgetPerWorker is a conservative per-concurrent-build
	// memory assumption, in bytes. BenchmarkLoadBranchSourceData measured
	// ~915 MiB allocated for one branch of even a small synthetic
	// project (scoped_extract_alloc_test.go) before the #541 streaming
	// fix that cut it to ~405 MiB allocated / ~3 MiB retained — and that
	// fix is what let BuildSem exist at all without still risking the
	// OOM. There is no benchmark for a large real project's branch
	// (protobuf + ZIP packaging included, not just source loading), so
	// this rounds up from the largest number that IS measured rather
	// than guess low.
	buildMemoryBudgetPerWorker = 1 << 30 // 1 GiB

	// buildConcurrencyBudgetShare reserves the rest of the detected
	// memory budget for everything else the process needs during this
	// phase — the general request pool's own transient allocations,
	// the Go runtime, OS overhead the cgroup accounting doesn't capture
	// — rather than letting build concurrency alone claim the whole
	// budget.
	buildConcurrencyBudgetShare = 0.5

	// maxAdaptiveBuildConcurrency caps how far detected memory can push
	// this. Issue #541's OOM happened at an effectively unbounded 25 (no
	// separate build limit existed yet) on a 32 GB VM, so this stays
	// below that even on a very large host until real large-project
	// measurements justify raising it.
	maxAdaptiveBuildConcurrency = 20
)

// AdaptiveBuildConcurrency derives a --project_data_build_concurrency
// default from the memory actually available to this process — the same
// cgroup/meminfo detection common.ApplyMemoryLimit uses for GOMEMLIMIT —
// instead of the single fixed guess DefaultBuildConcurrency was on its
// own. Falls back to DefaultBuildConcurrency when the budget can't be
// determined (non-Linux, or detection failed), so nothing changes for
// local/dev runs (#573 follow-up).
func AdaptiveBuildConcurrency() int {
	budget, _ := common.MemoryBudget()
	return adaptiveBuildConcurrency(budget)
}

// adaptiveBuildConcurrency is the testable core of AdaptiveBuildConcurrency.
func adaptiveBuildConcurrency(budgetBytes int64) int {
	if budgetBytes <= 0 {
		return DefaultBuildConcurrency
	}
	n := int(float64(budgetBytes) * buildConcurrencyBudgetShare / buildMemoryBudgetPerWorker)
	if n < DefaultBuildConcurrency {
		return DefaultBuildConcurrency
	}
	if n > maxAdaptiveBuildConcurrency {
		return maxAdaptiveBuildConcurrency
	}
	return n
}

// DefaultMaxIssueComments is the number of most-recent comments migrated
// onto each Cloud issue/hotspot when --max_issue_comments is unset (#571).
const DefaultMaxIssueComments = 5

// MaxAllowedIssueComments is the highest value --max_issue_comments accepts.
// Comments are migrated one add_comment API call at a time, so an
// unbounded value could put real pressure on SonarQube Cloud for issues
// with long discussion threads (#571).
const MaxAllowedIssueComments = 20

// ValidateMaxIssueComments rejects a --max_issue_comments value above
// MaxAllowedIssueComments. A value of 0 is not an error: it means "use the
// default" (applyDefaults fills in DefaultMaxIssueComments), matching the
// existing zero-means-default convention for Concurrency/Timeout. A value
// of -1 is the documented sentinel for "no cap": applyDefaults leaves it
// untouched and capIssueComments replays every source comment (#571).
func ValidateMaxIssueComments(n int) error {
	if n > MaxAllowedIssueComments {
		return fmt.Errorf("max_issue_comments %d exceeds the maximum allowed value of %d", n, MaxAllowedIssueComments)
	}
	return nil
}

// MaxBranchesPerProject is the hard cap on the number of long-lived
// branches migrated per project (#584) — a safeguard against projects with
// too many branches (little branch housekeeping, or a branch selection
// filter that casts too wide a net) making a migrate/transfer run too slow
// and putting too much API pressure on SonarQube Cloud. Not user-facing by
// design: a single constant, easy to revisit in one place. Applies only to
// migrate/transfer; extract has no such limit.
const MaxBranchesPerProject = 10

// MigrateConfig holds all parameters for a migrate run.
type MigrateConfig struct {
	Token         string
	EnterpriseKey string
	Edition       string // "enterprise", "developer", etc.
	URL           string // Cloud URL (default: https://sonarcloud.io/)
	RunID         string // Resume a prior run
	// Concurrency, if set (via --concurrency or the config file's
	// "concurrency" field), is only the STARTING point the dynamic
	// ConcurrencyLimiter seeds from — it is always re-evaluated every 30s
	// against observed API latency from the moment the run starts,
	// regardless of whether this was set (#573). --concurrency /
	// "concurrency" is deprecated in favor of --api_max_rate_per_min,
	// which controls the target rate the adjustment aims for. <= 0
	// resolves to 25 by applyDefaults.
	Concurrency int
	// APIMaxRatePerMin caps sustained SonarQube Cloud API calls/min via a
	// sliding-window limiter, and is the target rate the dynamic
	// ConcurrencyLimiter aims for (#573). <= 0 is resolved to 1500 by
	// applyDefaults. Valid range enforced by the CLI layer is [100, 1500].
	APIMaxRatePerMin int
	// BuildConcurrency bounds concurrent scanner-report CONSTRUCTION during
	// importProjectData, independently of Concurrency. See Executor.BuildSem.
	// <= 0 resolves via AdaptiveBuildConcurrency, not a single fixed
	// default — sized from memory actually available to this process
	// when it can be detected.
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

	// Objects, when non-nil, limits migration to the selected object
	// categories (settings, permission_templates, quality_profiles,
	// quality_gates, projects, portfolios, groups, license_profiles —
	// aliases qp/qg/pt/lp). nil means "everything" — same semantics as
	// common.ParseObjects's empty-input contract (#536).
	Objects map[string]bool
	// objectsRaw carries the raw --objects / config-file "objects" values
	// from LoadMigrateConfigFile's parsing step through to the
	// common.ParseObjects call that fills in Objects, so parsing logic
	// lives in one place (config_file.go) instead of being duplicated
	// between the config-file loader and cmd/migrate.go's CLI handling.
	// Cleared once Objects is populated; not meant to be read afterward.
	objectsRaw []string
	// ProjectKeyFilter, when non-empty, is a regexp pattern (raw --project_key
	// value, or the config file's top-level "project_key") restricting
	// migration to source project keys that fully match it (#536, mirrors
	// #529's transfer-side flag). Unlike extract, migrate never calls the
	// source API to resolve keys — createProjects filters the records it
	// already read locally from generateProjectMappings, so the pattern is
	// stored as-is and only ever used when the "projects" category is
	// selected (cmd/migrate.go no-ops the flag otherwise, per the issue).
	ProjectKeyFilter string

	// BranchRegexp, when non-empty, limits which branches importProjectData
	// migrates for each project, on top of whatever the extract phase already
	// limited getBranches to. Always compiled as a full-match regex
	// implicitly anchored with ^ and $ (mirrors ProjectKeyFilter,
	// extract.CompileProjectKeyPattern). The project's main branch is always
	// migrated regardless of match (same bypass as ExcludeBranches). Empty
	// means "migrate every extracted branch" (#582).
	BranchRegexp string

	// ProgressCallback, when set, is invoked with the same run-wide
	// percent/ETA snapshot as the #520 log line, on every tick and once
	// more at completion. Nil for CLI callers (go/cmd/migrate.go); the
	// GUI wizard sets it to drive a progress bar (#519).
	ProgressCallback func(percent float64, eta time.Duration, known bool)

	// MigrateHistory opts into the project-history migration PoC (#554):
	// replay each project's extracted historical (date, measures) snapshots
	// (see internal/extract's getProjectAnalysisHistory) as separate,
	// backdated analyses on the target's main branch, before the regular
	// current-snapshot import. Defaults to false — when unset, migrate
	// ignores any extracted history records and behaves exactly as before
	// this feature existed, even if extract happened to capture history
	// data for a different run.
	MigrateHistory bool

	// MaxIssueComments caps the number of source comments replayed onto a
	// single Cloud issue/hotspot during metadata sync, keeping only the
	// most recent ones (#571) — every comment is an extra
	// /api/issues/add_comment call, and instances with long comment
	// threads were putting avoidable pressure on SonarQube Cloud. <= 0
	// resolves to DefaultMaxIssueComments; values above MaxAllowedIssueComments
	// are rejected by ValidateMaxIssueComments at the CLI layer.
	MaxIssueComments int

	// BranchAnalyzedAfter is the raw --branch_analyzed_after value (or the
	// resolved target.branch_analyzed_after / top-level branch_analyzed_after
	// from the config file), in YYYY-MM-DD form. "" means unset — every
	// branch is selected, the pre-#583 behavior.
	BranchAnalyzedAfter string
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
	// ConcurrencyLimiter provides a live capacity figure, NOT a semaphore.
	// Nothing in this package acquires or releases it; every reference
	// reads ConcurrencyLimiter.Current() to size a per-task fan-out limit
	// (an errgroup.SetLimit for the few still-fixed/serial cases, a
	// DynamicGate for everything else, #573). Each task therefore gets
	// its own independent limit rather than sharing one pool. Always
	// dynamic (#573): a background goroutine
	// recalculates Current() every 30s from observed API latency,
	// targeting APIMaxRatePerMin calls/min — cfg.Concurrency, if set, is
	// only the starting value it seeds from, never a permanent fixed cap.
	//
	// Do not "fix" this by acquiring it. The fan-outs nest —
	// runSyncIssueMetadata's forEachMigrateItem holds a slot for each of
	// its workers, and each of those calls syncProjectIssues, whose inner
	// loop is bounded by nestedSyncLoopConcurrency (its own gate). Sharing
	// one counting semaphore across both levels would let the outer holders
	// take every slot and deadlock the inner work.
	ConcurrencyLimiter *ConcurrencyLimiter
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

	// Objects mirrors MigrateConfig.Objects (#536): nil means "everything
	// selected". Most task exclusion happens at plan time (see
	// excludedMigrateTasks / ResolveDependenciesExcluding), but
	// runSetGlobalSettings additionally needs it at RUNTIME to decide
	// whether its project-scope fallback path is allowed to assume
	// projects exist — see the "projects" category gate in
	// tasks_setglobalsettings.go.
	Objects map[string]bool
	// ProjectKeyRe is the compiled form of MigrateConfig.ProjectKeyFilter
	// (#536), or nil when no filter was configured. runCreateProjects
	// consults it to skip source projects whose key doesn't match; every
	// other project-scoped task scopes off createProjects's own output,
	// so filtering there is sufficient.
	ProjectKeyRe *regexp.Regexp
	// BranchRe is the compiled form of MigrateConfig.BranchRegexp, or nil
	// when unset. #582.
	BranchRe *regexp.Regexp
	// MigrateHistory — see MigrateConfig.MigrateHistory (#554).
	MigrateHistory bool
	// HistoryProgress tracks project-history replay (#554) as its own
	// unit of work for the overall ETA (#564): migrateBranchHistory
	// increments it once per historical point submitted. Nil when
	// MigrateHistory is off or there's no history to replay — callers
	// must go through it via ProgressLogger's own nil-safety, or check
	// for nil directly (see migrateBranchHistory).
	HistoryProgress *common.ProgressLogger

	// MaxIssueComments — see MigrateConfig.MaxIssueComments (#571).
	MaxIssueComments int

	// BranchAnalyzedAfter — see MigrateConfig.BranchAnalyzedAfter (#583).
	// Nil means no filter: every branch is selected.
	BranchAnalyzedAfter *time.Time

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

	// #571: reject up front rather than silently letting a mistyped, huge
	// value through — cmd/migrate.go, cmd/transfer.go and cmd/sync_issues.go
	// all validate this at build-config time, but callers that construct a
	// MigrateConfig directly (e.g. the GUI wizard) reach RunMigrate without
	// going through that check.
	if err := ValidateMaxIssueComments(cfg.MaxIssueComments); err != nil {
		return "", err
	}

	// #536: compile --project_key defensively even though cmd/migrate.go
	// already validated it at build-config time — a config-file-only
	// caller (e.g. the GUI wizard) may reach RunMigrate without going
	// through that validation.
	var projectKeyRe *regexp.Regexp
	if cfg.ProjectKeyFilter != "" {
		re, err := extract.CompileProjectKeyPattern(cfg.ProjectKeyFilter)
		if err != nil {
			return "", fmt.Errorf("invalid project_key pattern %q: %w", cfg.ProjectKeyFilter, err)
		}
		projectKeyRe = re
	}

	// #582: compile --branch_regexp defensively for the same reason as
	// --project_key above — a config-file-only caller may reach RunMigrate
	// without going through cmd/migrate.go's own validation.
	var branchRe *regexp.Regexp
	if cfg.BranchRegexp != "" {
		re, err := extract.CompileProjectKeyPattern(cfg.BranchRegexp)
		if err != nil {
			return "", fmt.Errorf("invalid branch regexp pattern %q: %w", cfg.BranchRegexp, err)
		}
		branchRe = re
	}
	// #583: parse --branch_analyzed_after defensively even though
	// cmd/migrate.go and cmd/transfer.go already validate it at
	// build-config time — a config-file-only caller (e.g. the GUI wizard)
	// may reach RunMigrate without going through that validation.
	branchAnalyzedAfter, err := common.ParseBranchAnalyzedAfter(cfg.BranchAnalyzedAfter)
	if err != nil {
		return "", err
	}

	tm := &RunTimings{StartedAt: time.Now()}

	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	collector := &eventCollector{}
	base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	logger := slog.New(newEventHandler(base, collector))

	// Installed on the HTTP clients now and pointed at the run directory
	// once it exists; entries are buffered until then.
	reqLog := newRequestLogWriter()
	defer reqLog.Close()
	clients := newMigrateClients(cfg, logger, reqLog)

	// Built ahead of validateMigrateConfig (issue #550) so
	// --default_organization can be checked against the live SonarQube
	// Cloud API BEFORE applyOrgMapping ever writes it to
	// organizations.csv. Previously the value was persisted first and
	// only validated afterwards by validateMigrateOrgs below, so a wrong
	// --default_organization on a failed run got written to disk; a
	// retry with a corrected value then found organizations.csv already
	// "mapped" and silently ignored the correction.
	appliedDefault, err := validateMigrateConfig(ctx, clients.Cloud, cfg, logger)
	if err != nil {
		return "", err
	}

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
		ConcurrencyLimiter:   clients.ConcurrencyLimiter,
		BuildSem:             make(chan struct{}, cfg.BuildConcurrency),
		ExcludeBranches:      cfg.ExcludeBranches,
		UnsupportedLanguages: cfg.UnsupportedLanguages,
		FastSync:             cfg.FastSync,
		MaxIssueComments:     cfg.MaxIssueComments,
		ProjectKeyPattern:    cfg.ProjectKeyPattern,
		Objects:              cfg.Objects,
		ProjectKeyRe:         projectKeyRe,
		BranchRe:             branchRe,
		MigrateHistory:       cfg.MigrateHistory,
		BranchAnalyzedAfter:  branchAnalyzedAfter,
		Logger:               logger,
	}

	// Overall progress/ETA logging (#520) — every 10s for the duration of
	// the run, stopped once phases finish (success or error).
	executor.Progress = common.NewTracker(logger, phases, CategorizeTask, common.DefaultCategoryWeights, common.ExpectedTaskDuration)

	// #554/#564: project-history replay runs inline inside importProjectData
	// rather than as its own TaskDef, so without this it's invisible to the
	// overall ETA. The point count is known upfront from already-extracted
	// data (no API calls), so give it its own tracked unit of work before a
	// single migrate task has even started. Left nil/unregistered when the
	// feature is off or there's nothing to replay — zero overhead otherwise.
	if totalPoints := projectHistoryPointTotal(executor); totalPoints > 0 {
		executor.HistoryProgress = common.NewProgressLogger(logger, "migrateProjectHistory", totalPoints)
		executor.Progress.Registry().Register("migrateProjectHistory", executor.HistoryProgress)
		executor.Progress.AddPseudoTask(common.CategoryProjectData, "migrateProjectHistory")
		executor.Progress.SetExpectedDuration("migrateProjectHistory",
			time.Duration(float64(totalPoints)*common.SecondsPerHistoryPoint*float64(time.Second)))
	}

	executor.Progress.OnUpdate(cfg.ProgressCallback)
	executor.Progress.Start(ctx, 10*time.Second)
	defer executor.Progress.Stop()

	// Dynamic concurrency re-evaluation (#573) — no-op when ConcurrencyLimiter
	// is fixed (--concurrency was explicitly set).
	executor.ConcurrencyLimiter.Start(ctx, 30*time.Second)
	defer executor.ConcurrencyLimiter.Stop()

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

	fmt.Printf("%s %s - Migration Complete: %s\n", version.ToolName, version.Version, runIDOut)
	return runIDOut, nil
}

// validateMigrateConfig validates the project-key renaming pattern syntax
// and the SQC org mapping, applying the --default_organization fallback if
// requested (issues #138, #279, #281). cc is used by applyOrgMapping to
// validate --default_organization against the live SonarQube Cloud API
// before it is ever persisted to organizations.csv (issue #550).
func validateMigrateConfig(ctx context.Context, cc *cloud.Client, cfg MigrateConfig, logger *slog.Logger) (appliedDefault bool, err error) {
	if err := ValidateProjectKeyPattern(cfg.ProjectKeyPattern); err != nil {
		return false, common.NewExitError(2, fmt.Errorf("invalid project_key_pattern: %w", err))
	}
	return applyOrgMapping(ctx, cc.Organizations, cfg.ExportDirectory, cfg.DefaultOrganization, cfg.EnterpriseKey, logger)
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
	Cloud              *cloud.Client
	CloudAPI           *cloud.Client
	Raw                *common.RawClient
	RawAPI             *common.RawClient
	CloudURL           string
	APIURL             string
	RateLimitTracker   *RateLimitTracker
	ConcurrencyLimiter *ConcurrencyLimiter
}

// newConcurrencyLimiter builds a ConcurrencyLimiter for a Cloud-facing
// executor (migrate, reset, sync-issues). Always dynamic (#573): starts at
// concurrency (whatever --concurrency / the config file's "concurrency"
// field resolved to, or the 25 default) and recalculates every 30s from
// observed API latency to target apiMaxRatePerMin calls/min — concurrency
// only seeds the starting point, it is never a permanent fixed cap.
func newConcurrencyLimiter(concurrency int, apiMaxRatePerMin int, logger *slog.Logger) *ConcurrencyLimiter {
	return NewDynamicConcurrencyLimiter(concurrency, apiMaxRatePerMin, logger)
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
	// One SlidingWindowLimiter and one ConcurrencyLimiter shared across
	// both cloudClient and apiClient below: both hosts share the same
	// egress IP and therefore the same 8000-calls/5min SonarQube Cloud
	// budget (#573).
	concurrencyLimiter := newConcurrencyLimiter(cfg.Concurrency, cfg.APIMaxRatePerMin, logger)
	apiRateLimiter := sqapi.NewSlidingWindowLimiter(cfg.APIMaxRatePerMin)
	clientOpts := []sqapi.Option{
		sqapi.WithTimeout(cfg.Timeout),
		sqapi.WithRetryLogger(retryLog),
		sqapi.WithRateLimitObserver(rateLimitObs),
		sqapi.WithRateLimitRecoveryLogger(rateLimitRecovery),
		sqapi.WithAPIRateLimiter(apiRateLimiter),
		sqapi.WithLatencyObserver(concurrencyLimiter.Observe),
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
		Cloud:              cloud.New(cloudClient),
		CloudAPI:           cloud.New(apiClient),
		Raw:                common.NewRawClient(cloudClient.HTTPClient(), cloudClient.BaseURL()),
		RawAPI:             common.NewRawClient(apiClient.HTTPClient(), apiClient.BaseURL()),
		CloudURL:           cloudClient.BaseURL(),
		APIURL:             apiClient.BaseURL(),
		RateLimitTracker:   rateLimitTracker,
		ConcurrencyLimiter: concurrencyLimiter,
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

	targets := MigrateTargetTasks(registry, cfg.TargetTask, MigrateTargetTasksFlags{SkipProfiles: cfg.SkipProfiles, IncludeProjectData: cfg.IncludeProjectData, SkipIssueSync: cfg.SkipIssueSync, SkipProjectDataMigration: cfg.SkipProjectDataMigration}, cfg.TargetTasks, cfg.Objects)
	var taskSet map[string]bool
	// Explicit overrides win over the --objects filter (see
	// MigrateTargetTasks' documented precedence): don't let the
	// exclusion set drop the very task the operator asked for.
	explicitOverride := cfg.TargetTask != "" || len(cfg.TargetTasks) > 0
	if cfg.Objects != nil && !explicitOverride {
		// #536: exclude cross-category dependency edges too — e.g.
		// setGlobalSettings/createPortfolios declaring createProjects as a
		// dependency must not force it to run when "projects" is excluded.
		taskSet = ResolveDependenciesExcluding(targets, registry, excludedMigrateTasks(cfg.Objects))
	} else {
		taskSet = ResolveDependencies(targets, registry)
	}
	if len(taskSet) == 0 {
		return nil, fmt.Errorf("no task left to run for the requested target/objects combination")
	}

	// #536: PlanPhasesExcluding degrades to plain PlanPhases when there
	// are no exclusions (cfg.Objects == nil), so it's safe to always
	// call it here.
	plan, err := PlanPhasesExcluding(taskSet, registry, excludedMigrateTasks(cfg.Objects))
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
// Combined with the per-task limit of e.ConcurrencyLimiter.Current() this
// bounds total in-flight requests at maxConcurrentTasksPerPhase * concurrency.
const maxConcurrentTasksPerPhase = 6

func runPhase(ctx context.Context, e *Executor, taskNames []string, registry map[string]*TaskDef, phaseIdx int, tm *RunTimings) error {
	phaseStart := time.Now()
	g, ctx := errgroup.WithContext(ctx)
	// Bound how many tasks in a phase run at once. Each task opens its
	// own errgroup limited to e.ConcurrencyLimiter.Current(), so an
	// unbounded phase multiplies that by the task count — a 14-task phase
	// at the default concurrency of 25 puts up to 350 requests in flight
	// against one host. Tasks stay concurrent (they are few and mostly
	// I/O bound), just not unboundedly so.
	g.SetLimit(maxConcurrentTasksPerPhase)
	for _, name := range taskNames {
		def := registry[name]
		e.Logger.Info("running task", "task", name)
		e.Progress.MarkTaskStarted(name)
		g.Go(func() error {
			taskStart := time.Now()
			counter := NewTaskCounter(name)
			taskCtx := WithTaskCounter(ctx, counter)
			runErr := def.Run(taskCtx, e)
			elapsed := time.Since(taskStart)
			// The counter is read before LogSummary so the recorded
			// outcome and the logged one come from the same snapshot.
			// OK means "did what it was asked", which a task that
			// returned nil while failing every item did not.
			outcome := counter.Outcome()
			tm.addTask(TaskTiming{
				Phase:              phaseIdx,
				Name:               name,
				Duration:           elapsed.Seconds(),
				StartedAt:          taskStart,
				OK:                 runErr == nil && outcome.Actionable() == 0,
				Err:                errString(runErr),
				Succeeded:          outcome.Succeeded,
				Failed:             outcome.Failed,
				ActionableFailures: outcome.Actionable(),
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
	if cfg.APIMaxRatePerMin <= 0 {
		cfg.APIMaxRatePerMin = 1500
	}
	if cfg.BuildConcurrency <= 0 {
		cfg.BuildConcurrency = AdaptiveBuildConcurrency()
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60
	}
	if cfg.MaxIssueComments == 0 {
		cfg.MaxIssueComments = DefaultMaxIssueComments
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
