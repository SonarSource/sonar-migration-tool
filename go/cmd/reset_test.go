// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newResetTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "reset"}
	f := cmd.Flags()
	f.String("config", "", "")
	f.String("edition", "enterprise", "")
	f.String(flagTargetURL, "https://sonarcloud.io/", "")
	f.Int("concurrency", 25, "")
	f.String("export_directory", "/app/files/", "")
	f.Bool("debug", false, "")
	// Deprecated alias — registered so tests can exercise back-compat (#406).
	f.String("url", "https://sonarcloud.io/", "")
	return cmd
}

func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "reset-cfg-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(contents); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func TestBuildResetConfigFromConfigFile(t *testing.T) {
	path := writeConfigFile(t, `{
		"token": "cfg-token",
		"enterprise_key": "cfg-ent",
		"url": "https://cfg.example.com/",
		"export_directory": "/cfg/files",
		"concurrency": 7
	}`)

	cmd := newResetTestCmd()
	if err := cmd.Flags().Set("config", path); err != nil {
		t.Fatal(err)
	}

	cfg, err := buildResetConfig(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Token != "cfg-token" {
		t.Errorf("Token: got %q want cfg-token", cfg.Token)
	}
	if cfg.EnterpriseKey != "cfg-ent" {
		t.Errorf("EnterpriseKey: got %q want cfg-ent", cfg.EnterpriseKey)
	}
	if cfg.URL != "https://cfg.example.com/" {
		t.Errorf("URL: got %q", cfg.URL)
	}
	if cfg.ExportDirectory != "/cfg/files" {
		t.Errorf("ExportDirectory: got %q", cfg.ExportDirectory)
	}
	if cfg.Concurrency != 7 {
		t.Errorf("Concurrency: got %d want 7", cfg.Concurrency)
	}
}

