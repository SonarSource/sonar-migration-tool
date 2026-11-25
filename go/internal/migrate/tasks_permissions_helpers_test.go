// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// newTemplateGroupApplier builds a templateGroupPermApplier around e with
// empty state, for exercising its methods directly.
func newTemplateGroupApplier(e *Executor, byName bool, exists map[string]bool) *templateGroupPermApplier {
	return &templateGroupPermApplier{
		e:           e,
		templateMap: map[string]templateGroupRef{},
		groupExists: exists,
		skipGroups:  map[string]bool{},
		resolver:    newTargetEntityResolver(e, "setTemplateGroupPermissions", byName),
		applied:     map[templateGroupPermTriple]bool{},
		appliedMu:   &sync.Mutex{},
		pairStats:   map[templateGroupPairKey]*templateGroupPairStat{},
		unresolved:  map[templateGroupPairKey]bool{},
		statsMu:     &sync.Mutex{},
		counter:     NewTaskCounter("setTemplateGroupPermissions"),
	}
}

// groupSearchExecutor returns an Executor whose target answers
// api/user_groups/search: "present" is found, "broken" fails, anything else
// is missing. searches counts the lookups made.
func groupSearchExecutor(t *testing.T, searches *atomic.Int32) *Executor {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/user_groups/search", func(w http.ResponseWriter, r *http.Request) {
		searches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("q") {
		case "present":
			_, _ = w.Write([]byte(`{"paging":{"total":1},"groups":[{"id":"7","name":"present"}]}`))
		case "broken":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(`{"paging":{"total":0},"groups":[]}`))
		}
	})
	return newCustomCloudTest(t, mux)
}

var testGroupTemplate = templateGroupRef{cloudID: "tpl", org: "org"}

func TestGroupResolvableAliasedShortCircuits(t *testing.T) {
	var searches atomic.Int32
	a := newTemplateGroupApplier(groupSearchExecutor(t, &searches), true, nil)
	var recs []json.RawMessage
	if !a.groupResolvable(context.Background(), testGroupTemplate, "sonar-users", "Members", true, &recs) {
		t.Error("aliased group must be resolvable")
	}
	if len(recs) != 0 || searches.Load() != 0 {
		t.Errorf("aliased group must not be looked up or recorded: %v, %d searches", recs, searches.Load())
	}
}

func TestGroupResolvableCreatedGroupShortCircuits(t *testing.T) {
	var searches atomic.Int32
	a := newTemplateGroupApplier(groupSearchExecutor(t, &searches), true, map[string]bool{"org\x00dev": true})
	var recs []json.RawMessage
	if !a.groupResolvable(context.Background(), testGroupTemplate, "dev", "dev", false, &recs) {
		t.Error("group in createGroups must be resolvable")
	}
	if searches.Load() != 0 {
		t.Error("created group must not be looked up")
	}
}

func TestGroupResolvableNotCreatedWithoutByName(t *testing.T) {
	var searches atomic.Int32
	a := newTemplateGroupApplier(groupSearchExecutor(t, &searches), false, map[string]bool{})
	var recs []json.RawMessage
	if a.groupResolvable(context.Background(), testGroupTemplate, "ghost", "ghost", false, &recs) {
		t.Error("unknown group without by-name lookup must not be resolvable")
	}
	if len(recs) != 0 || len(a.unresolved) != 0 || searches.Load() != 0 {
		t.Errorf("byName=false must neither record nor query: %v, %d searches", recs, searches.Load())
	}
}

func TestGroupResolvableByNameFound(t *testing.T) {
	var searches atomic.Int32
	a := newTemplateGroupApplier(groupSearchExecutor(t, &searches), true, map[string]bool{})
	var recs []json.RawMessage
	if !a.groupResolvable(context.Background(), testGroupTemplate, "present", "present", false, &recs) {
		t.Error("group found on target must be resolvable")
	}
	if len(recs) != 0 {
		t.Errorf("found group must not be recorded: %v", recs)
	}
}

