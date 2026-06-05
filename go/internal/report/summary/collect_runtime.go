// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/analysis"
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
	Warnings      WarningLedger
	Branches      []BranchStat
	Throughput    ThroughputStats
}

// runMetaFile mirrors the on-disk run_meta.json written by the migrate
// engine (shared contract A). Decoded locally so the summary package
// stays free of any migrate-package import.
type runMetaFile struct {
	StartedAt     time.Time      `json:"started_at"`
	CompletedAt   time.Time      `json:"completed_at"`
	OverallStatus string         `json:"overall_status"`
	Phases        []runMetaPhase `json:"phases"`
	Tasks         []runMetaTask  `json:"tasks"`
}

type runMetaPhase struct {
	Index           int     `json:"index"`
	Tasks           int     `json:"tasks"`
	DurationSeconds float64 `json:"duration_seconds"`
}

type runMetaTask struct {
	Phase           int     `json:"phase"`
	Name            string  `json:"name"`
	DurationSeconds float64 `json:"duration_seconds"`
	OK              bool    `json:"ok"`
	Err             string  `json:"err"`
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
			Phase:    t.Phase,
			Task:     t.Name,
			Duration: secondsToDuration(t.DurationSeconds),
			OK:       t.OK,
			Err:      t.Err,
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

// eventAggregator holds the in-order, deterministic state accumulated
// while streaming run_events.jsonl. Retries are keyed by method+endpoint
// (with retryOrder preserving first-seen order before the final stable
// Count-descending sort); branches are keyed by branch/project name.
type eventAggregator struct {
	retries    map[string]*RetryStat
	retryOrder []string
	branches   map[string]*BranchStat
}

func newEventAggregator() *eventAggregator {
	return &eventAggregator{
		retries:  map[string]*RetryStat{},
		branches: map[string]*BranchStat{},
	}
}

// apply routes a single event to the matching handler. Splitting the
// dispatch into focused handlers keeps each one simple and the overall
// flow easy to follow.
func (agg *eventAggregator) apply(ev logEventLine, rt *runtimeData) {
	switch {
	case ev.Message == "retrying request":
		agg.applyRetry(ev, rt)
	case strings.HasPrefix(ev.Message, "skipping branch: source code not retrievable"):
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
	rs.LastStatus = evStr(a, "status")
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
	bs := branchFor(agg.branches, branch)
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
	key := firstNonEmpty(evStr(a, "sourceBranch"), evStr(a, "targetBranch"), evStr(a, "project"))
	bs := branchFor(agg.branches, key)
	bs.Issues = evInt(a, "issues")
	bs.ExternalIssues = evInt(a, "externalIssues")
	bs.Components = evInt(a, "components")
	bs.ActiveRules = evInt(a, "activeRules")
	bs.ZipBytes = evInt64(a, "zipSizeBytes")
	bs.Status = "packaged"
}

func (agg *eventAggregator) applyTaskSubmitted(ev logEventLine) {
	a := ev.Attrs
	bs := branchFor(agg.branches, evStr(a, "targetBranch"))
	bs.TaskID = evStr(a, "taskId")
	if bs.Status != "packaged" {
		bs.Status = "submitted"
	}
}

func (agg *eventAggregator) applyAnalysisPreCreated(ev logEventLine) {
	a := ev.Attrs
	bs := branchFor(agg.branches, evStr(a, "branch"))
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

	// Branches: ascending by Branch name; accumulate throughput totals.
	for _, bs := range agg.branches {
		rt.Branches = append(rt.Branches, *bs)
	}
	sort.Slice(rt.Branches, func(i, j int) bool {
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
		rt.Failures = append(rt.Failures, FailureRow{
			EntityType:   row.EntityType,
			EntityName:   row.EntityName,
			Organization: row.Organization,
			URL:          row.URL,
			HTTPStatus:   row.HTTPStatus,
			ErrorMessage: row.ErrorMessage,
		})
	}
	sort.SliceStable(rt.Failures, func(i, j int) bool {
		if rt.Failures[i].EntityType != rt.Failures[j].EntityType {
			return rt.Failures[i].EntityType < rt.Failures[j].EntityType
		}
		return rt.Failures[i].EntityName < rt.Failures[j].EntityName
	})
}

// branchFor returns the BranchStat for the given key, creating it (with
// Branch set to the key) on first use so events that arrive in any order
// share one row.
func branchFor(m map[string]*BranchStat, key string) *BranchStat {
	bs, ok := m[key]
	if !ok {
		bs = &BranchStat{Branch: key}
		m[key] = bs
	}
	return bs
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
