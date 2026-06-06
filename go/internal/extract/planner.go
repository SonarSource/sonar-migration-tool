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

// PlanPhases computes ordered execution phases via topological sort.
// Tasks in the same phase have all dependencies satisfied and can run
// concurrently. Returns an error if the graph contains a cycle.
func PlanPhases(tasks map[string]bool, reg map[string]*TaskDef) ([][]string, error) {
	return common.PlanPhasesGeneric(tasks, reg)
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

// TargetTasks determines which tasks to extract based on config.
func TargetTasks(reg map[string]*TaskDef, targetTask, extractType string) []string {
	return targetTasks(reg, targetTask, extractType, false)
}

// TargetTasksWithProjectData is like TargetTasks but includes project data tasks.
func TargetTasksWithProjectData(reg map[string]*TaskDef, targetTask, extractType string) []string {
	return targetTasks(reg, targetTask, extractType, true)
}

func targetTasks(reg map[string]*TaskDef, targetTask, extractType string, includeProjectData bool) []string {
	if targetTask != "" {
		return []string{targetTask}
	}
	// Default: all tasks starting with "get".
	var tasks []string
	for name := range reg {
		if len(name) > 3 && name[:3] == "get" {
			if projectDataTaskNames[name] && !includeProjectData {
				continue
			}
			tasks = append(tasks, name)
		}
	}
	slices.Sort(tasks)
	return tasks
}
