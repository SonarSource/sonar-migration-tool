// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/wizard"
)

func setupTestExportDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create two run directories.
	run1 := filepath.Join(dir, "04-20-2026-01")
	run2 := filepath.Join(dir, "04-21-2026-01")
	os.MkdirAll(run1, 0o755)
	os.MkdirAll(run2, 0o755)

	// extract.json in run1.
	os.WriteFile(filepath.Join(run1, "extract.json"),
		[]byte(`{"url":"https://sonar.example.com/"}`), 0o644)

	// requests.log in run2 (indicates analysis available).
	os.WriteFile(filepath.Join(run2, "requests.log"), []byte("{}"), 0o644)

	// A non-matching directory (should be skipped).
	os.MkdirAll(filepath.Join(dir, "not-a-run"), 0o755)

	// A regular file (should be skipped).
	os.WriteFile(filepath.Join(dir, "organizations.csv"), []byte(""), 0o644)

	return dir
}

func TestRunsListHandler(t *testing.T) {
	dir := setupTestExportDir(t)
	handler := RunsListHandler(dir)

	req := httptest.NewRequest("GET", "/api/runs", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	var runs []RunInfo
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}

	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}

	// Sorted by run ID descending (newest first).
	if runs[0].RunID != "04-21-2026-01" {
		t.Errorf("first run: got %q, want 04-21-2026-01", runs[0].RunID)
	}
	if runs[1].RunID != "04-20-2026-01" {
		t.Errorf("second run: got %q, want 04-20-2026-01", runs[1].RunID)
	}

	// run1 has extract.json with URL.
	if runs[1].SourceURL != "https://sonar.example.com/" {
		t.Errorf("source_url: got %q", runs[1].SourceURL)
	}

	// run2 has requests.log.
	if !runs[0].HasAnalysis {
		t.Error("run2 should have analysis")
	}
}

