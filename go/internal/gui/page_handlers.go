// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package gui

import (
	"net/http"
	"sort"

	"github.com/sonar-solutions/sonar-migration-tool/internal/analysis"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report/maturity"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report/migration"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// HistoryPageData is the template data for the history page.
type HistoryPageData struct {
	Runs         []RunInfo
	ActiveReport string
	ReportHTML   string
}

// RunDetailData is the template data for the run detail page.
type RunDetailData struct {
	RunID    string
	Metadata map[string]string
	Analysis []analysis.ReportRow
	Summary  AnalysisSummary
}

// AnalysisSummary holds computed totals for the analysis table.
type AnalysisSummary struct {
	Total   int
	Success int
	Failure int
}

// ReportPageData is the template data for a standalone report page.
type ReportPageData struct {
	HTML string
}

// WizardPageHandler renders the wizard page.
func WizardPageHandler(tmpl *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmpl.RenderHTTP(w, "wizard", PageData{ActiveNav: "wizard"})
	}
}

// HistoryPageHandler renders the history page with optional report.
func HistoryPageHandler(tmpl *Templates, exportDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := HistoryPageData{}
		data.Runs = listRuns(exportDir)
		data.ActiveReport = r.URL.Query().Get("report")

		if data.ActiveReport == "migration" || data.ActiveReport == "maturity" {
			data.ReportHTML = renderReport(exportDir, data.ActiveReport)
		}

		tmpl.RenderHTTP(w, "history", PageData{ActiveNav: "history", Data: data})
	}
}

// RunDetailPageHandler renders a single run's detail page.
func RunDetailPageHandler(tmpl *Templates, exportDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("runId")
		if !runIDPattern.MatchString(runID) {
			http.Error(w, "invalid run ID", http.StatusBadRequest)
			return
		}

		data := buildRunDetail(exportDir, runID)
		if data == nil {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}

		tmpl.RenderHTTP(w, "run_detail", PageData{ActiveNav: "history", Data: data})
	}
}

// listRuns returns all run directories sorted by recency.
func listRuns(exportDir string) []RunInfo {
	runs := fetchRunList(exportDir)
	sort.Slice(runs, func(i, j int) bool { return common.RunIDAfter(runs[i].RunID, runs[j].RunID) })
	return runs
}

// renderReport generates a report and converts markdown to HTML.
func renderReport(exportDir, reportType string) string {
	mapping, err := structure.GetUniqueExtracts(exportDir)
	if err != nil {
		return ""
	}

	var md string
	switch reportType {
	case "migration":
		md = migration.GenerateMigrationReport(exportDir, mapping)
	case "maturity":
		md = maturity.GenerateMaturityReport(exportDir, mapping)
	default:
		return ""
	}

	html, err := RenderMarkdown(md)
	if err != nil {
		return ""
	}
	return html
}

// buildRunDetail loads metadata and analysis for a single run.
func buildRunDetail(exportDir, runID string) *RunDetailData {
	meta := loadRunMetadata(exportDir, runID)
	if meta == nil {
		return nil
	}

	data := &RunDetailData{
		RunID:    runID,
		Metadata: meta,
	}

	rows := loadAnalysis(exportDir, runID)
	if rows != nil {
		data.Analysis = rows
		data.Summary = computeSummary(rows)
	}

	return data
}

// computeSummary calculates success/failure totals.
func computeSummary(rows []analysis.ReportRow) AnalysisSummary {
	s := AnalysisSummary{Total: len(rows)}
	for _, r := range rows {
		if r.Outcome == "success" {
			s.Success++
		}
	}
	s.Failure = s.Total - s.Success
	return s
}
