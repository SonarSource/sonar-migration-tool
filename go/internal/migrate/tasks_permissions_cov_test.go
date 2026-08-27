// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

// newPermissionsTest wires an Executor against newMockCloudServer (which already
// answers add_user / add_group / add_group_to_template with 204) plus the
// standard extract data. Returns the executor for direct Run-function calls.
func newPermissionsTest(t *testing.T) *Executor {
	t.Helper()
	cloudSrv := newMockCloudServer()
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	dir := t.TempDir()
	setupExtractData(dir)
	return newTestExecutor(cloudSrv, apiSrv, dir)
}

// --- runGrantMigrationUserProjectPermissions (was 65.4%) ---

// Happy path: one project + one migration user → four add_user calls (one per
// permission in migrationUserProjectPermissions).
func TestRunGrantMigrationUserProjectPermissions(t *testing.T) {
	var (
		mu    sync.Mutex
		perms []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/permissions/add_user", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		perms = append(perms, r.FormValue("permission"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	e := newCustomCloudTest(t, mux)

	writeTaskJSONL(t, e, "getMigrationUser", []map[string]any{{"login": "migration-user"}})
	writeTaskJSONL(t, e, "createProjects", []map[string]any{
		{"key": "proj1", "cloud_project_key": "cloud-proj", "sonarcloud_org_key": "cloud-org"},
		// skipped org — no add_user calls for it.
		{"key": "proj2", "cloud_project_key": "cloud-proj2", "sonarcloud_org_key": ""},
		// missing cloud key — skipped.
		{"key": "proj3", "sonarcloud_org_key": "cloud-org"},
	})

	if err := runGrantMigrationUserProjectPermissions(context.Background(), e); err != nil {
		t.Fatalf("runGrantMigrationUserProjectPermissions: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(perms) != len(migrationUserProjectPermissions) {
		t.Fatalf("expected %d add_user calls, got %d: %v",
			len(migrationUserProjectPermissions), len(perms), perms)
	}
	got := map[string]bool{}
	for _, p := range perms {
		got[p] = true
	}
	for _, want := range migrationUserProjectPermissions {
		if !got[want] {
			t.Errorf("missing add_user for permission %q", want)
		}
	}
}

// No migration user record → nothing to grant, no error.
func TestRunGrantMigrationUserProjectPermissionsNoUser(t *testing.T) {
	e := newPermissionsTest(t)
	// createProjects present but no getMigrationUser.
	writeTaskJSONL(t, e, "createProjects", []map[string]any{
		{"key": "proj1", "cloud_project_key": "cloud-proj", "sonarcloud_org_key": "cloud-org"},
	})
	if err := runGrantMigrationUserProjectPermissions(context.Background(), e); err != nil {
		t.Fatalf("expected no error when no migration user, got %v", err)
	}
}

// Migration user record present but blank login → nothing to grant.
func TestRunGrantMigrationUserProjectPermissionsBlankLogin(t *testing.T) {
	e := newPermissionsTest(t)
	writeTaskJSONL(t, e, "getMigrationUser", []map[string]any{{"login": ""}})
	writeTaskJSONL(t, e, "createProjects", []map[string]any{
		{"key": "proj1", "cloud_project_key": "cloud-proj", "sonarcloud_org_key": "cloud-org"},
	})
	if err := runGrantMigrationUserProjectPermissions(context.Background(), e); err != nil {
		t.Fatalf("expected no error for blank login, got %v", err)
	}
}

// add_user failing (403) exercises the counter.Fail()/continue branch without
// failing the task.
func TestRunGrantMigrationUserProjectPermissionsAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/permissions/add_user", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Insufficient privileges"}]}`, http.StatusForbidden)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	e := newCustomCloudTest(t, mux)
	writeTaskJSONL(t, e, "getMigrationUser", []map[string]any{{"login": "migration-user"}})
	writeTaskJSONL(t, e, "createProjects", []map[string]any{
		{"key": "proj1", "cloud_project_key": "cloud-proj", "sonarcloud_org_key": "cloud-org"},
	})
	if err := runGrantMigrationUserProjectPermissions(context.Background(), e); err != nil {
		t.Fatalf("add_user 403 must be swallowed, got err %v", err)
	}
}

// --- runSetTemplateGroupPermissions (was 0.0%) ---

// setupTemplateGroupExtract writes the two template-group feeds plus the
// createPermissionTemplates and createGroups migrate outputs the task joins on.
func setupTemplateGroupExtract(t *testing.T, e *Executor) {
	t.Helper()
	writeTaskJSONL(t, e, "createPermissionTemplates", []map[string]any{
		{"server_url": testServerURL, "source_template_key": "tpl-src-1",
			"cloud_template_id": "tpl-cloud-1", "sonarcloud_org_key": "cloud-org"},
	})
	writeTaskJSONL(t, e, "createGroups", []map[string]any{
		{"name": "developers", "sonarcloud_org_key": "cloud-org"},
	})
	// Extract feeds. These are structure.ExtractItem feeds read via
	// forEachExtractItem, so they live in the extract dir.
	writeJSONL(extractPath(e, "getTemplateGroupsScanners"), []map[string]any{
		// Custom migrated group with scan+user — applied.
		{"templateId": "tpl-src-1", "name": "developers",
			"permissions": []string{"scan", "user"}, "serverUrl": testServerURL},
		// Built-in alias sonar-users → Members (no groupExists check).
		{"templateId": "tpl-src-1", "name": "sonar-users",
			"permissions": []string{"user"}, "serverUrl": testServerURL},
		// Skipped built-in.
		{"templateId": "tpl-src-1", "name": "sonar-administrators",
			"permissions": []string{"admin"}, "serverUrl": testServerURL},
		// Unknown template id — no mapping, skipped.
		{"templateId": "unknown", "name": "developers",
			"permissions": []string{"scan"}, "serverUrl": testServerURL},
	})
	writeJSONL(extractPath(e, "getTemplateGroupsViewers"), []map[string]any{
		// Same (tpl, developers, user) triple as the scanners feed — deduped.
		{"templateId": "tpl-src-1", "name": "developers",
			"permissions": []string{"user"}, "serverUrl": testServerURL},
		// A group that never made it into createGroups — dropped.
		{"templateId": "tpl-src-1", "name": "ghost",
			"permissions": []string{"user"}, "serverUrl": testServerURL},
	})
}

func TestRunSetTemplateGroupPermissions(t *testing.T) {
	var (
		mu   sync.Mutex
		adds []string // "<group>:<perm>"
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/permissions/add_group_to_template", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		adds = append(adds, r.FormValue("groupName")+":"+r.FormValue("permission"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	e := newCustomCloudTest(t, mux)
	setupTemplateGroupExtract(t, e)

	if err := runSetTemplateGroupPermissions(context.Background(), e); err != nil {
		t.Fatalf("runSetTemplateGroupPermissions: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	got := map[string]bool{}
	for _, a := range adds {
		got[a] = true
	}
	// developers gets scan + user (user deduped across both feeds).
	if !got["developers:scan"] || !got["developers:user"] {
		t.Errorf("expected developers scan+user, got %v", got)
	}
	// sonar-users is remapped to Members.
	if !got["Members:user"] {
		t.Errorf("expected sonar-users remapped to Members:user, got %v", got)
	}
	// Built-in / unknown / ghost groups must NOT be granted.
	for _, bad := range []string{"sonar-administrators:admin", "ghost:user"} {
		if got[bad] {
			t.Errorf("did not expect %q to be granted", bad)
		}
	}
	// Exactly three distinct grants (developers:scan, developers:user, Members:user).
	if len(got) != 3 {
		t.Errorf("expected 3 distinct grants, got %d: %v", len(got), got)
	}
}

// No permission templates in scope → early return, no extract read.
func TestRunSetTemplateGroupPermissionsNoTemplates(t *testing.T) {
	e := newPermissionsTest(t)
	// createPermissionTemplates absent → templateMap empty → early return.
	if err := runSetTemplateGroupPermissions(context.Background(), e); err != nil {
		t.Fatalf("expected clean early return, got %v", err)
	}
}

// --- runSetOrgGroupPermissions error branch (add_group fails) ---

func TestRunSetOrgGroupPermissionsAddGroupError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/permissions/add_group", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"msg":"boom"}]}`, http.StatusForbidden)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	cloudSrv := httptest.NewServer(mux)
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	dir := t.TempDir()
	setupExtractData(dir) // getGroups row: sonar-users (→ Members) with scan perm
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	if err := runSetOrgGroupPermissions(context.Background(), e); err != nil {
		t.Fatalf("add_group 403 must be swallowed, got %v", err)
	}
}

// extractPath returns the extract-run directory path for a feed name.
func extractPath(e *Executor, feed string) string {
	return filepath.Join(e.ExportDir, "extract-01", feed)
}
