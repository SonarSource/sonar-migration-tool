// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// tranche is the shared fixture for the #664 tests: a cloud mock that counts
// how often each endpoint is hit and records the AddProject / Select calls.
type tranche struct {
	mu       sync.Mutex
	hits     map[string]int
	adds     []string // "project|profile"
	selects  []string // "project|gateId"
	profiles string   // body for /api/qualityprofiles/search
	gates    string   // body for /api/qualitygates/list
	projects map[string]bool
	e        *Executor
	logs     *bytes.Buffer
}

func newTranche(t *testing.T) *tranche {
	t.Helper()
	tr := &tranche{
		hits:     map[string]int{},
		profiles: `{"profiles":[]}`,
		gates:    `{"qualitygates":[]}`,
		projects: map[string]bool{},
		logs:     &bytes.Buffer{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, _ *http.Request) {
		tr.hit("profiles")
		_, _ = w.Write([]byte(tr.profiles))
	})
	mux.HandleFunc("GET /api/qualitygates/list", func(w http.ResponseWriter, _ *http.Request) {
		tr.hit("gates")
		_, _ = w.Write([]byte(tr.gates))
	})
	mux.HandleFunc("GET /api/projects/search", func(w http.ResponseWriter, r *http.Request) {
		tr.hit("projects")
		key := r.URL.Query().Get("projects")
		comps := []map[string]any{}
		if tr.projects[key] {
			comps = append(comps, map[string]any{"key": key, "name": key})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"components": comps})
	})
	mux.HandleFunc("POST /api/qualityprofiles/add_project", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tr.mu.Lock()
		tr.adds = append(tr.adds, r.FormValue("project")+"|"+r.FormValue("qualityProfile"))
		tr.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/qualitygates/select", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tr.mu.Lock()
		tr.selects = append(tr.selects, r.FormValue("projectKey")+"|"+r.FormValue("gateId"))
		tr.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	addDefaultCloudHandler(mux)
	tr.e = newCustomCloudTest(t, mux)
	tr.e.Logger = slog.New(slog.NewTextHandler(tr.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return tr
}

func (tr *tranche) hit(name string) {
	tr.mu.Lock()
	tr.hits[name]++
	tr.mu.Unlock()
}

func (tr *tranche) count(name string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.hits[name]
}

func (tr *tranche) seed(t *testing.T, task string, rows ...map[string]any) {
	t.Helper()
	w, err := tr.e.Store.Writer(task)
	if err != nil {
		t.Fatalf("writer %s: %v", task, err)
	}
	for _, r := range rows {
		b, _ := json.Marshal(r)
		if err := w.WriteOne(b); err != nil {
			t.Fatalf("seed %s: %v", task, err)
		}
	}
}

func (tr *tranche) seedProjects(t *testing.T, extra map[string]any, keys ...string) {
	t.Helper()
	var rows []map[string]any
	for _, k := range keys {
		row := map[string]any{
			"key": k, "server_url": testServerURL, "sonarcloud_org_key": "org1",
			"cloud_project_key": "org1_" + k,
		}
		for ek, ev := range extra {
			row[ek] = ev
		}
		rows = append(rows, row)
	}
	tr.seed(t, "createProjects", rows...)
}

func profileAssignments(profile string, keys ...string) []map[string]any {
	var rows []map[string]any
	for _, k := range keys {
		rows = append(rows, map[string]any{
			"key": k, "profileName": profile, "language": "py", "serverUrl": testServerURL,
		})
	}
	return rows
}

func (tr *tranche) seedExtract(t *testing.T, task string, rows []map[string]any) {
	t.Helper()
	writeJSONL(filepath.Join(tr.e.ExportDir, "extract-01", task), rows)
}

func failedCount(c *TaskCounter) int64 { return c.Outcome().Failed }

func TestSetProjectProfilesResolvesByNameWhenCreateProfilesAbsent(t *testing.T) {
	tr := newTranche(t)
	tr.profiles = `{"profiles":[{"key":"k1","name":"Custom Python","language":"py"}]}`
	tr.seedProjects(t, nil, "a", "b")
	tr.seedExtract(t, "getProfileProjects", profileAssignments("Custom Python", "a", "b"))

	if err := runSetProjectProfiles(context.Background(), tr.e); err != nil {
		t.Fatal(err)
	}
	if len(tr.adds) != 2 {
		t.Fatalf("expected both projects bound, got %v", tr.adds)
	}
	if n := tr.count("profiles"); n != 1 {
		t.Errorf("the profile must be looked up once and cached, got %d lookups", n)
	}
}

func TestSetProjectProfilesWarnsOnceWhenProfileMissingInCloud(t *testing.T) {
	tr := newTranche(t)
	tr.seedProjects(t, nil, "a", "b")
	tr.seedExtract(t, "getProfileProjects", profileAssignments("Ghost Profile", "a", "b"))

	counter := NewTaskCounter("setProjectProfiles")
	ctx := context.WithValue(context.Background(), taskCounterCtxKey{}, counter)
	if err := runSetProjectProfiles(ctx, tr.e); err != nil {
		t.Fatal(err)
	}
	if len(tr.adds) != 0 {
		t.Errorf("no assignment may be sent for a profile that does not exist, got %v", tr.adds)
	}
	if got := failedCount(counter); got != 2 {
		t.Errorf("both unassigned projects must count as failures, got %d", got)
	}
	out := tr.logs.String()
	if n := strings.Count(out, "does not exist in the target organization"); n != 1 {
		t.Errorf("expected exactly one WARN for the missing profile, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "Ghost Profile") {
		t.Errorf("the WARN must name the profile:\n%s", out)
	}
}

func TestSetProjectProfilesNoLookupWhenCreateProfilesPresent(t *testing.T) {
	tr := newTranche(t)
	tr.seedProjects(t, nil, "a")
	tr.seed(t, "createProfiles", map[string]any{
		"name": "Custom Python", "language": "py", "sonarcloud_org_key": "org1",
	})
	tr.seedExtract(t, "getProfileProjects", profileAssignments("Custom Python", "a"))

	if err := runSetProjectProfiles(context.Background(), tr.e); err != nil {
		t.Fatal(err)
	}
	if len(tr.adds) != 1 {
		t.Errorf("expected the project bound from createProfiles output, got %v", tr.adds)
	}
	if n := tr.count("profiles"); n != 0 {
		t.Errorf("the normal path must not call the cloud for lookups, got %d", n)
	}
}

func TestSetProjectProfilesLookupErrorCountsAsFailure(t *testing.T) {
	tr := newTranche(t)
	tr.profiles = `not json`
	tr.seedProjects(t, nil, "a")
	tr.seedExtract(t, "getProfileProjects", profileAssignments("Custom Python", "a"))

	counter := NewTaskCounter("setProjectProfiles")
	ctx := context.WithValue(context.Background(), taskCounterCtxKey{}, counter)
	if err := runSetProjectProfiles(ctx, tr.e); err != nil {
		t.Fatal(err)
	}
	if len(tr.adds) != 0 || failedCount(counter) != 1 {
		t.Errorf("a failed lookup must count as a failure and assign nothing, adds=%v failed=%d",
			tr.adds, failedCount(counter))
	}
	if !strings.Contains(tr.logs.String(), "could not look up the quality profile") {
		t.Errorf("expected a lookup-failure WARN:\n%s", tr.logs.String())
	}
}

func TestSetProjectGatesResolvesByNameWhenCreateGatesAbsent(t *testing.T) {
	tr := newTranche(t)
	tr.gates = `{"qualitygates":[{"id":77,"name":"Corp gate"}]}`
	tr.seedProjects(t, map[string]any{"gate_name": "Corp gate"}, "a", "b")

	if err := runSetProjectGates(context.Background(), tr.e); err != nil {
		t.Fatal(err)
	}
	if len(tr.selects) != 2 || !strings.HasSuffix(tr.selects[0], "|77") {
		t.Fatalf("expected both projects bound to gate 77, got %v", tr.selects)
	}
	if n := tr.count("gates"); n != 1 {
		t.Errorf("the gate must be looked up once and cached, got %d lookups", n)
	}
}

func TestSetProjectGatesWarnsOnceWhenGateMissingInCloud(t *testing.T) {
	tr := newTranche(t)
	tr.seedProjects(t, map[string]any{"gate_name": "Ghost gate"}, "a", "b")

	counter := NewTaskCounter("setProjectGates")
	ctx := context.WithValue(context.Background(), taskCounterCtxKey{}, counter)
	if err := runSetProjectGates(ctx, tr.e); err != nil {
		t.Fatal(err)
	}
	if len(tr.selects) != 0 {
		t.Errorf("no gate may be selected, got %v", tr.selects)
	}
	if got := failedCount(counter); got != 2 {
		t.Errorf("both projects must count as failures, got %d", got)
	}
	out := tr.logs.String()
	if n := strings.Count(out, "does not exist in the target organization"); n != 1 {
		t.Errorf("expected exactly one WARN for the missing gate, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "Ghost gate") {
		t.Errorf("the WARN must name the gate:\n%s", out)
	}
}

func TestSetProjectGatesNoLookupWhenCreateGatesPresent(t *testing.T) {
	tr := newTranche(t)
	tr.seed(t, "createGates", map[string]any{
		"name": "Corp gate", "sonarcloud_org_key": "org1", "cloud_gate_id": "5",
	})
	tr.seedProjects(t, map[string]any{"gate_name": "Corp gate"}, "a")

	if err := runSetProjectGates(context.Background(), tr.e); err != nil {
		t.Fatal(err)
	}
	if len(tr.selects) != 1 || tr.count("gates") != 0 {
		t.Errorf("normal path must bind from createGates without lookups, selects=%v lookups=%d",
			tr.selects, tr.count("gates"))
	}
}

// portfolioFixture seeds one MANUAL portfolio over projects a and b, with
// generateProjectMappings present and createProjects absent.
func (tr *tranche) portfolioFixture(t *testing.T) {
	t.Helper()
	tr.e.ProjectKeyPattern = DefaultProjectKeyPattern
	tr.seed(t, "generateProjectMappings",
		map[string]any{"key": "a", "server_url": testServerURL, "sonarcloud_org_key": "org1"},
		map[string]any{"key": "b", "server_url": testServerURL, "sonarcloud_org_key": "org1"},
	)
	tr.seedExtract(t, "getPortfolioProjects", []map[string]any{
		{"portfolioKey": "pf", "refKey": "a", "serverUrl": testServerURL},
		{"portfolioKey": "pf", "refKey": "b", "serverUrl": testServerURL},
	})
}

func TestResolvePortfolioProjectIndexFromMappingsVerifiesExistence(t *testing.T) {
	tr := newTranche(t)
	tr.portfolioFixture(t)
	tr.projects["org1_a"] = true // b was never migrated

	idx, err := resolvePortfolioProjectIndex(context.Background(), tr.e)
	if err != nil {
		t.Fatal(err)
	}
	if got := idx[testServerURL+"a"].CloudKey; got != "org1_a" {
		t.Errorf("project a must resolve to org1_a, got %q", got)
	}
	if _, ok := idx[testServerURL+"b"]; ok {
		t.Error("project b does not exist in the cloud and must be dropped from the index")
	}
}

func TestResolvePortfolioProjectIndexFailsFastWhenNothingMigrated(t *testing.T) {
	tr := newTranche(t)
	tr.portfolioFixture(t)

	_, err := resolvePortfolioProjectIndex(context.Background(), tr.e)
	if err == nil {
		t.Fatal("expected a fail-fast error when no referenced project exists in the target")
	}
	for _, want := range []string{"--objects projects", "--run_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must mention %q", err, want)
		}
	}
}

func TestResolvePortfolioProjectIndexUsesCreateProjectsWhenPresent(t *testing.T) {
	tr := newTranche(t)
	tr.portfolioFixture(t)
	tr.seedProjects(t, nil, "a")

	idx, err := resolvePortfolioProjectIndex(context.Background(), tr.e)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 1 || tr.count("projects") != 0 {
		t.Errorf("normal path must use createProjects output with no API calls, idx=%v lookups=%d",
			idx, tr.count("projects"))
	}
}

// templateTranche is the #685 fixture: a cloud mock that counts group
// lookups and records the template group grants.
type templateTranche struct {
	mu      sync.Mutex
	lookups int
	grants  []string // "group:perm"
	groups  string   // body for /api/user_groups/search
	e       *Executor
	logs    *bytes.Buffer
}

func newTemplateTranche(t *testing.T) *templateTranche {
	t.Helper()
	tt := &templateTranche{groups: `{"groups":[]}`, logs: &bytes.Buffer{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/user_groups/search", func(w http.ResponseWriter, _ *http.Request) {
		tt.mu.Lock()
		tt.lookups++
		tt.mu.Unlock()
		_, _ = w.Write([]byte(tt.groups))
	})
	mux.HandleFunc("POST /api/permissions/add_group_to_template", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tt.mu.Lock()
		tt.grants = append(tt.grants, r.FormValue("groupName")+":"+r.FormValue("permission"))
		tt.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	addDefaultCloudHandler(mux)
	tt.e = newCustomCloudTest(t, mux)
	tt.e.Logger = slog.New(slog.NewTextHandler(tt.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// A permission template created by this run; no createGroups output,
	// as in `migrate --objects pt` after an earlier groups run.
	writeTaskJSONL(t, tt.e, "createPermissionTemplates", []map[string]any{
		{"server_url": testServerURL, "source_template_key": "tpl-src-1",
			"cloud_template_id": "tpl-cloud-1", "sonarcloud_org_key": "cloud-org"},
	})
	return tt
}

// seedFeeds writes the same group, with the given permissions, in both
// template-group feeds.
func (tt *templateTranche) seedFeeds(t *testing.T, group string, perms ...string) {
	t.Helper()
	for _, feed := range []string{"getTemplateGroupsScanners", "getTemplateGroupsViewers"} {
		writeJSONL(extractPath(tt.e, feed), []map[string]any{
			{"templateId": "tpl-src-1", "name": group, "permissions": perms, "serverUrl": testServerURL},
			{"templateId": "tpl-src-1", "name": "sonar-users", "permissions": []string{"user"}, "serverUrl": testServerURL},
		})
	}
}

func TestSetTemplateGroupPermissionsResolvesByNameWhenCreateGroupsAbsent(t *testing.T) {
	tt := newTemplateTranche(t)
	tt.groups = `{"groups":[{"id":42,"name":"developers"}]}`
	tt.seedFeeds(t, "developers", "scan", "user")

	if err := runSetTemplateGroupPermissions(context.Background(), tt.e); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sortedCopy(tt.grants), ",")
	if got != "Members:user,developers:scan,developers:user" {
		t.Errorf("grants = %s, want the earlier run's group granted and sonar-users aliased", got)
	}
	if tt.lookups != 1 {
		t.Errorf("the group must be looked up once and cached, got %d lookups", tt.lookups)
	}
}

func TestSetTemplateGroupPermissionsWarnsOnceWhenGroupMissingInCloud(t *testing.T) {
	tt := newTemplateTranche(t)
	tt.seedFeeds(t, "ghost", "scan", "user")

	counter := NewTaskCounter("setTemplateGroupPermissions")
	ctx := context.WithValue(context.Background(), taskCounterCtxKey{}, counter)
	if err := runSetTemplateGroupPermissions(ctx, tt.e); err != nil {
		t.Fatal(err)
	}
	for _, g := range tt.grants {
		if strings.HasPrefix(g, "ghost:") {
			t.Errorf("no grant may be sent for a group that does not exist, got %v", tt.grants)
		}
	}
	if got := failedCount(counter); got != 1 {
		t.Errorf("the ungranted (template, group) pair must count as one failure, got %d", got)
	}
	out := tt.logs.String()
	if n := strings.Count(out, "does not exist in the target organization"); n != 1 {
		t.Errorf("expected exactly one WARN for the missing group, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "ghost") {
		t.Errorf("the WARN must name the group:\n%s", out)
	}
}

func TestSetTemplateGroupPermissionsNoLookupWhenCreateGroupsPresent(t *testing.T) {
	tt := newTemplateTranche(t)
	writeTaskJSONL(t, tt.e, "createGroups", []map[string]any{
		{"name": "developers", "sonarcloud_org_key": "cloud-org"},
	})
	tt.seedFeeds(t, "ghost", "user")

	if err := runSetTemplateGroupPermissions(context.Background(), tt.e); err != nil {
		t.Fatal(err)
	}
	if tt.lookups != 0 {
		t.Errorf("the normal path must not call the cloud for lookups, got %d", tt.lookups)
	}
	if got := strings.Join(tt.grants, ","); got != "Members:user" {
		t.Errorf("grants = %s, want only the aliased built-in group", got)
	}
}

func TestSetTemplateGroupPermissionsLookupErrorCountsAsFailure(t *testing.T) {
	tt := newTemplateTranche(t)
	tt.groups = `not json`
	tt.seedFeeds(t, "developers", "user")

	counter := NewTaskCounter("setTemplateGroupPermissions")
	ctx := context.WithValue(context.Background(), taskCounterCtxKey{}, counter)
	if err := runSetTemplateGroupPermissions(ctx, tt.e); err != nil {
		t.Fatal(err)
	}
	if failedCount(counter) != 1 {
		t.Errorf("a failed lookup must count as one failure, got %d", failedCount(counter))
	}
	if !strings.Contains(tt.logs.String(), "could not look up the group") {
		t.Errorf("expected a lookup-failure WARN:\n%s", tt.logs.String())
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
