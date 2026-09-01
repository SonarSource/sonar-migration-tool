// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"sort"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// extractObjectTasks maps each --objects category (common.ObjectXxx, #536)
// to the extract task names it gates. Built by auditing every TaskDef
// registered in RegisterAll (tasks_*.go).
//
// Rules baked into this mapping, per an explicit product decision:
//   - Rules (tasks_rules.go: getRules, getRepos, getProfileRules,
//     getRuleDetails, getPluginRules, getTemplateRules,
//     getActiveProfileRules, getDeactivatedProfileRules) are gated under
//     quality_profiles rather than left unconditional.
//   - SonarQube Applications (tasks_views.go: getApplications,
//     getApplicationDetails) are gated under portfolios — same source
//     file, same edition gate as portfolios, and Applications have no
//     migrate-side task, so there is nowhere else for them to belong.
//   - license_profiles has no task-name mapping: the category is valid
//     for --objects (common.ObjectLicenseProfiles) but not implemented on
//     the extract side; cmd/extract.go logs a warning and proceeds.
//
// Tasks intentionally left OUT of every category — and therefore never
// excluded by any --objects selection, regardless of what's chosen —
// because they don't fit any of the issue's object categories:
//   - getUsers, getUserPermissions, getUserTokens (tasks_users.go): user
//     identity/token data has no dedicated --objects category.
//   - getPluginIssues (tasks_issues.go): the global (non-project-scoped)
//     plugin-rule issue list. Its per-project sibling
//     getProjectPluginIssues IS gated (via the "projects" category,
//     below), but getPluginIssues itself isn't project-scoped and isn't
//     part of the rule family in tasks_rules.go, so it stays unconditional.
var extractObjectTasks = map[string][]string{
	common.ObjectSettings: {
		"getServerInfo",
		"getServerSettings",
		"getServerSettingsDefinitions",
		"getPlugins",
		"getUsage",
		"getBindings", // ALM/DevOps platform bindings list
		"getAiCodeFixConfig",
		"getWebhooks",       // server-level webhooks
		"getServerWebhooks", // server-level webhooks (alias endpoint)
		"getWebhookDeliveries",
		"getGlobalNewCodePeriod",
		"getTasks", // global CE task list
	},
	common.ObjectPermissionTemplates: {
		"getTemplates",
		"getDefaultTemplates",
		"getTemplateGroupsScanners",
		"getTemplateGroupsViewers",
		"getTemplateUsersScanners",
		"getTemplateUsersViewers",
	},
	common.ObjectQualityProfiles: {
		"getProfiles",
		"getProfileBackups",
		"getProfileGroups",
		"getProfileUsers",
		"getProfileProjects",
		// Rule family (tasks_rules.go), gated under quality_profiles.
		"getRules",
		"getRepos",
		"getProfileRules",
		"getRuleDetails",
		"getPluginRules",
		"getTemplateRules",
		"getActiveProfileRules",
		"getDeactivatedProfileRules",
	},
	common.ObjectQualityGates: {
		"getGates",
		"getGateConditions",
		"getGateGroups",
		"getGateUsers",
	},
	// Every per-project task: getProjects itself plus the union of
	// extractProjectConfigTasks, extractProjectDataTasks, and
	// extractIssueSyncTasks (progress_categories.go) — read directly from
	// those maps so this list can never drift from the real registry.
	common.ObjectProjects: projectObjectCategoryTasks(),
	common.ObjectPortfolios: {
		"getPortfolios",
		"getPortfolioDetails",
		"getPortfolioProjects",
		// SonarQube Applications — gated with portfolios, see comment above.
		"getApplications",
		"getApplicationDetails",
	},
	common.ObjectGroups: {
		"getGroups",
		"getUserGroups",
	},
	// license_profiles: no extract task exists yet. The value is still
	// accepted by common.ParseObjects for validation purposes; nothing to
	// gate here (#536).
	common.ObjectLicenseProfiles: {},
}

// projectObjectCategoryTasks unions the task-name tables progress_categories.go
// already maintains for #520 progress weighting, so the "projects"
// --objects category always matches the real per-project task set.
func projectObjectCategoryTasks() []string {
	seen := make(map[string]bool)
	for name := range extractProjectConfigTasks {
		seen[name] = true
	}
	for name := range extractProjectDataTasks {
		seen[name] = true
	}
	for name := range extractIssueSyncTasks {
		seen[name] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// excludedExtractTasks returns every task name belonging to a category
// NOT present in selected (nil selected == everything selected == no
// exclusions). Used both to prune the default "get"-prefixed task set
// and to exclude cross-category dependency edges (#536).
func excludedExtractTasks(selected map[string]bool) map[string]bool {
	if selected == nil {
		return nil
	}
	excluded := make(map[string]bool)
	for category, tasks := range extractObjectTasks {
		if selected[category] {
			continue
		}
		for _, t := range tasks {
			excluded[t] = true
		}
	}
	return excluded
}
