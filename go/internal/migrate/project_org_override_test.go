// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// projectOrgFixture writes an export directory holding organizations.csv
// and projects.csv, and returns the directory plus a ready Executor whose
// logger writes into buf.
func projectOrgFixture(t *testing.T, orgCSV, projectsCSV string, buf *bytes.Buffer) (string, *Executor) {
	t.Helper()
	dir := t.TempDir()
	writeOrgCSV(t, dir, orgCSV)
	if err := os.WriteFile(filepath.Join(dir, structure.ProjectsCSVFileName), []byte(projectsCSV), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(dir, "run-01")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, &Executor{
		Store:     common.NewDataStore(runDir),
		ExportDir: dir,
		Logger:    slog.New(slog.NewTextHandler(buf, nil)),
	}
}

// orgByProject reads a generate*Mappings task's output and returns
// project key → resolved sonarcloud_org_key.
func orgByProject(t *testing.T, e *Executor, task string) map[string]string {
	t.Helper()
	items, err := e.Store.ReadAll(task)
	if err != nil {
		t.Fatalf("reading %s: %v", task, err)
	}
	out := make(map[string]string, len(items))
	for _, raw := range items {
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatalf("unmarshalling %s row: %v", task, err)
		}
		key, _ := row["key"].(string)
		org, _ := row["sonarcloud_org_key"].(string)
		out[key] = org
	}
	return out
}

const projectOrgFixtureOrgCSV = `sonarqube_org_key,sonarcloud_org_key,alm,is_cloud
https://sq.example.invalid,default-org,,false
gitlab.com/acme,gitlab-org,gitlab,true
`

// Issue #612: an unbound project with a filled sonarcloud_org_key cell
// migrates to that organization; a blank cell falls back to
// organizations.csv; a cloud-bound project keeps its binding-derived
// organization and the ignored override is WARNed about.
func TestLoadCSVToJSONLAppliesProjectOrgOverride(t *testing.T) {
	projectsCSV := `key,sonarcloud_org_key,name,server_url,sonarqube_org_key,alm,is_cloud_binding
unbound-overridden,other-org,Unbound Overridden,https://sq.example.invalid,https://sq.example.invalid,,false
unbound-default,,Unbound Default,https://sq.example.invalid,https://sq.example.invalid,,false
onprem-overridden,onprem-org,On-prem Overridden,https://sq.example.invalid,https://sq.example.invalid,github,false
cloud-bound,sneaky-org,Cloud Bound,https://sq.example.invalid,gitlab.com/acme,gitlab,true
`
	var logs bytes.Buffer
	_, e := projectOrgFixture(t, projectOrgFixtureOrgCSV, projectsCSV, &logs)

	if err := loadCSVToJSONL(e, "generateProjectMappings", structure.ProjectsCSVFileName); err != nil {
		t.Fatalf("loadCSVToJSONL: %v", err)
	}

	got := orgByProject(t, e, "generateProjectMappings")
	want := map[string]string{
		"unbound-overridden": "other-org",
		"unbound-default":    "default-org",
		"onprem-overridden":  "onprem-org",
		"cloud-bound":        "gitlab-org",
	}
	for key, wantOrg := range want {
		if got[key] != wantOrg {
			t.Errorf("project %q resolved to org %q, want %q", key, got[key], wantOrg)
		}
	}

	out := logs.String()
	if !strings.Contains(out, "organization override ignored") {
		t.Errorf("expected a WARN about the ignored override, got:\n%s", out)
	}
	if !strings.Contains(out, "sneaky-org") {
		t.Errorf("WARN does not name the ignored organization, got:\n%s", out)
	}
	// Only the cloud-bound project may be warned about.
	if n := strings.Count(out, "organization override ignored"); n != 1 {
		t.Errorf("got %d override WARNs, want exactly 1:\n%s", n, out)
	}
}

// The override column belongs to projects.csv alone. Every other mapping
// CSV must keep taking its organization from the organizations.csv join,
// even if a row happens to carry a sonarcloud_org_key cell.
func TestLoadCSVToJSONLIgnoresOverrideOutsideProjectsCSV(t *testing.T) {
	var logs bytes.Buffer
	dir, e := projectOrgFixture(t, projectOrgFixtureOrgCSV, "key,sonarcloud_org_key\np,\n", &logs)

	gatesCSV := "name,sonarcloud_org_key,sonarqube_org_key\nBackend QG,other-org,https://sq.example.invalid\n"
	if err := os.WriteFile(filepath.Join(dir, "gates.csv"), []byte(gatesCSV), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := loadCSVToJSONL(e, "generateGateMappings", "gates.csv"); err != nil {
		t.Fatalf("loadCSVToJSONL: %v", err)
	}

	items, err := e.Store.ReadAll("generateGateMappings")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d gate rows, want 1", len(items))
	}
	var row map[string]any
	if err := json.Unmarshal(items[0], &row); err != nil {
		t.Fatal(err)
	}
	if org, _ := row["sonarcloud_org_key"].(string); org != "default-org" {
		t.Errorf("gate org = %q, want %q (the organizations.csv join, not the row's own cell)", org, "default-org")
	}
}

