// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestGroupSelected(t *testing.T) {
	if !(&Executor{}).groupSelected("anything") {
		t.Error("no filter must select every group")
	}
	e := &Executor{GroupRe: regexp.MustCompile("^team-a-.+$")}
	if !e.groupSelected("team-a-devs") {
		t.Error("matching group must be selected")
	}
	if e.groupSelected("team-b-devs") {
		t.Error("non-matching group must not be selected")
	}
}

func TestLoadMigrateConfigFileGroupNameRegexp(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"flat", `{"url":"u","token":"t","enterprise_key":"e","group_name_regexp":"flat-.+"}`, "flat-.+"},
		{"unified top-level fallback", `{"group_name_regexp":"top-.+","target":{"url":"u","token":"t"}}`, "top-.+"},
		{"unified target wins", `{"group_name_regexp":"top-.+","target":{"url":"u","token":"t","group_name_regexp":"tgt-.+"}}`, "tgt-.+"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir() + "/cfg.json"
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadMigrateConfigFile(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.GroupNameRegexp != tc.want {
				t.Errorf("GroupNameRegexp: got %q, want %q", cfg.GroupNameRegexp, tc.want)
			}
		})
	}
}

// The built-in groups are grantees, not groups a slice migrates: every
// --group_name_regexp slice keeps their permissions, by the rule extract
// applies (extract.GroupInScope). #669 review.
func TestGroupSelectedKeepsBuiltInGroups(t *testing.T) {
	e := &Executor{GroupRe: regexp.MustCompile("^team-a-.+$")}
	for _, name := range []string{"Anyone", sonarUsersGroupName} {
		if !e.groupSelected(name) {
			t.Errorf("built-in group %q must stay in scope of a group slice", name)
		}
	}
}

// With a group slice, a project's grants to Anyone and to sonar-users
// (remapped to Members, #269) must still be applied, while a custom group
// outside the slice stays skipped.
func TestApplyGroupPermissionsSliceKeepsBuiltInGrants(t *testing.T) {
	var (
		mu      sync.Mutex
		granted []string
	)
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/permissions/add_group", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		granted = append(granted, r.FormValue("groupName")+":"+r.FormValue("permission"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)
	e.GroupRe = regexp.MustCompile("^team-a-.+$")

	w, err := e.Store.Writer("setProjectGroupPermissions")
	if err != nil {
		t.Fatal(err)
	}
	pm := projectMapping{CloudKey: "cloud-org1_proj1", OrgKey: testCloudOrg}
	ctx := context.Background()
	for _, group := range []string{"Anyone", sonarUsersGroupName, "team-a-devs", "team-b-devs"} {
		data, _ := json.Marshal(map[string]any{"name": group, "permissions": []string{"user"}})
		applyGroupPermissions(ctx, e, data, pm, w, TaskCounterFromContext(ctx))
	}

	mu.Lock()
	defer mu.Unlock()
	sort.Strings(granted)
	want := []string{"Anyone:user", "Members:user", "team-a-devs:user"}
	if strings.Join(granted, ",") != strings.Join(want, ",") {
		t.Errorf("granted %v, want %v", granted, want)
	}
}