func TestRunsListHandlerEmptyDir(t *testing.T) {
	dir := t.TempDir()
	handler := RunsListHandler(dir)

	req := httptest.NewRequest("GET", "/api/runs", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	var runs []RunInfo
	json.Unmarshal(w.Body.Bytes(), &runs)
	if runs != nil {
		t.Errorf("expected null/nil, got %v", runs)
	}
}

func TestRunDetailHandler(t *testing.T) {
	dir := setupTestExportDir(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs/{runId}", RunDetailHandler(dir))

	req := httptest.NewRequest("GET", "/api/runs/04-20-2026-01", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}

	var meta map[string]string
	json.Unmarshal(w.Body.Bytes(), &meta)
	if meta["url"] != "https://sonar.example.com/" {
		t.Errorf("url: got %q", meta["url"])
	}
}

func TestRunDetailHandlerNotFound(t *testing.T) {
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs/{runId}", RunDetailHandler(dir))

	req := httptest.NewRequest("GET", "/api/runs/99-99-9999-01", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestRunDetailHandlerInvalidID(t *testing.T) {
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs/{runId}", RunDetailHandler(dir))

	// Use a plausible but non-matching ID to test the regex validation.
	req := httptest.NewRequest("GET", "/api/runs/not-a-valid-id", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid run ID, got %d", w.Code)
	}
}

func TestWizardStateHandler(t *testing.T) {
	dir := t.TempDir()

	// Save a wizard state.
	state := &wizard.WizardState{Phase: wizard.PhaseExtract}
	state.Save(dir)

	handler := WizardStateHandler(dir)
	req := httptest.NewRequest("GET", "/api/state", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	var decoded wizard.WizardState
	json.Unmarshal(w.Body.Bytes(), &decoded)
	if decoded.Phase != wizard.PhaseExtract {
		t.Errorf("phase: got %q, want %q", decoded.Phase, wizard.PhaseExtract)
	}
}

func TestWizardStateHandlerNoState(t *testing.T) {
	dir := t.TempDir()

	handler := WizardStateHandler(dir)
	req := httptest.NewRequest("GET", "/api/state", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	// Should return init state.
	var decoded wizard.WizardState
	json.Unmarshal(w.Body.Bytes(), &decoded)
	if decoded.Phase != wizard.PhaseInit {
		t.Errorf("phase: got %q, want init", decoded.Phase)
	}
}

func TestGenerateReportHandlerInvalidType(t *testing.T) {
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/reports/{type}", GenerateReportHandler(dir))

	req := httptest.NewRequest("GET", "/api/reports/invalid", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestReportPDFHandler(t *testing.T) {
	dir := t.TempDir()
	pdfContent := []byte("%PDF-1.4 fake content")
	os.WriteFile(filepath.Join(dir, "migration_summary.pdf"), pdfContent, 0o644)

	handler := ReportPDFHandler(dir)
	req := httptest.NewRequest("GET", "/api/report/pdf", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/pdf" {
		t.Errorf("content-type: got %q, want application/pdf", ct)
	}
}

func TestReportPDFHandlerNotFound(t *testing.T) {
	dir := t.TempDir()

	handler := ReportPDFHandler(dir)
	req := httptest.NewRequest("GET", "/api/report/pdf", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestRunAnalysisHandlerInvalidID(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs/{runId}/analysis", RunAnalysisHandler(dir))

	req := httptest.NewRequest("GET", "/api/runs/bad-id/analysis", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func requireOK(t *testing.T, code int) {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
}

func TestRunAnalysisHandlerValidRun(t *testing.T) {
	dir := setupTestExportDir(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs/{runId}/analysis", RunAnalysisHandler(dir))

	req := httptest.NewRequest("GET", "/api/runs/04-20-2026-01/analysis", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	requireOK(t, w.Code)
}

func TestGenerateReportHandlerMigration(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/reports/{type}", GenerateReportHandler(dir))

	req := httptest.NewRequest("GET", "/api/reports/migration", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	requireOK(t, w.Code)
	ct := w.Header().Get("Content-Type")
	if ct != "text/markdown; charset=utf-8" {
		t.Errorf("content-type: got %q", ct)
	}
}

func TestGenerateReportHandlerMaturity(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/reports/{type}", GenerateReportHandler(dir))

	req := httptest.NewRequest("GET", "/api/reports/maturity", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	requireOK(t, w.Code)
}

func TestWizardStateHandlerError(t *testing.T) {
	dir := t.TempDir()
	// Make the state file a directory to trigger a Load error.
	os.Mkdir(filepath.Join(dir, ".wizard_state.json"), 0o755)

	handler := WizardStateHandler(dir)
	req := httptest.NewRequest("GET", "/api/state", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestLoadRunMetadataInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	runID := "04-20-2026-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)
	os.WriteFile(filepath.Join(runDir, extractMetaFile), []byte("{invalid}"), 0o644)

	result := loadRunMetadata(dir, runID)
	if result != nil {
		t.Error("expected nil for invalid JSON metadata")
	}
}

func TestLoadAnalysisNoData(t *testing.T) {
	dir := t.TempDir()
	result := loadAnalysis(dir, "nonexistent")
	if result != nil {
		t.Error("expected nil for missing analysis data")
	}
}

func TestRunIDPatternValidation(t *testing.T) {
	// Issue #108 — accept both the historical MM-DD-YYYY-NN format
	// (from previous releases) AND the new ISO YYYY-MM-DD-NN format
	// generated by generateRunID after the switch.
	valid := []string{
		"04-20-2026-01", "12-31-2025-99", "01-01-2000-01", // MM-DD-YYYY (legacy)
		"2026-04-20-01", "2025-12-31-99", "2000-01-01-01", // YYYY-MM-DD (ISO)
		// #542 — a 3+ digit daily counter (post-99, or the new
		// 4-digit zero-padded format) must still match.
		"2026-08-20-101", "04-20-2026-101", "2026-04-20-0001",
	}
	invalid := []string{
		"not-a-run", "04-20-2026", "2026-04-20", "../escape", "",
		"2026-4-20-01", "2026-04-20-1", // wrong digit counts
	}

	for _, id := range valid {
		if !runIDPattern.MatchString(id) {
			t.Errorf("%q should match", id)
		}
	}
	for _, id := range invalid {
		if runIDPattern.MatchString(id) {
			t.Errorf("%q should not match", id)
		}
	}
}

// TestRunsListHandler_OrdersNumericallyPast99 reproduces #542 at the GUI
// layer: once a day has both a two-digit and a three-digit run, the
// history list must put the three-digit (numerically later) run first,
// not the two-digit one that would sort first lexicographically.
func TestRunsListHandler_OrdersNumericallyPast99(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "2026-08-20-99"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "2026-08-20-101"), 0o755); err != nil {
		t.Fatal(err)
	}

	handler := RunsListHandler(dir)
	req := httptest.NewRequest("GET", "/api/runs", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	var runs []RunInfo
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].RunID != "2026-08-20-101" {
		t.Errorf("first (newest) run: got %q, want %q", runs[0].RunID, "2026-08-20-101")
	}
	if runs[1].RunID != "2026-08-20-99" {
		t.Errorf("second run: got %q, want %q", runs[1].RunID, "2026-08-20-99")
	}
}
