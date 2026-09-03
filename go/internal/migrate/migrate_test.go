// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
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

	targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: false, SkipIssueSync: false, SkipProjectDataMigration: false}, nil, nil)
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
	targets := MigrateTargetTasks(reg, "createProjects", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: false, SkipIssueSync: false, SkipProjectDataMigration: false}, nil, nil)
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
	explicit := []string{"setProjectGates", "importProjectData", "syncIssueMetadata"}
	targets := MigrateTargetTasks(reg, "createProjects", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: false, SkipIssueSync: false, SkipProjectDataMigration: false}, explicit, nil)
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
	targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: true, IncludeProjectData: false, SkipIssueSync: false, SkipProjectDataMigration: false}, nil, nil)
	for _, name := range targets {
		if name == "createProfiles" || name == "setProfileParent" || name == "restoreProfiles" ||
			name == "setDefaultProfiles" || name == "setProjectProfiles" || name == "setProfileGroupPermissions" {
			t.Errorf("expected profile task %q to be skipped", name)
		}
	}
}

// --no-issue-sync (or config issue-sync: false) must drop the two
// trailing metadata sync tasks but keep importProjectData itself —
// the operator wants to bring scans across but skip the per-issue
// touch-up. #299.
func TestMigrateTargetTasksSkipIssueSync(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: true /*includeProjectData*/, SkipIssueSync: true /*skipIssueSync*/, SkipProjectDataMigration: false /*skipProjectDataMigration*/}, nil, nil)

	var sawImport, sawIssue, sawHotspot bool
	for _, name := range targets {
		switch name {
		case "importProjectData":
			sawImport = true
		case "syncIssueMetadata":
			sawIssue = true
		case "syncHotspotMetadata":
			sawHotspot = true
		}
	}
	if !sawImport {
		t.Error("importProjectData should still run when only the trailing sync is opted out")
	}
	if sawIssue {
		t.Error("syncIssueMetadata should be excluded when SkipIssueSync=true")
	}
	if sawHotspot {
		t.Error("syncHotspotMetadata should be excluded when SkipIssueSync=true")
	}
}

// SkipIssueSync when IncludeProjectData is false is a no-op for the
// two sync tasks — they were already excluded by the project-data gate.
// The flag must not accidentally let them through.
func TestMigrateTargetTasksSkipIssueSyncWithoutProjectData(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: false, SkipIssueSync: true, SkipProjectDataMigration: false}, nil, nil)
	for _, name := range targets {
		if name == "syncIssueMetadata" || name == "syncHotspotMetadata" {
			t.Errorf("project-data-gated task %q must stay excluded when project data is off", name)
		}
	}
}

// #536: --objects=settings must drop createProjects (and every other
// "projects" category task) from the default target set while keeping
// the "settings" category tasks — MigrateTargetTasks composes
// excludedMigrateTasks with the existing skip-profile/project-data/
// issue-sync gates via isExcludedTask.
func TestMigrateTargetTasksWithObjectsFilter(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())

	objects, err := common.ParseObjects([]string{"settings"})
	if err != nil {
		t.Fatalf("ParseObjects: %v", err)
	}
	targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: false, SkipIssueSync: false, SkipProjectDataMigration: false}, nil, objects)

	want := map[string]bool{"setGlobalSettings": true, "setGlobalWebhooks": true, "setGlobalNewCodePeriod": true}
	got := make(map[string]bool, len(targets))
	for _, name := range targets {
		got[name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("expected settings-category task %q in target set, got %v", name, targets)
		}
	}
	excludedNames := []string{"createProjects", "createGroups", "createPortfolios", "createPermissionTemplates", "createGates", "createProfiles"}
	for _, name := range excludedNames {
		if got[name] {
			t.Errorf("task %q must be excluded when --objects=settings, got target set %v", name, targets)
		}
	}

	// An explicit targetTask override always wins — objects filtering
	// does not apply (matches the documented precedence).
	single := MigrateTargetTasks(reg, "createProjects", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: false, SkipIssueSync: false, SkipProjectDataMigration: false}, nil, objects)
	if len(single) != 1 || single[0] != "createProjects" {
		t.Errorf("expected explicit targetTask override to win over objects filter, got %v", single)
	}

	// An explicit targetTasks list override also wins over objects filtering.
	explicit := []string{"createProjects"}
	explicitOut := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: false, SkipIssueSync: false, SkipProjectDataMigration: false}, explicit, objects)
	if len(explicitOut) != 1 || explicitOut[0] != "createProjects" {
		t.Errorf("expected explicit targetTasks override to win over objects filter, got %v", explicitOut)
	}
}

