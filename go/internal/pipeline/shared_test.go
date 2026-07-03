// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStandardExtractIssues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("statuses"); got != "OPEN,CONFIRMED,REOPENED,RESOLVED,CLOSED" {
			t.Errorf("expected statuses param with legacy values, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
			"issues": []map[string]any{
				{"key": "issue-1", "status": "OPEN", "rule": "rule:1", "component": "comp", "type": "BUG", "creationDate": "2024-01-01", "updateDate": "2024-01-01"},
			},
		})
	}))
	defer srv.Close()

	p := newSQ99(newTestClient(t, srv.URL))
	issues, err := p.ExtractIssues(context.Background(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Key != "issue-1" {
		t.Errorf("got %+v, want [{Key:issue-1}]", issues)
	}
}

func TestStandardExtractIssuesModernParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("issueStatuses"); got != "OPEN,CONFIRMED,FALSE_POSITIVE,ACCEPTED,FIXED" {
			t.Errorf("expected issueStatuses param, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
			"issues": []map[string]any{
				{"key": "issue-2", "status": "OPEN", "rule": "rule:1", "component": "comp", "type": "BUG", "creationDate": "2024-01-01", "updateDate": "2024-01-01"},
			},
		})
	}))
	defer srv.Close()

	p := newSQ104(newTestClient(t, srv.URL))
	issues, err := p.ExtractIssues(context.Background(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Key != "issue-2" {
		t.Errorf("got %+v, want [{Key:issue-2}]", issues)
	}
}

func TestStandardExtractHotspots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
			"hotspots": []map[string]any{
				{"key": "hs-1", "component": "comp", "project": "proj", "securityCategory": "xss", "vulnerabilityProbability": "HIGH", "status": "TO_REVIEW", "message": "test", "creationDate": "2024-01-01", "updateDate": "2024-01-01", "ruleKey": "rule:1"},
			},
		})
	}))
	defer srv.Close()

	p := newSQ104(newTestClient(t, srv.URL))
	hotspots, err := p.ExtractHotspots(context.Background(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(hotspots) != 1 || hotspots[0].Key != "hs-1" {
		t.Errorf("got %+v, want [{Key:hs-1}]", hotspots)
	}
}

// new403Server returns an httptest.Server that replies HTTP 403 to every
// request. The server is closed via t.Cleanup.
func new403Server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStandardExtractHotspotsError(t *testing.T) {
	srv := new403Server(t)

	p := newSQ99(newTestClient(t, srv.URL))
	_, err := p.ExtractHotspots(context.Background(), "proj")
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}
}

func TestStandardExtractMetrics(t *testing.T) {
	srv := httptest.NewServer(metricsHandler(t,
		map[string]string{"lines": "comp-a"},
		nil,
	))
	defer srv.Close()

	p := newSQ100(newTestClient(t, srv.URL))
	metrics, err := p.ExtractMetrics(context.Background(), "proj", []string{"lines"})
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || metrics[0].Component != "comp-a" {
		t.Errorf("got %+v, want [{Component:comp-a}]", metrics)
	}
}

func TestStandardExtractGroups(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"groups": []map[string]any{{"id": 1, "name": "devs", "description": "Developers", "membersCount": 5}},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
		})
	}))
	defer srv.Close()

	p := newSQ99(newTestClient(t, srv.URL))
	groups, err := p.ExtractGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Name != "devs" {
		t.Errorf("got %+v, want [{Name:devs}]", groups)
	}
}

func TestStandardExtractGroupsError(t *testing.T) {
	srv := new403Server(t)

	p := newSQ100(newTestClient(t, srv.URL))
	_, err := p.ExtractGroups(context.Background())
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}
}

func TestStandardEnrichCleanCode(t *testing.T) {
	p := newSQ104(nil)
	input := []Issue{{Key: "issue-1"}}
	result, err := p.EnrichCleanCode(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Key != "issue-1" {
		t.Errorf("EnrichCleanCode should return issues unchanged, got %+v", result)
	}
}
