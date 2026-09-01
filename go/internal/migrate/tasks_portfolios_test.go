// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

func TestTransformPortfolioRegex(t *testing.T) {
	cases := []struct {
		name    string
		regex   string
		orgKeys []string
		pattern string
		want    string
	}{
		{"anchored simple",
			"^backend-", []string{"org1"}, DefaultProjectKeyPattern, "^org1_backend-"},
		{"anchored char class",
			"^[A-Z].*", []string{"org1"}, DefaultProjectKeyPattern, "^org1_[A-Z].*"},
		{"unanchored",
			"backend", []string{"org1"}, DefaultProjectKeyPattern, "org1_backend"},
		{"two orgs alternation",
			"^foo", []string{"org1", "org2"}, DefaultProjectKeyPattern,
			"^(?:org1_|org2_)foo"},
		{"empty regex stays empty",
			"", []string{"org1"}, DefaultProjectKeyPattern, ""},
		{"no orgs returns original",
			"^foo", nil, DefaultProjectKeyPattern, "^foo"},
		{"org key with regex metachars is quoted",
			"^bar", []string{"org-1.x"}, DefaultProjectKeyPattern, `^org-1\.x_bar`},
		{"duplicate org keys deduplicated",
			"^foo", []string{"org1", "org1"}, DefaultProjectKeyPattern, "^org1_foo"},
		{"static-prefix pattern (no org) uses constant prefix",
			"^foo", []string{"org1", "org2"}, "ACME_CORP_<ORIGINAL_PROJECT_KEY>", "^ACME_CORP_foo"},
		{"keep-unchanged pattern leaves regex as-is",
			"^foo", []string{"org1"}, "<ORIGINAL_PROJECT_KEY>", "^foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := transformPortfolioRegex(tc.regex, tc.orgKeys, tc.pattern)
			if got != tc.want {
				t.Errorf("transformPortfolioRegex(%q, %v, %q): got %q, want %q",
					tc.regex, tc.orgKeys, tc.pattern, got, tc.want)
			}
		})
	}
}

// TestRunConfigurePortfoliosRegex drives runConfigurePortfolios with pre-built
// JSONL inputs and asserts the PATCH /enterprises/portfolios/<id> body
// carries the rewritten regex + resolved organization UUID.
func TestRunConfigurePortfoliosRegex(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()

	var (
		mu        sync.Mutex
		patchBody map[string]any
		patchPath string
	)
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /enterprises/enterprises", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "ent-1", "key": "test-enterprise"},
		})
	})
	apiMux.HandleFunc("PATCH /enterprises/portfolios/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		patchPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&patchBody)
		w.WriteHeader(http.StatusNoContent)
	})
	apiSrv := httptest.NewServer(apiMux)
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	// Stub the extract data the task reads. newTestExecutor wires Mapping to
	// the "extract-01" directory it already created.
	extractTaskDir := filepath.Join(dir, "extract-01", "getPortfolioProjects")
	if err := os.MkdirAll(extractTaskDir, 0o755); err != nil {
		t.Fatalf("mkdir extract task: %v", err)
	}
	writeJSONLLine(t, filepath.Join(extractTaskDir, "results.1.jsonl"), map[string]any{
		"portfolioKey":  "src-portfolio-1",
		"portfolioName": "Backend Portfolio",
		"refKey":        "proj-backend",
		"serverUrl":     testServerURL,
	})

	// createProjects: one project that lives in SQC org "myorg".
	pw, _ := e.Store.Writer("createProjects")
	pw.WriteOne(json.RawMessage(`{"key":"proj-backend","server_url":"` + testServerURL + `","sonarcloud_org_key":"myorg","cloud_project_key":"myorg_proj-backend"}`))

	// createPortfolios: one REGEXP portfolio.
	cpw, _ := e.Store.Writer("createPortfolios")
	cpw.WriteOne(json.RawMessage(`{"source_portfolio_key":"src-portfolio-1","name":"Backend Portfolio","selection_mode":"REGEXP","regexp":"^backend-","cloud_portfolio_id":"cloud-portfolio-1"}`))

	if err := runConfigurePortfolios(context.Background(), e); err != nil {
		t.Fatalf("runConfigurePortfolios: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if patchPath != "/enterprises/portfolios/cloud-portfolio-1" {
		t.Errorf("expected PATCH on cloud-portfolio-1, got %q", patchPath)
	}
	if patchBody["selection"] != "regex" {
		t.Errorf("expected selection=regex, got %v", patchBody["selection"])
	}
	if patchBody["regularExpression"] != "^myorg_backend-" {
		t.Errorf("expected regex rewrite, got %q", patchBody["regularExpression"])
	}
	// branchKey must always be present alongside selection (SQC requirement).
	if branchKey, ok := patchBody["branchKey"]; !ok || branchKey != "" {
		t.Errorf("expected branchKey=\"\", got %v (present=%v)", branchKey, ok)
	}
	// organizationIds is intentionally omitted — the SQC enterprise PATCH
	// treats it as optional and the standard search endpoint doesn't expose
	// the UUIDs anyway.
	if _, ok := patchBody["organizationIds"]; ok {
		t.Errorf("organizationIds should be omitted from the PATCH body, got %v", patchBody["organizationIds"])
	}
}