func TestExcludedMigrateTasksNilSelectedMeansNoExclusions(t *testing.T) {
	if excluded := excludedMigrateTasks(nil); excluded != nil {
		t.Errorf("expected nil (no exclusions) for nil selected, got %v", excluded)
	}
}

// #536: excludedMigrateTasks must never exclude the migration-tool's own
// plumbing tasks, regardless of what's selected — they're not part of
// any category and must always run so every other write succeeds.
func TestExcludedMigrateTasksNeverExcludesPlumbing(t *testing.T) {
	objects, err := common.ParseObjects([]string{"settings"})
	if err != nil {
		t.Fatalf("ParseObjects: %v", err)
	}
	excluded := excludedMigrateTasks(objects)
	plumbing := []string{
		"grantMigrationUserProjectPermissions", "getMigrationUser",
		"createMigrationGroups", "addMigrationUserToMigrationGroups",
	}
	for _, name := range plumbing {
		if excluded[name] {
			t.Errorf("plumbing task %q must never be excluded, got excluded set %v", name, excluded)
		}
	}
}

// #536: cross-category dependency edges must not pull an excluded task
// back into the resolved set — setGlobalSettings declares createProjects
// as a dependency (used for its project-scope probe), but that must not
// force createProjects to run when "projects" is excluded.
func TestResolveDependenciesExcludingDropsCrossCategoryEdge(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	objects, err := common.ParseObjects([]string{"settings"})
	if err != nil {
		t.Fatalf("ParseObjects: %v", err)
	}
	excluded := excludedMigrateTasks(objects)
	if !excluded["createProjects"] {
		t.Fatal("expected createProjects to be excluded when objects=settings")
	}

	taskSet := ResolveDependenciesExcluding([]string{"setGlobalSettings"}, reg, excluded)
	if taskSet == nil {
		t.Fatal("ResolveDependenciesExcluding returned nil")
	}
	if taskSet["createProjects"] {
		t.Error("createProjects must not be pulled in by setGlobalSettings's dependency edge when excluded")
	}
	if !taskSet["setGlobalSettings"] {
		t.Error("setGlobalSettings itself must be in the resolved set")
	}
	// generateOrganizationMappings is setGlobalSettings's other (non-excluded)
	// dependency and must still resolve normally.
	if !taskSet["generateOrganizationMappings"] {
		t.Error("expected generateOrganizationMappings to still resolve as a dependency")
	}
}

// #536 end-to-end planning check: with --objects=settings, running the
// full MigrateTargetTasks -> ResolveDependenciesExcluding -> PlanPhases
// pipeline (exactly what RunMigrate does) must never place createProjects
// in any phase — i.e. runCreateProjects (and therefore POST
// /api/projects/create) can never execute for this run, structurally,
// not just because a mock server happened not to be called.
func TestPlanPhasesObjectsSettingsOnlyNeverSchedulesCreateProjects(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	objects, err := common.ParseObjects([]string{"settings"})
	if err != nil {
		t.Fatalf("ParseObjects: %v", err)
	}
	targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: false, SkipIssueSync: false, SkipProjectDataMigration: false}, nil, objects)
	taskSet := ResolveDependenciesExcluding(targets, reg, excludedMigrateTasks(objects))
	if taskSet == nil {
		t.Fatal("cannot resolve dependencies")
	}
	if taskSet["createProjects"] {
		t.Fatal("createProjects must not be scheduled when --objects=settings")
	}
	// PlanPhasesExcluding (not plain PlanPhases) is required here: some
	// selected tasks (e.g. setGlobalSettings) declare createProjects as a
	// dependency for their project-scope probe even though it's excluded
	// from taskSet — plain PlanPhases would wait forever for that
	// dependency to complete and report a false cycle.
	plan, err := PlanPhasesExcluding(taskSet, reg, excludedMigrateTasks(objects))
	if err != nil {
		t.Fatalf("PlanPhasesExcluding failed: %v", err)
	}
	if planContainsTask(plan, "createProjects") {
		t.Fatalf("createProjects must never appear in the phase plan when --objects=settings, got plan %v", plan)
	}
	if !planContainsTask(plan, "setGlobalSettings") {
		t.Error("expected setGlobalSettings to be scheduled when --objects=settings")
	}
}

