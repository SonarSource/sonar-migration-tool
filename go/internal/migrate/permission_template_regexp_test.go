// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestKeepTemplateByRegexp(t *testing.T) {
	re := regexp.MustCompile("^(?:Team-A.*)$")
	cases := []struct {
		name string
		e    *Executor
		item string
		want bool
	}{
		{"nil filter keeps everything", &Executor{}, `{"name":"anything"}`, true},
		{"match kept", &Executor{PermissionTemplateRe: re}, `{"name":"Team-A default"}`, true},
		{"non-match dropped", &Executor{PermissionTemplateRe: re}, `{"name":"Team-B default"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keepTemplateByRegexp(c.e, json.RawMessage(c.item)); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestLoadMigrateConfigFilePermissionTemplateRegexp(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"flat", `{"url":"u","token":"t","enterprise_key":"e","permission_template_regexp":"A.*"}`, "A.*"},
		{"flat alias", `{"url":"u","token":"t","enterprise_key":"e","pt_regexp":"B.*"}`, "B.*"},
		{"unified top level", `{"permission_template_regexp":"top.*","target":{"url":"u","token":"t","enterprise_key":"e"}}`, "top.*"},
		{"unified target wins", `{"permission_template_regexp":"top.*","target":{"url":"u","token":"t","enterprise_key":"e","permission_template_regexp":"tgt.*"}}`, "tgt.*"},
		{"unified target alias wins", `{"pt_regexp":"top.*","target":{"url":"u","token":"t","enterprise_key":"e","pt_regexp":"tgt.*"}}`, "tgt.*"},
		{"sectioned outer wins", `{"permission_template_regexp":"outer.*","migrate":{"url":"u","token":"t","enterprise_key":"e","permission_template_regexp":"inner.*"}}`, "outer.*"},
		{"sectioned inner", `{"migrate":{"url":"u","token":"t","enterprise_key":"e","permission_template_regexp":"inner.*"}}`, "inner.*"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cfg.json")
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadMigrateConfigFile(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.PermissionTemplateRegexp != c.want {
				t.Errorf("PermissionTemplateRegexp: got %q, want %q", cfg.PermissionTemplateRegexp, c.want)
			}
		})
	}
}
