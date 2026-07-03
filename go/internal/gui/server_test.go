// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package gui

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerStartAndShutdown(t *testing.T) {
	wp := NewWebPrompter(context.Background(), func(ServerMessage) {})
	hub := NewHub(wp)
	tmpl := mustParseTemplates(t)

	srv := NewServer("localhost:0", t.TempDir(), hub, tmpl)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Wait for ready.
	select {
	case <-srv.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("server did not become ready")
	}

	url := srv.URL()
	if url == "" {
		t.Fatal("URL should not be empty after ready")
	}

	// Verify it serves the wizard page.
	resp, err := http.Get(url + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / status: %d", resp.StatusCode)
	}

	// Shutdown.
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}

// startTestServerWithRun creates a server backed by an export dir that already
// contains one run directory (04-20-2026-01) with an extract.json stub.
// The context is cancelled via t.Cleanup. Returns the server's base URL.
func startTestServerWithRun(t *testing.T) string {
	t.Helper()
	wp := NewWebPrompter(context.Background(), func(ServerMessage) {})
	hub := NewHub(wp)
	tmpl := mustParseTemplates(t)
	exportDir := t.TempDir()
	runDir := filepath.Join(exportDir, "04-20-2026-01")
	os.MkdirAll(runDir, 0o755)
	os.WriteFile(filepath.Join(runDir, "extract.json"),
		[]byte(`{"url":"https://test.com/"}`), 0o644)
	srv := NewServer("localhost:0", exportDir, hub, tmpl)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Start(ctx)
	<-srv.Ready()
	return srv.URL()
}

func TestServerAPIRoutes(t *testing.T) {
	base := startTestServerWithRun(t)

	// Test /api/runs.
	resp, err := http.Get(base + "/api/runs")
	if err != nil {
		t.Fatalf("GET /api/runs: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/api/runs status: %d", resp.StatusCode)
	}

	// Test /api/state.
	resp, err = http.Get(base + "/api/state")
	if err != nil {
		t.Fatalf("GET /api/state: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/api/state status: %d", resp.StatusCode)
	}

	// Test /api/report/pdf (no PDF exists, expect 404).
	resp, err = http.Get(base + "/api/report/pdf")
	if err != nil {
		t.Fatalf("GET /api/report/pdf: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/api/report/pdf status: got %d, want 404", resp.StatusCode)
	}
}

func TestServerPageRoutes(t *testing.T) {
	base := startTestServerWithRun(t)

	routes := []struct {
		path       string
		wantStatus int
	}{
		{"/", http.StatusOK},
		{"/history", http.StatusOK},
		{"/history/04-20-2026-01", http.StatusOK},
		{"/static/app.css", http.StatusOK},
		{"/static/htmx.min.js", http.StatusOK},
	}

	for _, r := range routes {
		resp, err := http.Get(base + r.path)
		if err != nil {
			t.Fatalf("GET %s: %v", r.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != r.wantStatus {
			t.Errorf("GET %s status = %d, want %d", r.path, resp.StatusCode, r.wantStatus)
		}
	}
}

func TestServerURLBeforeReady(t *testing.T) {
	srv := NewServer("localhost:0", t.TempDir(), nil, nil)
	if url := srv.URL(); url != "" {
		t.Errorf("URL before start should be empty, got %q", url)
	}
}
