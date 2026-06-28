// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/cloud"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const (
	testCloudOrg   = "cloud-org1"
	testCustomGate = "Custom Gate"
	testSonarUsers = "sonar-users"
)

// newMockCloudServer creates a mock SonarQube Cloud server for testing.
func newMockCloudServer() *httptest.Server {
	mux := http.NewServeMux()

	// --- POST endpoints (write operations) ---

	mux.HandleFunc("POST /api/projects/create", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		json.NewEncoder(w).Encode(map[string]any{
			"project": map[string]any{
				"key":  r.FormValue("project"),
				"name": r.FormValue("name"),
			},
		})
	})

	mux.HandleFunc("POST /api/projects/delete", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/qualityprofiles/create", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		json.NewEncoder(w).Encode(map[string]any{
			"profile": map[string]any{
				"key":      "cloud-prof-1",
				"name":     r.FormValue("name"),
				"language": r.FormValue("language"),
			},
		})
	})

	mux.HandleFunc("POST /api/qualityprofiles/restore", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profile": map[string]any{
				"key":  "restored-prof-1",
				"name": "Restored",
			},
		})
	})

	mux.HandleFunc("POST /api/qualityprofiles/change_parent", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/qualityprofiles/set_default", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/qualityprofiles/add_project", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/qualityprofiles/add_group", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/qualityprofiles/delete", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				{"key": "p1", "name": "Sonar way", "language": "java", "isBuiltIn": true, "isDefault": false},
				{"key": "p2", "name": "Custom Java", "language": "java", "isBuiltIn": false, "isDefault": true},
			},
		})
	})

	mux.HandleFunc("POST /api/qualitygates/create", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		json.NewEncoder(w).Encode(map[string]any{
			"id":   42,
			"name": r.FormValue("name"),
		})
	})

	mux.HandleFunc("POST /api/qualitygates/create_condition", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		json.NewEncoder(w).Encode(map[string]any{
			"id":     1,
			"metric": r.FormValue("metric"),
			"op":     r.FormValue("op"),
			"error":  r.FormValue("error"),
		})
	})

	mux.HandleFunc("POST /api/qualitygates/destroy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/qualitygates/select", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/qualitygates/set_as_default", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /api/qualitygates/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"default": "Custom Gate",
			"qualitygates": []map[string]any{
				{"id": 1, "name": "Sonar way", "isBuiltIn": true, "isDefault": false},
				{"id": 42, "name": "Custom Gate", "isBuiltIn": false, "isDefault": true},
			},
		})
	})

	mux.HandleFunc("POST /api/user_groups/create", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		json.NewEncoder(w).Encode(map[string]any{
			"group": map[string]any{
				"id":   101,
				"name": r.FormValue("name"),
			},
		})
	})

	mux.HandleFunc("POST /api/user_groups/delete", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/user_groups/add_user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/permissions/create_template", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		json.NewEncoder(w).Encode(map[string]any{
			"permissionTemplate": map[string]any{
				"id":   "tpl-cloud-1",
				"name": r.FormValue("name"),
			},
		})
	})

	mux.HandleFunc("POST /api/permissions/delete_template", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/permissions/set_default_template", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/permissions/add_group", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/permissions/add_group_to_template", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/permissions/add_user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/settings/reset", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /api/settings/values", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"settings": []map[string]any{
				{"key": "sonar.exclusions", "values": []string{"**/*.gen.java"}, "inherited": false},
				{"key": "sonar.coverage.exclusions", "value": "**/*.test.java", "inherited": false},
				{"key": "sonar.inherited.example", "value": "default-value", "inherited": true},
			},
		})
	})

	mux.HandleFunc("POST /api/project_tags/set", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/rules/update", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		json.NewEncoder(w).Encode(map[string]any{
			"rule": map[string]any{
				"key":  r.FormValue("key"),
				"name": "Updated Rule",
			},
		})
	})

	mux.HandleFunc("POST /dop-translation/project-bindings", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// --- GET endpoints (read operations) ---

	mux.HandleFunc("GET /api/projects/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 1},
			"components": []map[string]any{
				{"key": "cloud-org1_proj1", "name": "Project 1", "id": "proj-id-1"},
			},
		})
	})

	mux.HandleFunc("GET /api/users/current", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"login": "migration-user",
			"name":  "Migration User",
		})
	})

	mux.HandleFunc("GET /api/alm_integration/list_repositories", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"repositories": []map[string]any{
				{"id": "repo-123", "slug": "myorg/myrepo", "label": "myrepo"},
			},
		})
	})

	mux.HandleFunc("/api/organizations/search", func(w http.ResponseWriter, r *http.Request) {
		// Echo every requested key back as `<key>-uuid`. configurePortfolios
		// tests pin specific keys and assert the synthetic UUID lands in the
		// PATCH body.
		keys := r.URL.Query().Get("organizations")
		var orgs []map[string]any
		for _, k := range strings.Split(keys, ",") {
			if k == "" {
				continue
			}
			orgs = append(orgs, map[string]any{
				"id":   k + "-uuid",
				"key":  k,
				"name": k,
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"organizations": orgs})
	})

	// Catch-all.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{})
	})

	return httptest.NewServer(mux)
}

