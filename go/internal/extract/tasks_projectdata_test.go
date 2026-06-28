// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestBuildBranchMap(t *testing.T) {
	branches := []json.RawMessage{
		json.RawMessage(`{"projectKey":"p1","name":"main","type":"LONG"}`),
		json.RawMessage(`{"projectKey":"p1","name":"develop","type":"LONG"}`),
		json.RawMessage(`{"projectKey":"p1","name":"feature-x","type":"SHORT"}`),
		json.RawMessage(`{"projectKey":"p2","name":"master","type":"BRANCH"}`),
		json.RawMessage(`{"projectKey":"","name":"orphan","type":"LONG"}`),
		json.RawMessage(`{"projectKey":"p3","name":"","type":"LONG"}`),
	}

	result := buildBranchMap(branches)

	if len(result["p1"]) != 2 {
		t.Errorf("p1: expected 2 branches, got %d", len(result["p1"]))
	}
	if result["p1"][0] != "main" || result["p1"][1] != "develop" {
		t.Errorf("p1: got %v", result["p1"])
	}
	if len(result["p2"]) != 1 || result["p2"][0] != "master" {
		t.Errorf("p2: expected [master], got %v", result["p2"])
	}
	if _, ok := result[""]; ok {
		t.Error("empty projectKey should be skipped")
	}
	if _, ok := result["p3"]; ok {
		t.Error("empty branch name should be skipped")
	}
}

func TestBuildBranchMapEmpty(t *testing.T) {
	result := buildBranchMap(nil)
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestBuildBranchMapShortFiltered(t *testing.T) {
	branches := []json.RawMessage{
		json.RawMessage(`{"projectKey":"p1","name":"pr-123","type":"short"}`),
		json.RawMessage(`{"projectKey":"p1","name":"pr-456","type":"SHORT"}`),
	}
	result := buildBranchMap(branches)
	if len(result["p1"]) != 0 {
		t.Errorf("expected no branches (all short), got %v", result["p1"])
	}
}

func TestProjectDataTasks(t *testing.T) {
	tasks := projectDataTasks()
	if len(tasks) != 6 {
		t.Fatalf("expected 6 project data tasks, got %d", len(tasks))
	}

	names := map[string]bool{}
	for _, task := range tasks {
		names[task.Name] = true
	}
	expected := []string{
		"getProjectIssuesFull",
		"getProjectHotspotsFull",
		"getProjectComponentTree",
		"getProjectSourceCode",
		"getProjectSCMData",
		"getProjectVersions",
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing task: %s", name)
		}
	}
}

func TestForEachProjectBranch(t *testing.T) {
	e := newTestExecutor(t)
	e.ServerURL = "http://test/"

	// Write project data.
	w, _ := e.Store.Writer("getProjects")
	for _, key := range []string{"p1", "p2"} {
		b, _ := json.Marshal(map[string]any{"key": key})
		w.WriteOne(b)
	}

	// Write branch data.
	bw, _ := e.Store.Writer("getBranches")
	for _, item := range []map[string]any{
		{"projectKey": "p1", "name": "main", "type": "LONG"},
		{"projectKey": "p1", "name": "develop", "type": "LONG"},
		{"projectKey": "p2", "name": "master", "type": "LONG"},
	} {
		b, _ := json.Marshal(item)
		bw.WriteOne(b)
	}

	var calls []string
	err := forEachProjectBranch(ctx(t), e, "testTask",
		func(ctx context.Context, projectKey, branch string, w *ChunkWriter) error {
			calls = append(calls, projectKey+"/"+branch)
			return nil
		})
	if err != nil {
		t.Fatalf("forEachProjectBranch: %v", err)
	}

	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %v", len(calls), calls)
	}
	expected := []string{"p1/main", "p1/develop", "p2/master"}
	for i, exp := range expected {
		if calls[i] != exp {
			t.Errorf("call[%d]: expected %s, got %s", i, exp, calls[i])
		}
	}
}

