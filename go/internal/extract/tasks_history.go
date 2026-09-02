// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"time"
)

// historyMetricKeys is the project-level metric set captured for each
// historical snapshot (#554). Deliberately project/root-level only — the
// issue's own PoC design explicitly says files/issues aren't needed for
// history points, just the project's measures — so this mirrors a typical
// SonarQube "overview" dashboard rather than the full per-file metric set
// getProjectComponentTree pulls for the live snapshot.
const historyMetricKeys = "ncloc,bugs,vulnerabilities,code_smells,coverage," +
	"duplicated_lines_density,complexity,cognitive_complexity,security_hotspots," +
	"comment_lines,classes,functions"

// historyPoint is one candidate historical analysis: its date and the
// project version recorded at that analysis.
type historyPoint struct {
	Date           time.Time
	ProjectVersion string
}

// projectAnalysisHistoryTask extracts a bounded set of historical analysis
// snapshots (date + project-level measures) per project+branch — the data
// the migrate phase needs to replay project history as separate, backdated
// analyses on the target (#554, PoC).
//
// Disabled by default: when Executor.MigrateHistory is false the task
// returns immediately without making any API call, so extract's behavior
// and cost for every existing user is completely unchanged.
func projectAnalysisHistoryTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		if !e.MigrateHistory {
			return nil
		}
		return forEachProjectBranch(ctx, e, "getProjectAnalysisHistory",
			func(ctx context.Context, projectKey, branch string, w *ChunkWriter) error {
				return extractProjectAnalysisHistory(ctx, e, projectKey, branch, w)
			})
	}
}

// extractProjectAnalysisHistory lists the historical analyses for one
// project+branch, bounds them per Executor.HistoryMaxPoints /
// HistoryMinIntervalDays, and writes one record per selected point carrying
// that point's date, project version, and project-level measures.
func extractProjectAnalysisHistory(ctx context.Context, e *Executor, projectKey, branch string, w *ChunkWriter) error {
	points, err := listHistoricalAnalyses(ctx, e, projectKey, branch)
	if err != nil {
		if isNonFatalHTTPErr(err) {
			e.Logger.Debug("getProjectAnalysisHistory skipped", "project", projectKey, "branch", branch, "err", err)
			return nil
		}
		return err
	}

	selected := selectBoundedHistoryPoints(points, e.HistoryMaxPoints, e.HistoryMinIntervalDays)
	if len(selected) == 0 {
		return nil
	}

	e.Logger.Debug("getProjectAnalysisHistory: selected historical points",
		"project", projectKey, "branch", branch, "available", len(points), "selected", len(selected))

	for _, p := range selected {
		measures, err := fetchHistoricalMeasures(ctx, e, projectKey, branch, p.Date)
		if err != nil {
			if isNonFatalHTTPErr(err) {
				e.Logger.Warn("getProjectAnalysisHistory: measures fetch failed, skipping point",
					"project", projectKey, "branch", branch, "date", p.Date, "err", err)
				continue
			}
			return err
		}
		record := map[string]any{
			"projectKey":     projectKey,
			"branch":         branch,
			"date":           p.Date.Format(time.RFC3339),
			"projectVersion": p.ProjectVersion,
			"measures":       measures,
			"serverUrl":      e.ServerURL,
		}
		b, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := w.WriteOne(b); err != nil {
			return err
		}
	}
	return nil
}

// listHistoricalAnalyses returns every historical analysis for a
// project+branch via /api/project_analyses/search, sorted oldest to newest.
// The API itself returns newest-first; this always re-sorts defensively
// rather than assuming that order.
func listHistoricalAnalyses(ctx context.Context, e *Executor, projectKey, branch string) ([]historyPoint, error) {
	params := url.Values{
		"project": {projectKey},
	}
	if branch != "" {
		params.Set("branch", branch)
	}
	items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
		Path:      "api/project_analyses/search",
		Params:    params,
		ResultKey: "analyses",
		PageLimit: 20,
	})
	if err != nil {
		return nil, err
	}
	points := make([]historyPoint, 0, len(items))
	for _, item := range items {
		date := parseHistoryDate(extractField(item, "date"))
		if date.IsZero() {
			continue
		}
		points = append(points, historyPoint{
			Date:           date,
			ProjectVersion: extractField(item, "projectVersion"),
		})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Date.Before(points[j].Date) })
	return points, nil
}