// TestRunConfigurePortfoliosSkipsManual ensures portfolios that come through
// with selection_mode empty / MANUAL are left untouched here (handled by
// setPortfolioProjects instead).
func TestRunConfigurePortfoliosManualSelectsProjects(t *testing.T) {
	// MANUAL source portfolios are migrated as native project-selection on
	// SQC (selection=projects + projects=[{branchId}]), looking up each
	// project's main branch UUID via /api/project_branches/list.
	cloudMux := http.NewServeMux()
	// Stand up a /api/project_branches/list handler before delegating the
	// rest of the cloud endpoints to the default mock so we control the
	// branch UUIDs.
	cloudMux.HandleFunc("GET /api/project_branches/list", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		// Synthesise a deterministic UUID-shaped id from the project key
		// so the test can assert it back without parsing the input order.
		uuid := "uuid-" + project
		json.NewEncoder(w).Encode(map[string]any{
			"branches": []map[string]any{
				{"name": "feature/x", "isMain": false, "branchId": "feature-" + project},
				{"name": "main", "isMain": true, "branchId": uuid},
			},
		})
	})
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()

	var (
		mu        sync.Mutex
		patchBody map[string]any
	)
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /enterprises/enterprises", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"id": "ent-1", "key": "test-enterprise"}})
	})
	apiMux.HandleFunc("PATCH /enterprises/portfolios/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&patchBody)
		w.WriteHeader(http.StatusNoContent)
	})
	apiSrv := httptest.NewServer(apiMux)
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	// Two SQS projects, both migrated to org "myorg".
	extractTaskDir := filepath.Join(dir, "extract-01", "getPortfolioProjects")
	if err := os.MkdirAll(extractTaskDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, _ := os.Create(filepath.Join(extractTaskDir, "results.1.jsonl"))
	for _, ref := range []string{"sqs-proj-a", "sqs-proj-b"} {
		b, _ := json.Marshal(map[string]any{
			"portfolioKey":  "manual-portfolio",
			"portfolioName": "Manual Portfolio",
			"refKey":        ref,
			"serverUrl":     testServerURL,
		})
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()

	pw, _ := e.Store.Writer("createProjects")
	pw.WriteOne(json.RawMessage(`{"key":"sqs-proj-a","server_url":"` + testServerURL + `","sonarcloud_org_key":"myorg","cloud_project_key":"myorg_sqs-proj-a"}`))
	pw.WriteOne(json.RawMessage(`{"key":"sqs-proj-b","server_url":"` + testServerURL + `","sonarcloud_org_key":"myorg","cloud_project_key":"myorg_sqs-proj-b"}`))

	cpw, _ := e.Store.Writer("createPortfolios")
	cpw.WriteOne(json.RawMessage(`{"source_portfolio_key":"manual-portfolio","name":"Manual Portfolio","selection_mode":"MANUAL","cloud_portfolio_id":"cloud-manual-1"}`))

	if err := runConfigurePortfolios(context.Background(), e); err != nil {
		t.Fatalf("runConfigurePortfolios: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if patchBody["selection"] != "projects" {
		t.Errorf("expected selection=projects for MANUAL portfolio, got %v", patchBody["selection"])
	}
	projects, ok := patchBody["projects"].([]any)
	if !ok || len(projects) != 2 {
		t.Fatalf("expected 2 projects refs, got %v", patchBody["projects"])
	}
	gotIDs := map[string]bool{}
	for _, p := range projects {
		m := p.(map[string]any)
		gotIDs[m["branchId"].(string)] = true
	}
	for _, want := range []string{"uuid-myorg_sqs-proj-a", "uuid-myorg_sqs-proj-b"} {
		if !gotIDs[want] {
			t.Errorf("expected branchId %q in PATCH body, got %v", want, gotIDs)
		}
	}
	if _, hasRegex := patchBody["regularExpression"]; hasRegex {
		t.Error("regularExpression should not be set for MANUAL projects selection")
	}
}

func TestRunConfigurePortfoliosSkipsNoneAndRest(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()

	patchCalled := false
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /enterprises/enterprises", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"id": "ent-1", "key": "test-enterprise"}})
	})
	apiMux.HandleFunc("PATCH /enterprises/portfolios/", func(w http.ResponseWriter, _ *http.Request) {
		patchCalled = true
		w.WriteHeader(http.StatusNoContent)
	})
	apiSrv := httptest.NewServer(apiMux)
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	cpw, _ := e.Store.Writer("createPortfolios")
	cpw.WriteOne(json.RawMessage(`{"source_portfolio_key":"none-portfolio","name":"None","selection_mode":"NONE","cloud_portfolio_id":"cloud-none"}`))
	cpw.WriteOne(json.RawMessage(`{"source_portfolio_key":"rest-portfolio","name":"Rest","selection_mode":"REST","cloud_portfolio_id":"cloud-rest"}`))

	if err := runConfigurePortfolios(context.Background(), e); err != nil {
		t.Fatalf("runConfigurePortfolios: %v", err)
	}
	if patchCalled {
		t.Error("PATCH must not be called for NONE/REST portfolios")
	}
}

