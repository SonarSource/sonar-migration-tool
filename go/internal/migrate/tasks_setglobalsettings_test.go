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
	"reflect"
	"strings"
	"sync"
	"testing"
)

// runGlobalSettingsTest wires up the cloud / api mocks and the executor
// for setGlobalSettings tests. Each test case provides:
//
//   - sqsSettings: the records that getServerSettings would have produced
//     (one map per setting; the test serializes them to JSONL).
//   - sqsDefs:     the records that getServerSettingsDefinitions produced
//     (used to detect customization via defaultValue).
//   - orgs:        the records that generateOrganizationMappings produced.
//   - sqcDefs:     the response body for SQC's list_definitions endpoint.
//
// It returns the captured /api/settings/set requests and the captured
// Warn-log buffer so each test can assert independently.
func runGlobalSettingsTest(t *testing.T,
	sqsSettings []map[string]any,
	sqsDefs []map[string]any,
	orgs []map[string]any,
	sqcDefs []map[string]any,
) (hits []settingsHit, logs string) {

	t.Helper()
	cloudMux := http.NewServeMux()
	muHits, hitsPtr := mountSettingsSetCapture(cloudMux)
	mountSettingsDefinitions(cloudMux, sqcDefs...)
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	t.Cleanup(cloudSrv.Close)

	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	var logBuf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// getServerSettings / getServerSettingsDefinitions are EXTRACT tasks
	// — runSetGlobalSettings reaches them via readExtractItems and
	// e.Mapping, NOT via the migrate store. Writing to the migrate store
	// would silently leave the production read path empty and the test
	// would be exercising a different code path than reality. Drop the
	// records into the configured extract directory instead.
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettings", sqsSettings)
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettingsDefinitions", sqsDefs)
	// extract.json is what GetUniqueExtracts inspects to assemble the
	// ExtractMapping; without it readExtractItems can't resolve the
	// extract dir for a server URL.
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)
	// generateOrganizationMappings IS produced by the migrate phase, so
	// it correctly lives in the migrate store.
	writeTaskJSONL(t, e, "generateOrganizationMappings", orgs)

	if err := runSetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetGlobalSettings: %v", err)
	}

	muHits.Lock()
	defer muHits.Unlock()
	hits = append(hits, *hitsPtr...)
	return hits, logBuf.String()
}

// writeExtractTaskJSONL writes records as JSONL into an extract run
// directory (NOT the migrate store). setGlobalSettings reads from here
// via readExtractItems / e.Mapping.
func writeExtractTaskJSONL(t *testing.T, exportDir, extractRun, task string, records []map[string]any) {
	t.Helper()
	taskDir := filepath.Join(exportDir, extractRun, task)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", taskDir, err)
	}
	f, err := os.Create(filepath.Join(taskDir, "results.1.jsonl"))
	if err != nil {
		t.Fatalf("create extract jsonl: %v", err)
	}
	defer f.Close()
	for _, r := range records {
		b, _ := json.Marshal(r)
		f.Write(b)
		f.Write([]byte("\n"))
	}
}

// writeExtractMetaJSON writes the extract.json file the structure
// package reads to learn which server URL produced this extract.
func writeExtractMetaJSON(t *testing.T, exportDir, extractRun, serverURL string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(exportDir, extractRun), 0o755); err != nil {
		t.Fatalf("mkdir extract: %v", err)
	}
	b, _ := json.Marshal(map[string]any{"url": serverURL})
	if err := os.WriteFile(filepath.Join(exportDir, extractRun, "extract.json"), b, 0o644); err != nil {
		t.Fatalf("write extract.json: %v", err)
	}
}

// writeTaskJSONL writes a slice of maps as JSONL into the named task's output
// in the migrate store — mirrors what an earlier task would have done.
func writeTaskJSONL(t *testing.T, e *Executor, task string, records []map[string]any) {
	t.Helper()
	w, err := e.Store.Writer(task)
	if err != nil {
		t.Fatalf("writer(%s): %v", task, err)
	}
	for _, r := range records {
		b, _ := json.Marshal(r)
		if err := w.WriteOne(b); err != nil {
			t.Fatalf("write(%s): %v", task, err)
		}
	}
}

// Settings whose value matches the SQS defaultValue must not produce any
// /api/settings/set request — they're not customized.
func TestRunSetGlobalSettingsSkipsWhenValueEqualsDefault(t *testing.T) {
	hits, _ := runGlobalSettingsTest(t,
		// SQS extract values
		[]map[string]any{
			{"key": "sonar.foo", "value": "default-foo"}, // matches default → skip
			{"key": "sonar.bar", "value": "custom-bar"},  // differs → migrate
		},
		// SQS list_definitions
		[]map[string]any{
			{"key": "sonar.foo", "type": "STRING", "multiValues": false, "defaultValue": "default-foo"},
			{"key": "sonar.bar", "type": "STRING", "multiValues": false, "defaultValue": "default-bar"},
		},
		// Org mapping
		[]map[string]any{
			{"sonarcloud_org_key": "org1"},
		},
		// SQC list_definitions — keys present
		[]map[string]any{
			{"key": "sonar.foo", "type": "STRING", "multiValues": false},
			{"key": "sonar.bar", "type": "STRING", "multiValues": false},
		},
	)
	if len(hits) != 1 {
		t.Fatalf("expected 1 settings.set call (only customized setting), got %d", len(hits))
	}
	if hits[0].key != "sonar.bar" {
		t.Errorf("expected the customized setting to be migrated, got %q", hits[0].key)
	}
}

// Customized setting + key on SQC with multiValues=true → send via
// values=. The test also confirms the request is org-scoped, not
// component-scoped.
func TestRunSetGlobalSettingsMultiValueOnSQC(t *testing.T) {
	hits, _ := runGlobalSettingsTest(t,
		[]map[string]any{
			{"key": "sonar.exclusions", "values": []string{"**/*.gen", "build/**"}},
		},
		[]map[string]any{
			{"key": "sonar.exclusions", "type": "STRING", "multiValues": true, "defaultValue": ""},
		},
		[]map[string]any{
			{"sonarcloud_org_key": "org1"},
			{"sonarcloud_org_key": "org2"},
		},
		[]map[string]any{
			{"key": "sonar.exclusions", "type": "STRING", "multiValues": true},
		},
	)
	if len(hits) != 2 {
		t.Fatalf("expected 2 calls (one per org), got %d", len(hits))
	}
	for _, h := range hits {
		if h.key != "sonar.exclusions" {
			t.Errorf("wrong key: %q", h.key)
		}
		if len(h.values) != 2 {
			t.Errorf("expected values= shape (multiValues=true), got %+v", h)
		}
		if h.value != "" {
			t.Errorf("must NOT collapse multi-value to single value=, got %q", h.value)
		}
	}
}