// selectBoundedHistoryPoints applies the #554 bounding rules to a
// chronologically-sorted (oldest first) list of candidate historical
// analyses:
//
//  1. Drop the single most recent analysis. It is already covered by the
//     existing "current snapshot" migration (importProjectData), which
//     carries the real, full-content report for the project's latest
//     state — just stamped with the migration run's own timestamp rather
//     than the source's true last-analysis date. Re-adding it here as a
//     second, measures-only synthetic entry for the same instant would be
//     a redundant near-duplicate immediately next to the real one.
//  2. Enforce minIntervalDays between consecutive selected points, walking
//     oldest to newest (greedy).
//  3. If more than maxPoints points remain, evenly subsample down to
//     maxPoints across the *entire* remaining span, so the final selection
//     still covers all of history rather than clustering near the oldest
//     end (a plain "take the first N" after step 2 would do that on any
//     project with more history than maxPoints*minIntervalDays covers).
//
// maxPoints <= 0 means "no cap" (interval-only bounding). Returns nil for
// an empty or single-point input (nothing to backfill before the current
// snapshot).
func selectBoundedHistoryPoints(points []historyPoint, maxPoints, minIntervalDays int) []historyPoint {
	if len(points) < 2 {
		// A single analysis (or none) has nothing to migrate as "history"
		// distinct from the current-snapshot migration.
		return nil
	}
	points = points[:len(points)-1]

	if minIntervalDays < 0 {
		minIntervalDays = 0
	}
	minInterval := time.Duration(minIntervalDays) * 24 * time.Hour

	spaced := make([]historyPoint, 0, len(points))
	spaced = append(spaced, points[0])
	last := points[0].Date
	for _, p := range points[1:] {
		if p.Date.Sub(last) >= minInterval {
			spaced = append(spaced, p)
			last = p.Date
		}
	}

	if maxPoints <= 0 || len(spaced) <= maxPoints {
		return spaced
	}
	if maxPoints == 1 {
		return spaced[:1]
	}

	out := make([]historyPoint, 0, maxPoints)
	for i := 0; i < maxPoints; i++ {
		idx := i * (len(spaced) - 1) / (maxPoints - 1)
		out = append(out, spaced[idx])
	}
	return out
}

// fetchHistoricalMeasures queries /api/measures/search_history for the
// calendar day containing date and returns the project-level metric values
// recorded at that exact analysis timestamp.
func fetchHistoricalMeasures(ctx context.Context, e *Executor, projectKey, branch string, date time.Time) ([]map[string]string, error) {
	day := date.UTC().Format("2006-01-02")
	params := url.Values{
		"component": {projectKey},
		"metrics":   {historyMetricKeys},
		"from":      {day},
		"to":        {day},
		"ps":        {"1000"},
	}
	if branch != "" {
		params.Set("branch", branch)
	}
	raw, err := e.Raw.Get(ctx, "api/measures/search_history", params)
	if err != nil {
		return nil, err
	}
	return matchHistoricalMeasures(raw, date), nil
}

// matchHistoricalMeasures picks, for each metric in a search_history
// response, the value recorded at (or, failing an exact match, the closest
// value at or before) the target timestamp — /api/measures/search_history's
// from/to bounds are date-only, so a single call can return several
// same-day analyses and this narrows back down to the one we actually
// selected.
func matchHistoricalMeasures(raw json.RawMessage, target time.Time) []map[string]string {
	var resp struct {
		Measures []struct {
			Metric  string `json:"metric"`
			History []struct {
				Date  string `json:"date"`
				Value string `json:"value"`
			} `json:"history"`
		} `json:"measures"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil
	}
	var out []map[string]string
	for _, m := range resp.Measures {
		best := ""
		var bestDiff time.Duration = -1
		for _, h := range m.History {
			t := parseHistoryDate(h.Date)
			if t.IsZero() || t.After(target) {
				continue
			}
			diff := target.Sub(t)
			if bestDiff < 0 || diff < bestDiff {
				bestDiff = diff
				best = h.Value
			}
		}
		if best != "" {
			out = append(out, map[string]string{"metric": m.Metric, "value": best})
		}
	}
	return out
}

// parseHistoryDate parses a SonarQube date string in RFC3339 or legacy
// UTC-offset format. Returns zero time on parse failure. Mirrors
// internal/migrate's parseISODate (duplicated rather than shared — extract
// and migrate are independent packages with no date-parsing dependency
// between them today).
func parseHistoryDate(dateStr string) time.Time {
	if dateStr == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, dateStr)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05-0700", dateStr)
	}
	if err != nil {
		return time.Time{}
	}
	return t
}
