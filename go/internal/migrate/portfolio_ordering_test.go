// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"slices"
	"testing"
)

// phaseOf returns the index of the phase containing task, or -1.
func phaseOf(plan [][]string, task string) int {
	for i, phase := range plan {
		if slices.Contains(phase, task) {
			return i
		}
	}
	return -1
}

// With both tasks present, configurePortfolios must land in a strictly later
// phase than importProjectData so portfolio CE computations cannot overlap the
// project analyses (issue #417).
func TestPortfoliosOrderedAfterImport(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	reg = FilterByEdition(reg, "enterprise") // portfolios exist only on enterprise/datacenter
	// includeProjectData=true (the migrate default: cfg.IncludeProjectData =
	// !cfg.SkipProjectDataMigration), so importProjectData is in the run.
	targets := MigrateTargetTasks(reg, "", false, true, false, false, nil)
	taskSet := ResolveDependencies(targets, reg)
	if taskSet == nil {
		t.Fatal("cannot resolve dependencies")
	}
	if !taskSet["configurePortfolios"] || !taskSet["importProjectData"] {
		t.Fatalf("expected both tasks in set; got configurePortfolios=%v importProjectData=%v",
			taskSet["configurePortfolios"], taskSet["importProjectData"])
	}

	orderPortfoliosAfterImport(taskSet, reg)

	plan, err := PlanPhases(taskSet, reg)
	if err != nil {
		t.Fatalf("PlanPhases: %v", err)
	}
	importPhase := phaseOf(plan, "importProjectData")
	portfolioPhase := phaseOf(plan, "configurePortfolios")
	if importPhase < 0 || portfolioPhase < 0 {
		t.Fatalf("tasks missing from plan: import=%d portfolio=%d", importPhase, portfolioPhase)
	}
	if portfolioPhase <= importPhase {
		t.Fatalf("configurePortfolios (phase %d) must run AFTER importProjectData (phase %d)",
			portfolioPhase, importPhase)
	}
}

// When project-data migration is skipped, importProjectData must NOT be pulled
// back into the run just to satisfy portfolio ordering.
func TestPortfolioOrderingDoesNotForceImportWhenSkipped(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	reg = FilterByEdition(reg, "enterprise")
	// skipProjectDataMigration = true (last bool before TargetTasks).
	targets := MigrateTargetTasks(reg, "", false, false, false, true, nil)
	taskSet := ResolveDependencies(targets, reg)
	if taskSet == nil {
		t.Fatal("cannot resolve dependencies")
	}
	if taskSet["importProjectData"] {
		t.Fatal("precondition: importProjectData should be excluded when project-data migration is skipped")
	}

	orderPortfoliosAfterImport(taskSet, reg)

	if taskSet["importProjectData"] {
		t.Fatal("orderPortfoliosAfterImport must not pull importProjectData into the run")
	}
	if def := reg["configurePortfolios"]; def != nil && slices.Contains(def.Dependencies, "importProjectData") {
		t.Fatal("configurePortfolios must not depend on importProjectData when the import is skipped")
	}
	// Plan must still build (no dangling dependency).
	if _, err := PlanPhases(taskSet, reg); err != nil {
		t.Fatalf("PlanPhases: %v", err)
	}
}

// The helper is idempotent and a no-op when configurePortfolios is absent
// (e.g. community edition / transfer scope).
func TestPortfolioOrderingNoopWithoutPortfolios(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	taskSet := map[string]bool{"importProjectData": true} // no configurePortfolios
	orderPortfoliosAfterImport(taskSet, reg)
	if def := reg["configurePortfolios"]; def != nil && slices.Contains(def.Dependencies, "importProjectData") {
		t.Fatal("must not modify configurePortfolios deps when it is not in the run")
	}
}