func TestBuildResetConfigFlagsOverrideConfig(t *testing.T) {
	path := writeConfigFile(t, `{
		"token": "cfg-token",
		"enterprise_key": "cfg-ent",
		"url": "https://cfg.example.com/",
		"concurrency": 7
	}`)

	cmd := newResetTestCmd()
	_ = cmd.Flags().Set("config", path)
	_ = cmd.Flags().Set(flagTargetURL, "https://flag.example.com/")
	_ = cmd.Flags().Set("concurrency", "99")
	_ = cmd.Flags().Set("debug", "true")

	cfg, err := buildResetConfig(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.URL != "https://flag.example.com/" {
		t.Errorf("URL: flag should win, got %q", cfg.URL)
	}
	if cfg.Concurrency != 99 {
		t.Errorf("Concurrency: flag should win, got %d", cfg.Concurrency)
	}
	if !cfg.Debug {
		t.Errorf("Debug: flag should win (true)")
	}
	if cfg.Token != "cfg-token" || cfg.EnterpriseKey != "cfg-ent" {
		t.Errorf("config-file values should persist when no flag/arg overrides them, got token=%q ent=%q",
			cfg.Token, cfg.EnterpriseKey)
	}
}

func TestBuildResetConfigPositionalArgsOverrideConfig(t *testing.T) {
	path := writeConfigFile(t, `{
		"token": "cfg-token",
		"enterprise_key": "cfg-ent"
	}`)

	cmd := newResetTestCmd()
	_ = cmd.Flags().Set("config", path)

	cfg, err := buildResetConfig(cmd, []string{"arg-token", "arg-ent"})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Token != "arg-token" {
		t.Errorf("Token: positional should win, got %q", cfg.Token)
	}
	if cfg.EnterpriseKey != "arg-ent" {
		t.Errorf("EnterpriseKey: positional should win, got %q", cfg.EnterpriseKey)
	}
}

func TestBuildResetConfigNoConfigUsesPositionalAndFlags(t *testing.T) {
	cmd := newResetTestCmd()
	_ = cmd.Flags().Set(flagTargetURL, "https://manual.example.com/")
	_ = cmd.Flags().Set("export_directory", "/manual/dir")

	cfg, err := buildResetConfig(cmd, []string{"tok", "ent"})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Token != "tok" {
		t.Errorf("Token: got %q", cfg.Token)
	}
	if cfg.EnterpriseKey != "ent" {
		t.Errorf("EnterpriseKey: got %q", cfg.EnterpriseKey)
	}
	if cfg.URL != "https://manual.example.com/" {
		t.Errorf("URL: got %q", cfg.URL)
	}
	if cfg.ExportDirectory != "/manual/dir" {
		t.Errorf("ExportDirectory: got %q", cfg.ExportDirectory)
	}
}

// Issue #406: the deprecated --url flag must still work so existing scripts
// don't break. The new --target_url wins when both are passed.
func TestBuildResetConfig_DeprecatedURLStillWorks(t *testing.T) {
	cmd := newResetTestCmd()
	_ = cmd.Flags().Set("url", "https://legacy.example.com/")
	cfg, err := buildResetConfig(cmd, []string{"tok", "ent"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://legacy.example.com/" {
		t.Errorf("deprecated --url should populate cfg.URL, got %q", cfg.URL)
	}
}

func TestBuildResetConfig_NewURLWinsOverDeprecated(t *testing.T) {
	cmd := newResetTestCmd()
	_ = cmd.Flags().Set("url", "https://legacy.example.com/")
	_ = cmd.Flags().Set(flagTargetURL, "https://new.example.com/")
	cfg, err := buildResetConfig(cmd, []string{"tok", "ent"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://new.example.com/" {
		t.Errorf("--target_url should win over deprecated --url, got %q", cfg.URL)
	}
}

func TestBuildResetConfigShape3FromExampleFile(t *testing.T) {
	// Round-trip a side-sectioned config via buildResetConfig. The shape is
	// no longer shipped as an example file (examples/ was consolidated in
	// #408), so the fixture is inline; the parser still supports the shape.
	const sideSectioned = `{
  "sonarqube": { "url": "http://localhost:9000", "token": "x" },
  "sonarcloud": {
    "url": "https://sonarcloud.io/",
    "token": "YOUR_SONARCLOUD_ADMIN_TOKEN_HERE",
    "enterprise_key": "YOUR_ENTERPRISE_KEY_HERE",
    "org_key": "YOUR_TARGET_ORGANIZATION_KEY_HERE"
  },
  "settings": { "export_directory": "./files", "concurrency": 10, "timeout": 60 }
}`
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(sideSectioned), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newResetTestCmd()
	_ = cmd.Flags().Set("config", path)

	cfg, err := buildResetConfig(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Token != "YOUR_SONARCLOUD_ADMIN_TOKEN_HERE" {
		t.Errorf("Token: got %q", cfg.Token)
	}
	if cfg.EnterpriseKey != "YOUR_ENTERPRISE_KEY_HERE" {
		t.Errorf("EnterpriseKey: got %q", cfg.EnterpriseKey)
	}
	if cfg.URL != "https://sonarcloud.io/" {
		t.Errorf("URL: got %q", cfg.URL)
	}
	if cfg.ExportDirectory != "./files" {
		t.Errorf("ExportDirectory: got %q", cfg.ExportDirectory)
	}
	if cfg.Concurrency != 10 {
		t.Errorf("Concurrency: got %d", cfg.Concurrency)
	}
}

// #381: confirmResetOrgs gates the destructive reset behind an
// interactive prompt. The helper must:
//   - list every mapped cloud org with its project count;
//   - accept whitespace-separated org keys (collapsing multiple spaces);
//   - return (nil, nil) on empty input / EOF, signalling a clean abort;
//   - error clearly when a typed key isn't in the displayed set;
//   - dedup typed keys; and
//   - skip the prompt entirely when autoYes is true.

func writeResetFixture(t *testing.T, orgsCSV, projectsCSV string) string {
	t.Helper()
	dir := t.TempDir()
	if orgsCSV != "" {
		if err := os.WriteFile(filepath.Join(dir, "organizations.csv"), []byte(orgsCSV), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if projectsCSV != "" {
		if err := os.WriteFile(filepath.Join(dir, "projects.csv"), []byte(projectsCSV), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestConfirmResetOrgs_HappyPathSubset(t *testing.T) {
	// #381 follow-up: project counts in the prompt are sourced from
	// prior migrate runs' createProjects JSONL (not the structure
	// phase's organizations.csv count) so the displayed number
	// matches exactly what reset will delete.
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\norg2,cloud-b\norg3,cloud-c\n", "")
	seedResetMigrateRun(t, dir, "run-01", []map[string]any{
		{"cloud_project_key": "p1", "sonarcloud_org_key": "cloud-a"},
		{"cloud_project_key": "p2", "sonarcloud_org_key": "cloud-a"},
		{"cloud_project_key": "p3", "sonarcloud_org_key": "cloud-b"},
		// cloud-c has no migrate-created projects → 0.
	})

	var out bytes.Buffer
	in := strings.NewReader("cloud-a cloud-b\n")
	got, err := confirmResetOrgs(dir, false, "", nil, in, &out)
	if err != nil {
		t.Fatalf("confirmResetOrgs: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"cloud-a", "cloud-b"}) {
		t.Errorf("got %+v, want [cloud-a cloud-b]", got)
	}
	for _, want := range []string{"cloud-a (2 projects)", "cloud-b (1 projects)", "cloud-c (0 projects)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected prompt to contain %q, got:\n%s", want, out.String())
		}
	}
}

// seedResetMigrateRun creates a fake migrate run directory under
// exportDir for the reset tests. Mirrors what real migrate runs look
// like: a run_meta.json marker plus a createProjects/ JSONL output.
func seedResetMigrateRun(t *testing.T, exportDir, runID string, records []map[string]any) {
	t.Helper()
	runDir := filepath.Join(exportDir, runID)
	if err := os.MkdirAll(filepath.Join(runDir, "createProjects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "run_meta.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(runDir, "createProjects", "results.1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range records {
		_ = enc.Encode(r)
	}
}

func TestConfirmResetOrgs_CollapsesWhitespace(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\norg2,cloud-b\n", "")

	got, err := confirmResetOrgs(dir, false, "", nil, strings.NewReader("   cloud-a    cloud-b   \n"), io.Discard)
	if err != nil {
		t.Fatalf("confirmResetOrgs: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"cloud-a", "cloud-b"}) {
		t.Errorf("got %+v, want [cloud-a cloud-b]", got)
	}
}

func TestConfirmResetOrgs_EmptyEnterAborts(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\n", "")

	var out bytes.Buffer
	got, err := confirmResetOrgs(dir, false, "", nil, strings.NewReader("\n"), &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil (abort), got %+v", got)
	}
	if !strings.Contains(out.String(), "Reset aborted.") {
		t.Errorf("expected abort message, got:\n%s", out.String())
	}
}

func TestConfirmResetOrgs_EOFAborts(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\n", "")

	// Empty reader → immediate EOF; treated the same as Enter alone.
	got, err := confirmResetOrgs(dir, false, "", nil, strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil (abort) on EOF, got %+v", got)
	}
}

func TestConfirmResetOrgs_UnknownKeyError(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\n", "")

	_, err := confirmResetOrgs(dir, false, "", nil, strings.NewReader("cloud-a typo-org\n"), io.Discard)
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "typo-org") || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error %q does not mention the typo'd key", err.Error())
	}
}

func TestConfirmResetOrgs_DedupsInput(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\norg2,cloud-b\n", "")

	got, err := confirmResetOrgs(dir, false, "", nil, strings.NewReader("cloud-a cloud-a cloud-b cloud-a\n"), io.Discard)
	if err != nil {
		t.Fatalf("confirmResetOrgs: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"cloud-a", "cloud-b"}) {
		t.Errorf("got %+v, want [cloud-a cloud-b]", got)
	}
}

func TestConfirmResetOrgs_AutoYesSkipsPrompt(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-b\norg2,cloud-a\n", "")

	// Reader returns content that would normally be parsed; autoYes
	// must take precedence and never read from it.
	in := strings.NewReader("this-should-not-be-read\n")
	got, err := confirmResetOrgs(dir, true, "", nil, in, io.Discard)
	if err != nil {
		t.Fatalf("confirmResetOrgs: %v", err)
	}
	// Sorted for deterministic display + return.
	if !reflect.DeepEqual(got, []string{"cloud-a", "cloud-b"}) {
		t.Errorf("got %+v, want sorted [cloud-a cloud-b]", got)
	}
	// Confirm reader untouched (still holds its original payload).
	b, _ := io.ReadAll(in)
	if string(b) == "" {
		t.Error("autoYes must NOT consume the input reader")
	}
}

func TestConfirmResetOrgs_SkipsSkippedSentinel(t *testing.T) {
	// "SKIPPED" cloud org keys (and empties) must NOT be listed as
	// targets — they were already opted out during mapping.
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\norg2,SKIPPED\norg3,\n", "")

	var out bytes.Buffer
	got, err := confirmResetOrgs(dir, true, "", nil, strings.NewReader(""), &out)
	if err != nil {
		t.Fatalf("confirmResetOrgs: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"cloud-a"}) {
		t.Errorf("got %+v, want [cloud-a]", got)
	}
	if strings.Contains(out.String(), "SKIPPED") {
		t.Errorf("SKIPPED placeholder must not appear in the prompt output, got:\n%s", out.String())
	}
}

func TestConfirmResetOrgs_EmptyOrgsCSVErrors(t *testing.T) {
	// No organizations.csv → cannot continue.
	dir := t.TempDir()
	_, err := confirmResetOrgs(dir, false, "", nil, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("expected error when organizations.csv is missing")
	}
}

// #550: --organization narrows the candidate org list to those whose
// key fully matches the pattern, BEFORE the prompt / --yes branch ever
// runs — mirroring transfer's --project_key anchored-regex tests
// (TestResolveTransferProjectKeys_PatternMatchesAnchored et al.).

func TestConfirmResetOrgs_OrganizationFilterNarrowsCandidates(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\norg2,cloud-b\norg3,cloud-c\n", "")

	var out bytes.Buffer
	got, err := confirmResetOrgs(dir, true, "cloud-a|cloud-b", nil, strings.NewReader(""), &out)
	if err != nil {
		t.Fatalf("confirmResetOrgs: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"cloud-a", "cloud-b"}) {
		t.Errorf("got %+v, want [cloud-a cloud-b]", got)
	}
	if strings.Contains(out.String(), "cloud-c") {
		t.Errorf("cloud-c should have been filtered out by --organization before display, got:\n%s", out.String())
	}
}

// A pattern is always compiled as a full-match anchored regex (#529's
// convention, replicated for --organization): "cloud-a" must not also
// match "cloud-ab" as a substring.
func TestConfirmResetOrgs_OrganizationFilterIsAnchored(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\norg2,cloud-ab\n", "")

	got, err := confirmResetOrgs(dir, true, "cloud-a", nil, strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("confirmResetOrgs: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"cloud-a"}) {
		t.Errorf("got %+v, want [cloud-a] (anchored match must exclude cloud-ab)", got)
	}
}

func TestConfirmResetOrgs_OrganizationFilterInvalidRegexErrors(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\n", "")

	_, err := confirmResetOrgs(dir, true, "(unterminated", nil, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("expected error for invalid --organization regex")
	}
}

func TestConfirmResetOrgs_OrganizationFilterNoMatchErrors(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\n", "")

	_, err := confirmResetOrgs(dir, true, "no-such-org", nil, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("expected error when --organization matches no mapped org")
	}
	if !strings.Contains(err.Error(), "no-such-org") {
		t.Errorf("error %q should name the pattern", err.Error())
	}
}

// #550: the --organization filter also narrows the interactive prompt's
// candidate list, not just the --yes shortcut.
func TestConfirmResetOrgs_OrganizationFilterAppliesToInteractivePrompt(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\norg2,cloud-b\n", "")

	// Typing the filtered-out org must be treated as unknown, since it
	// was never displayed as a candidate.
	_, err := confirmResetOrgs(dir, false, "cloud-a", nil, strings.NewReader("cloud-b\n"), io.Discard)
	if err == nil {
		t.Fatal("expected error: cloud-b was filtered out by --organization and should be unknown")
	}
}

// #550: when --yes is used, --organization is absent, and the config
// file already supplied confirmed_orgs, the config-file selection wins
// over "reset every mapped org."
func TestConfirmResetOrgs_PresetOrgsUsedWithYesWhenNoOrganizationFlag(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\norg2,cloud-b\norg3,cloud-c\n", "")

	got, err := confirmResetOrgs(dir, true, "", []string{"cloud-a", "cloud-c"}, strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("confirmResetOrgs: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"cloud-a", "cloud-c"}) {
		t.Errorf("got %+v, want [cloud-a cloud-c]", got)
	}
}

// --organization takes precedence over a config-file confirmed_orgs
// preset when both are present.
func TestConfirmResetOrgs_OrganizationFlagWinsOverPresetOrgs(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\norg2,cloud-b\n", "")

	got, err := confirmResetOrgs(dir, true, "cloud-b", []string{"cloud-a"}, strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("confirmResetOrgs: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"cloud-b"}) {
		t.Errorf("got %+v, want [cloud-b] (--organization should win over config confirmed_orgs)", got)
	}
}

func TestConfirmResetOrgs_PresetOrgsUnknownKeyErrors(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\n", "")

	_, err := confirmResetOrgs(dir, true, "", []string{"cloud-a", "typo-org"}, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("expected error for unknown confirmed_orgs key from config file")
	}
	if !strings.Contains(err.Error(), "typo-org") {
		t.Errorf("error %q does not mention the typo'd key", err.Error())
	}
}

// presetOrgs only applies to the --yes shortcut; the interactive prompt
// must still ask, ignoring any config-file preset.
func TestConfirmResetOrgs_PresetOrgsIgnoredWhenNotAutoYes(t *testing.T) {
	dir := writeResetFixture(t,
		"sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\norg2,cloud-b\n", "")

	got, err := confirmResetOrgs(dir, false, "", []string{"cloud-a"}, strings.NewReader("cloud-b\n"), io.Discard)
	if err != nil {
		t.Fatalf("confirmResetOrgs: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"cloud-b"}) {
		t.Errorf("got %+v, want [cloud-b] (interactive prompt wins; presetOrgs only applies to --yes)", got)
	}
}
