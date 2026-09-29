// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import "time"

// SeedTaskDurations gives Tracker a real, per-task expected duration
// instead of treating every task in a category as equally costly (#564).
//
// The get* (extract-side) entries are the #564 values: averages mined from
// four ~1.3-1.5GB runs plus one complete small run.
//
// The migrate-side entries were re-mined for #597 from 17 successful
// migrate runs archived under ~/.sq-manager/migrations/runs (2026-09-14 to
// 2026-09-28), against the same SonarQube Server 2026.4.1 source and
// SonarQube Cloud staging target, spanning 1 to 15 projects, 14 to 11,463
// files and 13 to 46,305 issues. Each value is the median of the per-run
// durations that run_meta.json records for that task. The #564 originals
// are kept in a trailing comment so the size of the correction stays
// visible, and because several were wrong by two orders of magnitude:
// mis-sized seeds are what #598 identified as the remaining ETA error.
//
// Deliberately NOT rate-per-item, and #597 proposed that it should be. The
// same archive rules it out for the migrate path: importProjectData, which
// is 85-95% of every run measured, is dominated by target-side compute
// engine queue time, not by source size. Three runs of the identical
// project (zvec, 1,306 files) took 2m58s, 23m20s and 35m24s — a 12x spread
// at constant size. A per-file or per-issue rate fitted through that scores
// R^2 below 0.05. Every other migrate task except syncIssueMetadata scores
// a NEGATIVE R^2 against every candidate size metric, meaning size predicts
// them worse than a flat constant does. syncIssueMetadata is the one
// exception and is seeded per project below.
//
// Any task not listed here (mostly the many small orchestration tasks —
// createGates, createGroups, setNewCodePeriods, configurePortfolios, ...)
// falls back to DefaultTaskDuration.
var SeedTaskDurations = map[string]time.Duration{
	// --- migrate side, re-mined for #597 (n=17 runs) ---
	"importProjectData":                    472 * time.Second, // #564: 580s — already close; median 472s, p90 2124s
	"syncIssueMetadata":                    3 * time.Second,   // #564: 492s — 186x too big; overridden per project, see SecondsPerProjectIssueSync
	"createProjects":                       8 * time.Second,   // #564: 15s
	"setGlobalSettings":                    8 * time.Second,   // #564: 56s — 7x too big
	"grantMigrationUserProjectPermissions": 5 * time.Second,   // #564: 22s
	"setProjectGroupPermissions":           3 * time.Second,   // #564: 121s — 40x too big
	"restoreProfiles":                      1 * time.Second,   // #564: 33s — 263x too big
	"syncHotspotMetadata":                  1 * time.Second,   // #564: 54s — median 0.01s over 86 clean per-project samples; the 8m outliers were the #597 indexing-wait bug, not work
	"createProfiles":                       1 * time.Second,   // #564: 10s
	// setProfileParent (#564: 15s) dropped — it recorded 0.00s in all 17
	// runs, so DefaultTaskDuration is closer than any seed.

	// --- extract side, unchanged from #564 ---
	"getProjectSourceCode":        161 * time.Second,
	"getProjectSCMData":           136 * time.Second,
	"getProjectIssuesFull":        42 * time.Second,
	"getProjectAnalysisHistory":   41 * time.Second,
	"getPluginRules":              41 * time.Second,
	"getProjectFixedIssueTypes":   37 * time.Second,
	"getProjectHotspotsFull":      37 * time.Second,
	"getPluginIssues":             36 * time.Second,
	"getProjectComponentTree":     31 * time.Second,
	"getProjectVersions":          30 * time.Second,
	"getProjectIssueTypes":        29 * time.Second,
	"getProjectPluginIssues":      28 * time.Second,
	"getProjectRecentIssueTypes":  22 * time.Second,
	"getProjectMeasures":          21 * time.Second,
	"getProjectIssues":            21 * time.Second,
	"getProjectGroupsPermissions": 21 * time.Second,
	"getProjectLinks":             21 * time.Second,
	"getProjectSettings":          19 * time.Second,
	"getProjectBindings":          18 * time.Second,
	"getProjectDetails":           17 * time.Second,
	"getProjectAnalyses":          17 * time.Second,
	"getActiveProfileRules":       13 * time.Second,
	"getAcceptedIssues":           11 * time.Second,
}

// DefaultTaskDuration is the fallback expected duration for any task not
// explicitly seeded above — matches the ~2-8s range observed for the many
// small orchestration tasks in the mined logs.
const DefaultTaskDuration = 3 * time.Second

// SecondsPerHistoryPoint seeds the migrate package's migrateProjectHistory
// pseudo-task duration per history point migrated (#554/#564) — ~483
// points replayed in ~22m21s in a real run that exercised the feature.
const SecondsPerHistoryPoint = 2.8

// SecondsPerProjectIssueSync seeds syncIssueMetadata per project in scope
// (#597). It is the one migrate task with a positive fit against any size
// metric the migrate path knows for free: over the same 17 archived runs
// its duration regresses on project count at R^2 0.36 with a rate of 0.572
// s/project, against a NEGATIVE R^2 for every other task tried. The fit is
// weak, so RunMigrate floors the result at the SeedTaskDurations entry
// rather than letting a one-project run seed 0.6s.
//
// Measured spread behind the fit: 1 project ~2.5s, 2 ~2.9s, 7 ~2.2s,
// 15 ~4-21s.
const SecondsPerProjectIssueSync = 0.572

// ExpectedTaskDuration returns the seeded expected duration for name, or
// DefaultTaskDuration when name isn't explicitly seeded.
func ExpectedTaskDuration(name string) time.Duration {
	if d, ok := SeedTaskDurations[name]; ok {
		return d
	}
	return DefaultTaskDuration
}
