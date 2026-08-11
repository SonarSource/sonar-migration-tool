// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newFlowTest creates a complete test environment: mock servers, extract
// data, and an Executor pre-loaded with the standard create* outputs.
// The servers are closed automatically via t.Cleanup.
func newFlowTest(t *testing.T) *Executor {
	t.Helper()
	e, _ := newFlowTestWithDOP(t)
	return e
}

// newFlowTestWithDOP is newFlowTest plus the enterprise API mock's
// DevOps-binding recorder, so a test can assert that
// POST /dop-translation/project-bindings reached the enterprise host with
// the expected payload (issue #122).
func newFlowTestWithDOP(t *testing.T) (*Executor, *dopBindingRecorder) {
	t.Helper()
	cloudSrv := newMockCloudServer()
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)
	return e, dopRecorderOf(apiSrv)
}

// setupCreateOutputs populates the Store with mock createX outputs
// that downstream tasks depend on.
func setupCreateOutputs(t *testing.T, e *Executor) {
	t.Helper()
	writeItem := func(task string, data map[string]any) {
		w, _ := e.Store.Writer(task)
		b, _ := json.Marshal(data)
		w.WriteOne(b)
	}

	writeItem("createProjects", map[string]any{
		"key": "proj1", "name": "Project 1", "server_url": testServerURL,
		"cloud_project_key": "cloud-org1_proj1", "sonarcloud_org_key": testCloudOrg,
		"gate_name": testCustomGate,
		"profiles":  []map[string]any{{"key": "prof1", "name": "Custom", "language": "java"}},
	})

	writeItem("createProfiles", map[string]any{
		"name": "Custom", "language": "java", "parent_name": "Sonar way",
		"source_profile_key": "prof1", "sonarcloud_org_key": testCloudOrg,
		"cloud_profile_key": "cloud-prof-1", "is_default": true,
	})

	writeItem("createGates", map[string]any{
		"name": testCustomGate, "source_gate_key": testCustomGate,
		"sonarcloud_org_key": testCloudOrg, "cloud_gate_id": "42", "is_default": true,
		"server_url": testServerURL,
	})

	writeItem("createGroups", map[string]any{
		"name": "sonar-users", "sonarcloud_org_key": testCloudOrg, "cloud_group_id": "101",
	})

	writeItem("createPermissionTemplates", map[string]any{
		"name": "Default", "sonarcloud_org_key": testCloudOrg,
		"cloud_template_id": "tpl-cloud-1", "is_default": true,
	})

	writeItem("generateOrganizationMappings", map[string]any{
		"sonarqube_org_key": "org1", "sonarcloud_org_key": testCloudOrg,
		"server_url": testServerURL,
	})

	writeItem("createMigrationGroups", map[string]any{
		"sonarcloud_org_key": testCloudOrg,
		"groups":             []string{"migration-scanners", "migration-viewers"},
	})

	writeItem("getMigrationUser", map[string]any{
		"login": "migration-user", "name": "Migration User",
	})

	writeItem("generateProjectMappings", map[string]any{
		"key": "proj1", "name": "Project 1", "server_url": testServerURL,
		"sonarcloud_org_key": testCloudOrg, "alm": "github",
		"repository": "myorg/myrepo", "is_cloud_binding": true,
	})
}

func TestSetProfileParent(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProfileParent"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProfileParent: %v", err)
	}
}

func TestSetDefaultProfiles(t *testing.T) {
	e := newFlowTest(t)

	// restoreProfiles is a dependency — stub it.
	w, _ := e.Store.Writer("restoreProfiles")
	w.WriteChunk(nil)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setDefaultProfiles"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setDefaultProfiles: %v", err)
	}
}

func TestSetDefaultGates(t *testing.T) {
	e := newFlowTest(t)

	// addGateConditions dependency.
	w, _ := e.Store.Writer("addGateConditions")
	w.WriteChunk(nil)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setDefaultGates"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setDefaultGates: %v", err)
	}
}

func TestSetDefaultTemplates(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setDefaultTemplates"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setDefaultTemplates: %v", err)
	}
}

func TestAddGateConditions(t *testing.T) {
	e := newFlowTest(t)

	// getGateConditions dependency — write mock data with conditions.
	w, _ := e.Store.Writer("getGateConditions")
	b, _ := json.Marshal(map[string]any{"sonarcloud_org_key": testCloudOrg, "cloud_gate_id": "42", "conditions": []map[string]any{{"metric": "coverage", "op": "LT", "error": "80"}}})
	w.WriteOne(b)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["addGateConditions"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("addGateConditions: %v", err)
	}
}

func TestRestoreProfiles(t *testing.T) {
	e := newFlowTest(t)

	// setProfileParent dependency.
	w, _ := e.Store.Writer("setProfileParent")
	w.WriteChunk(nil)

	// getProfileBackups dependency.
	w2, _ := e.Store.Writer("getProfileBackups")
	b2, _ := json.Marshal(map[string]any{"profileKey": "prof1", "sonarcloud_org_key": testCloudOrg, "backup": "<profile><name>Custom</name></profile>"})
	w2.WriteOne(b2)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["restoreProfiles"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("restoreProfiles: %v", err)
	}
}

func TestSetProjectProfiles(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProjectProfiles"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectProfiles: %v", err)
	}
}

