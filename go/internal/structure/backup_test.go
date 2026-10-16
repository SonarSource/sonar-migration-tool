// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var backupTestTime = time.Date(2026, 10, 16, 14, 3, 12, 0, time.Local)

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// #503: an edited organizations.csv and projects.csv survive as
// timestamped copies with identical bytes.
func TestBackupEditableCSVs_CopiesExistingFiles(t *testing.T) {
	dir := t.TempDir()
	orgs := "server_url,sonarcloud_org_key\nhttp://sqs,my-org\n"
	projects := "key,sonarcloud_org_key\np1,other-org\n"
	writeTestFile(t, filepath.Join(dir, OrganizationsCSVFileName), orgs)
	writeTestFile(t, filepath.Join(dir, ProjectsCSVFileName), projects)

	got, err := BackupEditableCSVs(dir, backupTestTime)
	if err != nil {
		t.Fatalf("BackupEditableCSVs: %v", err)
	}
	want := []string{
		filepath.Join(dir, "organizations.csv.bak-20261016-140312"),
		filepath.Join(dir, "projects.csv.bak-20261016-140312"),
	}
	if len(got) != len(want) {
		t.Fatalf("backups: got %v, want %v", got, want)
	}
	for i, path := range want {
		if got[i] != path {
			t.Errorf("backup %d: got %s, want %s", i, got[i], path)
		}
	}
	for path, contents := range map[string]string{want[0]: orgs, want[1]: projects} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if string(data) != contents {
			t.Errorf("%s: got %q, want %q", path, data, contents)
		}
	}
}

// A first extract has nothing to back up, and an empty CSV holds no edits.
func TestBackupEditableCSVs_SkipsMissingAndEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, ProjectsCSVFileName), "")

	got, err := BackupEditableCSVs(dir, backupTestTime)
	if err != nil {
		t.Fatalf("BackupEditableCSVs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no backups, got %v", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected only projects.csv in dir, got %d entries", len(entries))
	}
}

// An existing backup is never overwritten.
func TestBackupEditableCSVs_RefusesToOverwriteBackup(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, OrganizationsCSVFileName), "new\n")
	existing := filepath.Join(dir, "organizations.csv.bak-20261016-140312")
	writeTestFile(t, existing, "edited\n")

	if _, err := BackupEditableCSVs(dir, backupTestTime); err == nil {
		t.Fatal("expected an error when the backup name already exists")
	}
	data, _ := os.ReadFile(existing)
	if string(data) != "edited\n" {
		t.Errorf("existing backup was changed: %q", data)
	}
}