func TestGroupResolvableByNameMissingRecordedOnce(t *testing.T) {
	var searches atomic.Int32
	a := newTemplateGroupApplier(groupSearchExecutor(t, &searches), true, map[string]bool{})
	var recs []json.RawMessage
	for i := 0; i < 3; i++ { // the same pair surfaced by both feeds
		if a.groupResolvable(context.Background(), testGroupTemplate, "ghost", "ghost", false, &recs) {
			t.Fatal("missing group must not be resolvable")
		}
	}
	if len(recs) != 1 {
		t.Fatalf("expected exactly one unresolved record (dedup), got %d", len(recs))
	}
	if !strings.Contains(string(recs[0]), `"reason":"missing"`) || !strings.Contains(string(recs[0]), `"group":"ghost"`) {
		t.Errorf("unexpected record: %s", recs[0])
	}
	if !a.unresolved[templateGroupPairKey{"tpl", "ghost"}] {
		t.Error("pair must be marked unresolved")
	}
}

func TestGroupResolvableByNameLookupFailure(t *testing.T) {
	var searches atomic.Int32
	a := newTemplateGroupApplier(groupSearchExecutor(t, &searches), true, map[string]bool{})
	var recs []json.RawMessage
	if a.groupResolvable(context.Background(), testGroupTemplate, "broken", "broken", false, &recs) {
		t.Fatal("failed lookup must not be resolvable")
	}
	if len(recs) != 1 || !strings.Contains(string(recs[0]), `"reason":"lookup_failed"`) {
		t.Errorf("expected lookup_failed record, got %v", recs)
	}
}

func TestUnresolvedTemplateGroupRecord(t *testing.T) {
	for st, want := range map[entityState]string{entityMissing: "missing", entityError: "lookup_failed"} {
		var m map[string]string
		if err := json.Unmarshal(unresolvedTemplateGroupRecord("tpl", "org", "g", st), &m); err != nil {
			t.Fatal(err)
		}
		if m["reason"] != want || m["cloud_template_id"] != "tpl" || m["sonarcloud_org_key"] != "org" || m["group"] != "g" {
			t.Errorf("state %v: got %v", st, m)
		}
	}
}

func TestWriteUnresolvedTemplateGroups(t *testing.T) {
	e := newPermissionsTest(t)
	if err := writeUnresolvedTemplateGroups(e, nil); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if e.Store.TaskDirExists(templateGroupUnresolvedKey) {
		t.Error("nothing must be written when every group resolved")
	}
	recs := []json.RawMessage{json.RawMessage(`{"group":"b"}`), json.RawMessage(`{"group":"a"}`)}
	if err := writeUnresolvedTemplateGroups(e, recs); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := e.Store.ReadAll(templateGroupUnresolvedKey)
	if err != nil || len(got) != 2 {
		t.Fatalf("ReadAll = %v, %v", got, err)
	}
	if string(got[0]) != `{"group":"a"}` {
		t.Errorf("records must be sorted, got %s first", got[0])
	}
}

