// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/analysis"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
)

// targetMetricRe extracts metric names from the slog-formatted
// target_metrics attribute. That attribute arrives as the Go %v rendering
// of a []condition slice, e.g.
//
//	"[{Metric:new_maintainability_rating Op: Error:} {Metric:reliability_rating Op:GT Error:1}]"
//
// so the clean metric name(s) have to be teased out of the struct dump.
var targetMetricRe = regexp.MustCompile(`Metric:([A-Za-z0-9_]+)`)

// parseTargetMetrics returns a comma-separated list of the SonarQube Cloud
// target metric name(s) from the slog target_metrics attribute. The attribute
// is logged via slog.Any over a []condition slice, so in run_events.jsonl it
// is a JSON array of objects each carrying a "Metric" field — that is the
// primary path. A string form (slog's %v struct-dump, used if the value was
// ever logged as text) is parsed with targetMetricRe as a fallback. Anything
// unrecognized yields "" so the column is simply blank rather than noisy.
func parseTargetMetrics(v any) string {
	switch tv := v.(type) {
	case []any:
		names := make([]string, 0, len(tv))
		for _, el := range tv {
			if m, ok := el.(map[string]any); ok {
				if name, ok := m["Metric"].(string); ok && name != "" {
					names = append(names, name)
				}
			}
		}
		return strings.Join(names, ", ")
	case string:
		matches := targetMetricRe.FindAllStringSubmatch(tv, -1)
		if len(matches) == 0 {
			return tv
		}
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m[1])
		}
		return strings.Join(names, ", ")
	default:
		return ""
	}
}

// runtimeData carries the migrate-engine telemetry harvested from the
// run directory's run_meta.json / run_events.jsonl plus the failure
// rows from requests.log. The fields mirror the runtime fields appended
// to MigrationSummary (shared contract C) so collectRuntime's output can
// be copied onto a MigrationSummary verbatim.
type runtimeData struct {
	StartedAt     time.Time
	CompletedAt   time.Time
	TotalElapsed  time.Duration
	OverallStatus string
	Phases        []PhaseTiming
	Tasks         []TaskTiming
	Failures      []FailureRow
	FailureCauses []FailureCause
	Warnings      WarningLedger
	Branches      []BranchStat
	Throughput    ThroughputStats

	// ProjectKeyPattern is the target-key renaming pattern recorded in
	// run_meta.json, used to re-derive project keys for the collision /
	// over-length report (issue #138).
	ProjectKeyPattern string
}

// runMetaFile mirrors the on-disk run_meta.json written by the migrate
// engine (shared contract A). Decoded locally rather than by reusing the
// migrate type, so the on-disk shape is pinned here and a change on the
// writing side shows up as a decode mismatch instead of compiling silently.
type runMetaFile struct {
	StartedAt         time.Time      `json:"started_at"`
	CompletedAt       time.Time      `json:"completed_at"`
	OverallStatus     string         `json:"overall_status"`
	Phases            []runMetaPhase `json:"phases"`
	Tasks             []runMetaTask  `json:"tasks"`
	ProjectKeyPattern string         `json:"project_key_pattern"`
}

type runMetaPhase struct {
	Index           int     `json:"index"`
	Tasks           int     `json:"tasks"`
	DurationSeconds float64 `json:"duration_seconds"`
}

type runMetaTask struct {
	Phase           int       `json:"phase"`
	Name            string    `json:"name"`
	DurationSeconds float64   `json:"duration_seconds"`
	StartedAt       time.Time `json:"started_at"`
	OK              bool      `json:"ok"`
	Err             string    `json:"err"`

	// Per-item tallies (absent in runs recorded before they were
	// written, which decode to zero and render as they always did).
	Succeeded          int64 `json:"succeeded"`
	Failed             int64 `json:"failed"`
	ActionableFailures int64 `json:"actionable_failures"`
}

// logEventLine mirrors one run_events.jsonl record (shared contract A).
// attrs decode into a generic map; numeric attrs arrive as float64.
type logEventLine struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs"`
}