func TestForEachProjectBranchNoBranches(t *testing.T) {
	e := newTestExecutor(t)
	e.ServerURL = "http://test/"

	w, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	w.WriteOne(b)

	// Write empty branches.
	e.Store.Writer("getBranches")

	var calls []string
	err := forEachProjectBranch(ctx(t), e, "testTask",
		func(ctx context.Context, projectKey, branch string, w *ChunkWriter) error {
			calls = append(calls, projectKey+"/"+branch)
			return nil
		})
	if err != nil {
		t.Fatalf("forEachProjectBranch: %v", err)
	}
	if len(calls) != 1 || calls[0] != "p1/main" {
		t.Errorf("expected default branch [p1/main], got %v", calls)
	}
}

func TestForEachProjectBranchSkipped(t *testing.T) {
	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.RecordSkipped("p2")

	w, _ := e.Store.Writer("getProjects")
	for _, key := range []string{"p1", "p2"} {
		b, _ := json.Marshal(map[string]any{"key": key})
		w.WriteOne(b)
	}
	bw, _ := e.Store.Writer("getBranches")
	for _, item := range []map[string]any{
		{"projectKey": "p1", "name": "main", "type": "LONG"},
		{"projectKey": "p2", "name": "main", "type": "LONG"},
	} {
		b, _ := json.Marshal(item)
		bw.WriteOne(b)
	}

	var calls []string
	err := forEachProjectBranch(ctx(t), e, "testTask",
		func(ctx context.Context, projectKey, branch string, w *ChunkWriter) error {
			calls = append(calls, projectKey)
			return nil
		})
	if err != nil {
		t.Fatalf("forEachProjectBranch: %v", err)
	}
	if len(calls) != 1 || calls[0] != "p1" {
		t.Errorf("expected only p1 (p2 skipped), got %v", calls)
	}
}

func TestForEachProjectBranchError(t *testing.T) {
	e := newTestExecutor(t)
	e.ServerURL = "http://test/"

	w, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	w.WriteOne(b)
	e.Store.Writer("getBranches")

	err := forEachProjectBranch(ctx(t), e, "testTask",
		func(ctx context.Context, projectKey, branch string, w *ChunkWriter) error {
			return fmt.Errorf("boom")
		})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestProjectIssuesFullTask(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{
				{"key": "issue-1", "rule": "java:S100"},
			},
			"paging": map[string]any{"total": 1, "pageIndex": 1, "pageSize": 500},
		})
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	w.WriteOne(b)
	e.Store.Writer("getBranches")

	fn := projectIssuesFullTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("projectIssuesFullTask: %v", err)
	}

	items, _ := e.Store.ReadAll("getProjectIssuesFull")
	if len(items) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(items))
	}
}

// #400 regression: the /api/issues/search project filter must be
// passed as componentKeys (not components). SQ 9.9 only knows the
// historical componentKeys name; the newer components name is
// silently ignored there, so the API returns the global issue set
// (capped at 10k) for every project. The records then get enriched
// per-project, polluting each project's scanner-report import with
// the same ~10k server-wide issues — explaining the massive 9.9
// issue-loss bug.
func TestProjectIssuesFullTaskUsesComponentKeysParam(t *testing.T) {
	var (
		mu   sync.Mutex
		urls []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		urls = append(urls, r.URL.RawQuery)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{
				{"key": "issue-1", "rule": "java:S100"},
			},
			"paging": map[string]any{"total": 1, "pageIndex": 1, "pageSize": 500},
		})
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	w.WriteOne(b)
	e.Store.Writer("getBranches")

	if err := projectIssuesFullTask()(ctx(t), e); err != nil {
		t.Fatalf("projectIssuesFullTask: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(urls) == 0 {
		t.Fatal("expected at least one /api/issues/search call")
	}
	for _, q := range urls {
		if !strings.Contains(q, "componentKeys=p1") {
			t.Errorf("expected componentKeys=p1 in query, got %q", q)
		}
		// Guard against accidentally regressing back to the broken name.
		if strings.Contains(q, "components=p1") {
			t.Errorf("query must not use components=; SQ 9.9 ignores it. got %q", q)
		}
	}
}

