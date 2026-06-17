// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	sqcTypes "github.com/sonar-solutions/sq-api-go/types"
)

// fakeOrgLookup stubs the SQC Organizations.Search call so we can
// exercise validateOrgsExist without hitting the network. Pass the set
// of keys that "exist"; any other key will be reported as missing.
type fakeOrgLookup struct {
	existing map[string]bool
	calls    [][]string
}

func newFakeOrgs(existing ...string) *fakeOrgLookup {
	set := make(map[string]bool, len(existing))
	for _, k := range existing {
		set[k] = true
	}
	return &fakeOrgLookup{existing: set}
}

func (f *fakeOrgLookup) Search(ctx context.Context, keys ...string) ([]sqcTypes.Organization, error) {
	f.calls = append(f.calls, append([]string(nil), keys...))
	var out []sqcTypes.Organization
	for _, k := range keys {
		if f.existing[k] {
			out = append(out, sqcTypes.Organization{Key: k, Name: k})
		}
	}
	return out, nil
}

// Issue #283: default org missing → exit 3 + "Default organization"
// message naming the enterprise.
func TestValidateOrgsExist_DefaultMissingReturnsExit3(t *testing.T) {
	dir := t.TempDir()
	writeOrgCSV(t, dir, `sonarqube_org_key,sonarcloud_org_key
org-a,nonexistent-default
`)
	lookup := newFakeOrgs("some-other-org")

	err := validateOrgsExist(context.Background(), lookup, dir, "my-enterprise", "nonexistent-default", true /* appliedDefault */)
	if err == nil {
		t.Fatal("expected an error for missing default org")
	}
	var ec *common.ExitCodeError
	if !errors.As(err, &ec) || ec.Code != 3 {
		t.Fatalf("expected exit code 3, got %v (%T)", err, err)
	}
	want := `Default organization "nonexistent-default" does not exists in SonarQube Cloud, or is not part of Enterprise "my-enterprise"`
	if err.Error() != want {
		t.Errorf("error message:\n got:  %q\n want: %q", err.Error(), want)
	}
}

