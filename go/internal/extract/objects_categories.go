// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"sort"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// extractObjectTasks maps each --objects category (common.ObjectXxx, #536)
// to the extract task names it gates. Projected from
// common.ExtractCategoryTasks (the shared table both packages draw
// from, see its doc for the product decisions baked into it), with one
// override: the "projects" category is computed dynamically here via
// projectObjectCategoryTasks rather than listed statically, so it can
// never drift from the real per-project task set.
var extractObjectTasks = buildExtractObjectTasks()

func buildExtractObjectTasks() map[string][]string {
	m := make(map[string][]string, len(common.AllObjects))
	for _, category := range common.AllObjects {
		m[category] = common.ExtractCategoryTasks(category)
	}
	m[common.ObjectProjects] = projectObjectCategoryTasks()
	return m
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
	return common.ExcludedTasks(extractObjectTasks, selected)
}