// planContainsTask reports whether name appears in any phase of plan.
func planContainsTask(plan [][]string, name string) bool {
	for _, phase := range plan {
		if slices.Contains(phase, name) {
			return true
		}
	}
	return false
}

// #536 (Gitar review on PR #555): importProjectData/syncIssueMetadata/
// syncHotspotMetadata must be excluded by --objects=settings too, not
// just createProjects — otherwise `migrate --run_id=<earlier full run>
// --objects=settings` would replay scanner reports and issue/hotspot
// sync for exactly the projects the operator deselected, since
// runImportProjectData reads createProjects from the run store
// regardless of whether createProjects ran THIS invocation. Uses
// IncludeProjectData: true — the actual CLI default
// (cfg.IncludeProjectData = !cfg.SkipProjectDataMigration,
// cmd/migrate.go) — because the sibling test above used false, which
// masked this exact bug: isExcludedTask's own !includeProjectData gate
// already excludes these three regardless of the --objects fix, so
// that test could never have caught a regression here.
func TestPlanPhasesObjectsSettingsOnlyExcludesProjectDataTasks(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	objects, err := common.ParseObjects([]string{"settings"})
	if err != nil {
		t.Fatalf("ParseObjects: %v", err)
	}
	targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{IncludeProjectData: true}, nil, objects)
	for _, name := range []string{"importProjectData", "syncIssueMetadata", "syncHotspotMetadata"} {
		if slices.Contains(targets, name) {
			t.Errorf("%s must not be scheduled when --objects=settings (with the CLI-default IncludeProjectData=true), got targets %v", name, targets)
		}
	}
}

// #536 comprehensive sweep: every single-category and pairwise
// --objects selection against the real, full task registry must
// produce a valid plan — never a false "cycle detected" error from an
// unstripped cross-category dependency edge (e.g. setProjectProfiles
// declaring createProfiles, category quality_profiles, as a
// dependency while itself being category projects). Broader than the
// settings/portfolios-specific tests above, so a future task added
// with a new cross-category dependency gets caught here even before
// anyone notices the specific combination that triggers it.
func TestPlanPhasesEveryObjectsCombinationProducesAValidPlan(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	for _, cat := range common.AllObjects {
		objects, err := common.ParseObjects([]string{cat})
		if err != nil {
			t.Fatalf("ParseObjects(%s): %v", cat, err)
		}
		targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: true, SkipIssueSync: false, SkipProjectDataMigration: false}, nil, objects)
		excluded := excludedMigrateTasks(objects)
		taskSet := ResolveDependenciesExcluding(targets, reg, excluded)
		if taskSet == nil {
			t.Errorf("objects=%s: ResolveDependenciesExcluding returned nil", cat)
			continue
		}
		if _, err := PlanPhasesExcluding(taskSet, reg, excluded); err != nil {
			t.Errorf("objects=%s: PlanPhasesExcluding: %v", cat, err)
		}
	}
	for i, a := range common.AllObjects {
		for _, b := range common.AllObjects[i+1:] {
			objects, err := common.ParseObjects([]string{a, b})
			if err != nil {
				t.Fatalf("ParseObjects(%s,%s): %v", a, b, err)
			}
			targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: true, SkipIssueSync: false, SkipProjectDataMigration: false}, nil, objects)
			excluded := excludedMigrateTasks(objects)
			taskSet := ResolveDependenciesExcluding(targets, reg, excluded)
			if taskSet == nil {
				t.Errorf("objects=%s,%s: ResolveDependenciesExcluding returned nil", a, b)
				continue
			}
			if _, err := PlanPhasesExcluding(taskSet, reg, excluded); err != nil {
				t.Errorf("objects=%s,%s: PlanPhasesExcluding: %v", a, b, err)
			}
		}
	}
}

