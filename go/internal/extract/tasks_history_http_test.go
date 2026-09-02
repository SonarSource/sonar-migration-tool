// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// HTTP-side coverage for the #554 project-history extract: the task
// closure, extractProjectAnalysisHistory, listHistoricalAnalyses and
// fetchHistoricalMeasures, all driven against local httptest servers.
// The pure helpers (selectBoundedHistoryPoints / matchHistoricalMeasures /
// parseHistoryDate) are covered in tasks_history_test.go.

const (
	hhTaskName     = "getProjectAnalysisHistory"
	hhAnalysesPath = "/api/project_analyses/search"
	hhMeasuresPath = "/api/measures/search_history"
	hhProject      = "p1"
	hhBranch       = "main"
	hhMetric       = "ncloc"
)

// hhBase is the reference instant every fake analysis date derives from.
var hhBase = time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

// hhAPIDate renders an analysis date the way SonarQube's API does — the
// legacy "+0000" offset form, not RFC3339.
func hhAPIDate(dayOffset int) string {
	return hhBase.AddDate(0, 0, dayOffset).Format("2006-01-02T15:04:05-0700")
}

// hhRecordDate renders the same instant the way extractProjectAnalysisHistory
// must write it into its record (RFC3339).
func hhRecordDate(dayOffset int) string {
	return hhBase.AddDate(0, 0, dayOffset).Format(time.RFC3339)
}

// hhDay renders the calendar day of an analysis date, in the date's own
// location.
func hhDay(dayOffset int) string {
	return hhBase.AddDate(0, 0, dayOffset).Format("2006-01-02")
}

// hhWindow returns the (from, to) pair fetchHistoricalMeasures must send for
// the analysis at dayOffset: the day in the timestamp's OWN location, widened
// by one day on each side. matchHistoricalMeasures narrows the response back
// to the exact timestamp, so the widening only guards the boundary.
func hhWindow(dayOffset int) (string, string) {
	return hhDay(dayOffset - 1), hhDay(dayOffset + 1)
}

// hhRange returns n consecutive day offsets starting at start.
func hhRange(start, n int) []int {
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, start+i)
	}
	return out
}

// hhAnalysesPage builds one page of an /api/project_analyses/search
// response. dayOffsets must be ascending; entries come back NEWEST-FIRST
// as the real API returns them, each carrying a "v<dayOffset>" version so
// assertions can prove a version stays bound to its own date.
func hhAnalysesPage(dayOffsets []int, total int) map[string]any {
	analyses := make([]map[string]any, 0, len(dayOffsets))
	for i := len(dayOffsets) - 1; i >= 0; i-- {
		analyses = append(analyses, map[string]any{
			"key":            fmt.Sprintf("A%d", dayOffsets[i]),
			"date":           hhAPIDate(dayOffsets[i]),
			"projectVersion": fmt.Sprintf("v%d", dayOffsets[i]),
		})
	}
	return map[string]any{
		"analyses": analyses,
		"paging":   map[string]any{"pageIndex": 1, "pageSize": 500, "total": total},
	}
}

// hhMeasuresBody builds an /api/measures/search_history response covering the
// whole requested [from, to] window, one entry per day, echoing each day back
// as its own metric value. The real endpoint behaves this way — its bounds are
// date-only and it returns every analysis inside them — and since
// fetchHistoricalMeasures now widens the window by a day on each side, a fake
// that returned a single entry would not exercise matchHistoricalMeasures'
// narrowing at all. A record ending up with the wrong day's value is therefore
// a real failure this fake can surface.
func hhMeasuresBody(from, to string) map[string]any {
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return map[string]any{"measures": []map[string]any{}}
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		end = start
	}
	history := make([]map[string]any, 0, 3)
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		history = append(history, map[string]any{"date": day + "T10:00:00+0000", "value": day})
	}
	return map[string]any{
		"measures": []map[string]any{{"metric": hhMetric, "history": history}},
	}
}

