// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"sort"
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

// buildPhaseBreakdown measures how long each report phase took.
// Always returns all 4 entries, in runPhaseOrder, even when a phase had
// zero tasks (e.g. --skip_issue_sync) — a stable set of rows is clearer
// to read than a shrinking table.
//
// The duration is the wall-clock union of the phase's task intervals, not
// the sum of their durations. Tasks within a phase run concurrently, so
// summing counted the same seconds once per overlapping task: a 26.7s run
// reported four phases totalling 47.9s, 179% of the time that had
// actually passed. Runs recorded before task start times were written
// fall back to summing, which is the best the data supports.
//
// Each entry is now a true elapsed span for its own category, but the
// four are categories rather than a partition of the run: tasks of
// different categories can overlap, so the entries may still total
// slightly more than the run's elapsed time. They are meant to be read
// individually — "how long was project data being migrated" — not added
// up.
func buildPhaseBreakdown(tasks []TaskTiming) []PhaseBreakdownEntry {
	type interval struct{ start, end time.Time }

	spans := make(map[common.TaskCategory][]interval, len(runPhaseOrder))
	sums := make(map[common.TaskCategory]time.Duration, len(runPhaseOrder))
	for _, t := range tasks {
		category := migrate.CategorizeTask(t.Task)
		sums[category] += t.Duration
		if !t.StartedAt.IsZero() {
			spans[category] = append(spans[category],
				interval{start: t.StartedAt, end: t.StartedAt.Add(t.Duration)})
		}
	}

	out := make([]PhaseBreakdownEntry, 0, len(runPhaseOrder))
	for _, p := range runPhaseOrder {
		in := spans[p.category]
		if len(in) == 0 {
			out = append(out, PhaseBreakdownEntry{Name: p.name, Duration: sums[p.category]})
			continue
		}
		sort.Slice(in, func(i, j int) bool { return in[i].start.Before(in[j].start) })

		// Walk the intervals in start order, merging each into the open
		// one while it overlaps and banking the run when it does not.
		var total time.Duration
		open := in[0]
		for _, iv := range in[1:] {
			if iv.start.After(open.end) {
				total += open.end.Sub(open.start)
				open = iv
				continue
			}
			if iv.end.After(open.end) {
				open.end = iv.end
			}
		}
		total += open.end.Sub(open.start)
		out = append(out, PhaseBreakdownEntry{Name: p.name, Duration: total})
	}
	return out
}
