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
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

// settingsHit captures the request shape a /api/settings/set handler saw,
// so each test can assert exactly one of value=, values=, or fieldValues=
// was sent (rather than allowing both shapes to coexist).
type settingsHit struct {
	key    string
	value  string
	values []string
	fields []string
}

// mountSettingsSetCapture wires a POST /api/settings/set handler that
// records every request into the returned slice (guarded by the returned
// mutex). Each test gets a fresh mux/server pair via this helper.
func mountSettingsSetCapture(mux *http.ServeMux) (*sync.Mutex, *[]settingsHit) {
	var (
		mu   sync.Mutex
		hits []settingsHit
	)
	mux.HandleFunc("POST /api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		hits = append(hits, settingsHit{
			key:    r.FormValue("key"),
			value:  r.FormValue("value"),
			values: append([]string(nil), r.Form["values"]...),
			fields: append([]string(nil), r.Form["fieldValues"]...),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return &mu, &hits
}

// mountSettingsDefinitions installs a list_definitions handler that
// returns the supplied keys. Each entry is "<key>:<type>:<multi>" e.g.
// "sonar.exclusions:STRING:true". Use this to drive the new
// definition-aware dispatcher in setProjectSettings.
func mountSettingsDefinitions(mux *http.ServeMux, defs ...map[string]any) {
	mux.HandleFunc("GET /api/settings/list_definitions", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"definitions": defs})
	})
}

// mountSettingsDefinitionsScoped distinguishes the two flavours of the
// list_definitions endpoint: when the SDK sends component=<projectKey>
// SQC returns the SUPERSET of definitions visible at that project
// (language settings, external-analyzer settings, etc.). The org-scope
// response is the strict subset. Issues #189 / #191 hinge on that
// difference — the migration tool uses it to decide which SQS global
// settings have to be propagated to every SQC project instead of being
// set at org level. Tests that exercise that code path mount BOTH
// responses through this helper.
func mountSettingsDefinitionsScoped(mux *http.ServeMux, orgScope, projectScope []map[string]any) {
	mux.HandleFunc("GET /api/settings/list_definitions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("component") != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"definitions": projectScope})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"definitions": orgScope})
	})
}

