// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import "github.com/sonar-solutions/sonar-migration-tool/internal/common"

// migrateObjectTasks maps each --objects category (common.ObjectXxx, #536)
// to the migrate task names it gates. Projected from
// common.MigrateCategoryTasks (the shared table both packages draw
// from — see its doc for the product decisions baked into it, e.g. why
// project-data sync and the project-scope bindings both land in
// "projects").
var migrateObjectTasks = buildMigrateObjectTasks()

func buildMigrateObjectTasks() map[string][]string {
	m := make(map[string][]string, len(common.AllObjects))
	for _, category := range common.AllObjects {
		m[category] = common.MigrateCategoryTasks(category)
	}
	return m
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
