// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

// --- runDeleteGroups (was 38.5%) ---

// Enumerates via /api/user_groups/search and deletes every non-default group;
// the default "Members" group is preserved.
func TestRunDeleteGroupsDeletesNonDefault(t *testing.T) {
	var (
		mu      sync.Mutex
		deleted []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/user_groups/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"groups": []map[string]any{
				{"id": 1, "name": "Members", "default": true},
				{"id": 2, "name": "developers", "default": false},
				{"id": 3, "name": "migration-scanners", "default": false},
			},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 3},
		})
	})
	mux.HandleFunc("POST /api/user_groups/delete", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		deleted = append(deleted, r.FormValue("name"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runDeleteGroups(context.Background(), e); err != nil {
		t.Fatalf("runDeleteGroups: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 2 {
		t.Fatalf("expected 2 deletes (developers, migration-scanners), got %d: %v", len(deleted), deleted)
	}
	got := map[string]bool{}
	for _, n := range deleted {
		got[n] = true
	}
	if !got["developers"] || !got["migration-scanners"] {
		t.Errorf("unexpected delete set: %v", got)
	}
	if got["Members"] {
		t.Error("the default \"Members\" group must never be deleted")
	}
}

// A 404 from delete is treated as success (already gone / idempotent).
func TestRunDeleteGroupsNotFoundCountsSuccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/user_groups/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"groups": []map[string]any{
				{"id": 2, "name": "developers", "default": false},
			},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
		})
	})
	mux.HandleFunc("POST /api/user_groups/delete", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"msg":"No group found"}]}`, http.StatusNotFound)
	})
	e := newDeleteTest(t, mux)
	if err := runDeleteGroups(context.Background(), e); err != nil {
		t.Fatalf("404 on delete must be swallowed as success, got %v", err)
	}
}

// A non-404 delete failure hits the counter.Fail()/continue branch.
func TestRunDeleteGroupsDeleteError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/user_groups/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"groups": []map[string]any{{"id": 2, "name": "developers", "default": false}},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
		})
	})
	mux.HandleFunc("POST /api/user_groups/delete", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"msg":"boom"}]}`, http.StatusForbidden)
	})
	e := newDeleteTest(t, mux)
	if err := runDeleteGroups(context.Background(), e); err != nil {
		t.Fatalf("delete 403 must be swallowed, got %v", err)
	}
}

// The listing call itself failing hits the "listing groups failed" branch.
func TestRunDeleteGroupsListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/user_groups/search", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"msg":"boom"}]}`, http.StatusInternalServerError)
	})
	e := newDeleteTest(t, mux)
	if err := runDeleteGroups(context.Background(), e); err != nil {
		t.Fatalf("list failure must be swallowed per-org, got %v", err)
	}
}

// --- runDeleteProjects (was 76.9%) ---

func TestRunDeleteProjects(t *testing.T) {
	var (
		mu      sync.Mutex
		deleted []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/delete", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		deleted = append(deleted, r.FormValue("project"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	// deleteProjects reads getCreatedProjects, keyed by "key".
	writeTaskJSONL(t, e, "getCreatedProjects", []map[string]any{
		{"key": "cloud-proj-1"},
		{"key": ""}, // blank key — skipped.
		{"key": "cloud-proj-2"},
	})

	if err := runDeleteProjects(context.Background(), e); err != nil {
		t.Fatalf("runDeleteProjects: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 2 {
		t.Fatalf("expected 2 project deletes, got %d: %v", len(deleted), deleted)
	}
}

// A delete API failure exercises the counter.Fail() branch.
func TestRunDeleteProjectsAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/delete", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"msg":"boom"}]}`, http.StatusForbidden)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)
	writeTaskJSONL(t, e, "getCreatedProjects", []map[string]any{{"key": "cloud-proj-1"}})
	if err := runDeleteProjects(context.Background(), e); err != nil {
		t.Fatalf("delete 403 must be swallowed, got %v", err)
	}
}

// --- runDeletePortfolios (was 75.0%) ---

func TestRunDeletePortfolios(t *testing.T) {
	// The portfolio DELETE lands on the enterprise API host, which
	// newMockAPIServer (wired by newDeleteTest) answers with 204 on
	// "DELETE /enterprises/portfolios/". The cloud mux only needs the
	// org-mapping no-op traffic.
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, cloudMux)

	// deletePortfolios reads createPortfolios, keyed by "cloud_portfolio_id".
	writeTaskJSONL(t, e, "createPortfolios", []map[string]any{
		{"cloud_portfolio_id": "pf-1"},
		{"cloud_portfolio_id": ""}, // blank — skipped.
		{"cloud_portfolio_id": "pf-2"},
	})

	if err := runDeletePortfolios(context.Background(), e); err != nil {
		t.Fatalf("runDeletePortfolios: %v", err)
	}

	items, _ := e.Store.ReadAll("deletePortfolios")
	// The task writes no output rows (it only calls the API), so we assert on
	// the absence of an error above and that the run completed.
	_ = items
}

