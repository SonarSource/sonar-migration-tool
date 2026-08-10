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
