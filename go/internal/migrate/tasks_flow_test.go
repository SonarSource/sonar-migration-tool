package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

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
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProfileParent"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProfileParent: %v", err)
	}
}

func TestSetDefaultProfiles(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

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
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

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
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setDefaultTemplates"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setDefaultTemplates: %v", err)
	}
}

func TestAddGateConditions(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

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
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

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
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProjectProfiles"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectProfiles: %v", err)
	}
}

func TestSetProjectGates(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProjectGates"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectGates: %v", err)
	}
}

func TestSetProjectGroupPermissions(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProjectGroupPermissions"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectGroupPermissions: %v", err)
	}
}

func TestSetProjectSettings(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProjectSettings"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectSettings: %v", err)
	}
}

func TestSetProjectTags(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProjectTags"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectTags: %v", err)
	}
}

func TestCreateMigrationGroups(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

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
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
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
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["addMigrationUserToMigrationGroups"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("addMigrationUserToMigrationGroups: %v", err)
	}
}

func TestAddMigrationGroupToTemplates(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["addMigrationGroupToTemplates"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("addMigrationGroupToTemplates: %v", err)
	}
}

func TestSetOrgGroupPermissions(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setOrgGroupPermissions"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setOrgGroupPermissions: %v", err)
	}
}

func TestSetProfileGroupPermissions(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["setProfileGroupPermissions"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProfileGroupPermissions: %v", err)
	}
}

func TestUpdateRuleTags(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

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
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

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
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

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
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

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
	repoID := extractField(items[0], "repository_id")
	if repoID != "repo-123" {
		t.Errorf("expected repo-123, got %q", repoID)
	}

	// setProjectBinding.
	err = reg["setProjectBinding"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("setProjectBinding: %v", err)
	}
}

func TestSetPortfolioProjects(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

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
