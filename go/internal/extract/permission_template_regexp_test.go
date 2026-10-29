// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestKeepTemplateByRegexp(t *testing.T) {
	re, err := CompileProjectKeyPattern("Team-A.*")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		e    *Executor
		item string
		want bool
	}{
		{"nil filter keeps everything", &Executor{}, `{"name":"anything"}`, true},
		{"match kept", &Executor{PermissionTemplateRe: re}, `{"name":"Team-A default"}`, true},
		{"non-match dropped", &Executor{PermissionTemplateRe: re}, `{"name":"Team-B default"}`, false},
		{"full match, not substring", &Executor{PermissionTemplateRe: re}, `{"name":"My Team-A"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keepTemplateByRegexp(c.e, json.RawMessage(c.item)); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// #665: a permission template slice is a partial extract, so readers merge it
// onto the earlier slices instead of replacing them.
func TestIsPartialExtract_PermissionTemplateRegexp(t *testing.T) {
	if !isPartialExtract(ExtractConfig{PermissionTemplateRegexp: "A.*"}) {
		t.Error("a permission template slice must be a partial extract")
	}
}

func TestIsTemplateScopedTask(t *testing.T) {
	for _, name := range []string{"getTemplates", "getTemplateGroupsScanners", "getTemplateGroupsViewers",
		"getTemplateUsersScanners", "getTemplateUsersViewers"} {
		if !IsTemplateScopedTask(name) {
			t.Errorf("%s must be template-scoped", name)
		}
	}
	if IsTemplateScopedTask("getDefaultTemplates") {
		t.Error("getDefaultTemplates is never filtered, so it must stay server-wide")
	}
	// Every registered template task fanning out from getTemplates must be
	// covered, so a new one cannot silently be unioned with duplicates.
	for _, def := range templateTasks() {
		if def.Name != "getDefaultTemplates" && !IsTemplateScopedTask(def.Name) {
			t.Errorf("%s is a template task but not template-scoped", def.Name)
		}
	}
}

func loadPTConfig(t *testing.T, body string) ExtractConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadExtractConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func TestLoadExtractConfigFilePermissionTemplateRegexp(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"flat", `{"url":"u","token":"t","permission_template_regexp":"A.*"}`, "A.*"},
		{"flat alias", `{"url":"u","token":"t","pt_regexp":"B.*"}`, "B.*"},
		{"unified top level", `{"permission_template_regexp":"top.*","source":{"url":"u","token":"t"}}`, "top.*"},
		{"unified source wins", `{"permission_template_regexp":"top.*","source":{"url":"u","token":"t","permission_template_regexp":"src.*"}}`, "src.*"},
		{"unified source alias wins", `{"pt_regexp":"top.*","source":{"url":"u","token":"t","pt_regexp":"src.*"}}`, "src.*"},
		{"sectioned outer wins", `{"permission_template_regexp":"outer.*","extract":{"url":"u","token":"t","permission_template_regexp":"inner.*"}}`, "outer.*"},
		{"sectioned inner", `{"extract":{"url":"u","token":"t","permission_template_regexp":"inner.*"}}`, "inner.*"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := loadPTConfig(t, c.body).PermissionTemplateRegexp; got != c.want {
				t.Errorf("PermissionTemplateRegexp: got %q, want %q", got, c.want)
			}
		})
	}
}