// newAlreadyExistsCloudServer creates a mock Cloud server where all POST create
// endpoints return 400 "already exists", and GET search endpoints return
// matching resources for lookup.
func newAlreadyExistsCloudServer() *httptest.Server {
	existsBody := func(name string) string {
		return fmt.Sprintf(`{"errors":[{"msg":"%s already exists"}]}`, name)
	}

	mux := http.NewServeMux()

	// POST create endpoints — all return 400 "already exists".
	mux.HandleFunc("POST /api/projects/create", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, existsBody("Project"))
	})
	mux.HandleFunc("POST /api/qualityprofiles/create", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, existsBody("Quality profile"))
	})
	mux.HandleFunc("POST /api/qualitygates/create", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, existsBody("Quality gate"))
	})
	mux.HandleFunc("POST /api/user_groups/create", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, existsBody("Group"))
	})
	mux.HandleFunc("POST /api/permissions/create_template", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, existsBody("Template"))
	})

	// GET search endpoints — return existing resources for lookup.
	mux.HandleFunc("GET /api/projects/search", func(w http.ResponseWriter, r *http.Request) {
		// Mimic the happy path: the project key SQC reports as
		// "already exists" actually belongs to OUR target org, so
		// createProjects' verify-already-exists step finds it
		// (issue #193). A test that wants to simulate the
		// other-org case wires a different handler.
		projectKey := r.URL.Query().Get("projects")
		json.NewEncoder(w).Encode(map[string]any{
			"components": []map[string]any{
				{"key": projectKey, "name": projectKey},
			},
		})
	})
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				{"key": "existing-prof-key", "name": r.URL.Query().Get("qualityProfile"), "language": r.URL.Query().Get("language")},
			},
		})
	})
	mux.HandleFunc("GET /api/qualitygates/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"qualitygates": []map[string]any{
				{"id": 99, "name": testCustomGate},
			},
		})
	})
	mux.HandleFunc("GET /api/user_groups/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"groups": []map[string]any{
				{"id": 77, "name": r.URL.Query().Get("q")},
			},
		})
	})
	mux.HandleFunc("GET /api/permissions/search_templates", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"permissionTemplates": []map[string]any{
				{"id": "existing-tpl-id", "name": "Default"},
			},
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{})
	})

	return httptest.NewServer(mux)
}

// newMockAPIServer creates a mock enterprise API server.
func newMockAPIServer() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /enterprises/enterprises", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"enterprises": []map[string]any{
				{"id": "ent-1", "key": "test-enterprise", "name": "Test Enterprise"},
			},
		})
	})

	mux.HandleFunc("GET /enterprises/portfolios", func(w http.ResponseWriter, r *http.Request) {
		// Default: no existing portfolios. Tests that need pre-existing
		// portfolios should swap in their own server.
		json.NewEncoder(w).Encode(map[string]any{
			"portfolios": []any{},
			"page":       map[string]any{"pageIndex": 1, "pageSize": 50, "total": 0},
		})
	})

	mux.HandleFunc("POST /enterprises/portfolios", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": "portfolio-1", "key": "pf-1", "name": "Test Portfolio",
		})
	})

	mux.HandleFunc("PATCH /enterprises/portfolios/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("DELETE /enterprises/portfolios/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{})
	})

	return httptest.NewServer(mux)
}

