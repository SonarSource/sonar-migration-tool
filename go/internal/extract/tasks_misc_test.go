package extract

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Issue #278: on SonarQube Server 9.9, CE task types introduced in 10.x
// (GITHUB_AUTH_PROVISIONING etc.) are rejected with HTTP 400. fetchCETasks
// must treat that as a non-fatal "unsupported task type" and continue with
// the remaining types instead of aborting the whole extract.
func TestFetchCETasks_Skips400Types(t *testing.T) {
	var (
		called400 bool
		called200 int
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ce/activity", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("type") {
		case "GITHUB_AUTH_PROVISIONING":
			called400 = true
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"msg":"Value of parameter 'type' (GITHUB_AUTH_PROVISIONING) must be one of: [REPORT, ISSUE_SYNC]"}]}`))
		default:
			called200++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"paging": map[string]any{"total": 0},
				"tasks":  []map[string]any{},
			})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	e := &Executor{
		Raw:       NewRawClient(srv.Client(), srv.URL+"/"),
		Store:     NewDataStore(dir),
		ServerURL: srv.URL,
		Sem:       make(chan struct{}, 4),
		Logger:    slog.New(slog.NewTextHandler(&discardWriter{}, nil)),
	}

	types := []string{"REPORT", "ISSUE_SYNC", "GITHUB_AUTH_PROVISIONING", "AUDIT_PURGE"}
	if err := fetchCETasks(context.Background(), e, "getTasks", types, nil); err != nil {
		t.Fatalf("fetchCETasks should ignore 400s, got %v", err)
	}
	if !called400 {
		t.Error("expected the 400 type to have been requested")
	}
	if called200 != 3 {
		t.Errorf("expected 3 successful type queries, got %d", called200)
	}
}

// Non-400 errors must still abort the task — silently swallowing a 500
// or a network failure would hide real problems.
func TestFetchCETasks_PropagatesNon400Errors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ce/activity", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`server exploded`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	e := &Executor{
		Raw:       NewRawClient(srv.Client(), srv.URL+"/"),
		Store:     NewDataStore(dir),
		ServerURL: srv.URL,
		Sem:       make(chan struct{}, 4),
		Logger:    slog.New(slog.NewTextHandler(&discardWriter{}, nil)),
	}

	err := fetchCETasks(context.Background(), e, "getTasks", []string{"REPORT"}, nil)
	if err == nil {
		t.Fatal("expected an error to propagate")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected a 500 in the error, got %v", err)
	}
}
