package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestTransformPortfolioRegex(t *testing.T) {
	cases := []struct {
		name    string
		regex   string
		orgKeys []string
		want    string
	}{
		{"anchored simple",
			"^backend-", []string{"org1"}, "^org1_backend-"},
		{"anchored char class",
			"^[A-Z].*", []string{"org1"}, "^org1_[A-Z].*"},
		{"unanchored",
			"backend", []string{"org1"}, "org1_backend"},
		{"two orgs alternation",
			"^foo", []string{"org1", "org2"},
			"^(?:org1_|org2_)foo"},
		{"empty regex stays empty",
			"", []string{"org1"}, ""},
		{"no orgs returns original",
			"^foo", nil, "^foo"},
		{"org key with regex metachars is quoted",
			"^bar", []string{"org-1.x"}, `^org-1\.x_bar`},
		{"duplicate org keys deduplicated",
			"^foo", []string{"org1", "org1"}, "^org1_foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := transformPortfolioRegex(tc.regex, tc.orgKeys)
			if got != tc.want {
				t.Errorf("transformPortfolioRegex(%q, %v): got %q, want %q",
					tc.regex, tc.orgKeys, got, tc.want)
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
	if patchBody["selection"] != "regexp" {
		t.Errorf("expected selection=regexp, got %v", patchBody["selection"])
	}
	if patchBody["regularExpression"] != "^myorg_backend-" {
		t.Errorf("expected regex rewrite, got %q", patchBody["regularExpression"])
	}
	orgs, _ := patchBody["organizationIds"].([]any)
	if len(orgs) != 1 || orgs[0] != "myorg-uuid" {
		t.Errorf("expected organizationIds=[myorg-uuid], got %v", orgs)
	}
}

// TestRunConfigurePortfoliosSkipsManual ensures portfolios that come through
// with selection_mode empty / MANUAL are left untouched here (handled by
// setPortfolioProjects instead).
func TestRunConfigurePortfoliosSkipsManual(t *testing.T) {
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
	cpw.WriteOne(json.RawMessage(`{"source_portfolio_key":"manual-portfolio","name":"Manual Portfolio","selection_mode":"MANUAL","cloud_portfolio_id":"cloud-manual-1"}`))

	if err := runConfigurePortfolios(context.Background(), e); err != nil {
		t.Fatalf("runConfigurePortfolios: %v", err)
	}
	if patchCalled {
		t.Error("PATCH must not be called for MANUAL portfolios")
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
