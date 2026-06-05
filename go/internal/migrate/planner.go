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
	all = append(all, scanHistoryTasks()...)
	all = append(all, hotspotMetadataSyncTasks()...)
	all = append(all, issueMetadataSyncTasks()...)
	return all
}

// migrateProjectDataTasks lists every task that imports or syncs
// per-project data after the configuration migration finishes — the
// scan-history import plus the trailing issue + hotspot metadata
// syncs. All three are gated by --include-scan-history (only run
// when extracted data is available) AND by --skip-project-data-
// migration (operator opt-out, #303).
var migrateProjectDataTasks = map[string]bool{
	"importScanHistory":   true,
	"syncHotspotMetadata": true,
	"syncIssueMetadata":   true,
}

// migrateIssueSyncTasks lists the final per-issue / per-hotspot
// metadata sync tasks that --skip-issue-sync (or config skip-issue-
// sync: true) excludes. importScanHistory itself stays included;
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
// sync tasks from the default set while keeping importScanHistory itself.
// skipProjectDataMigration (#303) is the wider opt-out: it drops
// importScanHistory AND the two trailing sync tasks together.
func MigrateTargetTasks(reg map[string]*TaskDef, targetTask string, skipProfiles, includeScanHistory, skipIssueSync, skipProjectDataMigration bool, targetTasks []string) []string {
	if len(targetTasks) > 0 {
		return slices.Clone(targetTasks)
	}
	if targetTask != "" {
		return []string{targetTask}
	}
	var tasks []string
	for name := range reg {
		if isExcludedTask(name, skipProfiles, includeScanHistory, skipIssueSync, skipProjectDataMigration) {
			continue
		}
		tasks = append(tasks, name)
	}
	slices.Sort(tasks)
	return tasks
}

var excludePrefixes = []string{"get", "delete", "reset"}

func isExcludedTask(name string, skipProfiles, includeScanHistory, skipIssueSync, skipProjectDataMigration bool) bool {
	for _, prefix := range excludePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	if skipProfiles && (strings.Contains(name, "Profile") || strings.Contains(name, "profile")) {
		return true
	}
	// Project-data migration is the wider gate: it covers the whole
	// importScanHistory + sync trio. Checked before --include-scan-
	// history so a config with both set still surfaces a single
	// "skipped" outcome rather than a confusing "include then skip"
	// no-op.
	if skipProjectDataMigration && migrateProjectDataTasks[name] {
		return true
	}
	if migrateProjectDataTasks[name] && !includeScanHistory {
		return true
	}
	if skipIssueSync && migrateIssueSyncTasks[name] {
		return true
	}
	return false
}
