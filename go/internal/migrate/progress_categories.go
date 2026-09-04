// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import "github.com/sonar-solutions/sonar-migration-tool/internal/common"

// migrateProjectDataOnlyTasks is the ProjectData bucket for progress
// weighting (#520) — just importProjectData. migrateIssueSyncTasks
// (planner.go) lists exactly {syncHotspotMetadata, syncIssueMetadata}; for
// progress weighting the two are split (#526) — syncHotspotMetadata gets
// its own CategoryHotspotSync bucket, syncIssueMetadata stays IssueSync —
// since they run as separate, sequential trailing tasks and lumping them
// together made overall progress stick at ~100% while hotspot sync was
// still running.
var migrateProjectDataOnlyTasks = map[string]bool{
	"importProjectData": true,
}

// migrateProjectConfigTasks lists every per-project configuration task —
// everything that provisions or configures one project at a time, outside
// of the project-data import and issue/hotspot sync. Tasks that merely
// *depend* on createProjects for an API workaround (e.g. setGlobalSettings)
// are deliberately excluded — they are conceptually general, one-shot work.
var migrateProjectConfigTasks = map[string]bool{
	"createProjects":                       true,
	"getProjectIds":                        true,
	"getCreatedProjects":                   true,
	"deleteProjects":                       true,
	"setProjectProfiles":                   true,
	"setProjectGates":                      true,
	"setProjectGroupPermissions":           true,
	"setProjectSettings":                   true,
	"setProjectTags":                       true,
	"setProjectLinks":                      true,
	"setProjectSourceLink":                 true,
	"setProjectWebhooks":                   true,
	"setNewCodePeriods":                    true,
	"grantMigrationUserProjectPermissions": true,
	"matchProjectRepos":                    true,
	"setProjectBinding":                    true,
}

// CategorizeTask buckets a migrate task name for run-wide progress
// weighting (#520). Anything not explicitly listed is CategoryGeneral —
// mapping-generation, org-level create/configure/permission/portfolio/rule
// tasks, and delete/reset tasks, none of which loop over projects as their
// primary axis.
func CategorizeTask(name string) common.TaskCategory {
	switch {
	case migrateProjectDataOnlyTasks[name]:
		return common.CategoryProjectData
	case name == "syncHotspotMetadata":
		return common.CategoryHotspotSync
	case migrateIssueSyncTasks[name]:
		return common.CategoryIssueSync
	case migrateProjectConfigTasks[name]:
		return common.CategoryProjectConfig
	default:
		return common.CategoryGeneral
	}
}