// TestSetProjectProfilesUsesExplicitAssignmentsOnly is a regression
// for issue #160. The old implementation drove off the
// qualityProfiles array embedded in createProjects (sourced from
// api/navigation/component) which reports the profile used in the
// LAST ANALYSIS — historical data that can list a project as bound
// to a custom profile even after the explicit binding has been
// removed. The fix drives off getProfileProjects, which queries
// /api/qualityprofiles/projects?selected=selected — exactly the
// explicitly-assigned projects.
//
// This test seeds two projects (projA + projB), both in the same
// org. The getProfileProjects extract data lists ONLY projA as
// assigned to "Custom Python" — projB has no explicit assignment.
// Even if a stale navigation/component capture had listed projB as
// using "Custom Python", the new code path must not call
// AddProject for projB.
func TestSetProjectProfilesUsesExplicitAssignmentsOnly(t *testing.T) {
	type addProjectCall struct {
		language, profile, project, org string
	}
	var (
		mu    sync.Mutex
		calls []addProjectCall
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/qualityprofiles/add_project", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		calls = append(calls, addProjectCall{
			language: r.FormValue("language"),
			profile:  r.FormValue("qualityProfile"),
			project:  r.FormValue("project"),
			org:      r.FormValue("organization"),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()

	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	setupExtractData(dir)
	// Replace the default getProfileProjects with our scenario: only
	// projA is explicitly assigned to "Custom Python".
	writeJSONL(filepath.Join(dir, "extract-01", "getProfileProjects"), []map[string]any{
		{"key": "projA", "name": "Project A", "selected": true,
			"profileKey": "py-custom", "profileName": "Custom Python", "language": "py",
			"serverUrl": testServerURL},
	})

	e := newTestExecutor(cloudSrv, apiSrv, dir)

	// Both projects exist and are migrated. createProjects rows carry
	// the SQS key + cloud key + org.
	w, _ := e.Store.Writer("createProjects")
	for _, src := range []string{"projA", "projB"} {
		b, _ := json.Marshal(map[string]any{
			"server_url":         testServerURL,
			"key":                src,
			"cloud_project_key":  testCloudOrg + "_" + src,
			"sonarcloud_org_key": testCloudOrg,
		})
		w.WriteOne(b)
	}

	// "Custom Python" is migrated.
	w, _ = e.Store.Writer("createProfiles")
	b, _ := json.Marshal(map[string]any{
		"name":               "Custom Python",
		"language":           "py",
		"sonarcloud_org_key": testCloudOrg,
		"cloud_profile_key":  "py-custom-cloud",
		"source_profile_key": "py-custom",
	})
	w.WriteOne(b)

	if err := runSetProjectProfiles(context.Background(), e); err != nil {
		t.Fatalf("runSetProjectProfiles: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 AddProject call (projA), got %d: %+v", len(calls), calls)
	}
	got := calls[0]
	wantProject := testCloudOrg + "_projA"
	if got.project != wantProject {
		t.Errorf("AddProject project: got %q want %q", got.project, wantProject)
	}
	if got.profile != "Custom Python" || got.language != "py" {
		t.Errorf("AddProject profile/lang: got %s/%s want \"Custom Python\"/\"py\"", got.profile, got.language)
	}
	for _, c := range calls {
		if c.project == testCloudOrg+"_projB" {
			t.Errorf("projB must NOT receive AddProject — no explicit assignment in getProfileProjects")
		}
	}
}

func TestSetProjectGates(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProjectGates"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectGates: %v", err)
	}
}

func TestSetProjectGroupPermissions(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProjectGroupPermissions"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectGroupPermissions: %v", err)
	}
}

func TestSetProjectSettings(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProjectSettings"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectSettings: %v", err)
	}
}

func TestSetProjectTags(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProjectTags"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectTags: %v", err)
	}
}

// TestSetProjectSourceLink asserts the migration adds a "SQS migrated
// project" link back to the original SonarQube Server dashboard for
// every migrated project (#418).
func TestSetProjectSourceLink(t *testing.T) {
	type call struct {
		project string
		name    string
		url     string
	}
	var (
		mu       sync.Mutex
		recorded []call
	)
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/project_links/create", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		recorded = append(recorded, call{
			project: r.FormValue("projectKey"),
			name:    r.FormValue("name"),
			url:     r.FormValue("url"),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	cloudMux.HandleFunc("GET /api/project_links/search", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"links": []map[string]any{}})
	})
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	pw, _ := e.Store.Writer("createProjects")
	data, _ := json.Marshal(map[string]any{
		"key": "proj1", "server_url": testServerURL,
		"cloud_project_key": "cloud-org1_proj1", "sonarcloud_org_key": testCloudOrg,
	})
	_ = pw.WriteOne(data)

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["setProjectSourceLink"].Run(context.Background(), e); err != nil {
		t.Fatalf("setProjectSourceLink: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 1 {
		t.Fatalf("expected 1 project_links/create call, got %d: %+v", len(recorded), recorded)
	}
	wantURL := "https://sq.test/dashboard?id=proj1"
	if got := recorded[0]; got.project != "cloud-org1_proj1" || got.name != migratedProjectLinkName || got.url != wantURL {
		t.Fatalf("unexpected create call: %+v (want project=cloud-org1_proj1 name=%q url=%q)",
			got, migratedProjectLinkName, wantURL)
	}
}

// TestSetProjectSourceLinkSkipsExisting asserts a re-run doesn't create a
// duplicate link when one with the same name and URL already exists on
// the target project.
func TestSetProjectSourceLinkSkipsExisting(t *testing.T) {
	var (
		mu          sync.Mutex
		createCalls int
	)
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/project_links/create", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		createCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	cloudMux.HandleFunc("GET /api/project_links/search", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"links": []map[string]any{
				{"name": migratedProjectLinkName, "url": "https://sq.test/dashboard?id=proj1"},
			},
		})
	})
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	pw, _ := e.Store.Writer("createProjects")
	data, _ := json.Marshal(map[string]any{
		"key": "proj1", "server_url": testServerURL,
		"cloud_project_key": "cloud-org1_proj1", "sonarcloud_org_key": testCloudOrg,
	})
	_ = pw.WriteOne(data)

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["setProjectSourceLink"].Run(context.Background(), e); err != nil {
		t.Fatalf("setProjectSourceLink: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if createCalls != 0 {
		t.Fatalf("expected no create call when link already exists, got %d", createCalls)
	}
}

func TestCreateMigrationGroups(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createMigrationGroups"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createMigrationGroups: %v", err)
	}
}

