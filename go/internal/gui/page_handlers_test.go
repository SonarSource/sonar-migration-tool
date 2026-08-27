// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/analysis"
)

const runDetailRoute = "GET /runs/{runId}"

func requireStatus(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
}

func TestComputeSummary(t *testing.T) {
	rows := []analysis.ReportRow{
		{Outcome: "success"},
		{Outcome: "success"},
		{Outcome: "failure"},
		{Outcome: "error"},
	}

	s := computeSummary(rows)
	if s.Total != 4 {
		t.Errorf("Total = %d, want 4", s.Total)
	}
	if s.Success != 2 {
		t.Errorf("Success = %d, want 2", s.Success)
	}
	if s.Failure != 2 {
		t.Errorf("Failure = %d, want 2", s.Failure)
	}
}

func TestComputeSummaryEmpty(t *testing.T) {
	s := computeSummary(nil)
	if s.Total != 0 || s.Success != 0 || s.Failure != 0 {
		t.Errorf("expected all zeros, got %+v", s)
	}
}

func TestHistoryTemplateRenders(t *testing.T) {
	tmpl := mustParseTemplates(t)

	data := HistoryPageData{
		Runs: []RunInfo{
			{RunID: "04-29-2026-01", SourceURL: "https://sonar.example.com", HasAnalysis: true, HasReport: false},
		},
		ActiveReport: "",
	}

	w := renderToRecorder(t, tmpl, "history", PageData{ActiveNav: "history", Data: data})
	body := w.Body.String()

	assertContains(t, body, "04-29-2026-01", "run ID")
	assertContains(t, body, "sonar.example.com", "source URL")
	assertContains(t, body, "Run History", "page heading")
}

func TestRunDetailTemplateRenders(t *testing.T) {
	tmpl := mustParseTemplates(t)

	data := RunDetailData{
		RunID:    "04-29-2026-01",
		Metadata: map[string]string{"url": "https://sonar.example.com"},
		Analysis: []analysis.ReportRow{
			{EntityType: "Project", EntityName: "my-proj", Outcome: "success", HTTPStatus: "200"},
		},
		Summary: AnalysisSummary{Total: 1, Success: 1, Failure: 0},
	}

	w := renderToRecorder(t, tmpl, "run_detail", PageData{ActiveNav: "history", Data: data})
	body := w.Body.String()

	assertContains(t, body, "04-29-2026-01", "run ID")
	assertContains(t, body, "my-proj", "entity name")
	assertContains(t, body, "Analysis Report", "analysis heading")
}

func TestReportTemplateRenders(t *testing.T) {
	tmpl := mustParseTemplates(t)

	data := ReportPageData{HTML: "<h1>Test Report</h1>"}

	w := renderToRecorder(t, tmpl, "report", PageData{ActiveNav: "history", Data: data})
	body := w.Body.String()

	assertContains(t, body, "Test Report", "report content")
}

func TestRenderHTTPUnknownPage(t *testing.T) {
	tmpl := mustParseTemplates(t)

	w := httptest.NewRecorder()
	tmpl.RenderHTTP(w, "nonexistent", PageData{})

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for unknown page, got %d", w.Code)
	}
}

func TestWizardPageHandler(t *testing.T) {
	tmpl := mustParseTemplates(t)

	handler := WizardPageHandler(tmpl)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	requireStatus(t, w.Code, http.StatusOK)
	body := w.Body.String()
	assertContains(t, body, "wizard-stepper", "wizard stepper")
}

func TestRunDetailPageHandlerInvalidID(t *testing.T) {
	tmpl := mustParseTemplates(t)
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc(runDetailRoute, RunDetailPageHandler(tmpl, dir))

	req := httptest.NewRequest("GET", "/runs/invalid-id", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid run ID, got %d", w.Code)
	}
}

func TestRunDetailPageHandlerNotFound(t *testing.T) {
	tmpl := mustParseTemplates(t)
	dir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc(runDetailRoute, RunDetailPageHandler(tmpl, dir))

	req := httptest.NewRequest("GET", "/runs/01-01-2026-01", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent run, got %d", w.Code)
	}
}

func TestRunDetailPageHandlerSuccess(t *testing.T) {
	tmpl := mustParseTemplates(t)
	dir := t.TempDir()

	runID := "04-20-2026-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)
	os.WriteFile(filepath.Join(runDir, "extract.json"),
		[]byte(`{"url":"https://sonar.example.com"}`), 0o644)

	mux := http.NewServeMux()
	mux.HandleFunc(runDetailRoute, RunDetailPageHandler(tmpl, dir))

	req := httptest.NewRequest("GET", "/runs/"+runID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	requireStatus(t, w.Code, http.StatusOK)
	body := w.Body.String()
	assertContains(t, body, runID, "run ID in detail page")
}

func TestHistoryPageHandler(t *testing.T) {
	tmpl := mustParseTemplates(t)
	dir := t.TempDir()

	handler := HistoryPageHandler(tmpl, dir)
	req := httptest.NewRequest("GET", "/history", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	requireStatus(t, w.Code, http.StatusOK)
	body := w.Body.String()
	assertContains(t, body, "Run History", "history page heading")
}

func TestHistoryPageHandlerWithReportParam(t *testing.T) {
	tmpl := mustParseTemplates(t)
	dir := t.TempDir()

	handler := HistoryPageHandler(tmpl, dir)
	req := httptest.NewRequest("GET", "/history?report=migration", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	requireStatus(t, w.Code, http.StatusOK)
}

// TestListRuns_OrdersNumericallyPast99 reproduces #542 for the page
// (non-JSON-API) run listing path: a three-digit run must sort as
// newer than a two-digit one from the same day.
func TestListRuns_OrdersNumericallyPast99(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "2026-08-20-99"), 0o755)
	os.MkdirAll(filepath.Join(dir, "2026-08-20-101"), 0o755)

	runs := listRuns(dir)
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

func TestBuildRunDetailNil(t *testing.T) {
	dir := t.TempDir()
	result := buildRunDetail(dir, "nonexistent-01-01-2026-01")
	if result != nil {
		t.Error("expected nil for non-existent run")
	}
}

func TestBuildRunDetailWithAnalysis(t *testing.T) {
	dir := t.TempDir()
	runID := "04-20-2026-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)
	os.WriteFile(filepath.Join(runDir, "extract.json"),
		[]byte(`{"url":"https://sonar.example.com"}`), 0o644)

	result := buildRunDetail(dir, runID)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.RunID != runID {
		t.Errorf("RunID: got %q, want %q", result.RunID, runID)
	}
}

func TestRenderReportUnknownType(t *testing.T) {
	dir := t.TempDir()
	result := renderReport(dir, "unknown")
	if result != "" {
		t.Errorf("expected empty string for unknown report type, got %q", result)
	}
}

func TestListRunsSorted(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "01-01-2026-01"), 0o755)
	os.MkdirAll(filepath.Join(dir, "02-01-2026-01"), 0o755)

	runs := listRuns(dir)
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].RunID != "02-01-2026-01" {
		t.Errorf("expected newest first, got %q", runs[0].RunID)
	}
}
