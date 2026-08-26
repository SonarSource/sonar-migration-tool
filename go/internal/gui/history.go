// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package gui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/sonar-solutions/sonar-migration-tool/internal/analysis"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report/maturity"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report/migration"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/sonar-solutions/sonar-migration-tool/internal/wizard"
)

const (
	headerContentType = "Content-Type"
	extractMetaFile   = "extract.json"
)

// runIDPattern matches date-based run IDs in either the historical
// MM-DD-YYYY-NN format (e.g. "04-27-2026-01") OR the new ISO
// YYYY-MM-DD-NN format introduced for issue #108 (e.g.
// "2026-04-27-01"). Accepting both keeps run directories from
// previous releases visible in the GUI history list and resumable
// via --run_id. The trailing counter is zero-padded to a minimum of
// two digits but grows unpadded past 99, so it must match "two or
// more digits", not exactly two (#542) — a fixed \d{2} would silently
// drop every run past the 99th of a day from the history list.
var runIDPattern = regexp.MustCompile(`^(\d{2}-\d{2}-\d{4}|\d{4}-\d{2}-\d{2})-\d{2,}$`)

// RunInfo summarises a single run directory for the history list.
type RunInfo struct {
	RunID       string `json:"run_id"`
	SourceURL   string `json:"source_url,omitempty"`
	HasReport   bool   `json:"has_report"`
	HasAnalysis bool   `json:"has_analysis"`
}

// --- Reusable data-fetching functions (used by both JSON API and page handlers) ---

// fetchRunList scans exportDir for run directories and returns unsorted results.
func fetchRunList(exportDir string) []RunInfo {
	entries, err := os.ReadDir(exportDir)
	if err != nil {
		return nil
	}

	var runs []RunInfo
	for _, e := range entries {
		if !e.IsDir() || !runIDPattern.MatchString(e.Name()) {
			continue
		}
		runs = append(runs, buildRunInfo(exportDir, e.Name()))
	}
	return runs
}

// buildRunInfo creates a RunInfo for a single run directory.
func buildRunInfo(exportDir, runID string) RunInfo {
	dir := filepath.Join(exportDir, runID)
	ri := RunInfo{RunID: runID}

	if data, err := os.ReadFile(filepath.Join(dir, extractMetaFile)); err == nil {
		var meta map[string]string
		if json.Unmarshal(data, &meta) == nil {
			ri.SourceURL = meta["url"]
		}
	}

	ri.HasAnalysis = fileExists(filepath.Join(dir, "requests.log"))
	ri.HasReport = fileExists(filepath.Join(dir, "final_analysis_report.csv"))
	return ri
}

// loadRunMetadata reads extract.json for a single run.
func loadRunMetadata(exportDir, runID string) map[string]string {
	dir := filepath.Join(exportDir, runID)
	data, err := os.ReadFile(filepath.Join(dir, extractMetaFile))
	if err != nil {
		return nil
	}
	var meta map[string]string
	if json.Unmarshal(data, &meta) != nil {
		return nil
	}
	return meta
}

// loadAnalysis generates analysis report rows for a run.
func loadAnalysis(exportDir, runID string) []analysis.ReportRow {
	dir := filepath.Join(exportDir, runID)
	rows, err := analysis.GenerateReport(dir)
	if err != nil {
		return nil
	}
	return rows
}

// --- JSON API handlers (kept for HTMX partial fetches) ---

// RunsListHandler returns all run directories in exportDir as JSON.
func RunsListHandler(exportDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runs := fetchRunList(exportDir)
		sort.Slice(runs, func(i, j int) bool { return common.RunIDAfter(runs[i].RunID, runs[j].RunID) })
		writeJSON(w, http.StatusOK, runs)
	}
}

// RunDetailHandler returns metadata for a single run as JSON.
func RunDetailHandler(exportDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("runId")
		if !runIDPattern.MatchString(runID) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run ID"})
			return
		}
		dir := filepath.Join(exportDir, runID)
		data, err := os.ReadFile(filepath.Join(dir, extractMetaFile))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
			return
		}
		w.Header().Set(headerContentType, "application/json")
		w.Write(data)
	}
}

// RunAnalysisHandler returns analysis report rows for a run as JSON.
func RunAnalysisHandler(exportDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("runId")
		if !runIDPattern.MatchString(runID) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run ID"})
			return
		}
		dir := filepath.Join(exportDir, runID)
		rows, err := analysis.GenerateReport(dir)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rows)
	}
}

// GenerateReportHandler generates a migration or maturity report on the fly.
func GenerateReportHandler(exportDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reportType := r.PathValue("type")
		mapping, err := structure.GetUniqueExtracts(exportDir)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		var md string
		switch reportType {
		case "migration":
			md = migration.GenerateMigrationReport(exportDir, mapping)
		case "maturity":
			md = maturity.GenerateMaturityReport(exportDir, mapping)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown report type"})
			return
		}

		w.Header().Set(headerContentType, "text/markdown; charset=utf-8")
		w.Write([]byte(md))
	}
}

// WizardStateHandler returns the current wizard state as JSON.
func WizardStateHandler(exportDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state, err := wizard.Load(exportDir)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, state)
	}
}

// ReportPDFHandler serves the migration_summary.pdf from exportDir.
func ReportPDFHandler(exportDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pdfPath := filepath.Join(exportDir, "migration_summary.pdf")
		if !fileExists(pdfPath) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "report not found"})
			return
		}
		w.Header().Set(headerContentType, "application/pdf")
		http.ServeFile(w, r, pdfPath)
	}
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set(headerContentType, "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