// #398: with SkipIssueSync=true the issue search must NOT request
// additionalFields=_all (which is what brings comments / changelog).
// Without that gate, the heavy payload would still be fetched even
// though the operator opted out of the downstream sync.
func TestProjectIssuesFullTaskSkipIssueSync(t *testing.T) {
	var (
		mu     sync.Mutex
		params []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		params = append(params, r.URL.Query().Get("additionalFields"))
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{
				{"key": "issue-1", "rule": "java:S100"},
			},
			"paging": map[string]any{"total": 1, "pageIndex": 1, "pageSize": 500},
		})
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")
	e.SkipIssueSync = true

	w, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	w.WriteOne(b)
	e.Store.Writer("getBranches")

	if err := projectIssuesFullTask()(ctx(t), e); err != nil {
		t.Fatalf("projectIssuesFullTask: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(params) == 0 {
		t.Fatal("expected at least one /api/issues/search call")
	}
	for _, p := range params {
		if p != "" {
			t.Errorf("expected additionalFields to be unset with SkipIssueSync=true, got %q", p)
		}
	}
}

// #398: with SkipIssueSync=true the hotspot task must NOT fetch
// /api/hotspots/show per REVIEWED hotspot — that round-trip exists
// only to enrich the record with comments + rule for the migrate-side
// sync.
func TestProjectHotspotsFullTaskSkipsDetailEnrichment(t *testing.T) {
	var (
		mu       sync.Mutex
		showHits int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/hotspots/search":
			status := r.URL.Query().Get("status")
			if status == "REVIEWED" {
				json.NewEncoder(w).Encode(map[string]any{
					"hotspots": []map[string]any{
						{"key": "hs-1", "status": "REVIEWED", "resolution": "ACKNOWLEDGED"},
					},
					"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"hotspots": []map[string]any{},
				"paging":   map[string]any{"pageIndex": 1, "pageSize": 500, "total": 0},
			})
		case "/api/hotspots/show":
			mu.Lock()
			showHits++
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"key": "hs-1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")
	e.SkipIssueSync = true

	pw, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	pw.WriteOne(b)
	e.Store.Writer("getBranches")

	if err := projectHotspotsFullTask()(ctx(t), e); err != nil {
		t.Fatalf("projectHotspotsFullTask: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if showHits != 0 {
		t.Errorf("expected zero /api/hotspots/show calls with SkipIssueSync=true, got %d", showHits)
	}
}

// #323: SonarQube Server's /api/hotspots/search defaults to TO_REVIEW
// on several versions when `status` is omitted, silently dropping
// REVIEWED (incl. ACKNOWLEDGED) hotspots from the extract — so the
// migration sync code never sees a source for them. The extract task
// must issue both queries explicitly and merge.
func TestProjectHotspotsFullTaskQueriesBothStatuses(t *testing.T) {
	var (
		mu     sync.Mutex
		seen   []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hotspots/search" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		status := r.URL.Query().Get("status")
		mu.Lock()
		seen = append(seen, status)
		mu.Unlock()
		switch status {
		case "TO_REVIEW":
			json.NewEncoder(w).Encode(map[string]any{
				"hotspots": []map[string]any{
					{"key": "hs-tr", "status": "TO_REVIEW", "line": 5, "ruleKey": "rk1"},
				},
				"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
			})
		case "REVIEWED":
			json.NewEncoder(w).Encode(map[string]any{
				"hotspots": []map[string]any{
					{"key": "hs-ack", "status": "REVIEWED", "resolution": "ACKNOWLEDGED", "line": 10, "ruleKey": "rk1"},
				},
				"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
			})
		default:
			t.Errorf("unexpected status param: %q", status)
		}
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	pw, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	pw.WriteOne(b)
	e.Store.Writer("getBranches")

	fn := projectHotspotsFullTask()
	if err := fn(ctx(t), e); err != nil {
		t.Fatalf("projectHotspotsFullTask: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "TO_REVIEW" || seen[1] != "REVIEWED" {
		t.Errorf("expected per-status queries [TO_REVIEW, REVIEWED], got %v", seen)
	}

	items, _ := e.Store.ReadAll("getProjectHotspotsFull")
	keys := map[string]bool{}
	for _, raw := range items {
		keys[extractField(raw, "key")] = true
	}
	if !keys["hs-tr"] || !keys["hs-ack"] {
		t.Errorf("expected both hs-tr (TO_REVIEW) and hs-ack (ACKNOWLEDGED) extracted, got %v", keys)
	}
}

func TestProjectComponentTreeTask(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"components": []map[string]any{
				{"key": "p1:src/Main.java", "name": "Main.java", "language": "java"},
			},
			"paging": map[string]any{"total": 1, "pageIndex": 1, "pageSize": 500},
		})
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	w.WriteOne(b)
	e.Store.Writer("getBranches")

	fn := projectComponentTreeTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("projectComponentTreeTask: %v", err)
	}

	items, _ := e.Store.ReadAll("getProjectComponentTree")
	if len(items) != 1 {
		t.Fatalf("expected 1 component, got %d", len(items))
	}
}

func TestProjectSourceCodeTask(t *testing.T) {
	var capturedURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sources/raw" {
			capturedURL = r.URL.RawQuery
			w.Write([]byte("public class Main {}"))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	// Write dependency: component tree items.
	w, _ := e.Store.Writer("getProjectComponentTree")
	b, _ := json.Marshal(map[string]any{
		"key":        "p1:src/Main.java",
		"branch":     "main",
		"projectKey": "p1",
	})
	w.WriteOne(b)

	fn := projectSourceCodeTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("projectSourceCodeTask: %v", err)
	}

	items, _ := e.Store.ReadAll("getProjectSourceCode")
	if len(items) != 1 {
		t.Fatalf("expected 1 source, got %d", len(items))
	}
	src := extractField(items[0], "source")
	if src != "public class Main {}" {
		t.Errorf("unexpected source: %q", src)
	}
	// Branch must always be passed as a URL parameter.
	if !strings.Contains(capturedURL, "branch=main") {
		t.Errorf("expected branch=main in request URL, got %q", capturedURL)
	}
	// Branch must be preserved in the stored record.
	if extractField(items[0], "branch") != "main" {
		t.Errorf("expected branch 'main' in source record")
	}
}