// TestRunSetProjectSettingsDispatchesByShape covers the FALLBACK path:
// when SQC's list_definitions has no entry for a setting key (custom or
// plugin-defined settings), the task dispatches based on the extract
// record's shape — values=[...] → SetValues, fieldValues=[...] →
// SetFieldValues, plain value → Set. Records with no payload at all are
// skipped silently.
func TestRunSetProjectSettingsDispatchesByShape(t *testing.T) {
	cloudMux := http.NewServeMux()
	mu, hitsPtr := mountSettingsSetCapture(cloudMux)
	// Empty definitions registry so EVERY setting falls back to extract
	// shape — that's exactly what this test exercises.
	mountSettingsDefinitions(cloudMux)
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	extractDir := filepath.Join(e.ExportDir, "extract-01", "getProjectSettings")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, _ := os.Create(filepath.Join(extractDir, "results.1.jsonl"))
	for _, rec := range []map[string]any{
		// Single scalar value — should hit the "value" path. Note the real
		// extract enriches the record with "project" (see
		// projectSettingsTask), not "projectKey" — mirror that shape so
		// any future regression on the field name is caught immediately.
		{"project": "proj1", "key": "sonar.cfamily.ignoreHeaderComments", "value": "false"},
		// Multi-value list — should hit the SetValues path.
		{"project": "proj1", "key": "sonar.exclusions", "values": []string{"src/gen/**", "**/*.spec.ts"}},
		// Property-set — should hit the SetFieldValues path.
		{"project": "proj1", "key": "sonar.issue.ignore.allfile",
			"fieldValues": []map[string]any{{"fileRegexp": "Generated test"}}},
		// Empty payload — must be skipped silently.
		{"project": "proj1", "key": "sonar.cleanup.something"},
	} {
		b, _ := json.Marshal(rec)
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()

	pw, _ := e.Store.Writer("createProjects")
	b, _ := json.Marshal(map[string]any{
		"key": "proj1", "server_url": testServerURL,
		"sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj1",
	})
	pw.WriteOne(b)

	if err := runSetProjectSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetProjectSettings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	hits := *hitsPtr
	if len(hits) != 3 {
		t.Fatalf("expected 3 settings calls (empty record skipped), got %d", len(hits))
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].key < hits[j].key })

	// sonar.cfamily.ignoreHeaderComments — single value.
	cfamily := hits[0]
	if cfamily.key != "sonar.cfamily.ignoreHeaderComments" || cfamily.value != "false" {
		t.Errorf("cfamily: got %+v", cfamily)
	}
	if len(cfamily.values) != 0 || len(cfamily.fields) != 0 {
		t.Errorf("cfamily: must not send values/fieldValues, got %+v", cfamily)
	}

	// sonar.exclusions — multi-value list, repeated "values" param.
	excl := hits[1]
	if excl.key != "sonar.exclusions" {
		t.Errorf("excl: wrong key %q", excl.key)
	}
	want := []string{"**/*.spec.ts", "src/gen/**"}
	sort.Strings(excl.values)
	for i := range want {
		if i >= len(excl.values) || excl.values[i] != want[i] {
			t.Errorf("excl: got values=%v, want %v", excl.values, want)
			break
		}
	}
	if excl.value != "" {
		t.Errorf("excl: must not send single value param, got %q", excl.value)
	}

	// sonar.issue.ignore.allfile — property-set.
	ifa := hits[2]
	if ifa.key != "sonar.issue.ignore.allfile" {
		t.Errorf("ifa: wrong key %q", ifa.key)
	}
	if len(ifa.fields) != 1 {
		t.Fatalf("ifa: expected 1 fieldValues entry, got %d", len(ifa.fields))
	}
	var fv map[string]any
	if err := json.Unmarshal([]byte(ifa.fields[0]), &fv); err != nil {
		t.Fatalf("ifa: fieldValues JSON: %v", err)
	}
	if fv["fileRegexp"] != "Generated test" {
		t.Errorf("ifa: wrong fieldValues content: %+v", fv)
	}
}