// TestGrantMigrationUserProjectPermissions asserts each newly-created
// project receives the four expected permission grants for the
// migration user (issue #190): user, admin, issueadmin,
// securityhotspotadmin. The grant fires BEFORE the per-project
// mutations downstream — verified here by the dependency-graph
// assertion at the bottom.
func TestGrantMigrationUserProjectPermissions(t *testing.T) {
	type call struct {
		login      string
		permission string
		project    string
		org        string
	}
	var (
		mu       sync.Mutex
		recorded []call
	)
	// Custom cloud mock that captures add_user calls. Everything else
	// is taken from newMockCloudServer's behaviour (it would otherwise
	// answer 200 to whatever else is hit during ReadAll fixture).
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/permissions/add_user", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		recorded = append(recorded, call{
			login:      r.FormValue("login"),
			permission: r.FormValue("permission"),
			project:    r.FormValue("projectKey"),
			org:        r.FormValue("organization"),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)
	// getMigrationUser → login.
	uw, _ := e.Store.Writer("getMigrationUser")
	uw.WriteOne([]byte(`{"login":"migration-bot","name":"Migration Bot"}`))
	// createProjects → two projects across two orgs.
	pw, _ := e.Store.Writer("createProjects")
	for _, p := range []map[string]any{
		{"key": "src-a", "server_url": testServerURL,
			"sonarcloud_org_key": "orgA", "cloud_project_key": "orgA_src-a"},
		{"key": "src-b", "server_url": testServerURL,
			"sonarcloud_org_key": "orgB", "cloud_project_key": "orgB_src-b"},
	} {
		b, _ := json.Marshal(p)
		pw.WriteOne(b)
	}

	if err := runGrantMigrationUserProjectPermissions(context.Background(), e); err != nil {
		t.Fatalf("runGrantMigrationUserProjectPermissions: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// 2 projects × 4 permissions = 8 calls.
	if len(recorded) != 8 {
		t.Fatalf("expected 8 calls (2 projects × 4 perms), got %d: %+v", len(recorded), recorded)
	}
	// Assert: every call carries the migration-user login, each
	// project receives EXACTLY the 4 permissions in the list, and
	// the project + org pair on the call match createProjects.
	wantPerms := map[string]bool{
		"user":                 true,
		"admin":                true,
		"issueadmin":           true,
		"securityhotspotadmin": true,
	}
	gotPermsPerProject := make(map[string]map[string]int)
	for _, c := range recorded {
		if c.login != "migration-bot" {
			t.Errorf("login: want migration-bot, got %q", c.login)
		}
		if !wantPerms[c.permission] {
			t.Errorf("unexpected permission %q on project %q", c.permission, c.project)
		}
		if gotPermsPerProject[c.project] == nil {
			gotPermsPerProject[c.project] = make(map[string]int)
		}
		gotPermsPerProject[c.project][c.permission]++
	}
	for _, project := range []string{"orgA_src-a", "orgB_src-b"} {
		got := gotPermsPerProject[project]
		for perm := range wantPerms {
			if got[perm] != 1 {
				t.Errorf("project %q permission %q: want exactly 1 grant, got %d",
					project, perm, got[perm])
			}
		}
	}
}

// Per-project mutations must depend on
// grantMigrationUserProjectPermissions so the DAG runs the grant
// BEFORE any project-scoped write. Pin the dependency at the
// registry level so a refactor that drops the dep is caught here.
func TestGrantMigrationUserIsAPrerequisiteForPerProjectTasks(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	for _, name := range []string{
		"setProjectProfiles",
		"setProjectGates",
		"setProjectGroupPermissions",
		"setProjectSettings",
		"setProjectTags",
		"setNewCodePeriods",
		"setProjectBinding",
	} {
		task := reg[name]
		if task == nil {
			t.Errorf("task %q not registered", name)
			continue
		}
		found := false
		for _, dep := range task.Dependencies {
			if dep == "grantMigrationUserProjectPermissions" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("task %q must list grantMigrationUserProjectPermissions in Dependencies, got %v",
				name, task.Dependencies)
		}
	}
}

func TestAddMigrationUserToGroups(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["addMigrationUserToMigrationGroups"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("addMigrationUserToMigrationGroups: %v", err)
	}
}

func TestAddMigrationGroupToTemplates(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["addMigrationGroupToTemplates"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("addMigrationGroupToTemplates: %v", err)
	}
}

func TestSetOrgGroupPermissions(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setOrgGroupPermissions"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setOrgGroupPermissions: %v", err)
	}
}

func TestSetProfileGroupPermissions(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProfileGroupPermissions"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProfileGroupPermissions: %v", err)
	}
}

func TestUpdateRuleTags(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["updateRuleTags"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("updateRuleTags: %v", err)
	}

	items, _ := e.Store.ReadAll("updateRuleTags")
	if len(items) == 0 {
		t.Error("expected updateRuleTags output")
	}
}

func TestUpdateRuleDescriptions(t *testing.T) {
	e := newFlowTest(t)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["updateRuleDescriptions"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("updateRuleDescriptions: %v", err)
	}

	items, _ := e.Store.ReadAll("updateRuleDescriptions")
	if len(items) == 0 {
		t.Error("expected updateRuleDescriptions output")
	}
}

func TestApplyGroupPermissions(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	w, _ := e.Store.Writer("testApplyGroupPerms")
	pm := projectMapping{CloudKey: "cloud-proj1", OrgKey: testCloudOrg}

	// Valid permissions.
	data := json.RawMessage(`{"name":"devs","permissions":["scan","user"]}`)
	applyGroupPermissions(context.Background(), e, data, pm, w, NewTaskCounter("test"))

	items, _ := e.Store.ReadAll("testApplyGroupPerms")
	if len(items) != 1 {
		t.Errorf("expected 1 output item, got %d", len(items))
	}
	// Verify cloud_project_key was enriched.
	if extractField(items[0], "cloud_project_key") != "cloud-proj1" {
		t.Error("expected cloud_project_key enrichment")
	}

	// Invalid permissions should be skipped (no error).
	w2, _ := e.Store.Writer("testApplyGroupPermsInvalid")
	data2 := json.RawMessage(`{"name":"devs","permissions":["bogus","fake"]}`)
	applyGroupPermissions(context.Background(), e, data2, pm, w2, NewTaskCounter("test"))

	items2, _ := e.Store.ReadAll("testApplyGroupPermsInvalid")
	if len(items2) != 1 {
		t.Errorf("expected 1 output item (still written), got %d", len(items2))
	}

	// Verify counter tracks successes.
	counter := NewTaskCounter("test")
	w3, _ := e.Store.Writer("testApplyGroupPermsCounter")
	data3 := json.RawMessage(`{"name":"devs","permissions":["scan"]}`)
	applyGroupPermissions(context.Background(), e, data3, pm, w3, counter)
	if counter.succeeded.Load() != 1 {
		t.Errorf("expected 1 success, got %d", counter.succeeded.Load())
	}

	// Verify counter tracks failures (use failing server).
	failSrv := newFailingCloudServer()
	defer failSrv.Close()
	failE := newTestExecutor(failSrv, apiSrv, dir)
	failCounter := NewTaskCounter("test")
	w4, _ := failE.Store.Writer("testApplyGroupPermsFail")
	applyGroupPermissions(context.Background(), failE, data3, pm, w4, failCounter)
	if failCounter.failed.Load() != 1 {
		t.Errorf("expected 1 failure, got %d", failCounter.failed.Load())
	}
}