func TestWriteUnresolvedTemplateGroupsWriterError(t *testing.T) {
	e := newPermissionsTest(t)
	// Make the store root unusable so the writer cannot create its directory.
	root := t.TempDir()
	blocker := filepath.Join(root, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e2 := *e
	e2.Store = common.NewDataStore(filepath.Join(blocker, "sub"))
	err := writeUnresolvedTemplateGroups(&e2, []json.RawMessage{json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), templateGroupUnresolvedKey) {
		t.Errorf("expected writer error naming the key, got %v", err)
	}
}

func TestBuildTemplateGroupHelpers(t *testing.T) {
	templates := []json.RawMessage{
		json.RawMessage(`{"server_url":"s","source_template_key":"a","cloud_template_id":"c","sonarcloud_org_key":"o"}`),
		json.RawMessage(`{"server_url":"","source_template_key":"b"}`),
		json.RawMessage(`{"server_url":"s","source_template_key":""}`),
	}
	m := buildTemplateGroupPermTemplateMap(templates)
	if len(m) != 1 || m["s\x00a"] != (templateGroupRef{cloudID: "c", org: "o"}) {
		t.Errorf("template map = %v", m)
	}
	groups := []json.RawMessage{
		json.RawMessage(`{"name":"g","sonarcloud_org_key":"o"}`),
		json.RawMessage(`{"name":"","sonarcloud_org_key":"o"}`),
		json.RawMessage(`{"name":"h","sonarcloud_org_key":""}`),
	}
	ex := buildTemplateGroupExistsSet(groups)
	if len(ex) != 1 || !ex["o\x00g"] {
		t.Errorf("exists set = %v", ex)
	}
}

func TestTemplateGroupAlreadyAppliedAndPairStat(t *testing.T) {
	e := newPermissionsTest(t)
	a := newTemplateGroupApplier(e, false, nil)
	tmpl := templateGroupRef{cloudID: "tpl", org: "org"}

	if a.alreadyApplied(tmpl, "g", "user") {
		t.Error("first sight must not be reported as applied")
	}
	if !a.alreadyApplied(tmpl, "g", "user") {
		t.Error("second sight must be reported as applied")
	}
	if a.alreadyApplied(tmpl, "g", "scan") {
		t.Error("different permission is a different triple")
	}

	s1 := a.pairStat(tmpl, "g")
	s1.attempted++
	if s2 := a.pairStat(tmpl, "g"); s2 != s1 || s2.attempted != 1 {
		t.Error("pairStat must return the same counter for the same pair")
	}
	if a.pairStat(tmpl, "other") == s1 {
		t.Error("distinct pairs need distinct counters")
	}
}

func TestTemplateGroupApplyPermissionsSkipsBlankAndAdmin(t *testing.T) {
	var adds []string
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/permissions/add_group_to_template", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		adds = append(adds, r.FormValue("groupName")+":"+r.FormValue("permission"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newCustomCloudTest(t, mux)
	a := newTemplateGroupApplier(e, false, nil)
	tmpl := templateGroupRef{cloudID: "tpl", org: "org"}

	err := a.applyPermissions(context.Background(), []string{"", "admin", "user", "user"}, tmpl, testSonarUsers, "Members")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(adds) != 1 || adds[0] != "Members:user" {
		t.Errorf("adds = %v, want only Members:user (blank skipped, admin withheld, duplicate deduped)", adds)
	}
	if st := a.pairStats[templateGroupPairKey{"tpl", "Members"}]; st == nil || st.attempted != 1 || st.succeeded != 1 {
		t.Errorf("pair stats = %+v", st)
	}
}

func TestRunSetTemplateGroupPermissionsAllGrantsFail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/permissions/add_group_to_template", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	e := newCustomCloudTest(t, mux)
	setupTemplateGroupExtract(t, e)

	err := runSetTemplateGroupPermissions(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "received no permissions at all") {
		t.Fatalf("expected total-failure error, got %v", err)
	}
	if !strings.Contains(err.Error(), "group developers on template tpl-cloud-1") {
		t.Errorf("error should name the pair, got %v", err)
	}
}

