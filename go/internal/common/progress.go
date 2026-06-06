// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"fmt"
	"log/slog"
	"sync/atomic"
)

// ProgressLogger emits an INFO log line at regular intervals as a
// long-running task makes progress. Issue #202 introduced the
// pattern in the migrate package; #340 promoted it here so the same
// helper covers extract tasks too (the slow `getProject*` reads).
//
// Safe for concurrent use — the done counter is atomic so multiple
// goroutines fan-out-ing through `forEach*` helpers can all call
// Increment without coordination.
type ProgressLogger struct {
	task     string
	total    int
	done     atomic.Int64
	logger   *slog.Logger
	interval int64
}

// ProgressLogInterval names how often (in items) the progress logger
// should emit an INFO line for a given task. Per-task entries take
// precedence over the size-based fallback in NewProgressLogger.
//
// Cadence policy (#340): each task's interval is tuned so the log
// stream emits roughly every 5–10 seconds at the task's observed
// processing rate. Allowed values are {10, 20, 50, 100, 200, 500,
// 1000}. The default (set in NewProgressLogger) is 20 — applies to
// any task not listed here, including unmeasured ones.
//
// Entries below are derived from three reference runs:
// tasks_duration.log (migrate), reset.log (reset), all.log
// (extract). Tasks whose default-20 cadence already lands in the
// 5–10 s window are intentionally omitted.
//
// Many extract getProject* tasks fan out across ~78 projects in
// 5–8 s — fast enough that default-20 would emit a log every ~1 s.
// Bumping those to 100 hands operators a single "100%" end-of-task
// marker for short tasks (the interval caps at total) and a 5–10 s
// cadence for the 15 s+ ones that still iterate before completing.
//
// When a task appears in more than one run at different rates, we
// pick the interval that keeps the SLOWER run inside 5–10 s —
// over-logging on the fast side is preferable to leaving the
// operator wondering whether the migration is hung on the slow
// side. createPermissionTemplates is the one such overlap (13.3/s
// migrate, 6.0/s reset): 50 gives 8.4 s in reset versus 3.8 s in
// migrate, the right trade.
//
// importScanHistory / syncIssueMetadata / syncHotspotMetadata were
// not exercised by any reference run (project-data migration was
// off). Their tunings are preserved from #311 / #326 / #300 where
// per-project work was measured separately and known to be slow.
var ProgressLogInterval = map[string]int64{
	// Very fast tasks (>~100 items/s).
	"setOrgGroupPermissions": 1000,
	"updateRuleDescriptions": 1000,
	"updateRuleTags":         1000,
	"getGateConditions":      1000,
	// 50–100 items/s.
	"getProfileBackups": 500,
	"getProjectIds":     500,
	// 20–50 items/s.
	"createPortfolios":    200,
	"setNewCodePeriods":   200,
	"setDefaultProfiles":  200,
	"analyzeProfileRules": 200,
	"createGroups":        200,
	"createProfiles":      200,
	// 10–20 items/s.
	"setProjectGates":            100,
	"setProjectTags":             100,
	"setProjectGroupPermissions": 100,
	"setDefaultTemplates":        100,
	"getOrgRepos":                100,
	"deleteTemplates":            100,
	// Extract getProject* tasks at 10–20 items/s — pegged to 100
	// so short batches collapse to a single final 100% line and
	// long batches stay in the 5–10 s window.
	"getProjectSourceCode":        100,
	"getProjectSCMData":           100,
	"getProjectRecentIssueTypes":  100,
	"getProjectTags":              100,
	"getProjectSettings":          100,
	"getProjectIssueTypes":        100,
	"getProjectPullRequests":      100,
	"getProjectMeasures":          100,
	"getProjectLinks":             100,
	"getProjectIssues":            100,
	"getProjectGroupsPermissions": 100,
	"getProjectFixedIssueTypes":   100,
	// 5–10 items/s.
	"createProjects":                       50,
	"createGates":                          50,
	"setDefaultGates":                      50,
	"restoreProfiles":                      50,
	"setProjectLinks":                      50,
	"setProjectProfiles":                   50,
	"addMigrationGroupToTemplates":         50,
	"createMigrationGroups":                50,
	"setProfileParent":                     50,
	"setProjectSettings":                   50,
	"grantMigrationUserProjectPermissions": 50,
	"createPermissionTemplates":            50,
	"deleteProjects":                       50,
	// Extract getProject* tasks at 5–10 items/s.
	"getProjectHotspotsFull":  50,
	"getProjectComponentTree": 50,
	"getProjectVersions":      50,
	"getProjectPluginIssues":  50,
	"getPluginIssues":         50,
	// <2 items/s.
	"setProjectWebhooks":         10,
	"configurePortfolios":        10,
	"setProfileGroupPermissions": 10,
	"resetDefaultGates":          10,
	"resetDefaultProfiles":       10,
	"deleteProfiles":             10,
	"deleteGroups":               10,
	// Project-data migration tasks (see #311 / #326 / #300).
	"importScanHistory":   20,
	"syncIssueMetadata":   10,
	"syncHotspotMetadata": 10,
}

// NewProgressLogger creates a progress logger for the given task and
// expected total. Uses the per-task entry in ProgressLogInterval when
// present, else the 20-item default. Always caps interval at total so
// the very last item still emits a "100%" line via the n==total
// branch in Increment.
func NewProgressLogger(logger *slog.Logger, task string, total int) *ProgressLogger {
	// Default cadence is 20 items (#326 / #340).
	interval := int64(20)
	if int64(total) < interval {
		interval = int64(total)
	}
	if explicit, ok := ProgressLogInterval[task]; ok && explicit > 0 {
		interval = explicit
		if int64(total) < interval {
			interval = int64(total)
		}
	}
	return &ProgressLogger{task: task, total: total, logger: logger, interval: interval}
}

// NewProgressLoggerWithInterval creates a ProgressLogger with an
// explicit per-call interval, bypassing the global ProgressLogInterval
// map. Used by inner-loop progress tracking (e.g., per-issue / per-
// hotspot sync, #300) where the label is human-facing and shared by
// many call sites, but the interval differs per metric.
func NewProgressLoggerWithInterval(logger *slog.Logger, task string, total int, interval int64) *ProgressLogger {
	if interval <= 0 {
		interval = 1
	}
	if int64(total) < interval {
		interval = int64(total)
	}
	return &ProgressLogger{task: task, total: total, logger: logger, interval: interval}
}

// Increment records one completed item. When the running count hits
// a multiple of the interval (or the total exactly), emits an INFO
// log line of the form "<task> N/M - X%". Safe for concurrent use.
func (p *ProgressLogger) Increment() {
	if p.interval <= 0 {
		return
	}
	n := p.done.Add(1)
	if n%p.interval == 0 || int(n) == p.total {
		percent := 0
		if p.total > 0 {
			percent = int(n * 100 / int64(p.total))
		}
		p.logger.Info(fmt.Sprintf("%s %d/%d - %d%%", p.task, n, p.total, percent))
	}
}

// Interval returns the resolved per-iteration interval (testing only).
func (p *ProgressLogger) Interval() int64 { return p.interval }
