// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import "github.com/sonar-solutions/sonar-migration-tool/internal/common"

// extractProjectDataTasks and extractIssueSyncTasks split
// projectDataTaskNames (planner.go) for progress-weighting purposes only
// (#520) — gating still treats them as one group via IncludeProjectData.
// --skip_issue_sync does not remove tasks from the extract plan (it only
// drops additionalFields=_all / hotspot enrichment inside these two tasks'
// API calls), so on extract the IssueSync weight only ever falls to 0
// together with ProjectData, never independently. That's an honest
// reflection of the flag's real effect, and still satisfies "project data
// migration ... turned off" from the issue.
var extractProjectDataTasks = map[string]bool{
	"getProjectComponentTree": true,
	"getProjectSourceCode":    true,
	"getProjectSCMData":       true,
	"getProjectVersions":      true,
}

var extractIssueSyncTasks = map[string]bool{
	"getProjectIssuesFull":   true,
	"getProjectHotspotsFull": true,
}

// extractProjectConfigTasks lists every per-project task that isn't
// project-data or issue-sync — the getProject*/getBranches/getNewCodePeriods
// family that reads configuration and lightweight metadata for each project.
var extractProjectConfigTasks = map[string]bool{
	"getProjects":                 true,
	"getProjectTags":              true,
	"getProjectDetails":           true,
	"getProjectSettings":          true,
	"getProjectLinks":             true,
	"getProjectMeasures":          true,
	"getProjectWebhooks":          true,
	"getProjectBindings":          true,
	"getProjectGroupsPermissions": true,
	"getProjectUsersScanners":     true,
	"getProjectUsersViewers":      true,
	"getBranches":                 true,
	"getProjectPullRequests":      true,
	"getProjectWebhookDeliveries": true,
	"getProjectAnalyses":          true,
	"getProjectTasks":             true,
	"getNewCodePeriods":           true,
	"getProjectIssues":            true,
	"getAcceptedIssues":           true,
	"getProjectIssueTypes":        true,
	"getProjectFixedIssueTypes":   true,
	"getProjectRecentIssueTypes":  true,
	"getSafeHotspots":             true,
	"getProjectPluginIssues":      true,
	"getProjectTemplateIssues":    true,
}

// CategorizeTask buckets an extract task name for run-wide progress
// weighting (#520). Anything not explicitly listed is CategoryGeneral —
// server, user, rule, profile, gate, template, portfolio/app, and
// server-level webhook/misc tasks, none of which loop over projects as
// their primary axis.
func CategorizeTask(name string) common.TaskCategory {
	switch {
	case extractProjectDataTasks[name]:
		return common.CategoryProjectData
	case extractIssueSyncTasks[name]:
		return common.CategoryIssueSync
	case extractProjectConfigTasks[name]:
		return common.CategoryProjectConfig
	default:
		return common.CategoryGeneral
	}
}
