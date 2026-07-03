// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report"
)

// RenderMarkdown builds the Markdown twin of the PDF migration report and
// returns the bytes. It mirrors RenderPDF's content exactly: the same
// title/metadata, executive-summary table, per-entity 5-status tables (via
// the shared buildUnifiedRows ordering), and the runtime sections
// (bottlenecks, failure ledger, warnings/retries/skips, branch project data,
// limitations).
//
// The output is deterministic: every map-derived collection is sorted before
// emission and the per-entity tables reuse buildUnifiedRows so their row order
// matches the PDF byte-for-byte. Each runtime section emits NOTHING when its
// backing data is empty, so a predictive summary (no runtime telemetry)
// renders only the title, executive summary, and per-entity tables.
//
// Cell safety: report.FormatValue does NOT escape Markdown table syntax, so
// every free-text cell passes through mdCell (pipe + newline escaping) or
// mdDetail (inline-bold markers → **…**, project markers stripped, then
// mdCell).
func RenderMarkdown(summary *MigrationSummary) ([]byte, error) {
	var sb strings.Builder

	renderMarkdownTitle(&sb, summary)
	renderMarkdownExecutiveSummary(&sb, summary)
	renderMarkdownProjectKeyConflicts(&sb, summary)
	renderMarkdownSections(&sb, summary)
	renderMarkdownBottlenecks(&sb, summary)
	renderMarkdownFailureLedger(&sb, summary)
	renderMarkdownWarnings(&sb, summary)
	renderMarkdownBranchProjectData(&sb, summary)
	renderMarkdownLimitations(&sb, summary)

	return []byte(sb.String()), nil
}

// mdCell makes an arbitrary free-text value safe to drop into a Markdown
// table cell: pipes are escaped so they don't split the row into extra
// columns, and embedded newlines become <br> so a multi-line value stays on
// a single table row. report.FormatValue performs no escaping of its own, so
// every free-text cell MUST pass through here.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", "<br>")
	return s
}

// mdDetail prepares a Details-column value for a Markdown cell. It converts
// the inline-bold private-use markers (migrate.InlineBoldStart/End) into
// Markdown bold (**…**), strips any leftover "|scan:" / "|ncdFallback:"
// project detail markers using the same parsing the PDF renderer uses, then
// applies the standard cell-safety escaping via mdCell.
func mdDetail(s string) string {
	// buildUnifiedRows (via successDetails / partialDetails) has already
	// expanded the |scan: and |ncdFallback: markers into their own
	// human-readable lines, so the string arriving here carries no raw
	// markers — only inline-bold spans and newlines. Re-splitting on
	// "|scan:" here would truncate the closing bold marker and drop the
	// trailing issue lines, so we do NOT parse markers; we only translate
	// the inline-bold spans to Markdown bold and then escape the cell
	// (mdCell turns the embedded newlines into <br> and escapes pipes).
	s = strings.ReplaceAll(s, migrate.InlineBoldStart, "**")
	s = strings.ReplaceAll(s, migrate.InlineBoldEnd, "**")
	return mdCell(s)
}

// renderMarkdownTitle writes the top-level heading plus the run metadata
// bullets. The predictive report uses the "Prediction Report" title and omits
// the runtime timing bullets (which carry zero values under prediction).
func renderMarkdownTitle(sb *strings.Builder, summary *MigrationSummary) {
	if summary.Predictive {
		sb.WriteString("# SonarQube Migration Prediction Report\n\n")
	} else {
		sb.WriteString("# SonarQube Migration Report\n\n")
	}

	fmt.Fprintf(sb, "- Run ID: %s\n", summary.RunID)
	fmt.Fprintf(sb, "- Generated: %s\n", summary.GeneratedAt.Format(dateTimeLayout))

	if !summary.Predictive && !summary.StartedAt.IsZero() {
		fmt.Fprintf(sb, "- Started: %s\n", summary.StartedAt.Format(dateTimeLayout))
		fmt.Fprintf(sb, "- Completed: %s\n", summary.CompletedAt.Format(dateTimeLayout))
		fmt.Fprintf(sb, "- Total elapsed: %s\n", fmtDuration(summary.TotalElapsed))
		fmt.Fprintf(sb, "- Overall status: %s\n", summary.OverallStatus)
	}
	sb.WriteString("\n")
}