// Default exists → no error.
func TestValidateOrgsExist_DefaultExistsPasses(t *testing.T) {
	dir := t.TempDir()
	writeOrgCSV(t, dir, `sonarqube_org_key,sonarcloud_org_key
org-a,real-default
`)
	lookup := newFakeOrgs("real-default")
	if err := validateOrgsExist(context.Background(), lookup, dir, "ent", "real-default", true); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// CSV org missing → exit 3 + "Organization X specified in <csv>"
// message naming both the file and the enterprise.
func TestValidateOrgsExist_CSVOrgMissingReturnsExit3(t *testing.T) {
	dir := t.TempDir()
	writeOrgCSV(t, dir, `sonarqube_org_key,sonarcloud_org_key
org-a,real-org
org-b,nonexistent-org
`)
	lookup := newFakeOrgs("real-org")

	err := validateOrgsExist(context.Background(), lookup, dir, "my-enterprise", "" /* default */, false /* appliedDefault */)
	if err == nil {
		t.Fatal("expected an error for missing CSV org")
	}
	var ec *common.ExitCodeError
	if !errors.As(err, &ec) || ec.Code != 3 {
		t.Fatalf("expected exit code 3, got %v (%T)", err, err)
	}
	csvPath := filepath.Join(dir, "organizations.csv")
	want := `Organization "nonexistent-org" specified in "` + csvPath + `" does not exists in SonarQube Cloud, or does not belong to Enterprise "my-enterprise"`
	if err.Error() != want {
		t.Errorf("error message:\n got:  %q\n want: %q", err.Error(), want)
	}
}

// All CSV orgs exist → no error. SKIPPED and empty rows are ignored.
func TestValidateOrgsExist_AllCSVOrgsExistPasses(t *testing.T) {
	dir := t.TempDir()
	writeOrgCSV(t, dir, `sonarqube_org_key,sonarcloud_org_key
org-a,real-a
org-b,SKIPPED
org-c,real-c
org-d,
`)
	lookup := newFakeOrgs("real-a", "real-c")
	if err := validateOrgsExist(context.Background(), lookup, dir, "ent", "", false); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// Duplicate keys across CSV rows are looked up once.
func TestValidateOrgsExist_DeduplicatesKeys(t *testing.T) {
	dir := t.TempDir()
	writeOrgCSV(t, dir, `sonarqube_org_key,sonarcloud_org_key
org-a,shared
org-b,shared
org-c,shared
`)
	lookup := newFakeOrgs("shared")
	if err := validateOrgsExist(context.Background(), lookup, dir, "ent", "", false); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if len(lookup.calls) != 1 {
		t.Errorf("expected 1 lookup call, got %d (%v)", len(lookup.calls), lookup.calls)
	}
	if len(lookup.calls[0]) != 1 || lookup.calls[0][0] != "shared" {
		t.Errorf("expected one key 'shared', got %v", lookup.calls[0])
	}
}

// Empty default + appliedDefault=true is treated as a no-op rather
// than a failure (the caller is responsible for not calling us in this
// shape, but be defensive).
func TestValidateOrgsExist_EmptyDefaultNoOp(t *testing.T) {
	dir := t.TempDir()
	lookup := newFakeOrgs()
	if err := validateOrgsExist(context.Background(), lookup, dir, "ent", "", true); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if len(lookup.calls) != 0 {
		t.Errorf("must not query SQC when defaultOrg is empty")
	}
}

// Sanity: the spec'd CSV-variant message contains both the org key
// and the enterprise key.
func TestCSVOrgMissingError_MessageShape(t *testing.T) {
	err := csvOrgMissingError("my-org", "/tmp/migration/organizations.csv", "my-ent")
	if !strings.Contains(err.Error(), `"my-org"`) {
		t.Error("missing org key in message")
	}
	if !strings.Contains(err.Error(), `"/tmp/migration/organizations.csv"`) {
		t.Error("missing CSV path in message")
	}
	if !strings.Contains(err.Error(), `"my-ent"`) {
		t.Error("missing enterprise key in message")
	}
}

// Issue #138: a static-prefix pattern (no <ORGANIZATION_KEY>) whose prefix matches an
// existing SonarQube Cloud organization is ambiguous → exit 2.
func TestValidatePatternOrgCollision(t *testing.T) {
	ctx := context.Background()

	t.Run("static prefix collides with org → abort", func(t *testing.T) {
		lookup := newFakeOrgs("acme")
		err := validatePatternOrgCollision(ctx, lookup, "acme_<ORIGINAL_PROJECT_KEY>")
		var ec *common.ExitCodeError
		if !errors.As(err, &ec) || ec.Code != 2 {
			t.Fatalf("expected exit code 2, got %v (%T)", err, err)
		}
	})

	t.Run("static prefix with no matching org → ok", func(t *testing.T) {
		lookup := newFakeOrgs("some-other-org")
		if err := validatePatternOrgCollision(ctx, lookup, "acme_<ORIGINAL_PROJECT_KEY>"); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("pattern using <ORGANIZATION_KEY> skips the check entirely", func(t *testing.T) {
		lookup := newFakeOrgs("acme")
		// Even though "acme" exists, an org-scoped pattern is never ambiguous.
		if err := validatePatternOrgCollision(ctx, lookup, DefaultProjectKeyPattern); err != nil {
			t.Fatalf("expected no error for org-scoped pattern, got %v", err)
		}
		if len(lookup.calls) != 0 {
			t.Fatalf("org-scoped pattern must not query organizations, got %d calls", len(lookup.calls))
		}
	})

	t.Run("bare keep-unchanged pattern has no prefix to collide", func(t *testing.T) {
		lookup := newFakeOrgs("acme")
		if err := validatePatternOrgCollision(ctx, lookup, "<ORIGINAL_PROJECT_KEY>"); err != nil {
			t.Fatalf("expected no error for bare pattern, got %v", err)
		}
	})
}