// TestRunSetProjectSettingsRespectsSQCDefinitions is the regression test
// for sonar.java.file.suffixes (and any other "looks-multi-on-SQS, stored-
// as-CSV-string-on-SQC" setting). SQC returns 204 for a malformed shape
// but doesn't persist — the only way to know which shape is right is to
// ask SQC's list_definitions and dispatch on its multiValues / type flags.
func TestRunSetProjectSettingsRespectsSQCDefinitions(t *testing.T) {
	cloudMux := http.NewServeMux()
	mu, hitsPtr := mountSettingsSetCapture(cloudMux)
	// SQC says: file.suffixes is single STRING (CSV), exclusions is
	// multi-value, issue.ignore.allfile is a PROPERTY_SET. Exactly what
	// list_definitions returns against sonarcloud.io.
	mountSettingsDefinitions(cloudMux,
		map[string]any{"key": "sonar.java.file.suffixes", "type": "STRING", "multiValues": false},
		map[string]any{"key": "sonar.exclusions", "type": "STRING", "multiValues": true},
		map[string]any{"key": "sonar.issue.ignore.allfile", "type": "PROPERTY_SET", "multiValues": false},
	)
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	extractDir := filepath.Join(e.ExportDir, "extract-01", "getProjectSettings")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Every extract record comes in with values=[...] (which is what SQS
	// returns regardless of how SQC has defined the setting). The dispatcher
	// must look at SQC's definition to decide the post shape.
	f, _ := os.Create(filepath.Join(extractDir, "results.1.jsonl"))
	for _, rec := range []map[string]any{
		{"project": "proj1", "key": "sonar.java.file.suffixes",
			"values": []string{".jav", ".java", ".javax"}},
		{"project": "proj1", "key": "sonar.exclusions",
			"values": []string{"src/gen/**", "**/*.spec.ts"}},
		{"project": "proj1", "key": "sonar.issue.ignore.allfile",
			"fieldValues": []map[string]any{{"fileRegexp": "Generated test"}}},
	} {
		b, _ := json.Marshal(rec)
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()

	pw, _ := e.Store.Writer("createProjects")
	b, _ := json.Marshal(map[string]any{
		"key": "proj1", "server_url": testServerURL,
		"sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj1",
	})
	pw.WriteOne(b)

	if err := runSetProjectSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetProjectSettings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	hits := *hitsPtr
	if len(hits) != 3 {
		t.Fatalf("expected 3 settings calls, got %d", len(hits))
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].key < hits[j].key })

	// sonar.exclusions: SQC defines multiValues=true → values= shape.
	excl := hits[0]
	if excl.key != "sonar.exclusions" {
		t.Fatalf("excl: wrong key %q", excl.key)
	}
	sort.Strings(excl.values)
	if !reflect.DeepEqual(excl.values, []string{"**/*.spec.ts", "src/gen/**"}) {
		t.Errorf("excl: got values=%v, want [**/*.spec.ts src/gen/**]", excl.values)
	}
	if excl.value != "" {
		t.Errorf("excl: must NOT collapse to a single CSV value (multiValues=true), got %q", excl.value)
	}

	// sonar.issue.ignore.allfile: PROPERTY_SET → fieldValues=.
	ifa := hits[1]
	if ifa.key != "sonar.issue.ignore.allfile" {
		t.Fatalf("ifa: wrong key %q", ifa.key)
	}
	if len(ifa.fields) != 1 {
		t.Errorf("ifa: expected 1 fieldValues entry, got %d", len(ifa.fields))
	}

	// sonar.java.file.suffixes: SQC defines multiValues=false → must be
	// CSV-joined and sent as value=, NOT as values=. This is the central
	// regression — sending values= here returns 204 but silently no-ops.
	jfs := hits[2]
	if jfs.key != "sonar.java.file.suffixes" {
		t.Fatalf("jfs: wrong key %q", jfs.key)
	}
	if len(jfs.values) != 0 {
		t.Errorf("jfs: must NOT send values=[%s] for a single-value SQC setting (would 204 but not persist)",
			strings.Join(jfs.values, ","))
	}
	// Order of CSV elements mirrors the extract record's array order.
	if jfs.value != ".jav,.java,.javax" {
		t.Errorf("jfs: expected value=\".jav,.java,.javax\", got %q", jfs.value)
	}
}

// When a source project failed createProjects (or wasn't in the migrate
// scope), its settings extract records have no corresponding entry in
// projectKeyMap. Historically those records were silently dropped, which
// made setting-migration cascade failures invisible — users would see "task
// summary succeeded=N" without any hint that N was smaller than expected.
// This test enforces that the migrate task now logs a Warn line per dropped
// record, naming both the project key and the setting key.
func TestRunSetProjectSettingsWarnsOnUnmappedProject(t *testing.T) {
	cloudMux := http.NewServeMux()
	mountSettingsDefinitions(cloudMux)
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)
	// Capture Warn output so the test can assert on it.
	var buf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	extractDir := filepath.Join(e.ExportDir, "extract-01", "getProjectSettings")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, _ := os.Create(filepath.Join(extractDir, "results.1.jsonl"))
	// One record for a project that IS in createProjects, and one for a
	// project that ISN'T (the realistic cascade case: createProjects failed
	// for okorach-oss_sonar-tools, so its setting must surface a Warn).
	for _, rec := range []map[string]any{
		{"project": "proj1", "key": "sonar.exclusions", "values": []string{"**/*.gen"}},
		{"project": "okorach-oss_sonar-tools", "key": "sonar.java.file.suffixes",
			"values": []string{".java", ".jav"}},
	} {
		b, _ := json.Marshal(rec)
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()

	pw, _ := e.Store.Writer("createProjects")
	b, _ := json.Marshal(map[string]any{
		"key": "proj1", "server_url": testServerURL,
		"sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj1",
	})
	pw.WriteOne(b)

	if err := runSetProjectSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetProjectSettings: %v", err)
	}

	logs := buf.String()
	if !strings.Contains(logs, "project not found in migration scope") {
		t.Errorf("expected Warn for unmapped project, got:\n%s", logs)
	}
	if !strings.Contains(logs, "okorach-oss_sonar-tools") {
		t.Errorf("expected dropped project key in Warn, got:\n%s", logs)
	}
	if !strings.Contains(logs, "sonar.java.file.suffixes") {
		t.Errorf("expected dropped setting key in Warn, got:\n%s", logs)
	}
	// The mapped record (proj1/sonar.exclusions) should NOT produce a Warn.
	if strings.Contains(logs, "proj1") && strings.Contains(logs, "not found") {
		t.Errorf("mapped project must not Warn, got:\n%s", logs)
	}
}