// renderMarkdownExecutiveSummary writes the "## Executive Summary" table: one
// row per non-omitted section with its 5-status counts, plus a Totals row.
func renderMarkdownExecutiveSummary(sb *strings.Builder, summary *MigrationSummary) {
	columns := []report.Column{
		{Header: "Objects", Key: "objects"},
		// Outcome wording per #426 (legal); shared with the PDF renderer.
		{Header: outcomeSuccess, Key: "perfect"},
		{Header: outcomeNearPerfect, Key: "nearPerfect"},
		{Header: outcomePartial, Key: "partial"},
		{Header: outcomeFailed, Key: "failed"},
		{Header: outcomeSkipped, Key: "skipped"},
	}

	var rows []map[string]any
	var totalPerfect, totalNear, totalPartial, totalFailed, totalSkipped int
	for _, sec := range summary.Sections {
		if summary.OmitSections[sec.Name] {
			continue
		}
		perfect := len(sec.Succeeded)
		near := len(sec.NearPerfect)
		partial := len(sec.Partial)
		failed := len(sec.Failed)
		skipped := len(sec.Skipped)
		totalPerfect += perfect
		totalNear += near
		totalPartial += partial
		totalFailed += failed
		totalSkipped += skipped
		rows = append(rows, map[string]any{
			"objects":     mdCell(sec.Name),
			"perfect":     perfect,
			"nearPerfect": near,
			"partial":     partial,
			"failed":      failed,
			"skipped":     skipped,
		})
	}
	rows = append(rows, map[string]any{
		"objects":     "Total",
		"perfect":     totalPerfect,
		"nearPerfect": totalNear,
		"partial":     totalPartial,
		"failed":      totalFailed,
		"skipped":     totalSkipped,
	})

	sb.WriteString(report.GenerateSection(columns, rows, report.WithTitle("Executive Summary", 2)))
	sb.WriteString("\n")
}

// renderMarkdownSections writes one "## <Section.Name>" 5-status table per
// non-omitted section, reusing buildUnifiedRows so the row order matches the
// PDF. The Organization column is dropped for Portfolios and for predictive
// reports (mirroring renderUnifiedTable's hideOrg). Empty sections emit
// nothing.
func renderMarkdownSections(sb *strings.Builder, summary *MigrationSummary) {
	for _, section := range summary.Sections {
		if summary.OmitSections[section.Name] {
			continue
		}
		total := len(section.Succeeded) + len(section.NearPerfect) +
			len(section.Partial) + len(section.Failed) + len(section.Skipped)
		if total == 0 {
			continue
		}

		hideOrg := summary.Predictive || sectionsWithoutOrganization[section.Name]
		nameHeader := "Name"
		if section.Name == "Global Settings" {
			nameHeader = "Setting Key"
		}

		var columns []report.Column
		if hideOrg {
			columns = []report.Column{
				{Header: nameHeader, Key: "name"},
				{Header: "Outcome", Key: "outcome"},
				{Header: "Details", Key: "details"},
			}
		} else {
			columns = []report.Column{
				{Header: nameHeader, Key: "name"},
				{Header: "Organization", Key: "organization"},
				{Header: "Outcome", Key: "outcome"},
				{Header: "Details", Key: "details"},
			}
		}

		rows := buildUnifiedRows(section, summary.Predictive)
		mdRows := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			row := map[string]any{
				"name":    mdCell(r.displayName()),
				"outcome": r.outcome,
				"details": mdDetail(r.details),
			}
			if !hideOrg {
				row["organization"] = mdCell(r.org)
			}
			mdRows = append(mdRows, row)
		}

		sb.WriteString(report.GenerateSection(columns, mdRows,
			report.WithTitle(section.Name, 2),
			report.WithDescription(sectionCountSummary(section))))
		sb.WriteString("\n")
	}
}

