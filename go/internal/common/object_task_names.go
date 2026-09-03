// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

// objectCategoryTaskNames pairs one --objects category's extract-side
// and migrate-side task names, defined once here instead of as two
// independently maintained, structurally-identical map literals in
// internal/extract/objects_categories.go and
// internal/migrate/objects_categories.go (#536 — those two literals,
// despite listing entirely different task names, were flagged as
// duplicated code: same category keys, same shape, repeated twice).
//
// Both packages' own objects_categories.go project their half of this
// table into a local map[string][]string via ExtractCategoryTasks /
// MigrateCategoryTasks, then layer their own package-specific
// exceptions on top — most notably extract's ObjectProjects entry,
// which is computed dynamically from progress_categories.go rather
// than listed statically (so it can never drift from the real
// per-project task set); the Extract field below is left empty for
// that category for that reason.
//
// Rules baked into this table, per an explicit product decision
// (originally documented separately in each package's own file):
//   - Rules (extract's tasks_rules.go: getRules, getRepos,
//     getProfileRules, getRuleDetails, getPluginRules, getTemplateRules,
//     getActiveProfileRules, getDeactivatedProfileRules, getPluginIssues;
//     migrate's tasks_rules.go: updateRuleTags, updateRuleDescriptions)
//     are gated under quality_profiles rather than left unconditional.
//     getPluginIssues specifically: its only dependency is
//     getPluginRules, so excluding one without the other would leave
//     getPluginIssues scheduled with its dependency's edge stripped by
//     PlanPhasesExcluding, silently writing an empty plugin-issues
//     dataset instead of not running at all.
//   - SonarQube Applications (extract's tasks_views.go: getApplications,
//     getApplicationDetails) are gated under portfolios — same source
//     file, same edition gate as portfolios, and Applications have no
//     migrate-side task, so there is nowhere else for them to belong.
//   - Project-scope bindings (migrate's setProjectProfiles /
//     setProjectGates) belong to "projects" even though the underlying
//     definitions (createProfiles / createGates) belong to
//     quality_profiles / quality_gates — the binding is a per-project
//     association, not a profile/gate definition.
//   - Project DATA sync (migrate's importProjectData, syncIssueMetadata,
//     syncHotspotMetadata) is still project scope: without these, a
//     resumed run (--run_id pointing at an earlier full run) whose
//     createProjects output already exists on disk would replay scanner
//     reports and issue/hotspot sync for exactly the projects --objects
//     excluded, violating the documented "no project is
//     created/extracted/migrated" guarantee. Their own
//     --skip_project_data_migration / --skip_issue_sync gates still
//     apply on top via isExcludedTask.
//   - license_profiles has no task-name mapping on either side: the
//     category is valid for --objects (ObjectLicenseProfiles) but not
//     yet implemented; cmd/extract.go and cmd/migrate.go each log a
//     warning and proceed.
//
// Tasks intentionally left out of every category on both sides — and
// therefore never excluded by any --objects selection:
//   - extract: getUsers, getUserPermissions, getUserTokens (user
//     identity/token data has no dedicated --objects category).
//   - migrate: the tool's own internal plumbing that every other write
//     depends on to succeed at all — grantMigrationUserProjectPermissions,
//     getMigrationUser, createMigrationGroups,
//     addMigrationUserToMigrationGroups — and the generateXxxMappings
//     CSV-loading setup tasks, which only read a local file (no Cloud
//     API call), so leaving them unclassified costs at most a harmless
//     local file read. getGateConditions / getProfileBackups /
//     getEnterprises need no explicit entry either: they're already
//     excluded from migrate's default target set by isExcludedTask's
//     "get"-prefix rule, and are only pulled back in as a transitive
//     dependency of addGateConditions / restoreProfiles /
//     createPortfolios — all three of which ARE categorized, so
//     excluding the category already stops that dependency edge from
//     ever being walked.
var objectCategoryTaskNames = map[string]struct {
	Extract []string
	Migrate []string
}{
	ObjectSettings: {
		Extract: []string{
			"getServerInfo", "getServerSettings", "getServerSettingsDefinitions",
			"getPlugins", "getUsage",
			"getBindings", // ALM/DevOps platform bindings list
			"getAiCodeFixConfig",
			"getWebhooks",       // server-level webhooks
			"getServerWebhooks", // server-level webhooks (alias endpoint)
			"getWebhookDeliveries",
			"getGlobalNewCodePeriod",
			"getTasks", // global CE task list
		},
		Migrate: []string{"setGlobalSettings", "setGlobalWebhooks", "setGlobalNewCodePeriod"},
	},
	ObjectPermissionTemplates: {
		Extract: []string{
			"getTemplates", "getDefaultTemplates",
			"getTemplateGroupsScanners", "getTemplateGroupsViewers",
			"getTemplateUsersScanners", "getTemplateUsersViewers",
		},
		Migrate: []string{
			"createPermissionTemplates", "addMigrationGroupToTemplates",
			"setTemplateGroupPermissions", "setDefaultTemplates",
		},
	},
	ObjectQualityProfiles: {
		Extract: []string{
			"getProfiles", "getProfileBackups", "getProfileGroups",
			"getProfileUsers", "getProfileProjects",
			// Rule family, gated under quality_profiles.
			"getRules", "getRepos", "getProfileRules", "getRuleDetails",
			"getPluginRules", "getTemplateRules",
			"getActiveProfileRules", "getDeactivatedProfileRules",
			"getPluginIssues",
		},
		Migrate: []string{
			"createProfiles", "setProfileParent", "restoreProfiles", "analyzeProfileRules",
			"setDefaultProfiles", "setProfileGroupPermissions",
			"updateRuleTags", "updateRuleDescriptions",
		},
	},
	ObjectQualityGates: {
		Extract: []string{"getGates", "getGateConditions", "getGateGroups", "getGateUsers"},
		Migrate: []string{"createGates", "addGateConditions", "setDefaultGates"},
	},
	// ObjectProjects.Extract is intentionally empty: extract's "projects"
	// category is computed dynamically (unioning progress_categories.go's
	// tables) so it can never drift from the real per-project task set —
	// see internal/extract/objects_categories.go's projectObjectCategoryTasks.
	ObjectProjects: {
		Migrate: []string{
			"createProjects",
			"setProjectProfiles", "setProjectGates", "setProjectGroupPermissions",
			"setProjectSettings", "setProjectTags", "setProjectLinks", "setProjectSourceLink",
			"setProjectWebhooks", "setNewCodePeriods", "setProjectBinding",
			"getProjectIds", "getCreatedProjects", "matchProjectRepos", "getOrgBinding", "getOrgRepos",
			"importProjectData", "syncIssueMetadata", "syncHotspotMetadata",
		},
	},
	ObjectPortfolios: {
		Extract: []string{
			"getPortfolios", "getPortfolioDetails", "getPortfolioProjects",
			// SonarQube Applications — gated with portfolios.
			"getApplications", "getApplicationDetails",
		},
		Migrate: []string{"createPortfolios", "setPortfolioProjects", "configurePortfolios"},
	},
	ObjectGroups: {
		Extract: []string{"getGroups", "getUserGroups"},
		Migrate: []string{"createGroups", "setOrgGroupPermissions"},
	},
	// ObjectLicenseProfiles: no task exists on either side yet.
	ObjectLicenseProfiles: {},
}

// ExtractCategoryTasks returns the extract-side task names for one
// --objects category (empty for a category with no extract-side
// mapping, e.g. ObjectProjects — see objectCategoryTaskNames' doc).
func ExtractCategoryTasks(category string) []string {
	return objectCategoryTaskNames[category].Extract
}

// MigrateCategoryTasks returns the migrate-side task names for one
// --objects category.
func MigrateCategoryTasks(category string) []string {
	return objectCategoryTaskNames[category].Migrate
}