// hhTargetDay recovers the analysis day a measures call was made FOR from the
// window it requested: fetchHistoricalMeasures asks for the day before through
// the day after, so the middle day is the point itself. Keeps the assertions
// in these tests phrased in terms of points rather than window edges.
func hhTargetDay(from string) string {
	d, err := time.Parse("2006-01-02", from)
	if err != nil {
		return from
	}
	return d.AddDate(0, 0, 1).Format("2006-01-02")
}

// hhCalls records what the fake history server was asked for.
type hhCalls struct {
	mu           sync.Mutex
	analyses     []url.Values
	measuresDays []string
}

func (c *hhCalls) recordAnalyses(q url.Values) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.analyses = append(c.analyses, q)
}

func (c *hhCalls) recordMeasures(from string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.measuresDays = append(c.measuresDays, from)
}

// measuresFrom returns the "from" day of every measures call, in order.
func (c *hhCalls) measuresFrom() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.measuresDays...)
}

func (c *hhCalls) analysesQueries() []url.Values {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]url.Values(nil), c.analyses...)
}

// hhServeHistory serves both history endpoints for the given analysis day
// offsets (ascending) and records every call. measureStatus, when non-nil,
// is consulted with each requested "from" day and may return a non-zero
// HTTP status to fail that one point's measures fetch.
func hhServeHistory(calls *hhCalls, dayOffsets []int, measureStatus func(from string) int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case hhAnalysesPath:
			calls.recordAnalyses(r.URL.Query())
			_ = json.NewEncoder(w).Encode(hhAnalysesPage(dayOffsets, len(dayOffsets)))
		case hhMeasuresPath:
			from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
			target := hhTargetDay(from)
			calls.recordMeasures(target)
			if measureStatus != nil {
				if code := measureStatus(target); code != 0 {
					w.WriteHeader(code)
					return
				}
			}
			_ = json.NewEncoder(w).Encode(hhMeasuresBody(from, to))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// hhRecord mirrors the JSONL record extractProjectAnalysisHistory writes.
type hhRecord struct {
	ProjectKey     string              `json:"projectKey"`
	Branch         string              `json:"branch"`
	Date           string              `json:"date"`
	ProjectVersion string              `json:"projectVersion"`
	Measures       []map[string]string `json:"measures"`
	ServerURL      string              `json:"serverUrl"`
}

func hhWriter(t *testing.T, e *Executor) *ChunkWriter {
	t.Helper()
	w, err := e.Store.Writer(hhTaskName)
	if err != nil {
		t.Fatalf("opening the %s writer: %v", hhTaskName, err)
	}
	return w
}

// hhReadRecords reads back everything the history task wrote, sorted by
// date — ReadAll returns chunk files in lexical, not write, order.
func hhReadRecords(t *testing.T, e *Executor) []hhRecord {
	t.Helper()
	raws, err := e.Store.ReadAll(hhTaskName)
	if err != nil {
		t.Fatalf("reading back %s: %v", hhTaskName, err)
	}
	out := make([]hhRecord, 0, len(raws))
	for _, raw := range raws {
		var rec hhRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("unmarshalling record %s: %v", raw, err)
		}
		out = append(out, rec)
	}
	// RFC3339 UTC timestamps sort lexically in chronological order.
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

func hhDates(recs []hhRecord) []string {
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec.Date)
	}
	return out
}

// hhOnlyMeasure returns the metric/value of a single-measure slice.
func hhOnlyMeasure(t *testing.T, measures []map[string]string) (string, string) {
	t.Helper()
	if len(measures) != 1 {
		t.Fatalf("expected exactly 1 measure, got %v", measures)
	}
	return measures[0]["metric"], measures[0]["value"]
}

func hhEqualStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: expected %v, got %v", what, want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: expected %v, got %v", what, want, got)
		}
	}
}