// collectRuntime reads the migrate-engine telemetry from runDir. It is
// tolerant of every file being absent: missing run_meta.json,
// run_events.jsonl and requests.log each yield zero-value contributions
// rather than an error, so predictive reports (which have none of these)
// degrade cleanly to empty runtime sections.
func collectRuntime(runDir string) (runtimeData, error) {
	var rt runtimeData

	collectRunMeta(runDir, &rt)
	collectRunEvents(runDir, &rt)
	collectFailureRows(runDir, &rt)

	return rt, nil
}

// collectRunMeta parses run_meta.json into the phase/task timings and the
// run-level status/timestamps. Absent file => no-op.
func collectRunMeta(runDir string, rt *runtimeData) {
	data, err := os.ReadFile(filepath.Join(runDir, "run_meta.json"))
	if err != nil {
		return
	}
	var meta runMetaFile
	if err := json.Unmarshal(data, &meta); err != nil {
		return
	}

	rt.StartedAt = meta.StartedAt
	rt.CompletedAt = meta.CompletedAt
	rt.OverallStatus = meta.OverallStatus
	rt.ProjectKeyPattern = meta.ProjectKeyPattern
	if !meta.StartedAt.IsZero() && !meta.CompletedAt.IsZero() {
		rt.TotalElapsed = meta.CompletedAt.Sub(meta.StartedAt)
	}

	for _, p := range meta.Phases {
		rt.Phases = append(rt.Phases, PhaseTiming{
			Phase:    fmt.Sprintf("Phase %d", p.Index),
			Tasks:    p.Tasks,
			Duration: secondsToDuration(p.DurationSeconds),
		})
	}
	for _, t := range meta.Tasks {
		rt.Tasks = append(rt.Tasks, TaskTiming{
			Phase:              t.Phase,
			Task:               t.Name,
			Duration:           secondsToDuration(t.DurationSeconds),
			StartedAt:          t.StartedAt,
			OK:                 t.OK,
			Err:                t.Err,
			Succeeded:          t.Succeeded,
			Failed:             t.Failed,
			ActionableFailures: t.ActionableFailures,
		})
	}

	// Slowest-first, stable so equal durations keep their input order.
	sort.SliceStable(rt.Phases, func(i, j int) bool {
		return rt.Phases[i].Duration > rt.Phases[j].Duration
	})
	sort.SliceStable(rt.Tasks, func(i, j int) bool {
		return rt.Tasks[i].Duration > rt.Tasks[j].Duration
	})
}

// branchKey identifies one branch of one project.
//
// The project is part of the key because branch names are not unique
// across a migration — nearly every project has a "main". Keying on the
// bare name merged them all into a single row.
type branchKey struct {
	project string
	branch  string
}

// eventAggregator holds the in-order, deterministic state accumulated
// while streaming run_events.jsonl. Retries are keyed by method+endpoint
// (with retryOrder preserving first-seen order before the final stable
// Count-descending sort); branches are keyed by project+branch.
type eventAggregator struct {
	retries    map[string]*RetryStat
	retryOrder []string
	branches   map[branchKey]*BranchStat

	// aliases redirects one name for a branch onto the key its row
	// actually lives under. A project's main branch is packaged under its
	// source name but submitted under the target name, then renamed to
	// match the source once imported — so one branch arrives under two
	// names. Without the redirect it produced two rows: one holding the
	// issue and component counts, the other holding only the CE task id.
	aliases map[branchKey]branchKey

	// selfNamed holds the names a packaged event claimed as its OWN
	// source branch. Such a name belongs to a branch in its own right
	// and must never be redirected: a project can have both a "master"
	// (its main) and a separate "main", and the alias registered for the
	// main branch's rename would otherwise swallow the real "main".
	selfNamed map[branchKey]bool
}