// Customized setting + key on SQC with multiValues=false → SQS array
// must be CSV-joined into a single value= param (same trick as the
// sonar.java.file.suffixes fix from #120).
func TestRunSetGlobalSettingsSingleValueOnSQCJoinsCSV(t *testing.T) {
	hits, _ := runGlobalSettingsTest(t,
		[]map[string]any{
			{"key": "sonar.java.file.suffixes", "values": []string{".java", ".jav"}},
		},
		[]map[string]any{
			{"key": "sonar.java.file.suffixes", "type": "STRING", "multiValues": true, "defaultValue": ".java"},
		},
		[]map[string]any{
			{"sonarcloud_org_key": "org1"},
		},
		[]map[string]any{
			{"key": "sonar.java.file.suffixes", "type": "STRING", "multiValues": false},
		},
	)
	if len(hits) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hits))
	}
	h := hits[0]
	if len(h.values) != 0 {
		t.Errorf("must NOT send values= for a single-value SQC setting, got %v", h.values)
	}
	if h.value != ".java,.jav" {
		t.Errorf("expected CSV-joined value, got %q", h.value)
	}
}

// Customized setting whose key is NOT in SQC's list_definitions → no
// /api/settings/set request, and a Warn must be logged naming both the
// setting key and the org.
func TestRunSetGlobalSettingsWarnsWhenKeyNotOnSQC(t *testing.T) {
	hits, logs := runGlobalSettingsTest(t,
		[]map[string]any{
			{"key": "sonar.sqs.only", "value": "custom"},
		},
		[]map[string]any{
			{"key": "sonar.sqs.only", "type": "STRING", "multiValues": false, "defaultValue": "default"},
		},
		[]map[string]any{
			{"sonarcloud_org_key": "org1"},
		},
		// SQC doesn't define sonar.sqs.only at all.
		[]map[string]any{},
	)
	if len(hits) != 0 {
		t.Fatalf("expected zero API calls for SQS-only setting, got %d", len(hits))
	}
	if !strings.Contains(logs, "setting key not available on SQC") {
		t.Errorf("expected Warn about SQC-missing key, got:\n%s", logs)
	}
	if !strings.Contains(logs, "sonar.sqs.only") || !strings.Contains(logs, "org1") {
		t.Errorf("expected Warn to name the setting key and org, got:\n%s", logs)
	}
}

// When a customized SQS global key is missing from SQC's org-scope
// definitions but IS present at project scope, setGlobalSettings must
// NOT log a Warn — that key is handled by setProjectSettings's
// propagation pass. The skip reason switches from "not-on-sqc" to
// "project-scope-only". Issues #189 / #191.
func TestRunSetGlobalSettingsUsesProjectScopeOnlyReasonWhenKeyAtProjectScope(t *testing.T) {
	cloudMux := http.NewServeMux()
	muHits, hitsPtr := mountSettingsSetCapture(cloudMux)
	mountSettingsDefinitionsScoped(cloudMux,
		// Org-scope defs: no sonar.java.file.suffixes.
		[]map[string]any{},
		// Project-scope defs: includes it.
		[]map[string]any{
			{"key": "sonar.java.file.suffixes", "type": "STRING", "multiValues": false},
		},
	)
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()

	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	var logBuf bytes.Buffer
	// Capture Info + Warn so the test can assert the Info case fires
	// and the Warn case does NOT.
	e.Logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettings", []map[string]any{
		{"key": "sonar.java.file.suffixes", "values": []string{".java", ".jav"}},
	})
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettingsDefinitions", []map[string]any{
		{"key": "sonar.java.file.suffixes", "type": "STRING", "multiValues": false, "defaultValue": ".java"},
	})
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"sonarcloud_org_key": "org1"},
	})
	// createProjects record so loadProjectScopedSettingDefinitionsForOrgs
	// has a probe project to query SQC with component=.
	pw, _ := e.Store.Writer("createProjects")
	b, _ := json.Marshal(map[string]any{
		"key": "proj1", "server_url": testServerURL,
		"sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj1",
	})
	pw.WriteOne(b)

	if err := runSetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetGlobalSettings: %v", err)
	}

	muHits.Lock()
	defer muHits.Unlock()
	if len(*hitsPtr) != 0 {
		t.Fatalf("setGlobalSettings must NOT issue API calls for project-scope-only keys, got %d", len(*hitsPtr))
	}
	logs := logBuf.String()
	if strings.Contains(logs, "setting key not available on SQC") {
		t.Errorf("must NOT log the not-on-sqc Warn for a project-scope-only key, got:\n%s", logs)
	}
	if !strings.Contains(logs, "will be propagated by setProjectSettings") {
		t.Errorf("expected Info log about delegation to setProjectSettings, got:\n%s", logs)
	}
}

