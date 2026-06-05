// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"testing"
)

func TestRegisterAllCountsAndDependencies(t *testing.T) {
	all := RegisterAll()
	if len(all) < 30 {
		t.Errorf("expected at least 30 tasks, got %d", len(all))
	}

	// Verify all dependencies reference tasks that exist.
	reg := BuildMigrateRegistry(all)
	for _, def := range all {
		for _, dep := range def.Dependencies {
			if _, ok := reg[dep]; !ok {
				t.Errorf("task %q depends on %q which does not exist", def.Name, dep)
			}
		}
	}
}

func TestMigrateTargetTasks(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())

	targets := MigrateTargetTasks(reg, "", false, false, false, false, nil)
	// Should exclude get*, delete*, reset* tasks.
	for _, name := range targets {
		if name[:3] == "get" || name[:6] == "delete" || name[:5] == "reset" {
			t.Errorf("unexpected target task: %q", name)
		}
	}
	if len(targets) == 0 {
		t.Error("expected non-empty target tasks")
	}
}

func TestMigrateTargetTasksSingle(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	targets := MigrateTargetTasks(reg, "createProjects", false, false, false, false, nil)
	if len(targets) != 1 || targets[0] != "createProjects" {
		t.Errorf("expected [createProjects], got %v", targets)
	}
}

func TestMigrateTargetTasksExplicitList(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	// An explicit list takes precedence over the single targetTask and the
	// default-all behavior, and is returned verbatim (dependencies are
	// resolved later by ResolveDependencies). This is how the transfer
	// command requests a project-scoped migration.
	explicit := []string{"setProjectGates", "importScanHistory", "syncIssueMetadata"}
	targets := MigrateTargetTasks(reg, "createProjects", false, false, false, false, explicit)
	if len(targets) != len(explicit) {
		t.Fatalf("expected %d explicit targets, got %v", len(explicit), targets)
	}
	for i, name := range explicit {
		if targets[i] != name {
			t.Errorf("target %d: expected %q, got %q", i, name, targets[i])
		}
	}

	// The explicit list must resolve to a valid, acyclic plan.
	taskSet := ResolveDependencies(targets, reg)
	if taskSet == nil {
		t.Fatal("explicit target tasks failed to resolve dependencies")
	}
	if _, err := PlanPhases(taskSet, reg); err != nil {
		t.Fatalf("PlanPhases failed for explicit list: %v", err)
	}
}

func TestMigrateTargetTasksSkipProfiles(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	targets := MigrateTargetTasks(reg, "", true, false, false, false, nil)
	for _, name := range targets {
		if name == "createProfiles" || name == "setProfileParent" || name == "restoreProfiles" ||
			name == "setDefaultProfiles" || name == "setProjectProfiles" || name == "setProfileGroupPermissions" {
			t.Errorf("expected profile task %q to be skipped", name)
		}
	}
}

// --no-issue-sync (or config issue-sync: false) must drop the two
// trailing metadata sync tasks but keep importScanHistory itself —
// the operator wants to bring scans across but skip the per-issue
// touch-up. #299.
func TestMigrateTargetTasksSkipIssueSync(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	targets := MigrateTargetTasks(reg, "", false, true /*includeScanHistory*/, true /*skipIssueSync*/, false /*skipProjectDataMigration*/, nil)

	var sawImport, sawIssue, sawHotspot bool
	for _, name := range targets {
		switch name {
		case "importScanHistory":
			sawImport = true
		case "syncIssueMetadata":
			sawIssue = true
		case "syncHotspotMetadata":
			sawHotspot = true
		}
	}
	if !sawImport {
		t.Error("importScanHistory should still run when only the trailing sync is opted out")
	}
	if sawIssue {
		t.Error("syncIssueMetadata should be excluded when SkipIssueSync=true")
	}
	if sawHotspot {
		t.Error("syncHotspotMetadata should be excluded when SkipIssueSync=true")
	}
}

// SkipIssueSync when IncludeScanHistory is false is a no-op for the
// two sync tasks — they were already excluded by the scan-history gate.
// The flag must not accidentally let them through.
func TestMigrateTargetTasksSkipIssueSyncWithoutScanHistory(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	targets := MigrateTargetTasks(reg, "", false, false, true, false, nil)
	for _, name := range targets {
		if name == "syncIssueMetadata" || name == "syncHotspotMetadata" {
			t.Errorf("scan-history-gated task %q must stay excluded when scan history is off", name)
		}
	}
}

