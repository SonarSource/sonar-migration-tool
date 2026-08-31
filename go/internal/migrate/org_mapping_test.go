// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

func writeOrgCSV(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "organizations.csv"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

// Issue #279: when every sonarcloud_org_key column is empty and no
// default_organization is provided, migrate should fail fast with exit
// code 2.
func TestApplyOrgMapping_AllEmptyNoDefaultReturnsExit2(t *testing.T) {
	dir := t.TempDir()
	writeOrgCSV(t, dir, `sonarqube_org_key,sonarcloud_org_key
org-a,
org-b,
org-c,
`)
	_, err := applyOrgMapping(context.Background(), newFakeOrgs(), dir, "", "ent", discardLogger())
	if err == nil {
		t.Fatal("expected an error when no mapping is defined and no default")
	}
	var ec *common.ExitCodeError
	if !errors.As(err, &ec) || ec.Code != 2 {
		t.Errorf("expected exit code 2 error, got %v (%T)", err, err)
	}
	if !strings.Contains(err.Error(), "No organization mapping has been defined") {
		t.Errorf("error should carry the spec'd message, got %q", err.Error())
	}
}

// Issue #281: empty CSV + default_organization → rewrite the CSV with
// the default in every row, no error.
func TestApplyOrgMapping_AllEmptyWithDefaultFillsRows(t *testing.T) {
	dir := t.TempDir()
	writeOrgCSV(t, dir, `sonarqube_org_key,sonarcloud_org_key,server_url
org-a,,https://sqs-a.example.com
org-b,,https://sqs-b.example.com
`)
	applied, err := applyOrgMapping(context.Background(), newFakeOrgs("my-cloud-org"), dir, "my-cloud-org", "ent", discardLogger())
	if err != nil {
		t.Fatalf("expected success with default_organization, got %v", err)
	}
	if !applied {
		t.Error("appliedDefault must be true when CSV was empty and default was applied")
	}

	// CSV must now carry the default for every row, original columns preserved.
	f, err := os.Open(filepath.Join(dir, "organizations.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if records[0][0] != "sonarqube_org_key" || records[0][1] != "sonarcloud_org_key" || records[0][2] != "server_url" {
		t.Errorf("headers preserved: got %v", records[0])
	}
	if records[1][1] != "my-cloud-org" || records[2][1] != "my-cloud-org" {
		t.Errorf("expected my-cloud-org in every row, got %v", records)
	}
	if records[1][2] != "https://sqs-a.example.com" {
		t.Errorf("server_url should be preserved, got %q", records[1][2])
	}
}

// Issue #281: mapped CSV + default_organization → ignore the default,
// emit a WARN, leave the CSV untouched.
func TestApplyOrgMapping_MappedWithDefaultWarnsAndIgnores(t *testing.T) {
	dir := t.TempDir()
	original := `sonarqube_org_key,sonarcloud_org_key
org-a,real-cloud-org
org-b,
`
	writeOrgCSV(t, dir, original)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	applied, err := applyOrgMapping(context.Background(), newFakeOrgs(), dir, "default-cloud-org", "ent", logger)
	if err != nil {
		t.Fatalf("expected nil with mapped CSV + default, got %v", err)
	}
	if applied {
		t.Error("appliedDefault must be false when CSV already has mappings")
	}
	// CSV must be unchanged.
	got, _ := os.ReadFile(filepath.Join(dir, "organizations.csv"))
	if string(got) != original {
		t.Errorf("CSV must not be modified when mapping is already defined.\ngot:  %q\nwant: %q", string(got), original)
	}
	// WARN must mention the verbatim spec'd message.
	if !strings.Contains(buf.String(), "Since organizations.csv mapping is defined, the provided default organization parameter is ignored") {
		t.Errorf("expected the spec'd WARN message, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("expected WARN level, got %q", buf.String())
	}
}

// Mapped CSV + no default → no warning, no error, CSV untouched.
func TestApplyOrgMapping_MappedNoDefaultPasses(t *testing.T) {
	dir := t.TempDir()
	writeOrgCSV(t, dir, `sonarqube_org_key,sonarcloud_org_key
org-a,
org-b,real-cloud-org
`)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if _, err := applyOrgMapping(context.Background(), newFakeOrgs(), dir, "", "ent", logger); err != nil {
		t.Errorf("expected nil for partial mapping, got %v", err)
	}
	if strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("must not emit a WARN when no default is provided, got %q", buf.String())
	}
}

// SKIPPED rows count as mapped → no error, no default applied.
func TestApplyOrgMapping_OnlySkippedRowsPass(t *testing.T) {
	dir := t.TempDir()
	writeOrgCSV(t, dir, `sonarqube_org_key,sonarcloud_org_key
org-a,SKIPPED
org-b,SKIPPED
`)
	if _, err := applyOrgMapping(context.Background(), newFakeOrgs(), dir, "", "ent", discardLogger()); err != nil {
		t.Errorf("expected nil when all rows are SKIPPED, got %v", err)
	}
}

// Missing file → still surfaces the code-2 error (can't synthesize
// without rows).
func TestApplyOrgMapping_MissingFileReturnsExit2(t *testing.T) {
	dir := t.TempDir()
	if _, err := applyOrgMapping(context.Background(), newFakeOrgs(), dir, "any-default", "ent", discardLogger()); err == nil {
		t.Fatal("expected error when organizations.csv is missing")
	}
}

// Issue #550: a purely-numeric sonarcloud_org_key (e.g. "12345") used to
// be silently coerced to float64 by structure.LoadCSV, so hasMapping's
// `val, _ := row["sonarcloud_org_key"].(string)` type assertion failed
// and read back "" — treating a perfectly-mapped organization as
// unmapped and dropping it from the migration with no error or warning.
// With the fix, LoadCSV keeps "*_key" columns as raw strings, so a
// numeric-looking org key is still recognized as "already mapped".
func TestApplyOrgMapping_NumericOrgKeyRecognizedAsMapped(t *testing.T) {
	dir := t.TempDir()
	writeOrgCSV(t, dir, `sonarqube_org_key,sonarcloud_org_key
org-a,12345
org-b,67890
`)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	applied, err := applyOrgMapping(context.Background(), newFakeOrgs(), dir, "", "ent", logger)
	if err != nil {
		t.Fatalf("expected nil error for a numeric-looking but valid org mapping, got %v", err)
	}
	if applied {
		t.Error("appliedDefault must be false when the CSV already has a (numeric-looking) mapping")
	}

	// The CSV must be left untouched: it must not have been rewritten
	// with a default because it was misread as unmapped.
	got, readErr := os.ReadFile(filepath.Join(dir, "organizations.csv"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := "sonarqube_org_key,sonarcloud_org_key\norg-a,12345\norg-b,67890\n"
	if string(got) != want {
		t.Errorf("CSV must not be modified when a numeric-looking mapping is already defined.\ngot:  %q\nwant: %q", string(got), want)
	}
}

// Issue #550: a wrong --default_organization used to be validated against
// SonarQube Cloud only AFTER writeOrgCSVWithDefault had already persisted
// it to organizations.csv (validateOrgsExist ran later, in RunMigrate). A
// retry with a corrected --default_organization then found the CSV already
// "mapped" by the failed attempt's leftover value, so hasMapping silently
// ignored the correction with just a WARN.
//
// With the fix, applyOrgMapping validates defaultOrg against the live API
// (via the injected orgLookup) BEFORE writing anything. This reproduces the
// exact two-attempt scenario: first with a bogus org (must fail without
// touching the file), then immediately with a corrected org (must apply
// cleanly, proving the first failed attempt left no residue behind).
func TestApplyOrgMapping_InvalidDefaultThenCorrectedRetryApplies(t *testing.T) {
	dir := t.TempDir()
	original := `sonarqube_org_key,sonarcloud_org_key
org-a,
org-b,
`
	writeOrgCSV(t, dir, original)

	// Only "real-cloud-org" exists on the (mock) target — "bogus-org" does not.
	lookup := newFakeOrgs("real-cloud-org")

	// First attempt: wrong --default_organization.
	applied, err := applyOrgMapping(context.Background(), lookup, dir, "bogus-org", "ent", discardLogger())
	if err == nil {
		t.Fatal("expected an error for a --default_organization that does not exist on the target")
	}
	if applied {
		t.Error("appliedDefault must be false when the default organization failed validation")
	}
	var ec *common.ExitCodeError
	if !errors.As(err, &ec) || ec.Code != 3 {
		t.Errorf("expected exit code 3 error, got %v (%T)", err, err)
	}

	// The CSV must be completely untouched by the failed attempt.
	got, readErr := os.ReadFile(filepath.Join(dir, "organizations.csv"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("organizations.csv must not be modified by a failed --default_organization.\ngot:  %q\nwant: %q", string(got), original)
	}

	// Second attempt (retry): corrected --default_organization must apply
	// cleanly, because the first failed attempt left hasMapping false.
	applied, err = applyOrgMapping(context.Background(), lookup, dir, "real-cloud-org", "ent", discardLogger())
	if err != nil {
		t.Fatalf("expected the corrected retry to succeed, got %v", err)
	}
	if !applied {
		t.Error("appliedDefault must be true once the corrected default_organization is applied")
	}

	f, err := os.Open(filepath.Join(dir, "organizations.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if records[1][1] != "real-cloud-org" || records[2][1] != "real-cloud-org" {
		t.Errorf("expected real-cloud-org in every row after the corrected retry, got %v", records)
	}
}
