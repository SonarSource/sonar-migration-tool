// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/cobra"
)

// newSyncIssuesTestCmd mirrors syncIssuesCmd's flag set so
// resolveSyncIssuesConfig can be exercised in isolation without invoking RunE.
func newSyncIssuesTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sync-issues"}
	f := cmd.Flags()
	f.StringP(flagConfig, "c", "", "")
	f.String(flagSourceURL, "", "")
	f.String(flagSourceToken, "", "")
	f.StringSlice(flagProjectKeys, nil, "")
	f.String(flagTargetURL, "", "")
	f.String(flagTargetToken, "", "")
	f.String(flagDefaultOrg, "", "")
	f.String(flagProjectKeyPattern, "", "")
	f.String(flagEnterpriseKey, "", "")
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

func writeSyncIssuesConfig(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// sync-issues reads the common unified source/target config shape just
// like extract, migrate and transfer do — same loader, same field names.
func TestResolveSyncIssuesConfig_UnifiedConfigShape(t *testing.T) {
	path := writeSyncIssuesConfig(t, `{
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
			"default_organization": "my-org",
			"project_key_pattern": "acme_<ORIGINAL_PROJECT_KEY>"
		}
	}`)

	cmd := newSyncIssuesTestCmd()
	if err := cmd.ParseFlags([]string{"-c", path}); err != nil {
		t.Fatal(err)
	}

	cfg, err := resolveSyncIssuesConfig(cmd)
	if err != nil {
		t.Fatalf("resolveSyncIssuesConfig: %v", err)
	}

	want := syncIssuesConfig{
		sourceURL:           "https://sq.example.com",
		sourceToken:         "sq-token",
		targetURL:           "https://sonarcloud.io/",
		targetToken:         "sc-token",
		defaultOrganization: "my-org",
		projectKeyPattern:   "acme_<ORIGINAL_PROJECT_KEY>",
		enterpriseKey:       "ent-key",
		exportDir:           "/tmp/from-cfg",
		sourceConcurrency:   7,
		targetConcurrency:   7,
		sourceTimeout:       42,
		targetTimeout:       42,
		pemFilePath:         "/cert/pem",
		keyFilePath:         "/cert/key",
		certPassword:        "p4ss",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v\nwant %+v", cfg, want)
	}
}

// CLI flags must take precedence over config-file values, matching the
// contract documented for extract/migrate/transfer.
func TestResolveSyncIssuesConfig_CLIOverridesConfig(t *testing.T) {
	path := writeSyncIssuesConfig(t, `{
		"source": {"url": "https://from-cfg", "token": "cfg-source"},
		"target": {"token": "cfg-target", "default_organization": "cfg-org"}
	}`)

	cmd := newSyncIssuesTestCmd()
	if err := cmd.ParseFlags([]string{
		"-c", path,
		"--source_url", "https://from-flag",
		"--target_token", "flag-target",
		"--default_organization", "flag-org",
		"--project_key", "proj-a",
		"--project_key", "proj-b",
		"--concurrency", "11",
		"--timeout", "99",
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := resolveSyncIssuesConfig(cmd)
	if err != nil {
		t.Fatalf("resolveSyncIssuesConfig: %v", err)
	}

	if cfg.sourceURL != "https://from-flag" {
		t.Errorf("sourceURL: got %q, want %q", cfg.sourceURL, "https://from-flag")
	}
	if cfg.sourceToken != "cfg-source" {
		t.Errorf("sourceToken (config-only field): got %q, want %q", cfg.sourceToken, "cfg-source")
	}
	if cfg.targetToken != "flag-target" {
		t.Errorf("targetToken: got %q, want %q", cfg.targetToken, "flag-target")
	}
	if cfg.defaultOrganization != "flag-org" {
		t.Errorf("defaultOrganization: got %q, want %q", cfg.defaultOrganization, "flag-org")
	}
	if want := []string{"proj-a", "proj-b"}; !reflect.DeepEqual(cfg.projectKeys, want) {
		t.Errorf("projectKeys: got %v, want %v", cfg.projectKeys, want)
	}
	// #528 — sync-issues's --concurrency/--timeout set both source and
	// target at once (config file is the only way to differ them).
	if cfg.sourceConcurrency != 11 || cfg.targetConcurrency != 11 {
		t.Errorf("concurrency: got source=%d target=%d, want both 11", cfg.sourceConcurrency, cfg.targetConcurrency)
	}
	if cfg.sourceTimeout != 99 || cfg.targetTimeout != 99 {
		t.Errorf("timeout: got source=%d target=%d, want both 99", cfg.sourceTimeout, cfg.targetTimeout)
	}
}

// Issue #528: source and target must be able to carry genuinely
// different concurrency/timeout values from the config file — before
// this fix, loadSyncIssuesFileDefaults collapsed the two
// independently-resolved values into one shared field, always
// discarding target's timeout and arbitrarily preferring one side for
// concurrency.
func TestResolveSyncIssuesConfig_SourceAndTargetConcurrencyTimeoutDiffer(t *testing.T) {
	path := writeSyncIssuesConfig(t, `{
		"concurrency": 10,
		"timeout": 60,
		"source": {
			"url": "https://sq.example.com",
			"token": "sq-token",
			"concurrency": 10,
			"timeout": 10
		},
		"target": {
			"token": "sc-token",
			"default_organization": "my-org",
			"concurrency": 5,
			"timeout": 30
		}
	}`)

	cmd := newSyncIssuesTestCmd()
	if err := cmd.ParseFlags([]string{"-c", path}); err != nil {
		t.Fatal(err)
	}

	cfg, err := resolveSyncIssuesConfig(cmd)
	if err != nil {
		t.Fatalf("resolveSyncIssuesConfig: %v", err)
	}

	checks := []struct {
		name string
		got  int
		want int
	}{
		{"sourceConcurrency", cfg.sourceConcurrency, 10},
		{"targetConcurrency", cfg.targetConcurrency, 5},
		{"sourceTimeout", cfg.sourceTimeout, 10},
		{"targetTimeout", cfg.targetTimeout, 30},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
}

// Issue #528 follow-up: when only ONE side sets concurrency/timeout, the
// unset side must borrow the explicitly-set side's value rather than
// falling through to the hardcoded package default — see the identical
// test in transfer_test.go.
func TestResolveSyncIssuesConfig_SingleSideConcurrencyTimeoutFallsBackToOtherSide(t *testing.T) {
	path := writeSyncIssuesConfig(t, `{
		"source": {"url": "https://sq.example.com", "token": "sq-token"},
		"target": {
			"token": "sc-token",
			"default_organization": "my-org",
			"concurrency": 5,
			"timeout": 30
		}
	}`)

	cmd := newSyncIssuesTestCmd()
	if err := cmd.ParseFlags([]string{"-c", path}); err != nil {
		t.Fatal(err)
	}

	cfg, err := resolveSyncIssuesConfig(cmd)
	if err != nil {
		t.Fatalf("resolveSyncIssuesConfig: %v", err)
	}

	if cfg.sourceConcurrency != 5 {
		t.Errorf("sourceConcurrency: got %d, want 5 (borrowed from target)", cfg.sourceConcurrency)
	}
	if cfg.sourceTimeout != 30 {
		t.Errorf("sourceTimeout: got %d, want 30 (borrowed from target)", cfg.sourceTimeout)
	}
}

// Issue #527: --fast_sync overrides the config file; the config file
// value is used when the flag is absent; defaults to false otherwise.
func TestResolveSyncIssuesConfig_FastSync(t *testing.T) {
	path := writeSyncIssuesConfig(t, `{
		"fast_sync": false,
		"source": {"url": "https://sq.example.com", "token": "tok"},
		"target": {"token": "ct", "default_organization": "org"}
	}`)

	cmd := newSyncIssuesTestCmd()
	if err := cmd.ParseFlags([]string{"-c", path, "--" + flagFastSync}); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveSyncIssuesConfig(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.fastSync {
		t.Error("CLI --fast_sync should override config file's false")
	}

	cmd2 := newSyncIssuesTestCmd()
	if err := cmd2.ParseFlags([]string{"-c", path}); err != nil {
		t.Fatal(err)
	}
	cfg2, err := resolveSyncIssuesConfig(cmd2)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.fastSync {
		t.Error("expected config file's fast_sync=false to be used when flag absent")
	}
}

// project_key is optional for sync-issues (unlike transfer, which is
// project-scoped by design) — omitting it must resolve cleanly to an empty
// filter meaning "every project".
func TestResolveSyncIssuesConfig_ProjectKeyOptional(t *testing.T) {
	cmd := newSyncIssuesTestCmd()
	if err := cmd.ParseFlags([]string{
		"--source_url", "https://sq",
		"--source_token", "tok",
		"--target_token", "ct",
		"--default_organization", "my-org",
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveSyncIssuesConfig(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.projectKeys) != 0 {
		t.Errorf("projectKeys: got %v, want empty", cfg.projectKeys)
	}
}

func TestResolveSyncIssuesConfig_EnterpriseKeyDefaultsToOrg(t *testing.T) {
	cmd := newSyncIssuesTestCmd()
	if err := cmd.ParseFlags([]string{
		"--source_url", "https://sq",
		"--source_token", "tok",
		"--target_token", "ct",
		"--default_organization", "my-org",
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveSyncIssuesConfig(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.enterpriseKey != "my-org" {
		t.Errorf("enterpriseKey: got %q, want %q", cfg.enterpriseKey, "my-org")
	}
}

// Validation must error if either side is missing credentials — but,
// unlike transfer, must NOT require a project key (sync-issues defaults to
// every project on the source).
func TestValidateSyncIssuesConfig_MissingFields(t *testing.T) {
	cases := []struct {
		name   string
		cfg    syncIssuesConfig
		errSub string
	}{
		{
			name:   "missing source",
			cfg:    syncIssuesConfig{targetToken: "t", defaultOrganization: "o"},
			errSub: "--" + flagSourceURL,
		},
		{
			name:   "missing target",
			cfg:    syncIssuesConfig{sourceURL: "u", sourceToken: "t"},
			errSub: "--" + flagTargetToken,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateSyncIssuesConfig(c.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !contains(err.Error(), c.errSub) {
				t.Errorf("error %q does not mention %q", err.Error(), c.errSub)
			}
		})
	}
}

func TestValidateSyncIssuesConfig_HappyPathNoProjectKey(t *testing.T) {
	cfg := syncIssuesConfig{
		sourceURL: "https://sq", sourceToken: "t",
		targetToken: "ct", defaultOrganization: "o",
	}
	if err := validateSyncIssuesConfig(cfg); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}
