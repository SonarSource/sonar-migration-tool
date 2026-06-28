// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// newCreateTest creates a complete test environment for create-task tests:
// mock servers, extract data, and a fresh Executor. The servers are closed
// via t.Cleanup. Returns the executor and its export directory.
func newCreateTest(t *testing.T) (*Executor, string) {
	t.Helper()
	cloudSrv := newMockCloudServer()
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	dir := t.TempDir()
	setupExtractData(dir)
	return newTestExecutor(cloudSrv, apiSrv, dir), dir
}

func TestCreateProjects(t *testing.T) {
	e, dir := newCreateTest(t)

	// Run setup task first.
	setupCSVs(t, dir)
	runTask(t, e, "generateProjectMappings")

	// Run createProjects.
	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createProjects"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createProjects: %v", err)
	}

	items, _ := e.Store.ReadAll("createProjects")
	if len(items) == 0 {
		t.Fatal("expected createProjects output")
	}
	// Verify cloud_project_key was set.
	key := extractField(items[0], "cloud_project_key")
	if key == "" {
		t.Error("expected cloud_project_key to be set")
	}
}

func TestCreateProfiles(t *testing.T) {
	e, dir := newCreateTest(t)

	setupCSVs(t, dir)
	runTask(t, e, "generateProfileMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createProfiles"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createProfiles: %v", err)
	}

	items, _ := e.Store.ReadAll("createProfiles")
	if len(items) == 0 {
		t.Fatal("expected createProfiles output")
	}
	profKey := extractField(items[0], "cloud_profile_key")
	if profKey == "" {
		t.Error("expected cloud_profile_key")
	}
}

func TestCreateGates(t *testing.T) {
	e, dir := newCreateTest(t)

	setupCSVs(t, dir)
	runTask(t, e, "generateGateMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createGates"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createGates: %v", err)
	}

	items, _ := e.Store.ReadAll("createGates")
	if len(items) == 0 {
		t.Fatal("expected createGates output")
	}
	gateID := extractField(items[0], "cloud_gate_id")
	if gateID == "" {
		t.Error("expected cloud_gate_id")
	}
}

func TestCreateGroups(t *testing.T) {
	e, dir := newCreateTest(t)

	setupCSVs(t, dir)
	runTask(t, e, "generateGroupMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createGroups"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createGroups: %v", err)
	}

	items, _ := e.Store.ReadAll("createGroups")
	if len(items) == 0 {
		t.Fatal("expected createGroups output")
	}
}

func TestCreatePermissionTemplates(t *testing.T) {
	e, dir := newCreateTest(t)

	setupCSVs(t, dir)
	runTask(t, e, "generateTemplateMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createPermissionTemplates"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createPermissionTemplates: %v", err)
	}

	items, _ := e.Store.ReadAll("createPermissionTemplates")
	if len(items) == 0 {
		t.Fatal("expected createPermissionTemplates output")
	}
	tplID := extractField(items[0], "cloud_template_id")
	if tplID == "" {
		t.Error("expected cloud_template_id")
	}
}

func TestCreatePortfolios(t *testing.T) {
	e, dir := newCreateTest(t)

	setupCSVs(t, dir)
	runTask(t, e, "generatePortfolioMappings")

	// Write fake getEnterprises output.
	w, _ := e.Store.Writer("getEnterprises")
	w.WriteOne(json.RawMessage(`[{"id":"ent-1","key":"test-enterprise"}]`))

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createPortfolios"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createPortfolios: %v", err)
	}
	// portfolios CSV is empty, so no output expected.
}