func TestApplyOrgPermissions(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	// Valid permissions.
	data := json.RawMessage(`{"permissions":["scan","admin"]}`)
	applyOrgPermissions(context.Background(), e, data, "devs", testCloudOrg, NewTaskCounter("test"))

	// Invalid permissions — should not panic.
	data2 := json.RawMessage(`{"permissions":["bogus"]}`)
	applyOrgPermissions(context.Background(), e, data2, "devs", testCloudOrg, NewTaskCounter("test"))

	// Empty permissions — should be a no-op.
	data3 := json.RawMessage(`{"permissions":[]}`)
	applyOrgPermissions(context.Background(), e, data3, "devs", testCloudOrg, NewTaskCounter("test"))

	// Verify counter tracks successes and failures.
	counter := NewTaskCounter("test")
	data4 := json.RawMessage(`{"permissions":["scan","admin"]}`)
	applyOrgPermissions(context.Background(), e, data4, "devs", testCloudOrg, counter)
	if counter.succeeded.Load() != 2 {
		t.Errorf("expected 2 successes, got %d", counter.succeeded.Load())
	}

	failSrv := newFailingCloudServer()
	defer failSrv.Close()
	failE := newTestExecutor(failSrv, apiSrv, dir)
	failCounter := NewTaskCounter("test")
	applyOrgPermissions(context.Background(), failE, data4, "devs", testCloudOrg, failCounter)
	if failCounter.failed.Load() != 2 {
		t.Errorf("expected 2 failures, got %d", failCounter.failed.Load())
	}
}

func TestDeleteTasks(t *testing.T) {
	e := newFlowTest(t)

	// getCreatedProjects dependency for deleteProjects.
	w, _ := e.Store.Writer("getCreatedProjects")
	w.WriteOne(json.RawMessage(`{"key":"cloud-org1_proj1","sonarcloud_org_key":"cloud-org1"}`))

	// setDefaultProfiles/Gates/Templates dependencies for reset tasks.
	for _, task := range []string{"setDefaultProfiles", "setDefaultGates", "setDefaultTemplates"} {
		wt, _ := e.Store.Writer(task)
		wt.WriteChunk(nil)
	}

	reg := BuildMigrateRegistry(RegisterAll())

	deleteTasks := []string{
		"deleteProjects", "deleteProfiles", "deleteGates", "deleteGroups",
		"deleteTemplates", "resetDefaultProfiles", "resetDefaultGates", "resetPermissionTemplates",
		"resetGlobalSettings",
	}
	for _, name := range deleteTasks {
		t.Run(name, func(t *testing.T) {
			def, ok := reg[name]
			if !ok {
				t.Skipf("task %q not in registry (may be edition-filtered)", name)
			}
			err := def.Run(context.Background(), e)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
	}
}

func TestDeletePortfolios(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	// createPortfolios dependency.
	w, _ := e.Store.Writer("createPortfolios")
	w.WriteOne(json.RawMessage(`{"cloud_portfolio_id":"portfolio-1","name":"Test"}`))

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["deletePortfolios"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("deletePortfolios: %v", err)
	}
}

func TestMatchProjectReposAndBind(t *testing.T) {
	e, dop := newFlowTestWithDOP(t)

	// getProjectIds dependency.
	writeItem := func(task string, data map[string]any) {
		w, _ := e.Store.Writer(task)
		b, _ := json.Marshal(data)
		w.WriteOne(b)
	}

	writeItem("getProjectIds", map[string]any{
		"key": "cloud-org1_proj1", "id": "proj-id-1", "sonarcloud_org_key": testCloudOrg,
	})
	writeItem("getOrgRepos", map[string]any{
		"id": "repo-123", "slug": "myorg/myrepo", "label": "myrepo", "sonarcloud_org_key": testCloudOrg,
	})
	// Issue #122: the target org must itself be bound to the same DevOps
	// platform as the source project before a binding is attempted.
	writeItem("getOrgBinding", map[string]any{
		"sonarcloud_org_key": testCloudOrg, "bound": true,
		"alm": "github", "dop_organization": "myorg",
		"alm_url": "https://github.com/myorg",
	})

	reg := BuildMigrateRegistry(RegisterAll())

	// matchProjectRepos.
	err := reg["matchProjectRepos"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("matchProjectRepos: %v", err)
	}

	items, _ := e.Store.ReadAll("matchProjectRepos")
	if len(items) == 0 {
		t.Fatal("expected matchProjectRepos output")
	}
	if extractBool(items[0], "binding_skipped") {
		t.Fatalf("expected a binding record, got a skip: %s", items[0])
	}
	// GitHub binds by fully qualified slug, not by the numeric id.
	if repoID := extractField(items[0], "repository_id"); repoID != "myorg/myrepo" {
		t.Errorf("expected myorg/myrepo, got %q", repoID)
	}
	if projID := extractField(items[0], "project_id"); projID != "proj-id-1" {
		t.Errorf("expected proj-id-1, got %q", projID)
	}

	// setProjectBinding.
	err = reg["setProjectBinding"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectBinding: %v", err)
	}
	bindings, _ := e.Store.ReadAll("setProjectBinding")
	if len(bindings) != 1 {
		t.Fatalf("expected 1 setProjectBinding record, got %d", len(bindings))
	}
	if status := extractField(bindings[0], "status"); status != "success" {
		t.Errorf("expected status success, got %q (%s)", status, bindings[0])
	}

	// The write must have reached the ENTERPRISE host (issue #122). The
	// standard-host mock 404s this path, so a regression to e.Cloud would
	// surface as status=failed above and zero recorded requests here.
	reqs := dop.All()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 POST /dop-translation/project-bindings on the enterprise host, got %d", len(reqs))
	}
	if reqs[0]["projectId"] != "proj-id-1" || reqs[0]["repositoryId"] != "myorg/myrepo" {
		t.Errorf("unexpected binding payload: %v", reqs[0])
	}
}

// TestMatchProjectReposOrgNotBound covers the issue #122 requirement that a
// source project which WAS bound, but whose target organization is not
// bound to the same DevOps platform, is recorded as a skip carrying the
// exact explanation the migration report must show.
func TestMatchProjectReposOrgNotBound(t *testing.T) {
	cases := []struct {
		name       string
		orgBinding map[string]any
	}{
		{
			name: "org not bound at all",
			orgBinding: map[string]any{
				"sonarcloud_org_key": testCloudOrg, "bound": false,
			},
		},
		{
			// The staging org is bound to GitHub, so a GitLab/Azure/
			// Bitbucket-bound source project hits this branch.
			name: "org bound to a different platform",
			orgBinding: map[string]any{
				"sonarcloud_org_key": testCloudOrg, "bound": true,
				"alm": "gitlab", "dop_organization": "somegroup",
				"alm_url": "https://gitlab.com/somegroup",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newFlowTest(t)
			seedBindingInputs(t, e, tc.orgBinding)

			reg := BuildMigrateRegistry(RegisterAll())
			if err := reg["matchProjectRepos"].Run(context.Background(), e); err != nil {
				t.Fatalf("matchProjectRepos: %v", err)
			}
			assertOrgNotBoundSkip(t, e)

			// setProjectBinding forwards the skip verbatim without calling
			// the DevOps binding endpoint.
			if err := reg["setProjectBinding"].Run(context.Background(), e); err != nil {
				t.Fatalf("setProjectBinding: %v", err)
			}
			assertSkipForwarded(t, e)
		})
	}
}

// seedBindingInputs writes the matchProjectRepos inputs: one created cloud
// project, one bindable repository in the target org, and the target org's
// own DevOps platform binding.
func seedBindingInputs(t *testing.T, e *Executor, orgBinding map[string]any) {
	t.Helper()
	writeItem := func(task string, data map[string]any) {
		w, _ := e.Store.Writer(task)
		b, _ := json.Marshal(data)
		w.WriteOne(b)
	}
	writeItem("getProjectIds", map[string]any{
		"key": "cloud-org1_proj1", "id": "proj-id-1", "sonarcloud_org_key": testCloudOrg,
	})
	writeItem("getOrgRepos", map[string]any{
		"id": "repo-123", "slug": "myorg/myrepo", "label": "myrepo",
		"sonarcloud_org_key": testCloudOrg,
	})
	writeItem("getOrgBinding", orgBinding)
}

// assertOrgNotBoundSkip asserts matchProjectRepos produced exactly one skip
// record carrying the issue #122 org-not-bound reason and no binding ids.
func assertOrgNotBoundSkip(t *testing.T, e *Executor) {
	t.Helper()
	items, _ := e.Store.ReadAll("matchProjectRepos")
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 skip record, got %d", len(items))
	}
	rec := items[0]
	if !extractBool(rec, "binding_skipped") {
		t.Fatalf("expected binding_skipped=true, got %s", rec)
	}
	if got := extractField(rec, "skip_reason"); got != BindingSkipOrgNotBound {
		t.Errorf("skip_reason = %q, want %q", got, BindingSkipOrgNotBound)
	}
	const want = "project binding was not possible because the org itself is not bound"
	if got := extractField(rec, "skip_detail"); got != want {
		t.Errorf("skip_detail = %q, want %q", got, want)
	}
	// No binding must have been attempted.
	if id := extractField(rec, "project_id"); id != "" {
		t.Errorf("expected no project_id on a skip record, got %q", id)
	}
}

