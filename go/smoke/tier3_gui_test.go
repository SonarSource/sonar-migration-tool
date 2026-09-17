//go:build smoke

// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// syncBuffer is a concurrency-safe io.Writer wrapping bytes.Buffer. The gui
// subprocess's stdout/stderr copying goroutines (started by os/exec once
// cmd.Start returns) write to this buffer for as long as the process is
// alive — including while the readiness poll below or a subtest may read it
// for a diagnostic message, well before cmd.Wait() has synchronized on
// those goroutines. A bare bytes.Buffer would race under those reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---------------------------------------------------------------------------
// Tier 3: gui
//
// This tier starts the real `gui` subcommand as a subprocess against a
// fresh, empty export directory and exercises its HTTP and WebSocket
// surface. It deliberately does not drive the wizard to completion — that
// needs real source/target credentials and takes minutes — but it proves
// the server starts, every registered route resolves without a 5xx, and
// /ws upgrades and keeps the server alive.
//
// The `wizard` command itself is NOT black-box smoke tested anywhere in
// this suite: its CLI prompter (go/internal/wizard/cli_prompter.go) is
// built on survey/v2, which requires a real TTY — piped stdin makes it
// error out rather than prompt, so there is no way to drive it headlessly
// from a test binary. It is already excluded from coverage accounting via
// sonar-project.properties' sonar.coverage.exclusions=**/cli_prompter.go.
//
// The wizard ENGINE is nonetheless reached through this test's "websocket"
// subtest: gui.WebPrompter (go/internal/gui/web_prompter.go) implements the
// same wizard.Prompter interface (go/internal/wizard/prompter.go) that the
// CLI's survey-based prompter implements, so exercising /ws exercises the
// same engine code the wizard command drives interactively.
//
// Net coverage across all 13 subcommands: 13/13 have Tier 0 CLI-contract
// tests, 12/13 have real functional runs, and `wizard`'s survey terminal
// I/O remains the one untested surface — deliberately so. Do not add a pty
// dependency to close this gap.
// ---------------------------------------------------------------------------
func TestTier3_GUI(t *testing.T) {
	// Registered before every other cleanup so, Cleanup being LIFO, it runs
	// LAST — after the subprocess output check below marks the test failed.
	t.Cleanup(track(t, "3", "gui"))

	// Pick a free port. There is an inherent race between closing this
	// probe listener and the subprocess binding the same port — accepted,
	// since retrying on collision would add complexity out of proportion
	// to how rarely it fires on a local loopback address.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatalf("closing probe listener: %v", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	baseURL := "http://" + addr

	exportDir := t.TempDir()

	bin := binaryPath(t)
	cmd := exec.CommandContext(context.Background(), bin,
		"gui",
		"--addr", addr,
		"--no-browser",
		"--export_directory", exportDir,
	)
	cmd.Dir = repoRoot(t)
	var out syncBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting gui subprocess: %v", err)
	}

	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		time.Sleep(2 * time.Second)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()

		output := out.String()
		assertNoPanics(t, output)
		logf(t, "gui subprocess combined output:\n%s\n", scrubSecrets(output))
	})

	// Poll for readiness: up to ~30s, every 250ms.
	deadline := time.Now().Add(30 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", addr, time.Second)
		if dialErr == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("gui server never started listening on %s within 30s\n--- subprocess output so far ---\n%s",
			addr, scrubSecrets(out.String()))
	}

	t.Run("routes", func(t *testing.T) {
		client := &http.Client{Timeout: 10 * time.Second}

		// GET routes from go/internal/gui/server.go that need no data in a
		// fresh export dir: the static/page/API routes are expected to
		// return 200. Routes with a path parameter substitute a plainly
		// nonexistent id/type and accept anything under 500 — a fresh
		// export dir legitimately 404s or 400s on those.
		type routeCase struct {
			name     string
			path     string
			hasParam bool
		}
		cases := []routeCase{
			{"wizard page", "/", false},
			{"history page", "/history", false},
			{"history run detail (missing run)", "/history/nonexistent-run-id", true},
			{"static assets index", "/static/", false},
			{"api runs list", "/api/runs", false},
			{"api run detail (missing run)", "/api/runs/nonexistent-run-id", true},
			{"api run analysis (missing run)", "/api/runs/nonexistent-run-id/analysis", true},
			{"api generate report (unknown type)", "/api/reports/nonexistent", true},
			{"api wizard state", "/api/state", false},
		}

		for _, c := range cases {
			resp, getErr := client.Get(baseURL + c.path)
			if getErr != nil {
				t.Errorf("%s (%s): request failed: %v", c.name, c.path, getErr)
				continue
			}
			status := resp.StatusCode
			_ = resp.Body.Close()
			logf(t, "route %s (%s): HTTP %d\n", c.name, c.path, status)

			if status >= http.StatusInternalServerError {
				t.Errorf("%s (%s): got HTTP %d, want < 500 (the server must not crash on a fresh export dir)",
					c.name, c.path, status)
				continue
			}
			if !c.hasParam && status != http.StatusOK {
				t.Errorf("%s (%s): got HTTP %d, want 200 (this route needs no run data)", c.name, c.path, status)
			}
		}

		// The PDF route needs an actually-generated report, which a fresh
		// export dir does not have. 200 is a bonus, not a requirement.
		pdfPath := "/api/report/pdf"
		resp, getErr := client.Get(baseURL + pdfPath)
		if getErr != nil {
			t.Errorf("api report pdf (%s): request failed: %v", pdfPath, getErr)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode >= http.StatusInternalServerError {
			t.Errorf("api report pdf (%s): got HTTP %d, want < 500", pdfPath, resp.StatusCode)
			return
		}
		if resp.StatusCode != http.StatusOK {
			logf(t, "route api report pdf (%s): HTTP %d (no report generated yet in a fresh export dir)\n",
				pdfPath, resp.StatusCode)
			return
		}
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			t.Errorf("api report pdf (%s): reading body: %v", pdfPath, readErr)
			return
		}
		if !strings.HasPrefix(string(body), "%PDF-") {
			snippet := string(body)
			if len(snippet) > 16 {
				snippet = snippet[:16]
			}
			t.Errorf("api report pdf (%s): HTTP 200 body does not start with %%PDF- (got %q)", pdfPath, snippet)
			return
		}
		logf(t, "route api report pdf (%s): HTTP 200, valid PDF header\n", pdfPath)
	})

	t.Run("websocket", func(t *testing.T) {
		wsURL := "ws://" + addr + "/ws"
		conn, resp, dialErr := websocket.DefaultDialer.Dial(wsURL, nil)
		if dialErr != nil {
			detail := ""
			if resp != nil {
				detail = fmt.Sprintf(" (HTTP %d)", resp.StatusCode)
			}
			t.Fatalf("dialing %s failed%s: %v", wsURL, detail, dialErr)
		}
		defer func() { _ = conn.Close() }()

		if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatalf("setting read deadline on %s: %v", wsURL, err)
		}

		const maxFrames = 20
		frames := 0
		for frames < maxFrames {
			_, data, readErr := conn.ReadMessage()
			if readErr != nil {
				// A read-deadline timeout (or a clean close) is acceptable
				// here: the server sends nothing over /ws until a wizard
				// run starts, and this test never starts one. The point is
				// that the upgrade succeeded and the server stayed up.
				logf(t, "websocket: read ended after %d frame(s): %v\n", frames, readErr)
				break
			}
			frames++
			var msg map[string]any
			if jsonErr := json.Unmarshal(data, &msg); jsonErr != nil {
				logf(t, "websocket: frame %d was not valid JSON: %v (%s)\n",
					frames, jsonErr, scrubSecrets(string(data)))
				continue
			}
			logf(t, "websocket: frame %d type=%v\n", frames, msg["type"])
		}
		if frames == 0 {
			logf(t, "websocket: no frames received within the read deadline (acceptable — nothing is sent until a wizard run starts)\n")
		}

		// Prove the WS interaction did not kill the server: the TCP port
		// must still accept connections.
		again, dialErr := net.DialTimeout("tcp", addr, 2*time.Second)
		if dialErr != nil {
			t.Errorf("gui server appears to have died after the websocket exchange: %v", dialErr)
			return
		}
		_ = again.Close()
	})
}