func newEventAggregator() *eventAggregator {
	return &eventAggregator{
		retries:  map[string]*RetryStat{},
		branches: map[branchKey]*BranchStat{},
		aliases:  map[branchKey]branchKey{},

		selfNamed: map[branchKey]bool{},
	}
}

// branchFor returns the row for one branch of one project, following any
// alias registered for it and creating the row on first use so events
// that arrive in any order share it.
func (agg *eventAggregator) branchFor(project, branch string) *BranchStat {
	k := branchKey{project: project, branch: branch}
	if canonical, ok := agg.aliases[k]; ok {
		k = canonical
	}
	bs, ok := agg.branches[k]
	if !ok {
		bs = &BranchStat{Project: k.project, Branch: k.branch}
		agg.branches[k] = bs
	}
	return bs
}

// claimBranchName records that a packaged event named `branch` as its own
// source branch, dropping any alias already pointing that name elsewhere.
//
// The main branch is packaged under its source name but submitted under
// SonarQube Cloud's, so the two are aliased together. When a project's
// source main is "master" while SonarQube Cloud's is "main", that alias
// is main->master — and a second, genuinely distinct branch called "main"
// then resolved through it, overwriting the main branch's issue and
// component counts and emitting no row of its own.
func (agg *eventAggregator) claimBranchName(project, branch string) {
	if branch == "" {
		return
	}
	k := branchKey{project: project, branch: branch}
	agg.selfNamed[k] = true
	delete(agg.aliases, k)
}

// aliasBranch records that `from` and `to` name the same branch of
// `project`, folding an already-collected `from` row into `to`.
//
// Safe in either event order: called before the row exists it just
// registers the redirect, and called after it merges what was collected
// under the other name.
func (agg *eventAggregator) aliasBranch(project, from, to string) {
	if from == "" || to == "" || from == to {
		return
	}
	fromKey := branchKey{project: project, branch: from}
	// Some packaged event named `from` as its own source branch, so it is
	// a branch of its own and keeps its own row. Redirecting it here is
	// how the row collapse this keying fixes came back one level down.
	if agg.selfNamed[fromKey] {
		return
	}
	toKey := branchKey{project: project, branch: to}
	agg.aliases[fromKey] = toKey

	stale, ok := agg.branches[fromKey]
	if !ok {
		return
	}
	delete(agg.branches, fromKey)
	if target, ok := agg.branches[toKey]; ok {
		mergeBranchStat(target, stale)
		return
	}
	stale.Project, stale.Branch = project, to
	agg.branches[toKey] = stale
}

// mergeBranchStat folds src into dst, keeping whatever dst already knows.
// Only ever called for two names of one branch, where in practice each
// field was carried by exactly one of them.
func mergeBranchStat(dst, src *BranchStat) {
	if dst.Type == "" {
		dst.Type = src.Type
	}
	if dst.TaskID == "" {
		dst.TaskID = src.TaskID
	}
	if dst.SkipReason == "" {
		dst.SkipReason = src.SkipReason
	}
	if dst.Issues == 0 {
		dst.Issues = src.Issues
	}
	if dst.ExternalIssues == 0 {
		dst.ExternalIssues = src.ExternalIssues
	}
	if dst.Components == 0 {
		dst.Components = src.Components
	}
	if dst.ActiveRules == 0 {
		dst.ActiveRules = src.ActiveRules
	}
	if dst.ZipBytes == 0 {
		dst.ZipBytes = src.ZipBytes
	}
	// "packaged" is the stronger statement and wins, the same precedence
	// applyTaskSubmitted has always applied.
	if dst.Status != "packaged" && src.Status != "" {
		dst.Status = src.Status
	}
}

