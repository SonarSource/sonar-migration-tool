// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
)

// --- Pure exported-filter helpers (were 0.0%) ---

func TestExportedHotspotHasManualChanges(t *testing.T) {
	tests := []struct {
		name        string
		status      string
		resolution  string
		hasComments bool
		want        bool
	}{
		{name: "TO_REVIEW no comments — skip", status: "TO_REVIEW", want: false},
		{name: "TO_REVIEW with comment — sync", status: "TO_REVIEW", hasComments: true, want: true},
		{name: "REVIEWED SAFE — sync", status: "REVIEWED", resolution: "SAFE", want: true},
		{name: "REVIEWED ACKNOWLEDGED — sync", status: "REVIEWED", resolution: "ACKNOWLEDGED", want: true},
		{name: "REVIEWED FIXED — sync", status: "REVIEWED", resolution: "FIXED", want: true},
		{name: "REVIEWED unknown — skip", status: "REVIEWED", resolution: "WHATEVER", want: false},
		{name: "case-insensitive", status: "reviewed", resolution: "safe", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HotspotHasManualChanges(tc.status, tc.resolution, tc.hasComments); got != tc.want {
				t.Errorf("HotspotHasManualChanges(%q,%q,%v) = %v, want %v",
					tc.status, tc.resolution, tc.hasComments, got, tc.want)
			}
		})
	}
}

// TestIsAcknowledgedResolution now lives in tasks_hotspotsync_test.go
// upstream, which covers the same cases as named subtests. Removed here
// to avoid a redeclaration build failure.

// --- Extract-parsing helpers (were 0.0%) ---

func TestParseMatchableHotspotFullShape(t *testing.T) {
	raw := json.RawMessage(`{
		"key": "hs-1",
		"component": "proj:src/Main.java",
		"status": "REVIEWED",
		"resolution": "SAFE",
		"line": 42,
		"textRange": {"startOffset": 17},
		"rule": {"key": "java:S2092"},
		"branch": "develop",
		"comment": [
			{"login": "alice", "htmlText": "<b>hi</b>", "markdown": "hi", "createdAt": "2024-01-01"},
			{"login": "bob", "markdown": "", "htmlText": ""}
		]
	}`)
	h := parseMatchableHotspot(raw)
	if h.Key != "hs-1" || h.Component != "proj:src/Main.java" {
		t.Fatalf("unexpected key/component: %+v", h)
	}
	if h.Status != "REVIEWED" || h.Resolution != "SAFE" {
		t.Errorf("status/resolution = %q/%q", h.Status, h.Resolution)
	}
	if h.Line != 42 {
		t.Errorf("line = %d, want 42", h.Line)
	}
	if h.Offset != 17 {
		t.Errorf("offset = %d, want 17 (from textRange.startOffset)", h.Offset)
	}
	if h.RuleKey != "java:S2092" {
		t.Errorf("ruleKey = %q, want java:S2092 (nested rule.key)", h.RuleKey)
	}
	if h.Branch != "develop" {
		t.Errorf("branch = %q, want develop", h.Branch)
	}
	// The blank-body comment is dropped; only the alice comment survives.
	if len(h.Comments) != 1 || h.Comments[0].Login != "alice" {
		t.Errorf("comments = %+v, want a single alice comment", h.Comments)
	}
}

// A top-level ruleKey (not nested) and a string-typed line must also parse.
func TestParseMatchableHotspotTopLevelRuleAndStringLine(t *testing.T) {
	raw := json.RawMessage(`{"key":"hs-2","ruleKey":"py:S4823","line":"7"}`)
	h := parseMatchableHotspot(raw)
	if h.RuleKey != "py:S4823" {
		t.Errorf("ruleKey = %q, want py:S4823", h.RuleKey)
	}
	if h.Line != 7 {
		t.Errorf("line = %d, want 7 (string coerced)", h.Line)
	}
}

func TestExtractHotspotLineAndOffsetEdgeCases(t *testing.T) {
	// Absent line/textRange → 0.
	if got := extractHotspotLine(json.RawMessage(`{}`)); got != 0 {
		t.Errorf("extractHotspotLine(missing) = %d, want 0", got)
	}
	if got := extractHotspotLine(json.RawMessage(`not json`)); got != 0 {
		t.Errorf("extractHotspotLine(bad json) = %d, want 0", got)
	}
	if got := extractHotspotStartOffset(json.RawMessage(`{}`)); got != 0 {
		t.Errorf("extractHotspotStartOffset(missing textRange) = %d, want 0", got)
	}
	if got := extractHotspotStartOffset(json.RawMessage(`{"textRange":{}}`)); got != 0 {
		t.Errorf("extractHotspotStartOffset(no startOffset) = %d, want 0", got)
	}
	if got := extractHotspotStartOffset(json.RawMessage(`{"textRange":{"startOffset":9}}`)); got != 9 {
		t.Errorf("extractHotspotStartOffset = %d, want 9", got)
	}
}

