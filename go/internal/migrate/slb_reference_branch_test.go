// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"sync"
	"testing"
)

func TestLoadMigrateConfigFile_UseSLBForReferenceBranchNewCode(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"absent", `{"target": {"url": "u", "token": "t"}}`, false},
		{"target true", `{"target": {"url": "u", "token": "t", "use_slb_for_reference_branch_new_code": true}}`, true},
		{"top level true", `{"use_slb_for_reference_branch_new_code": true, "target": {"url": "u", "token": "t"}}`, true},
		{"target false wins", `{"use_slb_for_reference_branch_new_code": true, "target": {"url": "u", "token": "t", "use_slb_for_reference_branch_new_code": false}}`, false},
		{"flat true", `{"url": "u", "token": "t", "use_slb_for_reference_branch_new_code": "yes"}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := histCfgLoad(t, c.body)
			if cfg.UseSLBForReferenceBranchNewCode != c.want {
				t.Errorf("UseSLBForReferenceBranchNewCode = %v, want %v", cfg.UseSLBForReferenceBranchNewCode, c.want)
			}
			if cfg.MigrateHistory {
				t.Errorf("MigrateHistory must not be set by this option")
			}
		})
	}
}

func TestReferenceBranchNCDBranches(t *testing.T) {
	dir := t.TempDir()
	e := newTestExecutor(httptest.NewServer(http.NotFoundHandler()), httptest.NewServer(http.NotFoundHandler()), dir)
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)
	writeExtractTaskJSONL(t, dir, "extract-01", "getNewCodePeriods", []map[string]any{
		{"projectKey": "p", "branchKey": "", "type": "PREVIOUS_VERSION", "serverUrl": testServerURL},
		{"projectKey": "p", "branchKey": "feat-a", "type": "REFERENCE_BRANCH", "value": "main", "serverUrl": testServerURL},
		{"projectKey": "p", "branchKey": "feat-b", "type": "NUMBER_OF_DAYS", "value": "30", "serverUrl": testServerURL},
		{"projectKey": "other", "branchKey": "feat-c", "type": "REFERENCE_BRANCH", "serverUrl": testServerURL},
	})
	branches := []branchInfo{{Name: "main", IsMain: true}, {Name: "feat-a"}, {Name: "feat-b"}, {Name: "feat-c"}}

	got := referenceBranchNCDBranches(e, testServerURL, "p", branches)
	if !reflect.DeepEqual(got, map[string]bool{"feat-a": true}) {
		t.Errorf("branch override: got %v, want only feat-a", got)
	}

	// A project-level reference branch is inherited by branches with no entry
	// of their own, but not by one that overrides it.
	writeExtractTaskJSONL(t, dir, "extract-01", "getNewCodePeriods", []map[string]any{
		{"projectKey": "p", "branchKey": "", "type": "REFERENCE_BRANCH", "value": "main", "serverUrl": testServerURL},
		{"projectKey": "p", "branchKey": "feat-b", "type": "NUMBER_OF_DAYS", "value": "30", "serverUrl": testServerURL},
	})
	got = referenceBranchNCDBranches(e, testServerURL, "p", branches)
	names := make([]string, 0, len(got))
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"feat-a", "feat-c", "main"}) {
		t.Errorf("inherited: got %v", names)
	}

	// The main branch is never marked short-lived.
	marked := markShortLivedBranches(branches, got)
	if !reflect.DeepEqual(marked, []string{"feat-a", "feat-c"}) {
		t.Errorf("markShortLivedBranches = %v, want [feat-a feat-c]", marked)
	}
	if branches[0].ShortLived {
		t.Error("main branch must never be short-lived")
	}
}

func TestPatternMatchesAny(t *testing.T) {
	if !patternMatchesAny("(branch|release)-.*", []string{"release-1.0"}) {
		t.Error("default pattern should match release-1.0")
	}
	if patternMatchesAny("(branch|release)-.*", []string{"feat-a"}) {
		t.Error("default pattern should not match feat-a")
	}
	if patternMatchesAny("feat", []string{"feat-a"}) {
		t.Error("pattern is a full match, not a substring match")
	}
	if !patternMatchesAny("(", []string{"x"}) {
		t.Error("an uncompilable pattern must be treated as matching")
	}
}

// settingsServer fakes the project settings endpoints and records the calls.
type settingsServer struct {
	mu    sync.Mutex
	calls []string
	// current is the value api/settings/values reports; "" means unset.
	current   string
	inherited bool
}

func (s *settingsServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/settings/values", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		resp := map[string]any{"settings": []map[string]any{}}
		if s.current != "" {
			resp["settings"] = []map[string]any{{"key": sqcBranchRegexSetting, "value": s.current, "inherited": s.inherited}}
		}
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		s.mu.Lock()
		s.calls = append(s.calls, "set:"+url.QueryEscape(r.PostForm.Get("value")))
		s.mu.Unlock()
	})
	mux.HandleFunc("/api/settings/reset", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		s.mu.Lock()
		s.calls = append(s.calls, "reset:"+r.PostForm.Get("keys"))
		s.mu.Unlock()
	})
	return mux
}

func TestShortLivedPatternGuard(t *testing.T) {
	cases := []struct {
		name      string
		current   string
		inherited bool
		names     []string
		want      []string
	}{
		{"default pattern does not match: untouched", "", false, []string{"feat-a"}, nil},
		{"explicit pattern does not match: untouched", "main|develop", false, []string{"feat-a"}, nil},
		{"default pattern matches: reset after", "", false, []string{"branch-a"},
			[]string{"set:" + url.QueryEscape(neverMatchBranchRegex), "reset:" + sqcBranchRegexSetting}},
		{"own pattern matches: restored to its value", ".*", false, []string{"feat-a"},
			[]string{"set:" + url.QueryEscape(neverMatchBranchRegex), "set:" + url.QueryEscape(".*")}},
		{"inherited pattern matches: reset after", ".*", true, []string{"feat-a"},
			[]string{"set:" + url.QueryEscape(neverMatchBranchRegex), "reset:" + sqcBranchRegexSetting}},
		{"no short-lived branches: untouched", ".*", false, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &settingsServer{current: c.current, inherited: c.inherited}
			srv := httptest.NewServer(fake.handler())
			defer srv.Close()
			e := newTestExecutor(srv, srv, t.TempDir())

			restore := shortLivedPatternGuard(t.Context(), e, "org_proj", c.names)
			restore()

			if !reflect.DeepEqual(fake.calls, c.want) {
				t.Errorf("calls = %v, want %v", fake.calls, c.want)
			}
		})
	}
}
