package regtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// newTestSuite returns a Suite wired to the given SQS and SC test servers
// with a minimal config. Both servers are tracked so the caller can close
// them via t.Cleanup.
func newTestSuite(t *testing.T, sqs, sc *httptest.Server) *Suite {
	t.Helper()
	cfg := Config{
		SQSURL:    sqs.URL,
		SQSToken:  "test-sqs",
		SCURL:     sc.URL,
		SCToken:   "test-sc",
		SCOrg:     "test-org",
		ExportDir: t.TempDir(),
	}
	// Hand-build the Suite so we don't need to reach into a SonarQube server
	// for the version handshake. The RawClients are constructed directly.
	sqsRaw := common.NewRawClient(&http.Client{}, sqs.URL+"/")
	scRaw := common.NewRawClient(&http.Client{}, sc.URL+"/")
	return &Suite{cfg: cfg, sqsRaw: sqsRaw, scRaw: scRaw}
}

// TestGetProjectsPaginatesBeyond500 ensures getProjects no longer truncates
// at 500 projects. The SQS test server reports 1,200 total and serves three
// pages of 500/500/200.
func TestGetProjectsPaginatesBeyond500(t *testing.T) {
	// SQS server with 3 pages
	sqs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		page := r.URL.Query().Get("p")
		var components []map[string]string
		var paging map[string]int
		switch page {
		case "1":
			paging = map[string]int{"pageIndex": 1, "pageSize": 500, "total": 1200}
			for i := 0; i < 500; i++ {
				components = append(components, map[string]string{"key": "proj-" + itoa(i)})
			}
		case "2":
			paging = map[string]int{"pageIndex": 2, "pageSize": 500, "total": 1200}
			for i := 500; i < 1000; i++ {
				components = append(components, map[string]string{"key": "proj-" + itoa(i)})
			}
		case "3":
			paging = map[string]int{"pageIndex": 3, "pageSize": 500, "total": 1200}
			for i := 1000; i < 1200; i++ {
				components = append(components, map[string]string{"key": "proj-" + itoa(i)})
			}
		default:
			paging = map[string]int{"pageIndex": 0, "pageSize": 500, "total": 1200}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"paging":     paging,
			"components": components,
		})
	}))
	defer sqs.Close()

	// Empty SC server (not exercised by getProjects)
	sc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer sc.Close()

	s := newTestSuite(t, sqs, sc)
	keys, err := s.getProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1200 {
		t.Errorf("getProjects returned %d keys, want 1200 (no silent truncation beyond 500)", len(keys))
	}
	if keys[0] != "proj-0" || keys[1199] != "proj-1199" {
		t.Errorf("unexpected key range: first=%q last=%q", keys[0], keys[1199])
	}
}

// TestCheckTemplatePermissionsSkippedNotPassed ensures the regression check
// is no longer marked Match:true regardless of actual data — the SC
// permission-template API has no directly comparable structure, so the
// SQS baseline is recorded as SKIPPED, not PASS.
func TestCheckTemplatePermissionsSkippedNotPassed(t *testing.T) {
	sqs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "search_templates"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"permissionTemplates": []map[string]string{
					{"id": "tmpl-1", "name": "Default Template"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "template_groups"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"groups": []map[string]any{
					{"name": "admins"}, {"name": "devs"},
				},
			})
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer sqs.Close()
	sc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer sc.Close()

	s := newTestSuite(t, sqs, sc)
	results := checkTemplatePermissions(context.Background(), s)
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	for _, r := range results {
		if r.Notes != "SKIPPED" {
			t.Errorf("result %q should be SKIPPED (Notes=%q, Match=%v) — was the unconditional-PASS regression re-introduced?",
				r.Name, r.Notes, r.Match)
		}
		if r.Match {
			t.Errorf("result %q has Match:true; SKIPPED checks must not be marked as passing", r.Name)
		}
	}
}

// TestCheckCustomRulesDoesNotUnconditionallyMatch ensures the rule-count
// check is no longer hard-coded to Match:true. The default behaviour is the
// standard sqsCount == scCount comparison.
func TestCheckCustomRulesDoesNotUnconditionallyMatch(t *testing.T) {
	sqs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return paging.total = 100
		_, _ = w.Write([]byte(`{"paging":{"pageIndex":1,"pageSize":1,"total":100}}`))
	}))
	defer sqs.Close()
	sc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return paging.total = 80
		_, _ = w.Write([]byte(`{"paging":{"pageIndex":1,"pageSize":1,"total":80}}`))
	}))
	defer sc.Close()

	s := newTestSuite(t, sqs, sc)
	results := checkCustomRules(context.Background(), s)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Match {
		t.Errorf("checkCustomRules still hard-coded to Match:true; got SQS=%s SC=%s Match=true",
			r.SQSValue, r.SCValue)
	}
	if r.SQSValue != "100" || r.SCValue != "80" {
		t.Errorf("result values wrong: SQS=%s SC=%s (want 100 / 80)", r.SQSValue, r.SCValue)
	}
}

