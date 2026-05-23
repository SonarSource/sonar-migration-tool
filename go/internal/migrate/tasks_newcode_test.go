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
	"sort"
	"strings"
	"sync"
	"testing"
)

// ncdGlobalCall captures the parameters of one
// /api/new_code_periods/set POST so the runSetGlobalNewCodePeriod
// tests can assert org, type, value, and the absence of project (this
// task is org-scope only).
type ncdGlobalCall struct {
	project string
	org     string
	ncdType string
	value   string
}

// runSetGlobalNCDTest wires the extract + migrate fixtures and runs
// runSetGlobalNewCodePeriod. ncd is the SQS-side global NCD record;
// orgs is the generateOrganizationMappings content.
func runSetGlobalNCDTest(t *testing.T, ncd map[string]any, orgs []map[string]any) (hits []ncdGlobalCall, logs string) {
	t.Helper()
	var (
		mu       sync.Mutex
		recorded []ncdGlobalCall
	)
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/new_code_periods/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		recorded = append(recorded, ncdGlobalCall{
			project: r.FormValue("project"),
			org:     r.FormValue("organization"),
			ncdType: r.FormValue("type"),
			value:   r.FormValue("value"),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	t.Cleanup(cloudSrv.Close)

	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	var buf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// SQS global NCD extract — written into the extract directory
	// (not the migrate store) because readExtractItems goes through
	// e.Mapping.
	extractDir := filepath.Join(dir, "extract-01", "getGlobalNewCodePeriod")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, _ := os.Create(filepath.Join(extractDir, "results.1.jsonl"))
	if ncd != nil {
		b, _ := json.Marshal(ncd)
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()
	b, _ := json.Marshal(map[string]any{"url": testServerURL})
	os.WriteFile(filepath.Join(dir, "extract-01", "extract.json"), b, 0o644)

	pw, _ := e.Store.Writer("generateOrganizationMappings")
	for _, o := range orgs {
		bb, _ := json.Marshal(o)
		pw.WriteOne(bb)
	}

	if err := runSetGlobalNewCodePeriod(context.Background(), e); err != nil {
		t.Fatalf("runSetGlobalNewCodePeriod: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	hits = append(hits, recorded...)
	return hits, buf.String()
}

// SQS NUMBER_OF_DAYS=30 → each SQC org receives one
// /api/new_code_periods/set POST with organization=<org> and the SQC
// type "days". The body must NOT carry a project (this is org-scope).
func TestRunSetGlobalNewCodePeriodFansOutDaysToEveryOrg(t *testing.T) {
	hits, _ := runSetGlobalNCDTest(t,
		map[string]any{"type": "NUMBER_OF_DAYS", "value": "30", "serverUrl": testServerURL},
		[]map[string]any{
			{"sonarcloud_org_key": "orgA"},
			{"sonarcloud_org_key": "orgB"},
		},
	)
	if len(hits) != 2 {
		t.Fatalf("expected 2 calls (one per org), got %d: %+v", len(hits), hits)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].org < hits[j].org })
	want := []ncdGlobalCall{
		{org: "orgA", ncdType: "days", value: "30"},
		{org: "orgB", ncdType: "days", value: "30"},
	}
	for i, w := range want {
		if hits[i] != w {
			t.Errorf("call %d: got %+v, want %+v", i, hits[i], w)
		}
		if hits[i].project != "" {
			t.Errorf("call %d: project must be empty (org-scope), got %q", i, hits[i].project)
		}
	}
}

// PREVIOUS_VERSION is SQC's own default — task must not POST anything
// (issue #196 principle: don't migrate settings equal to default).
func TestRunSetGlobalNewCodePeriodSkipsPreviousVersion(t *testing.T) {
	hits, logs := runSetGlobalNCDTest(t,
		map[string]any{"type": "PREVIOUS_VERSION", "serverUrl": testServerURL},
		[]map[string]any{{"sonarcloud_org_key": "orgA"}},
	)
	if len(hits) != 0 {
		t.Errorf("PREVIOUS_VERSION must NOT trigger any POST, got %d", len(hits))
	}
	if !strings.Contains(logs, "PREVIOUS_VERSION") || !strings.Contains(logs, "skipping") {
		t.Errorf("expected Info log noting the skip, got:\n%s", logs)
	}
}

// REFERENCE_BRANCH with a branch value → maps to SQC's
// "reference_branch" type and forwards the value.
func TestRunSetGlobalNewCodePeriodReferenceBranch(t *testing.T) {
	hits, _ := runSetGlobalNCDTest(t,
		map[string]any{"type": "REFERENCE_BRANCH", "value": "main", "serverUrl": testServerURL},
		[]map[string]any{{"sonarcloud_org_key": "orgA"}},
	)
	if len(hits) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hits))
	}
	if hits[0].ncdType != "reference_branch" || hits[0].value != "main" {
		t.Errorf("expected type=reference_branch value=main, got %+v", hits[0])
	}
}

