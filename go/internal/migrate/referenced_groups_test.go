// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// #692: a tranche that grants group permissions (templates, projects,
// profiles) without selecting groups must still schedule createGroups,
// ahead of the tasks granting the permissions; setOrgGroupPermissions
// stays with the groups tranche.
func TestPlanPhasesGroupReferencingObjectsScheduleCreateGroups(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	cases := []struct {
		objects     []string
		wantCreate  bool
		grantingTsk string
	}{
		{[]string{"permission_templates"}, true, "setTemplateGroupPermissions"},
		{[]string{"projects"}, true, "setProjectGroupPermissions"},
		{[]string{"quality_profiles"}, true, "setProfileGroupPermissions"},
		{[]string{"settings"}, false, ""},
		{[]string{"portfolios"}, false, ""},
		{[]string{"groups"}, true, ""},
	}
	for _, tc := range cases {
		objects, err := common.ParseObjects(tc.objects)
		if err != nil {
			t.Fatalf("ParseObjects(%v): %v", tc.objects, err)
		}
		flags := MigrateTargetTasksFlags{IncludeProjectData: true}
		targets := MigrateTargetTasks(reg, "", flags, nil, objects)
		excluded := excludedMigrateTasks(objects)
		plan, err := PlanPhasesExcluding(ResolveDependenciesExcluding(targets, reg, excluded), reg, excluded)
		if err != nil {
			t.Fatalf("%v: PlanPhasesExcluding: %v", tc.objects, err)
		}
		if got := planContainsTask(plan, "createGroups"); got != tc.wantCreate {
			t.Errorf("%v: createGroups scheduled = %v, want %v (plan %v)", tc.objects, got, tc.wantCreate, plan)
		}
		if !slices.Contains(tc.objects, "groups") && planContainsTask(plan, "setOrgGroupPermissions") {
			t.Errorf("%v: setOrgGroupPermissions belongs to the groups tranche only (plan %v)", tc.objects, plan)
		}
		if tc.grantingTsk != "" && phaseOf(plan, "createGroups") >= phaseOf(plan, tc.grantingTsk) {
			t.Errorf("%v: createGroups (phase %d) must run before %s (phase %d)",
				tc.objects, phaseOf(plan, "createGroups"), tc.grantingTsk, phaseOf(plan, tc.grantingTsk))
		}
	}
}

func phaseOf(plan [][]string, name string) int {
	for i, phase := range plan {
		if slices.Contains(phase, name) {
			return i
		}
	}
	return -1
}

// groupsTarget fakes the target organization's groups: it lists existing
// and records every create.
type groupsTarget struct {
	mu       sync.Mutex
	existing []string
	created  []string
	listings int
}

func (g *groupsTarget) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/user_groups/search", func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.listings++
		groups := make([]map[string]any, 0, len(g.existing))
		for i, name := range g.existing {
			groups = append(groups, map[string]any{"id": 500 + i, "name": name})
		}
		json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": len(groups)},
			"groups": groups,
		})
	})
	mux.HandleFunc("POST /api/user_groups/create", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		g.mu.Lock()
		g.created = append(g.created, r.FormValue("name"))
		g.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"group": map[string]any{"id": 101, "name": r.FormValue("name")}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// runCreateGroupsAgainst runs createGroups over groups.csv rows for one
// organization, with the given --objects selection.
func runCreateGroupsAgainst(t *testing.T, target *groupsTarget, objects []string, names ...string) *Executor {
	t.Helper()
	srv := target.server(t)
	dir := t.TempDir()
	e := newTestExecutor(srv, srv, dir)
	if objects != nil {
		parsed, err := common.ParseObjects(objects)
		if err != nil {
			t.Fatalf("ParseObjects: %v", err)
		}
		e.Objects = parsed
	}
	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		rows = append(rows, map[string]any{"name": name, "server_url": testServerURL,
			"sonarqube_org_key": "org1", "sonarcloud_org_key": testCloudOrg, "description": ""})
	}
	writeCSVFromMaps(t, dir, "groups", rows)
	runTask(t, e, "generateGroupMappings")
	if err := runCreateGroups(context.Background(), e); err != nil {
		t.Fatalf("createGroups: %v", err)
	}
	return e
}

// In a projects tranche, only the groups the target lacks are created and
// recorded; the ones an earlier tranche created are left alone.
func TestCreateGroupsInGroupReferencingTrancheCreatesOnlyMissing(t *testing.T) {
	target := &groupsTarget{existing: []string{"ci-tools"}}
	e := runCreateGroupsAgainst(t, target, []string{"projects"}, "ci-tools", "project-admins", "developers", "sonar-users")

	slices.Sort(target.created)
	if want := []string{"developers", "project-admins"}; !slices.Equal(target.created, want) {
		t.Errorf("created %v, want %v", target.created, want)
	}
	if target.listings != 1 {
		t.Errorf("listed the organization's groups %d times, want once", target.listings)
	}
	items, _ := e.Store.ReadAll("createGroups")
	var recorded []string
	for _, it := range items {
		recorded = append(recorded, extractField(it, "name"))
	}
	slices.Sort(recorded)
	if want := []string{"developers", "project-admins"}; !slices.Equal(recorded, want) {
		t.Errorf("createGroups recorded %v, want only the groups this run created %v", recorded, want)
	}
}

// A groups tranche, and a run selecting everything, keep the previous
// behaviour: every row is created or looked up, without listing first.
func TestCreateGroupsInGroupsTrancheDoesNotListFirst(t *testing.T) {
	for _, objects := range [][]string{nil, {"groups"}, {"groups", "projects"}} {
		target := &groupsTarget{existing: []string{"ci-tools"}}
		runCreateGroupsAgainst(t, target, objects, "project-admins")
		if target.listings != 0 {
			t.Errorf("%v: listed the organization's groups %d times, want 0", objects, target.listings)
		}
		if !slices.Equal(target.created, []string{"project-admins"}) {
			t.Errorf("%v: created %v, want [project-admins]", objects, target.created)
		}
	}
}

// If the listing fails, every referenced group is still attempted, as
// createGroups copes with a group that already exists.
func TestCreateGroupsListingFailureFallsBackToCreate(t *testing.T) {
	mux := http.NewServeMux()
	var created []string
	mux.HandleFunc("GET /api/user_groups/search", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"msg":"boom"}]}`, http.StatusInternalServerError)
	})
	mux.HandleFunc("POST /api/user_groups/create", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		created = append(created, r.FormValue("name"))
		json.NewEncoder(w).Encode(map[string]any{"group": map[string]any{"id": 7, "name": r.FormValue("name")}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	e := newTestExecutor(srv, srv, dir)
	e.Objects, _ = common.ParseObjects([]string{"permission_templates"})
	writeCSVFromMaps(t, dir, "groups", []map[string]any{{"name": "project-admins", "server_url": testServerURL,
		"sonarqube_org_key": "org1", "sonarcloud_org_key": testCloudOrg, "description": ""}})
	runTask(t, e, "generateGroupMappings")
	if err := runCreateGroups(context.Background(), e); err != nil {
		t.Fatalf("createGroups: %v", err)
	}
	if !slices.Equal(created, []string{"project-admins"}) {
		t.Errorf("created %v, want [project-admins]", created)
	}
}