// SQC's /api/settings/list_definitions falsely reports certain keys
// (e.g. sonar.coverage.jacoco.xmlReportPaths, sonar.androidLint.reportPaths)
// as settable at org scope. The actual /api/settings/set call rejects
// org-scoped writes for them with HTTP 400 "Provided property can't
// be set at organization level". When this runtime rejection happens,
// setGlobalSettings must fall back to setting the value on every
// project in the org — otherwise the setting silently disappears
// during migration.
func TestRunSetGlobalSettingsFallsBackToProjectsOnOrgLevelRejection(t *testing.T) {
	cloudMux := http.NewServeMux()

	// Track org-vs-project requests independently so the test can
	// assert: the org-scope POST fired once (and 400'd), and a
	// project-scope POST fired for each of the two projects.
	var (
		mu          sync.Mutex
		orgHits     []settingsHit
		projectHits []settingsHit
	)
	cloudMux.HandleFunc("POST /api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		hit := settingsHit{
			key:    r.FormValue("key"),
			value:  r.FormValue("value"),
			values: append([]string(nil), r.Form["values"]...),
			fields: append([]string(nil), r.Form["fieldValues"]...),
		}
		mu.Lock()
		if r.FormValue("component") != "" {
			projectHits = append(projectHits, hit)
			w.WriteHeader(http.StatusNoContent)
		} else {
			orgHits = append(orgHits, hit)
			// Mimic the real SQC 400 message verbatim — the SDK's
			// IsOrgLevelRejection helper greps the body for it.
			// Use SonarCloud's actual response shape: the apostrophe in
		// "can't" is JSON-escaped as ' in the wire body. The SDK
		// detector must match against the DECODED message, so this
		// test verifies the integration end-to-end with realistic
		// data instead of the simplified literal-apostrophe form.
		http.Error(w, `{"errors":[{"msg":"Provided property can't be set at organization level: `+hit.key+`"}]}`, http.StatusBadRequest)
		}
		mu.Unlock()
	})
	mountSettingsDefinitionsScoped(cloudMux,
		// Org scope INCLUDES the key (this is the SQC list_definitions
		// inconsistency — the api reports it then refuses the write).
		[]map[string]any{
			{"key": "sonar.coverage.jacoco.xmlReportPaths", "type": "STRING", "multiValues": true},
		},
		// Project scope also includes it (rightly so).
		[]map[string]any{
			{"key": "sonar.coverage.jacoco.xmlReportPaths", "type": "STRING", "multiValues": true},
		},
	)
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()

	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	var logBuf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettings", []map[string]any{
		{"key": "sonar.coverage.jacoco.xmlReportPaths", "values": []string{"**/jacoco*.xml"}},
	})
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettingsDefinitions", []map[string]any{
		{"key": "sonar.coverage.jacoco.xmlReportPaths", "type": "STRING", "multiValues": true, "defaultValue": ""},
	})
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"sonarcloud_org_key": "org1"},
	})
	// Two projects in the org — both should receive the fan-out call.
	pw, _ := e.Store.Writer("createProjects")
	for _, key := range []string{"projA", "projB"} {
		b, _ := json.Marshal(map[string]any{
			"key": key, "server_url": testServerURL,
			"sonarcloud_org_key": "org1", "cloud_project_key": "org1_" + key,
		})
		pw.WriteOne(b)
	}

	if err := runSetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetGlobalSettings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(orgHits) != 1 {
		t.Errorf("expected exactly one org-scope attempt (the one that 400'd), got %d", len(orgHits))
	}
	if len(projectHits) != 2 {
		t.Fatalf("expected fan-out to BOTH projects after org-level rejection, got %d hits: %+v",
			len(projectHits), projectHits)
	}
	for _, h := range projectHits {
		if h.key != "sonar.coverage.jacoco.xmlReportPaths" {
			t.Errorf("wrong key in fan-out: %q", h.key)
		}
		if len(h.values) != 1 || h.values[0] != "**/jacoco*.xml" {
			t.Errorf("expected values=[**/jacoco*.xml] in fan-out, got %+v", h)
		}
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "key not settable at org level despite list_definitions claim") {
		t.Errorf("expected Info log noting the SQC inconsistency, got:\n%s", logs)
	}

	// Output Outcome's Detail must use the new fan-out wording —
	// "Applied to all projects (values=[...])" — instead of listing
	// the individual projects. Issue follow-up requested this so the
	// report stays readable when an org has many projects.
	out, _ := e.Store.ReadAll("setGlobalSettings")
	if len(out) != 1 {
		t.Fatalf("expected one setGlobalSettings record, got %d", len(out))
	}
	var rec struct {
		Outcomes []struct {
			Org    string `json:"org"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"outcomes"`
	}
	_ = json.Unmarshal(out[0], &rec)
	if len(rec.Outcomes) != 1 || rec.Outcomes[0].Status != "applied-to-projects" {
		t.Fatalf("expected one applied-to-projects outcome, got %+v", rec.Outcomes)
	}
	if !strings.Contains(rec.Outcomes[0].Detail, "to all projects") {
		t.Errorf("Detail must include \"to all projects\", got %q", rec.Outcomes[0].Detail)
	}
	if !strings.Contains(rec.Outcomes[0].Detail, "**/jacoco*.xml") {
		t.Errorf("Detail must include the value, got %q", rec.Outcomes[0].Detail)
	}
	if strings.Contains(rec.Outcomes[0].Detail, "projA") || strings.Contains(rec.Outcomes[0].Detail, "projB") {
		t.Errorf("Detail must NOT list individual projects when fan-out applied to ALL, got %q", rec.Outcomes[0].Detail)
	}
}

// Once SQC has rejected a key at org level for ONE org, the task
// must not retry the same failing POST for any subsequent org —
// it should reuse the verdict and go straight to project fan-out.
// This bounds the wasted-request count at one per (key) instead of
// one per (key × org).
func TestRunSetGlobalSettingsOrgLevelRejectionTriedOncePerKey(t *testing.T) {
	cloudMux := http.NewServeMux()

	var (
		mu      sync.Mutex
		orgHits []settingsHit
		projHit int
	)
	cloudMux.HandleFunc("POST /api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		if r.FormValue("component") != "" {
			projHit++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		orgHits = append(orgHits, settingsHit{key: r.FormValue("key")})
		http.Error(w, `{"errors":[{"msg":"Provided property can't be set at organization level: `+r.FormValue("key")+`"}]}`, http.StatusBadRequest)
	})
	mountSettingsDefinitionsScoped(cloudMux,
		[]map[string]any{{"key": "sonar.coverage.jacoco.xmlReportPaths", "type": "STRING", "multiValues": true}},
		[]map[string]any{{"key": "sonar.coverage.jacoco.xmlReportPaths", "type": "STRING", "multiValues": true}},
	)
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()

	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	var logBuf bytes.Buffer
	// The memoization log was demoted to Debug in #258 — open the
	// logger to Debug so the assertion below still picks it up.
	e.Logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettings", []map[string]any{
		{"key": "sonar.coverage.jacoco.xmlReportPaths", "values": []string{"**/jacoco*.xml"}},
	})
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettingsDefinitions", []map[string]any{
		{"key": "sonar.coverage.jacoco.xmlReportPaths", "type": "STRING", "multiValues": true, "defaultValue": ""},
	})
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)
	// Three orgs, one project each. The first org's POST rejects;
	// the next two orgs must NOT re-issue the failing org POST.
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"sonarcloud_org_key": "orgA"},
		{"sonarcloud_org_key": "orgB"},
		{"sonarcloud_org_key": "orgC"},
	})
	pw, _ := e.Store.Writer("createProjects")
	for i, org := range []string{"orgA", "orgB", "orgC"} {
		b, _ := json.Marshal(map[string]any{
			"key":                "p" + string(rune('A'+i)),
			"server_url":         testServerURL,
			"sonarcloud_org_key": org,
			"cloud_project_key":  org + "_p" + string(rune('A'+i)),
		})
		pw.WriteOne(b)
	}

	if err := runSetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetGlobalSettings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(orgHits) != 1 {
		t.Errorf("org-level POST must be tried at most ONCE per key (memoization across orgs), got %d hits: %+v",
			len(orgHits), orgHits)
	}
	if projHit != 3 {
		t.Errorf("expected fan-out to all 3 projects (one per org), got %d", projHit)
	}
	if !strings.Contains(logBuf.String(), "org-level write already rejected for this key") {
		t.Errorf("expected Info log noting the memoized rejection on second org, got:\n%s", logBuf.String())
	}
}