// renderMarkdownBottlenecks writes the "## Bottlenecks" section: phase
// timings, the slowest tasks, and per-branch CE status. No-op when every
// backing collection is empty.
func renderMarkdownBottlenecks(sb *strings.Builder, summary *MigrationSummary) {
	if len(summary.Phases) == 0 && len(summary.Tasks) == 0 && len(summary.Branches) == 0 {
		return
	}

	sb.WriteString("## Bottlenecks\n\n")

	if len(summary.Phases) > 0 {
		columns := []report.Column{
			{Header: "Phase", Key: "phase"},
			{Header: "Tasks", Key: "tasks"},
			{Header: "Duration", Key: "duration"},
		}
		rows := make([]map[string]any, 0, len(summary.Phases))
		for _, p := range summary.Phases {
			rows = append(rows, map[string]any{
				"phase":    mdCell(p.Phase),
				"tasks":    p.Tasks,
				"duration": fmtDuration(p.Duration),
			})
		}
		sb.WriteString(report.GenerateSection(columns, rows,
			report.WithTitle("Phase Timings", 3)))
		sb.WriteString("\n")
	}

	if len(summary.Tasks) > 0 {
		columns := []report.Column{
			{Header: "Task", Key: "task"},
			{Header: "Phase", Key: "phase"},
			{Header: "Duration", Key: "duration"},
			{Header: "OK", Key: "ok"},
		}
		rows := make([]map[string]any, 0, len(summary.Tasks))
		for _, t := range summary.Tasks {
			rows = append(rows, map[string]any{
				"task":     mdCell(t.Task),
				"phase":    t.Phase,
				"duration": fmtDuration(t.Duration),
				"ok":       t.OK,
			})
		}
		sb.WriteString(report.GenerateSection(columns, rows,
			report.WithTitle("Slowest Tasks", 3)))
		sb.WriteString("\n")
	}

	if len(summary.Branches) > 0 {
		columns := []report.Column{
			{Header: "Branch", Key: "branch"},
			{Header: "Type", Key: "type"},
			{Header: "Status", Key: "status"},
			{Header: "Task Id", Key: "taskId"},
		}
		rows := make([]map[string]any, 0, len(summary.Branches))
		for _, b := range summary.Branches {
			rows = append(rows, map[string]any{
				"branch": mdCell(b.Branch),
				"type":   mdCell(b.Type),
				"status": mdCell(b.Status),
				"taskId": mdCell(b.TaskID),
			})
		}
		sb.WriteString(report.GenerateSection(columns, rows,
			report.WithTitle("Per-Branch CE", 3)))
		sb.WriteString("\n")
	}
}

// renderMarkdownFailureLedger writes the "## Failure Ledger" table. No-op when
// there are no failures.
func renderMarkdownFailureLedger(sb *strings.Builder, summary *MigrationSummary) {
	if len(summary.Failures) == 0 {
		return
	}

	columns := []report.Column{
		{Header: "Entity Type", Key: "entityType"},
		{Header: "Name", Key: "name"},
		{Header: "Organization", Key: "organization"},
		{Header: "HTTP", Key: "http"},
		{Header: "Error", Key: "error"},
	}
	rows := make([]map[string]any, 0, len(summary.Failures))
	for _, f := range summary.Failures {
		rows = append(rows, map[string]any{
			"entityType":   mdCell(f.EntityType),
			"name":         mdCell(f.EntityName),
			"organization": mdCell(f.Organization),
			"http":         mdCell(f.HTTPStatus),
			"error":        mdCell(f.ErrorMessage),
		})
	}
	sb.WriteString(report.GenerateSection(columns, rows,
		report.WithTitle("Failure Ledger", 2)))
	sb.WriteString("\n")
}

