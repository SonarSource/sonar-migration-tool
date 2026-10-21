package common

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func writeAdaptiveTestPage(t *testing.T, w http.ResponseWriter, r *http.Request, total int) {
	t.Helper()

	page, err := strconv.Atoi(r.URL.Query().Get("p"))
	if err != nil {
		t.Errorf("invalid page: %v", err)
		http.Error(w, "invalid page", http.StatusBadRequest)
		return
	}

	size, err := strconv.Atoi(r.URL.Query().Get("ps"))
	if err != nil || size <= 0 {
		t.Errorf("invalid page size: %q", r.URL.Query().Get("ps"))
		http.Error(w, "invalid page size", http.StatusBadRequest)
		return
	}

	items := make([]map[string]string, 0, size)
	start := (page - 1) * size
	for i := start; i < start+size && i < total; i++ {
		items = append(items, map[string]string{
			"key": strconv.Itoa(i),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"items": items,
		"paging": map[string]int{
			"total": total,
		},
	}); err != nil {
		t.Errorf("encoding page: %v", err)
	}
}

func TestAdaptivePaginationRemembersFallbackPerEndpoint(t *testing.T) {
	var projectRequests []string
	var issueRequests []string

	mux := http.NewServeMux()

	mux.HandleFunc("/api/projects/search", func(w http.ResponseWriter, r *http.Request) {
		size := r.URL.Query().Get("ps")
		projectRequests = append(projectRequests, size)

		n, _ := strconv.Atoi(size)
		if n > 50 {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}

		writeAdaptiveTestPage(t, w, r, 55)
	})

	mux.HandleFunc("/api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		issueRequests = append(issueRequests, r.URL.Query().Get("ps"))
		writeAdaptiveTestPage(t, w, r, 55)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	raw := NewRawClient(server.Client(), server.URL+"/")
	logs := captureDefaultLogger(t)

	opts := PaginatedOpts{
		Path:      "api/projects/search",
		ResultKey: "items",
	}

	first, err := raw.GetPaginatedResult(context.Background(), opts)
	if err != nil {
		t.Fatalf("first project fetch: %v", err)
	}
	if first.PageSize != 50 || first.Fetched != 55 || first.Truncated {
		t.Fatalf("unexpected first result: %+v", first)
	}

	second, err := raw.GetPaginatedResult(context.Background(), opts)
	if err != nil {
		t.Fatalf("second project fetch: %v", err)
	}
	if second.PageSize != 50 || second.Fetched != 55 {
		t.Fatalf("unexpected second result: %+v", second)
	}

	wantProjects := []string{"500", "200", "100", "50", "50", "50", "50"}
	if !reflect.DeepEqual(projectRequests, wantProjects) {
		t.Errorf("project page sizes: got %v, want %v", projectRequests, wantProjects)
	}

	issues, err := raw.GetPaginatedResult(context.Background(), PaginatedOpts{
		Path:      "api/issues/search",
		ResultKey: "items",
	})
	if err != nil {
		t.Fatalf("issue fetch: %v", err)
	}
	if issues.PageSize != 500 || issues.Fetched != 55 {
		t.Fatalf("unexpected issue result: %+v", issues)
	}
	if !reflect.DeepEqual(issueRequests, []string{"500"}) {
		t.Errorf("issue page sizes: got %v, want [500]", issueRequests)
	}

	warnings := strings.Count(
		logs.String(),
		"paginated API request returned HTTP 5xx",
	)
	if warnings != 3 {
		t.Errorf("fallback warnings: got %d, want 3", warnings)
	}
}

// A failure on page 2 at ps=500 keeps the 400 items page 1 holds below
// the first 200-aligned offset and resumes there, at page 3 of 200,
// instead of re-reading from page 1. Items 400-499 are read again as
// part of that page, so every item comes back once, in order.
func TestAdaptivePaginationResumesAfterLaterPageFailure(t *testing.T) {
	var requests []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("p")
		size := r.URL.Query().Get("ps")
		requests = append(requests, page+":"+size)

		if page == "2" && size == "500" {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}

		writeAdaptiveTestPage(t, w, r, 650)
	}))
	defer server.Close()

	raw := NewRawClient(server.Client(), server.URL+"/")

	result, err := raw.GetPaginatedResult(context.Background(), PaginatedOpts{
		Path:      "api/projects/search",
		ResultKey: "items",
	})
	if err != nil {
		t.Fatalf("paginated fetch: %v", err)
	}

	if result.PageSize != 200 || result.Fetched != 650 || result.Truncated {
		t.Fatalf("unexpected result: %+v", result)
	}

	wantRequests := []string{
		"1:500",
		"2:500",
		"3:200",
		"4:200",
	}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Errorf("requests: got %v, want %v", requests, wantRequests)
	}

	seen := make(map[string]bool)

	for i, item := range result.Items {
		var record struct {
			Key string `json:"key"`
		}

		if err := json.Unmarshal(item, &record); err != nil {
			t.Fatalf("decoding issue %d: %v", i, err)
		}

		wantKey := strconv.Itoa(i)
		if record.Key != wantKey {
			t.Errorf("item %d: got key %q, want %q", i, record.Key, wantKey)
		}
		if seen[record.Key] {
			t.Errorf("duplicate key %q", record.Key)
		}
		seen[record.Key] = true
	}

	if len(seen) != 650 {
		t.Errorf("unique keys: got %d, want 650", len(seen))
	}
}