// During the runtime fan-out, projects that already have a
// per-project SQS override for the same key must NOT be touched —
// setProjectSettings's per-record loop applies their specific value
// in parallel, and the fan-out would race against it (potentially
// clobbering the override with the global value).
func TestRunSetGlobalSettingsFanOutSkipsPerProjectOverrides(t *testing.T) {
	cloudMux := http.NewServeMux()

	var (
		mu       sync.Mutex
		projHits []settingsHit
	)
	cloudMux.HandleFunc("POST /api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		if r.FormValue("component") != "" {
			projHits = append(projHits, settingsHit{
				key:    r.FormValue("key"),
				values: append([]string(nil), r.Form["values"]...),
			})
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, `{"errors":[{"msg":"Provided property can't be set at organization level: `+r.FormValue("key")+`"}]}`, http.StatusBadRequest)
	})
	mountSettingsDefinitionsScoped(cloudMux,
		[]map[string]any{{"key": "sonar.coverage.jacoco.xmlReportPaths", "type": "STRING", "multiValues": true}},
		[]map[string]any{{"key": "sonar.coverage.jacoco.xmlReportPaths", "type": "STRING", "multiValues": true}},
	)
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()

	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	var logBuf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// SQS extract: per-project override for projA only.
	writeExtractTaskJSONL(t, dir, "extract-01", "getProjectSettings", []map[string]any{
		{"project": "projA", "key": "sonar.coverage.jacoco.xmlReportPaths",
			"values": []string{"specific-for-A.xml"}},
	})
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettings", []map[string]any{
		{"key": "sonar.coverage.jacoco.xmlReportPaths", "values": []string{"global-default.xml"}},
	})
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettingsDefinitions", []map[string]any{
		{"key": "sonar.coverage.jacoco.xmlReportPaths", "type": "STRING", "multiValues": true, "defaultValue": ""},
	})
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"sonarcloud_org_key": "org1"},
	})
	pw, _ := e.Store.Writer("createProjects")
	for _, key := range []string{"projA", "projB"} {
		b, _ := json.Marshal(map[string]any{
			"key": key, "server_url": testServerURL,
			"sonarcloud_org_key": "org1", "cloud_project_key": "org1_" + key,
		})
		pw.WriteOne(b)
	}

	if err := runSetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetGlobalSettings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// projB receives the global; projA must be skipped because it
	// has its own SQS override (setProjectSettings applies that).
	if len(projHits) != 1 {
		t.Fatalf("expected exactly one fan-out call (projB only), got %d: %+v", len(projHits), projHits)
	}
	if len(projHits[0].values) != 1 || projHits[0].values[0] != "global-default.xml" {
		t.Errorf("fan-out value should be the global (global-default.xml), got %+v", projHits[0])
	}
	if !strings.Contains(logBuf.String(), "per-project override wins, skipping fan-out") {
		t.Errorf("expected Debug log noting per-project override skip, got:\n%s", logBuf.String())
	}
}

// Customized PROPERTY_SET setting → sent via fieldValues= shape.
func TestRunSetGlobalSettingsPropertySet(t *testing.T) {
	hits, _ := runGlobalSettingsTest(t,
		[]map[string]any{
			{"key": "sonar.issue.ignore.allfile",
				"fieldValues": []map[string]any{{"fileRegexp": "Generated test"}}},
		},
		[]map[string]any{
			// defaultValue irrelevant for property-set: isSettingCustomized
			// treats any non-empty fieldValues as customized.
			{"key": "sonar.issue.ignore.allfile", "type": "PROPERTY_SET", "multiValues": false},
		},
		[]map[string]any{
			{"sonarcloud_org_key": "org1"},
		},
		[]map[string]any{
			{"key": "sonar.issue.ignore.allfile", "type": "PROPERTY_SET", "multiValues": false},
		},
	)
	if len(hits) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hits))
	}
	if len(hits[0].fields) != 1 {
		t.Errorf("expected fieldValues= shape, got %+v", hits[0])
	}
}

// SQS exposes sonar.global.exclusions (platform-enforced patterns) and
// sonar.exclusions (per-project default) separately, but SQC has only
// sonar.exclusions. The migrate task must:
//
//   - send a single sonar.exclusions PATCH whose values= is the union of
//     both SQS keys (deduped, order preserved),
//   - NOT emit a "not-on-sqc" Warn for sonar.global.exclusions (SQC has no
//     such key, but its patterns are still being migrated — just under a
//     different name), and
//   - mark the result so the report can call out the merge.
func TestRunSetGlobalSettingsMergesGlobalExclusionsIntoExclusions(t *testing.T) {
	hits, logs := runGlobalSettingsTest(t,
		// SQS extract values — both keys customized.
		[]map[string]any{
			{"key": "sonar.global.exclusions", "values": []string{"**/*.gen", "vendor/**"}},
			{"key": "sonar.exclusions", "values": []string{"build/**", "**/*.gen"}}, // **/*.gen also in global → dedupe
		},
		[]map[string]any{
			{"key": "sonar.global.exclusions", "type": "STRING", "multiValues": true, "defaultValue": ""},
			{"key": "sonar.exclusions", "type": "STRING", "multiValues": true, "defaultValue": ""},
		},
		[]map[string]any{
			{"sonarcloud_org_key": "org1"},
		},
		[]map[string]any{
			// SQC only has sonar.exclusions.
			{"key": "sonar.exclusions", "type": "STRING", "multiValues": true},
		},
	)
	if len(hits) != 1 {
		t.Fatalf("expected exactly 1 settings.set call (merged exclusions), got %d (%+v)", len(hits), hits)
	}
	if hits[0].key != "sonar.exclusions" {
		t.Fatalf("expected the call to land on sonar.exclusions, got %q", hits[0].key)
	}
	want := []string{"**/*.gen", "vendor/**", "build/**"}
	if !reflect.DeepEqual(hits[0].values, want) {
		t.Errorf("merged patterns: want %v (global-first, exclusions-second, deduped), got %v",
			want, hits[0].values)
	}
	if strings.Contains(logs, "sonar.global.exclusions") && strings.Contains(logs, "not available on SQC") {
		t.Errorf("must NOT Warn about sonar.global.exclusions being SQC-missing — its patterns were migrated:\n%s", logs)
	}
}

