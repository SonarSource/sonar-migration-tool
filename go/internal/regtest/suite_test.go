// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package regtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// writeConfig writes the given JSON content to a config file inside a
// fresh temp directory and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func TestLoadConfigFile_UnifiedShapeWithDefaultOrganization(t *testing.T) {
	exportDir := t.TempDir()
	path := writeConfig(t, `{
		"export_directory": "`+exportDir+`",
		"source": {"url": "http://sqs.local", "token": "sqs-token"},
		"target": {"url": "https://sc.local", "token": "sc-token", "enterprise_key": "my-enterprise", "default_organization": "my-org"}
	}`)

	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if cfg.SQSURL != "http://sqs.local" || cfg.SQSToken != "sqs-token" {
		t.Errorf("source fields = %+v", cfg)
	}
	if cfg.SCURL != "https://sc.local" || cfg.SCToken != "sc-token" {
		t.Errorf("target fields = %+v", cfg)
	}
	if cfg.SCOrg != "my-org" {
		t.Errorf("SCOrg = %q, want %q (enterprise_key must not be used as the org key)", cfg.SCOrg, "my-org")
	}
	if cfg.ExportDir != exportDir {
		t.Errorf("ExportDir = %q, want %q", cfg.ExportDir, exportDir)
	}
}

func TestLoadConfigFile_ResolvesOrgFromOrganizationsCSV(t *testing.T) {
	exportDir := t.TempDir()
	csv := "sonarqube_org_key,sonarcloud_org_key,binding_key,server_url,alm,url,is_cloud,project_count\n" +
		"http://sqs.local/,resolved-org,http://sqs.local/,http://sqs.local/,,,false,3\n"
	if err := os.WriteFile(filepath.Join(exportDir, "organizations.csv"), []byte(csv), 0o600); err != nil {
		t.Fatalf("writing organizations.csv: %v", err)
	}
	path := writeConfig(t, `{
		"export_directory": "`+exportDir+`",
		"source": {"url": "http://sqs.local", "token": "sqs-token"},
		"target": {"url": "https://sc.local", "token": "sc-token"}
	}`)

	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if cfg.SCOrg != "resolved-org" {
		t.Errorf("SCOrg = %q, want %q", cfg.SCOrg, "resolved-org")
	}
}

func TestLoadConfigFile_NoOrgFoundIsAnError(t *testing.T) {
	exportDir := t.TempDir()
	path := writeConfig(t, `{
		"export_directory": "`+exportDir+`",
		"source": {"url": "http://sqs.local", "token": "sqs-token"},
		"target": {"url": "https://sc.local", "token": "sc-token"}
	}`)

	if _, err := LoadConfigFile(path); err == nil {
		t.Fatal("expected an error when no default_organization and no organizations.csv exist")
	}
}

func TestLoadConfigFile_MultipleOrgsRequireDefaultOrganization(t *testing.T) {
	exportDir := t.TempDir()
	csv := "sonarqube_org_key,sonarcloud_org_key,binding_key,server_url,alm,url,is_cloud,project_count\n" +
		"http://sqs.local/,org-a,http://sqs.local/,http://sqs.local/,,,false,3\n" +
		"http://other.local/,org-b,http://other.local/,http://other.local/,,,false,2\n"
	if err := os.WriteFile(filepath.Join(exportDir, "organizations.csv"), []byte(csv), 0o600); err != nil {
		t.Fatalf("writing organizations.csv: %v", err)
	}
	path := writeConfig(t, `{
		"export_directory": "`+exportDir+`",
		"source": {"url": "http://sqs.local", "token": "sqs-token"},
		"target": {"url": "https://sc.local", "token": "sc-token"}
	}`)

	if _, err := LoadConfigFile(path); err == nil {
		t.Fatal("expected an error when organizations.csv maps multiple distinct orgs and no default_organization is set")
	}
}

// writeCreateProjectsResult writes a single createProjects task record
// under exportDir/runID/createProjects/, mirroring what a real migrate
// run's project_key_pattern-aware creation task produces (#138).
func writeCreateProjectsResult(t *testing.T, exportDir, runID, sourceKey, cloudKey string) {
	t.Helper()
	runDir := filepath.Join(exportDir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	store := common.NewDataStore(runDir)
	w, err := store.Writer("createProjects")
	if err != nil {
		t.Fatalf("creating writer: %v", err)
	}
	rec, _ := json.Marshal(map[string]string{"key": sourceKey, "cloud_project_key": cloudKey})
	if err := w.WriteOne(rec); err != nil {
		t.Fatalf("writing record: %v", err)
	}
}

// TestScProjectKey_UsesCustomProjectKeyPatternMapping ensures regtest
// verifies against the SonarCloud key a project was ACTUALLY migrated to,
// not the default "<org>_<key>" convention — target.project_key_pattern
// (#138) can produce an arbitrarily different key (e.g. the bare source
// key with no org prefix), and every check queries SC by scProjectKey's
// return value.
func TestScProjectKey_UsesCustomProjectKeyPatternMapping(t *testing.T) {
	exportDir := t.TempDir()
	writeCreateProjectsResult(t, exportDir, "2026-08-25-01", "BANKING-PORTAL", "BANKING-PORTAL")

	suite, err := NewSuite(Config{
		SQSURL: "http://sqs.local", SQSToken: "t",
		SCURL: "https://sc.local", SCToken: "t", SCOrg: "latest-unbound",
		ExportDir: exportDir,
	})
	if err != nil {
		t.Fatalf("NewSuite: %v", err)
	}
	if got := suite.scProjectKey("BANKING-PORTAL"); got != "BANKING-PORTAL" {
		t.Errorf("scProjectKey(%q) = %q, want %q (mapped key, not org_key default)",
			"BANKING-PORTAL", got, "BANKING-PORTAL")
	}
	// An unmapped key (e.g. not part of this run) still falls back to the
	// default convention.
	if got, want := suite.scProjectKey("other-project"), "latest-unbound_other-project"; got != want {
		t.Errorf("scProjectKey(%q) = %q, want %q (default fallback)", "other-project", got, want)
	}
}

// TestScProjectKey_FallsBackWhenNoRunDataExists covers a Suite built
// against an export directory that has no run yet (e.g. regtest invoked
// before any migrate/transfer ran) — it must not error, just use the
// default convention for every key.
func TestScProjectKey_FallsBackWhenNoRunDataExists(t *testing.T) {
	suite, err := NewSuite(Config{
		SQSURL: "http://sqs.local", SQSToken: "t",
		SCURL: "https://sc.local", SCToken: "t", SCOrg: "my-org",
		ExportDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSuite: %v", err)
	}
	if got, want := suite.scProjectKey("proj"), "my-org_proj"; got != want {
		t.Errorf("scProjectKey(%q) = %q, want %q", "proj", got, want)
	}
}

// TestLatestRunDir_PicksMostRecentlyModified ensures the run-dir picker
// doesn't fall into the string-sort trap that breaks once a day has 10+
// runs (e.g. "...-9" sorting after "...-10" alphabetically) — it must
// pick by modification time instead.
func TestLatestRunDir_PicksMostRecentlyModified(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "2026-08-25-09")
	newer := filepath.Join(dir, "2026-08-25-10")
	if err := os.Mkdir(older, 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.Mkdir(newer, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := latestRunDir(dir)
	if err != nil {
		t.Fatalf("latestRunDir: %v", err)
	}
	if got != "2026-08-25-10" {
		t.Errorf("latestRunDir = %q, want %q", got, "2026-08-25-10")
	}
}
