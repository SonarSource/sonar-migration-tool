// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	// Default to a 2025.x version so version-gated tasks (e.g.
	// getAiCodeFixConfig, which requires the v2 fix-suggestions
	// endpoint introduced in 2025.3 — issue #372) exercise their
	// happy path. Tests covering older-server behaviour spin up
	// their own mock with an explicit version.
	testServerVersion = "2025.4.0.12345"
	testToken         = "test-token"
	testResultsFile   = "results.1.jsonl"
)

// newMockServer creates a comprehensive mock SonarQube server for testing.
// It handles all common endpoints with reasonable defaults.
func newMockServer() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/server/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testServerVersion)
	})

	mux.HandleFunc("GET /api/system/info", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"edition": "developer", "version": testServerVersion,
			"serverUrl": "https://test.example.com/",
		})
	})

	mux.HandleFunc("GET /api/projects/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 2},
			"components": []map[string]any{
				{"key": "proj1", "name": "Project 1", "qualifier": "TRK"},
				{"key": "proj2", "name": "Project 2", "qualifier": "TRK"},
			},
		})
	})

	mux.HandleFunc("GET /api/users/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 1},
			"users":  []map[string]any{{"login": "admin", "name": "Admin"}},
		})
	})

	mux.HandleFunc("GET /api/permissions/groups", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 2},
			"groups": []map[string]any{
				{"id": "1", "name": "sonar-users"},
				{"id": "2", "name": "Anyone"},
			},
		})
	})

	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				{"key": "prof1", "name": "Sonar way", "language": "java", "isBuiltIn": true},
				{"key": "prof2", "name": "Custom", "language": "java", "isBuiltIn": false, "parentKey": "prof1"},
			},
		})
	})

	mux.HandleFunc("GET /api/qualitygates/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"qualitygates": []map[string]any{
				{"name": "Sonar way", "isBuiltIn": true},
				{"name": "Custom Gate", "isBuiltIn": false},
			},
		})
	})

	mux.HandleFunc("GET /api/qualitygates/show", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"name":       r.URL.Query().Get("name"),
			"conditions": []map[string]any{{"metric": "coverage", "op": "LT", "error": "80"}},
		})
	})

	mux.HandleFunc("GET /api/qualitygates/search_groups", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 0},
			"groups": []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/qualitygates/search_users", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 0},
			"users":  []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/permissions/search_templates", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"permissionTemplates": []map[string]any{{"id": "tpl1", "name": "Default"}},
			"defaultTemplates":    []map[string]any{{"templateId": "tpl1", "qualifier": "TRK"}},
		})
	})

	mux.HandleFunc("GET /api/permissions/template_groups", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 1},
			"groups": []map[string]any{{"name": "sonar-users"}},
		})
	})

	mux.HandleFunc("GET /api/permissions/template_users", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 0},
			"users":  []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/rules/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("f") == "actives" {
			json.NewEncoder(w).Encode(map[string]any{
				"paging":  map[string]any{"total": 0},
				"actives": []map[string]any{},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 1},
			"rules":  []map[string]any{{"key": "java:S100", "repo": "java", "name": "Test Rule"}},
		})
	})

	mux.HandleFunc("GET /api/rules/show", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"rule": map[string]any{"key": r.URL.Query().Get("key"), "name": "Rule Detail"},
		})
	})

	mux.HandleFunc("GET /api/rules/repositories", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"repositories": []map[string]any{{"key": "java", "name": "Java"}},
		})
	})

	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"total": 5, "issues": []map[string]any{},
			"paging": map[string]any{"total": 5},
		})
	})

	mux.HandleFunc("GET /api/hotspots/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 0}, "hotspots": []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/navigation/component", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"key": r.URL.Query().Get("component"), "name": "Component",
		})
	})

	mux.HandleFunc("GET /api/settings/values", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"settings": []map[string]any{
				{"key": "sonar.core.id", "value": "xxx", "inherited": true},
				{"key": "sonar.custom", "value": "custom", "inherited": false},
			},
		})
	})

	mux.HandleFunc("GET /api/settings/list_definitions", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"definitions": []map[string]any{
				{"key": "sonar.custom", "type": "STRING", "multiValues": false, "defaultValue": ""},
				{"key": "sonar.core.id", "type": "STRING", "multiValues": false, "defaultValue": "xxx"},
			},
		})
	})

	mux.HandleFunc("GET /api/plugins/installed", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"plugins": []map[string]any{{"key": "findbugs", "name": "FindBugs"}},
		})
	})

	mux.HandleFunc("GET /api/projects/license_usage", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"projects": []map[string]any{{"key": "proj1", "loc": 1000}},
		})
	})

	mux.HandleFunc("GET /api/alm_settings/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"almSettings": []map[string]any{{"key": "gh1", "alm": "github"}},
		})
	})

	mux.HandleFunc("GET /api/alm_settings/get_binding", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"key": "gh1", "repository": "org/repo",
		})
	})

	mux.HandleFunc("GET /api/v2/fix-suggestions/feature-enablements", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"enablement":         "DISABLED",
			"enabledProjectKeys": []string{},
			"providers": []map[string]any{
				{"type": "OPENAI", "name": "OpenAI", "selfHosted": false, "selected": false, "model": nil},
			},
		})
	})

	mux.HandleFunc("GET /api/project_branches/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"branches": []map[string]any{{"name": "main", "isMain": true}},
		})
	})

	mux.HandleFunc("GET /api/project_pull_requests/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"pullRequests": []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/project_links/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"links": []map[string]any{{"type": "homepage", "url": "https://example.com"}},
		})
	})

	mux.HandleFunc("GET /api/measures/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"measures": []map[string]any{{"metric": "ncloc", "value": "1234"}},
		})
	})

	mux.HandleFunc("GET /api/webhooks/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"webhooks": []map[string]any{{"key": "wh1", "name": "CI", "url": "https://ci.example.com"}},
		})
	})

	mux.HandleFunc("GET /api/webhooks/deliveries", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging":     map[string]any{"total": 0},
			"deliveries": []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/permissions/users", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 0},
			"users":  []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/user_groups/users", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 1},
			"users":  []map[string]any{{"login": "admin"}},
		})
	})

	mux.HandleFunc("GET /api/user_tokens/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"userTokens": []map[string]any{{"name": "ci-token"}},
		})
	})

	mux.HandleFunc("GET /api/qualityprofiles/backup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, "<profile><name>Sonar way</name></profile>")
	})

	mux.HandleFunc("GET /api/qualityprofiles/search_groups", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 0},
			"groups": []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/qualityprofiles/search_users", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 0},
			"users":  []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/project_analyses/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging":   map[string]any{"total": 0},
			"analyses": []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/ce/activity", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 0},
			"tasks":  []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/new_code_periods/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"newCodePeriods": []map[string]any{},
		})
	})

	mux.HandleFunc("GET /api/new_code_periods/show", func(w http.ResponseWriter, r *http.Request) {
		// global form (no project, no branch) — used by issue #136
		// extract to surface the SQS platform-wide NCD default.
		json.NewEncoder(w).Encode(map[string]any{
			"type": "PREVIOUS_VERSION",
		})
	})

	mux.HandleFunc("GET /api/components/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging":     map[string]any{"total": 0},
			"components": []map[string]any{},
		})
	})

	// Catch-all for any unhandled endpoint.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{})
	})

	return httptest.NewServer(mux)
}

