// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"golang.org/x/sync/errgroup"
)

// sortSpec describes how a task's items should be ordered before iteration
// (#326). orgField names the JSON field used to bucket items by SonarCloud
// org so each org's objects are processed contiguously — empty means
// enterprise-wide, no bucketing. sortField names the JSON field used to
// alphabetize items within each bucket.
type sortSpec struct {
	orgField  string
	sortField string
}

// taskSortSpecs registers per-task ordering for the centralized iteration
// helpers. Tasks not listed here keep their input order (no-op sort).
//
// Within each entry the chosen sortField is the operator-visible identifier:
// project key for projects, name for groups / profiles / gates / portfolios
// / permission templates. Org-scoped objects are bucketed by
// sonarcloud_org_key first; portfolios are enterprise-wide so they sort
// purely by name. Extract-driven tasks read records that don't carry the
// org key, so they sort by projectKey alone — the within-org alphabetical
// order is preserved as a sub-sequence.
var taskSortSpecs = map[string]sortSpec{
	// Migrate-driven (records carry sonarcloud_org_key).
	"createProjects":            {orgField: "sonarcloud_org_key", sortField: "cloud_project_key"},
	"setProjectGates":           {orgField: "sonarcloud_org_key", sortField: "cloud_project_key"},
	"setProjectBinding":         {orgField: "sonarcloud_org_key", sortField: "cloud_project_key"},
	"createProfiles":            {orgField: "sonarcloud_org_key", sortField: "name"},
	"createGates":               {orgField: "sonarcloud_org_key", sortField: "name"},
	"createGroups":              {orgField: "sonarcloud_org_key", sortField: "name"},
	"createPermissionTemplates": {orgField: "sonarcloud_org_key", sortField: "name"},
	"setDefaultProfiles":        {orgField: "sonarcloud_org_key", sortField: "name"},
	"setDefaultGates":           {orgField: "sonarcloud_org_key", sortField: "name"},
	"setDefaultTemplates":       {orgField: "sonarcloud_org_key", sortField: "name"},
	"syncIssueMetadata":         {orgField: "sonarcloud_org_key", sortField: "cloud_project_key"},
	"syncHotspotMetadata":       {orgField: "sonarcloud_org_key", sortField: "cloud_project_key"},
	"importProjectData":         {orgField: "sonarcloud_org_key", sortField: "cloud_project_key"},
	// Enterprise-wide (no org bucketing).
	"createPortfolios":    {sortField: "name"},
	"configurePortfolios": {sortField: "name"},
	// Extract-driven tasks: records carry projectKey but no org key.
	"setProjectProfiles":         {sortField: "projectKey"},
	"setProjectGroupPermissions": {sortField: "projectKey"},
	"setProjectSettings":         {sortField: "projectKey"},
	"setProjectTags":             {sortField: "projectKey"},
	"setProjectLinks":            {sortField: "projectKey"},
	"setProjectWebhooks":         {sortField: "projectKey"},
	"setNewCodePeriods":          {sortField: "projectKey"},
}

// sortMigrateItems orders items per the task's sortSpec. Stable, in-place;
// a no-op for tasks without a spec.
func sortMigrateItems(taskName string, items []json.RawMessage) {
	spec, ok := taskSortSpecs[taskName]
	if !ok {
		return
	}
	slices.SortStableFunc(items, func(a, b json.RawMessage) int {
		if spec.orgField != "" {
			if c := strings.Compare(extractField(a, spec.orgField), extractField(b, spec.orgField)); c != 0 {
				return c
			}
		}
		return strings.Compare(extractField(a, spec.sortField), extractField(b, spec.sortField))
	})
}

// sortExtractItems orders extract items per the task's sortSpec. Stable,
// in-place; a no-op for tasks without a spec. Extract records don't carry
// the org key, so spec.orgField (when set) is ignored here.
func sortExtractItems(taskName string, items []structure.ExtractItem) {
	spec, ok := taskSortSpecs[taskName]
	if !ok {
		return
	}
	slices.SortStableFunc(items, func(a, b structure.ExtractItem) int {
		return strings.Compare(extractField(a.Data, spec.sortField), extractField(b.Data, spec.sortField))
	})
}