// apply routes a single event to the matching handler. Splitting the
// dispatch into focused handlers keeps each one simple and the overall
// flow easy to follow.
func (agg *eventAggregator) apply(ev logEventLine, rt *runtimeData) {
	switch {
	case ev.Message == "retrying request":
		agg.applyRetry(ev, rt)
	// Two prefixes because the engine's wording changed to "migrating
	// branch without source: ..." while this matcher still only knew the
	// original "skipping branch: ...", so the skip ledger silently stopped
	// being populated by real runs. Both are accepted rather than swapping
	// one for the other, so reports over older run directories keep working.
	case strings.HasPrefix(ev.Message, "skipping branch: source code not retrievable"),
		strings.HasPrefix(ev.Message, "migrating branch without source: source code not retrievable"):
		agg.applyBranchSkip(ev, rt)
	case strings.HasPrefix(ev.Message, "addGateConditions: source metric has no SonarQube Cloud equivalent"):
		agg.applyGateConditionSkip(ev, rt)
	case strings.HasPrefix(ev.Message, "addGateConditions: source metric remapped"):
		agg.applyGateConditionRemap(ev, rt)
	case ev.Message == "report packaged":
		agg.applyReportPackaged(ev)
	case ev.Message == "CE task submitted":
		agg.applyTaskSubmitted(ev)
	case ev.Message == "analysis pre-created (branch anchored on target)":
		agg.applyAnalysisPreCreated(ev)
	}
}

func (agg *eventAggregator) applyRetry(ev logEventLine, rt *runtimeData) {
	a := ev.Attrs
	method := evStr(a, "method")
	endpoint := evStr(a, "endpoint")
	key := method + " " + endpoint
	rs, ok := agg.retries[key]
	if !ok {
		rs = &RetryStat{Method: method, Endpoint: endpoint}
		agg.retries[key] = rs
		agg.retryOrder = append(agg.retryOrder, key)
	}
	rs.Count++
	if attempt := evInt(a, "attempt"); attempt > rs.MaxAttempt {
		rs.MaxAttempt = attempt
	}
	rs.LastStatus = evScalarStr(a, "status")
	rt.Throughput.TotalRetries++
}

func (agg *eventAggregator) applyBranchSkip(ev logEventLine, rt *runtimeData) {
	a := ev.Attrs
	branch := evStr(a, "branch")
	rt.Warnings.BranchSkips = append(rt.Warnings.BranchSkips, BranchSkip{
		Branch:   branch,
		Findings: evInt(a, "findings"),
		Reason:   ev.Message,
	})
	bs := agg.branchFor(evStr(a, "project"), branch)
	bs.Status = "skipped"
	bs.SkipReason = ev.Message
}

func (agg *eventAggregator) applyGateConditionSkip(ev logEventLine, rt *runtimeData) {
	a := ev.Attrs
	rt.Warnings.GateConditions = append(rt.Warnings.GateConditions, GateConditionSkip{
		Gate:   evStr(a, "gate"),
		Metric: evStr(a, "metric"),
		Action: "skipped",
		Note:   ev.Message,
	})
}

func (agg *eventAggregator) applyGateConditionRemap(ev logEventLine, rt *runtimeData) {
	a := ev.Attrs
	gate := evStr(a, "gate")
	sourceMetric := evStr(a, "source_metric")
	rt.Warnings.GateConditions = append(rt.Warnings.GateConditions, GateConditionSkip{
		Gate:   gate,
		Metric: sourceMetric,
		Action: "remapped",
		Note:   ev.Message,
	})
	rt.Warnings.MetricRemaps = append(rt.Warnings.MetricRemaps, MetricRemap{
		Gate:         gate,
		SourceMetric: sourceMetric,
		TargetMetric: parseTargetMetrics(a["target_metrics"]),
	})
}

