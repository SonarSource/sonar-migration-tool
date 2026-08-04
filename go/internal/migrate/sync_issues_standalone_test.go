// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestApplyDefaults(t *testing.T) {
	cfg := SyncIssuesConfig{}
	cfg.applyDefaults()

	if cfg.Concurrency != 25 {
		t.Errorf("Concurrency = %d, want 25", cfg.Concurrency)
	}
	if cfg.Timeout != 60 {
		t.Errorf("Timeout = %d, want 60", cfg.Timeout)
	}
	if cfg.ExportDirectory != "./migration-files/" {
		t.Errorf("ExportDirectory = %q, want ./migration-files/", cfg.ExportDirectory)
	}
	if cfg.URL != "https://sonarcloud.io/" {
		t.Errorf("URL = %q, want https://sonarcloud.io/", cfg.URL)
	}
	if cfg.ProjectKeyPattern != DefaultProjectKeyPattern {
		t.Errorf("ProjectKeyPattern = %q, want %q", cfg.ProjectKeyPattern, DefaultProjectKeyPattern)
	}
}

func TestApplyDefaultsPreservesExplicitValuesAndAddsTrailingSlash(t *testing.T) {
	cfg := SyncIssuesConfig{
		Concurrency:       3,
		Timeout:           5,
		ExportDirectory:   "/tmp/custom",
		URL:               "https://sqc.example.com",
		ProjectKeyPattern: "acme_<ORIGINAL_PROJECT_KEY>",
	}
	cfg.applyDefaults()

	if cfg.Concurrency != 3 {
		t.Errorf("Concurrency = %d, want 3 (explicit value preserved)", cfg.Concurrency)
	}
	if cfg.Timeout != 5 {
		t.Errorf("Timeout = %d, want 5 (explicit value preserved)", cfg.Timeout)
	}
	if cfg.ExportDirectory != "/tmp/custom" {
		t.Errorf("ExportDirectory = %q, want /tmp/custom (explicit value preserved)", cfg.ExportDirectory)
	}
	if cfg.URL != "https://sqc.example.com/" {
		t.Errorf("URL = %q, want trailing slash appended", cfg.URL)
	}
	if cfg.ProjectKeyPattern != "acme_<ORIGINAL_PROJECT_KEY>" {
		t.Errorf("ProjectKeyPattern = %q, want explicit value preserved", cfg.ProjectKeyPattern)
	}
}

// writeSyncTargetCSVs writes minimal projects.csv / organizations.csv
// fixtures into dir — only the columns resolveSyncTargets actually reads.
func writeSyncTargetCSVs(t *testing.T, dir, projectsCSV, orgsCSV string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "projects.csv"), []byte(projectsCSV), 0o644); err != nil {
		t.Fatalf("write projects.csv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "organizations.csv"), []byte(orgsCSV), 0o644); err != nil {
		t.Fatalf("write organizations.csv: %v", err)
	}
}

