// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// Issue #278: /api/hotspots/search uses parameter "projectKey" on SQS 9.9
// and "project" on 10.x+. safeHotspotsTask must pick the right name based
// on the detected server version.
func TestSafeHotspotsTask_VersionedProjectParam(t *testing.T) {
	cases := []struct {
		name      string
		version   string
		wantParam string
		otherName string // server rejects this name to lock in the version dispatch
	}{
		{name: "SQS 9.9 requires projectKey", version: "9.9", wantParam: "projectKey", otherName: "project"},
		{name: "SQS 9.9.3 (with patch) still requires projectKey", version: "9.9.3.12345", wantParam: "projectKey", otherName: "project"},
		{name: "SQS 10.0 requires project", version: "10.0", wantParam: "project", otherName: "projectKey"},
		{name: "SQS 10.7 requires project", version: "10.7", wantParam: "project", otherName: "projectKey"},
		{name: "Year-versioned 2026.4 uses project", version: "2026.4.0.123541", wantParam: "project", otherName: "projectKey"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured string
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/hotspots/search", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get(tc.otherName) != "" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"errors":[{"msg":"wrong param"}]}`))
					return
				}
				captured = r.URL.Query().Get(tc.wantParam)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"paging":   map[string]any{"total": 0},
					"hotspots": []map[string]any{},
				})
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			dir := t.TempDir()
			store := NewDataStore(dir)
			ww, err := store.Writer("getProjects")
			if err != nil {
				t.Fatal(err)
			}
			if err := ww.WriteOne(json.RawMessage(`{"key":"my-project"}`)); err != nil {
				t.Fatal(err)
			}

			e := &Executor{
				Raw:       NewRawClient(srv.Client(), srv.URL+"/"),
				Store:     store,
				ServerURL: srv.URL,
				Version:   common.ParseVersion(tc.version),
				Sem:       make(chan struct{}, 2),
				Logger:    slog.New(slog.NewTextHandler(&discardWriter{}, nil)),
			}
			if err := safeHotspotsTask()(context.Background(), e); err != nil {
				t.Fatalf("safeHotspotsTask: %v", err)
			}
			if captured != "my-project" {
				t.Errorf("%s=my-project: got %q", tc.wantParam, captured)
			}
		})
	}
}

func TestHotspotsProjectParam(t *testing.T) {
	cases := []struct {
		version string
		want    string
	}{
		{"9.9", "projectKey"},
		{"9.9.3", "projectKey"},
		{"9.9.99999", "projectKey"},
		{"10.0", "project"},
		{"10.0.1", "project"},
		{"10.7", "project"},
		{"2025.1", "project"},
		{"2026.4.0.123541", "project"},
	}
	for _, tc := range cases {
		got := hotspotsProjectParam(common.ParseVersion(tc.version))
		if got != tc.want {
			t.Errorf("version %q: got %q, want %q", tc.version, got, tc.want)
		}
	}
}

// #654: with project_key_regexp, getProjects holds only the matching projects.
// No issue-search task may then query issues outside them — in particular none
// may send an api/issues/search request without componentKeys, which is
// instance-wide (on a 4M-issue server that is what made extract crawl).
func TestIssueTasksOnlyQueryTheSelectedProjects(t *testing.T) {
	const selected = "matching-project"
	var (
		mu  sync.Mutex
		bad []string
	)
	flag := func(q string) {
		mu.Lock()
		bad = append(bad, q)
		mu.Unlock()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("componentKeys") != selected {
			flag(r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"paging": map[string]any{"total": 0}, "issues": []any{}})
	})
	mux.HandleFunc("GET /api/hotspots/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project") != selected {
			flag(r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"paging": map[string]any{"total": 0}, "hotspots": []any{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	store := NewDataStore(t.TempDir())
	seed := func(task, record string) {
		ww, err := store.Writer(task)
		if err != nil {
			t.Fatal(err)
		}
		if err := ww.WriteOne(json.RawMessage(record)); err != nil {
			t.Fatal(err)
		}
	}
	seed("getProjects", `{"key":"`+selected+`"}`)

	e := &Executor{
		Raw:       NewRawClient(srv.Client(), srv.URL+"/"),
		Store:     store,
		ServerURL: srv.URL,
		Version:   common.ParseVersion("10.7"),
		Sem:       make(chan struct{}, 2),
		Logger:    slog.New(slog.NewTextHandler(&discardWriter{}, nil)),
	}
	for _, task := range issueTasks() {
		if err := task.Run(context.Background(), e); err != nil {
			t.Fatalf("%s: %v", task.Name, err)
		}
	}
	if len(bad) > 0 {
		t.Errorf("issue tasks queried outside the selected project: %v", bad)
	}
}

// Issue #668: getProjectPluginIssues and getProjectTemplateIssues made one
// api/issues/search call per 30 rules for every project and nothing read their
// output, so they must not be in the plan.
func TestIssueTasks_NoRuleJoinedIssueTasks(t *testing.T) {
	removed := map[string]bool{"getProjectPluginIssues": true, "getProjectTemplateIssues": true}
	for _, td := range RegisterAll() {
		if removed[td.Name] {
			t.Fatalf("task %s must not be registered: nothing consumes its output (#668)", td.Name)
		}
	}
}
