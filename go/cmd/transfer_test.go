// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/spf13/cobra"
)

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
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
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
	err := ensureTransferProjectExtracted(cfg)
	if err == nil {
		t.Fatal("expected error for misspelled project key, got nil")
	}
	for _, want := range []string{"missspelled-key", srvURL, "--" + flagProjectKey} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
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
			if err := ensureTransferProjectExtracted(cfg); err != nil {
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
	// all present.
	assertAllInSet(t, taskSet, true, []string{
		"createProjects", "createGates", "createProfiles",
		"setProjectGates", "setProjectProfiles",
		"importProjectData", "syncIssueMetadata", "syncHotspotMetadata",
	})

	// Project-scoped: these global / instance-wide tasks must NOT be pulled
	// in by dependency resolution.
	assertAllInSet(t, taskSet, false, []string{
		"createPortfolios", "setPortfolioProjects", "configurePortfolios",
		"setGlobalSettings", "setGlobalWebhooks", "setGlobalNewCodePeriod",
		"createPermissionTemplates", "setTemplateGroupPermissions", "setDefaultTemplates",
		"setOrgGroupPermissions", "setProfileGroupPermissions",
		"setDefaultProfiles", "setDefaultGates",
		"updateRuleTags", "updateRuleDescriptions",
		"matchProjectRepos", "setProjectBinding",
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
