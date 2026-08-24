// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/spf13/cobra"
)

// newProjectListingMockServer serves the endpoints extract.ListAllProjectKeys
// needs (version detection, edition detection, and a paginated,
// unfiltered /api/projects/search), returning the given project keys.
func newProjectListingMockServer(t *testing.T, keys []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/server/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "2025.4.0.12345")
	})
	mux.HandleFunc("GET /api/system/info", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"edition": "developer"})
	})
	mux.HandleFunc("GET /api/projects/search", func(w http.ResponseWriter, r *http.Request) {
		components := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			components = append(components, map[string]any{"key": k, "qualifier": "TRK"})
		}
		json.NewEncoder(w).Encode(map[string]any{
			"paging":     map[string]any{"total": len(keys)},
			"components": components,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newTransferTestCmd mirrors transferCmd's flag set so resolveTransferConfig
// can be exercised in isolation without invoking RunE.
func newTransferTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "transfer"}
	f := cmd.Flags()
	f.StringP(flagConfig, "c", "", "")
	f.String(flagSourceURL, "", "")
	f.String(flagSourceToken, "", "")
	f.String(flagProjectKey, "", "")
	f.String(flagTargetURL, "", "")
	f.String(flagTargetToken, "", "")
	f.String(flagDefaultOrg, "", "")
	f.String(flagEnterpriseKey, "", "")
	f.String(flagEdition, "", "")
	f.String(flagExportDir, "./migration-files/", "")
	f.Int(flagConcurrency, 0, "")
	f.Int(flagTimeout, 0, "")
	f.String(flagPEMFilePath, "", "")
	f.String(flagKeyFilePath, "", "")
	f.String(flagCertPassword, "", "")
	f.Bool(flagDebug, false, "")
	f.Bool(flagFastSync, false, "")
	return cmd
}

func writeTransferConfig(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Issue #295: transfer reads the common unified source/target config
// shape just like extract and migrate do — same loader, same field
// names. No transfer-specific keys.
func TestResolveTransferConfig_UnifiedConfigShape(t *testing.T) {
	path := writeTransferConfig(t, `{
		"concurrency": 7,
		"timeout": 42,
		"export_directory": "/tmp/from-cfg",
		"source": {
			"url": "https://sq.example.com",
			"token": "sq-token",
			"pem_file_path": "/cert/pem",
			"key_file_path": "/cert/key",
			"cert_password": "p4ss"
		},
		"target": {
			"url": "https://sonarcloud.io/",
			"token": "sc-token",
			"enterprise_key": "ent-key",
			"edition": "developer",
			"default_organization": "my-org"
		}
	}`)

	cmd := newTransferTestCmd()
	if err := cmd.ParseFlags([]string{"-c", path}); err != nil {
		t.Fatal(err)
	}

	cfg, err := resolveTransferConfig(cmd)
	if err != nil {
		t.Fatalf("resolveTransferConfig: %v", err)
	}

	want := transferConfig{
		sourceURL:           "https://sq.example.com",
		sourceToken:         "sq-token",
		targetURL:           "https://sonarcloud.io/",
		targetToken:         "sc-token",
		defaultOrganization: "my-org",
		enterpriseKey:       "ent-key",
		edition:             "developer",
		exportDir:           "/tmp/from-cfg",
		concurrency:         7,
		timeout:             42,
		pemFilePath:         "/cert/pem",
		keyFilePath:         "/cert/key",
		certPassword:        "p4ss",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v\nwant %+v", cfg, want)
	}
}

// Issue #295: CLI flags must take precedence over config-file values
// for every parameter, matching the contract documented for the other
// actions.
func TestResolveTransferConfig_CLIOverridesConfig(t *testing.T) {
	path := writeTransferConfig(t, `{
		"concurrency": 7,
		"timeout": 42,
		"export_directory": "/tmp/from-cfg",
		"debug": false,
		"source": {
			"url": "https://from-cfg",
			"token": "cfg-source",
			"pem_file_path": "/cfg/pem",
			"key_file_path": "/cfg/key",
			"cert_password": "cfg-pass"
		},
		"target": {
			"url": "https://from-cfg",
			"token": "cfg-target",
			"enterprise_key": "cfg-ent",
			"edition": "developer",
			"default_organization": "cfg-org"
		}
	}`)

	cmd := newTransferTestCmd()
	args := []string{
		"-c", path,
		"--source_url", "https://cli-source",
		"--source_token", "cli-source-tok",
		"--target_url", "https://cli-target",
		"--target_token", "cli-target-tok",
		"--default_organization", "cli-org",
		"--enterprise_key", "cli-ent",
		"--edition", "community",
		"--export_dir", "/tmp/cli",
		"--concurrency", "11",
		"--timeout", "99",
		"--pem_file_path", "/cli/pem",
		"--key_file_path", "/cli/key",
		"--cert_password", "cli-pass",
		"--debug",
		"--" + flagFastSync,
	}
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}

	cfg, err := resolveTransferConfig(cmd)
	if err != nil {
		t.Fatalf("resolveTransferConfig: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"sourceURL", cfg.sourceURL, "https://cli-source"},
		{"sourceToken", cfg.sourceToken, "cli-source-tok"},
		{"targetURL", cfg.targetURL, "https://cli-target"},
		{"targetToken", cfg.targetToken, "cli-target-tok"},
		{"defaultOrganization", cfg.defaultOrganization, "cli-org"},
		{"enterpriseKey", cfg.enterpriseKey, "cli-ent"},
		{"edition", cfg.edition, "community"},
		{"exportDir", cfg.exportDir, "/tmp/cli"},
		{"concurrency", cfg.concurrency, 11},
		{"timeout", cfg.timeout, 99},
		{"pemFilePath", cfg.pemFilePath, "/cli/pem"},
		{"keyFilePath", cfg.keyFilePath, "/cli/key"},
		{"certPassword", cfg.certPassword, "cli-pass"},
		{"debug", cfg.debug, true},
		{"fastSync", cfg.fastSync, true},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

// Issue #527: --fast_sync loads from the config file when the CLI flag
// is absent, and defaults to false when neither is set.
func TestResolveTransferConfig_FastSync(t *testing.T) {
	path := writeTransferConfig(t, `{
		"fast_sync": true,
		"source": {"url": "https://sq.example.com", "token": "tok"},
		"target": {"url": "https://sonarcloud.io/", "token": "ct", "default_organization": "org"}
	}`)
	cmd := newTransferTestCmd()
	if err := cmd.ParseFlags([]string{"-c", path}); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveTransferConfig(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.fastSync {
		t.Error("expected config file's fast_sync=true to be used")
	}

	// Default: neither flag nor config sets it.
	cmd2 := newTransferTestCmd()
	if err := cmd2.ParseFlags(nil); err != nil {
		t.Fatal(err)
	}
	cfg2, err := resolveTransferConfig(cmd2)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.fastSync {
		t.Error("expected fast_sync to default to false")
	}
}

// Issue #295: when --enterprise_key is absent, it falls back to
// --default_organization so portfolio-less migrations stay one-flag.
func TestResolveTransferConfig_EnterpriseKeyDefaultsToOrg(t *testing.T) {
	cmd := newTransferTestCmd()
	if err := cmd.ParseFlags([]string{
		"--source_url", "https://sq",
		"--source_token", "tok",
		"--target_token", "ct",
		"--default_organization", "my-org",
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveTransferConfig(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.enterpriseKey != "my-org" {
		t.Errorf("enterpriseKey: got %q, want %q", cfg.enterpriseKey, "my-org")
	}
}

// Validation must error if either side is missing credentials or the
// project key is missing (#383), with messages that name the relevant
// CLI flag so users aren't pointed at the retired --sq-* / --sc-*
// names.
func TestValidateTransferConfig_MissingFields(t *testing.T) {
	cases := []struct {
		name   string
		cfg    transferConfig
		errSub string
	}{
		{
			name:   "missing source",
			cfg:    transferConfig{targetToken: "t", defaultOrganization: "o", projectKey: "p"},
			errSub: "--" + flagSourceURL,
		},
		{
			name:   "missing target",
			cfg:    transferConfig{sourceURL: "u", sourceToken: "t", projectKey: "p"},
			errSub: "--" + flagTargetToken,
		},
		{
			name: "missing project key (#383)",
			cfg: transferConfig{
				sourceURL: "u", sourceToken: "t",
				targetToken: "tt", defaultOrganization: "o",
			},
			errSub: "--" + flagProjectKey,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateTransferConfig(c.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !contains(err.Error(), c.errSub) {
				t.Errorf("error %q does not mention %q", err.Error(), c.errSub)
			}
		})
	}
}

// #383: with all required fields present (including project_key),
// validation must pass.
func TestValidateTransferConfig_HappyPath(t *testing.T) {
	cfg := transferConfig{
		sourceURL:           "https://sq.example.com",
		sourceToken:         "sq-tok",
		projectKey:          "my-project",
		targetToken:         "sc-tok",
		defaultOrganization: "my-org",
	}
	if err := validateTransferConfig(cfg); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// #474: --unsupported_languages must be validated before the extract phase
// runs. A typo'd mode silently falling back to the default would transfer data
// the operator asked to skip, after a long extract has already completed.
func TestValidateTransferConfig_UnsupportedLanguages(t *testing.T) {
	base := transferConfig{
		sourceURL:           "https://sq.example.com",
		sourceToken:         "sq-tok",
		projectKey:          "my-project",
		targetToken:         "sc-tok",
		defaultOrganization: "my-org",
	}
	for _, mode := range []string{"", "exclude", "skip", "warn", "SKIP"} {
		cfg := base
		cfg.unsupportedLanguages = mode
		if err := validateTransferConfig(cfg); err != nil {
			t.Errorf("mode %q: expected no error, got %v", mode, err)
		}
	}
	for _, mode := range []string{"skipp", "none", "exclude-all"} {
		cfg := base
		cfg.unsupportedLanguages = mode
		err := validateTransferConfig(cfg)
		if err == nil {
			t.Errorf("mode %q: expected a validation error", mode)
			continue
		}
		if !contains(err.Error(), "--"+flagUnsupportedLanguages) {
			t.Errorf("mode %q: error %q does not name the flag", mode, err.Error())
		}
	}
}

// #529: --project_key is always compiled as an anchored regex; an
// uncompilable pattern must be rejected up front, naming the flag.
func TestValidateTransferConfig_InvalidRegex(t *testing.T) {
	cfg := transferConfig{
		sourceURL:           "https://sq.example.com",
		sourceToken:         "sq-tok",
		projectKey:          "BANKING_(",
		targetToken:         "sc-tok",
		defaultOrganization: "my-org",
	}
	err := validateTransferConfig(cfg)
	if err == nil {
		t.Fatal("expected an error for an uncompilable --project_key pattern")
	}
	if !contains(err.Error(), "--"+flagProjectKey) {
		t.Errorf("error %q does not mention --%s", err.Error(), flagProjectKey)
	}
}

// A plain literal key (the overwhelmingly common case) must still pass
// validation — it's a trivially valid regex that matches only itself.
func TestValidateTransferConfig_PlainKeyIsValidPattern(t *testing.T) {
	cfg := transferConfig{
		sourceURL:           "https://sq.example.com",
		sourceToken:         "sq-tok",
		projectKey:          "my-project",
		targetToken:         "sc-tok",
		defaultOrganization: "my-org",
	}
	if err := validateTransferConfig(cfg); err != nil {
		t.Errorf("expected no error for a plain key, got %v", err)
	}
}

// #383: a misspelled --project_key passes validation but silently
// returns zero projects from /api/projects/search?projects=<typo>.
// ensureTransferProjectExtracted closes that gap by checking the
// post-extract getProjects records and erroring with a clear message
// that names the exact value the operator typed.
func TestEnsureTransferProjectExtracted_MissingProject(t *testing.T) {
	dir := t.TempDir()
	srvURL := "http://localhost:10000"

	// Synthesise an extract dir with extract.json + getProjects/ containing
	// real project keys, but NOT the one the operator typed.
	extractDir := filepath.Join(dir, "2026-06-12-01")
	if err := os.MkdirAll(filepath.Join(extractDir, "getProjects"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, extractDir, "extract.json", `{"url":"`+srvURL+`/"}`)
	writeFile(t, filepath.Join(extractDir, "getProjects"), "results.1.jsonl",
		`{"key":"real-project-1","serverUrl":"`+srvURL+`/"}`+"\n"+
			`{"key":"real-project-2","serverUrl":"`+srvURL+`/"}`+"\n")

	cfg := transferConfig{
		sourceURL:  srvURL,
		projectKey: "missspelled-key",
		exportDir:  dir,
	}
	err := ensureTransferProjectExtracted(cfg, []string{"missspelled-key"})
	if err == nil {
		t.Fatal("expected error for misspelled project key, got nil")
	}
	for _, want := range []string{"missspelled-key", srvURL, "--" + flagProjectKey} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// #529: with multiple matched keys, ensureTransferProjectExtracted must
// name only the ones actually missing from the extract, not the whole set.
func TestEnsureTransferProjectExtracted_MultipleKeysOneMissing(t *testing.T) {
	dir := t.TempDir()
	srvURL := "http://localhost:10000"

	extractDir := filepath.Join(dir, "2026-06-12-01")
	if err := os.MkdirAll(filepath.Join(extractDir, "getProjects"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, extractDir, "extract.json", `{"url":"`+srvURL+`/"}`)
	writeFile(t, filepath.Join(extractDir, "getProjects"), "results.1.jsonl",
		`{"key":"BANKING_a","serverUrl":"`+srvURL+`/"}`+"\n"+
			`{"key":"BANKING_b","serverUrl":"`+srvURL+`/"}`+"\n")

	cfg := transferConfig{sourceURL: srvURL, projectKey: "BANKING_.+", exportDir: dir}
	err := ensureTransferProjectExtracted(cfg, []string{"BANKING_a", "BANKING_b", "BANKING_c"})
	if err == nil {
		t.Fatal("expected error naming the missing key, got nil")
	}
	if !contains(err.Error(), "BANKING_c") {
		t.Errorf("error %q should name the missing key BANKING_c", err.Error())
	}
	if contains(err.Error(), "BANKING_a") || contains(err.Error(), "BANKING_b") {
		t.Errorf("error %q should not name the keys that WERE extracted", err.Error())
	}
}

// #383: when the configured project key matches a record in
// getProjects (trailing-slash variations on the source URL accepted),
// no error is returned.
func TestEnsureTransferProjectExtracted_PresentProject(t *testing.T) {
	cases := []struct {
		name      string
		cfgURL    string
		recordURL string
	}{
		{name: "both with trailing slash", cfgURL: "http://localhost:10000/", recordURL: "http://localhost:10000/"},
		{name: "cfg without slash, record with", cfgURL: "http://localhost:10000", recordURL: "http://localhost:10000/"},
		{name: "cfg with slash, record without", cfgURL: "http://localhost:10000/", recordURL: "http://localhost:10000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			extractDir := filepath.Join(dir, "2026-06-12-01")
			if err := os.MkdirAll(filepath.Join(extractDir, "getProjects"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, extractDir, "extract.json", `{"url":"`+c.recordURL+`"}`)
			writeFile(t, filepath.Join(extractDir, "getProjects"), "results.1.jsonl",
				`{"key":"my-project","serverUrl":"`+c.recordURL+`"}`+"\n")

			cfg := transferConfig{sourceURL: c.cfgURL, projectKey: "my-project", exportDir: dir}
			if err := ensureTransferProjectExtracted(cfg, []string{"my-project"}); err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}

// writeFile is a tiny helper that writes contents to dir/name.
func writeFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTransferTargetTasksResolveToProjectScopedPlan locks in the contract of
// the project-scoped transfer task list: (a) every name is a real task,
// (b) the leaves resolve to an acyclic plan, (c) the quality profiles' rules
// and the gate's conditions are configured before project data is imported
// (so reproduced issues match) and metadata sync runs after the import, and
// (d) dependency resolution does not drag in the global / instance-wide
// entities transfer deliberately leaves untouched.
func TestTransferTargetTasksResolveToProjectScopedPlan(t *testing.T) {
	// transfer migrates with the default (enterprise) edition.
	reg := migrate.FilterByEdition(
		migrate.BuildMigrateRegistry(migrate.RegisterAll()),
		common.EditionEnterprise,
	)

	targets := migrate.MigrateTargetTasks(reg, "", false, false, false, false, transferTargetTasks)
	if len(targets) != len(transferTargetTasks) {
		t.Fatalf("explicit transfer targets not honored verbatim: got %v", targets)
	}

	taskSet := migrate.ResolveDependencies(targets, reg)
	if taskSet == nil {
		t.Fatal("transferTargetTasks failed to resolve dependencies — an unknown task name?")
	}

	plan, err := migrate.PlanPhases(taskSet, reg)
	if err != nil {
		t.Fatalf("PlanPhases: %v", err)
	}

	phaseOf := map[string]int{}
	for i, phase := range plan {
		for _, name := range phase {
			phaseOf[name] = i
		}
	}

	// Issues/hotspots are reproduced by replaying the scan report, so the
	// quality profiles' rules and the gate's conditions must be in place
	// first; metadata sync needs the issues to already exist in Cloud.
	assertRunsBefore(t, phaseOf, "restoreProfiles", "importProjectData")
	assertRunsBefore(t, phaseOf, "addGateConditions", "importProjectData")
	assertRunsBefore(t, phaseOf, "importProjectData", "syncIssueMetadata")
	assertRunsBefore(t, phaseOf, "importProjectData", "syncHotspotMetadata")

	// The project, its gate, its profiles, and its issue/hotspot history are
	// all present. The project's DevOps platform binding is project-scoped
	// and included too (issue #122), along with the read-only tasks that
	// resolve the target org's own binding and its bindable repositories.
	assertAllInSet(t, taskSet, true, []string{
		"createProjects", "createGates", "createProfiles",
		"setProjectGates", "setProjectProfiles",
		"importProjectData", "syncIssueMetadata", "syncHotspotMetadata",
		"matchProjectRepos", "setProjectBinding", "getOrgBinding", "getOrgRepos",
	})

	// The binding needs the project to exist and the migration user to be a
	// project admin before it can be written.
	assertRunsBefore(t, phaseOf, "createProjects", "setProjectBinding")
	assertRunsBefore(t, phaseOf, "matchProjectRepos", "setProjectBinding")
	assertRunsBefore(t, phaseOf, "getOrgBinding", "matchProjectRepos")

	// Project-scoped: these global / instance-wide tasks must NOT be pulled
	// in by dependency resolution.
	assertAllInSet(t, taskSet, false, []string{
		"createPortfolios", "setPortfolioProjects", "configurePortfolios",
		"setGlobalSettings", "setGlobalWebhooks", "setGlobalNewCodePeriod",
		"createPermissionTemplates", "setTemplateGroupPermissions", "setDefaultTemplates",
		"setOrgGroupPermissions", "setProfileGroupPermissions",
		"setDefaultProfiles", "setDefaultGates",
		"updateRuleTags", "updateRuleDescriptions",
		"createMigrationGroups",
	})
}

// assertRunsBefore fails the test unless task early is scheduled in an
// earlier phase than task late.
func assertRunsBefore(t *testing.T, phaseOf map[string]int, early, late string) {
	t.Helper()
	if phaseOf[early] >= phaseOf[late] {
		t.Errorf("%s (phase %d) must run before %s (phase %d)",
			early, phaseOf[early], late, phaseOf[late])
	}
}

// assertAllInSet fails the test for any name whose membership in set does not
// match want.
func assertAllInSet(t *testing.T, set map[string]bool, want bool, names []string) {
	t.Helper()
	for _, name := range names {
		if set[name] != want {
			t.Errorf("task %q: in resolved set = %v, want %v", name, set[name], want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// #529: resolveTransferProjectKeys must return every source project key
// that fully matches --project_key as an anchored (^...$) regex — a
// substring match (e.g. a key with "BANKING_" in the middle) must NOT be
// included.
func TestResolveTransferProjectKeys_PatternMatchesAnchored(t *testing.T) {
	srv := newProjectListingMockServer(t, []string{
		"BANKING_core", "BANKING_payments", "other-project", "not-BANKING_at-all",
	})

	cfg := transferConfig{sourceURL: srv.URL, sourceToken: "tok", projectKey: "BANKING_.+"}
	got, err := resolveTransferProjectKeys(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resolveTransferProjectKeys: %v", err)
	}
	want := []string{"BANKING_core", "BANKING_payments"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (sorted, anchored match only)", got, want)
	}
}

// A plain literal key must resolve to itself only, even among other
// projects whose keys share a substring.
func TestResolveTransferProjectKeys_LiteralKeyMatchesItself(t *testing.T) {
	srv := newProjectListingMockServer(t, []string{"my-project", "my-project-2", "other"})

	cfg := transferConfig{sourceURL: srv.URL, sourceToken: "tok", projectKey: "my-project"}
	got, err := resolveTransferProjectKeys(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resolveTransferProjectKeys: %v", err)
	}
	if want := []string{"my-project"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A pattern matching nothing on the source must produce a clear error
// rather than silently proceeding with zero projects.
func TestResolveTransferProjectKeys_NoMatchIsAnError(t *testing.T) {
	srv := newProjectListingMockServer(t, []string{"other-project"})

	cfg := transferConfig{sourceURL: srv.URL, sourceToken: "tok", projectKey: "BANKING_.+"}
	_, err := resolveTransferProjectKeys(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected an error when the pattern matches no source project")
	}
	if !contains(err.Error(), "BANKING_.+") {
		t.Errorf("error %q should name the pattern", err.Error())
	}
}
