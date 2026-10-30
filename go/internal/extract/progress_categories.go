// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import "github.com/sonar-solutions/sonar-migration-tool/internal/common"

// extractProjectDataTasks and extractIssueSyncTasks split
// projectDataTaskNames (planner.go) into stages (#520) — gating still
// treats them as one group via IncludeProjectData. --skip_issue_sync does
// not remove tasks from the extract plan (it only drops
// additionalFields=_all / hotspot enrichment inside these two tasks' API
// calls), so on extract the IssueSync stage only ever empties together
// with ProjectData, never independently.
var extractProjectDataTasks = map[string]bool{
	"getProjectComponentTree":   true,
	"getProjectSourceCode":      true,
	"getProjectSCMData":         true,
	"getProjectVersions":        true,
	"getProjectAnalysisHistory": true, // #554 — PoC project-history migration.
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
	"getProjectRecentIssueTypes":  true,
	"getSafeHotspots":             true,
}

// CategorizeTask buckets an extract task name into the same four stages
// migrate.CategorizeTask uses (#520). The overall progress tracker no
// longer weights by it (#621); it is kept so the task groups above stay
// checked for full coverage. Anything not explicitly listed is CategoryGeneral —
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

// IsProjectScopedTask reports whether the task writes one record per project
// (or per project component), as opposed to server-wide data such as gates,
// profiles or groups. Readers merging a partial extract onto an earlier one
// use it to let a re-extracted project replace its older records (#664).
func IsProjectScopedTask(name string) bool {
	return CategorizeTask(name) != common.CategoryGeneral
}

// IsPortfolioScopedTask reports whether the task writes one record (or set of
// records) per portfolio. These tasks are server-wide, not project-scoped, but
// a portfolio slice (--portfolio_key_regexp / --portfolio_name_regexp) only
// extracts some portfolios, so readers merging slices let a re-extracted
// portfolio replace its older records (#666).
func IsPortfolioScopedTask(name string) bool {
	switch name {
	case "getPortfolios", "getPortfolioDetails", "getPortfolioProjects":
		return true
	}
	return false
}

// IsGroupScopedTask reports whether the task writes records that belong to a
// user group fetched by getGroups. Like the portfolio tasks they are
// server-wide, but a group slice (--group_name_regexp) only extracts some
// groups, so readers merging slices let a re-extracted group replace its
// older records instead of keeping only the newest slice's (#669).
func IsGroupScopedTask(name string) bool {
	switch name {
	case "getGroups", "getUserGroups":
		return true
	}
	return false
}