// readExtractItems reads JSONL items from an extract task across all extract runs.
func readExtractItems(e *Executor, taskKey string) ([]structure.ExtractItem, error) {
	return structure.ReadExtractData(e.ExportDir, e.Mapping, taskKey)
}

// forEachMigrateItem reads items from a completed migrate task and calls fn
// for each, concurrently bounded by semaphore.
func forEachMigrateItem(ctx context.Context, e *Executor, taskName, depTask string,
	fn func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error) error {

	return forEachMigrateItemFiltered(ctx, e, taskName, depTask, nil, fn)
}

// forEachMigrateItemFiltered is like forEachMigrateItem with an optional filter.
func forEachMigrateItemFiltered(ctx context.Context, e *Executor, taskName, depTask string,
	filterFn func(json.RawMessage) bool,
	fn func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error) error {

	return forEachMigrateItemImpl(ctx, e, taskName, depTask, filterFn, cap(e.Sem), fn)
}

// forEachMigrateItemSerial is forEachMigrateItemFiltered with concurrency
// pinned to 1, so each item is fully processed before the next starts.
// Used by createProfiles (#338): SonarCloud quality-profile creation is
// asynchronous but the name must be unique, so two parallel POSTs for
// the same (name, language) can both succeed at the API layer and then
// fail the uniqueness check at the database layer. Serial processing
// is cheap — typical migrations create <30 profiles.
func forEachMigrateItemSerial(ctx context.Context, e *Executor, taskName, depTask string,
	filterFn func(json.RawMessage) bool,
	fn func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error) error {

	return forEachMigrateItemImpl(ctx, e, taskName, depTask, filterFn, 1, fn)
}

// forEachMigrateItemImpl is the shared body that backs the concurrent
// and serial migrate iterators. `concurrency` is the errgroup limit
// (pass 1 to serialize, or cap(e.Sem) for the default fan-out).
func forEachMigrateItemImpl(ctx context.Context, e *Executor, taskName, depTask string,
	filterFn func(json.RawMessage) bool,
	concurrency int,
	fn func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error) error {

	items, err := e.Store.ReadAll(depTask)
	if err != nil {
		return fmt.Errorf("%s: reading %s: %w", taskName, depTask, err)
	}

	// Pre-filter to get accurate count for progress logging.
	var filtered []json.RawMessage
	for _, item := range items {
		if filterFn == nil || filterFn(item) {
			filtered = append(filtered, item)
		}
	}

	// Order items so the log stream reflects alphabetical progress within
	// each org (#326). No-op for tasks not in the sort registry.
	sortMigrateItems(taskName, filtered)

	e.Logger.Info("starting task", "task", taskName, "items", len(filtered))
	prog := common.NewProgressLogger(e.Logger, taskName, len(filtered))

	w, err := e.Store.Writer(taskName)
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)
	for _, item := range filtered {
		g.Go(func() error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			err := fn(ctx, item, w)
			prog.Increment()
			return err
		})
	}
	return g.Wait()
}

// forEachExtractItem reads items from an extract task and calls fn for each,
// concurrently bounded by semaphore. Unlike forEachMigrateItem which reads from
// the migrate store, this reads from extract data across all extract runs.
func forEachExtractItem(ctx context.Context, e *Executor, taskName, extractKey string,
	fn func(ctx context.Context, item structure.ExtractItem, w *common.ChunkWriter) error) error {

	items, err := readExtractItems(e, extractKey)
	if err != nil {
		return fmt.Errorf("%s: reading %s: %w", taskName, extractKey, err)
	}

	// Order items so the log stream reflects alphabetical progress (#326).
	// No-op for tasks not in the sort registry.
	sortExtractItems(taskName, items)

	e.Logger.Info("starting task", "task", taskName, "items", len(items))
	prog := common.NewProgressLogger(e.Logger, taskName, len(items))

	w, err := e.Store.Writer(taskName)
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))
	for _, item := range items {
		g.Go(func() error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			err := fn(ctx, item, w)
			prog.Increment()
			return err
		})
	}
	return g.Wait()
}

