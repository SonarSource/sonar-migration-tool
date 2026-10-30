// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestKeepGroupByRegexp(t *testing.T) {
	re := regexp.MustCompile("^team-a-.+$")
	tests := []struct {
		name string
		e    *Executor
		item string
		want bool
	}{
		{"no filter keeps everything", &Executor{}, `{"name":"whatever"}`, true},
		{"matching group kept", &Executor{GroupRe: re}, `{"name":"team-a-devs"}`, true},
		{"non-matching group dropped", &Executor{GroupRe: re}, `{"name":"team-b-devs"}`, false},
		{"Anyone always kept", &Executor{GroupRe: re}, `{"name":"Anyone"}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := keepGroupByRegexp(tc.e, json.RawMessage(tc.item)); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadExtractConfigFileGroupNameRegexp(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"flat", `{"url":"u","token":"t","group_name_regexp":"flat-.+"}`, "flat-.+"},
		{"unified top-level fallback", `{"group_name_regexp":"top-.+","source":{"url":"u","token":"t"}}`, "top-.+"},
		{"unified source wins", `{"group_name_regexp":"top-.+","source":{"url":"u","token":"t","group_name_regexp":"src-.+"}}`, "src-.+"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir() + "/cfg.json"
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadExtractConfigFile(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.GroupNameRegexp != tc.want {
				t.Errorf("GroupNameRegexp: got %q, want %q", cfg.GroupNameRegexp, tc.want)
			}
		})
	}
}

func TestIsGroupScopedTask(t *testing.T) {
	for _, name := range []string{"getGroups", "getUserGroups"} {
		if !IsGroupScopedTask(name) {
			t.Errorf("%s must be group-scoped", name)
		}
	}
	for _, name := range []string{"getUsers", "getProfileGroups", "getProjects"} {
		if IsGroupScopedTask(name) {
			t.Errorf("%s must not be group-scoped", name)
		}
	}
}

func TestIsPartialExtract_GroupNameRegexp(t *testing.T) {
	if !isPartialExtract(ExtractConfig{GroupNameRegexp: "a.*"}) {
		t.Error("a group slice must be a partial extract")
	}
}
