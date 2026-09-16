// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import "time"

// SeedTaskDurations gives Tracker a real, per-task expected duration
// instead of treating every task in a category as equally costly (#564).
// Values are averages mined from real extract/migrate run logs (four
// ~1.3-1.5GB runs plus one complete small run) — see issue #564 for the
// methodology. Any task not listed here (mostly the many small
// orchestration tasks — createGates, createGroups, setNewCodePeriods,
// configurePortfolios, ...) falls back to DefaultTaskDuration.
var SeedTaskDurations = map[string]time.Duration{
	"importProjectData":                    580 * time.Second,
	"syncIssueMetadata":                    492 * time.Second,
	"getProjectSourceCode":                 161 * time.Second,
	"getProjectSCMData":                    136 * time.Second,
	"setProjectGroupPermissions":           121 * time.Second,
	"setGlobalSettings":                    56 * time.Second,
	"syncHotspotMetadata":                  54 * time.Second,
	"getProjectIssuesFull":                 42 * time.Second,
	"getProjectAnalysisHistory":            41 * time.Second,
	"getPluginRules":                       41 * time.Second,
	"getProjectFixedIssueTypes":            37 * time.Second,
	"getProjectHotspotsFull":               37 * time.Second,
	"getPluginIssues":                      36 * time.Second,
	"restoreProfiles":                      33 * time.Second,
	"getProjectComponentTree":              31 * time.Second,
	"getProjectVersions":                   30 * time.Second,
	"getProjectIssueTypes":                 29 * time.Second,
	"getProjectPluginIssues":               28 * time.Second,
	"getProjectRecentIssueTypes":           22 * time.Second,
	"grantMigrationUserProjectPermissions": 22 * time.Second,
	"getProjectMeasures":                   21 * time.Second,
	"getProjectIssues":                     21 * time.Second,
	"getProjectGroupsPermissions":          21 * time.Second,
	"getProjectLinks":                      21 * time.Second,
	"getProjectSettings":                   19 * time.Second,
	"getProjectBindings":                   18 * time.Second,
	"getProjectDetails":                    17 * time.Second,
	"getProjectAnalyses":                   17 * time.Second,
	"setProfileParent":                     15 * time.Second,
	"createProjects":                       15 * time.Second,
	"getActiveProfileRules":                13 * time.Second,
	"getAcceptedIssues":                    11 * time.Second,
	"createProfiles":                       10 * time.Second,
}

// DefaultTaskDuration is the fallback expected duration for any task not
// explicitly seeded above — matches the ~2-8s range observed for the many
// small orchestration tasks in the mined logs.
const DefaultTaskDuration = 3 * time.Second

// SecondsPerHistoryPoint seeds the migrate package's migrateProjectHistory
// pseudo-task duration per history point migrated (#554/#564) — ~483
// points replayed in ~22m21s in a real run that exercised the feature.
const SecondsPerHistoryPoint = 2.8

// ExpectedTaskDuration returns the seeded expected duration for name, or
// DefaultTaskDuration when name isn't explicitly seeded.
func ExpectedTaskDuration(name string) time.Duration {
	if d, ok := SeedTaskDurations[name]; ok {
		return d
	}
	return DefaultTaskDuration
}