// assertSkipForwarded asserts setProjectBinding passed the skip record
// through untouched, without recording a write status.
func assertSkipForwarded(t *testing.T, e *Executor) {
	t.Helper()
	out, _ := e.Store.ReadAll("setProjectBinding")
	if len(out) != 1 || !extractBool(out[0], "binding_skipped") {
		t.Fatalf("expected the skip record forwarded, got %v", out)
	}
	if extractField(out[0], "status") != "" {
		t.Errorf("skip record must carry no write status: %s", out[0])
	}
}

// TestMatchProjectReposUnboundSourceProject covers the issue #122
// requirement that no binding is attempted when the source project is not
// bound on the SonarQube Server side — not even a skip record, because
// there is nothing partial about it.
func TestMatchProjectReposUnboundSourceProject(t *testing.T) {
	e := newFlowTest(t)

	// A project mapping with no DevOps binding at all.
	w, _ := e.Store.Writer("generateProjectMappings")
	pm, _ := json.Marshal(map[string]any{
		"key": "proj1", "sonarcloud_org_key": testCloudOrg,
		"alm": "", "repository": "", "is_cloud_binding": false,
	})
	w.WriteOne(pm)

	writeItem := func(task string, data map[string]any) {
		wr, _ := e.Store.Writer(task)
		b, _ := json.Marshal(data)
		wr.WriteOne(b)
	}
	writeItem("getProjectIds", map[string]any{
		"key": "cloud-org1_proj1", "id": "proj-id-1", "sonarcloud_org_key": testCloudOrg,
	})
	writeItem("getOrgBinding", map[string]any{
		"sonarcloud_org_key": testCloudOrg, "bound": true, "alm": "github",
	})

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["matchProjectRepos"].Run(context.Background(), e); err != nil {
		t.Fatalf("matchProjectRepos: %v", err)
	}
	items, _ := e.Store.ReadAll("matchProjectRepos")
	if len(items) != 0 {
		t.Fatalf("expected no records for an unbound source project, got %d: %v", len(items), items)
	}
}

// TestGetOrgBinding exercises the org DevOps-binding lookup against the
// live response shape.
func TestGetOrgBinding(t *testing.T) {
	e := newFlowTest(t)

	w, _ := e.Store.Writer("generateOrganizationMappings")
	om, _ := json.Marshal(map[string]any{"sonarcloud_org_key": testCloudOrg})
	w.WriteOne(om)

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["getOrgBinding"].Run(context.Background(), e); err != nil {
		t.Fatalf("getOrgBinding: %v", err)
	}
	items, _ := e.Store.ReadAll("getOrgBinding")
	if len(items) != 1 {
		t.Fatalf("expected 1 record, got %d", len(items))
	}
	if !extractBool(items[0], "bound") {
		t.Errorf("expected bound=true, got %s", items[0])
	}
	if got := extractField(items[0], "alm"); got != "github" {
		t.Errorf("alm = %q, want github", got)
	}
	if got := extractField(items[0], "dop_organization"); got != "myorg" {
		t.Errorf("dop_organization = %q, want myorg", got)
	}
}