func (agg *eventAggregator) applyReportPackaged(ev logEventLine) {
	a := ev.Attrs
	project := evStr(a, "project")
	target := evStr(a, "targetBranch")
	// Canonicalize on the source branch name: that is the name the branch
	// ends up carrying on the target, because the main branch is renamed
	// to match the source once its data is imported. The target name is
	// aliased onto it so the CE submission, which only knows the target
	// name, lands on this same row.
	branch := firstNonEmpty(evStr(a, "sourceBranch"), target, project)
	// Claimed before the alias is registered, so this branch keeps its
	// own row whichever order the two events arrive in.
	agg.claimBranchName(project, branch)
	agg.aliasBranch(project, target, branch)

	bs := agg.branchFor(project, branch)
	bs.Issues = evInt(a, "issues")
	bs.ExternalIssues = evInt(a, "externalIssues")
	bs.Components = evInt(a, "components")
	bs.ActiveRules = evInt(a, "activeRules")
	bs.ZipBytes = evInt64(a, "zipSizeBytes")
	bs.Status = "packaged"
}

func (agg *eventAggregator) applyTaskSubmitted(ev logEventLine) {
	a := ev.Attrs
	bs := agg.branchFor(evStr(a, "project"), evStr(a, "targetBranch"))
	bs.TaskID = evStr(a, "taskId")
	if bs.Status != "packaged" {
		bs.Status = "submitted"
	}
}

func (agg *eventAggregator) applyAnalysisPreCreated(ev logEventLine) {
	a := ev.Attrs
	bs := agg.branchFor(evStr(a, "project"), evStr(a, "branch"))
	bs.Type = evStr(a, "branchType")
}

