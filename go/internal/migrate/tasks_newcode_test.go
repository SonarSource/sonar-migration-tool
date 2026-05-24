package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

// ncdGlobalCall captures one PATCH
// /organizations/organizations/{ref} from runSetGlobalNewCodePeriod,
// including the JSON body so the tests can verify the leak-period
// fields AND name end up on the wire.
type ncdGlobalCall struct {
	orgRef                string
	name                  string
	defaultLeakPeriodType string
	defaultLeakPeriod     string
}

// runSetGlobalNCDTest wires both bases: sonarcloud.io (for the
// /api/organizations/search lookup that gets the org's current name)
// and api.sonarcloud.io (for the PATCH
// /organizations/organizations/{key} that actually writes the NCD).
// Including name in the PATCH body matches what the SonarCloud UI
// sends — the endpoint has been observed to reject the PATCH
// otherwise.
func runSetGlobalNCDTest(t *testing.T, ncd map[string]any, orgs []map[string]any) (hits []ncdGlobalCall, logs string) {
	t.Helper()
	var (
		mu       sync.Mutex
		recorded []ncdGlobalCall
	)

	// Cloud mux (sonarcloud.io): /api/organizations/search so the
	// name lookup succeeds. Echo back the requested key as the name.
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("/api/organizations/search", func(w http.ResponseWriter, r *http.Request) {
		keys := strings.Split(r.URL.Query().Get("organizations"), ",")
		var out []map[string]any
		for _, k := range keys {
			out = append(out, map[string]any{"key": k, "name": k + " (display)"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"organizations": out})
	})
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	t.Cleanup(cloudSrv.Close)

	// API mux (api.sonarcloud.io): PATCH /organizations/organizations/{ref}.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/organizations/organizations/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, `{"errors":[{"msg":"method not allowed"}]}`, http.StatusMethodNotAllowed)
			return
		}
		ref := strings.TrimPrefix(r.URL.Path, "/organizations/organizations/")
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		mu.Lock()
		recorded = append(recorded, ncdGlobalCall{
			orgRef:                ref,
			name:                  asStr(decoded["name"]),
			defaultLeakPeriodType: asStr(decoded["defaultLeakPeriodType"]),
			defaultLeakPeriod:     asStr(decoded["defaultLeakPeriod"]),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	var buf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

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

func asStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// SQS NUMBER_OF_DAYS=30 → each SQC org receives one PATCH
// /organizations/{key} with body
// {"defaultLeakPeriodType":"days","defaultLeakPeriod":"30"}.
// Also verifies the task emits a setGlobalNewCodePeriod JSONL record
// with per-org outcomes so the report's Global Settings section can
// render one row per (NCD, org) — issue #136 reporting follow-up.
func TestRunSetGlobalNewCodePeriodFansOutDaysToEveryOrg(t *testing.T) {
	hits, _ := runSetGlobalNCDTest(t,
		map[string]any{"type": "NUMBER_OF_DAYS", "value": "30", "serverUrl": testServerURL},
		[]map[string]any{
			{"sonarcloud_org_key": "orgA"},
			{"sonarcloud_org_key": "orgB"},
		},
	)
	if len(hits) != 2 {
		t.Fatalf("expected 2 PATCHes (one per org), got %d: %+v", len(hits), hits)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].orgRef < hits[j].orgRef })
	want := []ncdGlobalCall{
		{orgRef: "orgA", name: "orgA (display)", defaultLeakPeriodType: "days", defaultLeakPeriod: "30"},
		{orgRef: "orgB", name: "orgB (display)", defaultLeakPeriodType: "days", defaultLeakPeriod: "30"},
	}
	for i, w := range want {
		if hits[i] != w {
			t.Errorf("call %d: got %+v, want %+v", i, hits[i], w)
		}
	}
}

// The task must write a single JSONL record to setGlobalNewCodePeriod
// with one outcome per migrated org, in the same schema as
// setGlobalSettings — that's how the migration report's Global
// Settings section surfaces the NCD migration alongside the other
// global settings.
func TestRunSetGlobalNewCodePeriodEmitsReportRecord(t *testing.T) {
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("/api/organizations/search", func(w http.ResponseWriter, r *http.Request) {
		keys := strings.Split(r.URL.Query().Get("organizations"), ",")
		var out []map[string]any
		for _, k := range keys {
			out = append(out, map[string]any{"key": k, "name": k})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"organizations": out})
	})
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/organizations/organizations/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})
	apiSrv := httptest.NewServer(apiMux)
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	extractDir := filepath.Join(dir, "extract-01", "getGlobalNewCodePeriod")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, _ := os.Create(filepath.Join(extractDir, "results.1.jsonl"))
	b, _ := json.Marshal(map[string]any{"type": "NUMBER_OF_DAYS", "value": "61", "serverUrl": testServerURL})
	f.Write(b)
	f.Write([]byte("\n"))
	f.Close()
	meta, _ := json.Marshal(map[string]any{"url": testServerURL})
	os.WriteFile(filepath.Join(dir, "extract-01", "extract.json"), meta, 0o644)

	pw, _ := e.Store.Writer("generateOrganizationMappings")
	for _, k := range []string{"orgA", "orgB"} {
		bb, _ := json.Marshal(map[string]any{"sonarcloud_org_key": k})
		pw.WriteOne(bb)
	}

	if err := runSetGlobalNewCodePeriod(context.Background(), e); err != nil {
		t.Fatalf("runSetGlobalNewCodePeriod: %v", err)
	}

	out, _ := e.Store.ReadAll("setGlobalNewCodePeriod")
	if len(out) != 1 {
		t.Fatalf("expected exactly one setGlobalNewCodePeriod record, got %d", len(out))
	}
	var rec struct {
		Key      string `json:"key"`
		Outcomes []struct {
			Org    string `json:"org"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"outcomes"`
	}
	if err := json.Unmarshal(out[0], &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec.Key != "newCodePeriod" {
		t.Errorf("key: want \"newCodePeriod\", got %q", rec.Key)
	}
	if len(rec.Outcomes) != 2 {
		t.Fatalf("want one outcome per org, got %d: %+v", len(rec.Outcomes), rec.Outcomes)
	}
	for _, oc := range rec.Outcomes {
		if oc.Status != "applied" {
			t.Errorf("org %s: want status=applied, got %q", oc.Org, oc.Status)
		}
		if !strings.Contains(oc.Detail, "defaultLeakPeriodType=days") ||
			!strings.Contains(oc.Detail, "defaultLeakPeriod=61") {
			t.Errorf("org %s: detail must include type+value, got %q", oc.Org, oc.Detail)
		}
	}
}

// PREVIOUS_VERSION must STILL PATCH each org — the SQC org may have
// been previously set to "32 days" or another non-default value and
// we need to actively reset it back to previous_version. SonarCloud
// validates the (type, value) pair and rejects type=previous_version
// with an empty value ("Invalid default leak period for type
// PREVIOUS_VERSION") — the value must mirror the type.
func TestRunSetGlobalNewCodePeriodAppliesPreviousVersion(t *testing.T) {
	hits, _ := runSetGlobalNCDTest(t,
		map[string]any{"type": "PREVIOUS_VERSION", "serverUrl": testServerURL},
		[]map[string]any{{"sonarcloud_org_key": "orgA"}},
	)
	if len(hits) != 1 {
		t.Fatalf("expected 1 PATCH (must reset stale value), got %d: %+v", len(hits), hits)
	}
	if hits[0].defaultLeakPeriodType != "previous_version" {
		t.Errorf("expected type=previous_version, got %q", hits[0].defaultLeakPeriodType)
	}
	if hits[0].defaultLeakPeriod != "previous_version" {
		t.Errorf("PREVIOUS_VERSION must travel with defaultLeakPeriod=\"previous_version\" (SQC rejects empty), got %q",
			hits[0].defaultLeakPeriod)
	}
}

// REFERENCE_BRANCH maps to SQC's "reference_branch" type with the
// branch name as the value.
func TestRunSetGlobalNewCodePeriodReferenceBranch(t *testing.T) {
	hits, _ := runSetGlobalNCDTest(t,
		map[string]any{"type": "REFERENCE_BRANCH", "value": "main", "serverUrl": testServerURL},
		[]map[string]any{{"sonarcloud_org_key": "orgA"}},
	)
	if len(hits) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(hits))
	}
	if hits[0].defaultLeakPeriodType != "reference_branch" || hits[0].defaultLeakPeriod != "main" {
		t.Errorf("expected type=reference_branch value=main, got %+v", hits[0])
	}
}

// Legacy DAYS → NUMBER_OF_DAYS → SQC "days".
func TestRunSetGlobalNewCodePeriodNormalizesLegacyDaysAlias(t *testing.T) {
	hits, _ := runSetGlobalNCDTest(t,
		map[string]any{"type": "DAYS", "value": "7", "serverUrl": testServerURL},
		[]map[string]any{{"sonarcloud_org_key": "orgA"}},
	)
	if len(hits) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(hits))
	}
	if hits[0].defaultLeakPeriodType != "days" || hits[0].defaultLeakPeriod != "7" {
		t.Errorf("DAYS must normalize to days, got %+v", hits[0])
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
