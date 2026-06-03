package extract

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// With cfg.Debug=true, baseSDKOptions installs the HTTP debug logger so
// every API request the SDK makes emits a Debug slog entry. Verify by
// hitting an httptest server and checking the captured log output.
func TestBaseSDKOptions_DebugTrueEmitsHTTPLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`10.7.0.12345`))
	}))
	t.Cleanup(srv.Close)

	// Swap the slog default for one writing into a buffer so the debug
	// transport's output is observable.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg := ExtractConfig{
		URL:   srv.URL + "/",
		Token: "tok",
		Debug: true,
	}
	if _, err := detectVersion(context.Background(), cfg); err != nil {
		t.Fatalf("detectVersion: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `level=DEBUG`) {
		t.Errorf("expected a Debug-level entry, got %q", out)
	}
	if !strings.Contains(out, `msg="http request"`) {
		t.Errorf("expected the http-request debug entry, got %q", out)
	}
	if !strings.Contains(out, "/api/server/version") {
		t.Errorf("expected the captured URL to appear, got %q", out)
	}
}

// With cfg.Debug=false, no http-request Debug entries are emitted even
// when the default slog handler is set to Debug level.
func TestBaseSDKOptions_DebugFalseSuppressesHTTPLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`10.7.0.12345`))
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg := ExtractConfig{
		URL:   srv.URL + "/",
		Token: "tok",
		Debug: false,
	}
	if _, err := detectVersion(context.Background(), cfg); err != nil {
		t.Fatalf("detectVersion: %v", err)
	}

	if strings.Contains(buf.String(), `msg="http request"`) {
		t.Errorf("expected NO http-request entry without Debug, got %q", buf.String())
	}
}