// cloudUnboundOrg500Body is the verbatim body SonarQube Cloud returns
// from show_bound_organization for an organization that is NOT bound to
// a DevOps platform. Probed live against sc-staging.io for issue #505:
// two of three real orgs answered HTTP 500 with exactly this, so a 500
// here is the normal "not bound" answer rather than a transient fault.
const cloudUnboundOrg500Body = `{"errors":[{"msg":"An unexpected error occurred. Please try again later."}]}`

// newOrgTaskTest builds a flow executor whose SonarQube Cloud host
// answers `pattern` with h (everything else: an empty JSON object), and
// seeds the single organization mapping the org-scoped tasks iterate.
func newOrgTaskTest(t *testing.T, pattern string, h http.HandlerFunc) *Executor {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, h)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(mux)
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	w, _ := e.Store.Writer("generateOrganizationMappings")
	om, _ := json.Marshal(map[string]any{"sonarcloud_org_key": testCloudOrg})
	w.WriteOne(om)
	return e
}

// respondWith answers with a fixed status and body.
func respondWith(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
}

// runOrgBindingTask runs getOrgBinding, asserts it did NOT abort the
// migration (issue #505), and returns its single record.
func runOrgBindingTask(t *testing.T, e *Executor) json.RawMessage {
	t.Helper()
	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["getOrgBinding"].Run(context.Background(), e); err != nil {
		t.Fatalf("getOrgBinding must never abort the migration, got: %v", err)
	}
	items, _ := e.Store.ReadAll("getOrgBinding")
	if len(items) != 1 {
		t.Fatalf("expected 1 record, got %d: %v", len(items), items)
	}
	if extractBool(items[0], "bound") {
		t.Errorf("expected bound=false, got %s", items[0])
	}
	return items[0]
}

// TestGetOrgBindingUnboundOrgAnswers500 is the issue #505 regression
// test. An unbound SonarQube Cloud org answers this endpoint with HTTP
// 500; the old code treated anything but 400/403/404 as fatal, so the
// entire run died with "phase 2: task getOrgBinding: ...". The 500 is a
// plain "not bound", recorded as such and never surfaced as a lookup
// failure.
func TestGetOrgBindingUnboundOrgAnswers500(t *testing.T) {
	e := newOrgTaskTest(t, "GET /api/alm_integration/show_bound_organization",
		respondWith(http.StatusInternalServerError, cloudUnboundOrg500Body))

	rec := runOrgBindingTask(t, e)
	if got := extractField(rec, "lookup_error"); got != "" {
		t.Errorf("500 is Cloud's normal unbound answer and must not be recorded "+
			"as a failed lookup, got lookup_error=%q", got)
	}
}

// TestGetOrgBindingOrgNotFound404 pins the pre-existing behaviour for the
// answer that really is a 404 — "Could not find organization with key
// ..." — which also means there is no binding to replicate.
func TestGetOrgBindingOrgNotFound404(t *testing.T) {
	e := newOrgTaskTest(t, "GET /api/alm_integration/show_bound_organization",
		respondWith(http.StatusNotFound,
			`{"errors":[{"msg":"Could not find organization with key 'cloud-org1'"}]}`))

	rec := runOrgBindingTask(t, e)
	if got := extractField(rec, "lookup_error"); got != "" {
		t.Errorf("a 404 is an answer, not a failed lookup, got lookup_error=%q", got)
	}
}

// TestGetOrgBindingUnexpectedFailureDegrades covers the other half of
// issue #505: reading an org's DevOps binding only enables the optional
// project-binding extra, so an unexpected failure of it degrades to
// "unknown" and lets the migration finish — while still recording WHY so
// the report can tell it apart from a genuine unbound org.
func TestGetOrgBindingUnexpectedFailureDegrades(t *testing.T) {
	e := newOrgTaskTest(t, "GET /api/alm_integration/show_bound_organization",
		respondWith(http.StatusBadGateway, `{"errors":[{"msg":"Bad gateway"}]}`))

	rec := runOrgBindingTask(t, e)
	got := extractField(rec, "lookup_error")
	if !strings.Contains(got, "502") || !strings.Contains(got, "Bad gateway") {
		t.Errorf("lookup_error = %q, want the underlying API error", got)
	}
}

// TestGetOrgBindingContextCancelled: degrade-never-abort applies to
// lookup failures, not to a run being torn down. A cancelled context must
// still propagate, and must not be recorded as "this org is not bound".
func TestGetOrgBindingContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := newOrgTaskTest(t, "GET /api/alm_integration/show_bound_organization",
		func(_ http.ResponseWriter, r *http.Request) {
			cancel() // the run is torn down while the lookup is in flight
			select {
			case <-r.Context().Done(): // the client aborted the request
			case <-time.After(5 * time.Second):
			}
		})

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["getOrgBinding"].Run(ctx, e); err == nil {
		t.Fatal("expected the cancellation to propagate, got nil")
	}
	if items, _ := e.Store.ReadAll("getOrgBinding"); len(items) != 0 {
		t.Errorf("a cancelled lookup must not be recorded as unbound: %v", items)
	}
}

// TestGetOrgReposUnexpectedFailureDegrades: listing an org's DevOps
// repositories is the second best-effort lookup feeding the optional
// project binding. It must not abort the run, and it must record why it
// failed so the report does not claim the repository was missing from an
// organization that was never listed (issue #505).
func TestGetOrgReposUnexpectedFailureDegrades(t *testing.T) {
	e := newOrgTaskTest(t, "GET /api/alm_integration/list_repositories",
		respondWith(http.StatusServiceUnavailable, `{"errors":[{"msg":"Service Unavailable"}]}`))

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["getOrgRepos"].Run(context.Background(), e); err != nil {
		t.Fatalf("getOrgRepos must never abort the migration, got: %v", err)
	}
	items, _ := e.Store.ReadAll("getOrgRepos")
	if len(items) != 1 {
		t.Fatalf("expected 1 marker record, got %d: %v", len(items), items)
	}
	if got := extractField(items[0], "repos_lookup_error"); !strings.Contains(got, "503") {
		t.Errorf("repos_lookup_error = %q, want the underlying API error", got)
	}
}

