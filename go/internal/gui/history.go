package gui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/sonar-solutions/sonar-migration-tool/internal/analysis"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report/migration"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report/maturity"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/sonar-solutions/sonar-migration-tool/internal/wizard"
)

const headerContentType = "Content-Type"

// runIDPattern matches date-based run IDs like "04-27-2026-01".
var runIDPattern = regexp.MustCompile(`^\d{2}-\d{2}-\d{4}-\d{2}$`)

// RunInfo summarises a single run directory for the history list.
type RunInfo struct {
	RunID       string `json:"run_id"`
	SourceURL   string `json:"source_url,omitempty"`
	HasReport   bool   `json:"has_report"`
	HasAnalysis bool   `json:"has_analysis"`
}

// RunsListHandler returns all run directories in exportDir.
func RunsListHandler(exportDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries, err := os.ReadDir(exportDir)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		var runs []RunInfo
		for _, e := range entries {
			if !e.IsDir() || !runIDPattern.MatchString(e.Name()) {
				continue
			}
			dir := filepath.Join(exportDir, e.Name())
			ri := RunInfo{RunID: e.Name()}

			// Read extract.json for source URL.
			if data, err := os.ReadFile(filepath.Join(dir, "extract.json")); err == nil {
				var meta map[string]string
				if json.Unmarshal(data, &meta) == nil {
					ri.SourceURL = meta["url"]
				}
			}

			ri.HasAnalysis = fileExists(filepath.Join(dir, "requests.log"))
			ri.HasReport = fileExists(filepath.Join(dir, "final_analysis_report.csv"))
			runs = append(runs, ri)
		}

		sort.Slice(runs, func(i, j int) bool { return runs[i].RunID > runs[j].RunID })
		writeJSON(w, http.StatusOK, runs)
	}
}

// RunDetailHandler returns metadata for a single run.
func RunDetailHandler(exportDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("runId")
		if !runIDPattern.MatchString(runID) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run ID"})
			return
		}
		dir := filepath.Join(exportDir, runID)
		data, err := os.ReadFile(filepath.Join(dir, "extract.json"))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
			return
		}
		w.Header().Set(headerContentType, "application/json")
		w.Write(data)
	}
}

// RunAnalysisHandler returns analysis report rows for a run.
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

// WizardStateHandler returns the current wizard state.
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
