// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"slices"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// TaskDef defines a single migrate task with a typed Run function.
type TaskDef struct {
	Name         string
	Editions     []common.Edition
	Dependencies []string
	Run          func(ctx context.Context, e *Executor) error
}

// TaskName implements common.TaskMeta.
func (t *TaskDef) TaskName() string { return t.Name }

// TaskEditions implements common.TaskMeta.
func (t *TaskDef) TaskEditions() []common.Edition { return t.Editions }

// TaskDeps implements common.TaskMeta.
func (t *TaskDef) TaskDeps() []string { return t.Dependencies }

// BuildMigrateRegistry returns a name-keyed lookup map.
func BuildMigrateRegistry(defs []TaskDef) map[string]*TaskDef {
	reg := make(map[string]*TaskDef, len(defs))
	for i := range defs {
		reg[defs[i].Name] = &defs[i]
	}
	return reg
}

// FilterByEdition returns tasks supporting the given edition.
func FilterByEdition(reg map[string]*TaskDef, edition common.Edition) map[string]*TaskDef {
	return common.FilterByEditionGeneric(reg, edition)
}

// ResolveDependencies recursively resolves transitive dependencies.
func ResolveDependencies(targets []string, reg map[string]*TaskDef) map[string]bool {
	return common.ResolveDependenciesGeneric(targets, reg)
}

// ResolveDependenciesExcluding is like ResolveDependencies, except any task
// name present in excluded is treated as vacuously satisfied: it is never
// added to the result and its own dependencies are never walked. Used when
// an --objects filter is active (#536) so a task whose category was
// excluded from the run doesn't get pulled back in just because another,
// selected task happens to declare it as a dependency.
func ResolveDependenciesExcluding(targets []string, reg map[string]*TaskDef, excluded map[string]bool) map[string]bool {
	return common.ResolveDependenciesExcludingGeneric(targets, reg, excluded)
}

// PlanPhases computes topologically sorted execution phases.
func PlanPhases(tasks map[string]bool, reg map[string]*TaskDef) ([][]string, error) {
	return common.PlanPhasesGeneric(tasks, reg)
}

// PlanPhasesExcluding is like PlanPhases, but first strips any excluded
// task name out of every TaskDef's Dependencies before computing phases.
//
// common.PlanPhasesGeneric's readiness check (allDepsCompleted) has no
// notion of "excluded" — a task only becomes ready once every name in
// its Dependencies has been scheduled and marked complete. When an
// --objects filter is active, ResolveDependenciesExcluding deliberately
// leaves an excluded task OUT of tasks (it's vacuously satisfied, never
// added, never walked) — so a task with a cross-category dependency on
// an excluded one (e.g. setGlobalSettings declares createProjects as a
// dependency for its project-scope probe; createProjects is excluded
// when --objects=settings) would otherwise wait forever for a
// dependency that will never run, surfacing as a false "cycle detected
// in task dependency graph" error instead of a valid plan (#536).
// extract has the identical bug for the same reason (e.g.
// getProjectPluginIssues/getProjectTemplateIssues, category
// "projects", depend on getPluginRules/getTemplateRules, category
// "quality_profiles") — see extract.PlanPhasesExcluding, an
// independent copy of this same fix. Kept as two package-local copies
// rather than promoted to shared common.PlanPhasesGeneric since
// TaskDef is a distinct type per package and there's no shared
// interface for the copy-with-filtered-Dependencies step.
//
// The returned plan is computed against the filtered dependency view;
// the registry returned to callers for task execution is unaffected —
// Dependencies is only consulted during planning, never by runPhase.
func PlanPhasesExcluding(tasks map[string]bool, reg map[string]*TaskDef, excluded map[string]bool) ([][]string, error) {
	if len(excluded) == 0 {
		return PlanPhases(tasks, reg)
	}
	filtered := make(map[string]*TaskDef, len(reg))
	for name, def := range reg {
		hasExcludedDep := false
		for _, dep := range def.Dependencies {
			if excluded[dep] {
				hasExcludedDep = true
				break
			}
		}
		if !hasExcludedDep {
			filtered[name] = def
			continue
		}
		deps := make([]string, 0, len(def.Dependencies))
		for _, dep := range def.Dependencies {
			if !excluded[dep] {
				deps = append(deps, dep)
			}
		}
		cp := *def
		cp.Dependencies = deps
		filtered[name] = &cp
	}
	return PlanPhases(tasks, filtered)
}

