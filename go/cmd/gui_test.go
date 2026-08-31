// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// newGUITestCmd mirrors guiCmd's flag set so resolveGUIDefaults can be
// exercised in isolation, without bringing up the HTTP server.
func newGUITestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "gui"}
	f := cmd.Flags()
	f.String("export_directory", DefaultExportDirectory, "")
	f.String("addr", "localhost:0", "")
	f.Bool("no-browser", false, "")
	f.StringP("config", "c", "", "")
	return cmd
}

func writeGUIConfig(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// #388: with no --config, resolveGUIDefaults must return the configured
// export directory and a nil seed so the wizard behaves exactly as it
// did before the flag was added — a fresh blank form.
func TestResolveGUIDefaults_NoConfig(t *testing.T) {
	cmd := newGUITestCmd()
	exportDir, seed, err := resolveGUIDefaults(cmd)
	if err != nil {
		t.Fatalf("resolveGUIDefaults: %v", err)
	}
	if seed != nil {
		t.Errorf("expected nil seed when --config absent, got %+v", seed)
	}
	if exportDir != DefaultExportDirectory {
		t.Errorf("exportDir: got %q, want %q", exportDir, DefaultExportDirectory)
	}
}

// #388/#515: with --config pointing at a unified-shape config, the
// seed carries every URL, token, enterprise key, cert setting, default
// organization, and project key pattern the file declares.
func TestResolveGUIDefaults_UnifiedShapeFillsSeed(t *testing.T) {
	path := writeGUIConfig(t, `{
		"export_directory": "/tmp/from-cfg",
		"project_key": "BANKING_.+",
		"source": {
			"url": "https://sq.example.com",
			"token": "sq-token",
			"pem_file_path": "/etc/cert.pem",
			"key_file_path": "/etc/cert.key",
			"cert_password": "certsecret"
		},
		"target": {
			"url": "https://sonarcloud.io/",
			"token": "sc-token",
			"enterprise_key": "ent-key",
			"default_organization": "my-org"
		}
	}`)

	cmd := newGUITestCmd()
	if err := cmd.ParseFlags([]string{"-c", path}); err != nil {
		t.Fatal(err)
	}
	exportDir, seed, err := resolveGUIDefaults(cmd)
	if err != nil {
		t.Fatalf("resolveGUIDefaults: %v", err)
	}
	if exportDir != "/tmp/from-cfg" {
		t.Errorf("exportDir: got %q, want /tmp/from-cfg (from config)", exportDir)
	}
	if seed == nil {
		t.Fatal("expected seed populated from config, got nil")
	}
	assertSeedStringField(t, "SourceURL", seed.SourceURL, "https://sq.example.com")
	assertSeedStringField(t, "SourceToken", seed.SourceToken, "sq-token")
	assertSeedStringField(t, "TargetURL", seed.TargetURL, "https://sonarcloud.io/")
	assertSeedStringField(t, "TargetToken", seed.TargetToken, "sc-token")
	assertSeedStringField(t, "EnterpriseKey", seed.EnterpriseKey, "ent-key")
	assertSeedStringField(t, "PEMFilePath", seed.PEMFilePath, "/etc/cert.pem")
	assertSeedStringField(t, "KeyFilePath", seed.KeyFilePath, "/etc/cert.key")
	assertSeedStringField(t, "CertPassword", seed.CertPassword, "certsecret")
	assertSeedStringField(t, "DefaultOrganization", seed.DefaultOrganization, "my-org")
	assertSeedStringField(t, "ProjectKeyPattern", seed.ProjectKeyPattern, "BANKING_.+")
}

// #515: the top-level project_key field (transfer's convention) is
// parsed via the shared overlay loader even when the rest of the
// config is otherwise minimal.
func TestResolveGUIDefaults_ProjectKeyFromOverlay(t *testing.T) {
	path := writeGUIConfig(t, `{
		"project_key": "my-project",
		"source": {"url": "https://sq"},
		"target": {"url": "https://sc"}
	}`)
	cmd := newGUITestCmd()
	if err := cmd.ParseFlags([]string{"-c", path}); err != nil {
		t.Fatal(err)
	}
	_, seed, err := resolveGUIDefaults(cmd)
	if err != nil {
		t.Fatalf("resolveGUIDefaults: %v", err)
	}
	if seed == nil {
		t.Fatal("expected non-nil seed")
	}
	assertSeedStringField(t, "ProjectKeyPattern", seed.ProjectKeyPattern, "my-project")
}

// #388: --export_directory on the CLI must win over the config-file
// value, mirroring the precedence the transfer command uses.
func TestResolveGUIDefaults_CLIExportDirWinsOverConfig(t *testing.T) {
	path := writeGUIConfig(t, `{
		"export_directory": "/tmp/from-cfg",
		"source": {"url": "https://sq", "token": "t"},
		"target": {"url": "https://sc", "token": "t"}
	}`)
	cmd := newGUITestCmd()
	if err := cmd.ParseFlags([]string{"-c", path, "--export_directory", "/tmp/from-cli"}); err != nil {
		t.Fatal(err)
	}
	exportDir, _, err := resolveGUIDefaults(cmd)
	if err != nil {
		t.Fatalf("resolveGUIDefaults: %v", err)
	}
	if exportDir != "/tmp/from-cli" {
		t.Errorf("exportDir: got %q, want /tmp/from-cli (CLI wins)", exportDir)
	}
}

// #388: a config with no export_directory leaves the default in place,
// and seed fields the config didn't carry stay nil.
func TestResolveGUIDefaults_MissingFieldsLeaveSeedFieldNil(t *testing.T) {
	path := writeGUIConfig(t, `{
		"source": {"url": "https://sq"}
	}`)
	cmd := newGUITestCmd()
	if err := cmd.ParseFlags([]string{"-c", path}); err != nil {
		t.Fatal(err)
	}
	exportDir, seed, err := resolveGUIDefaults(cmd)
	if err != nil {
		t.Fatalf("resolveGUIDefaults: %v", err)
	}
	if exportDir != DefaultExportDirectory {
		t.Errorf("exportDir: got %q, want default %q", exportDir, DefaultExportDirectory)
	}
	if seed == nil {
		t.Fatal("expected non-nil seed when --config is set")
	}
	assertSeedStringField(t, "SourceURL", seed.SourceURL, "https://sq")
	if seed.SourceToken != nil {
		t.Errorf("SourceToken: got %v, want nil (config didn't carry it)", seed.SourceToken)
	}
	if seed.TargetURL != nil {
		t.Errorf("TargetURL: got %v, want nil", seed.TargetURL)
	}
	if seed.TargetToken != nil {
		t.Errorf("TargetToken: got %v, want nil", seed.TargetToken)
	}
	if seed.EnterpriseKey != nil {
		t.Errorf("EnterpriseKey: got %v, want nil", seed.EnterpriseKey)
	}
	if seed.PEMFilePath != nil {
		t.Errorf("PEMFilePath: got %v, want nil", seed.PEMFilePath)
	}
	if seed.KeyFilePath != nil {
		t.Errorf("KeyFilePath: got %v, want nil", seed.KeyFilePath)
	}
	if seed.CertPassword != nil {
		t.Errorf("CertPassword: got %v, want nil", seed.CertPassword)
	}
	if seed.DefaultOrganization != nil {
		t.Errorf("DefaultOrganization: got %v, want nil", seed.DefaultOrganization)
	}
	if seed.ProjectKeyPattern != nil {
		t.Errorf("ProjectKeyPattern: got %v, want nil", seed.ProjectKeyPattern)
	}
}

// #388: a malformed --config returns an error (better than silently
// running with a blank form when the operator clearly intended to
// pre-fill it).
func TestResolveGUIDefaults_MalformedConfigErrors(t *testing.T) {
	path := writeGUIConfig(t, `{ not valid json `)
	cmd := newGUITestCmd()
	if err := cmd.ParseFlags([]string{"-c", path}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveGUIDefaults(cmd); err == nil {
		t.Fatal("expected error on malformed --config, got nil")
	}
}

func assertSeedStringField(t *testing.T, field string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s: got nil, want %q", field, want)
		return
	}
	if *got != want {
		t.Errorf("%s: got %q, want %q", field, *got, want)
	}
}
