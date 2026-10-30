// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
		{"getProjectIssueTypes", common.CategoryProjectConfig},
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

// #554: getProjectAnalysisHistory must be excluded whenever "projects" is
// not among the selected --objects categories, and included when it is.
func TestExcludedExtractTasks_ProjectAnalysisHistory(t *testing.T) {
	objects := map[string]bool{common.ObjectSettings: true}
	if excludedExtractTasks(objects)["getProjectAnalysisHistory"] != true {
		t.Error("getProjectAnalysisHistory should be excluded when \"projects\" is not selected")
	}
	objects = map[string]bool{common.ObjectProjects: true}
	if excludedExtractTasks(objects)["getProjectAnalysisHistory"] {
		t.Error("getProjectAnalysisHistory should not be excluded when \"projects\" is selected")
	}
}

// #664: structure merges partial extracts per project, so every registered
// task that loops over projects must be reported as project-scoped.
func TestIsProjectScopedTask_CoversProjectDependentTasks(t *testing.T) {
	for _, def := range RegisterAll() {
		for _, dep := range def.Dependencies {
			if (dep == "getProjects" || dep == "getBranches") && !IsProjectScopedTask(def.Name) {
				t.Errorf("%s depends on %s but is not project-scoped", def.Name, dep)
			}
		}
	}
	for _, name := range []string{"getGates", "getProfiles", "getGroups", "getPortfolios", "getServerInfo"} {
		if IsProjectScopedTask(name) {
			t.Errorf("%s must not be project-scoped", name)
		}
	}
}

// #664: extract.json records whether the run was partial and its projects.
func TestWriteMetadataFile_PartialFlag(t *testing.T) {
	read := func(m extractMeta) map[string]any {
		dir := t.TempDir()
		if err := writeMetadataFile(dir, m); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "extract.json"))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	full := read(extractMeta{URL: "http://x"})
	if full["partial"] != false {
		t.Errorf("full extract: partial = %v, want false", full["partial"])
	}
	if _, ok := full["project_keys"]; ok {
		t.Error("full extract must not carry project_keys")
	}
	part := read(extractMeta{URL: "http://x", Partial: true, ProjectKeys: []string{"a", "b"}})
	if part["partial"] != true {
		t.Errorf("partial extract: partial = %v, want true", part["partial"])
	}
	keys, _ := part["project_keys"].([]any)
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Errorf("project_keys = %v, want [a b]", part["project_keys"])
	}
}

// #666: a portfolio slice is a partial extract, so readers merge it onto the
// earlier slices instead of replacing them.
func TestIsPartialExtract(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  ExtractConfig
		want bool
	}{
		"full":           {ExtractConfig{}, false},
		"objects":        {ExtractConfig{Objects: map[string]bool{"projects": true}}, true},
		"project keys":   {ExtractConfig{ProjectKeys: []string{"a"}}, true},
		"portfolio key":  {ExtractConfig{PortfolioKeyRegexp: "a.*"}, true},
		"portfolio name": {ExtractConfig{PortfolioNameRegexp: "A.*"}, true},
	} {
		if got := isPartialExtract(tc.cfg); got != tc.want {
			t.Errorf("%s: isPartialExtract = %v, want %v", name, got, tc.want)
		}
	}
}

func TestIsPortfolioScopedTask(t *testing.T) {
	for _, name := range []string{"getPortfolios", "getPortfolioDetails", "getPortfolioProjects"} {
		if !IsPortfolioScopedTask(name) {
			t.Errorf("%s must be portfolio-scoped", name)
		}
	}
	for _, name := range []string{"getProjects", "getApplications", "getGates"} {
		if IsPortfolioScopedTask(name) {
			t.Errorf("%s must not be portfolio-scoped", name)
		}
	}
	// Every registered portfolio task must be covered, so a new one cannot
	// silently be unioned with duplicates across slices.
	for _, def := range RegisterAll() {
		if strings.HasPrefix(def.Name, "getPortfolio") && !IsPortfolioScopedTask(def.Name) {
			t.Errorf("%s is a portfolio task but not portfolio-scoped", def.Name)
		}
	}
}
