// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// requestLogBufferLimit bounds how many entries are held before the run
// directory exists. Pre-flight validation issues a handful of requests
// before the directory is created; anything beyond this is counted and
// dropped rather than grown without limit.
const requestLogBufferLimit = 2000

// requestLogWriter persists one JSON line per completed HTTP request to
// <runDir>/requests.log, in the shape the report pipeline parses:
//
//	{"process_type":"request_completed","status":"failure",
//	 "payload":{"method":"POST","url":"/api/settings/set","status":400,
//	            "data":{...form fields...},"response":"{\"errors\":[...]}"}}
//
// Seven report paths read this file — the Failed column in every section,
// the Failure Ledger, config partials, project failures, portfolio
// failures, permission-template routing and final_analysis_report.csv —
// and nothing had ever written it, so all of them silently produced empty
// results. One customer run logged 42,048 failed settings writes and the
// generated report showed Failed = 0 in every section.
//
// The writer is installed on the HTTP client before the run directory is
// known, so entries are buffered until Open is called.
type requestLogWriter struct {
	mu      sync.Mutex
	f       *os.File
	buf     []sqapi.RequestLogEntry
	dropped int
	closed  bool
}

func newRequestLogWriter() *requestLogWriter {
	return &requestLogWriter{}
}

// Log records one completed request. Safe for concurrent use; called from
// the HTTP transport.
func (w *requestLogWriter) Log(entry sqapi.RequestLogEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if w.f == nil {
		if len(w.buf) >= requestLogBufferLimit {
			w.dropped++
			return
		}
		w.buf = append(w.buf, entry)
		return
	}
	w.write(entry)
}

// Open points the writer at runDir and flushes anything buffered.
func (w *requestLogWriter) Open(runDir string, logger *slog.Logger) {
	f, err := os.OpenFile(filepath.Join(runDir, "requests.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logger.Warn("could not open requests.log; the report's Failed bucket and Failure Ledger will be empty",
			"err", err)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.f = f
	for _, entry := range w.buf {
		w.write(entry)
	}
	w.buf = nil
	if w.dropped > 0 {
		logger.Warn("some pre-flight requests were not recorded in requests.log",
			"dropped", w.dropped, "buffer_limit", requestLogBufferLimit)
	}
}

// Close flushes and releases the file.
func (w *requestLogWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
}

// write serializes one entry. Caller holds the lock.
func (w *requestLogWriter) write(entry sqapi.RequestLogEntry) {
	payload := map[string]any{
		"method": entry.Method,
		"url":    entry.URL,
		"status": entry.Status,
	}
	if len(entry.Data) > 0 {
		payload["data"] = entry.Data
	}
	if entry.Response != "" {
		payload["response"] = entry.Response
	}
	if entry.Err != "" {
		payload["error"] = entry.Err
	}
	status := "success"
	if entry.Err != "" || entry.Status >= 400 {
		status = "failure"
	}
	line, err := json.Marshal(map[string]any{
		"process_type": "request_completed",
		"status":       status,
		"payload":      payload,
	})
	if err != nil {
		return
	}
	_, _ = w.f.Write(append(line, '\n'))
}
