// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import "github.com/sonar-solutions/sonar-migration-tool/internal/common"

// migrateObjectTasks maps each --objects category (common.ObjectXxx, #536)
// to the migrate task names it gates. Built by auditing every TaskDef
// registered in RegisterAll (tasks_*.go); mirrors
// internal/extract/objects_categories.go's extractObjectTasks.
//
// Rules baked into this mapping, per an explicit product decision:
//   - Rules (tasks_rules.go: updateRuleTags, updateRuleDescriptions) are
//     gated under quality_profiles rather than left unconditional.
//   - Project-scope bindings (setProjectProfiles, setProjectGates) belong
//     to "projects" even though the underlying definitions
//     (createProfiles / createGates) belong to quality_profiles /
//     quality_gates — the binding is a per-project association, not a
//     profile/gate definition.
//   - license_profiles has no task-name mapping: the category is valid
//     for --objects (common.ObjectLicenseProfiles) but not implemented on
//     the migrate side; cmd/migrate.go logs a warning and proceeds.
//
// Tasks intentionally left OUT of every category — and therefore never
// excluded by any --objects selection, regardless of what's chosen:
//
//   - Migration-tool plumbing that every other write depends on to
//     succeed at all: grantMigrationUserProjectPermissions,
//     getMigrationUser, createMigrationGroups,
//     addMigrationUserToMigrationGroups. These must always run.
//   - The generateXxxMappings CSV-loading setup tasks (tasks_setup.go):
//     generateProjectMappings, generateProfileMappings,
//     generateGateMappings, generateGroupMappings,
//     generateTemplateMappings, generatePortfolioMappings,
//     generateOrganizationMappings. Each only reads a local CSV file
//     into JSONL — no SonarQube Cloud API call is made — so leaving
//     them unclassified ("always run") costs at most a harmless local
//     file read even when the create* task consuming it is excluded.
//     Flagged here for review rather than silently guessing.
//   - getGateConditions / getProfileBackups / getEnterprises: these
//     "get"-prefixed read tasks are already excluded from the default
//     target set by isExcludedTask's prefix rule and are only pulled
//     back in as a transitive dependency of addGateConditions /
//     restoreProfiles / createPortfolios respectively — all three of
//     which ARE categorized (quality_gates / quality_profiles /
//     portfolios). Excluding the category already stops the dependency
//     edge from ever being walked (ResolveDependenciesExcluding
//     short-circuits at the excluded consumer without recursing into
//     its dependencies), so these three needed no explicit entry.
var migrateObjectTasks = map[string][]string{
	common.ObjectSettings: {
		"setGlobalSettings",
		"setGlobalWebhooks",
		"setGlobalNewCodePeriod",
	},
	common.ObjectPermissionTemplates: {
		"createPermissionTemplates",
		"addMigrationGroupToTemplates",
		"setTemplateGroupPermissions",
		"setDefaultTemplates",
	},
	common.ObjectQualityProfiles: {
		"createProfiles",
		"setProfileParent",
		"restoreProfiles",
		"analyzeProfileRules",
		"setDefaultProfiles",
		"setProfileGroupPermissions",
		"updateRuleTags",
		"updateRuleDescriptions",
	},
	common.ObjectQualityGates: {
		"createGates",
		"addGateConditions",
		"setDefaultGates",
	},
	// createProjects plus every project-scoped task (cmd/transfer.go's
	// transferTargetTasks is exactly this category's leaf-task set for a
	// single project, minus the tasks that belong to other categories),
	// plus the bindings (setProjectProfiles / setProjectGates — the
	// association, not the profile/gate definition), plus read-side
	// infra that exists only to support project operations.
	common.ObjectProjects: {
		"createProjects",
		"setProjectProfiles", "setProjectGates", "setProjectGroupPermissions",
		"setProjectSettings", "setProjectTags", "setProjectLinks", "setProjectSourceLink",
		"setProjectWebhooks", "setNewCodePeriods", "setProjectBinding",
		"getProjectIds", "getCreatedProjects", "matchProjectRepos", "getOrgBinding", "getOrgRepos",
		// Project DATA is still project scope: without these, a resumed
		// run (--run_id pointing at an earlier full run) whose
		// createProjects output already exists on disk would replay
		// scanner reports and issue/hotspot sync for exactly the
		// projects --objects excluded, violating the documented "no
		// project is created/extracted/migrated" guarantee. Their own
		// --skip_project_data_migration / --skip_issue_sync gates still
		// apply on top via isExcludedTask.
		"importProjectData", "syncIssueMetadata", "syncHotspotMetadata",
	},
	common.ObjectPortfolios: {
		"createPortfolios",
		"setPortfolioProjects",
		"configurePortfolios",
	},
	common.ObjectGroups: {
		"createGroups",
		"setOrgGroupPermissions",
	},
	// license_profiles: no migrate task exists yet. The value is still
	// accepted by common.ParseObjects for validation purposes; nothing
	// to gate here (#536).
	common.ObjectLicenseProfiles: {},
}

// excludedMigrateTasks returns every task name belonging to a category
// NOT present in selected (nil selected == everything selected == no
// exclusions). Used both to prune the default task set (via
// isExcludedTask) and to exclude cross-category dependency edges
// (#536) — e.g. setGlobalSettings/createPortfolios declaring
// createProjects as a dependency must not force it to run when
// "projects" is excluded.
func excludedMigrateTasks(selected map[string]bool) map[string]bool {
	return common.ExcludedTasks(migrateObjectTasks, selected)
}
