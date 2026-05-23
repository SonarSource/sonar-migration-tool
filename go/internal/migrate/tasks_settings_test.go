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

// TestRunSetProjectSettingsDispatchesByShape verifies that the migrate task
// dispatches each /api/settings/values record to the right SQC PUT shape:
//
//   - single "value"   → "value" form field
//   - multi  "values"  → repeated "values" form fields
//   - "fieldValues"    → repeated "fieldValues" JSON-encoded form fields
//
// Records missing every payload (only a key) must be skipped silently —
// not counted as a failure.
func TestRunSetProjectSettingsDispatchesByShape(t *testing.T) {
	type recorded struct {
		key    string
		value  string
		values []string
		fields []string
	}
	var (
		mu   sync.Mutex
		hits []recorded
	)
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		hits = append(hits, recorded{
			key:    r.FormValue("key"),
			value:  r.FormValue("value"),
			values: append([]string(nil), r.Form["values"]...),
			fields: append([]string(nil), r.Form["fieldValues"]...),
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

	extractDir := filepath.Join(dir, "extract-01", "getProjectSettings")
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

// When a source project failed createProjects (or wasn't in the migrate
// scope), its settings extract records have no corresponding entry in
// projectKeyMap. Historically those records were silently dropped, which
// made setting-migration cascade failures invisible — users would see "task
// summary succeeded=N" without any hint that N was smaller than expected.
// This test enforces that the migrate task now logs a Warn line per dropped
// record, naming both the project key and the setting key.
func TestRunSetProjectSettingsWarnsOnUnmappedProject(t *testing.T) {
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()

	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	// Capture Warn output so the test can assert on it.
	var buf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	extractDir := filepath.Join(dir, "extract-01", "getProjectSettings")
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