// renderMarkdownWarnings writes the "## Warnings, Retries & Skips" section
// with one sub-table per populated ledger collection: retried requests,
// branch skips, gate-condition skips/remaps, and metric remaps. No-op when
// the whole ledger is empty.
func renderMarkdownWarnings(sb *strings.Builder, summary *MigrationSummary) {
	w := summary.Warnings
	if len(w.Retries) == 0 && len(w.BranchSkips) == 0 &&
		len(w.GateConditions) == 0 && len(w.MetricRemaps) == 0 {
		return
	}

	sb.WriteString("## Warnings, Retries & Skips\n\n")

	if len(w.Retries) > 0 {
		columns := []report.Column{
			{Header: "Method", Key: "method"},
			{Header: "Endpoint", Key: "endpoint"},
			{Header: "Count", Key: "count"},
			{Header: "Max Attempt", Key: "maxAttempt"},
			{Header: "Last Status", Key: "lastStatus"},
		}
		rows := make([]map[string]any, 0, len(w.Retries))
		for _, r := range w.Retries {
			rows = append(rows, map[string]any{
				"method":     mdCell(r.Method),
				"endpoint":   mdCell(r.Endpoint),
				"count":      r.Count,
				"maxAttempt": r.MaxAttempt,
				"lastStatus": mdCell(r.LastStatus),
			})
		}
		sb.WriteString(report.GenerateSection(columns, rows,
			report.WithTitle("Retries", 3)))
		sb.WriteString("\n")
	}

	if len(w.BranchSkips) > 0 {
		columns := []report.Column{
			{Header: "Branch", Key: "branch"},
			{Header: "Findings", Key: "findings"},
			{Header: "Reason", Key: "reason"},
		}
		rows := make([]map[string]any, 0, len(w.BranchSkips))
		for _, s := range w.BranchSkips {
			rows = append(rows, map[string]any{
				"branch":   mdCell(s.Branch),
				"findings": s.Findings,
				"reason":   mdCell(s.Reason),
			})
		}
		sb.WriteString(report.GenerateSection(columns, rows,
			report.WithTitle("Branch Skips", 3)))
		sb.WriteString("\n")
	}

	if len(w.GateConditions) > 0 {
		columns := []report.Column{
			{Header: "Gate", Key: "gate"},
			{Header: "Metric", Key: "metric"},
			{Header: "Action", Key: "action"},
			{Header: "Note", Key: "note"},
		}
		rows := make([]map[string]any, 0, len(w.GateConditions))
		for _, g := range w.GateConditions {
			rows = append(rows, map[string]any{
				"gate":   mdCell(g.Gate),
				"metric": mdCell(g.Metric),
				"action": mdCell(g.Action),
				"note":   mdCell(g.Note),
			})
		}
		sb.WriteString(report.GenerateSection(columns, rows,
			report.WithTitle("Gate Condition Skips", 3)))
		sb.WriteString("\n")
	}

	if len(w.MetricRemaps) > 0 {
		columns := []report.Column{
			{Header: "Gate", Key: "gate"},
			{Header: "Source Metric", Key: "sourceMetric"},
			{Header: "Target Metric", Key: "targetMetric"},
		}
		rows := make([]map[string]any, 0, len(w.MetricRemaps))
		for _, m := range w.MetricRemaps {
			rows = append(rows, map[string]any{
				"gate":         mdCell(m.Gate),
				"sourceMetric": mdCell(m.SourceMetric),
				"targetMetric": mdCell(m.TargetMetric),
			})
		}
		sb.WriteString(report.GenerateSection(columns, rows,
			report.WithTitle("Metric Remaps", 3)))
		sb.WriteString("\n")
	}
}

// renderMarkdownBranchProjectData writes the "## Branch Project Data" table.
// No-op when there are no branches.
func renderMarkdownBranchProjectData(sb *strings.Builder, summary *MigrationSummary) {
	if len(summary.Branches) == 0 {
		return
	}

	columns := []report.Column{
		{Header: "Branch", Key: "branch"},
		{Header: "Type", Key: "type"},
		{Header: "Status", Key: "status"},
		{Header: "Issues", Key: "issues"},
		{Header: "External Issues", Key: "externalIssues"},
		{Header: "Components", Key: "components"},
		{Header: "Active Rules", Key: "activeRules"},
		{Header: "Zip Bytes", Key: "zipBytes"},
		{Header: "Task Id", Key: "taskId"},
		{Header: "Skip Reason", Key: "skipReason"},
	}
	rows := make([]map[string]any, 0, len(summary.Branches))
	for _, b := range summary.Branches {
		rows = append(rows, map[string]any{
			"branch":         mdCell(b.Branch),
			"type":           mdCell(b.Type),
			"status":         mdCell(b.Status),
			"issues":         b.Issues,
			"externalIssues": b.ExternalIssues,
			"components":     b.Components,
			"activeRules":    b.ActiveRules,
			"zipBytes":       int(b.ZipBytes),
			"taskId":         mdCell(b.TaskID),
			"skipReason":     mdCell(b.SkipReason),
		})
	}
	sb.WriteString(report.GenerateSection(columns, rows,
		report.WithTitle("Branch Project Data", 2)))
	sb.WriteString("\n")
}

// renderMarkdownLimitations writes the "## Migration Limitations" section: one
// bullet per limitation message. No-op when there are no limitations.
func renderMarkdownLimitations(sb *strings.Builder, summary *MigrationSummary) {
	if len(summary.Limitations) == 0 {
		return
	}

	// Copy + sort so the bullet order is deterministic regardless of the
	// collector's insertion order.
	lines := make([]string, len(summary.Limitations))
	copy(lines, summary.Limitations)
	sort.Strings(lines)

	sb.WriteString("## Migration Limitations\n\n")
	for _, line := range lines {
		fmt.Fprintf(sb, "- %s\n", mdCell(toPredictiveTense(line, summary.Predictive)))
	}
	sb.WriteString("\n")
}

// fmtDuration renders a time.Duration for the Markdown report. Durations are
// rounded to the millisecond so equal underlying values render identically,
// keeping the output deterministic.
func fmtDuration(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}
