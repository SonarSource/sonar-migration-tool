// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// swapBodyWriter redirects debugBodyWriter for the duration of the test
// so the multi-line body output is observable.
func swapBodyWriter(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := debugBodyWriter
	debugBodyWriter = &buf
	t.Cleanup(func() { debugBodyWriter = prev })
	return &buf
}

// JSON response bodies are pretty-printed on the body writer with real
// newlines so the structure is human-readable.
func TestNewHTTPDebugLogger_PrettyPrintsJSON(t *testing.T) {
	body := swapBodyWriter(t)
	var meta bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&meta, &slog.HandlerOptions{Level: slog.LevelDebug}))

	debug := NewHTTPDebugLogger(logger)
	debug("GET", "https://x/api/y", nil, nil, 200, []byte(`{"a":1,"nested":{"b":2}}`), nil)

	out := body.String()
	wantLines := []string{
		`  response_body:`,
		`    {`,
		`      "a": 1,`,
		`      "nested": {`,
		`        "b": 2`,
		`      }`,
		`    }`,
	}
	for _, line := range wantLines {
		if !strings.Contains(out, line) {
			t.Errorf("expected body output to contain line %q\nfull output:\n%s", line, out)
		}
	}
	// Meta line still flows through slog.
	if !strings.Contains(meta.String(), `msg="http request"`) {
		t.Errorf("expected slog meta entry, got %q", meta.String())
	}
	if strings.Contains(meta.String(), "response_body") {
		t.Errorf("body should not appear in slog meta entry, got %q", meta.String())
	}
}

// Non-JSON bodies fall through to verbatim string output.
func TestNewHTTPDebugLogger_NonJSONVerbatim(t *testing.T) {
	body := swapBodyWriter(t)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	debug := NewHTTPDebugLogger(logger)
	debug("GET", "https://x/api/y", nil, nil, 200, []byte("10.7.0.12345"), nil)

	if !strings.Contains(body.String(), `    10.7.0.12345`) {
		t.Errorf("expected verbatim plain-text body, got %q", body.String())
	}
}

// Request bodies are formatted the same way as response bodies.
func TestNewHTTPDebugLogger_PrettyPrintsRequestBody(t *testing.T) {
	body := swapBodyWriter(t)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	debug := NewHTTPDebugLogger(logger)
	debug("POST", "https://x/api/y", nil, []byte(`{"k":"v"}`), 200, nil, nil)

	out := body.String()
	if !strings.Contains(out, `  request_body:`) {
		t.Errorf("expected request_body label, got %q", out)
	}
	if !strings.Contains(out, `      "k": "v"`) {
		t.Errorf("expected request body to be indented JSON, got %q", out)
	}
}

// Error path: SDK reports a network failure. The meta line carries the
// err field; no body is emitted when there is none.
func TestNewHTTPDebugLogger_ErrorPath(t *testing.T) {
	body := swapBodyWriter(t)
	var meta bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&meta, &slog.HandlerOptions{Level: slog.LevelDebug}))

	debug := NewHTTPDebugLogger(logger)
	debug("GET", "https://x/api/y", nil, nil, 0, nil, &dnsError{})

	if !strings.Contains(meta.String(), `msg="http request failed"`) {
		t.Errorf("expected the failure message, got %q", meta.String())
	}
	if !strings.Contains(meta.String(), `err=`) {
		t.Errorf("expected err= field, got %q", meta.String())
	}
	if body.Len() != 0 {
		t.Errorf("expected no body output, got %q", body.String())
	}
}

// Binary bodies (NUL bytes, control bytes, invalid UTF-8) must be
// replaced with a "<binary, N bytes>" placeholder so a debug log
// against an endpoint that returns binary (e.g. /api/sources/raw on
// binary files, ZIPs, PDFs) doesn't garble the operator's terminal.
func TestNewHTTPDebugLogger_BinaryBodyReplaced(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{name: "NUL byte present", body: []byte{'h', 'i', 0x00, '!'}},
		{name: "control byte ESC", body: []byte{'h', 'i', 0x1b, '['}},
		{name: "invalid UTF-8", body: []byte{0xff, 0xfe, 0xfd}},
		{name: "PNG header", body: []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := swapBodyWriter(t)
			logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))

			debug := NewHTTPDebugLogger(logger)
			debug("GET", "https://x/api/y", nil, nil, 200, tc.body, nil)

			want := strings.Contains(body.String(), "<binary, ") && strings.Contains(body.String(), " bytes>")
			if !want {
				t.Errorf("expected binary placeholder, got %q", body.String())
			}
			// The raw bytes themselves must not appear.
			if bytes.Contains(body.Bytes(), []byte{0x00}) {
				t.Errorf("NUL byte leaked into debug output: %q", body.String())
			}
		})
	}
}

// Printable text (UTF-8 with non-ASCII chars) stays verbatim — the
// binary detector must not over-trigger on, say, French project names
// or other non-ASCII text that's still safe for the terminal.
func TestNewHTTPDebugLogger_NonASCIIUTF8KeptVerbatim(t *testing.T) {
	body := swapBodyWriter(t)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	debug := NewHTTPDebugLogger(logger)
	debug("GET", "https://x/api/y", nil, nil, 200, []byte("Bonjour — naïve café 한국어"), nil)

	out := body.String()
	if strings.Contains(out, "<binary") {
		t.Errorf("non-ASCII UTF-8 must not be classified as binary, got %q", out)
	}
	if !strings.Contains(out, "Bonjour — naïve café 한국어") {
		t.Errorf("expected verbatim non-ASCII text, got %q", out)
	}
}

// Empty bodies must not produce a labeled block.
func TestNewHTTPDebugLogger_EmptyBodiesSkipped(t *testing.T) {
	body := swapBodyWriter(t)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	debug := NewHTTPDebugLogger(logger)
	debug("GET", "https://x/api/y", nil, nil, 204, nil, nil)

	if body.Len() != 0 {
		t.Errorf("expected no body output for empty bodies, got %q", body.String())
	}
}

// Regression guard: a multi-MB JSON body must format and write in well
// under a second. A naive O(n²) implementation hung the extract command
// indefinitely when reformatting large responses like /api/rules/search.
func TestNewHTTPDebugLogger_LargeBodyTerminatesQuickly(t *testing.T) {
	body := swapBodyWriter(t)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// ~1.5 MB of valid JSON: a flat array of small objects.
	var sb strings.Builder
	sb.WriteString(`[`)
	for i := 0; i < 30_000; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"key":"foo","value":"bar","type":"CODE_SMELL"}`)
	}
	sb.WriteString(`]`)
	big := []byte(sb.String())

	debug := NewHTTPDebugLogger(logger)
	done := make(chan struct{})
	go func() {
		debug("GET", "https://x/api/y", nil, nil, 200, big, nil)
		close(done)
	}()

	select {
	case <-done:
		// success — indentBlock + write completed.
	case <-time.After(5 * time.Second):
		t.Fatal("debug logger did not format a 1.5MB body within 5s — likely O(n²) regression")
	}

	if body.Len() == 0 {
		t.Fatal("expected body output, got none")
	}
}

type dnsError struct{}

func (dnsError) Error() string { return "lookup x: no such host" }
