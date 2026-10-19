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
	"strings"
	"testing"
	"time"
)

// newUsageExecutor returns an Executor whose RawClient talks to handler
// through client (srv.Client() when nil), and the buffer its logger
// writes to.
func newUsageExecutor(t *testing.T, handler http.HandlerFunc, client *http.Client) (*Executor, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	if client == nil {
		client = srv.Client()
	}
	var logs bytes.Buffer
	e := newTestExecutor(t)
	e.Raw = NewRawClient(client, srv.URL+"/")
	e.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	return e, &logs
}

func TestRunGetUsage_WritesProjects(t *testing.T) {
	e, _ := newUsageExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/license_usage" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"projects": []map[string]any{{"projectKey": "p1", "linesOfCode": 1200}},
		})
	}, nil)

	if err := runGetUsage(context.Background(), e); err != nil {
		t.Fatalf("runGetUsage: %v", err)
	}
	items, err := e.Store.ReadAll("getUsage")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !strings.Contains(string(items[0]), `"p1"`) {
		t.Fatalf("expected the one project written, got %s", items)
	}
}

// A gateway that gives up on license_usage must not stop the extract.
func TestRunGetUsage_HTTP504IsSkipped(t *testing.T) {
	e, logs := newUsageExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}, nil)

	if err := runGetUsage(context.Background(), e); err != nil {
		t.Fatalf("expected a 504 to be skipped, got %v", err)
	}
	if e.Store.TaskDirExists("getUsage") {
		t.Error("expected no getUsage data after a skipped fetch")
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "getUsage skipped") {
		t.Errorf("expected a getUsage skipped WARN, got %q", logs.String())
	}
}

// The reported failure: license_usage outlives the client's timeout.
func TestRunGetUsage_ClientTimeoutIsSkipped(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	e, logs := newUsageExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}, &http.Client{Timeout: 50 * time.Millisecond})

	if err := runGetUsage(context.Background(), e); err != nil {
		t.Fatalf("expected a client timeout to be skipped, got %v", err)
	}
	if !strings.Contains(logs.String(), "Client.Timeout exceeded") {
		t.Errorf("expected the timeout in the WARN, got %q", logs.String())
	}
}

// Cancellation (Ctrl-C, or another task failing the phase) still stops it.
func TestRunGetUsage_CancellationIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e, _ := newUsageExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}, nil)

	if err := runGetUsage(ctx, e); err == nil {
		t.Fatal("expected cancellation to be returned as an error")
	}
}
