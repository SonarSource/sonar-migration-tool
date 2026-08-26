// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	explicit := []string{"setProjectGates", "importProjectData", "syncIssueMetadata"}
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
// trailing metadata sync tasks but keep importProjectData itself —
// the operator wants to bring scans across but skip the per-issue
// touch-up. #299.
func TestMigrateTargetTasksSkipIssueSync(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	targets := MigrateTargetTasks(reg, "", false, true /*includeProjectData*/, true /*skipIssueSync*/, false /*skipProjectDataMigration*/, nil)

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
	targets := MigrateTargetTasks(reg, "", false, false, true, false, nil)
	for _, name := range targets {
		if name == "syncIssueMetadata" || name == "syncHotspotMetadata" {
			t.Errorf("project-data-gated task %q must stay excluded when project data is off", name)
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
	targets := MigrateTargetTasks(reg, "", false, true /*includeProjectData*/, false /*skipIssueSync*/, true /*skipProjectDataMigration*/, nil)
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
	targets := MigrateTargetTasks(reg, "", false, true /*includeProjectData*/, false /*skipIssueSync*/, false /*skipProjectDataMigration*/, nil)
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
	got := MigrateTargetTasks(reg, "", false, true, false, true /*skipProjectDataMigration*/, explicit)
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
	got := MigrateTargetTasks(reg, "", false, true, true /*skipIssueSync*/, false, explicit)
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

// Regression for the runID-collision bug surfaced after #359:
// generateRunID counted dirs and returned count+1, which collides
// with an existing dir as soon as the numbering has any gap
// (e.g. dirs -10..-19 with no -01..-09 would yield count=10 and
// alias onto -11). The fix returns max(N)+1 where N is the
// existing suffix on today's dirs.
func TestGenerateRunID_HandlesNumberingGaps(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")

	t.Run("empty directory yields -0001", func(t *testing.T) {
		dir := t.TempDir()
		got := generateRunID(dir)
		want := today + "-0001"
		if got != want {
			t.Errorf("want %q, got %q", want, got)
		}
	})

	t.Run("dirs -10..-19 with gaps below yields -0020 (not -11 collision)", func(t *testing.T) {
		dir := t.TempDir()
		for i := 10; i <= 19; i++ {
			if err := os.MkdirAll(filepath.Join(dir, fmt.Sprintf("%s-%02d", today, i)), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}
		got := generateRunID(dir)
		want := today + "-0020"
		if got != want {
			t.Errorf("want %q (max+1), got %q", want, got)
		}
	})

	t.Run("non-contiguous numbering still returns max+1", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []int{1, 3, 7, 42} {
			if err := os.MkdirAll(filepath.Join(dir, fmt.Sprintf("%s-%02d", today, n)), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}
		got := generateRunID(dir)
		want := today + "-0043"
		if got != want {
			t.Errorf("want %q, got %q", want, got)
		}
	})

	t.Run("dirs from other days are ignored", func(t *testing.T) {
		dir := t.TempDir()
		// Other days don't participate in the count.
		if err := os.MkdirAll(filepath.Join(dir, "2020-01-01-99"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		got := generateRunID(dir)
		want := today + "-0001"
		if got != want {
			t.Errorf("foreign-day dir should not affect count: want %q, got %q", want, got)
		}
	})

	t.Run("dirs with non-numeric suffix are ignored", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, today+"-rc1"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		got := generateRunID(dir)
		// Only well-formed dirs influence max; rc1 is skipped.
		if !strings.HasPrefix(got, today+"-") {
			t.Errorf("expected today-prefixed ID, got %q", got)
		}
		if got != today+"-0001" {
			t.Errorf("non-numeric suffix should be ignored: want %q, got %q", today+"-0001", got)
		}
	})

	// #542 — the sequence number is zero-padded to four digits so
	// that among IDs generated after this fix, lexicographic order
	// keeps matching numeric order past the 99th run of a day (a
	// pre-existing, non-padded "-99" dir from before this fix is a
	// separate, unavoidable transition case handled by
	// common.RunIDAfter instead, not by generateRunID's own output).
	t.Run("crossing the 99->100 boundary still zero-pads to four digits", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, today+"-99"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		got := generateRunID(dir)
		want := today + "-0100"
		if got != want {
			t.Errorf("want %q, got %q", want, got)
		}
	})
}