func TestExtractNestedField(t *testing.T) {
	if got := extractNestedField(json.RawMessage(`{"rule":{"key":"x"}}`), "rule", "key"); got != "x" {
		t.Errorf("nested = %q, want x", got)
	}
	if got := extractNestedField(json.RawMessage(`{}`), "rule", "key"); got != "" {
		t.Errorf("missing outer = %q, want empty", got)
	}
	if got := extractNestedField(json.RawMessage(`bad`), "rule", "key"); got != "" {
		t.Errorf("bad json = %q, want empty", got)
	}
}

func TestParseHotspotCommentsVariants(t *testing.T) {
	// Plural "comments" fallback.
	plural := json.RawMessage(`{"comments":[{"login":"a","markdown":"m"}]}`)
	if c := parseHotspotComments(plural); len(c) != 1 || c[0].Markdown != "m" {
		t.Errorf("plural comments = %+v", c)
	}
	// Neither key present.
	if c := parseHotspotComments(json.RawMessage(`{}`)); c != nil {
		t.Errorf("no comment key = %+v, want nil", c)
	}
	// Bad JSON.
	if c := parseHotspotComments(json.RawMessage(`bad`)); c != nil {
		t.Errorf("bad json = %+v, want nil", c)
	}
	// Comment array present but element bodies blank → dropped.
	blank := json.RawMessage(`{"comment":[{"login":"a"}]}`)
	if c := parseHotspotComments(blank); len(c) != 0 {
		t.Errorf("blank-body comment must be dropped, got %+v", c)
	}
}

// --- loadMatchableHotspots (was 0.0%) ---

// writeHotspotExtract writes an extract.json + getProjectHotspotsFull feed
// scoped to testServerURL so loadMatchableHotspots can read it.
func writeHotspotExtract(t *testing.T, dir string, rows []map[string]any) {
	t.Helper()
	extractDir := filepath.Join(dir, "extract-01")
	writeJSON(filepath.Join(extractDir, "extract.json"),
		map[string]any{"url": testServerURL, "edition": "enterprise"})
	writeJSONL(filepath.Join(extractDir, "getProjectHotspotsFull"), rows)
}

func TestLoadMatchableHotspotsFiltersByServerAndProject(t *testing.T) {
	dir := t.TempDir()
	writeHotspotExtract(t, dir, []map[string]any{
		// Wanted: right server, right project.
		{"key": "hs-1", "ruleKey": "java:S1", "component": "proj1:A.java",
			"project": "proj1", "line": 10, "status": "REVIEWED", "resolution": "SAFE",
			"serverUrl": testServerURL},
		// Wrong project — filtered out.
		{"key": "hs-2", "ruleKey": "java:S1", "component": "other:B.java",
			"project": "other", "line": 11, "serverUrl": testServerURL},
		// Uses projectKey (not project) as the key field.
		{"key": "hs-4", "ruleKey": "java:S1", "component": "proj1:D.java",
			"projectKey": "proj1", "line": 13, "serverUrl": testServerURL},
		// No key → skipped by parse guard.
		{"ruleKey": "java:S1", "component": "proj1:E.java",
			"project": "proj1", "line": 14, "serverUrl": testServerURL},
	})
	e := newProjectDataExecutor(t, dir)

	got, err := loadMatchableHotspots(e, testServerURL, "proj1")
	if err != nil {
		t.Fatalf("loadMatchableHotspots: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 hotspots (hs-1, hs-4), got %d: %+v", len(got), got)
	}
	keys := map[string]bool{}
	for _, h := range got {
		keys[h.Key] = true
	}
	if !keys["hs-1"] || !keys["hs-4"] {
		t.Errorf("expected hs-1 and hs-4, got %v", keys)
	}
}

// --- End-to-end syncProjectHotspots (was 0.0%) ---