// When only sonar.global.exclusions is customized (sonar.exclusions is
// at its SQS default of empty), the global patterns must still land on
// SQC's sonar.exclusions — otherwise the platform-enforced exclusions
// are silently lost during migration.
func TestRunSetGlobalSettingsAppliesGlobalExclusionsWhenExclusionsAtDefault(t *testing.T) {
	hits, _ := runGlobalSettingsTest(t,
		[]map[string]any{
			{"key": "sonar.global.exclusions", "values": []string{"vendor/**"}},
			// sonar.exclusions is at default — must NOT need to be in the
			// extract for the merge to fire (it would normally be filtered
			// out by isSettingCustomized).
		},
		[]map[string]any{
			{"key": "sonar.global.exclusions", "type": "STRING", "multiValues": true, "defaultValue": ""},
			{"key": "sonar.exclusions", "type": "STRING", "multiValues": true, "defaultValue": ""},
		},
		[]map[string]any{
			{"sonarcloud_org_key": "org1"},
		},
		[]map[string]any{
			{"key": "sonar.exclusions", "type": "STRING", "multiValues": true},
		},
	)
	if len(hits) != 1 {
		t.Fatalf("expected 1 call (global patterns onto sonar.exclusions), got %d", len(hits))
	}
	if hits[0].key != "sonar.exclusions" {
		t.Fatalf("expected sonar.exclusions to be the target, got %q", hits[0].key)
	}
	if !reflect.DeepEqual(hits[0].values, []string{"vendor/**"}) {
		t.Errorf("want values=[vendor/**] from global-only side, got %v", hits[0].values)
	}
}

// Same shape as the sonar.global.exclusions test, but exercises the
// second entry in globalExclusionPairs: SQS's
// sonar.global.test.exclusions must be folded into
// sonar.test.exclusions on SQC. Verifies the pair-table refactor covers
// the new pair without any extra code in the merge function.
func TestRunSetGlobalSettingsMergesGlobalTestExclusionsIntoTestExclusions(t *testing.T) {
	hits, logs := runGlobalSettingsTest(t,
		// SQS extract values — both keys customized.
		[]map[string]any{
			{"key": "sonar.global.test.exclusions", "values": []string{"src/test/gen/**", "vendor/test/**"}},
			{"key": "sonar.test.exclusions", "values": []string{"src/test/legacy/**", "src/test/gen/**"}}, // dedupe
		},
		[]map[string]any{
			{"key": "sonar.global.test.exclusions", "type": "STRING", "multiValues": true, "defaultValue": ""},
			{"key": "sonar.test.exclusions", "type": "STRING", "multiValues": true, "defaultValue": ""},
		},
		[]map[string]any{
			{"sonarcloud_org_key": "org1"},
		},
		[]map[string]any{
			// SQC only has sonar.test.exclusions.
			{"key": "sonar.test.exclusions", "type": "STRING", "multiValues": true},
		},
	)
	if len(hits) != 1 {
		t.Fatalf("expected exactly 1 settings.set call (merged test exclusions), got %d (%+v)", len(hits), hits)
	}
	if hits[0].key != "sonar.test.exclusions" {
		t.Fatalf("expected the call to land on sonar.test.exclusions, got %q", hits[0].key)
	}
	want := []string{"src/test/gen/**", "vendor/test/**", "src/test/legacy/**"}
	if !reflect.DeepEqual(hits[0].values, want) {
		t.Errorf("merged patterns: want %v (global-first, local-second, deduped), got %v",
			want, hits[0].values)
	}
	if strings.Contains(logs, "sonar.global.test.exclusions") && strings.Contains(logs, "not available on SQC") {
		t.Errorf("must NOT Warn about sonar.global.test.exclusions being SQC-missing — its patterns were migrated:\n%s", logs)
	}
}

// When only sonar.global.test.exclusions is customized
// (sonar.test.exclusions at default), the global patterns must still
// reach SQC's sonar.test.exclusions — same behaviour as the exclusions
// pair, just one row over in globalExclusionPairs.
func TestRunSetGlobalSettingsAppliesGlobalTestExclusionsWhenTestExclusionsAtDefault(t *testing.T) {
	hits, _ := runGlobalSettingsTest(t,
		[]map[string]any{
			{"key": "sonar.global.test.exclusions", "values": []string{"vendor/test/**"}},
		},
		[]map[string]any{
			{"key": "sonar.global.test.exclusions", "type": "STRING", "multiValues": true, "defaultValue": ""},
			{"key": "sonar.test.exclusions", "type": "STRING", "multiValues": true, "defaultValue": ""},
		},
		[]map[string]any{
			{"sonarcloud_org_key": "org1"},
		},
		[]map[string]any{
			{"key": "sonar.test.exclusions", "type": "STRING", "multiValues": true},
		},
	)
	if len(hits) != 1 {
		t.Fatalf("expected 1 call (global test patterns onto sonar.test.exclusions), got %d", len(hits))
	}
	if hits[0].key != "sonar.test.exclusions" {
		t.Fatalf("expected sonar.test.exclusions to be the target, got %q", hits[0].key)
	}
	if !reflect.DeepEqual(hits[0].values, []string{"vendor/test/**"}) {
		t.Errorf("want values=[vendor/test/**] from global-only side, got %v", hits[0].values)
	}
}