// collectRunEvents streams run_events.jsonl line-by-line, aggregating
// retries, branch skips, gate-condition decisions, metric remaps,
// per-branch stats and the run-wide throughput totals. Absent file or
// unparseable lines are skipped silently.
func collectRunEvents(runDir string, rt *runtimeData) {
	f, err := os.Open(filepath.Join(runDir, "run_events.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()

	agg := newEventAggregator()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10 MB max line
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev logEventLine
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		agg.apply(ev, rt)
	}

	// Retries: stable Count-descending order.
	for _, key := range agg.retryOrder {
		rt.Warnings.Retries = append(rt.Warnings.Retries, *agg.retries[key])
	}
	sort.SliceStable(rt.Warnings.Retries, func(i, j int) bool {
		return rt.Warnings.Retries[i].Count > rt.Warnings.Retries[j].Count
	})

	// Branches: grouped by project, ascending by branch within it, so a
	// multi-project run reads project by project.
	for _, bs := range agg.branches {
		rt.Branches = append(rt.Branches, *bs)
	}
	sort.Slice(rt.Branches, func(i, j int) bool {
		if rt.Branches[i].Project != rt.Branches[j].Project {
			return rt.Branches[i].Project < rt.Branches[j].Project
		}
		return rt.Branches[i].Branch < rt.Branches[j].Branch
	})
	for _, bs := range rt.Branches {
		rt.Throughput.TotalIssues += bs.Issues
		rt.Throughput.TotalExternalIssues += bs.ExternalIssues
		rt.Throughput.TotalComponents += bs.Components
		rt.Throughput.TotalZipBytes += bs.ZipBytes
		switch bs.Status {
		case "packaged":
			rt.Throughput.BranchesPackaged++
		case "skipped":
			rt.Throughput.BranchesSkipped++
		}
		if bs.TaskID != "" {
			rt.Throughput.TasksSubmitted++
		}
	}
}

// collectFailureRows reads requests.log via the analysis parser and keeps
// only the failures, sorted by entity type then name.
func collectFailureRows(runDir string, rt *runtimeData) {
	rows, _ := analysis.ParseRequestsLog(runDir)
	for _, row := range rows {
		if row.Outcome != "failure" {
			continue
		}
		// Classify with the same rules the run used, so the report explains
		// each failure instead of reprinting a raw API message.
		status, _ := strconv.Atoi(row.HTTPStatus)
		v := migrate.ClassifyHTTPFailure(status, row.ErrorMessage)
		rt.Failures = append(rt.Failures, FailureRow{
			EntityType:   row.EntityType,
			EntityName:   row.EntityName,
			Organization: row.Organization,
			URL:          row.URL,
			HTTPStatus:   row.HTTPStatus,
			ErrorMessage: row.ErrorMessage,
			Project:      row.Project,
			Cause:        string(v.Class),
			Why:          v.Why,
			Remediation:  v.Remediation,
			Reportable:   v.Reportable,
		})
	}
	sort.SliceStable(rt.Failures, func(i, j int) bool {
		if rt.Failures[i].EntityType != rt.Failures[j].EntityType {
			return rt.Failures[i].EntityType < rt.Failures[j].EntityType
		}
		if rt.Failures[i].EntityName != rt.Failures[j].EntityName {
			return rt.Failures[i].EntityName < rt.Failures[j].EntityName
		}
		return rt.Failures[i].Project < rt.Failures[j].Project
	})
	rt.FailureCauses = groupFailureCauses(rt.Failures)
}

// maxCauseSampleEntities caps how many entity names a cause lists. The point
// is orientation, not an inventory — the full set is in the ledger table.
const maxCauseSampleEntities = 5

// groupFailureCauses collapses the ledger into one entry per distinct
// explanation, most frequent first, so a run with thousands of identical
// failures explains itself in a handful of lines instead of repeating the
// same prose on every row. A customer run produced 42,048 failures from a
// single cause.
//
// Grouped on (class, explanation) rather than class alone. One class can
// carry several genuinely different explanations — "not allowed to use
// private projects", "not allowed to use custom permission templates" and
// "lacks permission to modify quality gates" are all environment problems
// with different fixes — and keying on the class alone would show whichever
// one happened to be read first and silently misdescribe the rest.
func groupFailureCauses(rows []FailureRow) []FailureCause {
	if len(rows) == 0 {
		return nil
	}
	type causeKey struct{ class, why string }
	order := make([]causeKey, 0, 4)
	byCause := make(map[causeKey]*FailureCause, 4)
	for _, r := range rows {
		if r.Cause == "" {
			continue
		}
		k := causeKey{r.Cause, r.Why}
		c, ok := byCause[k]
		if !ok {
			c = &FailureCause{
				Cause: r.Cause, Why: r.Why,
				Remediation: r.Remediation, Reportable: r.Reportable,
			}
			byCause[k] = c
			order = append(order, k)
		}
		c.Count++
		if len(c.Entities) < maxCauseSampleEntities && r.EntityName != "" {
			c.Entities = append(c.Entities, r.EntityName)
		}
	}
	out := make([]FailureCause, 0, len(order))
	for _, k := range order {
		out = append(out, *byCause[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// secondsToDuration converts a float seconds value to a time.Duration
// without losing sub-second precision.
func secondsToDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

// firstNonEmpty returns the first non-empty string from its arguments,
// or "" when all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// evStr reads a string attr. JSON strings decode as string; anything else
// returns "".
func evStr(attrs map[string]any, key string) string {
	if attrs == nil {
		return ""
	}
	if v, ok := attrs[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// evScalarStr reads an attr that may have been logged as either a string
// or a number and renders it for display.
//
// evStr alone is not enough for these: an HTTP status is logged as
// status=500, which decodes to float64, so reading it as a string yielded
// "" and the retry ledger's "Last Status" column was blank on every row —
// dropping the one field that says why the request was retried.
func evScalarStr(attrs map[string]any, key string) string {
	if attrs == nil {
		return ""
	}
	switch v := attrs[key].(type) {
	case string:
		return v
	case float64:
		if v == math.Trunc(v) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

// evFloat reads a numeric attr. JSON numbers decode as float64.
func evFloat(attrs map[string]any, key string) float64 {
	if attrs == nil {
		return 0
	}
	if v, ok := attrs[key]; ok {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return 0
}

// evInt reads a numeric attr as int (numbers arrive as float64).
func evInt(attrs map[string]any, key string) int {
	return int(evFloat(attrs, key))
}

// evInt64 reads a numeric attr as int64 (numbers arrive as float64).
func evInt64(attrs map[string]any, key string) int64 {
	return int64(evFloat(attrs, key))
}
