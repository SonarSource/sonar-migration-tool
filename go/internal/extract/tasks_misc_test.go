// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Issue #278: on SonarQube Server 9.9, CE task types introduced in 10.x
// (GITHUB_AUTH_PROVISIONING etc.) are rejected with HTTP 400. fetchCETasks
// must treat that as a non-fatal "unsupported task type" and continue with
// the remaining types instead of aborting the whole extract.
//
// Issue #533: once the 400's error message reveals the server's real
// supported set, any later type NOT in that set must be skipped with no
// HTTP request at all — AUDIT_PURGE (tried after GITHUB_AUTH_PROVISIONING,
// and absent from the mock's reported "[REPORT, ISSUE_SYNC]" set) must
// never be requested.
func TestFetchCETasks_Skips400Types(t *testing.T) {
	var (
		called400        bool
		called200        int
		calledAuditPurge bool
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ce/activity", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("type") {
		case "GITHUB_AUTH_PROVISIONING":
			called400 = true
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"msg":"Value of parameter 'type' (GITHUB_AUTH_PROVISIONING) must be one of: [REPORT, ISSUE_SYNC]"}]}`))
		case "AUDIT_PURGE":
			calledAuditPurge = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"paging": map[string]any{"total": 0},
				"tasks":  []map[string]any{},
			})
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
	if called200 != 2 {
		t.Errorf("expected 2 successful type queries (REPORT, ISSUE_SYNC), got %d", called200)
	}
	if calledAuditPurge {
		t.Error("AUDIT_PURGE is not in the server's reported valid set and must not be requested (issue #533)")
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

// Issue #533: SCA_RESCAN_BRANCH must be part of the maximal type list
// fetchCETasks tries, alongside every other documented CE task type.
func TestCETaskTypesIncludesSCARescanBranch(t *testing.T) {
	found := false
	for _, tt := range ceTaskTypes {
		if tt == "SCA_RESCAN_BRANCH" {
			found = true
		}
	}
	if !found {
		t.Errorf("ceTaskTypes = %v, want it to include SCA_RESCAN_BRANCH", ceTaskTypes)
	}
}

func TestParseValidCETaskTypes(t *testing.T) {
	tests := []struct {
		name      string
		msg       string
		wantTypes []string
		wantOK    bool
	}{
		{
			"well-formed message",
			"Value of parameter 'type' (PROJECT_IMPORT) must be one of: [REPORT, ISSUE_SYNC, AUDIT_PURGE]",
			[]string{"REPORT", "ISSUE_SYNC", "AUDIT_PURGE"},
			true,
		},
		{"single type", "must be one of: [REPORT]", []string{"REPORT"}, true},
		{"unrelated 400 body", "componentKeys must not be empty", nil, false},
		{"empty message", "", nil, false},
		{"empty bracket", "must be one of: []", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseValidCETaskTypes(tc.msg)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && !reflect.DeepEqual(got, tc.wantTypes) {
				t.Errorf("types = %v, want %v", got, tc.wantTypes)
			}
		})
	}
}

// Issue #533: the first "unsupported task type" 400 must log at INFO (not
// WARN, to avoid reading as a problem), and once the server's real
// supported set is known from that message, later unsupported types must
// produce no log line at all — not even INFO.
func TestFetchCETasks_DowngradesFirstUnsupportedTypeToInfo(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ce/activity", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("type") {
		case "REPORT":
			_ = json.NewEncoder(w).Encode(map[string]any{"paging": map[string]any{"total": 0}, "tasks": []map[string]any{}})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"msg":"Value of parameter 'type' (` + r.URL.Query().Get("type") + `) must be one of: [REPORT]"}]}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var logBuf bytes.Buffer
	dir := t.TempDir()
	e := &Executor{
		Raw:       NewRawClient(srv.Client(), srv.URL+"/"),
		Store:     NewDataStore(dir),
		ServerURL: srv.URL,
		Sem:       make(chan struct{}, 4),
		Logger:    slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	// PROJECT_IMPORT and VIEW_REFRESH both 400 with the same "[REPORT]"
	// valid set; REPORT succeeds. Mirrors the issue's log excerpt.
	types := []string{"PROJECT_IMPORT", "VIEW_REFRESH", "REPORT"}
	if err := fetchCETasks(context.Background(), e, "getTasks", types, nil); err != nil {
		t.Fatalf("fetchCETasks: %v", err)
	}

	logs := logBuf.String()
	if got := strings.Count(logs, "skipped task type"); got != 1 {
		t.Fatalf("expected exactly 1 skipped-task-type log line, got %d:\n%s", got, logs)
	}
	if !strings.Contains(logs, "level=INFO") {
		t.Errorf("the single unsupported-type log line must be INFO, got:\n%s", logs)
	}
	if strings.Contains(logs, "level=WARN") {
		t.Errorf("no WARN expected once the valid set is known from the first 400, got:\n%s", logs)
	}
}

// Issue #533: when the 400's error body doesn't match the expected "must
// be one of" shape, fetchCETasks must fall back to the pre-#533 behavior
// of trying every type — but only the first such warning is downgraded to
// INFO; later ones stay at WARN.
func TestFetchCETasks_FallsBackToPerTypeWarnOnUnparsableMessage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ce/activity", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"msg":"unsupported task type"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var logBuf bytes.Buffer
	dir := t.TempDir()
	e := &Executor{
		Raw:       NewRawClient(srv.Client(), srv.URL+"/"),
		Store:     NewDataStore(dir),
		ServerURL: srv.URL,
		Sem:       make(chan struct{}, 4),
		Logger:    slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	types := []string{"PROJECT_IMPORT", "VIEW_REFRESH"}
	if err := fetchCETasks(context.Background(), e, "getTasks", types, nil); err != nil {
		t.Fatalf("fetchCETasks: %v", err)
	}

	logs := logBuf.String()
	if got := strings.Count(logs, "skipped task type"); got != 2 {
		t.Fatalf("expected both unparsable types to be attempted and logged, got %d lines:\n%s", got, logs)
	}
	if got := strings.Count(logs, "level=INFO"); got != 1 {
		t.Errorf("expected exactly 1 INFO (the first), got %d:\n%s", got, logs)
	}
	if got := strings.Count(logs, "level=WARN"); got != 1 {
		t.Errorf("expected exactly 1 WARN (the second), got %d:\n%s", got, logs)
	}
}