// buildOrgKeyLookup loads organizations.csv and returns a map from
// sonarqube_org_key to sonarcloud_org_key.
func buildOrgKeyLookup(exportDir string) (map[string]string, error) {
	rows, err := structure.LoadCSV(exportDir, "organizations.csv")
	if err != nil {
		return nil, err
	}
	lookup := make(map[string]string, len(rows))
	for _, row := range rows {
		sqKey, _ := row["sonarqube_org_key"].(string)
		scKey, _ := row["sonarcloud_org_key"].(string)
		if sqKey != "" {
			lookup[sqKey] = scKey
		}
	}
	return lookup, nil
}

// loadCSVToJSONL reads a CSV file and writes each row as a JSONL object
// to the task output. Used by generate*Mappings tasks.
// It enriches each row with sonarcloud_org_key by joining on sonarqube_org_key
// from organizations.csv.
func loadCSVToJSONL(e *Executor, taskName, csvFilename string) error {
	rows, err := structure.LoadCSV(e.ExportDir, csvFilename)
	if err != nil {
		return fmt.Errorf("%s: loading %s: %w", taskName, csvFilename, err)
	}

	orgLookup, err := buildOrgKeyLookup(e.ExportDir)
	if err != nil {
		return fmt.Errorf("%s: loading organizations.csv for join: %w", taskName, err)
	}

	w, err := e.Store.Writer(taskName)
	if err != nil {
		return err
	}

	items := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		// Enrich with sonarcloud_org_key from org lookup.
		if sqKey, ok := row["sonarqube_org_key"].(string); ok && sqKey != "" {
			if scKey, found := orgLookup[sqKey]; found {
				row["sonarcloud_org_key"] = scKey
			}
		}
		// #381: in reset mode, rows whose cloud org wasn't confirmed
		// by the operator get their sonarcloud_org_key rewritten to
		// the SKIPPED sentinel so the existing shouldSkipOrg check at
		// every per-org reset/delete task site naturally excludes
		// them. A nil map = not in reset mode = no rewrite.
		if e.ResetConfirmedOrgs != nil {
			scKey, _ := row["sonarcloud_org_key"].(string)
			if !e.ResetConfirmedOrgs[scKey] {
				row["sonarcloud_org_key"] = skippedOrgSentinel
			}
		}
		b, err := json.Marshal(row)
		if err != nil {
			continue
		}
		items = append(items, b)
	}
	return w.WriteChunk(items)
}

// buildServerOrgLookup returns a map from server URL to SonarCloud org key
// using the generateOrganizationMappings migrate output.
func buildServerOrgLookup(e *Executor) map[string]string {
	orgItems, _ := e.Store.ReadAll("generateOrganizationMappings")
	orgKeys := make(map[string]string, len(orgItems))
	for _, o := range orgItems {
		serverURL := extractField(o, "server_url")
		cloudKey := extractField(o, "sonarcloud_org_key")
		orgKeys[serverURL] = cloudKey
	}
	return orgKeys
}

// Unsupported languages that are filtered during migration.
var unsupportedLanguages = map[string]bool{
	"c++": true, "grvy": true, "ps": true,
}

// validPermissions for project group permissions.
var validPermissions = map[string]bool{
	"admin": true, "codeviewer": true, "issueadmin": true,
	"securityhotspotadmin": true, "scan": true, "user": true,
}

// skippedOrgSentinel is the marker value for organizations that should be
// excluded from migration (user chose to skip them during the wizard).
const skippedOrgSentinel = "SKIPPED"

// shouldSkipOrg returns true if the org key is empty or marked SKIPPED.
func shouldSkipOrg(orgKey string) bool {
	return orgKey == "" || orgKey == skippedOrgSentinel
}