// newTestExecutor builds a fully wired Executor for testing.
func newTestExecutor(cloudSrv, apiSrv *httptest.Server, exportDir string) *Executor {
	runDir := filepath.Join(exportDir, "run-test")
	os.MkdirAll(runDir, 0o755)

	cloudClient := sqapi.NewCloudClient(cloudSrv.URL+"/", "test-token")
	apiClient := sqapi.NewCloudClient(apiSrv.URL+"/", "test-token")

	return &Executor{
		Cloud:     cloud.New(cloudClient),
		CloudAPI:  cloud.New(apiClient),
		Raw:       common.NewRawClient(cloudSrv.Client(), cloudSrv.URL+"/"),
		RawAPI:    common.NewRawClient(apiSrv.Client(), apiSrv.URL+"/"),
		Store:     common.NewDataStore(runDir),
		CloudURL:  cloudSrv.URL + "/",
		APIURL:    apiSrv.URL + "/",
		EntKey:    "test-enterprise",
		Edition:   common.EditionEnterprise,
		ExportDir: exportDir,
		Mapping:   structure.ExtractMapping{testServerURL: "extract-01"},
		Sem:       make(chan struct{}, 5),
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
}

// setupExtractData creates fake extract JSONL data that migrate tasks read.
func setupExtractData(dir string) {
	extractDir := filepath.Join(dir, "extract-01")

	writeJSON(filepath.Join(extractDir, "extract.json"),
		map[string]any{"url": testServerURL, "edition": "enterprise"})

	// Gate conditions. The real extract task (api/qualitygates/show →
	// enrichAll(conditions, ...)) writes one record per condition, each
	// enriched with the parent gateName and serverUrl. Match that shape so
	// runGetGateConditions sees the join key it expects.
	writeJSONL(filepath.Join(extractDir, "getGateConditions"), []map[string]any{
		{"metric": "coverage", "op": "LT", "error": "80", "gateName": "Custom Gate", "serverUrl": testServerURL},
	})

	// Profile backups.
	writeJSONL(filepath.Join(extractDir, "getProfileBackups"), []map[string]any{
		{"profileKey": "prof1", "backup": "<profile><name>Custom</name></profile>"},
	})

	// Project group permissions.
	writeJSONL(filepath.Join(extractDir, "getProjectGroupsPermissions"), []map[string]any{
		{"project": "proj1", "name": testSonarUsers, "permissions": []string{"scan", "user"},
			"serverUrl": testServerURL},
	})

	// Project settings.
	writeJSONL(filepath.Join(extractDir, "getProjectSettings"), []map[string]any{
		{"projectKey": "proj1", "key": "sonar.custom.setting", "value": "custom-value",
			"serverUrl": testServerURL},
	})

	// Project tags.
	writeJSONL(filepath.Join(extractDir, "getProjectTags"), []map[string]any{
		{"projectKey": "proj1", "tags": []string{"java", "backend"},
			"serverUrl": testServerURL},
	})

	// Rule details.
	writeJSONL(filepath.Join(extractDir, "getRuleDetails"), []map[string]any{
		{"key": "java:S100", "tags": []string{"convention"}, "mdNote": "Custom note"},
	})

	// Groups (for org-level permissions).
	writeJSONL(filepath.Join(extractDir, "getGroups"), []map[string]any{
		{"id": "1", "name": testSonarUsers, "permissions": []string{"scan"},
			"serverUrl": testServerURL},
	})

	// Profile groups.
	writeJSONL(filepath.Join(extractDir, "getProfileGroups"), []map[string]any{
		{"profileKey": "prof1", "name": testSonarUsers, "serverUrl": testServerURL},
	})

	// Profile↔project explicit assignments (issue #160 — definitive
	// source of project profile bindings, replacing the stale
	// navigation/component qualityProfiles array).
	writeJSONL(filepath.Join(extractDir, "getProfileProjects"), []map[string]any{
		{"key": "proj1", "name": "Project 1", "selected": true,
			"profileKey": "prof1", "profileName": "Custom", "language": "java",
			"serverUrl": testServerURL},
	})

	// Portfolio projects (empty for non-enterprise tests).
	writeJSONL(filepath.Join(extractDir, "getPortfolioProjects"), nil)
}

// newFailingCloudServer creates a mock server that returns 403 for all POST requests.
func newFailingCloudServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"errors":[{"msg":"Insufficient privileges"}]}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{})
	})
	return httptest.NewServer(mux)
}