// TestCheckGlobalSettingsSubsetLogic ensures the settings check is no
// longer satisfied by scCount > 0 alone. It should pass only when SC has
// at least one migrated setting AND that count is no greater than SQS.
func TestCheckGlobalSettingsSubsetLogic(t *testing.T) {
	cases := []struct {
		name    string
		sqs     int
		sc      int
		wantMat bool
	}{
		{"sqs=0, sc=0", 0, 0, false},
		{"sqs=10, sc=0", 10, 0, false},
		{"sqs=10, sc=10", 10, 10, true},
		{"sqs=10, sc=5", 10, 5, true},
		{"sqs=10, sc=20 (misconfiguration)", 10, 20, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sqs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				settings := make([]map[string]any, tc.sqs)
				for i := range settings {
					settings[i] = map[string]any{"key": "k"}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"settings": settings})
			}))
			defer sqs.Close()
			sc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				settings := make([]map[string]any, tc.sc)
				for i := range settings {
					settings[i] = map[string]any{"key": "k"}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"settings": settings})
			}))
			defer sc.Close()

			s := newTestSuite(t, sqs, sc)
			results := checkGlobalSettings(context.Background(), s)
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if results[0].Match != tc.wantMat {
				t.Errorf("sqs=%d sc=%d: got Match=%v, want %v (the unconditional 'scCount > 0' check is too loose)",
					tc.sqs, tc.sc, results[0].Match, tc.wantMat)
			}
		})
	}
}

// TestCheckProjectSettingsSubsetLogic is the per-project counterpart to
// TestCheckGlobalSettingsSubsetLogic. It uses the same subset-relationship
// rule (scCount > 0 && scCount <= sqsCount) and would also silently PASS on
// any non-zero SC count under the old, broken implementation.
func TestCheckProjectSettingsSubsetLogic(t *testing.T) {
	cases := []struct {
		name    string
		sqs     int
		sc      int
		wantMat bool
	}{
		{"sqs=0, sc=0", 0, 0, false},
		{"sqs=10, sc=0", 10, 0, false},
		{"sqs=10, sc=10", 10, 10, true},
		{"sqs=10, sc=5", 10, 5, true},
		{"sqs=10, sc=20 (misconfiguration)", 10, 20, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sqs, sc := newProjectSettingsServers(t, tc.sqs, tc.sc)
			defer sqs.Close()
			defer sc.Close()

			s := newTestSuite(t, sqs, sc)
			results := checkProjectSettings(context.Background(), s)
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if results[0].Match != tc.wantMat {
				t.Errorf("sqs=%d sc=%d: got Match=%v, want %v (the unconditional 'scCount > 0' check is too loose)",
					tc.sqs, tc.sc, results[0].Match, tc.wantMat)
			}
		})
	}
}

// newProjectSettingsServers wires a minimal SQS/SC pair where SQS reports
// one project and the requested number of settings, and SC reports the
// requested number of project settings. Extracted from
// TestCheckProjectSettingsSubsetLogic to keep that test under the
// cognitive-complexity threshold.
func newProjectSettingsServers(t *testing.T, sqsCount, scCount int) (*httptest.Server, *httptest.Server) {
	t.Helper()
	settingsSQS := make([]map[string]any, sqsCount)
	for i := range settingsSQS {
		settingsSQS[i] = map[string]any{"key": "k"}
	}
	settingsSC := make([]map[string]any, scCount)
	for i := range settingsSC {
		settingsSC[i] = map[string]any{"key": "k"}
	}
	sqs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeProjectSettingsResponse(w, r, settingsSQS, true)
	}))
	sc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeProjectSettingsResponse(w, r, settingsSC, false)
	}))
	return sqs, sc
}

func writeProjectSettingsResponse(w http.ResponseWriter, r *http.Request, settings []map[string]any, withProject bool) {
	switch r.URL.Path {
	case "/api/projects/search":
		if withProject {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"paging":     map[string]int{"pageIndex": 1, "pageSize": 500, "total": 1},
				"components": []map[string]string{{"key": "proj-a"}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"paging":     map[string]int{"pageIndex": 1, "pageSize": 500, "total": 0},
			"components": []map[string]string{},
		})
	case "/api/settings/values":
		_ = json.NewEncoder(w).Encode(map[string]any{"settings": settings})
	default:
		_, _ = w.Write([]byte(`{}`))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	if neg {
		digits = "-" + digits
	}
	return digits
}