// firstExtractDir returns the path of the first subdirectory created under dir
// by a RunExtract call, fataling if none exists. This is the extract run dir.
func firstExtractDir(t *testing.T, dir string) string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Fatal("expected extract run directory under export dir")
	}
	return filepath.Join(dir, entries[0].Name())
}

// TestRunExtractIntegration runs a small extract with a mock SonarQube server.
func TestRunExtractIntegration(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	dir := t.TempDir()
	cfg := ExtractConfig{
		URL: srv.URL, Token: testToken, ExportDirectory: dir,
		TargetTask: "getProjectIssues", Concurrency: 5,
	}

	_, err := RunExtract(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunExtract failed: %v", err)
	}

	extractDir := firstExtractDir(t, dir)

	// Verify extract.json metadata.
	metaData, err := os.ReadFile(filepath.Join(extractDir, "extract.json"))
	if err != nil {
		t.Fatalf("reading extract.json: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("parsing extract.json: %v", err)
	}
	if meta["edition"] != "developer" {
		t.Errorf("expected edition=developer, got %v", meta["edition"])
	}

	// Verify both getProjects and getProjectIssues output exist.
	for _, task := range []string{"getProjects", "getProjectIssues"} {
		if _, err := os.Stat(filepath.Join(extractDir, task)); os.IsNotExist(err) {
			t.Errorf("expected %s output directory", task)
		}
	}
}

// TestRunExtractFull exercises the full task registry with a comprehensive mock.
func TestRunExtractFull(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	dir := t.TempDir()
	cfg := ExtractConfig{
		URL: srv.URL, Token: testToken, ExportDirectory: dir,
		Concurrency: 10, // Full extract, all tasks
	}

	_, err := RunExtract(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunExtract (full) failed: %v", err)
	}

	extractDir := firstExtractDir(t, dir)

	// Verify key task output directories exist.
	expectedTasks := []string{
		"getProjects", "getUsers", "getGroups", "getProfiles", "getGates",
		"getTemplates", "getDefaultTemplates", "getRules", "getRepos",
		"getPlugins", "getServerInfo", "getServerSettings", "getServerSettingsDefinitions", "getUsage",
		"getBindings", "getAiCodeFixConfig", "getWebhooks", "getServerWebhooks", "getUserPermissions",
		"getProfileRules", "getTasks",
		// Per-project tasks
		"getProjectDetails", "getProjectSettings", "getProjectLinks",
		"getProjectMeasures", "getProjectWebhooks", "getProjectBindings",
		"getProjectGroupsPermissions", "getProjectUsersScanners", "getProjectUsersViewers",
		"getBranches", "getProjectPullRequests",
		"getProjectIssues", "getAcceptedIssues", "getSafeHotspots",
		"getProjectIssueTypes", "getProjectFixedIssueTypes", "getProjectRecentIssueTypes",
		"getNewCodePeriods", "getGlobalNewCodePeriod", "getProjectAnalyses", "getProjectTasks",
		// Per-profile tasks
		"getProfileBackups", "getActiveProfileRules",
		// getDeactivatedProfileRules depends on parentKey being non-null — "Custom" profile has it
		"getDeactivatedProfileRules",
		// Per-gate tasks (Custom Gate is not built-in)
		"getGateConditions", "getGateGroups", "getGateUsers",
		// Per-template tasks
		"getTemplateGroupsScanners", "getTemplateGroupsViewers",
		"getTemplateUsersScanners", "getTemplateUsersViewers",
		// Per-user tasks
		"getUserGroups", "getUserTokens",
		// Per-rule tasks
		"getRuleDetails",
		// getPluginRules — java repo IS in standardRepos, so no plugin rules output unless there are non-standard ones
		// Per-webhook tasks
		"getWebhookDeliveries",
		"getProjectWebhookDeliveries",
		// Profile groups/users (non-built-in "Custom" profile)
		"getProfileGroups", "getProfileUsers",
	}

	for _, task := range expectedTasks {
		if _, err := os.Stat(filepath.Join(extractDir, task)); os.IsNotExist(err) {
			t.Errorf("missing expected task output: %s", task)
		}
	}

	// Verify getProjectSettings filters inherited settings.
	settingsData, err := os.ReadFile(filepath.Join(extractDir, "getProjectSettings", testResultsFile))
	if err == nil && len(settingsData) > 0 {
		// Should only contain non-inherited settings.
		lines := 0
		for _, b := range settingsData {
			if b == '\n' {
				lines++
			}
		}
		// We return 2 settings, 1 inherited + 1 not. Per 2 projects = 2 non-inherited lines.
		if lines != 2 {
			t.Logf("getProjectSettings: expected 2 non-inherited settings, got %d lines", lines)
		}
	}
}

// TestRunExtractResumability verifies that completed tasks are skipped.
func TestRunExtractResumability(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	dir := t.TempDir()
	extractID := "04-16-2026-01"

	// Pre-create getProjects output to simulate a completed task.
	projectsDir := filepath.Join(dir, extractID, "getProjects")
	_ = os.MkdirAll(projectsDir, 0o755)
	_ = os.WriteFile(
		filepath.Join(projectsDir, testResultsFile),
		[]byte(`{"key":"proj1","name":"P1"}`+"\n"),
		0o644,
	)

	cfg := ExtractConfig{
		URL: srv.URL, Token: testToken, ExportDirectory: dir,
		ExtractID: extractID, TargetTask: "getProjects", Concurrency: 5,
	}

	_, err := RunExtract(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunExtract failed: %v", err)
	}

	content, _ := os.ReadFile(filepath.Join(projectsDir, testResultsFile))
	if string(content) != `{"key":"proj1","name":"P1"}`+"\n" {
		t.Errorf("expected original file preserved, got %q", string(content))
	}
}

// TestRunExtractEditionFiltering verifies enterprise tasks are excluded for community.
func TestRunExtractEditionFiltering(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/server/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testServerVersion)
	})
	mux.HandleFunc("GET /api/system/info", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"edition": "community"})
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 0},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	cfg := ExtractConfig{
		URL: srv.URL, Token: testToken, ExportDirectory: dir,
		TargetTask: "getPortfolios", Concurrency: 5,
	}

	// getPortfolios requires enterprise — should fail to resolve deps.
	_, err := RunExtract(context.Background(), cfg)
	if err == nil {
		t.Error("expected error for enterprise-only task on community edition")
	}
}

