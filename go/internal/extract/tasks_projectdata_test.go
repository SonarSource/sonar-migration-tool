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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sources/raw" {
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
