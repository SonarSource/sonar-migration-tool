// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"slices"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// Edition is an alias for common.Edition.
type Edition = common.Edition

// Edition constants forwarded from common.
const (
	EditionCommunity  = common.EditionCommunity
	EditionDeveloper  = common.EditionDeveloper
	EditionEnterprise = common.EditionEnterprise
	EditionDatacenter = common.EditionDatacenter
)

// AllEditions is forwarded from common.
var AllEditions = common.AllEditions

// ParseEdition is forwarded from common.
var ParseEdition = common.ParseEdition

// TaskDef defines a single extract task with a typed Run function.
type TaskDef struct {
	Name         string
	Editions     []Edition
	Dependencies []string
	Run          func(ctx context.Context, e *Executor) error
}

// TaskName implements common.TaskMeta.
func (t *TaskDef) TaskName() string { return t.Name }

// TaskEditions implements common.TaskMeta.
func (t *TaskDef) TaskEditions() []Edition { return t.Editions }

// TaskDeps implements common.TaskMeta.
func (t *TaskDef) TaskDeps() []string { return t.Dependencies }

// BuildRegistry returns a name-keyed lookup map from a list of TaskDefs.
func BuildRegistry(defs []TaskDef) map[string]*TaskDef {
	reg := make(map[string]*TaskDef, len(defs))
	for i := range defs {
		reg[defs[i].Name] = &defs[i]
	}
	return reg
}

// FilterByEdition returns a new registry containing only tasks that support
// the given edition.
func FilterByEdition(reg map[string]*TaskDef, edition Edition) map[string]*TaskDef {
	return common.FilterByEditionGeneric(reg, edition)
}

// ResolveDependencies recursively collects all transitive dependencies for a
// set of target tasks. Returns nil for any target whose dependencies cannot be
// resolved (missing from registry).
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

// PlanPhases computes ordered execution phases via topological sort.
// Tasks in the same phase have all dependencies satisfied and can run
// concurrently. Returns an error if the graph contains a cycle.
func PlanPhases(tasks map[string]bool, reg map[string]*TaskDef) ([][]string, error) {
	return common.PlanPhasesGeneric(tasks, reg)
}

// PlanPhasesExcluding is like PlanPhases, but first strips any excluded
// task name out of every TaskDef's Dependencies before computing phases.
//
// common.PlanPhasesGeneric's readiness check has no notion of
// "excluded" — a task only becomes ready once every name in its
// Dependencies has been scheduled and marked complete. When an
// --objects filter is active, ResolveDependenciesExcluding deliberately
// leaves an excluded task OUT of tasks (vacuously satisfied, never
// added, never walked) — so a task with a cross-category dependency on
// an excluded one (e.g. getProjectPluginIssues/getProjectTemplateIssues,
// category "projects", declare getPluginRules/getTemplateRules,
// category "quality_profiles", as dependencies — excluded when
// --objects=projects) would otherwise wait forever for a dependency
// that will never run, surfacing as a false "cycle detected in task
// dependency graph" error instead of a valid plan (#536). Confirmed by
// reproduction: extract DOES have cross-category dependency edges,
// unlike an earlier assumption in migrate's identical helper.
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

// RegisterAll returns every extract task definition.
func RegisterAll() []TaskDef {
	var all []TaskDef
	all = append(all, systemTasks()...)
	all = append(all, userTasks()...)
	all = append(all, projectTasks()...)
	all = append(all, branchTasks()...)
	all = append(all, issueTasks()...)
	all = append(all, ruleTasks()...)
	all = append(all, profileTasks()...)
	all = append(all, gateTasks()...)
	all = append(all, templateTasks()...)
	all = append(all, viewTasks()...)
	all = append(all, webhookTasks()...)
	all = append(all, miscTasks()...)
	all = append(all, projectDataTasks()...)
	return all
}

// projectDataTaskNames lists task names that pull issue, source-code,
// SCM-blame, and version data — extracted by default and dropped only
// when --skip_project_data_migration is set.
var projectDataTaskNames = map[string]bool{
	"getProjectIssuesFull":    true,
	"getProjectComponentTree": true,
	"getProjectSourceCode":    true,
	"getProjectSCMData":       true,
	"getProjectHotspotsFull":  true,
	"getProjectVersions":      true,
}

// TargetTasks determines which tasks to extract based on config. objects,
// when non-nil, additionally drops any default task whose category isn't
// selected (#536); pass nil for "everything" (no objects filter).
func TargetTasks(reg map[string]*TaskDef, targetTask, extractType string, objects map[string]bool) []string {
	return targetTasks(reg, targetTask, extractType, false, objects)
}

// TargetTasksWithProjectData is like TargetTasks but includes project data tasks.
func TargetTasksWithProjectData(reg map[string]*TaskDef, targetTask, extractType string, objects map[string]bool) []string {
	return targetTasks(reg, targetTask, extractType, true, objects)
}

func targetTasks(reg map[string]*TaskDef, targetTask, extractType string, includeProjectData bool, objects map[string]bool) []string {
	if targetTask != "" {
		// An explicit single-task override always wins: objects filtering
		// does not apply here (#536).
		return []string{targetTask}
	}
	excluded := excludedExtractTasks(objects)
	// Default: all tasks starting with "get".
	var tasks []string
	for name := range reg {
		if len(name) > 3 && name[:3] == "get" {
			if projectDataTaskNames[name] && !includeProjectData {
				continue
			}
			if excluded[name] {
				continue
			}
			tasks = append(tasks, name)
		}
	}
	slices.Sort(tasks)
	return tasks
}
