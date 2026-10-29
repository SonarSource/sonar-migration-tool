// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

func TestBuildRegistry(t *testing.T) {
	defs := []TaskDef{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}
	reg := BuildRegistry(defs)
	if len(reg) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(reg))
	}
	if reg["a"].Name != "a" {
		t.Fatalf("expected 'a', got %q", reg["a"].Name)
	}
}

func TestFilterByEdition(t *testing.T) {
	noop := func(ctx context.Context, e *Executor) error { return nil }
	defs := []TaskDef{
		{Name: "all", Editions: AllEditions, Run: noop},
		{Name: "entOnly", Editions: []Edition{EditionEnterprise, EditionDatacenter}, Run: noop},
		{Name: "noEditions", Run: noop}, // no editions = all
	}
	reg := BuildRegistry(defs)
	filtered := FilterByEdition(reg, EditionCommunity)
	if _, ok := filtered["all"]; !ok {
		t.Error("expected 'all' in community filter")
	}
	if _, ok := filtered["entOnly"]; ok {
		t.Error("expected 'entOnly' excluded from community filter")
	}
	if _, ok := filtered["noEditions"]; !ok {
		t.Error("expected 'noEditions' in community filter (empty editions = all)")
	}
}

func TestPlanPhasesSimple(t *testing.T) {
	noop := func(ctx context.Context, e *Executor) error { return nil }
	defs := []TaskDef{
		{Name: "a", Run: noop},
		{Name: "b", Dependencies: []string{"a"}, Run: noop},
		{Name: "c", Dependencies: []string{"a"}, Run: noop},
		{Name: "d", Dependencies: []string{"b", "c"}, Run: noop},
	}
	reg := BuildRegistry(defs)
	tasks := map[string]bool{"a": true, "b": true, "c": true, "d": true}

	plan, err := PlanPhases(tasks, reg)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 3 {
		t.Fatalf("expected 3 phases, got %d: %v", len(plan), plan)
	}
	// Phase 0: a
	if len(plan[0]) != 1 || plan[0][0] != "a" {
		t.Errorf("phase 0: expected [a], got %v", plan[0])
	}
	// Phase 1: b, c (sorted)
	if len(plan[1]) != 2 {
		t.Errorf("phase 1: expected 2 tasks, got %v", plan[1])
	}
	// Phase 2: d
	if len(plan[2]) != 1 || plan[2][0] != "d" {
		t.Errorf("phase 2: expected [d], got %v", plan[2])
	}
}

func TestPlanPhasesCycle(t *testing.T) {
	noop := func(ctx context.Context, e *Executor) error { return nil }
	defs := []TaskDef{
		{Name: "a", Dependencies: []string{"b"}, Run: noop},
		{Name: "b", Dependencies: []string{"a"}, Run: noop},
	}
	reg := BuildRegistry(defs)
	tasks := map[string]bool{"a": true, "b": true}

	_, err := PlanPhases(tasks, reg)
	if err == nil {
		t.Error("expected cycle detection error")
	}
}

func TestResolveDependencies(t *testing.T) {
	noop := func(ctx context.Context, e *Executor) error { return nil }
	defs := []TaskDef{
		{Name: "a", Run: noop},
		{Name: "b", Dependencies: []string{"a"}, Run: noop},
		{Name: "c", Dependencies: []string{"b"}, Run: noop},
	}
	reg := BuildRegistry(defs)
	result := ResolveDependencies([]string{"c"}, reg)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(result))
	}
	for _, name := range []string{"a", "b", "c"} {
		if !result[name] {
			t.Errorf("expected %q in resolved deps", name)
		}
	}
}

func TestResolveDependenciesMissingDep(t *testing.T) {
	noop := func(ctx context.Context, e *Executor) error { return nil }
	defs := []TaskDef{
		{Name: "a", Dependencies: []string{"missing"}, Run: noop},
	}
	reg := BuildRegistry(defs)
	result := ResolveDependencies([]string{"a"}, reg)
	if result != nil {
		t.Error("expected nil for unresolvable dependency")
	}
}

