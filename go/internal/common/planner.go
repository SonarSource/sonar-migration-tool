// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"fmt"
	"slices"
)

// TaskMeta is the interface the planner algorithms need. Both extract.TaskDef
// and migrate.TaskDef satisfy this via their own wrapper methods.
type TaskMeta interface {
	TaskName() string
	TaskEditions() []Edition
	TaskDeps() []string
}

// FilterByEditionGeneric returns a filtered map of TaskMeta entries supporting
// the given edition.
func FilterByEditionGeneric[T TaskMeta](reg map[string]T, edition Edition) map[string]T {
	out := make(map[string]T, len(reg))
	for name, def := range reg {
		eds := def.TaskEditions()
		if len(eds) == 0 || slices.Contains(eds, edition) {
			out[name] = def
		}
	}
	return out
}

// ResolveDependenciesGeneric recursively collects all transitive dependencies.
// Returns nil if any dependency is missing.
func ResolveDependenciesGeneric[T TaskMeta](targets []string, reg map[string]T) map[string]bool {
	result := make(map[string]bool)
	for _, t := range targets {
		if !resolveGeneric(t, reg, result) {
			return nil
		}
	}
	return result
}

func resolveGeneric[T TaskMeta](task string, reg map[string]T, seen map[string]bool) bool {
	if seen[task] {
		return true
	}
	def, ok := reg[task]
	if !ok {
		return false
	}
	seen[task] = true
	for _, dep := range def.TaskDeps() {
		if !resolveGeneric(dep, reg, seen) {
			delete(seen, task)
			return false
		}
	}
	return true
}

// ResolveDependenciesExcludingGeneric is like ResolveDependenciesGeneric,
// except any task name present in excluded is treated as vacuously
// satisfied: it is never added to the result and its own dependencies
// are never walked. Used by --objects (#536) so a task whose category
// was excluded from the run doesn't get pulled back in — and forced to
// run — just because another, selected task happens to declare it as a
// dependency (e.g. setGlobalSettings declares createProjects as a
// dependency for its project-scope fallback path; that must not force
// project creation when --objects=settings excludes the "projects"
// category). Returns nil if a non-excluded dependency is missing from
// reg, same contract as ResolveDependenciesGeneric.
func ResolveDependenciesExcludingGeneric[T TaskMeta](targets []string, reg map[string]T, excluded map[string]bool) map[string]bool {
	result := make(map[string]bool)
	for _, t := range targets {
		if excluded[t] {
			continue
		}
		if !resolveGenericExcluding(t, reg, excluded, result) {
			return nil
		}
	}
	return result
}

func resolveGenericExcluding[T TaskMeta](task string, reg map[string]T, excluded, seen map[string]bool) bool {
	if seen[task] {
		return true
	}
	if excluded[task] {
		return true
	}
	def, ok := reg[task]
	if !ok {
		return false
	}
	seen[task] = true
	for _, dep := range def.TaskDeps() {
		if excluded[dep] {
			continue
		}
		if !resolveGenericExcluding(dep, reg, excluded, seen) {
			delete(seen, task)
			return false
		}
	}
	return true
}

// PlanPhasesGeneric computes ordered execution phases via topological sort.
func PlanPhasesGeneric[T TaskMeta](tasks map[string]bool, reg map[string]T) ([][]string, error) {
	completed := make(map[string]bool)
	var plan [][]string

	for len(completed) < len(tasks) {
		phase := collectReadyTasks(tasks, completed, reg)
		if len(phase) == 0 {
			return nil, fmt.Errorf("cycle detected in task dependency graph")
		}
		slices.Sort(phase)
		plan = append(plan, phase)
		for _, t := range phase {
			completed[t] = true
		}
	}
	return plan, nil
}

// collectReadyTasks returns all tasks whose dependencies are fully completed.
func collectReadyTasks[T TaskMeta](tasks map[string]bool, completed map[string]bool, reg map[string]T) []string {
	var phase []string
	for task := range tasks {
		if completed[task] {
			continue
		}
		if allDepsCompleted(reg[task], completed) {
			phase = append(phase, task)
		}
	}
	return phase
}

// allDepsCompleted checks whether all dependencies of a task are in the completed set.
func allDepsCompleted[T TaskMeta](def T, completed map[string]bool) bool {
	for _, dep := range def.TaskDeps() {
		if !completed[dep] {
			return false
		}
	}
	return true
}