// A record must carry the project, branch, RFC3339 date, project version,
// measures and server URL of the point it was built from.
func TestExtractProjectAnalysisHistoryWritesRecords(t *testing.T) {
	calls := &hhCalls{}
	srv, e := newSrvExecutor(t, hhServeHistory(calls, []int{0, 10, 20, 30}, nil))
	defer srv.Close()
	e.HistoryMaxPoints = 10
	e.HistoryMinIntervalDays = 0

	if err := extractProjectAnalysisHistory(ctx(t), e, hhProject, hhBranch, hhWriter(t, e)); err != nil {
		t.Fatalf("extractProjectAnalysisHistory: %v", err)
	}

	recs := hhReadRecords(t, e)
	if len(recs) != 3 {
		t.Fatalf("expected 3 records (newest of 4 analyses dropped), got %d: %+v", len(recs), recs)
	}
	for i, day := range []int{0, 10, 20} {
		rec := recs[i]
		if rec.ProjectKey != hhProject || rec.Branch != hhBranch {
			t.Errorf("record %d: expected %s/%s, got %s/%s", i, hhProject, hhBranch, rec.ProjectKey, rec.Branch)
		}
		if rec.Date != hhRecordDate(day) {
			t.Errorf("record %d: expected date %s, got %s", i, hhRecordDate(day), rec.Date)
		}
		if want := fmt.Sprintf("v%d", day); rec.ProjectVersion != want {
			t.Errorf("record %d: expected projectVersion %s, got %s", i, want, rec.ProjectVersion)
		}
		// The record must carry the configured server URL, not the
		// throwaway httptest address.
		if rec.ServerURL != "http://test/" {
			t.Errorf("record %d: expected serverUrl http://test/, got %s", i, rec.ServerURL)
		}
		metric, value := hhOnlyMeasure(t, rec.Measures)
		if metric != hhMetric || value != hhDay(day) {
			t.Errorf("record %d: expected %s=%s (the measures of its own point), got %s=%s",
				i, hhMetric, hhDay(day), metric, value)
		}
	}
}

// The newest analysis belongs to the regular current-snapshot import, so
// it must neither be written nor have its measures fetched.
func TestExtractProjectAnalysisHistoryExcludesNewestAnalysis(t *testing.T) {
	calls := &hhCalls{}
	srv, e := newSrvExecutor(t, hhServeHistory(calls, []int{0, 1, 2}, nil))
	defer srv.Close()
	e.HistoryMaxPoints = 10
	e.HistoryMinIntervalDays = 0

	if err := extractProjectAnalysisHistory(ctx(t), e, hhProject, hhBranch, hhWriter(t, e)); err != nil {
		t.Fatalf("extractProjectAnalysisHistory: %v", err)
	}

	hhEqualStrings(t, "record dates", hhDates(hhReadRecords(t, e)),
		[]string{hhRecordDate(0), hhRecordDate(1)})
	for _, from := range calls.measuresFrom() {
		if from == hhDay(2) {
			t.Errorf("measures were fetched for the newest analysis (%s); it must be skipped", from)
		}
	}
}

// #554 bounding must hold end to end: 20 analyses 5 days apart, a 10-day
// minimum interval and a cap of 4 leave exactly days 0/30/60/90 — evenly
// spread across the whole span rather than clustered at the oldest end.
func TestExtractProjectAnalysisHistoryAppliesBoundingEndToEnd(t *testing.T) {
	days := make([]int, 0, 20)
	for i := 0; i < 20; i++ {
		days = append(days, i*5)
	}
	calls := &hhCalls{}
	srv, e := newSrvExecutor(t, hhServeHistory(calls, days, nil))
	defer srv.Close()
	e.HistoryMaxPoints = 4
	e.HistoryMinIntervalDays = 10

	if err := extractProjectAnalysisHistory(ctx(t), e, hhProject, hhBranch, hhWriter(t, e)); err != nil {
		t.Fatalf("extractProjectAnalysisHistory: %v", err)
	}

	hhEqualStrings(t, "record dates", hhDates(hhReadRecords(t, e)),
		[]string{hhRecordDate(0), hhRecordDate(30), hhRecordDate(60), hhRecordDate(90)})
	// Only the selected points may cost a measures round-trip.
	hhEqualStrings(t, "measures calls", calls.measuresFrom(),
		[]string{hhDay(0), hhDay(30), hhDay(60), hhDay(90)})
}