func TestProjectSourceCodeTaskEmptyKey(t *testing.T) {
	e := newTestExecutor(t)
	e.ServerURL = "http://test/"

	w, _ := e.Store.Writer("getProjectComponentTree")
	b, _ := json.Marshal(map[string]any{"key": "", "branch": "main"})
	w.WriteOne(b)

	fn := projectSourceCodeTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("projectSourceCodeTask: %v", err)
	}

	items, _ := e.Store.ReadAll("getProjectSourceCode")
	if len(items) != 0 {
		t.Errorf("expected 0 items for empty key, got %d", len(items))
	}
}

func TestProjectSCMDataTask(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sources/scm" {
			json.NewEncoder(w).Encode(map[string]any{
				"scm": [][]any{
					{1, "author@example.com", "2024-01-01", "abc123"},
				},
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjectComponentTree")
	b, _ := json.Marshal(map[string]any{
		"key":        "p1:src/Main.java",
		"branch":     "main",
		"projectKey": "p1",
	})
	w.WriteOne(b)

	fn := projectSCMDataTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("projectSCMDataTask: %v", err)
	}

	items, _ := e.Store.ReadAll("getProjectSCMData")
	if len(items) != 1 {
		t.Fatalf("expected 1 SCM record, got %d", len(items))
	}
}

func TestProjectComponentTreeTaskNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	w.WriteOne(b)
	e.Store.Writer("getBranches")

	fn := projectComponentTreeTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("expected non-fatal skip, got error: %v", err)
	}
}

func TestProjectSourceCodeTaskNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjectComponentTree")
	b, _ := json.Marshal(map[string]any{"key": "p1:src/File.java", "branch": "main", "projectKey": "p1"})
	w.WriteOne(b)

	fn := projectSourceCodeTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("expected non-fatal skip, got error: %v", err)
	}
	// A source record must be written even on 404 (empty source, branch preserved)
	// so migration can distinguish "checked but purged" from "never attempted".
	items, _ := e.Store.ReadAll("getProjectSourceCode")
	if len(items) != 1 {
		t.Fatalf("expected 1 empty source record for 404, got %d", len(items))
	}
	if extractField(items[0], "source") != "" {
		t.Errorf("expected empty source for 404, got %q", extractField(items[0], "source"))
	}
	if extractField(items[0], "branch") != "main" {
		t.Errorf("expected branch preserved in record, got %q", extractField(items[0], "branch"))
	}
}

func TestProjectSourceCodeTaskLinesFallback(t *testing.T) {
	// api/sources/raw returns empty (housekeeping purged raw_source_data).
	// api/sources/lines should be called as a fallback and its HTML-highlighted
	// source stripped to plain text.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sources/raw":
			// 200 OK but empty body — source purged from raw_source_data column
			w.WriteHeader(200)
		case "/api/sources/lines":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"sources":[{"line":1,"code":"<span class=\"k\">public</span> class Foo {}"},{"line":2,"code":"  int x = 1 &amp; 2;"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjectComponentTree")
	b, _ := json.Marshal(map[string]any{
		"key":        "p1:src/Foo.java",
		"branch":     "main",
		"projectKey": "p1",
	})
	w.WriteOne(b)

	fn := projectSourceCodeTask()
	if err := fn(ctx(t), e); err != nil {
		t.Fatalf("projectSourceCodeTask: %v", err)
	}

	items, _ := e.Store.ReadAll("getProjectSourceCode")
	if len(items) != 1 {
		t.Fatalf("expected 1 source record, got %d", len(items))
	}
	got := extractField(items[0], "source")
	// HTML tags stripped, &amp; unescaped, lines joined with \n
	want := "public class Foo {}\n  int x = 1 & 2;"
	if got != want {
		t.Errorf("unexpected source from fallback:\n got:  %q\n want: %q", got, want)
	}
	if extractField(items[0], "branch") != "main" {
		t.Errorf("expected branch 'main' in source record")
	}
}

func TestProjectSourceCodeTaskEmptyBranch(t *testing.T) {
	e := newTestExecutor(t)
	e.ServerURL = "http://test/"

	// Component tree item with no branch field — should be skipped with a warning.
	w, _ := e.Store.Writer("getProjectComponentTree")
	b, _ := json.Marshal(map[string]any{"key": "p1:src/File.java", "projectKey": "p1"})
	w.WriteOne(b)

	fn := projectSourceCodeTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("projectSourceCodeTask: %v", err)
	}

	items, _ := e.Store.ReadAll("getProjectSourceCode")
	if len(items) != 0 {
		t.Errorf("expected 0 items for missing branch, got %d", len(items))
	}
}

func TestProjectSCMDataTaskNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjectComponentTree")
	b, _ := json.Marshal(map[string]any{"key": "p1:src/File.java", "branch": "main", "projectKey": "p1"})
	w.WriteOne(b)

	fn := projectSCMDataTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("expected non-fatal skip, got error: %v", err)
	}
}