// runProjectSettingsPropagationTest wires up cloud + api + executor
// for the propagation-pass tests below. perProjectExtract is what
// projectSettingsTask would have written (per-project overrides);
// sqsGlobals is what getServerSettings produced; sqsDefs is what
// getServerSettingsDefinitions produced; orgDefs / projectDefs are the
// SQC list_definitions responses for org-scope and project-scope.
func runProjectSettingsPropagationTest(t *testing.T,
	projects []map[string]any,
	perProjectExtract []map[string]any,
	sqsGlobals []map[string]any,
	sqsDefs []map[string]any,
	orgDefs, projectDefs []map[string]any,
) (hits []settingsHit, logs string) {
	t.Helper()
	cloudMux := http.NewServeMux()
	muHits, hitsPtr := mountSettingsSetCapture(cloudMux)
	mountSettingsDefinitionsScoped(cloudMux, orgDefs, projectDefs)
	addDefaultCloudHandler(cloudMux)
	cloudSrv := httptest.NewServer(cloudMux)
	t.Cleanup(cloudSrv.Close)

	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	var buf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Per-project SQS extract → extract dir.
	extractDir := filepath.Join(dir, "extract-01", "getProjectSettings")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, _ := os.Create(filepath.Join(extractDir, "results.1.jsonl"))
	for _, r := range perProjectExtract {
		b, _ := json.Marshal(r)
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()

	// SQS global extracts → extract dir (the propagation pass reads
	// from here via readExtractItems, NOT the migrate store).
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettings", sqsGlobals)
	writeExtractTaskJSONL(t, dir, "extract-01", "getServerSettingsDefinitions", sqsDefs)
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)

	// createProjects → migrate store.
	pw, _ := e.Store.Writer("createProjects")
	for _, p := range projects {
		b, _ := json.Marshal(p)
		pw.WriteOne(b)
	}

	if err := runSetProjectSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetProjectSettings: %v", err)
	}
	muHits.Lock()
	defer muHits.Unlock()
	hits = append(hits, *hitsPtr...)
	return hits, buf.String()
}

// Customized SQS global whose key is project-scope-only on SQC must be
// applied to EVERY SQC project — that's the core requirement of #189
// and #191 (language settings, external analyzer settings). Closes the
// gap where SQS had a customized global value that SQC could only
// store per-project.
func TestRunSetProjectSettingsPropagatesGlobalToAllProjects(t *testing.T) {
	hits, _ := runProjectSettingsPropagationTest(t,
		// Two projects, same org.
		[]map[string]any{
			{"key": "projA", "server_url": testServerURL,
				"sonarcloud_org_key": "org1", "cloud_project_key": "org1_projA"},
			{"key": "projB", "server_url": testServerURL,
				"sonarcloud_org_key": "org1", "cloud_project_key": "org1_projB"},
		},
		// No per-project overrides.
		nil,
		// SQS global sonar.java.file.suffixes is customized.
		[]map[string]any{
			{"key": "sonar.java.file.suffixes", "values": []string{".java", ".jav"}},
		},
		// SQS defaultValue empty, so the global is "customized".
		[]map[string]any{
			{"key": "sonar.java.file.suffixes", "type": "STRING", "multiValues": false, "defaultValue": ".java"},
		},
		// SQC org-scope: empty (the language key is NOT here).
		[]map[string]any{},
		// SQC project-scope: language key IS visible.
		[]map[string]any{
			{"key": "sonar.java.file.suffixes", "type": "STRING", "multiValues": false},
		},
	)
	if len(hits) != 2 {
		t.Fatalf("expected 2 settings.set calls (one per project), got %d (%+v)", len(hits), hits)
	}
	seen := map[string]string{}
	for _, h := range hits {
		seen[h.value] = h.key
	}
	// SDK's CSV join: ".java,.jav" because multiValues=false on SQC.
	if _, ok := seen[".java,.jav"]; !ok {
		t.Errorf("expected value=.java,.jav (CSV-joined), got hits %+v", hits)
	}
}

