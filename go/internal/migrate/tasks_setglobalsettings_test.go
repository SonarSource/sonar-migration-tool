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

// renderGlobalSettingDetail must annotate the merged record so the
// summary report displays the cross-key provenance — this is the report
// requirement called out in the issue. The detail string is formed from
// the carried global-key name so the same code path covers every pair
// in globalExclusionPairs.
func TestRenderGlobalSettingDetailMentionsMerge(t *testing.T) {
	cases := []struct {
		name   string
		rec    globalSettingResult
		expect string
	}{
		{
			name: "exclusions",
			rec: globalSettingResult{
				Key:              "sonar.exclusions",
				Values:           []string{"a", "b"},
				AppliedOrgs:      []string{"org1"},
				MergedFromGlobal: "sonar.global.exclusions",
			},
			expect: "merged from sonar.global.exclusions + sonar.exclusions",
		},
		{
			name: "test exclusions",
			rec: globalSettingResult{
				Key:              "sonar.test.exclusions",
				Values:           []string{"a"},
				AppliedOrgs:      []string{"org1"},
				MergedFromGlobal: "sonar.global.test.exclusions",
			},
			expect: "merged from sonar.global.test.exclusions + sonar.test.exclusions",
		},
	}
	for _, c := range cases {
		got := renderGlobalSettingDetail(c.rec)
		if !strings.Contains(got, c.expect) {
			t.Errorf("%s: expected detail to contain %q, got: %s", c.name, c.expect, got)
		}
	}
}

// Compile-time guard: catch accidental removal of the helper that drives
// most tests above.
var _ = sync.Mutex{}