// When createGroups never ran, groups are resolved by name on the target; a
// group the target lacks is reported once in the unresolved file.
func TestRunSetTemplateGroupPermissionsUnresolvedByName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/user_groups/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("q") == "developers" {
			_, _ = w.Write([]byte(`{"paging":{"total":1},"groups":[{"id":"1","name":"developers"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"paging":{"total":0},"groups":[]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	e := newCustomCloudTest(t, mux)
	writeTaskJSONL(t, e, "createPermissionTemplates", []map[string]any{
		{"server_url": testServerURL, "source_template_key": "tpl-src-1",
			"cloud_template_id": "tpl-cloud-1", "sonarcloud_org_key": "cloud-org"},
	})
	writeJSONL(extractPath(e, "getTemplateGroupsScanners"), []map[string]any{
		{"templateId": "tpl-src-1", "name": "developers", "permissions": []string{"user"}, "serverUrl": testServerURL},
		{"templateId": "tpl-src-1", "name": "ghost", "permissions": []string{"user"}, "serverUrl": testServerURL},
	})
	writeJSONL(extractPath(e, "getTemplateGroupsViewers"), []map[string]any{
		{"templateId": "tpl-src-1", "name": "ghost", "permissions": []string{"scan"}, "serverUrl": testServerURL},
	})

	if err := runSetTemplateGroupPermissions(context.Background(), e); err != nil {
		t.Fatalf("runSetTemplateGroupPermissions: %v", err)
	}
	recs, err := e.Store.ReadAll(templateGroupUnresolvedKey)
	if err != nil || len(recs) != 1 || !strings.Contains(string(recs[0]), `"group":"ghost"`) {
		t.Fatalf("expected one unresolved ghost record, got %v (err %v)", recs, err)
	}
}

// A feed that fails (here: the run context is already cancelled, so no item
// is admitted) must not abort the other feed; the task reports the failures
// once both have run (#551).
func TestRunSetTemplateGroupPermissionsFeedError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	e := newCustomCloudTest(t, mux)
	setupTemplateGroupExtract(t, e)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runSetTemplateGroupPermissions(ctx, e)
	if err == nil || !strings.HasPrefix(err.Error(), "setTemplateGroupPermissions:") {
		t.Fatalf("expected aggregated feed error, got %v", err)
	}
}

// grantRecordingExecutor returns an Executor whose target accepts every
// add_group_to_template call and records it as "group:permission".
func grantRecordingExecutor(t *testing.T) (*Executor, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var grants []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/permissions/add_group_to_template", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		grants = append(grants, r.FormValue("groupName")+":"+r.FormValue("permission"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return newCustomCloudTest(t, mux), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), grants...)
	}
}

// newApplyGuardApplier builds an applier with a populated template map,
// one created group ("dev") and one skipped group ("skipped").
func newApplyGuardApplier(e *Executor) *templateGroupPermApplier {
	a := newTemplateGroupApplier(e, false, map[string]bool{"org\x00dev": true})
	a.templateMap = map[string]templateGroupRef{
		"srv\x00tplA":    {cloudID: "tpl", org: "org"},
		"srv\x00noCloud": {cloudID: "", org: "org"},
		"srv\x00noOrg":   {cloudID: "tpl", org: ""},
	}
	a.skipGroups = map[string]bool{"skipped": true}
	return a
}

func TestTemplateGroupApplierApply(t *testing.T) {
	cases := []struct {
		name string
		row  string
		want []string
	}{
		{"blank template id", `{"templateId":"","name":"dev","permissions":["user"]}`, nil},
		{"blank group name", `{"templateId":"tplA","name":"","permissions":["user"]}`, nil},
		{"skipped group", `{"templateId":"tplA","name":"skipped","permissions":["user"]}`, nil},
		{"unknown template", `{"templateId":"other","name":"dev","permissions":["user"]}`, nil},
		{"template without cloud id", `{"templateId":"noCloud","name":"dev","permissions":["user"]}`, nil},
		{"template without org", `{"templateId":"noOrg","name":"dev","permissions":["user"]}`, nil},
		{"group not created and no by-name lookup", `{"templateId":"tplA","name":"ghost","permissions":["user"]}`, nil},
		{"created group", `{"templateId":"tplA","name":"dev","permissions":["user","scan"]}`, []string{"dev:user", "dev:scan"}},
		{"aliased built-in group", `{"templateId":"tplA","name":"` + testSonarUsers + `","permissions":["user"]}`, []string{"Members:user"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertTemplateGroupApply(t, tc.row, tc.want)
		})
	}
}

// assertTemplateGroupApply runs apply for one feed row and checks the
// grants it issued.
func assertTemplateGroupApply(t *testing.T, row string, want []string) {
	t.Helper()
	e, grants := grantRecordingExecutor(t)
	a := newApplyGuardApplier(e)
	var recs []json.RawMessage
	if err := a.apply(context.Background(), "srv", json.RawMessage(row), &recs); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := grants()
	if len(got) != len(want) {
		t.Fatalf("grants = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("grants = %v, want %v", got, want)
			break
		}
	}
	if len(recs) != 0 {
		t.Errorf("no unresolved record expected, got %v", recs)
	}
}