// RegisterAll returns every migrate task definition.
func RegisterAll() []TaskDef {
	var all []TaskDef
	all = append(all, setupTasks()...)
	all = append(all, readTasks()...)
	all = append(all, createTasks()...)
	all = append(all, configureTasks()...)
	all = append(all, associateTasks()...)
	all = append(all, permissionTasks()...)
	all = append(all, almTasks()...)
	all = append(all, portfolioTasks()...)
	all = append(all, ruleTasks()...)
	all = append(all, deleteTasks()...)
	all = append(all, projectDataTasks()...)
	all = append(all, hotspotMetadataSyncTasks()...)
	all = append(all, issueMetadataSyncTasks()...)
	return all
}

// migrateProjectDataTasks lists every task that imports or syncs
// per-project data after the configuration migration finishes — the
// project-data import plus the trailing issue + hotspot metadata
// syncs. All three run by default; the operator opts out via
// --skip_project_data_migration (#303).
var migrateProjectDataTasks = map[string]bool{
	"importProjectData":   true,
	"syncHotspotMetadata": true,
	"syncIssueMetadata":   true,
}

// migrateIssueSyncTasks lists the final per-issue / per-hotspot
// metadata sync tasks that --skip_issue_sync (or config skip_issue_sync:
// true) excludes. importProjectData itself stays included;
// only the trailing sync pair is skipped. #299.
var migrateIssueSyncTasks = map[string]bool{
	"syncHotspotMetadata": true,
	"syncIssueMetadata":   true,
}

// MigrateTargetTasks determines which tasks to run. Precedence:
//  1. targetTasks — an explicit leaf list (used by the transfer command for
//     project-scoped migration); returned as-is, dependencies are resolved
//     transitively by ResolveDependencies.
//  2. targetTask — a single named task.
//  3. Default: all tasks NOT starting with "get", "delete", or "reset".
//
// skipIssueSync (#299) drops the trailing per-issue / per-hotspot metadata
// sync tasks from the default set while keeping importProjectData itself.
// skipProjectDataMigration (#303) is the wider opt-out: it drops
// importProjectData AND the two trailing sync tasks together.
//
// objects (#536), when non-nil, additionally drops any default task whose
// category isn't selected — composed with the skip gates above via
// isExcludedTask. objects filtering does NOT apply when targetTasks or
// targetTask (explicit overrides) are used, matching the precedence
// documented above.
func MigrateTargetTasks(reg map[string]*TaskDef, targetTask string, skipProfiles, includeProjectData, skipIssueSync, skipProjectDataMigration bool, targetTasks []string, objects map[string]bool) []string {
	if len(targetTasks) > 0 {
		// Filter the explicit list against the skip gates so transfer's
		// project-scoped target list still honors --skip_project_data_migration
		// / --skip_issue_sync. Without this the transfer
		// command would always run importProjectData + the syncs even
		// when the operator opted out, because the explicit list
		// bypassed isExcludedTask. The other gates (--skip_profiles,
		// project-data-without-flag) don't apply to transfer's curated
		// list, so we restrict the filter to the project-data and
		// issue-sync membership maps.
		out := make([]string, 0, len(targetTasks))
		for _, name := range targetTasks {
			if skipProjectDataMigration && migrateProjectDataTasks[name] {
				continue
			}
			if skipIssueSync && migrateIssueSyncTasks[name] {
				continue
			}
			out = append(out, name)
		}
		return out
	}
	if targetTask != "" {
		return []string{targetTask}
	}
	excluded := excludedMigrateTasks(objects)
	var tasks []string
	for name := range reg {
		if isExcludedTask(name, skipProfiles, includeProjectData, skipIssueSync, skipProjectDataMigration) {
			continue
		}
		if excluded[name] {
			continue
		}
		tasks = append(tasks, name)
	}
	slices.Sort(tasks)
	return tasks
}

var excludePrefixes = []string{"get", "delete", "reset"}

func isExcludedTask(name string, skipProfiles, includeProjectData, skipIssueSync, skipProjectDataMigration bool) bool {
	for _, prefix := range excludePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	if skipProfiles && (strings.Contains(name, "Profile") || strings.Contains(name, "profile")) {
		return true
	}
	// Project-data migration is the wider gate: it covers the whole
	// importProjectData + sync trio. Checked before --include-scan-
	// history so a config with both set still surfaces a single
	// "skipped" outcome rather than a confusing "include then skip"
	// no-op.
	if skipProjectDataMigration && migrateProjectDataTasks[name] {
		return true
	}
	if migrateProjectDataTasks[name] && !includeProjectData {
		return true
	}
	if skipIssueSync && migrateIssueSyncTasks[name] {
		return true
	}
	return false
}
