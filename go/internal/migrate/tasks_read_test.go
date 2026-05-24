package migrate

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestGetCreatedProjects(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	reg := BuildMigrateRegistry(RegisterAll())
	err := reg["getCreatedProjects"].Run(context.Background(), e)
	if err != nil {
		t.Fatalf("getCreatedProjects: %v", err)
	}

	items, _ := e.Store.ReadAll("getCreatedProjects")
	if len(items) == 0 {
		t.Error("expected getCreatedProjects output")
	}
}

// Regression: getCreatedProjects must iterate per-org (one
// /api/projects/search call per organization), not per createProjects
// record. Reset fed deleteProjects through this task, and the old
// per-record iteration produced one chunk per createProjects entry —
// for a tenant with N projects in M orgs, deleteProjects would attempt
// N deletions per record, returning 404 for every duplicate. Pin the
// per-org iteration shape so we never regress.
func TestGetCreatedProjectsDeduplicatesByOrg(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	setupCreateOutputs(t, e)

	// Write extra createProjects records for the same org. If the
	// iteration source regressed back to createProjects, getCreatedProjects
	// would call /api/projects/search once per record and write one
	// chunk per record. Iterating generateOrganizationMappings (one
	// record per org) caps it at one chunk per org.
	w, _ := e.Store.Writer("createProjects")
	for i := 0; i < 9; i++ {
		extra, _ := json.Marshal(map[string]any{
			"sonarcloud_org_key": testCloudOrg,
			"cloud_project_key":  fmt.Sprintf("cloud-org1_proj%d", i+2),
		})
		w.WriteOne(extra)
	}

	reg := BuildMigrateRegistry(RegisterAll())
	if err := reg["getCreatedProjects"].Run(context.Background(), e); err != nil {
		t.Fatalf("getCreatedProjects: %v", err)
	}

	items, _ := e.Store.ReadAll("getCreatedProjects")
	// Mock returns 1 component per /api/projects/search call. With one
	// org we expect exactly 1 component. Old behaviour: 10 (1 per
	// createProjects record).
	if len(items) != 1 {
		t.Errorf("expected 1 item (one chunk per org), got %d — iteration likely regressed to per-record", len(items))
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