// A project with a single analysis has no history distinct from its
// current snapshot: no records, and not one measures call.
func TestExtractProjectAnalysisHistoryNothingSelectedSkipsMeasures(t *testing.T) {
	calls := &hhCalls{}
	srv, e := newSrvExecutor(t, hhServeHistory(calls, []int{0}, nil))
	defer srv.Close()
	e.HistoryMaxPoints = 10
	e.HistoryMinIntervalDays = 0

	if err := extractProjectAnalysisHistory(ctx(t), e, hhProject, hhBranch, hhWriter(t, e)); err != nil {
		t.Fatalf("expected no error when nothing is selected, got %v", err)
	}
	if recs := hhReadRecords(t, e); len(recs) != 0 {
		t.Errorf("expected no records, got %+v", recs)
	}
	if got := calls.measuresFrom(); len(got) != 0 {
		t.Errorf("expected zero measures calls when nothing is selected, got %v", got)
	}
}

// A 403 listing analyses skips the project+branch instead of failing the
// whole extract.
func TestExtractProjectAnalysisHistoryAnalysesListNonFatal(t *testing.T) {
	calls := &hhCalls{}
	srv, e := newSrvExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == hhMeasuresPath {
			calls.recordMeasures(r.URL.Query().Get("from"))
		}
		w.WriteHeader(http.StatusForbidden)
	})
	defer srv.Close()
	e.HistoryMaxPoints = 10

	if err := extractProjectAnalysisHistory(ctx(t), e, hhProject, hhBranch, hhWriter(t, e)); err != nil {
		t.Fatalf("expected a 403 on the analyses list to be a non-fatal skip, got %v", err)
	}
	if recs := hhReadRecords(t, e); len(recs) != 0 {
		t.Errorf("expected no records after a skipped project, got %+v", recs)
	}
	if got := calls.measuresFrom(); len(got) != 0 {
		t.Errorf("expected no measures calls after a skipped project, got %v", got)
	}
}

// A 500 listing analyses is a real failure and must propagate.
func TestExtractProjectAnalysisHistoryAnalysesListFatal(t *testing.T) {
	srv, e := newSrvExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()
	e.HistoryMaxPoints = 10

	err := extractProjectAnalysisHistory(ctx(t), e, hhProject, hhBranch, hhWriter(t, e))
	if err == nil {
		t.Fatal("expected a 500 on the analyses list to be returned as an error")
	}
	if !common.IsHTTPError(err, 500) {
		t.Errorf("expected the underlying HTTP 500 to survive, got %v", err)
	}
}

// A 404 on ONE point's measures skips only that point — the remaining
// points must still be written (a `continue`, not an early return).
func TestExtractProjectAnalysisHistoryMeasuresNonFatalSkipsOnePoint(t *testing.T) {
	calls := &hhCalls{}
	failDay := hhDay(10)
	srv, e := newSrvExecutor(t, hhServeHistory(calls, []int{0, 10, 20, 30}, func(from string) int {
		if from == failDay {
			return http.StatusNotFound
		}
		return 0
	}))
	defer srv.Close()
	e.HistoryMaxPoints = 10
	e.HistoryMinIntervalDays = 0

	if err := extractProjectAnalysisHistory(ctx(t), e, hhProject, hhBranch, hhWriter(t, e)); err != nil {
		t.Fatalf("expected a 404 on one point's measures to be non-fatal, got %v", err)
	}

	// 3 points selected, 1 of them lost its measures -> 2 records, and the
	// point AFTER the failing one must still be there.
	hhEqualStrings(t, "record dates", hhDates(hhReadRecords(t, e)),
		[]string{hhRecordDate(0), hhRecordDate(20)})
	hhEqualStrings(t, "measures calls", calls.measuresFrom(),
		[]string{hhDay(0), hhDay(10), hhDay(20)})
}