// TestRunExtractAiCodeFixSkippedOnOldSQS verifies issue #372: SQS 2025.1
// returns HTTP 405 for GET api/v2/fix-suggestions/feature-enablements
// because the endpoint was only introduced in 2025.3. The task should
// skip itself before issuing the GET, so the extract does NOT fail and
// no getAiCodeFixConfig output directory is created.
func TestRunExtractAiCodeFixSkippedOnOldSQS(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/server/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "2025.1.0.12345")
	})
	mux.HandleFunc("GET /api/system/info", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"edition": "developer", "version": "2025.1.0.12345",
		})
	})
	// If the task fails to gate itself, the mock answers with 405 just
	// like the real SQS 2025.1 server in the issue report.
	v2Hit := false
	mux.HandleFunc("GET /api/v2/fix-suggestions/feature-enablements", func(w http.ResponseWriter, r *http.Request) {
		v2Hit = true
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"message":"Method not supported."}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	cfg := ExtractConfig{
		URL: srv.URL, Token: testToken, ExportDirectory: dir,
		TargetTask: "getAiCodeFixConfig", Concurrency: 1,
	}

	if _, err := RunExtract(context.Background(), cfg); err != nil {
		t.Fatalf("RunExtract failed: %v", err)
	}
	if v2Hit {
		t.Error("api/v2/fix-suggestions/feature-enablements was called; task should skip on SQS < 2025.3")
	}

	extractDir := firstExtractDir(t, dir)
	if _, err := os.Stat(filepath.Join(extractDir, "getAiCodeFixConfig")); !os.IsNotExist(err) {
		t.Errorf("getAiCodeFixConfig directory should not exist on SQS < 2025.3, got err=%v", err)
	}
}

