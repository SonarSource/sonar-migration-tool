// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGetGateConditions(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["getGateConditions"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("getGateConditions: %v", err)
	}

	items, _ := e.Store.ReadAll("getGateConditions")
	if len(items) == 0 {
		t.Error("expected getGateConditions output")
	}
}

func TestGetProfileBackups(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["getProfileBackups"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("getProfileBackups: %v", err)
	}

	items, _ := e.Store.ReadAll("getProfileBackups")
	if len(items) == 0 {
		t.Error("expected getProfileBackups output")
	}
}

// seedMigrateRunDir writes a fake migrate run under exportDir: a
// run_meta.json marker (distinguishes it from a reset run, which uses
// clear.json) and a createProjects/results.1.jsonl JSONL with the
// supplied records. Returns the run path.
func seedMigrateRunDir(t *testing.T, exportDir, runID string, records []map[string]any) string {
	t.Helper()
	runDir := filepath.Join(exportDir, runID)
	if err := os.MkdirAll(filepath.Join(runDir, "createProjects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "run_meta.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(runDir, "createProjects", "results.1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, r := range records {
		b, _ := json.Marshal(r)
		f.Write(b)
		f.Write([]byte("\n"))
	}
	return runDir
}

// #381 follow-up: getCreatedProjects now scopes deletion to projects
// the migrate tool ACTUALLY created (read from prior migrate run
// dirs' createProjects JSONL) instead of listing every project in the
// SonarCloud org via /api/projects/search. The new behaviour:
//
//   - Reads every subdir of exportDir that has run_meta.json (= a
//     migrate run) and unions their createProjects records.
//   - Dedupes by cloud_project_key so re-runs that re-touch the same
//     project don't produce duplicate delete attempts.
//   - Skips records whose sonarcloud_org_key is empty / SKIPPED.
//   - When Executor.ResetConfirmedOrgs is set (the operator confirmed
//     a subset via the interactive prompt), records whose cloud org
//     isn't confirmed are filtered out so deleteProjects never sees
//     them.
//   - Emits {key, sonarcloud_org_key, source_key, server_url} per
//     project, with `key` carrying the CLOUD key (the shape
//     deleteProjects extracts).
func TestGetCreatedProjects_UnionsMigrateRuns(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	// Two migrate runs with overlapping cloud_project_key (dedup
	// must keep exactly one record).
	seedMigrateRunDir(t, dir, "2026-06-12-01", []map[string]any{
		{"key": "src-a", "cloud_project_key": "cloud-a", "sonarcloud_org_key": "org1"},
		{"key": "src-b", "cloud_project_key": "cloud-b", "sonarcloud_org_key": "org1"},
	})
	seedMigrateRunDir(t, dir, "2026-06-12-02", []map[string]any{
		{"key": "src-b", "cloud_project_key": "cloud-b", "sonarcloud_org_key": "org1"},
		{"key": "src-c", "cloud_project_key": "cloud-c", "sonarcloud_org_key": "org2"},
	})
	// Reset run (clear.json instead of run_meta.json) must be ignored.
	resetDir := filepath.Join(dir, "2026-06-12-03")
	os.MkdirAll(filepath.Join(resetDir, "createProjects"), 0o755)
	os.WriteFile(filepath.Join(resetDir, "clear.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(resetDir, "createProjects", "results.1.jsonl"),
		[]byte(`{"cloud_project_key":"cloud-LEAK","sonarcloud_org_key":"org1"}`+"\n"), 0o644)

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["getCreatedProjects"].Run(context.Background(), e); err != nil {
		t.Fatalf("getCreatedProjects: %v", err)
	}

	items, _ := e.Store.ReadAll("getCreatedProjects")
	gotKeys := map[string]string{}
	for _, raw := range items {
		var rec map[string]any
		_ = json.Unmarshal(raw, &rec)
		k, _ := rec["key"].(string)
		org, _ := rec["sonarcloud_org_key"].(string)
		gotKeys[k] = org
	}
	want := map[string]string{
		"cloud-a": "org1",
		"cloud-b": "org1",
		"cloud-c": "org2",
	}
	if len(gotKeys) != len(want) {
		t.Errorf("got %d records, want %d: %+v", len(gotKeys), len(want), gotKeys)
	}
	for k, org := range want {
		if got, ok := gotKeys[k]; !ok || got != org {
			t.Errorf("missing or wrong org for %s: got %q, want %q", k, got, org)
		}
	}
	if _, leaked := gotKeys["cloud-LEAK"]; leaked {
		t.Error("reset run's createProjects must NOT contribute records (clear.json, not run_meta.json)")
	}
}

// #381: Executor.ResetConfirmedOrgs filters per-org. Records whose
// sonarcloud_org_key isn't in the confirmed set are excluded.
func TestGetCreatedProjects_HonorsResetConfirmedOrgs(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	e.ResetConfirmedOrgs = map[string]bool{"org1": true}

	seedMigrateRunDir(t, dir, "2026-06-12-01", []map[string]any{
		{"cloud_project_key": "cloud-a", "sonarcloud_org_key": "org1"},
		{"cloud_project_key": "cloud-b", "sonarcloud_org_key": "org2"},
		{"cloud_project_key": "cloud-c", "sonarcloud_org_key": "org1"},
		{"cloud_project_key": "cloud-d", "sonarcloud_org_key": "SKIPPED"},
	})

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["getCreatedProjects"].Run(context.Background(), e); err != nil {
		t.Fatalf("getCreatedProjects: %v", err)
	}

	items, _ := e.Store.ReadAll("getCreatedProjects")
	keys := map[string]bool{}
	for _, raw := range items {
		var rec map[string]any
		_ = json.Unmarshal(raw, &rec)
		k, _ := rec["key"].(string)
		keys[k] = true
	}
	if !keys["cloud-a"] || !keys["cloud-c"] {
		t.Errorf("expected cloud-a and cloud-c (org1) to pass through, got %+v", keys)
	}
	if keys["cloud-b"] {
		t.Error("cloud-b (org2 — not confirmed) must be filtered out")
	}
	if keys["cloud-d"] {
		t.Error("cloud-d (SKIPPED org) must be filtered out")
	}
}

// #381: when no prior migrate run exists, getCreatedProjects writes no
// records (reset has nothing safe to delete). It must not error so the
// rest of the reset plan can still run (resetGlobalSettings etc.).
func TestGetCreatedProjects_NoMigrateRunIsNoOp(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["getCreatedProjects"].Run(context.Background(), e); err != nil {
		t.Fatalf("getCreatedProjects: %v", err)
	}
	items, _ := e.Store.ReadAll("getCreatedProjects")
	if len(items) != 0 {
		t.Errorf("expected no records when there are no migrate runs, got %d", len(items))
	}
}

func TestGetProjectIdsEmptyOrg(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	// createProjects with empty org key should be skipped.
	w, _ := e.Store.Writer("createProjects")
	b, _ := json.Marshal(map[string]any{"cloud_project_key": "x", "sonarcloud_org_key": ""})
	w.WriteOne(b)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["getProjectIds"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("getProjectIds: %v", err)
	}
}

func TestGetOrgReposEmptyOrg(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	w, _ := e.Store.Writer("generateOrganizationMappings")
	b, _ := json.Marshal(map[string]any{"sonarcloud_org_key": ""})
	w.WriteOne(b)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["getOrgRepos"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("getOrgRepos: %v", err)
	}
}