// SQS sometimes exports the legacy alias DAYS instead of
// NUMBER_OF_DAYS. The task normalizes it before mapping to SQC's
// "days", matching sonar-tools.
func TestRunSetGlobalNewCodePeriodNormalizesLegacyDaysAlias(t *testing.T) {
	hits, _ := runSetGlobalNCDTest(t,
		map[string]any{"type": "DAYS", "value": "7", "serverUrl": testServerURL},
		[]map[string]any{{"sonarcloud_org_key": "orgA"}},
	)
	if len(hits) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hits))
	}
	if hits[0].ncdType != "days" || hits[0].value != "7" {
		t.Errorf("DAYS must be normalized to days, got %+v", hits[0])
	}
}

// TestRunSetNewCodePeriodsTranslatesAndSets verifies that runSetNewCodePeriods
// translates SQS NCD types to their SQC equivalents, omits the value for
// previous_version, and resolves projectKey + branch to the right cloud
// project + organization.
func TestRunSetNewCodePeriodsTranslatesAndSets(t *testing.T) {
	type call struct {
		project, branch, ncdType, value, org string
	}
	var (
		mu       sync.Mutex
		recorded []call
	)
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/new_code_periods/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		recorded = append(recorded, call{
			project: r.FormValue("project"),
			branch:  r.FormValue("branch"),
			ncdType: r.FormValue("type"),
			value:   r.FormValue("value"),
			org:     r.FormValue("organization"),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()

	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	// Three extract records covering each translated NCD type plus an
	// unmapped one (UNKNOWN) which the task should skip with a warning.
	extractDir := filepath.Join(dir, "extract-01", "getNewCodePeriods")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, _ := os.Create(filepath.Join(extractDir, "results.1.jsonl"))
	for _, rec := range []map[string]any{
		{"projectKey": "proj-days", "branchKey": "main", "type": "NUMBER_OF_DAYS", "value": "14"},
		{"projectKey": "proj-prev", "branchKey": "main", "type": "PREVIOUS_VERSION", "value": nil},
		{"projectKey": "proj-ref", "branchKey": "main", "type": "REFERENCE_BRANCH", "value": "develop"},
		{"projectKey": "proj-unknown", "branchKey": "main", "type": "UNKNOWN_MODE"},
	} {
		b, _ := json.Marshal(rec)
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()

	pw, _ := e.Store.Writer("createProjects")
	for _, src := range []map[string]any{
		{"key": "proj-days", "server_url": testServerURL, "sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj-days"},
		{"key": "proj-prev", "server_url": testServerURL, "sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj-prev"},
		{"key": "proj-ref", "server_url": testServerURL, "sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj-ref"},
		{"key": "proj-unknown", "server_url": testServerURL, "sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj-unknown"},
	} {
		b, _ := json.Marshal(src)
		pw.WriteOne(b)
	}

	if err := runSetNewCodePeriods(context.Background(), e); err != nil {
		t.Fatalf("runSetNewCodePeriods: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Expect 3 calls — the UNKNOWN_MODE record should be skipped.
	if len(recorded) != 3 {
		t.Fatalf("expected 3 calls, got %d: %+v", len(recorded), recorded)
	}
	sort.Slice(recorded, func(i, j int) bool { return recorded[i].project < recorded[j].project })

	want := []call{
		{project: "org1_proj-days", branch: "main", ncdType: "days", value: "14", org: "org1"},
		{project: "org1_proj-prev", branch: "main", ncdType: "previous_version", value: "", org: "org1"},
		{project: "org1_proj-ref", branch: "main", ncdType: "reference_branch", value: "develop", org: "org1"},
	}
	for i, w := range want {
		if recorded[i] != w {
			t.Errorf("call %d: got %+v, want %+v", i, recorded[i], w)
		}
	}
}