func TestPlanPhasesNoCycles(t *testing.T) {
	all := RegisterAll()
	reg := BuildMigrateRegistry(all)
	targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: false, SkipIssueSync: false, SkipProjectDataMigration: false}, nil, nil)
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
		// GitHub / Azure / Bitbucket Cloud bind by fully qualified slug —
		// SonarQube Cloud resolves the repository by calling the platform
		// with this value (verified live against GitHub: posting the
		// numeric id 404s).
		{"github match", "github", "org/myrepo", "", "org/myrepo"},
		{"github no match", "github", "org/nomatch", "", ""},
		// GitLab bindings store the numeric project id, so that is both
		// what is matched on and what is sent back.
		{"gitlab match", "gitlab", "12345", "", "12345"},
		{"gitlab no match", "gitlab", "99999", "", ""},
		// A GitLab binding must never fall back to a name match — the
		// source only carries an opaque id, so a name guess could bind
		// the wrong repository.
		{"gitlab never falls back to name", "gitlab", "myrepo", "", ""},
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

// TestMatchDevOpsPlatformLiveShapes exercises the matcher against the
// exact payload SonarQube Cloud's /api/alm_integration/list_repositories
// returns — {label, installationKey, slug, linkedProjects, private},
// with NO `id` field. The pre-#122 implementation read `id` and therefore
// never matched anything against a live instance.
func TestMatchDevOpsPlatformLiveShapes(t *testing.T) {
	// Captured verbatim from sc-staging.io, org open-digital-society-1.
	githubRepos := []json.RawMessage{
		json.RawMessage(`{"label":"nodejs-sonar-github","installationKey":"Open-Digital-Society/nodejs-sonar-github|579847459","linkedProjects":[],"private":true,"slug":"Open-Digital-Society/nodejs-sonar-github"}`),
		json.RawMessage(`{"label":"sonarqube-example","installationKey":"Open-Digital-Society/sonarqube-example|625940442","linkedProjects":[],"private":false,"slug":"Open-Digital-Society/sonarqube-example"}`),
	}

	tests := []struct {
		name       string
		alm        string
		repository string
		slug       string
		repos      []json.RawMessage
		expected   string
	}{
		{
			name: "github exact slug match returns the slug",
			alm:  "github", repository: "Open-Digital-Society/sonarqube-example",
			repos: githubRepos, expected: "Open-Digital-Society/sonarqube-example",
		},
		{
			name: "github is case insensitive",
			alm:  "github", repository: "open-digital-society/SonarQube-Example",
			repos: githubRepos, expected: "Open-Digital-Society/sonarqube-example",
		},
		{
			// Migrating out of DevOps org "okorach" into a Cloud org bound
			// to "Open-Digital-Society": the bare repository name still
			// identifies the repo unambiguously, because
			// list_repositories only returns the bound org's repos.
			name: "github falls back to bare repository name across orgs",
			alm:  "github", repository: "okorach/sonarqube-example",
			repos: githubRepos, expected: "Open-Digital-Society/sonarqube-example",
		},
		{
			name: "github unknown repository does not match",
			alm:  "github", repository: "okorach/demo-actions-maven",
			repos: githubRepos, expected: "",
		},
		{
			// installationKey is "<slug>|<numeric id>"; the numeric part
			// is the GitLab project id a SQS GitLab binding stores.
			name: "gitlab matches the installationKey id suffix",
			alm:  "gitlab", repository: "30452699",
			repos: []json.RawMessage{
				json.RawMessage(`{"label":"demo-gitlabci-maven","installationKey":"okorach/demo-gitlabci-maven|30452699","slug":"okorach/demo-gitlabci-maven"}`),
			},
			expected: "30452699",
		},
		{
			// SonarQube Cloud labels Azure repositories
			// "<project name> / <repository name>"; a SQS Azure binding
			// carries the project name in `slug` and the repository name
			// in `repository`.
			name: "azure matches project name and repository name",
			alm:  "azure", repository: "ddd", slug: "fooo",
			repos: []json.RawMessage{
				json.RawMessage(`{"label":"fooo / ddd","installationKey":"fooo/ddd|9","slug":"fooo/ddd"}`),
			},
			expected: "fooo/ddd",
		},
		{
			name: "bitbucket cloud matches the repository slug",
			alm:  "bitbucketcloud", repository: "my-slug",
			repos: []json.RawMessage{
				json.RawMessage(`{"label":"My Slug","installationKey":"okorach/my-slug|7","slug":"okorach/my-slug"}`),
			},
			expected: "okorach/my-slug",
		},
		{
			name: "unbound project with no identifiers never matches",
			alm:  "github", repository: "", slug: "",
			repos: githubRepos, expected: "",
		},
		{
			name: "unknown platform never matches",
			alm:  "bitbucket", repository: "project3-BBS", slug: "project3-SLUG",
			repos: githubRepos, expected: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchDevOpsPlatform(tt.alm, tt.repository, tt.slug, tt.repos)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestAlmFromURL(t *testing.T) {
	tests := []struct{ url, want string }{
		{"https://github.com/Open-Digital-Society", "github"},
		{"https://gitlab.com/mygroup", "gitlab"},
		{"https://dev.azure.com/olivierkorach", "azure"},
		{"https://myorg.visualstudio.com", "azure"},
		{"https://bitbucket.org/okorach/", "bitbucketcloud"},
		{"https://bitbucket-server.your-company.com", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := almFromURL(tt.url); got != tt.want {
			t.Errorf("almFromURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestParseBoundOrganization(t *testing.T) {
	// Captured verbatim from sc-staging.io for a bound organization.
	bound := json.RawMessage(`{"almOrganization":{"key":"Open-Digital-Society","url":"","almUrl":"https://github.com/Open-Digital-Society","avatar":"https://avatars.githubusercontent.com/u/101562377?v=4","personal":false}}`)
	almURL, dopOrg := parseBoundOrganization(bound)
	if almURL != "https://github.com/Open-Digital-Society" || dopOrg != "Open-Digital-Society" {
		t.Fatalf("bound: got (%q, %q)", almURL, dopOrg)
	}

	// An unbound organization carries no almOrganization object.
	almURL, dopOrg = parseBoundOrganization(json.RawMessage(`{}`))
	if almURL != "" || dopOrg != "" {
		t.Fatalf("unbound: got (%q, %q), want empty", almURL, dopOrg)
	}
	if almFromURL(almURL) != "" {
		t.Error("unbound org must not resolve to a platform")
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

// --skip_project_data_migration must drop importProjectData along
// with the two trailing sync tasks. #303.
func TestMigrateTargetTasksSkipProjectDataMigration(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: true /*includeProjectData*/, SkipIssueSync: false /*skipIssueSync*/, SkipProjectDataMigration: true /*skipProjectDataMigration*/}, nil, nil)
	for _, name := range targets {
		switch name {
		case "importProjectData", "syncIssueMetadata", "syncHotspotMetadata":
			t.Errorf("project data migration disabled: task %q must be excluded", name)
		}
	}
}

// Without --skip_project_data_migration, all three project-data
// tasks should run. Regression guard so the skip gate doesn't
// accidentally always exclude.
func TestMigrateTargetTasksProjectDataMigrationEnabledByDefault(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	targets := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: true /*includeProjectData*/, SkipIssueSync: false /*skipIssueSync*/, SkipProjectDataMigration: false /*skipProjectDataMigration*/}, nil, nil)
	got := map[string]bool{}
	for _, name := range targets {
		got[name] = true
	}
	for _, name := range []string{"importProjectData", "syncIssueMetadata", "syncHotspotMetadata"} {
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
		"importProjectData",
		"syncIssueMetadata",
		"syncHotspotMetadata",
	}
	got := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: true, SkipIssueSync: false, SkipProjectDataMigration: true /*skipProjectDataMigration*/}, explicit, nil)
	// setProjectGates is kept; the three project-data tasks are dropped.
	if len(got) != 1 || got[0] != "setProjectGates" {
		t.Errorf("expected only setProjectGates to survive, got %v", got)
	}
}

// Same explicit list with --skip_issue_sync (and project data on)
// must drop only the trailing pair; importProjectData stays.
func TestMigrateTargetTasksExplicitListHonorsSkipIssueSync(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	explicit := []string{
		"setProjectGates",
		"importProjectData",
		"syncIssueMetadata",
		"syncHotspotMetadata",
	}
	got := MigrateTargetTasks(reg, "", MigrateTargetTasksFlags{SkipProfiles: false, IncludeProjectData: true, SkipIssueSync: true /*skipIssueSync*/, SkipProjectDataMigration: false}, explicit, nil)
	want := map[string]bool{"setProjectGates": true, "importProjectData": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d tasks, got %v", len(want), got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected task %q in result", n)
		}
	}
}

// generateRunID moved to common.GenerateRunID (#542) — its own tests,
// including the numbering-gap regression from #359, now live in
// go/internal/common/runid_test.go, deduplicated across the three
// packages (migrate, extract, wizard) that used to each hand-maintain
// an identical copy of this function and its test.