// renderValueSummary returns the "value=<bold>X</bold>" fragment used
// in per-row Detail strings, e.g. "Applied value=true" / "Applied
// value=a,b to all projects". The value portion is wrapped with the
// inline bold markers so the PDF renderer stresses it. Multi-value
// settings collapse to a comma-joined CSV (matching what /api/settings/set
// actually expects); property-set settings keep their JSON form because
// flattening would lose structure.
func TestRenderValueSummary(t *testing.T) {
	cases := []struct {
		name string
		rec  globalSettingResult
		want string
	}{
		{"single", globalSettingResult{Value: "true"}, "value=" + InlineBoldStart + "true" + InlineBoldEnd},
		{"multi", globalSettingResult{Values: []string{"a", "b"}}, "value=" + InlineBoldStart + "a,b" + InlineBoldEnd},
		{"fieldValues", globalSettingResult{
			FieldValues: []map[string]any{{"fileRegexp": "x"}},
		}, "value=" + InlineBoldStart + `[{"fileRegexp":"x"}]` + InlineBoldEnd},
	}
	for _, c := range cases {
		got := renderValueSummary(c.rec)
		if got != c.want {
			t.Errorf("%s: want %q, got %q", c.name, c.want, got)
		}
	}
}

// When SQC rejects the org-scope POST AND every project in the org
// also fails (e.g. the projects don't actually exist in SQC because
// createProjects recorded phantom entries — issue #193), the row's
// Detail must NOT say "Applied to all projects". The status switches
// to "failed" and the wording reflects what actually happened so the
// operator isn't misled.
func TestRunSetGlobalSettingsFanOutAllFailedRendersAsFailed(t *testing.T) {
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("component") != "" {
			// Every project fan-out fails — mimics #193 phantom
			// projects that SQC says don't exist.
			http.Error(w, `{"errors":[{"msg":"Project doesn't exist"}]}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"errors":[{"msg":"Provided property can't be set at organization level: `+r.FormValue("key")+`"}]}`, http.StatusBadRequest)
	})
	mountSettingsDefinitionsScoped(cloudMux,
		[]map[string]any{{"key": "sonar.html.file.suffixes", "type": "STRING", "multiValues": true}},
		[]map[string]any{{"key": "sonar.html.file.suffixes", "type": "STRING", "multiValues": true}},
	)
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettings", []map[string]any{
		{"key": "sonar.html.file.suffixes", "values": []string{".html", ".xhtml"}},
	})
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettingsDefinitions", []map[string]any{
		{"key": "sonar.html.file.suffixes", "type": "STRING", "multiValues": true, "defaultValue": ""},
	})
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"sonarcloud_org_key": "org1"},
	})
	pw, _ := e.Store.Writer("createProjects")
	for _, key := range []string{"projA", "projB"} {
		b, _ := json.Marshal(map[string]any{
			"key": key, "server_url": testServerURL,
			"sonarcloud_org_key": "org1", "cloud_project_key": "org1_" + key,
		})
		pw.WriteOne(b)
	}

	if err := runSetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetGlobalSettings: %v", err)
	}

	out, _ := e.Store.ReadAll("setGlobalSettings")
	if len(out) != 1 {
		t.Fatalf("expected one record, got %d", len(out))
	}
	var rec struct {
		Outcomes []struct {
			Org    string `json:"org"`
			Status string `json:"status"`
			Detail string `json:"detail"`
			Reason string `json:"reason"`
		} `json:"outcomes"`
	}
	_ = json.Unmarshal(out[0], &rec)
	if len(rec.Outcomes) != 1 || rec.Outcomes[0].Status != "failed" {
		t.Fatalf("expected status=failed when every fan-out project 404s, got %+v", rec.Outcomes)
	}
	if strings.Contains(rec.Outcomes[0].Detail, "Applied to all projects") {
		t.Errorf("Detail must NOT claim \"Applied to all projects\" when none succeeded, got %q", rec.Outcomes[0].Detail)
	}
	if !strings.HasPrefix(rec.Outcomes[0].Detail, "Failed:") {
		t.Errorf("Detail should start with \"Failed:\", got %q", rec.Outcomes[0].Detail)
	}
	if rec.Outcomes[0].Reason == "" {
		t.Errorf("Reason must carry the API error message for the Failed bucket, got empty")
	}
}

// Mixed fan-out outcome: some projects succeed, others fail. The row
// status becomes "partial" so the report puts it in the Partial
// bucket, and the Detail wording reflects the actual N/M counts
// instead of falsely claiming "all projects".
func TestRunSetGlobalSettingsFanOutPartialRendersAsPartial(t *testing.T) {
	cloudMux := http.NewServeMux()
	var failProject = "org1_projA"
	cloudMux.HandleFunc("POST /api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		comp := r.FormValue("component")
		if comp == "" {
			http.Error(w, `{"errors":[{"msg":"Provided property can't be set at organization level: `+r.FormValue("key")+`"}]}`, http.StatusBadRequest)
			return
		}
		if comp == failProject {
			http.Error(w, `{"errors":[{"msg":"Project doesn't exist"}]}`, http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mountSettingsDefinitionsScoped(cloudMux,
		[]map[string]any{{"key": "sonar.html.file.suffixes", "type": "STRING", "multiValues": true}},
		[]map[string]any{{"key": "sonar.html.file.suffixes", "type": "STRING", "multiValues": true}},
	)
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettings", []map[string]any{
		{"key": "sonar.html.file.suffixes", "values": []string{".html"}},
	})
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettingsDefinitions", []map[string]any{
		{"key": "sonar.html.file.suffixes", "type": "STRING", "multiValues": true, "defaultValue": ""},
	})
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"sonarcloud_org_key": "org1"},
	})
	pw, _ := e.Store.Writer("createProjects")
	for _, key := range []string{"projA", "projB"} {
		b, _ := json.Marshal(map[string]any{
			"key": key, "server_url": testServerURL,
			"sonarcloud_org_key": "org1", "cloud_project_key": "org1_" + key,
		})
		pw.WriteOne(b)
	}

	if err := runSetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetGlobalSettings: %v", err)
	}

	out, _ := e.Store.ReadAll("setGlobalSettings")
	var rec struct {
		Outcomes []struct {
			Org    string `json:"org"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"outcomes"`
	}
	_ = json.Unmarshal(out[0], &rec)
	if len(rec.Outcomes) != 1 || rec.Outcomes[0].Status != "partial" {
		t.Fatalf("expected status=partial when fan-out is mixed, got %+v", rec.Outcomes)
	}
	if !strings.Contains(rec.Outcomes[0].Detail, " to 1 of 2 projects") {
		t.Errorf("Detail must report the N/M count, got %q", rec.Outcomes[0].Detail)
	}
	if !strings.Contains(rec.Outcomes[0].Detail, "failed: "+failProject) {
		t.Errorf("Detail must enumerate the failed project, got %q", rec.Outcomes[0].Detail)
	}
}