// A 500 fetching a point's measures aborts the project+branch.
func TestExtractProjectAnalysisHistoryMeasuresFatal(t *testing.T) {
	calls := &hhCalls{}
	srv, e := newSrvExecutor(t, hhServeHistory(calls, []int{0, 10, 20}, func(string) int {
		return http.StatusInternalServerError
	}))
	defer srv.Close()
	e.HistoryMaxPoints = 10
	e.HistoryMinIntervalDays = 0

	err := extractProjectAnalysisHistory(ctx(t), e, hhProject, hhBranch, hhWriter(t, e))
	if err == nil {
		t.Fatal("expected a 500 on a measures fetch to be returned as an error")
	}
	if !common.IsHTTPError(err, 500) {
		t.Errorf("expected the underlying HTTP 500 to survive, got %v", err)
	}
	if recs := hhReadRecords(t, e); len(recs) != 0 {
		t.Errorf("expected no records to be written before the fatal error, got %+v", recs)
	}
}

// search_history's from/to are date-only and the SERVER interprets them in
// its own timezone, so the day must be formatted in the analysis timestamp's
// own location — never in UTC. An analysis at 2024-05-15T01:00:00+03:00 is
// UTC day 2024-05-14; querying that UTC day asks for the wrong calendar date
// and either finds nothing (the point migrates with no measures) or
// attributes a neighbouring analysis's values to it. This pins the local day
// 2024-05-15, widened to 05-14..05-16; the UTC reading would produce
// 05-13..05-15 and fail here.
func TestFetchHistoricalMeasuresUsesServerLocalCalendarDay(t *testing.T) {
	var (
		mu    sync.Mutex
		query url.Values
	)
	srv, e := newSrvExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != hhMeasuresPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		query = r.URL.Query()
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(hhMeasuresBody(r.URL.Query().Get("from"), r.URL.Query().Get("to")))
	})
	defer srv.Close()

	// 2024-05-15T01:00:00+03:00 == 2024-05-14T22:00:00Z.
	date, err := time.Parse(time.RFC3339, "2024-05-15T01:00:00+03:00")
	if err != nil {
		t.Fatalf("parsing the fixture date: %v", err)
	}
	measures, err := fetchHistoricalMeasures(ctx(t), e, "my-project", "develop", date)
	if err != nil {
		t.Fatalf("fetchHistoricalMeasures: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := map[string]string{
		"component": "my-project",
		"metrics":   historyMetricKeys,
		"from":      "2024-05-14",
		"to":        "2024-05-16",
		"ps":        "1000",
		"branch":    "develop",
	}
	for key, wantValue := range want {
		if got := query.Get(key); got != wantValue {
			t.Errorf("query param %s: expected %q, got %q", key, wantValue, got)
		}
	}
	metric, value := hhOnlyMeasure(t, measures)
	if metric != hhMetric || value != "2024-05-14" {
		t.Errorf("expected the day's measure to be mapped through, got %s=%s", metric, value)
	}
}

// An unset branch must not send an empty branch param — SonarQube treats
// branch="" as a real (missing) branch.
func TestFetchHistoricalMeasuresOmitsBranchWhenEmpty(t *testing.T) {
	var (
		mu    sync.Mutex
		query url.Values
	)
	srv, e := newSrvExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		query = r.URL.Query()
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(hhMeasuresBody(r.URL.Query().Get("from"), r.URL.Query().Get("to")))
	})
	defer srv.Close()

	if _, err := fetchHistoricalMeasures(ctx(t), e, hhProject, "", hhBase); err != nil {
		t.Fatalf("fetchHistoricalMeasures: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if _, ok := query["branch"]; ok {
		t.Errorf("expected no branch param for an empty branch, got %v", query["branch"])
	}
	wantFrom, wantTo := hhWindow(0)
	if got := query.Get("from"); got != wantFrom {
		t.Errorf("expected from=%s, got %s", wantFrom, got)
	}
	if got := query.Get("to"); got != wantTo {
		t.Errorf("expected to=%s, got %s", wantTo, got)
	}
}

