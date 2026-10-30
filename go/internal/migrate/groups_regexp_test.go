// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"os"
	"regexp"
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