// TestGetOrgReposUnboundOrg: Cloud rejects list_repositories for an org
// with no DevOps binding with HTTP 400 "This organization is not bound to
// an ALM application". That is an answer, not a failed listing, so no
// marker is recorded — getOrgBinding already reports the unbound org.
func TestGetOrgReposUnboundOrg(t *testing.T) {
	e := newOrgTaskTest(t, "GET /api/alm_integration/list_repositories",
		respondWith(http.StatusBadRequest,
			`{"errors":[{"msg":"This organization is not bound to an ALM application"}]}`))

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["getOrgRepos"].Run(context.Background(), e); err != nil {
		t.Fatalf("getOrgRepos: %v", err)
	}
	if items, _ := e.Store.ReadAll("getOrgRepos"); len(items) != 0 {
		t.Errorf("expected no records for an unbound org, got %v", items)
	}
}

// TestMatchProjectReposOrgBindingUnknown is the issue #505 report-honesty
// test: when the org's DevOps binding could not be READ, the project must
// not be told "the org itself is not bound" — something the tool never
// observed — but that the binding could not be read, with the API error
// attached for the report (issue #122 asked for it to be surfaced).
func TestMatchProjectReposOrgBindingUnknown(t *testing.T) {
	e := newFlowTest(t)
	const apiErr = "HTTP 502 GET https://sonarcloud.io/api/alm_integration/" +
		"show_bound_organization?organization=cloud-org1 - Bad gateway"
	seedBindingInputs(t, e, map[string]any{
		"sonarcloud_org_key": testCloudOrg, "bound": false,
		"lookup_error": apiErr,
	})

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["matchProjectRepos"].Run(context.Background(), e); err != nil {
		t.Fatalf("matchProjectRepos: %v", err)
	}
	rec := assertSingleSkip(t, e, BindingSkipOrgBindingUnknown,
		"project binding was not possible because the target organization's "+
			"DevOps platform binding could not be read")
	if got := extractField(rec, "skip_error"); got != apiErr {
		t.Errorf("skip_error = %q, want the API error %q", got, apiErr)
	}

	// setProjectBinding forwards it without attempting the write.
	if err := reg["setProjectBinding"].Run(context.Background(), e); err != nil {
		t.Fatalf("setProjectBinding: %v", err)
	}
	assertSkipForwarded(t, e)
}

// TestMatchProjectReposReposUnknown: the org IS bound, but its repository
// listing failed. Reporting "the repository was not found in the bound
// DevOps organization" would assert something never checked (issue #505).
func TestMatchProjectReposReposUnknown(t *testing.T) {
	e := newFlowTest(t)
	const apiErr = "HTTP 503 GET https://sonarcloud.io/api/alm_integration/" +
		"list_repositories?organization=cloud-org1 - Service Unavailable"
	seedBindingInputsWithRepo(t, e,
		map[string]any{
			"sonarcloud_org_key": testCloudOrg, "bound": true,
			"alm": "github", "dop_organization": "myorg",
			"alm_url": "https://github.com/myorg",
		},
		// The marker runGetOrgRepos writes instead of repositories.
		map[string]any{
			"sonarcloud_org_key": testCloudOrg, "repos_lookup_error": apiErr,
		})

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["matchProjectRepos"].Run(context.Background(), e); err != nil {
		t.Fatalf("matchProjectRepos: %v", err)
	}
	rec := assertSingleSkip(t, e, BindingSkipReposUnknown,
		"project binding was not possible because the repositories of the "+
			"bound DevOps organization could not be listed")
	if got := extractField(rec, "skip_error"); got != apiErr {
		t.Errorf("skip_error = %q, want the API error %q", got, apiErr)
	}
}

// assertSingleSkip asserts matchProjectRepos produced exactly one skip
// record with the given reason and operator-facing detail.
func assertSingleSkip(t *testing.T, e *Executor, reason, detail string) json.RawMessage {
	t.Helper()
	items, _ := e.Store.ReadAll("matchProjectRepos")
	if len(items) != 1 || !extractBool(items[0], "binding_skipped") {
		t.Fatalf("expected exactly 1 skip record, got %v", items)
	}
	if got := extractField(items[0], "skip_reason"); got != reason {
		t.Errorf("skip_reason = %q, want %q", got, reason)
	}
	if got := extractField(items[0], "skip_detail"); got != detail {
		t.Errorf("skip_detail = %q, want %q", got, detail)
	}
	return items[0]
}

func TestSetPortfolioProjects(t *testing.T) {
	e := newFlowTest(t)

	// createPortfolios dependency.
	w, _ := e.Store.Writer("createPortfolios")
	bp, _ := json.Marshal(map[string]any{"cloud_portfolio_id": "portfolio-1", "source_portfolio_key": "pf1", "name": "Test"})
	w.WriteOne(bp)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setPortfolioProjects"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setPortfolioProjects: %v", err)
	}
}

// TestCreateTasksLookupFailure exercises the "already exists + lookup fails" path
// in create tasks, where the create returns 400 and the subsequent GET lookup also fails.
func TestCreateTasksLookupFailure(t *testing.T) {
	lookupFailSrv := newAlreadyExistsButLookupFailsServer()
	defer lookupFailSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(lookupFailSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	lookupFailTasks := []string{
		"createProfiles",
		"createGates",
		"createGroups",
		"createPermissionTemplates",
	}
	for _, taskName := range lookupFailTasks {
		def, ok := reg[taskName]
		if !ok {
			t.Errorf("task %q not found", taskName)
			continue
		}
		err := def.Run(context.Background(), e)
		if err != nil {
			t.Errorf("task %q should warn-and-swallow, got: %v", taskName, err)
		}
	}
}

func TestRunMigrateIntegration(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	setupCSVs(t, dir)

	cfg := MigrateConfig{
		Token:           "test-token",
		EnterpriseKey:   "test-enterprise",
		Edition:         "enterprise",
		URL:             cloudSrv.URL + "/",
		Concurrency:     5,
		ExportDirectory: dir,
		TargetTask:      "createProjects", // Only run one task + deps.
	}

	_, err := RunMigrate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunMigrate: %v", err)
	}
}