func TestTargetTasks(t *testing.T) {
	noop := func(ctx context.Context, e *Executor) error { return nil }
	reg := BuildRegistry([]TaskDef{
		{Name: "getProjects", Run: noop},
		{Name: "getUsers", Run: noop},
		{Name: "migrate", Run: noop},
	})
	targets := TargetTasks(reg, "", "all", nil)
	if len(targets) != 2 {
		t.Fatalf("expected 2 'get*' tasks, got %d: %v", len(targets), targets)
	}

	targets = TargetTasks(reg, "getProjects", "all", nil)
	if len(targets) != 1 || targets[0] != "getProjects" {
		t.Errorf("expected single target, got %v", targets)
	}
}

func TestTargetTasksWithObjectsFilter(t *testing.T) {
	noop := func(ctx context.Context, e *Executor) error { return nil }
	reg := BuildRegistry([]TaskDef{
		{Name: "getProjects", Run: noop},
		{Name: "getGates", Run: noop},
		{Name: "getGateConditions", Run: noop},
	})

	// No filter: everything starting with "get" is included.
	all := TargetTasks(reg, "", "all", nil)
	if len(all) != 3 {
		t.Fatalf("expected 3 tasks with no objects filter, got %d: %v", len(all), all)
	}

	// quality_gates only: getProjects (category "projects") must be
	// dropped, quality-gate tasks kept.
	objects, err := common.ParseObjects([]string{"quality_gates"})
	if err != nil {
		t.Fatalf("ParseObjects: %v", err)
	}
	filtered := TargetTasks(reg, "", "all", objects)
	want := map[string]bool{"getGates": true, "getGateConditions": true}
	if len(filtered) != len(want) {
		t.Fatalf("expected %d tasks for quality_gates filter, got %d: %v", len(want), len(filtered), filtered)
	}
	for _, name := range filtered {
		if !want[name] {
			t.Errorf("unexpected task %q in quality_gates-filtered target set", name)
		}
	}

	// An explicit --target_task override always wins, objects filtering
	// does not apply.
	explicit := TargetTasks(reg, "getProjects", "all", objects)
	if len(explicit) != 1 || explicit[0] != "getProjects" {
		t.Errorf("expected explicit target_task override to win, got %v", explicit)
	}
}

// #654: getPluginIssues queried api/issues/search for every plugin rule across
// ALL projects, ignoring project_key_regexp, and nothing read its output. It
// was removed; getProjectPluginIssues is the project-scoped equivalent.
func TestGetPluginIssuesIsNotRegistered(t *testing.T) {
	registry := BuildRegistry(RegisterAll())
	if _, ok := registry["getPluginIssues"]; ok {
		t.Error("getPluginIssues must not be registered: it queries issues across every project (#654)")
	}
}

// #536 regression: getProjectPluginIssues/getProjectTemplateIssues
// (category "projects") declare getPluginRules/getTemplateRules
// (category "quality_profiles") as dependencies. Against the REAL,
// full task registry, --objects=projects excludes quality_profiles —
// including getPluginRules/getTemplateRules — while still selecting
// getProjectPluginIssues/getProjectTemplateIssues. Without
// PlanPhasesExcluding stripping the now-excluded dependency edges
// before topological sort, this surfaced as a false "cycle detected in
// task dependency graph" error (reproduced directly against buildPlan
// before the fix) instead of a valid plan.
func TestBuildPlanObjectsProjectsOnlyDoesNotFalselyCycle(t *testing.T) {
	objects, err := common.ParseObjects([]string{"projects"})
	if err != nil {
		t.Fatalf("ParseObjects: %v", err)
	}
	cfg := ExtractConfig{Objects: objects}
	_, plan, _, err := buildPlan(cfg, EditionEnterprise)
	if err != nil {
		t.Fatalf("buildPlan with objects=projects: %v", err)
	}
	if len(plan) == 0 {
		t.Fatal("expected a non-empty plan")
	}
}

