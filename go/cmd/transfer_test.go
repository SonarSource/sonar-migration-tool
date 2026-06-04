package cmd

import (
	"os"
	"path/filepath"
	"testing"

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
	f.String(flagExportDir, "./migration-files/", "")
	f.Bool(flagIncludeScanHistory, false, "")
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
		exportDir:           "/tmp/from-cfg",
		concurrency:         7,
		timeout:             42,
		pemFilePath:         "/cert/pem",
		keyFilePath:         "/cert/key",
		certPassword:        "p4ss",
	}
	if cfg != want {
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
			"default_organization": "cfg-org"
		}
	}`)

	cmd := newTransferTestCmd()
	args := []string{
		"-c", path,
		"--source-url", "https://cli-source",
		"--source-token", "cli-source-tok",
		"--target-url", "https://cli-target",
		"--target-token", "cli-target-tok",
		"--default_organization", "cli-org",
		"--enterprise_key", "cli-ent",
		"--export-dir", "/tmp/cli",
		"--concurrency", "11",
		"--timeout", "99",
		"--pem_file_path", "/cli/pem",
		"--key_file_path", "/cli/key",
		"--cert_password", "cli-pass",
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
		{"exportDir", cfg.exportDir, "/tmp/cli"},
		{"concurrency", cfg.concurrency, 11},
		{"timeout", cfg.timeout, 99},
		{"pemFilePath", cfg.pemFilePath, "/cli/pem"},
		{"keyFilePath", cfg.keyFilePath, "/cli/key"},
		{"certPassword", cfg.certPassword, "cli-pass"},
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
		"--source-url", "https://sq",
		"--source-token", "tok",
		"--target-token", "ct",
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

// Validation must error if either side is missing credentials, with a
// message that names the new --source-* / --target-* flags so users
// aren't pointed at the retired --sq-* / --sc-* names.
func TestValidateTransferConfig_MissingCredentials(t *testing.T) {
	cases := []struct {
		name   string
		cfg    transferConfig
		errSub string
	}{
		{
			name:   "missing source",
			cfg:    transferConfig{targetToken: "t", defaultOrganization: "o"},
			errSub: "--" + flagSourceURL,
		},
		{
			name:   "missing target",
			cfg:    transferConfig{sourceURL: "u", sourceToken: "t"},
			errSub: "--" + flagTargetToken,
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