// logAPIWarn logs an API error with structured fields. If the error is an
// APIError, it extracts the human-readable message, status code, and endpoint.
func logAPIWarn(logger *slog.Logger, msg string, err error, attrs ...any) {
	var apiErr *sqapi.APIError
	if errors.As(err, &apiErr) {
		attrs = append(attrs,
			"err", apiErr.Message(),
			"status", apiErr.StatusCode,
			"endpoint", apiErr.Endpoint(),
		)
	} else {
		attrs = append(attrs, "err", err)
	}
	logger.Warn(msg, attrs...)
}

// TaskCounter tracks success/failure counts for a task. Safe for concurrent use.
type TaskCounter struct {
	succeeded atomic.Int64
	failed    atomic.Int64
	task      string
}

// NewTaskCounter creates a counter for tracking task operation results.
func NewTaskCounter(task string) *TaskCounter {
	return &TaskCounter{task: task}
}

// taskCounterCtxKey scopes the per-task counter inside the task's
// context (#333). runPhase injects a fresh counter so the merged
// "task summary" log can be emitted from a single place after the
// task returns.
type taskCounterCtxKey struct{}

// WithTaskCounter returns a child context carrying the given counter.
func WithTaskCounter(ctx context.Context, c *TaskCounter) context.Context {
	return context.WithValue(ctx, taskCounterCtxKey{}, c)
}

// TaskCounterFromContext returns the counter injected by runPhase, or
// a throwaway counter if none is present (so tests and ad-hoc Run
// invocations that bypass runPhase still compile and run).
func TaskCounterFromContext(ctx context.Context) *TaskCounter {
	if c, ok := ctx.Value(taskCounterCtxKey{}).(*TaskCounter); ok && c != nil {
		return c
	}
	return NewTaskCounter("")
}

// Success increments the success count.
func (c *TaskCounter) Success() { c.succeeded.Add(1) }

// Fail increments the failure count.
func (c *TaskCounter) Fail() { c.failed.Add(1) }

// LogSummary emits the end-of-task INFO log. When the counter saw at
// least one Success/Fail it logs a "task summary" line that carries
// both the counts and the elapsed duration (#333 — merged from the
// previously-separate "Task X: Duration ..." line). When the counter
// is empty (setup-style tasks that don't track per-item outcomes), it
// falls back to the plain duration line so every task still ends with
// exactly one closing log entry.
func (c *TaskCounter) LogSummary(logger *slog.Logger, duration time.Duration) {
	s, f := c.succeeded.Load(), c.failed.Load()
	total := s + f
	if total == 0 {
		common.LogTaskDuration(logger, c.task, duration)
		return
	}
	logger.Info("task summary",
		"task", c.task,
		"succeeded", s,
		"failed", f,
		"total", total,
		"duration", common.FormatHMSMillis(duration),
	)
}

// Progress logging is shared with the extract package via
// common.ProgressLogger (moved out of this file in #340 so the same
// helper covers both extract and migrate tasks).

// runProjectSyncLoop applies fn concurrently to every item in items,
// bounded by e.Sem, emitting a "<label> n/total - x%" progress line
// every `interval` completions (#300). Per-item errors are not
// propagated — the caller's `apply` is responsible for logging and
// counter bookkeeping. Used by syncProjectIssues / syncProjectHotspots
// to share the actionable-pair iteration shape exactly.
func runProjectSyncLoop[T any](
	ctx context.Context, e *Executor,
	items []T, label string, interval int64,
	apply func(ctx context.Context, item T),
) {
	prog := common.NewProgressLoggerWithInterval(e.Logger, label, len(items), interval)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))
	for _, item := range items {
		g.Go(func() error {
			if gctx.Err() != nil {
				return nil
			}
			apply(gctx, item)
			prog.Increment()
			return nil
		})
	}
	_ = g.Wait()
}

// extractField is a convenience alias.
var extractField = common.ExtractField

// extractBool is a convenience alias.
var extractBool = common.ExtractBool