// A day can hold several analyses; the response must be narrowed back down
// to the target timestamp, and a metric only recorded after it is dropped.
func TestFetchHistoricalMeasuresMatchesTargetTimestamp(t *testing.T) {
	srv, e := newSrvExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"measures":[
			{"metric":"ncloc","history":[
				{"date":"2024-05-14T10:00:00+0000","value":"100"},
				{"date":"2024-05-14T12:00:00+0000","value":"200"},
				{"date":"2024-05-14T14:00:00+0000","value":"300"}]},
			{"metric":"coverage","history":[
				{"date":"2024-05-14T14:00:00+0000","value":"88.0"}]}]}`))
	})
	defer srv.Close()

	target := time.Date(2024, 5, 14, 12, 0, 0, 0, time.UTC)
	measures, err := fetchHistoricalMeasures(ctx(t), e, hhProject, hhBranch, target)
	if err != nil {
		t.Fatalf("fetchHistoricalMeasures: %v", err)
	}
	metric, value := hhOnlyMeasure(t, measures)
	if metric != hhMetric || value != "200" {
		t.Errorf("expected ncloc=200 at the target timestamp (coverage is later and must drop), got %s=%s",
			metric, value)
	}
}

func TestFetchHistoricalMeasuresHTTPError(t *testing.T) {
	srv, e := newSrvExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	measures, err := fetchHistoricalMeasures(ctx(t), e, hhProject, hhBranch, hhBase)
	if err == nil {
		t.Fatal("expected an error for a failing search_history call")
	}
	if measures != nil {
		t.Errorf("expected no measures alongside the error, got %v", measures)
	}
}

// The API answers newest-first and can carry undated entries; the result
// must be oldest-first with every version still bound to its own date.
func TestListHistoricalAnalysesSortsOldestFirstAndDropsUndated(t *testing.T) {
	srv, e := newSrvExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != hhAnalysesPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"analyses":[
			{"date":"2024-03-01T10:00:00+0000","projectVersion":"3.0"},
			{"date":"garbage","projectVersion":"unparseable"},
			{"projectVersion":"no-date-field"},
			{"date":"","projectVersion":"empty-date"},
			{"date":"2024-02-01T10:00:00+0000","projectVersion":"2.0"},
			{"date":"2024-01-01T10:00:00+0000","projectVersion":"1.0"}],
			"paging":{"pageIndex":1,"pageSize":500,"total":6}}`))
	})
	defer srv.Close()

	points, err := listHistoricalAnalyses(ctx(t), e, hhProject, hhBranch)
	if err != nil {
		t.Fatalf("listHistoricalAnalyses: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("expected the 3 dated analyses only, got %d: %+v", len(points), points)
	}
	versions := make([]string, 0, len(points))
	for i, p := range points {
		if i > 0 && !p.Date.After(points[i-1].Date) {
			t.Errorf("expected points oldest-first, but point %d (%v) is not after %v", i, p.Date, points[i-1].Date)
		}
		versions = append(versions, p.ProjectVersion)
	}
	hhEqualStrings(t, "versions in date order", versions, []string{"1.0", "2.0", "3.0"})
}

func TestListHistoricalAnalysesSendsProjectAndBranch(t *testing.T) {
	calls := &hhCalls{}
	srv, e := newSrvExecutor(t, hhServeHistory(calls, []int{0, 1}, nil))
	defer srv.Close()

	if _, err := listHistoricalAnalyses(ctx(t), e, hhProject, "develop"); err != nil {
		t.Fatalf("listHistoricalAnalyses (branch): %v", err)
	}
	if _, err := listHistoricalAnalyses(ctx(t), e, hhProject, ""); err != nil {
		t.Fatalf("listHistoricalAnalyses (no branch): %v", err)
	}

	queries := calls.analysesQueries()
	if len(queries) != 2 {
		t.Fatalf("expected 2 analyses calls, got %d", len(queries))
	}
	for i, q := range queries {
		if got := q.Get("project"); got != hhProject {
			t.Errorf("call %d: expected project=%s, got %q", i, hhProject, got)
		}
	}
	if got := queries[0].Get("branch"); got != "develop" {
		t.Errorf("expected branch=develop on the first call, got %q", got)
	}
	if _, ok := queries[1]["branch"]; ok {
		t.Errorf("expected no branch param when the branch is empty, got %v", queries[1]["branch"])
	}
}

