package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// newTestClient creates a sqapi.Client pointing at a test HTTP server.
func newTestClient(t *testing.T, baseURL string) *sqapi.Client {
	t.Helper()
	return sqapi.NewServerClient(baseURL, "test-token", 10.0)
}

func TestPaginateAll(t *testing.T) {
	data := [][]string{{"a", "b"}, {"c", "d"}, {"e", "f"}}
	calls := 0
	items, err := paginateAll(context.Background(), func(_ context.Context, page int) ([]string, int, error) {
		calls++
		if page <= len(data) {
			return data[page-1], 6, nil
		}
		return nil, 6, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 6 {
		t.Fatalf("got %d items, want 6", len(items))
	}
	// Pages 1-3 are fetched; on page 3 len(all)==total so the loop breaks.
	if calls != 3 {
		t.Errorf("expected 3 fetches, got %d", calls)
	}
}

func TestPaginateAllEmpty(t *testing.T) {
	calls := 0
	items, err := paginateAll(context.Background(), func(_ context.Context, _ int) ([]string, int, error) {
		calls++
		return nil, 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0", len(items))
	}
	if calls != 1 {
		t.Errorf("expected 1 fetch (empty first page stops immediately), got %d", calls)
	}
}

// metricsHandler returns an httptest handler that serves
// /api/measures/component_tree. The componentsByMetric map maps a metric key
// to the components (and their single measure) to return.
func metricsHandler(t *testing.T, componentsByMetric map[string]string, statusByMetric map[string]int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		metricKey := r.URL.Query().Get("metricKeys")
		if code, ok := statusByMetric[metricKey]; ok {
			http.Error(w, "error", code)
			return
		}
		compKey := componentsByMetric[metricKey]
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
			"components": []map[string]any{
				{"key": compKey, "measures": []map[string]any{{"metric": metricKey, "value": "42"}}},
			},
		})
	}
}

func TestFetchAllMetricsBatchMergesSameComponent(t *testing.T) {
	srv := httptest.NewServer(metricsHandler(t,
		map[string]string{"lines": "my-comp", "coverage": "my-comp"},
		nil,
	))
	defer srv.Close()

	result, err := fetchAllMetrics(context.Background(), newTestClient(t, srv.URL), "proj", []string{"lines", "coverage"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d components, want 1 (same component in both batches)", len(result))
	}
	if len(result[0].Measures) != 2 {
		t.Fatalf("got %d measures, want 2 (merged from 2 batches)", len(result[0].Measures))
	}
}

func TestFetchAllMetricsBatchComponentOnlyInSecondBatch(t *testing.T) {
	srv := httptest.NewServer(metricsHandler(t,
		map[string]string{"lines": "comp-a", "coverage": "comp-b"},
		nil,
	))
	defer srv.Close()

	result, err := fetchAllMetrics(context.Background(), newTestClient(t, srv.URL), "proj", []string{"lines", "coverage"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("got %d components, want 2 (different component per batch)", len(result))
	}
}

func TestFetchAllMetricsBatchErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(metricsHandler(t,
		map[string]string{"lines": "comp-a"},
		map[string]int{"coverage": http.StatusInternalServerError},
	))
	defer srv.Close()

	_, err := fetchAllMetrics(context.Background(), newTestClient(t, srv.URL), "proj", []string{"lines", "coverage"}, 1)
	if err == nil {
		t.Fatal("expected error from failed batch, got nil")
	}
	if !strings.Contains(err.Error(), "metrics batch") {
		t.Errorf("error should mention 'metrics batch', got: %v", err)
	}
}

func TestFetchAllIssuesSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
			"issues": []map[string]any{
				{"key": "iss-1", "status": "OPEN", "rule": "rule:1", "component": "comp", "type": "BUG", "creationDate": "2024-01-01", "updateDate": "2024-01-01"},
			},
		})
	}))
	defer srv.Close()

	issues, err := fetchAllIssues(context.Background(), newTestClient(t, srv.URL), "proj", "statuses", []string{"OPEN"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Key != "iss-1" {
		t.Errorf("got %+v, want [{Key:iss-1}]", issues)
	}
}

func TestFetchAllIssuesNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := fetchAllIssues(context.Background(), newTestClient(t, srv.URL), "proj", "statuses", []string{"OPEN"})
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("error should mention 'HTTP 403', got: %v", err)
	}
}

func TestFetchAllHotspotsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
			"hotspots": []map[string]any{
				{"key": "hs-1", "component": "comp", "project": "proj", "status": "TO_REVIEW", "securityCategory": "xss", "vulnerabilityProbability": "HIGH", "message": "msg", "creationDate": "2024-01-01", "updateDate": "2024-01-01", "ruleKey": "rule:1"},
			},
		})
	}))
	defer srv.Close()

	hotspots, err := fetchAllHotspots(context.Background(), newTestClient(t, srv.URL), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(hotspots) != 1 || hotspots[0].Key != "hs-1" {
		t.Errorf("got %+v, want [{Key:hs-1}]", hotspots)
	}
}

func TestFetchAllHotspotsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := fetchAllHotspots(context.Background(), newTestClient(t, srv.URL), "proj")
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("error should mention 'HTTP 403', got: %v", err)
	}
}

func TestFetchAllGroupsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"groups": []map[string]any{{"id": 1, "name": "grp-1", "description": "Group 1"}},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
		})
	}))
	defer srv.Close()

	groups, err := fetchAllGroups(context.Background(), newTestClient(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Name != "grp-1" {
		t.Errorf("got %+v, want [{Name:grp-1}]", groups)
	}
}

func TestFetchAllGroupsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := fetchAllGroups(context.Background(), newTestClient(t, srv.URL))
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("error should mention 'HTTP 403', got: %v", err)
	}
}

func TestMergeComponentMetricsEmpty(t *testing.T) {
	if got := mergeComponentMetrics(nil); got != nil {
		t.Errorf("mergeComponentMetrics(nil) = %v, want nil", got)
	}
}

func TestMergeComponentMetricsNoDuplicates(t *testing.T) {
	in := []ComponentMetrics{
		{Component: "a", Measures: []Measure{{Metric: "ncloc", Value: "10"}}},
		{Component: "b", Measures: []Measure{{Metric: "ncloc", Value: "20"}}},
	}
	got := mergeComponentMetrics(in)
	if len(got) != 2 {
		t.Fatalf("got %d components, want 2", len(got))
	}
	if got[0].Component != "a" || got[1].Component != "b" {
		t.Errorf("unexpected component order: %+v", got)
	}
}

func TestMergeComponentMetricsMergesSameComponent(t *testing.T) {
	in := []ComponentMetrics{
		{Component: "a", Measures: []Measure{{Metric: "ncloc", Value: "10"}}},
		{Component: "a", Measures: []Measure{{Metric: "coverage", Value: "80"}}},
		{Component: "b", Measures: []Measure{{Metric: "ncloc", Value: "20"}}},
	}
	got := mergeComponentMetrics(in)
	if len(got) != 2 {
		t.Fatalf("got %d components, want 2", len(got))
	}
	if got[0].Component != "a" || len(got[0].Measures) != 2 {
		t.Errorf("a should have 2 merged measures, got %+v", got[0])
	}
	if got[1].Component != "b" || len(got[1].Measures) != 1 {
		t.Errorf("b should have 1 measure, got %+v", got[1])
	}
}

// TestFetchComponentMetricsPaginates verifies that fetchComponentMetrics
// pages through every page (i.e. does not stop at the first page even when
// the server reports a total larger than a single page) and merges
// components that appear across pages.
func TestFetchComponentMetricsPaginates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("p")
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			json.NewEncoder(w).Encode(map[string]any{
				"paging": map[string]any{"pageIndex": 1, "pageSize": 1, "total": 2},
				"components": []map[string]any{
					{"key": "comp-a", "measures": []map[string]any{{"metric": "ncloc", "value": "10"}}},
				},
			})
		case "2":
			json.NewEncoder(w).Encode(map[string]any{
				"paging": map[string]any{"pageIndex": 2, "pageSize": 1, "total": 2},
				"components": []map[string]any{
					{"key": "comp-b", "measures": []map[string]any{{"metric": "ncloc", "value": "20"}}},
				},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{
				"paging":     map[string]any{"pageIndex": 0, "pageSize": 1, "total": 2},
				"components": []map[string]any{},
			})
		}
	}))
	defer srv.Close()

	pages, err := paginateAll(context.Background(), func(_ context.Context, page int) ([]ComponentMetrics, int, error) {
		return fetchComponentMetricsPage(context.Background(), newTestClient(t, srv.URL), "proj", []string{"ncloc"}, page, 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	merged := mergeComponentMetrics(pages)
	if len(merged) != 2 {
		t.Fatalf("got %d components, want 2 (one per page)", len(merged))
	}
}