// TestCreatePortfoliosReusesExisting verifies that when a portfolio with the
// same name already exists in the enterprise (the situation on `reset` or a
// resumed run), runCreatePortfolios reuses its cloud ID via ListPortfolios
// instead of trying to POST and failing. This is the linchpin of issue #175:
// without this, deletePortfolios sees an empty input JSONL and is a no-op.
func TestCreatePortfoliosReusesExisting(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()

	// Stand up a custom API server that:
	// - has one pre-existing portfolio "PreExistingPortfolio" with id "p-existing"
	// - fails any POST attempt (proves we did NOT call CreatePortfolio)
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /enterprises/enterprises", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"enterprises": []map[string]any{{"id": "ent-1", "key": "test-enterprise"}},
		})
	})
	apiMux.HandleFunc("GET /enterprises/portfolios", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"portfolios": []map[string]any{
				{"id": "p-existing", "name": "PreExistingPortfolio"},
			},
			"page": map[string]any{"pageIndex": 1, "pageSize": 50, "total": 1},
		})
	})
	postCalled := false
	apiMux.HandleFunc("POST /enterprises/portfolios", func(w http.ResponseWriter, _ *http.Request) {
		postCalled = true
		http.Error(w, `{"message":"already exists"}`, http.StatusBadRequest)
	})
	apiSrv := httptest.NewServer(apiMux)
	defer apiSrv.Close()

	dir := t.TempDir()
	setupExtractData(dir)
	// Inject one resolved project for src-1 so the portfolio is not
	// considered "empty" and skipped by the new empty-portfolio guard
	// in createPortfolios. The serverURL has to match the test
	// executor's extract Mapping (testServerURL) so readExtractItems
	// can find this record.
	writeJSONL(filepath.Join(dir, "extract-01", "getPortfolioProjects"), []map[string]any{
		{"portfolioKey": "src-1", "refKey": "proj-a", "serverUrl": testServerURL},
	})
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	// Write a portfolios.csv with the same name as the pre-existing portfolio.
	csvPath := filepath.Join(dir, "portfolios.csv")
	csv := "source_portfolio_key,name,server_url,description\n" +
		"src-1,PreExistingPortfolio," + testServerURL + ",Reused\n"
	if err := os.WriteFile(csvPath, []byte(csv), 0o644); err != nil {
		t.Fatalf("write portfolios.csv: %v", err)
	}
	// Write a stub organizations.csv (required by loadCSVToJSONL's join).
	orgCSV := "sonarqube_org_key,sonarcloud_org_key,binding_key,server_url,alm,url,is_cloud,project_count\n"
	if err := os.WriteFile(filepath.Join(dir, "organizations.csv"), []byte(orgCSV), 0o644); err != nil {
		t.Fatalf("write organizations.csv: %v", err)
	}

	runTask(t, e, "generatePortfolioMappings")
	w, _ := e.Store.Writer("getEnterprises")
	w.WriteOne(json.RawMessage(`[{"id":"ent-1","key":"test-enterprise"}]`))

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["createPortfolios"].Run(context.Background(), e); err != nil {
		t.Fatalf("createPortfolios: %v", err)
	}

	if postCalled {
		t.Error("POST /enterprises/portfolios should NOT have been called when portfolio already exists")
	}

	items, _ := e.Store.ReadAll("createPortfolios")
	if len(items) != 1 {
		t.Fatalf("expected 1 createPortfolios item, got %d", len(items))
	}
	if id := extractField(items[0], "cloud_portfolio_id"); id != "p-existing" {
		t.Errorf("expected cloud_portfolio_id 'p-existing', got %q", id)
	}
}

func TestGetMigrationUser(t *testing.T) {
	e, dir := newCreateTest(t)

	setupCSVs(t, dir)
	runTask(t, e, "generateOrganizationMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["getMigrationUser"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("getMigrationUser: %v", err)
	}

	items, _ := e.Store.ReadAll("getMigrationUser")
	if len(items) == 0 {
		t.Fatal("expected getMigrationUser output")
	}
	login := extractField(items[0], "login")
	if login != "migration-user" {
		t.Errorf("expected login=migration-user, got %q", login)
	}
}

func TestGetEnterprises(t *testing.T) {
	e, dir := newCreateTest(t)

	setupCSVs(t, dir)
	runTask(t, e, "generateOrganizationMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["getEnterprises"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("getEnterprises: %v", err)
	}

	items, _ := e.Store.ReadAll("getEnterprises")
	if len(items) == 0 {
		t.Fatal("expected getEnterprises output")
	}
}

func TestGetProjectIds(t *testing.T) {
	e, _ := newCreateTest(t)

	// Write createProjects dependency.
	w, _ := e.Store.Writer("createProjects")
	w.WriteOne(json.RawMessage(`{"cloud_project_key":"cloud-org1_proj1","sonarcloud_org_key":"cloud-org1"}`))

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["getProjectIds"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("getProjectIds: %v", err)
	}

	items, _ := e.Store.ReadAll("getProjectIds")
	if len(items) == 0 {
		t.Fatal("expected getProjectIds output")
	}
}

func TestGetOrgRepos(t *testing.T) {
	e, dir := newCreateTest(t)

	setupCSVs(t, dir)
	runTask(t, e, "generateOrganizationMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["getOrgRepos"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("getOrgRepos: %v", err)
	}

	items, _ := e.Store.ReadAll("getOrgRepos")
	if len(items) == 0 {
		t.Fatal("expected getOrgRepos output")
	}
}

// --- Already-exists idempotency tests ---

