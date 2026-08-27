// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

func TestCategorizeTask(t *testing.T) {
	cases := []struct {
		task string
		want common.TaskCategory
	}{
		// Project data (#520): the full-fidelity per-project data dumps.
		{"getProjectComponentTree", common.CategoryProjectData},
		{"getProjectSourceCode", common.CategoryProjectData},
		{"getProjectSCMData", common.CategoryProjectData},
		{"getProjectVersions", common.CategoryProjectData},
		// Issue sync: pulled out of projectDataTaskNames for weighting only.
		{"getProjectIssuesFull", common.CategoryIssueSync},
		{"getProjectHotspotsFull", common.CategoryIssueSync},
		// Project config: per-project metadata/config tasks.
		{"getProjects", common.CategoryProjectConfig},
		{"getProjectDetails", common.CategoryProjectConfig},
		{"getBranches", common.CategoryProjectConfig},
		{"getProjectIssues", common.CategoryProjectConfig},
		{"getProjectTemplateIssues", common.CategoryProjectConfig},
		// General: server/user/rule/profile/gate/etc. — not project-scoped.
		{"getServerInfo", common.CategoryGeneral},
		{"getPlugins", common.CategoryGeneral},
		{"getRules", common.CategoryGeneral},
		{"getProfiles", common.CategoryGeneral},
		{"getGates", common.CategoryGeneral},
		{"getPortfolios", common.CategoryGeneral},
		{"getWebhooks", common.CategoryGeneral},
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

// Every task in projectDataTaskNames (planner.go's gating map for
// --skip_project_data_migration) must land in either ProjectData or
// IssueSync here — never General or ProjectConfig — since #520's weighting
// relies on both categories dropping to 0 together when the flag is set.
func TestProjectDataTaskNamesFullyCategorized(t *testing.T) {
	for name := range projectDataTaskNames {
		cat := CategorizeTask(name)
		if cat != common.CategoryProjectData && cat != common.CategoryIssueSync {
			t.Errorf("%s: gated by --skip_project_data_migration but categorized as %v, want ProjectData or IssueSync", name, cat)
		}
	}
}