// #536 comprehensive sweep: every single-category and pairwise
// --objects selection against the real, full task registry must
// produce a valid plan — never a false "cycle detected" error from an
// unstripped cross-category dependency edge. Broader than
// TestBuildPlanObjectsProjectsOnlyDoesNotFalselyCycle's one known
// scenario, so a future task added with a new cross-category
// dependency gets caught here even before anyone notices the specific
// combination that triggers it.
func TestBuildPlanEveryObjectsCombinationProducesAValidPlan(t *testing.T) {
	for _, cat := range common.AllObjects {
		objects, err := common.ParseObjects([]string{cat})
		if err != nil {
			t.Fatalf("ParseObjects(%s): %v", cat, err)
		}
		cfg := ExtractConfig{Objects: objects, IncludeProjectData: true}
		if _, _, _, err := buildPlan(cfg, EditionEnterprise); err != nil {
			t.Errorf("objects=%s: buildPlan: %v", cat, err)
		}
	}
	for i, a := range common.AllObjects {
		for _, b := range common.AllObjects[i+1:] {
			objects, err := common.ParseObjects([]string{a, b})
			if err != nil {
				t.Fatalf("ParseObjects(%s,%s): %v", a, b, err)
			}
			cfg := ExtractConfig{Objects: objects, IncludeProjectData: true}
			if _, _, _, err := buildPlan(cfg, EditionEnterprise); err != nil {
				t.Errorf("objects=%s,%s: buildPlan: %v", a, b, err)
			}
		}
	}
}

func TestRegisterAllCountsAndDependencies(t *testing.T) {
	all := RegisterAll()
	if len(all) < 60 {
		t.Errorf("expected at least 60 tasks, got %d", len(all))
	}

	// Verify all dependencies reference tasks that exist.
	reg := BuildRegistry(all)
	for _, def := range all {
		for _, dep := range def.Dependencies {
			if _, ok := reg[dep]; !ok {
				t.Errorf("task %q depends on %q which does not exist", def.Name, dep)
			}
		}
	}
}

func TestParseEdition(t *testing.T) {
	tests := []struct {
		input    string
		expected Edition
	}{
		{`{"edition":"enterprise"}`, EditionEnterprise},
		{`{"edition":"community"}`, EditionCommunity},
		{`{"edition":"developer"}`, EditionDeveloper},
		{`{"edition":"datacenter"}`, EditionDatacenter},
		{`{"edition":"unknown"}`, EditionCommunity},
		{`{}`, EditionCommunity},
		{`invalid`, EditionCommunity},
	}
	for _, tt := range tests {
		got := ParseEdition([]byte(tt.input))
		if got != tt.expected {
			t.Errorf("ParseEdition(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// #365: the per-project issue-count queries are dropped when issues or their
// sync are not migrated, while everything migrate and the maturity report read
// stays. An explicit target_task is never filtered.
func TestBuildPlanDropsIssueCountTasks(t *testing.T) {
	scheduled := func(cfg ExtractConfig) map[string]bool {
		_, _, targets, err := buildPlan(cfg, EditionEnterprise)
		if err != nil {
			t.Fatalf("buildPlan: %v", err)
		}
		set := map[string]bool{}
		for _, n := range targets {
			set[n] = true
		}
		return set
	}
	counts := []string{"getProjectIssues", "getAcceptedIssues", "getProjectFixedIssueTypes",
		"getSafeHotspots", "getProjectPluginIssues", "getProjectTemplateIssues"}

	full := scheduled(ExtractConfig{IncludeProjectData: true})
	for _, n := range counts {
		if !full[n] {
			t.Errorf("default run: %s must still be extracted", n)
		}
	}

	for name, cfg := range map[string]ExtractConfig{
		"skip_project_data_migration": {IncludeProjectData: false},
		"skip_issue_sync":             {IncludeProjectData: true, SkipIssueSync: true},
	} {
		got := scheduled(cfg)
		for _, n := range counts {
			if got[n] {
				t.Errorf("%s: %s must not be extracted", name, n)
			}
		}
		for _, n := range []string{"getProjectIssueTypes", "getProjectRecentIssueTypes", "getProjects", "getBranches", "getRuleDetails"} {
			if !got[n] {
				t.Errorf("%s: %s must still be extracted", name, n)
			}
		}
	}

	explicit := scheduled(ExtractConfig{SkipIssueSync: true, TargetTask: "getAcceptedIssues"})
	if !explicit["getAcceptedIssues"] {
		t.Error("an explicit target_task must win over the skip flags")
	}
}