// newHotspotSyncCloud stands up a cloud mock that reports one indexed issue and
// returns a single matching cloud counterpart on line 7 for the targeted
// search, recording every write endpoint the sync drives.
func newHotspotSyncCloud(t *testing.T) (*httptest.Server, *hotspotSyncRec) {
	t.Helper()
	rec := &hotspotSyncRec{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		// The project-wide indexing probe (no rules) reports a non-zero total.
		if r.URL.Query().Get("rules") == "" {
			json.NewEncoder(w).Encode(map[string]any{
				"issues": []map[string]any{},
				"paging": map[string]any{"pageIndex": 1, "pageSize": 1, "total": 1},
			})
			return
		}
		// Targeted per-hotspot search: one counterpart on line 7.
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{{
				"key": "cloud-hs-1", "rule": "java:S2092",
				"component": "cloud-proj:src/Main.java", "line": 7,
				"tags":        []string{"secret"},
				"transitions": []string{"accept", "confirm"},
			}},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
		})
	})
	mux.HandleFunc("POST /api/issues/do_transition", func(w http.ResponseWriter, _ *http.Request) {
		rec.transition++
	})
	mux.HandleFunc("POST /api/issues/add_comment", func(w http.ResponseWriter, _ *http.Request) {
		rec.comment++
	})
	mux.HandleFunc("POST /api/issues/set_tags", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		rec.setTags = append(rec.setTags, r.Form.Get("tags"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, rec
}

type hotspotSyncRec struct {
	transition int
	comment    int
	setTags    []string
}

func TestSyncProjectHotspotsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	writeHotspotExtract(t, dir, []map[string]any{
		{
			"key": "hs-1", "ruleKey": "java:S2092", "component": "proj1:src/Main.java",
			"project": "proj1", "line": 7, "branch": "main",
			"status": "REVIEWED", "resolution": "SAFE",
			"comment":   []map[string]any{{"login": "alice", "markdown": "safe here"}},
			"serverUrl": testServerURL,
		},
	})
	cloudSrv, rec := newHotspotSyncCloud(t)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	res := syncProjectHotspots(context.Background(), e, syncHotspotInput{
		CloudKey:  "cloud-proj",
		OrgKey:    "cloud-org",
		ServerURL: testServerURL,
		ServerKey: "proj1",
	})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Stats.Actionable != 1 {
		t.Errorf("actionable = %d, want 1", res.Stats.Actionable)
	}
	if res.Stats.A != 1 {
		t.Errorf("synced (A) = %d, want 1", res.Stats.A)
	}
	// The sqs-hotspot tag must be written to the matched cloud issue.
	if len(rec.setTags) != 1 {
		t.Fatalf("expected 1 set_tags call, got %d: %v", len(rec.setTags), rec.setTags)
	}
	if got := rec.setTags[0]; !containsTag(got, scanreport.HotspotIssueTag) {
		t.Errorf("set_tags payload %q missing %q", got, scanreport.HotspotIssueTag)
	}
}

// No source hotspots at all → zeroed stats, no error, no cloud writes.
func TestSyncProjectHotspotsNoSource(t *testing.T) {
	dir := t.TempDir()
	writeHotspotExtract(t, dir, nil)
	cloudSrv, rec := newHotspotSyncCloud(t)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	res := syncProjectHotspots(context.Background(), e, syncHotspotInput{
		CloudKey: "cloud-proj", OrgKey: "cloud-org",
		ServerURL: testServerURL, ServerKey: "proj1",
	})
	if res.Error != "" || res.Stats.Actionable != 0 || res.Stats.A != 0 {
		t.Errorf("want zeroed stats/no error, got %+v", res)
	}
	if len(rec.setTags) != 0 {
		t.Errorf("no cloud writes expected, got %v", rec.setTags)
	}
}

// --- runSyncHotspotMetadata task entry (was 0.0%) ---

func TestRunSyncHotspotMetadataTask(t *testing.T) {
	dir := t.TempDir()
	writeHotspotExtract(t, dir, []map[string]any{
		{
			"key": "hs-1", "ruleKey": "java:S2092", "component": "proj1:src/Main.java",
			"project": "proj1", "line": 7, "branch": "main",
			"status": "REVIEWED", "resolution": "SAFE",
			"serverUrl": testServerURL,
		},
	})
	cloudSrv, _ := newHotspotSyncCloud(t)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	// createProjects is the dependency the task fans out over.
	writeTaskJSONL(t, e, "createProjects", []map[string]any{
		{"key": "proj1", "cloud_project_key": "cloud-proj",
			"sonarcloud_org_key": "cloud-org", "server_url": testServerURL},
		// A row missing the cloud key/org is skipped early.
		{"key": "proj2", "server_url": testServerURL},
	})

	if err := runSyncHotspotMetadata(context.Background(), e); err != nil {
		t.Fatalf("runSyncHotspotMetadata: %v", err)
	}

	items, _ := e.Store.ReadAll("syncHotspotMetadata")
	if len(items) != 1 {
		t.Fatalf("expected 1 result record (proj2 skipped), got %d: %s", len(items), items)
	}
	if got := extractField(items[0], "cloud_project_key"); got != "cloud-proj" {
		t.Errorf("cloud_project_key = %q, want cloud-proj", got)
	}
}

// containsTag reports whether a comma-joined set_tags payload contains tag.
func containsTag(payload, tag string) bool {
	for _, p := range splitCSV(payload) {
		if p == tag {
			return true
		}
	}
	return false
}

func splitCSV(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, cur)
	return out
}