// newAlreadyExistsButLookupFailsServer creates a mock Cloud server where POST
// create endpoints return 400 "already exists" AND GET search/lookup endpoints
// also return errors. This exercises the "create failed + lookup failed" paths.
func newAlreadyExistsButLookupFailsServer() *httptest.Server {
	existsBody := `{"errors":[{"msg":"already exists"}]}`
	mux := http.NewServeMux()

	// POST create endpoints — return 400 "already exists".
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, existsBody)
	})

	// GET lookup endpoints — return 500 to simulate lookup failure.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Internal error"}]}`, 500)
	})

	return httptest.NewServer(mux)
}

func writeJSON(path string, data any) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	b, _ := json.Marshal(data)
	os.WriteFile(path, b, 0o644)
}

// addDefaultCloudHandler installs a catch-all GET handler that responds with an
// empty JSON object. Mount this on a cloudMux after all specific handlers so
// that any unexpected endpoint gets a safe no-op response instead of a 404.
func addDefaultCloudHandler(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
}

// newCustomCloudTest wires a test environment with a caller-supplied cloudMux,
// a fresh mock API server, a temp export dir, and a new Executor. It does NOT
// call setupExtractData — use this for tasks whose extract data is written
// inline by the test. Servers are closed via t.Cleanup.
func newCustomCloudTest(t *testing.T, cloudMux *http.ServeMux) *Executor {
	t.Helper()
	cloudSrv := httptest.NewServer(cloudMux)
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	dir := t.TempDir()
	return newTestExecutor(cloudSrv, apiSrv, dir)
}

// newAlreadyExistsTest is like newCreateTest but uses newAlreadyExistsCloudServer
// instead of newMockCloudServer — covering the already-exists idempotency paths.
func newAlreadyExistsTest(t *testing.T) (*Executor, string) {
	t.Helper()
	cloudSrv := newAlreadyExistsCloudServer()
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	dir := t.TempDir()
	setupExtractData(dir)
	return newTestExecutor(cloudSrv, apiSrv, dir), dir
}

// writeGlobalSettingsOrg writes the shared boilerplate needed by custom
// setGlobalSettings tests: the extract.json metadata for extract-01 and a
// single generateOrganizationMappings row for org1.
func writeGlobalSettingsOrg(t *testing.T, e *Executor) {
	t.Helper()
	writeExtractMetaJSON(t, e.ExportDir, "extract-01", testServerURL)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"sonarcloud_org_key": "org1"},
	})
}

// newDeleteTest wires a test environment for delete/reset tasks: mock servers,
// extract data, an Executor, and a pre-seeded generateOrganizationMappings row
// for testCloudOrg. Servers are closed via t.Cleanup.
func newDeleteTest(t *testing.T, mux *http.ServeMux) *Executor {
	t.Helper()
	cloudSrv := httptest.NewServer(mux)
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"sonarqube_org_key": "org1", "sonarcloud_org_key": testCloudOrg},
	})
	return e
}

func writeJSONL(taskDir string, items []map[string]any) {
	os.MkdirAll(taskDir, 0o755)
	f, _ := os.Create(filepath.Join(taskDir, "results.1.jsonl"))
	defer f.Close()
	for _, item := range items {
		b, _ := json.Marshal(item)
		fmt.Fprintln(f, string(b))
	}
}
