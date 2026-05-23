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
	if !strings.Contains(rec.Outcomes[0].Detail, "Applied to all projects") {
		t.Errorf("Detail must say \"Applied to all projects\", got %q", rec.Outcomes[0].Detail)
	}
	if !strings.Contains(rec.Outcomes[0].Detail, "values=[**/jacoco*.xml]") {
		t.Errorf("Detail must include the value summary, got %q", rec.Outcomes[0].Detail)
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
	e.Logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

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

// renderValueSummary picks the compact value representation each
// orgOutcome.Detail string uses for the parenthesised data tag —
// "Applied (value=X)", "Applied (values=[a,b])", or
// "Applied (fieldValues=[...])". Pinning this directly keeps the
// per-row wording stable across refactors.
func TestRenderValueSummary(t *testing.T) {
	cases := []struct {
		name string
		rec  globalSettingResult
		want string
	}{
		{"single", globalSettingResult{Value: "true"}, "value=true"},
		{"multi", globalSettingResult{Values: []string{"a", "b"}}, "values=[a,b]"},
		{"fieldValues", globalSettingResult{
			FieldValues: []map[string]any{{"fileRegexp": "x"}},
		}, `fieldValues=[{"fileRegexp":"x"}]`},
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
	if !strings.Contains(rec.Outcomes[0].Detail, "Applied to 1 of 2 projects") {
		t.Errorf("Detail must report the N/M count, got %q", rec.Outcomes[0].Detail)
	}
	if !strings.Contains(rec.Outcomes[0].Detail, "failed: "+failProject) {
		t.Errorf("Detail must enumerate the failed project, got %q", rec.Outcomes[0].Detail)
	}
}

// Compile-time guard: catch accidental removal of the helper that drives
// most tests above.
var _ = sync.Mutex{}