// When a project has a per-project SQS override, the override must win
// for that project — but the global value still propagates to the
// other projects in the same org.
func TestRunSetProjectSettingsPerProjectOverrideWinsOverGlobal(t *testing.T) {
	hits, logs := runProjectSettingsPropagationTest(t,
		[]map[string]any{
			{"key": "projA", "server_url": testServerURL,
				"sonarcloud_org_key": "org1", "cloud_project_key": "org1_projA"},
			{"key": "projB", "server_url": testServerURL,
				"sonarcloud_org_key": "org1", "cloud_project_key": "org1_projB"},
		},
		// projA has a per-project override; projB does NOT.
		[]map[string]any{
			{"project": "projA", "key": "sonar.java.file.suffixes",
				"values": []string{".jjj"}},
		},
		// SQS global sonar.java.file.suffixes is customized.
		[]map[string]any{
			{"key": "sonar.java.file.suffixes", "values": []string{".java", ".jav"}},
		},
		[]map[string]any{
			{"key": "sonar.java.file.suffixes", "type": "STRING", "multiValues": false, "defaultValue": ".java"},
		},
		[]map[string]any{},
		[]map[string]any{
			{"key": "sonar.java.file.suffixes", "type": "STRING", "multiValues": false},
		},
	)
	// One call for the override (.jjj on projA), one for the
	// propagated global (.java,.jav on projB). Project A's override
	// must NOT be clobbered by the global propagation pass.
	if len(hits) != 2 {
		t.Fatalf("expected 2 settings.set calls, got %d", len(hits))
	}
	// SQC project-scope defs say sonar.java.file.suffixes is
	// multiValues=false, so the SDK CSV-joins both calls into
	// value=. Distinguish the two by content.
	values := []string{hits[0].value, hits[1].value}
	sort.Strings(values)
	if values[0] != ".java,.jav" {
		t.Errorf("expected propagated global value=.java,.jav, got %v", values)
	}
	if values[1] != ".jjj" {
		t.Errorf("expected per-project override value=.jjj, got %v", values)
	}
	if !strings.Contains(logs, "per-project override wins") {
		t.Errorf("expected Debug log noting the override-wins decision, got:\n%s", logs)
	}
}

// When the customized SQS global's key DOES exist at SQC org-scope,
// setProjectSettings's propagation pass must leave it alone —
// setGlobalSettings is responsible for that one. Prevents double-apply
// (org + every project) regressions.
func TestRunSetProjectSettingsLeavesOrgScopeGlobalsToSetGlobalSettings(t *testing.T) {
	hits, _ := runProjectSettingsPropagationTest(t,
		[]map[string]any{
			{"key": "projA", "server_url": testServerURL,
				"sonarcloud_org_key": "org1", "cloud_project_key": "org1_projA"},
		},
		nil,
		[]map[string]any{
			// sonar.exclusions: SQC has it at org scope.
			{"key": "sonar.exclusions", "values": []string{"**/*.gen"}},
		},
		[]map[string]any{
			{"key": "sonar.exclusions", "type": "STRING", "multiValues": true, "defaultValue": ""},
		},
		// Org scope: includes sonar.exclusions.
		[]map[string]any{
			{"key": "sonar.exclusions", "type": "STRING", "multiValues": true},
		},
		// Project scope: superset.
		[]map[string]any{
			{"key": "sonar.exclusions", "type": "STRING", "multiValues": true},
		},
	)
	if len(hits) != 0 {
		t.Fatalf("setProjectSettings must NOT propagate org-scope globals (setGlobalSettings owns them), got %d hits: %+v",
			len(hits), hits)
	}
}