func TestResolveSyncTargets(t *testing.T) {
	orgsCSV := "sonarqube_org_key,sonarcloud_org_key\n" +
		"src-org,my-cloud-org\n" +
		"unmapped-org,\n"

	tests := []struct {
		name        string
		projectsCSV string
		pattern     string
		projectKeys []string
		want        []syncTarget
	}{
		{
			name: "renders cloud key from default pattern",
			projectsCSV: "key,server_url,sonarqube_org_key\n" +
				"proj-a,https://sqs.example.com,src-org\n",
			pattern: DefaultProjectKeyPattern,
			want: []syncTarget{
				{Key: "proj-a", ServerURL: "https://sqs.example.com", CloudProjectKey: "my-cloud-org_proj-a", OrgKey: "my-cloud-org"},
			},
		},
		{
			name: "custom pattern",
			projectsCSV: "key,server_url,sonarqube_org_key\n" +
				"proj-a,https://sqs.example.com,src-org\n",
			pattern: "acme_<ORIGINAL_PROJECT_KEY>",
			want: []syncTarget{
				{Key: "proj-a", ServerURL: "https://sqs.example.com", CloudProjectKey: "acme_proj-a", OrgKey: "my-cloud-org"},
			},
		},
		{
			name: "unmapped org is skipped",
			projectsCSV: "key,server_url,sonarqube_org_key\n" +
				"proj-a,https://sqs.example.com,src-org\n" +
				"proj-b,https://sqs.example.com,unmapped-org\n",
			pattern: DefaultProjectKeyPattern,
			want: []syncTarget{
				{Key: "proj-a", ServerURL: "https://sqs.example.com", CloudProjectKey: "my-cloud-org_proj-a", OrgKey: "my-cloud-org"},
			},
		},
		{
			name: "unknown org key (not in organizations.csv) is skipped",
			projectsCSV: "key,server_url,sonarqube_org_key\n" +
				"proj-a,https://sqs.example.com,src-org\n" +
				"proj-c,https://sqs.example.com,never-heard-of-it\n",
			pattern: DefaultProjectKeyPattern,
			want: []syncTarget{
				{Key: "proj-a", ServerURL: "https://sqs.example.com", CloudProjectKey: "my-cloud-org_proj-a", OrgKey: "my-cloud-org"},
			},
		},
		{
			name: "projectKeys filter narrows to the requested project",
			projectsCSV: "key,server_url,sonarqube_org_key\n" +
				"proj-a,https://sqs.example.com,src-org\n" +
				"proj-b,https://sqs.example.com,src-org\n",
			pattern:     DefaultProjectKeyPattern,
			projectKeys: []string{"proj-b"},
			want: []syncTarget{
				{Key: "proj-b", ServerURL: "https://sqs.example.com", CloudProjectKey: "my-cloud-org_proj-b", OrgKey: "my-cloud-org"},
			},
		},
		{
			name:        "empty projects.csv — no targets",
			projectsCSV: "key,server_url,sonarqube_org_key\n",
			pattern:     DefaultProjectKeyPattern,
			want:        nil,
		},
		{
			name: "row with empty key is skipped",
			projectsCSV: "key,server_url,sonarqube_org_key\n" +
				",https://sqs.example.com,src-org\n" +
				"proj-a,https://sqs.example.com,src-org\n",
			pattern: DefaultProjectKeyPattern,
			want: []syncTarget{
				{Key: "proj-a", ServerURL: "https://sqs.example.com", CloudProjectKey: "my-cloud-org_proj-a", OrgKey: "my-cloud-org"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSyncTargetCSVs(t, dir, tc.projectsCSV, orgsCSV)

			got, err := resolveSyncTargets(dir, tc.pattern, tc.projectKeys)
			if err != nil {
				t.Fatalf("resolveSyncTargets: %v", err)
			}
			sort.Slice(got, func(i, j int) bool { return got[i].Key < got[j].Key })
			if len(got) != len(tc.want) {
				t.Fatalf("got %d targets, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("target[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestResolveSyncTargetsLoadProjectsCSVError(t *testing.T) {
	dir := t.TempDir()
	// Malformed CSV (inconsistent field count) makes the reader return an
	// error rather than the "missing file" nil,nil case.
	if err := os.WriteFile(filepath.Join(dir, "projects.csv"), []byte("key,server_url\nproj-a\n"), 0o644); err != nil {
		t.Fatalf("write projects.csv: %v", err)
	}

	if _, err := resolveSyncTargets(dir, DefaultProjectKeyPattern, nil); err == nil {
		t.Fatal("resolveSyncTargets: want error for malformed projects.csv, got nil")
	}
}

func TestResolveSyncTargetsLoadOrganizationsCSVError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "projects.csv"), []byte("key,server_url,sonarqube_org_key\nproj-a,https://sqs.example.com,src-org\n"), 0o644); err != nil {
		t.Fatalf("write projects.csv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "organizations.csv"), []byte("sonarqube_org_key,sonarcloud_org_key\nsrc-org\n"), 0o644); err != nil {
		t.Fatalf("write organizations.csv: %v", err)
	}

	if _, err := resolveSyncTargets(dir, DefaultProjectKeyPattern, nil); err == nil {
		t.Fatal("resolveSyncTargets: want error for malformed organizations.csv, got nil")
	}
}

// newSyncIssuesTestServer builds a minimal Cloud mock server sufficient for
// RunSyncIssues's own control flow: organization lookups (validateOrgsExist
// / validatePatternOrgCollision) succeed for any key. No issues/hotspots
// endpoints are needed because the test fixtures below carry no source
// issue/hotspot extract data, so syncProjectIssues/syncProjectHotspots
// return immediately without making any further Cloud calls.
func newSyncIssuesTestServer() *httptest.Server {
	return newMockCloudServer()
}

// writeSyncIssuesFixture wires the minimal on-disk state RunSyncIssues
// needs: a mapped organizations.csv, a single-project projects.csv pointing
// at testServerURL, and an extract-01/extract.json so GetUniqueExtracts
// resolves testServerURL -> extract-01.
func writeSyncIssuesFixture(t *testing.T, dir string) {
	t.Helper()
	writeSyncTargetCSVs(t, dir,
		"key,server_url,sonarqube_org_key\n"+
			"proj-a,"+testServerURL+",src-org\n",
		"sonarqube_org_key,sonarcloud_org_key\n"+
			"src-org,my-cloud-org\n")
	writeJSON(filepath.Join(dir, "extract-01", "extract.json"),
		map[string]any{"url": testServerURL, "edition": "enterprise"})
}

func TestRunSyncIssuesHappyPath(t *testing.T) {
	cloudSrv := newSyncIssuesTestServer()
	defer cloudSrv.Close()

	dir := t.TempDir()
	writeSyncIssuesFixture(t, dir)

	cfg := SyncIssuesConfig{
		URL:             cloudSrv.URL,
		Token:           "test-token",
		ExportDirectory: dir,
		Concurrency:     5,
		Timeout:         10,
	}

	summary, err := RunSyncIssues(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunSyncIssues: %v", err)
	}
	if summary.ProjectsSynced != 1 {
		t.Errorf("ProjectsSynced = %d, want 1", summary.ProjectsSynced)
	}
	if summary.IssuesSynced != 0 || summary.HotspotsSynced != 0 {
		t.Errorf("expected no synced issues/hotspots (no source extract data), got %+v", summary)
	}
}

func TestRunSyncIssuesInvalidProjectKeyPattern(t *testing.T) {
	cloudSrv := newSyncIssuesTestServer()
	defer cloudSrv.Close()

	dir := t.TempDir()
	writeSyncIssuesFixture(t, dir)

	cfg := SyncIssuesConfig{
		URL:               cloudSrv.URL,
		Token:             "test-token",
		ExportDirectory:   dir,
		ProjectKeyPattern: "no-placeholders-here",
	}

	if _, err := RunSyncIssues(context.Background(), cfg); err == nil {
		t.Fatal("RunSyncIssues: want error for invalid project_key_pattern, got nil")
	}
}

func TestRunSyncIssuesMissingOrgMapping(t *testing.T) {
	cloudSrv := newSyncIssuesTestServer()
	defer cloudSrv.Close()

	dir := t.TempDir()
	// organizations.csv deliberately absent and no DefaultOrganization —
	// applyOrgMapping must reject the run (#279).
	if err := os.WriteFile(filepath.Join(dir, "projects.csv"),
		[]byte("key,server_url,sonarqube_org_key\nproj-a,"+testServerURL+",src-org\n"), 0o644); err != nil {
		t.Fatalf("write projects.csv: %v", err)
	}

	cfg := SyncIssuesConfig{
		URL:             cloudSrv.URL,
		Token:           "test-token",
		ExportDirectory: dir,
	}

	if _, err := RunSyncIssues(context.Background(), cfg); err == nil {
		t.Fatal("RunSyncIssues: want error for missing organization mapping, got nil")
	}
}

func TestRunSyncIssuesNoMatchingTargets(t *testing.T) {
	cloudSrv := newSyncIssuesTestServer()
	defer cloudSrv.Close()

	dir := t.TempDir()
	writeSyncIssuesFixture(t, dir)

	cfg := SyncIssuesConfig{
		URL:             cloudSrv.URL,
		Token:           "test-token",
		ExportDirectory: dir,
		ProjectKeys:     []string{"does-not-exist"},
	}

	summary, err := RunSyncIssues(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunSyncIssues: %v", err)
	}
	if summary.ProjectsSynced != 0 {
		t.Errorf("ProjectsSynced = %d, want 0 when no project matches the filter", summary.ProjectsSynced)
	}
}