func TestCreateProjects_AlreadyExists(t *testing.T) {
	cloudSrv := newAlreadyExistsCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	setupCSVs(t, dir)
	runTask(t, e, "generateProjectMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createProjects"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createProjects: %v", err)
	}

	items, _ := e.Store.ReadAll("createProjects")
	if len(items) == 0 {
		t.Fatal("expected createProjects output on re-run")
	}
	key := extractField(items[0], "cloud_project_key")
	if key == "" {
		t.Error("expected cloud_project_key to be set from derived key")
	}
	if key != testCloudOrg+"_proj1" {
		t.Errorf("expected derived key %q, got %q", testCloudOrg+"_proj1", key)
	}
}

// SonarQube Cloud project keys are GLOBALLY unique, so an
// "already exists" 400 from /api/projects/create doesn't guarantee
// the existing project is in our target org — it might be claimed
// by a different org. If we naively recorded a createProjects entry
// for it, downstream tasks (setProjectSettings, setGlobalSettings'
// fan-out) would PATCH a phantom project and get 404s. This test
// asserts createProjects now verifies the project's actual org via
// /api/projects/search and refuses to record an entry when the key
// belongs elsewhere. Issue #193.
func TestCreateProjects_AlreadyExistsInDifferentOrg(t *testing.T) {
	// Build a custom Cloud mock that returns "already exists" for
	// create AND returns an EMPTY project search result, mimicking
	// SQC saying "the key exists, but not in this org".
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/create", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"errors":[{"msg":"Could not create Project, key already exists: x"}]}`)
	})
	mux.HandleFunc("GET /api/projects/search", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"components": []map[string]any{}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()

	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	setupCSVs(t, dir)
	runTask(t, e, "generateProjectMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["createProjects"].Run(context.Background(), e); err != nil {
		t.Fatalf("createProjects: %v", err)
	}

	items, _ := e.Store.ReadAll("createProjects")
	if len(items) != 0 {
		t.Fatalf("expected NO createProjects records when key exists in another org, got %d: %s",
			len(items), items)
	}
}

func TestCreateProfiles_AlreadyExists(t *testing.T) {
	cloudSrv := newAlreadyExistsCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	setupCSVs(t, dir)
	runTask(t, e, "generateProfileMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createProfiles"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createProfiles: %v", err)
	}

	items, _ := e.Store.ReadAll("createProfiles")
	if len(items) == 0 {
		t.Fatal("expected createProfiles output on re-run")
	}
	profKey := extractField(items[0], "cloud_profile_key")
	if profKey != "existing-prof-key" {
		t.Errorf("expected existing-prof-key, got %q", profKey)
	}
}

func TestCreateGates_AlreadyExists(t *testing.T) {
	cloudSrv := newAlreadyExistsCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	setupCSVs(t, dir)
	runTask(t, e, "generateGateMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createGates"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createGates: %v", err)
	}

	items, _ := e.Store.ReadAll("createGates")
	if len(items) == 0 {
		t.Fatal("expected createGates output on re-run")
	}
	gateID := extractField(items[0], "cloud_gate_id")
	if gateID != "99" {
		t.Errorf("expected gate ID 99, got %q", gateID)
	}
}

func TestCreateGroups_AlreadyExists(t *testing.T) {
	cloudSrv := newAlreadyExistsCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	setupCSVs(t, dir)
	runTask(t, e, "generateGroupMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createGroups"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createGroups: %v", err)
	}

	items, _ := e.Store.ReadAll("createGroups")
	if len(items) == 0 {
		t.Fatal("expected createGroups output on re-run")
	}
	groupID := extractField(items[0], "cloud_group_id")
	if groupID != "77" {
		t.Errorf("expected group ID 77, got %q", groupID)
	}
}

func TestCreatePermissionTemplates_AlreadyExists(t *testing.T) {
	cloudSrv := newAlreadyExistsCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	setupCSVs(t, dir)
	runTask(t, e, "generateTemplateMappings")

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["createPermissionTemplates"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("createPermissionTemplates: %v", err)
	}

	items, _ := e.Store.ReadAll("createPermissionTemplates")
	if len(items) == 0 {
		t.Fatal("expected createPermissionTemplates output on re-run")
	}
	tplID := extractField(items[0], "cloud_template_id")
	if tplID != "existing-tpl-id" {
		t.Errorf("expected existing-tpl-id, got %q", tplID)
	}
}

// --- Helpers ---

