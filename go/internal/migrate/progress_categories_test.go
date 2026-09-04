// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

func TestCategorizeTask(t *testing.T) {
	cases := []struct {
		task string
		want common.TaskCategory
	}{
		// Project data / issue sync (#520); hotspot sync split out (#526).
		{"importProjectData", common.CategoryProjectData},
		{"syncHotspotMetadata", common.CategoryHotspotSync},
		{"syncIssueMetadata", common.CategoryIssueSync},
		// Project config: per-project provisioning/configuration.
		{"createProjects", common.CategoryProjectConfig},
		{"getProjectIds", common.CategoryProjectConfig},
		{"setProjectProfiles", common.CategoryProjectConfig},
		{"setProjectGates", common.CategoryProjectConfig},
		{"grantMigrationUserProjectPermissions", common.CategoryProjectConfig},
		{"matchProjectRepos", common.CategoryProjectConfig},
		{"setProjectBinding", common.CategoryProjectConfig},
		// General: org-level / one-shot tasks, including the deliberate
		// edge case — setGlobalSettings depends on createProjects (an API
		// workaround) but is conceptually general, not per-project.
		{"setGlobalSettings", common.CategoryGeneral},
		{"setGlobalWebhooks", common.CategoryGeneral},
		{"setGlobalNewCodePeriod", common.CategoryGeneral},
		{"generateProjectMappings", common.CategoryGeneral},
		{"createProfiles", common.CategoryGeneral},
		{"createGroups", common.CategoryGeneral},
		{"createPortfolios", common.CategoryGeneral},
		{"getOrgBinding", common.CategoryGeneral},
		{"someUnknownFutureTask", common.CategoryGeneral},
	}
	for _, c := range cases {
		t.Run(c.task, func(t *testing.T) {
			if got := CategorizeTask(c.task); got != c.want {
				t.Errorf("CategorizeTask(%q) = %v, want %v", c.task, got, c.want)
			}
		})
	}
}

// migrateIssueSyncTasks (planner.go's gating map for --skip_issue_sync)
// must map to CategoryIssueSync or CategoryHotspotSync here (#526 splits
// syncHotspotMetadata out of the IssueSync bucket), and
// migrateProjectDataTasks (the wider --skip_project_data_migration gate)
// must map to ProjectData, IssueSync, or HotspotSync — never General or
// ProjectConfig — since #520's weighting relies on these categories
// dropping to 0 together with the gates.
func TestGatingMapsFullyCategorized(t *testing.T) {
	for name := range migrateIssueSyncTasks {
		cat := CategorizeTask(name)
		if cat != common.CategoryIssueSync && cat != common.CategoryHotspotSync {
			t.Errorf("%s: gated by --skip_issue_sync but categorized as %v, want IssueSync or HotspotSync", name, cat)
		}
	}
	for name := range migrateProjectDataTasks {
		cat := CategorizeTask(name)
		if cat != common.CategoryProjectData && cat != common.CategoryIssueSync && cat != common.CategoryHotspotSync {
			t.Errorf("%s: gated by --skip_project_data_migration but categorized as %v, want ProjectData, IssueSync, or HotspotSync", name, cat)
		}
	}
}