// #536 scenario (b): --objects=portfolios (no "projects" category
// selected) must still schedule createPortfolios / setPortfolioProjects
// / configurePortfolios — even though all three declare createProjects
// as a dependency — and runConfigurePortfolios must hit its existing
// "MANUAL portfolio has no resolved cloud projects to migrate" warning
// path gracefully (no crash, no PATCH sent) rather than silently doing
// nothing or erroring, since createProjects never runs in this
// configuration and buildProjectCloudKeyIndex reads an empty
// createProjects output.
func TestRunConfigurePortfoliosWithProjectsExcludedHitsNoResolvedProjectsWarning(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()

	patchCalled := false
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /enterprises/enterprises", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"id": "ent-1", "key": "test-enterprise"}})
	})
	apiMux.HandleFunc("PATCH /enterprises/portfolios/", func(w http.ResponseWriter, _ *http.Request) {
		patchCalled = true
		w.WriteHeader(http.StatusNoContent)
	})
	apiSrv := httptest.NewServer(apiMux)
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	// The "projects" category is not selected — mirrors what a real
	// --objects=portfolios run leaves on the Executor.
	e.Objects = map[string]bool{common.ObjectPortfolios: true}

	var logBuf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// No createProjects records written at all — createProjects is
	// excluded from this run's plan, so its output directory never
	// exists (buildProjectCloudKeyIndex must tolerate this).
	cpw, _ := e.Store.Writer("createPortfolios")
	cpw.WriteOne(json.RawMessage(`{"source_portfolio_key":"manual-portfolio","name":"Manual Portfolio","selection_mode":"MANUAL","cloud_portfolio_id":"cloud-manual-1"}`))

	if err := runConfigurePortfolios(context.Background(), e); err != nil {
		t.Fatalf("runConfigurePortfolios must not error when no projects are known: %v", err)
	}
	if patchCalled {
		t.Error("PATCH must not be sent for a MANUAL portfolio with no resolved cloud projects")
	}
	if !strings.Contains(logBuf.String(), "MANUAL portfolio has no resolved cloud projects to migrate") {
		t.Errorf("expected the existing graceful warning, got logs:\n%s", logBuf.String())
	}

	failures, _ := e.Store.ReadAll("configurePortfolios.failures")
	if len(failures) != 1 {
		t.Fatalf("expected one recorded portfolio failure, got %d", len(failures))
	}
}

// #536 scenario (b), planning level: with --objects=portfolios,
// createPortfolios / setPortfolioProjects / configurePortfolios must
// still be scheduled (their Enterprise-only feature is independent of
// the objects filter) despite each declaring createProjects — which IS
// excluded — as a dependency. This exercises exactly the
// MigrateTargetTasks -> ResolveDependenciesExcluding -> PlanPhasesExcluding
// pipeline RunMigrate uses.
func TestPlanPhasesObjectsPortfoliosOnlySchedulesPortfolioTasksWithoutCreateProjects(t *testing.T) {
	reg := BuildMigrateRegistry(RegisterAll())
	objects, err := common.ParseObjects([]string{"portfolios"})
	if err != nil {
		t.Fatalf("ParseObjects: %v", err)
	}
	targets := MigrateTargetTasks(reg, "", false, false, false, false, nil, objects)
	excluded := excludedMigrateTasks(objects)
	if !excluded["createProjects"] {
		t.Fatal("expected createProjects to be excluded when objects=portfolios")
	}
	taskSet := ResolveDependenciesExcluding(targets, reg, excluded)
	if taskSet == nil {
		t.Fatal("cannot resolve dependencies")
	}
	if taskSet["createProjects"] {
		t.Fatal("createProjects must not be scheduled when --objects=portfolios")
	}
	for _, name := range []string{"createPortfolios", "setPortfolioProjects", "configurePortfolios"} {
		if !taskSet[name] {
			t.Errorf("expected %q to still be scheduled when --objects=portfolios", name)
		}
	}

	plan, err := PlanPhasesExcluding(taskSet, reg, excluded)
	if err != nil {
		t.Fatalf("PlanPhasesExcluding failed: %v", err)
	}
	for _, phase := range plan {
		for _, name := range phase {
			if name == "createProjects" {
				t.Fatalf("createProjects must never appear in the phase plan when --objects=portfolios, got phase %v", phase)
			}
		}
	}
}

func writeJSONLLine(t *testing.T, path string, item map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	b, _ := json.Marshal(item)
	f.Write(b)
	f.Write([]byte("\n"))
}