// setupCSVs creates test CSV files in the export directory.
// Note: sonarcloud_org_key is included because in real usage the user fills
// this in organizations.csv, and generateProjectMappings joins the data.
// For tests we include it directly in all CSVs so tasks can find it.
func setupCSVs(t *testing.T, dir string) {
	t.Helper()
	orgs := []map[string]any{
		{"sonarqube_org_key": "org1", "sonarcloud_org_key": testCloudOrg,
			"server_url": testServerURL, "alm": "github",
			"url": "https://api.github.com", "is_cloud": true, "project_count": 1},
	}
	writeCSVFromMaps(t, dir, "organizations", orgs)

	projects := []map[string]any{
		{"key": "proj1", "name": "Project 1", "gate_name": testCustomGate,
			"server_url": testServerURL, "sonarqube_org_key": "org1",
			"sonarcloud_org_key": testCloudOrg, "main_branch": "main",
			"is_cloud_binding": true, "new_code_definition_type": "days",
			"new_code_definition_value": 30, "alm": "github",
			"repository": "myorg/myrepo", "slug": "", "monorepo": false,
			"summary_comment_enabled": false,
			"profiles": []map[string]any{{"key": "prof1", "name": "Custom", "language": "java"}}},
	}
	writeCSVFromMaps(t, dir, "projects", projects)

	profiles := []map[string]any{
		{"unique_key": "org1prof1", "name": "Custom", "language": "java",
			"parent_name": "Sonar way", "server_url": testServerURL,
			"source_profile_key": "prof1", "sonarqube_org_key": "org1",
			"sonarcloud_org_key": testCloudOrg, "is_default": true},
	}
	writeCSVFromMaps(t, dir, "profiles", profiles)

	gates := []map[string]any{
		{"name": testCustomGate, "server_url": testServerURL,
			"source_gate_key": testCustomGate, "is_default": true,
			"sonarqube_org_key": "org1", "sonarcloud_org_key": testCloudOrg},
	}
	writeCSVFromMaps(t, dir, "gates", gates)

	groups := []map[string]any{
		// "sonar-users" is the SonarQube Server built-in group;
		// runCreateGroups now skips it (it has no SQC counterpart),
		// so keep at least one non-built-in group below for tests
		// that assert createGroups produces output.
		{"name": "sonar-users", "server_url": testServerURL,
			"sonarqube_org_key": "org1", "sonarcloud_org_key": testCloudOrg,
			"description": "Default group"},
		{"name": "developers", "server_url": testServerURL,
			"sonarqube_org_key": "org1", "sonarcloud_org_key": testCloudOrg,
			"description": "Project developers"},
	}
	writeCSVFromMaps(t, dir, "groups", groups)

	templates := []map[string]any{
		{"unique_key": "org1tpl1", "source_template_key": "tpl1", "name": "Default",
			"server_url": testServerURL, "is_default": true,
			"sonarqube_org_key": "org1", "sonarcloud_org_key": testCloudOrg,
			"description": "", "project_key_pattern": ""},
	}
	writeCSVFromMaps(t, dir, "templates", templates)

	writeCSVFromMaps(t, dir, "portfolios", nil)
}

// writeCSVFromMaps writes a CSV file from a slice of maps.
func writeCSVFromMaps(t *testing.T, dir, name string, rows []map[string]any) {
	t.Helper()
	if len(rows) == 0 {
		os.WriteFile(filepath.Join(dir, name+".csv"), nil, 0o644)
		return
	}
	// Extract headers from first row.
	var headers []string
	for k := range rows[0] {
		headers = append(headers, k)
	}
	sort.Strings(headers)

	f, _ := os.Create(filepath.Join(dir, name+".csv"))
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write(headers)
	for _, row := range rows {
		record := make([]string, len(headers))
		for i, h := range headers {
			v := row[h]
			switch val := v.(type) {
			case string:
				record[i] = val
			case bool:
				b, _ := json.Marshal(val)
				record[i] = string(b)
			case int:
				record[i] = fmt.Sprintf("%d", val)
			case float64:
				record[i] = fmt.Sprintf("%g", val)
			default:
				if v == nil {
					record[i] = ""
				} else {
					b, _ := json.Marshal(val)
					record[i] = string(b)
				}
			}
		}
		w.Write(record)
	}
	w.Flush()
}

// runTask runs a setup task by name.
func runTask(t *testing.T, e *Executor, name string) {
	t.Helper()
	reg := BuildMigrateRegistry(RegisterAll())
	def, ok := reg[name]
	if !ok {
		t.Fatalf("task %q not found", name)
	}
	if err := def.Run(context.Background(), e); err != nil {
		t.Fatalf("task %s: %v", name, err)
	}
}