// TestTasksWithFailingServer runs all task functions against a server that
// returns 403 for all POST requests. This exercises the error-path logging
// (logAPIWarn, counter.Fail) that the happy-path tests don't reach.
func TestTasksWithFailingServer(t *testing.T) {
	failSrv := newFailingCloudServer()
	defer failSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(failSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	// Write org mapping for buildServerOrgLookup.
	w, _ := e.Store.Writer("generateOrganizationMappings")
	orgData, _ := json.Marshal(map[string]any{
		"server_url": testServerURL, "sonarcloud_org_key": testCloudOrg,
	})
	_ = w.WriteChunk([]json.RawMessage{orgData})

	reg := BuildMigrateRegistry(RegisterAll())

	// These tasks should not return errors — they warn-and-swallow.
	// The failing server exercises the counter.Fail() + logAPIWarn paths.
	errorPathTasks := []string{
		// Associate tasks.
		"setProjectProfiles",
		"setProjectGates",
		"setProjectGroupPermissions",
		"setProjectSettings",
		"setProjectTags",
		// Permission tasks.
		"setOrgGroupPermissions",
		"setProfileGroupPermissions",
		"createMigrationGroups",
		"addMigrationGroupToTemplates",
		// Configure tasks.
		"setProfileParent",
		"restoreProfiles",
		"addGateConditions",
		"setDefaultProfiles",
		"setDefaultGates",
		"setDefaultTemplates",
		// Create tasks (will fail on create, not reach already-exists path).
		"createProjects",
		"createProfiles",
		"createGates",
		"createGroups",
		"createPermissionTemplates",
		// Rule tasks.
		"updateRuleTags",
		"updateRuleDescriptions",
		// Delete tasks.
		"deleteProjects",
		"deleteProfiles",
		"deleteGates",
		"deleteGroups",
		"deleteTemplates",
		"deletePortfolios",
	}

	for _, taskName := range errorPathTasks {
		def, ok := reg[taskName]
		if !ok {
			t.Errorf("task %q not found in registry", taskName)
			continue
		}
		err := def.Run(context.Background(), e)
		if err != nil {
			t.Errorf("task %q should warn-and-swallow, but returned error: %v", taskName, err)
		}
	}
}

// TestMatchProjectReposRepoNotFound covers the case where the target org IS
// bound to the project's DevOps platform but the source repository does not
// exist in the bound DevOps organization — for example migrating a project
// bound to github.com/okorach into an org bound to github.com/other-org.
func TestMatchProjectReposRepoNotFound(t *testing.T) {
	e := newFlowTest(t)
	seedBindingInputsWithRepo(t, e,
		map[string]any{
			"sonarcloud_org_key": testCloudOrg, "bound": true,
			"alm": "github", "dop_organization": "other-org",
			"alm_url": "https://github.com/other-org",
		},
		map[string]any{
			"label": "unrelated", "slug": "other-org/unrelated",
			"installationKey": "other-org/unrelated|99", "sonarcloud_org_key": testCloudOrg,
		})

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["matchProjectRepos"].Run(context.Background(), e); err != nil {
		t.Fatalf("matchProjectRepos: %v", err)
	}
	items, _ := e.Store.ReadAll("matchProjectRepos")
	if len(items) != 1 || !extractBool(items[0], "binding_skipped") {
		t.Fatalf("expected 1 skip record, got %v", items)
	}
	if got := extractField(items[0], "skip_reason"); got != BindingSkipRepoNotFound {
		t.Errorf("skip_reason = %q, want %q", got, BindingSkipRepoNotFound)
	}
	want := "project binding was not possible because the repository was not found in the bound DevOps organization"
	if got := extractField(items[0], "skip_detail"); got != want {
		t.Errorf("skip_detail = %q, want %q", got, want)
	}
}

// TestMatchProjectReposResolvesCloudProjectID covers the issue #122 fix for
// SonarQube Cloud not returning an internal project id from
// /api/projects/search: when the getProjectIds record carries no `id`, the
// binding falls back to /api/navigation/component. Before this fix the
// project id stayed empty and the binding was silently never created.
func TestMatchProjectReposResolvesCloudProjectID(t *testing.T) {
	e := newFlowTest(t)

	writeItem := func(task string, data map[string]any) {
		w, _ := e.Store.Writer(task)
		b, _ := json.Marshal(data)
		w.WriteOne(b)
	}
	// Exactly what Cloud returns: no "id" field.
	writeItem("getProjectIds", map[string]any{
		"key": "cloud-org1_proj1", "name": "Project 1",
		"qualifier": "TRK", "sonarcloud_org_key": testCloudOrg,
	})
	writeItem("getOrgRepos", map[string]any{
		"label": "myrepo", "slug": "myorg/myrepo",
		"installationKey": "myorg/myrepo|123", "sonarcloud_org_key": testCloudOrg,
	})
	writeItem("getOrgBinding", map[string]any{
		"sonarcloud_org_key": testCloudOrg, "bound": true,
		"alm": "github", "dop_organization": "myorg",
		"alm_url": "https://github.com/myorg",
	})

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["matchProjectRepos"].Run(context.Background(), e); err != nil {
		t.Fatalf("matchProjectRepos: %v", err)
	}
	items, _ := e.Store.ReadAll("matchProjectRepos")
	if len(items) != 1 {
		t.Fatalf("expected 1 binding record, got %d: %v", len(items), items)
	}
	if extractBool(items[0], "binding_skipped") {
		t.Fatalf("expected a binding record, got a skip: %s", items[0])
	}
	if got := extractField(items[0], "project_id"); got != "resolved-uuid-1" {
		t.Errorf("project_id = %q, want resolved-uuid-1 (from /api/navigation/component)", got)
	}
	if got := extractField(items[0], "repository_id"); got != "myorg/myrepo" {
		t.Errorf("repository_id = %q, want myorg/myrepo", got)
	}
}

// seedBindingInputsWithRepo is seedBindingInputs with a caller-supplied
// repository fixture.
func seedBindingInputsWithRepo(t *testing.T, e *Executor, orgBinding, repo map[string]any) {
	t.Helper()
	writeItem := func(task string, data map[string]any) {
		w, _ := e.Store.Writer(task)
		b, _ := json.Marshal(data)
		w.WriteOne(b)
	}
	writeItem("getProjectIds", map[string]any{
		"key": "cloud-org1_proj1", "id": "proj-id-1", "sonarcloud_org_key": testCloudOrg,
	})
	writeItem("getOrgRepos", repo)
	writeItem("getOrgBinding", orgBinding)
}
