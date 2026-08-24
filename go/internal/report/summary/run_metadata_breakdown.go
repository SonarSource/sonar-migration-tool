// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
)

// runPhaseOrder is the fixed, always-rendered order of #530's 4 report
// phases, each mapped from the common.TaskCategory migrate.CategorizeTask
// already assigns every task name for progress/ETA weighting (#520) — so
// this breakdown and the live progress bar always agree on what belongs
// where.
var runPhaseOrder = []struct {
	category common.TaskCategory
	name     string
}{
	{common.CategoryGeneral, "Global objects provisioning"},
	{common.CategoryProjectConfig, "Projects configuration provisioning"},
	{common.CategoryProjectData, "Project data migration"},
	{common.CategoryIssueSync, "Issue and Hotspot sync"},
}

// buildPhaseBreakdown sums each task's duration into its report phase.
// Always returns all 4 entries, in runPhaseOrder, even when a phase had
// zero tasks (e.g. --skip_issue_sync) — a stable set of rows is clearer
// to read than a shrinking table.
func buildPhaseBreakdown(tasks []TaskTiming) []PhaseBreakdownEntry {
	totals := make(map[common.TaskCategory]time.Duration, len(runPhaseOrder))
	for _, t := range tasks {
		totals[migrate.CategorizeTask(t.Task)] += t.Duration
	}
	out := make([]PhaseBreakdownEntry, 0, len(runPhaseOrder))
	for _, p := range runPhaseOrder {
		out = append(out, PhaseBreakdownEntry{Name: p.name, Duration: totals[p.category]})
	}
	return out
}
