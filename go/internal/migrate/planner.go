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

// PlanPhases computes topologically sorted execution phases.
func PlanPhases(tasks map[string]bool, reg map[string]*TaskDef) ([][]string, error) {
	return common.PlanPhasesGeneric(tasks, reg)
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
func MigrateTargetTasks(reg map[string]*TaskDef, targetTask string, skipProfiles, includeProjectData, skipIssueSync, skipProjectDataMigration bool, targetTasks []string) []string {
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
	var tasks []string
	for name := range reg {
		if isExcludedTask(name, skipProfiles, includeProjectData, skipIssueSync, skipProjectDataMigration) {
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