func TestPlanPhasesNoCycles(t *testing.T) {
	all := RegisterAll()
	reg := BuildMigrateRegistry(all)
	targets := MigrateTargetTasks(reg, "", false, false, false, false, nil)
	taskSet := ResolveDependencies(targets, reg)
	if taskSet == nil {
		t.Fatal("cannot resolve dependencies")
	}

	plan, err := PlanPhases(taskSet, reg)
	if err != nil {
		t.Fatalf("PlanPhases failed: %v", err)
	}
	if len(plan) == 0 {
		t.Error("expected non-empty plan")
	}

	// Verify first phase has no dependencies on other migrate tasks.
	for _, taskName := range plan[0] {
		def := reg[taskName]
		if len(def.Dependencies) > 0 {
			t.Logf("phase 0 task %q has deps: %v (should all be extract tasks)", taskName, def.Dependencies)
		}
	}
}

func TestFilterByEdition(t *testing.T) {
	all := RegisterAll()
	reg := BuildMigrateRegistry(all)

	// Enterprise should include portfolio tasks.
	entReg := FilterByEdition(reg, "enterprise")
	if _, ok := entReg["createPortfolios"]; !ok {
		t.Error("expected createPortfolios in enterprise edition")
	}

	// Community should exclude portfolio tasks.
	comReg := FilterByEdition(reg, "community")
	if _, ok := comReg["createPortfolios"]; ok {
		t.Error("expected createPortfolios excluded from community edition")
	}
}

func TestMatchDevOpsPlatform(t *testing.T) {
	repos := []json.RawMessage{
		json.RawMessage(`{"id":"12345","slug":"org/myrepo","label":"My Repo"}`),
		json.RawMessage(`{"id":"67890","slug":"org/other","label":"Other"}`),
	}

	tests := []struct {
		name       string
		alm        string
		repository string
		slug       string
		expected   string
	}{
		{"github match", "github", "org/myrepo", "", "12345"},
		{"github no match", "github", "org/nomatch", "", ""},
		{"gitlab match", "gitlab", "12345", "", "12345"},
		{"gitlab no match", "gitlab", "99999", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchDevOpsPlatform(tt.alm, tt.repository, tt.slug, repos)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestExtractAnyStr(t *testing.T) {
	// String value.
	raw := json.RawMessage(`{"value":"hello"}`)
	if got := extractAnyStr(raw, "value"); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}

	// Numeric value.
	raw = json.RawMessage(`{"value":30}`)
	if got := extractAnyStr(raw, "value"); got != "30" {
		t.Errorf("expected '30', got %q", got)
	}

	// Missing key.
	raw = json.RawMessage(`{"other":"x"}`)
	if got := extractAnyStr(raw, "value"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// --skip_project_data_migration must drop importScanHistory along
// with the two trailing sync tasks. #303.
func TestMigrateTargetTasksSkipProjectDataMigration(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	targets := MigrateTargetTasks(reg, "", false, true /*includeScanHistory*/, false /*skipIssueSync*/, true /*skipProjectDataMigration*/, nil)
	for _, name := range targets {
		switch name {
		case "importScanHistory", "syncIssueMetadata", "syncHotspotMetadata":
			t.Errorf("project data migration disabled: task %q must be excluded", name)
		}
	}
}

// Without --skip_project_data_migration, all three project-data
// tasks should run. Regression guard so the skip gate doesn't
// accidentally always exclude.
func TestMigrateTargetTasksProjectDataMigrationEnabledByDefault(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	targets := MigrateTargetTasks(reg, "", false, true /*includeScanHistory*/, false /*skipIssueSync*/, false /*skipProjectDataMigration*/, nil)
	got := map[string]bool{}
	for _, name := range targets {
		got[name] = true
	}
	for _, name := range []string{"importScanHistory", "syncIssueMetadata", "syncHotspotMetadata"} {
		if !got[name] {
			t.Errorf("expected %q in the plan when project data migration is enabled", name)
		}
	}
}

// Explicit TargetTasks lists (used by transfer for project-scoped
// migration) must still respect --skip_project_data_migration —
// otherwise the operator's opt-out is silently bypassed.
func TestMigrateTargetTasksExplicitListHonorsSkipProjectDataMigration(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	explicit := []string{
		"setProjectGates",
		"importScanHistory",
		"syncIssueMetadata",
		"syncHotspotMetadata",
	}
	got := MigrateTargetTasks(reg, "", false, true, false, true /*skipProjectDataMigration*/, explicit)
	// setProjectGates is kept; the three project-data tasks are dropped.
	if len(got) != 1 || got[0] != "setProjectGates" {
		t.Errorf("expected only setProjectGates to survive, got %v", got)
	}
}

// Same explicit list with --skip_issue_sync (and project data on)
// must drop only the trailing pair; importScanHistory stays.
func TestMigrateTargetTasksExplicitListHonorsSkipIssueSync(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	explicit := []string{
		"setProjectGates",
		"importScanHistory",
		"syncIssueMetadata",
		"syncHotspotMetadata",
	}
	got := MigrateTargetTasks(reg, "", false, true, true /*skipIssueSync*/, false, explicit)
	want := map[string]bool{"setProjectGates": true, "importScanHistory": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d tasks, got %v", len(want), got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected task %q in result", n)
		}
	}
}