// TestRawClientGetPaginated tests pagination with a mock server.
func TestRawClientGetPaginated(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/test/list", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("p")
		if page == "" || page == "1" {
			json.NewEncoder(w).Encode(map[string]any{
				"paging": map[string]any{"total": 3},
				"items":  []map[string]any{{"id": "1"}, {"id": "2"}},
			})
		} else {
			json.NewEncoder(w).Encode(map[string]any{
				"paging": map[string]any{"total": 3},
				"items":  []map[string]any{{"id": "3"}},
			})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	raw := NewRawClient(srv.Client(), srv.URL+"/")
	items, err := raw.GetPaginated(context.Background(), PaginatedOpts{
		Path: "api/test/list", ResultKey: "items", MaxPageSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
}

// TestRawClientGetArray tests non-paginated array fetch.
func TestRawClientGetArray(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/test/array", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{"id": "1"}, {"id": "2"}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	raw := NewRawClient(srv.Client(), srv.URL+"/")
	items, err := raw.GetArray(context.Background(), "api/test/array", "items", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

// TestRawClientGetRaw tests raw byte fetch.
func TestRawClientGetRaw(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/test/raw", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<xml>content</xml>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	raw := NewRawClient(srv.Client(), srv.URL+"/")
	data, err := raw.GetRaw(context.Background(), "api/test/raw", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "<xml>content</xml>" {
		t.Errorf("expected XML content, got %q", string(data))
	}
}

// TestRawClientHTTPError tests error handling for 4xx/5xx responses.
func TestRawClientHTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/test/error", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"errors":[{"msg":"Insufficient privileges"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	raw := NewRawClient(srv.Client(), srv.URL+"/")
	_, err := raw.Get(context.Background(), "api/test/error", nil)
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
}

// TestRawClientPageLimit tests that PageLimit caps pagination.
func TestRawClientPageLimit(t *testing.T) {
	requestCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/test/paged", func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"total": 5000},
			"items":  []map[string]any{{"id": "x"}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	raw := NewRawClient(srv.Client(), srv.URL+"/")
	_, err := raw.GetPaginated(context.Background(), PaginatedOpts{
		Path: "api/test/paged", ResultKey: "items", MaxPageSize: 500, PageLimit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	// total=5000 / pageSize=500 = 10 pages, but limit is 3.
	if requestCount != 3 {
		t.Errorf("expected 3 requests (page limit), got %d", requestCount)
	}
}

// TestExtractArray tests various extraction scenarios.
func TestExtractArrayEmptyKey(t *testing.T) {
	body := []byte(`[{"id":"1"},{"id":"2"}]`)
	items, err := extractArray(body, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestExtractArrayMissingKey(t *testing.T) {
	body := []byte(`{"other":[1,2]}`)
	items, err := extractArray(body, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if items != nil {
		t.Errorf("expected nil for missing key, got %v", items)
	}
}

func TestExtractTotalNestedPath(t *testing.T) {
	body := []byte(`{"paging":{"total":42}}`)
	total := extractTotal(body, "paging.total")
	if total != 42 {
		t.Errorf("expected 42, got %d", total)
	}
}

func TestExtractTotalMissingPath(t *testing.T) {
	body := []byte(`{"other":1}`)
	total := extractTotal(body, "paging.total")
	if total != 0 {
		t.Errorf("expected 0 for missing path, got %d", total)
	}
}

func TestTruncate(t *testing.T) {
	short := truncate([]byte("hi"), 10)
	if short != "hi" {
		t.Errorf("expected 'hi', got %q", short)
	}
	long := truncate([]byte("hello world this is long"), 5)
	if long != "hello..." {
		t.Errorf("expected 'hello...', got %q", long)
	}
}

func TestTotalPages(t *testing.T) {
	tests := []struct{ total, pageSize, expected int }{
		{0, 500, 0}, {1, 500, 1}, {500, 500, 1}, {501, 500, 2}, {1000, 500, 2},
		{-1, 500, 0}, {100, 0, 0},
	}
	for _, tt := range tests {
		got := totalPages(tt.total, tt.pageSize)
		if got != tt.expected {
			t.Errorf("totalPages(%d, %d) = %d, want %d", tt.total, tt.pageSize, got, tt.expected)
		}
	}
}

func TestDaysAgo(t *testing.T) {
	result := daysAgo(30)
	if len(result) < 19 {
		t.Errorf("expected ISO date string, got %q", result)
	}
}

func TestChunkStrings(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}
	chunks := chunkStrings(items, 2)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	if len(chunks[2]) != 1 {
		t.Errorf("expected last chunk size 1, got %d", len(chunks[2]))
	}
}

func TestBuildIssueCombos(t *testing.T) {
	combos := buildIssueCombos(nil)
	// 3 types × 5 severities = 15
	if len(combos) != 15 {
		t.Errorf("expected 15 combos, got %d", len(combos))
	}

	combosWithRes := buildIssueCombos(resolutions)
	// 4 resolutions × 3 types × 5 severities = 60
	if len(combosWithRes) != 60 {
		t.Errorf("expected 60 combos with resolutions, got %d", len(combosWithRes))
	}
}

func TestFetchAndWriteArray(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	dir := t.TempDir()
	store := NewDataStore(dir)
	raw := NewRawClient(srv.Client(), srv.URL+"/")
	e := &Executor{Raw: raw, Store: store, ServerURL: srv.URL + "/", Sem: make(chan struct{}, 5)}

	err := fetchAndWriteArray(context.Background(), e, "testArrayTask",
		"api/qualitygates/list", "qualitygates", nil, map[string]any{"serverUrl": e.ServerURL})
	if err != nil {
		t.Fatal(err)
	}

	items, err := store.ReadAll("testArrayTask")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 gates, got %d", len(items))
	}
}

func TestFetchAndWriteSingle(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	dir := t.TempDir()
	store := NewDataStore(dir)
	raw := NewRawClient(srv.Client(), srv.URL+"/")
	e := &Executor{Raw: raw, Store: store, ServerURL: srv.URL + "/", Sem: make(chan struct{}, 5)}

	err := fetchAndWriteSingle(context.Background(), e, "testSingleTask",
		"api/system/info", nil, "", map[string]any{"serverUrl": e.ServerURL})
	if err != nil {
		t.Fatal(err)
	}

	items, err := store.ReadAll("testSingleTask")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("expected 1 item, got %d", len(items))
	}
}

func TestFetchAndWriteSingleWithResultKey(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	dir := t.TempDir()
	store := NewDataStore(dir)
	raw := NewRawClient(srv.Client(), srv.URL+"/")
	e := &Executor{Raw: raw, Store: store, ServerURL: srv.URL + "/", Sem: make(chan struct{}, 5)}

	err := fetchAndWriteSingle(context.Background(), e, "testRuleShow",
		"api/rules/show", nil, "rule", map[string]any{"serverUrl": e.ServerURL})
	if err != nil {
		t.Fatal(err)
	}

	items, err := store.ReadAll("testRuleShow")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("expected 1 item, got %d", len(items))
	}
	// Verify it extracted the "rule" sub-object.
	name := extractField(items[0], "name")
	if name != "Rule Detail" {
		t.Errorf("expected 'Rule Detail', got %q", name)
	}
}

// Regression for the runID-collision bug — twin of the migrate-side
// test in migrate/migrate_test.go. Both helpers must use max+1 so
// extract and migrate never alias onto an existing run dir when the
// numbering has gaps.
func TestGenerateRunID_HandlesNumberingGaps(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")

	t.Run("empty directory yields -01", func(t *testing.T) {
		dir := t.TempDir()
		got := generateRunID(dir)
		want := today + "-01"
		if got != want {
			t.Errorf("want %q, got %q", want, got)
		}
	})

	t.Run("dirs -10..-19 yields -20 (not -11 collision)", func(t *testing.T) {
		dir := t.TempDir()
		for i := 10; i <= 19; i++ {
			if err := os.MkdirAll(filepath.Join(dir, fmt.Sprintf("%s-%02d", today, i)), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}
		got := generateRunID(dir)
		want := today + "-20"
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
		want := today + "-43"
		if got != want {
			t.Errorf("want %q, got %q", want, got)
		}
	})
}
