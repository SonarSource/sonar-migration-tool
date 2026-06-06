// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

// Package scanreport builds and submits SonarScanner-compatible reports
// to SonarCloud's Compute Engine for project data migration.
package scanreport

import (
	"sort"
	"time"

	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

const (
	issueBatchSize = 5000
	oneDayMs       = int64(24 * time.Hour / time.Millisecond)
	stubAuthor     = "sonar-migration-tool@sonarcloud.io"
)

// ExtractedIssue holds the fields needed from an extracted SonarQube issue
// for changeset backdating.
type ExtractedIssue struct {
	Key          string
	Component    string
	CreationDate time.Time
	StartLine    int32
	EndLine      int32
}

// BackdateChangesets rewrites SCM changeset data so that the CE assigns each
// issue its original creation date. It builds per-file, per-line date maps
// from issue creation dates and reconstructs changeset entries accordingly.
//
// The changesets map is keyed by component key and modified in place.
func BackdateChangesets(issues []ExtractedIssue, changesets map[string]*pb.Changesets, fallbackDate time.Time) {
	if len(issues) == 0 {
		return
	}

	fallbackMs := fallbackDate.UnixMilli()
	overrides := buildSafetySplitOverrides(issues, fallbackMs)
	fileLineDates := buildFileLineDates(issues, overrides, fallbackMs)

	for compKey, lineDateMap := range fileLineDates {
		cs, ok := changesets[compKey]
		if !ok {
			continue
		}
		lineCount := len(cs.ChangesetIndexByLine)
		if lineCount == 0 {
			continue
		}
		rebuildChangesetForFile(cs, lineDateMap, lineCount)
	}
}

// rebuildChangesetForFile replaces the changeset entries and line index for a
// single file based on the per-line date map.
func rebuildChangesetForFile(cs *pb.Changesets, lineDateMap map[int32]int64, lineCount int) {
	uniqueDates := collectSortedDates(lineDateMap)
	dateToIdx := make(map[int64]int32, len(uniqueDates))
	entries := make([]*pb.Changesets_Changeset, len(uniqueDates))
	for i, dateMs := range uniqueDates {
		dateToIdx[dateMs] = int32(i)
		entries[i] = &pb.Changesets_Changeset{
			Revision: "migration-date-" + time.UnixMilli(dateMs).Format("20060102"),
			Author:   stubAuthor,
			Date:     dateMs,
		}
	}

	newIdx := make([]int32, lineCount)
	for i := range lineCount {
		if dateMs, ok := lineDateMap[int32(i+1)]; ok {
			newIdx[i] = dateToIdx[dateMs]
		}
		// else 0 — oldest date, prevents MAX inflation
	}

	cs.Changeset = entries
	cs.ChangesetIndexByLine = newIdx
}

// collectSortedDates returns sorted unique date values from a line-date map.
func collectSortedDates(lineDateMap map[int32]int64) []int64 {
	seen := make(map[int64]struct{})
	for _, d := range lineDateMap {
		seen[d] = struct{}{}
	}
	dates := make([]int64, 0, len(seen))
	for d := range seen {
		dates = append(dates, d)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i] < dates[j] })
	return dates
}

// buildSafetySplitOverrides splits any calendar day with >issueBatchSize issues
// into sub-groups with synthetic 1-day-apart dates.
// Returns a map of issueKey -> overridden dateMs.
func buildSafetySplitOverrides(issues []ExtractedIssue, fallbackMs int64) map[string]int64 {
	overrides := make(map[string]int64)

	type dayGroup struct {
		dateMs int64
		issues []ExtractedIssue
	}
	groups := make(map[int64]*dayGroup)

	for _, iss := range issues {
		dateMs := issueDateMs(iss, fallbackMs)
		dayKey := dateMs / oneDayMs
		g, ok := groups[dayKey]
		if !ok {
			g = &dayGroup{dateMs: dayKey * oneDayMs}
			groups[dayKey] = g
		}
		g.issues = append(g.issues, iss)
	}

	for _, g := range groups {
		if len(g.issues) <= issueBatchSize {
			continue
		}
		batches := groupIssuesIntoBatches(g.issues)
		total := len(batches)
		for batchIdx := range total - 1 {
			syntheticDate := g.dateMs - int64(total-1-batchIdx)*oneDayMs
			for _, iss := range batches[batchIdx] {
				overrides[iss.Key] = syntheticDate
			}
		}
	}
	return overrides
}

// groupIssuesIntoBatches splits sorted issues into batches of <=issueBatchSize
// without splitting files across batches.
func groupIssuesIntoBatches(issues []ExtractedIssue) [][]ExtractedIssue {
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].Component < issues[j].Component
	})

	var batches [][]ExtractedIssue
	var current []ExtractedIssue
	var currentFile string
	var fileBuffer []ExtractedIssue

	flush := func() {
		if len(fileBuffer) == 0 {
			return
		}
		if len(current)+len(fileBuffer) > issueBatchSize && len(current) > 0 {
			batches = append(batches, current)
			current = nil
		}
		current = append(current, fileBuffer...)
		fileBuffer = nil
	}

	for i := range issues {
		if issues[i].Component != currentFile {
			flush()
			currentFile = issues[i].Component
		}
		fileBuffer = append(fileBuffer, issues[i])
	}
	flush()
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

// buildFileLineDates builds a per-file, per-line date map from issues.
// For overlapping lines, the oldest date wins (prevents CE MAX inflation).
func buildFileLineDates(issues []ExtractedIssue, overrides map[string]int64, fallbackMs int64) map[string]map[int32]int64 {
	result := make(map[string]map[int32]int64)
	for _, iss := range issues {
		applyIssueDates(iss, overrides, fallbackMs, result)
	}
	return result
}

func applyIssueDates(iss ExtractedIssue, overrides map[string]int64, fallbackMs int64, result map[string]map[int32]int64) {
	startLine := iss.StartLine
	endLine := iss.EndLine
	if endLine == 0 {
		endLine = startLine
	}
	if startLine <= 0 || iss.Component == "" {
		return
	}

	dateMs := overrides[iss.Key]
	if dateMs == 0 {
		dateMs = issueDateMs(iss, fallbackMs)
	}

	lineDateMap, ok := result[iss.Component]
	if !ok {
		lineDateMap = make(map[int32]int64)
		result[iss.Component] = lineDateMap
	}

	for ln := startLine; ln <= endLine; ln++ {
		if existing, ok := lineDateMap[ln]; !ok || dateMs < existing {
			lineDateMap[ln] = dateMs
		}
	}
}

// issueDateMs returns the issue creation date as epoch milliseconds,
// falling back to the provided default.
func issueDateMs(iss ExtractedIssue, fallbackMs int64) int64 {
	if iss.CreationDate.IsZero() {
		return fallbackMs
	}
	return iss.CreationDate.UnixMilli()
}