// isSettingCustomized's job is to filter out SQS-side global settings
// whose value happens to equal the SQS default — those are not real
// customizations and migrating them just inflates the SQC API call
// count (issue #196). The comparison uses parentValue / parentValues
// as the primary signal because list_definitions's defaultValue is
// missing for some setting keys.
func TestIsSettingCustomized(t *testing.T) {
	mk := func(m map[string]any) json.RawMessage {
		b, _ := json.Marshal(m)
		return b
	}
	cases := []struct {
		name        string
		raw         map[string]any
		defaultVal  string
		wantCustom  bool
		description string
	}{
		// Scalar paths
		{
			name:        "scalar value matches parentValue",
			raw:         map[string]any{"value": "20", "parentValue": "20"},
			wantCustom:  false,
			description: "value=parentValue → not customized (issue #196 case)",
		},
		{
			name:       "scalar value matches defaultValue (no parentValue)",
			raw:        map[string]any{"value": "true"},
			defaultVal: "true",
			wantCustom: false,
		},
		{
			name:       "scalar value differs from parentValue",
			raw:        map[string]any{"value": "true", "parentValue": "false"},
			wantCustom: true,
		},
		{
			name:        "parentValue takes precedence when both available",
			raw:         map[string]any{"value": "true", "parentValue": "true"},
			defaultVal:  "false", // intentional mismatch; parentValue wins
			wantCustom:  false,
			description: "if parentValue agrees with value, it's not customized even when list_definitions says otherwise",
		},

		// Multi-value paths
		{
			name:        "multi values match parentValues (different order)",
			raw:         map[string]any{"values": []string{".kt", ".kts"}, "parentValues": []string{".kts", ".kt"}},
			wantCustom:  false,
			description: "sorted equality — order doesn't matter",
		},
		{
			name:       "multi values match defaultValue CSV (no parentValues)",
			raw:        map[string]any{"values": []string{"a", "b"}},
			defaultVal: "b,a",
			wantCustom: false,
		},
		{
			name:       "multi values differ in element count",
			raw:        map[string]any{"values": []string{".kt"}, "parentValues": []string{".kt", ".kts"}},
			wantCustom: true,
		},
		{
			name:       "multi values differ in element content",
			raw:        map[string]any{"values": []string{"a", "c"}, "parentValues": []string{"a", "b"}},
			wantCustom: true,
		},
		{
			name:       "parentValues takes precedence over defaultValue",
			raw:        map[string]any{"values": []string{"a", "b"}, "parentValues": []string{"a", "b"}},
			defaultVal: "x,y", // intentional mismatch
			wantCustom: false,
		},

		// Property-set path — opaque, always treated as customized
		{
			name:       "property-set (fieldValues populated)",
			raw:        map[string]any{"fieldValues": []map[string]any{{"k": "v"}}},
			wantCustom: true,
		},

		// Missing both parentValue/parentValues AND defaultValue
		{
			name:       "scalar with no signal — treated as customized",
			raw:        map[string]any{"value": "anything"},
			wantCustom: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsSettingCustomized(mk(c.raw), c.defaultVal)
			if got != c.wantCustom {
				t.Errorf("%s: want customized=%v, got %v (raw=%v default=%q)",
					c.description, c.wantCustom, got, c.raw, c.defaultVal)
			}
		})
	}
}

