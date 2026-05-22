package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

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
