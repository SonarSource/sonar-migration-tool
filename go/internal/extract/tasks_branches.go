// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"encoding/json"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

func branchTasks() []TaskDef {
	return []TaskDef{
		{Name: "getBranches", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: perProjectArrayFiltered("getBranches", "api/project_branches/list", "branches", "project", "projectKey", keepBranchByRegexp)},
		{Name: "getProjectPullRequests", Editions: []Edition{EditionDeveloper, EditionEnterprise, EditionDatacenter},
			Dependencies: []string{"getProjects"},
			Run:          perProjectArray("getProjectPullRequests", "api/project_pull_requests/list", "pullRequests", "project", "projectKey")},
	}
}

// keepBranchByRegexp decides whether a raw "api/project_branches/list" item
// survives extraction: no filter configured, or it's the project's main
// branch (always kept regardless of match — mirrors migrate's
// filterBranches main-branch bypass), or its name matches e.BranchRe.
// #582.
func keepBranchByRegexp(e *Executor, item json.RawMessage) bool {
	if e.BranchRe == nil {
		return true
	}
	if common.ExtractBool(item, "isMain") {
		return true
	}
	return e.BranchRe.MatchString(extractField(item, "name"))
}