// SQS-only settings (issue #200) must be intercepted before the API
// loop. Some are dropped silently — no report row at all — and some
// emit a single section-level note (Organization="") describing why
// the value isn't portable. This table test pins each of the four
// keys plus a control key that should pass through unchanged.
func TestPartitionSQSOnlySettings(t *testing.T) {
	mk := func(m map[string]any) json.RawMessage {
		b, _ := json.Marshal(m)
		return b
	}
	cases := []struct {
		name       string
		raw        map[string]any
		wantSilent bool
		wantNote   string // empty == not emitted; substring match
	}{
		{
			name:       "serverBaseURL is always silent",
			raw:        map[string]any{"key": "sonar.core.serverBaseURL", "value": "https://my-sonar.example.com"},
			wantSilent: true,
		},
		{
			name:       "disableNotificationOnUpdate is always silent",
			raw:        map[string]any{"key": "sonar.builtInQualityProfiles.disableNotificationOnUpdate", "value": "true"},
			wantSilent: true,
		},
		{
			name:       "allowDisableInheritedRules=false is silent",
			raw:        map[string]any{"key": "sonar.qualityProfiles.allowDisableInheritedRules", "value": "false"},
			wantSilent: true,
		},
		{
			name:     "allowDisableInheritedRules=true emits the canonical not-on-SQC note",
			raw:      map[string]any{"key": "sonar.qualityProfiles.allowDisableInheritedRules", "value": "true"},
			wantNote: SkipDetailNotOnSQC,
		},
		{
			name:       "ratingGrid at default is silent",
			raw:        map[string]any{"key": "sonar.technicalDebt.ratingGrid", "value": "0.05,0.1,0.2,0.5"},
			wantSilent: true,
		},
		{
			name:     "ratingGrid customized emits the canonical not-on-SQC note",
			raw:      map[string]any{"key": "sonar.technicalDebt.ratingGrid", "value": "0.03,0.07,0.2,0.5"},
			wantNote: SkipDetailNotOnSQC,
		},
		{
			name: "unknown setting passes through",
			raw:  map[string]any{"key": "sonar.exclusions", "values": []string{"a"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rem, notes := partitionSQSOnlySettings([]json.RawMessage{mk(c.raw)})
			isSQSOnly := c.wantSilent || c.wantNote != ""
			if isSQSOnly {
				if len(rem) != 0 {
					t.Errorf("SQS-only key must be removed from customized, got %d", len(rem))
				}
			} else {
				if len(rem) != 1 {
					t.Errorf("non-SQS-only key must pass through, got %d", len(rem))
				}
			}
			if c.wantSilent {
				if len(notes) != 0 {
					t.Errorf("silent skip must emit no note, got %+v", notes)
				}
				return
			}
			if c.wantNote != "" {
				if len(notes) != 1 {
					t.Fatalf("expected one note, got %d", len(notes))
				}
				if !strings.Contains(notes[0].Outcomes[0].Detail, c.wantNote) {
					t.Errorf("note Detail must contain %q, got %q", c.wantNote, notes[0].Outcomes[0].Detail)
				}
				if notes[0].Outcomes[0].Org != "" {
					t.Errorf("note must NOT carry an Org (section-level), got %q", notes[0].Outcomes[0].Org)
				}
				if notes[0].Outcomes[0].Reason != "sqs-only" {
					t.Errorf("note Reason must be \"sqs-only\" for report grouping, got %q", notes[0].Outcomes[0].Reason)
				}
			}
		})
	}
}

// Regression for issue #200: when an SQS-only setting's value equals
// SQS's parentValue (i.e. it's at SQS's "default") but the per-key
// rule still wants a note — e.g.
// sonar.qualityProfiles.allowDisableInheritedRules=true on a SQS
// where parentValue=true — the note must still reach the report.
// The previous order ran isSettingCustomized BEFORE
// partitionSQSOnlySettings, so this key was dropped as
// "not-customized" and the section-level note never fired.
func TestRunSetGlobalSettingsSQSOnlyNoteFiresEvenAtSQSDefault(t *testing.T) {
	cloudMux := http.NewServeMux()
	_, _ = mountSettingsSetCapture(cloudMux)
	mountSettingsDefinitions(cloudMux)
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettings", []map[string]any{
		// value matches parentValue — isSettingCustomized's #196
		// fix would discard this if it ran first. The SQS-only
		// interceptor must catch it before that.
		{"key": "sonar.qualityProfiles.allowDisableInheritedRules",
			"value": "true", "parentValue": "true"},
	})
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettingsDefinitions", []map[string]any{
		{"key": "sonar.qualityProfiles.allowDisableInheritedRules",
			"type": "BOOLEAN", "defaultValue": "true"},
	})
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"sonarcloud_org_key": "orgA"},
	})

	if err := runSetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetGlobalSettings: %v", err)
	}

	records, _ := e.Store.ReadAll("setGlobalSettings")
	foundNote := false
	for _, raw := range records {
		key, _ := jsonpathString(raw, "key")
		if key != "sonar.qualityProfiles.allowDisableInheritedRules" {
			continue
		}
		var rec struct {
			Outcomes []struct {
				Org    string `json:"org"`
				Status string `json:"status"`
				Reason string `json:"reason"`
				Detail string `json:"detail"`
			} `json:"outcomes"`
		}
		_ = json.Unmarshal(raw, &rec)
		if len(rec.Outcomes) != 1 {
			t.Fatalf("expected one section-level outcome, got %d", len(rec.Outcomes))
		}
		oc := rec.Outcomes[0]
		if oc.Org != "" || oc.Reason != "sqs-only" {
			t.Errorf("note must be section-level (Org=\"\") with Reason=sqs-only, got %+v", oc)
		}
		if oc.Detail != SkipDetailNotOnSQC {
			t.Errorf("Detail must be the canonical not-on-SQC string, got %q", oc.Detail)
		}
		foundNote = true
	}
	if !foundNote {
		t.Errorf("section-level note for SQS-only key did NOT reach the report (even though value=true)")
	}
}

// End-to-end: when an SQS-only setting is customized, the API must
// NOT be called for it; the only sign in the JSONL output is the
// section-level note row.
func TestRunSetGlobalSettingsInterceptsSQSOnlyKeys(t *testing.T) {
	cloudMux := http.NewServeMux()
	mu, hitsPtr := mountSettingsSetCapture(cloudMux)
	mountSettingsDefinitions(cloudMux,
		// Pretend SQC knows about sonar.exclusions but NOT the
		// SQS-only keys; the migrate code shouldn't reach this
		// handler for them anyway.
		map[string]any{"key": "sonar.exclusions", "type": "STRING", "multiValues": true},
	)
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettings", []map[string]any{
		// Two SQS-only keys: one silent, one with a note.
		{"key": "sonar.core.serverBaseURL", "value": "https://my-sonar.example.com"},
		{"key": "sonar.technicalDebt.ratingGrid", "value": "0.03,0.07,0.2,0.5"},
		// One legit customization that SHOULD be migrated.
		{"key": "sonar.exclusions", "values": []string{"**/*.gen"}},
	})
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettingsDefinitions", []map[string]any{
		{"key": "sonar.core.serverBaseURL", "type": "STRING", "defaultValue": ""},
		{"key": "sonar.technicalDebt.ratingGrid", "type": "STRING", "defaultValue": "0.05,0.1,0.2,0.5"},
		{"key": "sonar.exclusions", "type": "STRING", "multiValues": true, "defaultValue": ""},
	})
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"sonarcloud_org_key": "orgA"},
		{"sonarcloud_org_key": "orgB"},
	})

	if err := runSetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetGlobalSettings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Exactly the customized sonar.exclusions PATCHes — one per
	// org. The SQS-only keys MUST not produce any settings.set call.
	if len(*hitsPtr) != 2 {
		t.Fatalf("expected only sonar.exclusions PATCHes (2 orgs × 1 key), got %d: %+v", len(*hitsPtr), *hitsPtr)
	}
	for _, h := range *hitsPtr {
		if h.key != "sonar.exclusions" {
			t.Errorf("unexpected SQS-only key reached SQC: %s", h.key)
		}
	}

	// JSONL output must contain a single section-level note for
	// the customized ratingGrid; serverBaseURL was silent so
	// nothing should appear for it.
	records, _ := e.Store.ReadAll("setGlobalSettings")
	noteCount := 0
	for _, raw := range records {
		key, _ := jsonpathString(raw, "key")
		if key == "sonar.technicalDebt.ratingGrid" || key == "sonar.core.serverBaseURL" {
			noteCount++
		}
	}
	if noteCount != 1 {
		t.Errorf("expected exactly one SQS-only note row (ratingGrid), got %d", noteCount)
	}
}

// jsonpathString is a tiny helper for the test above — pull a top
// level string field from a raw JSON line.
func jsonpathString(raw []byte, key string) (string, bool) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", false
	}
	s, ok := m[key].(string)
	return s, ok
}

// Compile-time guard: catch accidental removal of the helper that drives
// most tests above.
var _ = sync.Mutex{}