// #410: SonarQube returns 404 from api/sources/scm for some files (notably on
// non-main branches) even though api/sources/lines serves the file and carries
// per-line blame. fetchSCMData must reconstruct the blame from sources/lines so
// those files migrate with real SCM instead of synthetic changesets.
func TestProjectSCMDataTaskFallsBackToLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sources/scm":
			w.WriteHeader(404) // mirror SonarQube's behavior for these files
		case "/api/sources/lines":
			json.NewEncoder(w).Encode(map[string]any{
				"sources": []map[string]any{
					{"line": 1, "code": "a", "scmAuthor": "alice@example.com", "scmDate": "2024-01-01T00:00:00+0000", "scmRevision": "rev1"},
					{"line": 2, "code": "b", "scmAuthor": "bob@example.com", "scmDate": "2024-02-01T00:00:00+0000", "scmRevision": "rev2"},
					{"line": 3, "code": "", "scmAuthor": "", "scmDate": "", "scmRevision": ""}, // no blame -> dropped
				},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjectComponentTree")
	b, _ := json.Marshal(map[string]any{"key": "p1:cli/audit.py", "branch": "release-3.x", "projectKey": "p1"})
	w.WriteOne(b)

	if err := projectSCMDataTask()(ctx(t), e); err != nil {
		t.Fatalf("projectSCMDataTask: %v", err)
	}

	items, _ := e.Store.ReadAll("getProjectSCMData")
	if len(items) != 1 {
		t.Fatalf("expected 1 reconstructed SCM record, got %d", len(items))
	}
	var rec struct {
		Key    string  `json:"key"`
		Branch string  `json:"branch"`
		Scm    [][]any `json:"scm"`
	}
	if err := json.Unmarshal(items[0], &rec); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if rec.Key != "p1:cli/audit.py" || rec.Branch != "release-3.x" {
		t.Errorf("wrong key/branch: %+v", rec)
	}
	if len(rec.Scm) != 2 { // line 3 had no blame -> dropped
		t.Fatalf("expected 2 blame rows, got %d: %v", len(rec.Scm), rec.Scm)
	}
	if rec.Scm[0][1] != "alice@example.com" || rec.Scm[1][1] != "bob@example.com" {
		t.Errorf("authors not carried into blame rows: %v", rec.Scm)
	}
}

func TestProjectIssuesFullTaskNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	w.WriteOne(b)
	e.Store.Writer("getBranches")

	fn := projectIssuesFullTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("expected non-fatal skip, got error: %v", err)
	}
}

func TestProjectVersionsTask(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/navigation/component" {
			json.NewEncoder(w).Encode(map[string]any{
				"key":     r.URL.Query().Get("component"),
				"name":    "My Project",
				"version": "2.5.0",
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	w.WriteOne(b)
	e.Store.Writer("getBranches")

	fn := projectVersionsTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("projectVersionsTask: %v", err)
	}

	items, _ := e.Store.ReadAll("getProjectVersions")
	if len(items) != 1 {
		t.Fatalf("expected 1 version record, got %d", len(items))
	}
	version := extractField(items[0], "version")
	if version != "2.5.0" {
		t.Errorf("expected version 2.5.0, got %s", version)
	}
	proj := extractField(items[0], "projectKey")
	if proj != "p1" {
		t.Errorf("expected projectKey p1, got %s", proj)
	}
	branch := extractField(items[0], "branch")
	if branch != "main" {
		t.Errorf("expected branch main, got %s", branch)
	}
}

func TestProjectVersionsTaskNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()

	e := newTestExecutor(t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("getProjects")
	b, _ := json.Marshal(map[string]any{"key": "p1"})
	w.WriteOne(b)
	e.Store.Writer("getBranches")

	fn := projectVersionsTask()
	err := fn(ctx(t), e)
	if err != nil {
		t.Fatalf("expected non-fatal skip, got error: %v", err)
	}
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}