// --- Reset/delete list-failure branches (drive the early counter.Fail path) ---

// deleteProfiles: a failing /api/qualityprofiles/search hits the
// "listing profiles failed" branch and returns nil per-org.
func TestRunDeleteProfilesListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"msg":"boom"}]}`, http.StatusInternalServerError)
	})
	e := newDeleteTest(t, mux)
	if err := runDeleteProfiles(context.Background(), e); err != nil {
		t.Fatalf("profile list failure must be swallowed, got %v", err)
	}
}

// deleteGates: a failing /api/qualitygates/list hits the analogous branch.
func TestRunDeleteGatesListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualitygates/list", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"msg":"boom"}]}`, http.StatusInternalServerError)
	})
	e := newDeleteTest(t, mux)
	if err := runDeleteGates(context.Background(), e); err != nil {
		t.Fatalf("gate list failure must be swallowed, got %v", err)
	}
}

// deleteTemplates: a failing /api/permissions/search_templates hits the
// "listing templates failed" branch.
func TestRunDeleteTemplatesListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/permissions/search_templates", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"msg":"boom"}]}`, http.StatusInternalServerError)
	})
	e := newDeleteTest(t, mux)
	if err := runDeleteTemplates(context.Background(), e); err != nil {
		t.Fatalf("template list failure must be swallowed, got %v", err)
	}
}

// resetGlobalSettings: a failing /api/settings/values hits its list-failure
// branch.
func TestRunResetGlobalSettingsListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/settings/values", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"msg":"boom"}]}`, http.StatusInternalServerError)
	})
	e := newDeleteTest(t, mux)
	if err := runResetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("settings list failure must be swallowed, got %v", err)
	}
}

// resetDefaultProfiles: no built-in profile for a language whose default is
// custom hits the "no built-in profile found" warn+fail branch.
func TestRunResetDefaultProfilesNoBuiltIn(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				// Custom is the default, and there is NO built-in for java.
				{"key": "j2", "name": "Custom Java", "language": "java", "isBuiltIn": false, "isDefault": true},
			},
		})
	})
	e := newDeleteTest(t, mux)
	if err := runResetDefaultProfiles(context.Background(), e); err != nil {
		t.Fatalf("runResetDefaultProfiles: %v", err)
	}
}

// resetPermissionTemplates: no built-in "Default Template" present must now
// hard-fail the task (#550) rather than warn-and-continue. Silently
// proceeding let deleteTemplates run against an org whose built-in was
// renamed (so it never got promoted to default), risking deletion of
// whatever template legitimately IS the current default.
func TestRunResetPermissionTemplatesNoBuiltIn(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/permissions/search_templates", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"permissionTemplates": []map[string]any{
				{"id": "tpl-custom", "name": "Custom Only"},
			},
			"defaultTemplates": []map[string]any{
				{"templateId": "tpl-custom", "qualifier": "TRK"},
			},
		})
	})
	e := newDeleteTest(t, mux)
	err := runResetPermissionTemplates(context.Background(), e)
	if err == nil {
		t.Fatal("runResetPermissionTemplates: expected an error when no built-in \"Default Template\" is found, got nil")
	}
}