// An override naming an organization organizations.csv never mentions
// must still be checked against SonarQube Cloud, or the run only
// discovers the typo mid-flight as a 404.
func TestValidateOrgsExistChecksProjectOverrides(t *testing.T) {
	projectsCSV := `key,sonarcloud_org_key,server_url,sonarqube_org_key,alm,is_cloud_binding
p1,typo-org,https://sq.example.invalid,https://sq.example.invalid,,false
`
	var logs bytes.Buffer
	dir, _ := projectOrgFixture(t, projectOrgFixtureOrgCSV, projectsCSV, &logs)

	lookup := newFakeOrgs("default-org", "gitlab-org")
	err := validateOrgsExist(context.Background(), lookup, dir, "my-enterprise", "", false)
	if err == nil {
		t.Fatal("expected an error for the missing override org")
	}
	var ec *common.ExitCodeError
	if !errors.As(err, &ec) || ec.Code != 3 {
		t.Fatalf("expected exit code 3, got %v (%T)", err, err)
	}
	if !strings.Contains(err.Error(), "typo-org") || !strings.Contains(err.Error(), structure.ProjectsCSVFileName) {
		t.Errorf("error should name the org and projects.csv, got: %q", err.Error())
	}
}

// A refused override is never a target organization, so it must not be
// validated — otherwise a stale cell on a bound project would abort a
// run that was going to ignore it anyway.
func TestValidateOrgsExistSkipsRefusedOverrides(t *testing.T) {
	projectsCSV := `key,sonarcloud_org_key,server_url,sonarqube_org_key,alm,is_cloud_binding
p1,does-not-exist,https://sq.example.invalid,gitlab.com/acme,gitlab,true
`
	var logs bytes.Buffer
	dir, _ := projectOrgFixture(t, projectOrgFixtureOrgCSV, projectsCSV, &logs)

	lookup := newFakeOrgs("default-org", "gitlab-org")
	if err := validateOrgsExist(context.Background(), lookup, dir, "my-enterprise", "", false); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// migrateOrgKeys drives the pre-flight DevOps-binding probe, so it has
// to list the override organizations too.
func TestMigrateOrgKeysIncludesOverrides(t *testing.T) {
	projectsCSV := `key,sonarcloud_org_key,server_url,sonarqube_org_key,alm,is_cloud_binding
p1,extra-org,https://sq.example.invalid,https://sq.example.invalid,,false
p2,extra-org,https://sq.example.invalid,https://sq.example.invalid,,false
p3,,https://sq.example.invalid,https://sq.example.invalid,,false
p4,refused-org,https://sq.example.invalid,gitlab.com/acme,gitlab,true
`
	var logs bytes.Buffer
	dir, _ := projectOrgFixture(t, projectOrgFixtureOrgCSV, projectsCSV, &logs)

	got, err := migrateOrgKeys(MigrateConfig{ExportDirectory: dir}, false)
	if err != nil {
		t.Fatalf("migrateOrgKeys: %v", err)
	}
	want := []string{"default-org", "gitlab-org", "extra-org"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("migrateOrgKeys = %v, want %v", got, want)
	}
}

// With --default_organization applied, the overrides still have to be
// listed: the default path used to return that one key and nothing else.
func TestMigrateOrgKeysIncludesOverridesWithAppliedDefault(t *testing.T) {
	projectsCSV := `key,sonarcloud_org_key,server_url,sonarqube_org_key,alm,is_cloud_binding
p1,extra-org,https://sq.example.invalid,https://sq.example.invalid,,false
`
	var logs bytes.Buffer
	dir, _ := projectOrgFixture(t, projectOrgFixtureOrgCSV, projectsCSV, &logs)

	got, err := migrateOrgKeys(MigrateConfig{ExportDirectory: dir, DefaultOrganization: "the-default"}, true)
	if err != nil {
		t.Fatalf("migrateOrgKeys: %v", err)
	}
	want := []string{"the-default", "extra-org"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("migrateOrgKeys = %v, want %v", got, want)
	}
}

// sync-issues has to resolve an overridden project in the organization it
// was actually migrated into, and render its key with that organization.
func TestResolveSyncTargetsHonoursProjectOrgOverride(t *testing.T) {
	projectsCSV := `key,sonarcloud_org_key,server_url,sonarqube_org_key,alm,is_cloud_binding
p1,extra-org,https://sq.example.invalid,https://sq.example.invalid,,false
p2,,https://sq.example.invalid,https://sq.example.invalid,,false
`
	var logs bytes.Buffer
	dir, _ := projectOrgFixture(t, projectOrgFixtureOrgCSV, projectsCSV, &logs)

	targets, err := resolveSyncTargets(dir, "<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>", nil)
	if err != nil {
		t.Fatalf("resolveSyncTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}
	byKey := make(map[string]syncTarget, len(targets))
	for _, tgt := range targets {
		byKey[tgt.Key] = tgt
	}
	if got := byKey["p1"].OrgKey; got != "extra-org" {
		t.Errorf("p1 org = %q, want %q", got, "extra-org")
	}
	if got := byKey["p1"].CloudProjectKey; got != "extra-org_p1" {
		t.Errorf("p1 cloud key = %q, want %q", got, "extra-org_p1")
	}
	if got := byKey["p2"].OrgKey; got != "default-org" {
		t.Errorf("p2 org = %q, want %q", got, "default-org")
	}
}