// extractProjectAnalysisHistory's skip path keys off the 403/404
// classification, so the status has to survive as an *HTTPError.
func TestListHistoricalAnalysesHTTPErrorClassification(t *testing.T) {
	for _, status := range []int{403, 500} {
		srv, e := newSrvExecutor(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		})

		points, err := listHistoricalAnalyses(ctx(t), e, hhProject, hhBranch)
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
		if points != nil {
			t.Errorf("status %d: expected no points alongside the error, got %+v", status, points)
		}
		if got := isNonFatalHTTPErr(err); got != (status == 403) {
			t.Errorf("status %d: isNonFatalHTTPErr = %v, expected %v", status, got, status == 403)
		}
	}
}

// The analyses list is paginated; every page must be pulled or history
// silently starts at page 1's oldest entry.
func TestListHistoricalAnalysesFetchesEveryPage(t *testing.T) {
	const total = 600
	var (
		mu    sync.Mutex
		pages []string
	)
	srv, e := newSrvExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != hhAnalysesPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		page := r.URL.Query().Get("p")
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		offsets := hhRange(0, 500)
		if page != "1" {
			offsets = hhRange(500, 100)
		}
		_ = json.NewEncoder(w).Encode(hhAnalysesPage(offsets, total))
	})
	defer srv.Close()

	points, err := listHistoricalAnalyses(ctx(t), e, hhProject, hhBranch)
	if err != nil {
		t.Fatalf("listHistoricalAnalyses: %v", err)
	}
	if len(points) != total {
		t.Errorf("expected %d points across both pages, got %d", total, len(points))
	}
	mu.Lock()
	defer mu.Unlock()
	hhEqualStrings(t, "requested pages", pages, []string{"1", "2"})
}

// #554's default-off contract: with MigrateHistory unset the task must not
// make a single API call, nor create its output directory.
func TestProjectAnalysisHistoryTaskDisabledMakesNoAPICall(t *testing.T) {
	var (
		mu   sync.Mutex
		hits int
	)
	srv, e := newSrvExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	if err := projectAnalysisHistoryTask()(ctx(t), e); err != nil {
		t.Fatalf("expected the disabled task to be a no-op, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Errorf("expected zero API calls with MigrateHistory=false, got %d", hits)
	}
	if e.Store.TaskDirExists(hhTaskName) {
		t.Errorf("expected no %s output directory with MigrateHistory=false", hhTaskName)
	}
}

// With MigrateHistory set the task fans out over the seeded project and
// writes bounded records for it.
func TestProjectAnalysisHistoryTaskEnabledWritesRecords(t *testing.T) {
	calls := &hhCalls{}
	srv, e := newSrvExecutor(t, hhServeHistory(calls, []int{0, 10, 20}, nil))
	defer srv.Close()
	e.MigrateHistory = true
	e.HistoryMaxPoints = 10
	e.HistoryMinIntervalDays = 0

	if err := projectAnalysisHistoryTask()(ctx(t), e); err != nil {
		t.Fatalf("projectAnalysisHistoryTask: %v", err)
	}

	recs := hhReadRecords(t, e)
	hhEqualStrings(t, "record dates", hhDates(recs), []string{hhRecordDate(0), hhRecordDate(10)})
	for i, rec := range recs {
		if rec.ProjectKey != hhProject || rec.Branch != hhBranch {
			t.Errorf("record %d: expected %s/%s, got %s/%s", i, hhProject, hhBranch, rec.ProjectKey, rec.Branch)
		}
	}
	queries := calls.analysesQueries()
	if len(queries) != 1 {
		t.Fatalf("expected exactly 1 analyses call for the single seeded project+branch, got %d", len(queries))
	}
	if got := queries[0].Get("branch"); got != hhBranch {
		t.Errorf("expected the task to query branch=%s, got %q", hhBranch, got)
	}
}
