// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEditionParsing(t *testing.T) {
	tests := []struct {
		input    string
		expected Edition
	}{
		{`{"edition":"enterprise"}`, EditionEnterprise},
		{`{"edition":"community"}`, EditionCommunity},
		{`{"edition":"developer"}`, EditionDeveloper},
		{`{"edition":"datacenter"}`, EditionDatacenter},
		// #395: a truly unrecognised non-empty value falls back to
		// Community (with a Warn line that this test doesn't assert).
		{`{"edition":"unknown"}`, EditionCommunity},
		{`{}`, EditionCommunity},
		{`invalid`, EditionCommunity},
		// Nested System.Edition format (newer SonarQube API).
		{`{"System":{"Edition":"Enterprise"}}`, EditionEnterprise},
		{`{"System":{"Edition":"Developer"}}`, EditionDeveloper},
		{`{"System":{"Edition":"DataCenter"}}`, EditionDatacenter},
		{`{"System":{"Edition":"Community"}}`, EditionCommunity},
		// Top-level takes precedence over nested.
		{`{"edition":"developer","System":{"Edition":"Enterprise"}}`, EditionDeveloper},
		// #395: api/system/info reports the edition in display form
		// — i.e. with a space and mixed case. parseEditionString
		// normalises (lowercase + strip whitespace) and then matches
		// by prefix so display-form values resolve correctly.
		{`{"edition":"Data Center"}`, EditionDatacenter},
		{`{"edition":"data center"}`, EditionDatacenter},
		{`{"edition":"DATA CENTER"}`, EditionDatacenter},
		{`{"edition":"  Data  Center  "}`, EditionDatacenter},
		{`{"edition":"Data Center Edition"}`, EditionDatacenter},
		{`{"edition":"Enterprise Edition"}`, EditionEnterprise},
		{`{"edition":"Developer Edition"}`, EditionDeveloper},
		{`{"edition":"Community Edition"}`, EditionCommunity},
		{`{"System":{"Edition":"Data Center"}}`, EditionDatacenter},
		{`{"System":{"Edition":"Data Center Edition"}}`, EditionDatacenter},
	}
	for _, tt := range tests {
		got := ParseEdition([]byte(tt.input))
		if got != tt.expected {
			t.Errorf("ParseEdition(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestEnrichRaw(t *testing.T) {
	raw := json.RawMessage(`{"key":"proj1"}`)
	enriched := EnrichRaw(raw, map[string]any{"serverUrl": "https://sq.example.com/"})

	var obj map[string]any
	if err := json.Unmarshal(enriched, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["key"] != "proj1" {
		t.Errorf("expected key=proj1, got %v", obj["key"])
	}
	if obj["serverUrl"] != "https://sq.example.com/" {
		t.Errorf("expected serverUrl, got %v", obj["serverUrl"])
	}
}

func TestExtractField(t *testing.T) {
	raw := json.RawMessage(`{"key":"myProject","name":"My Project"}`)
	if got := ExtractField(raw, "key"); got != "myProject" {
		t.Errorf("expected myProject, got %q", got)
	}
	if got := ExtractField(raw, "missing"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestExtractBool(t *testing.T) {
	raw := json.RawMessage(`{"isBuiltIn":true,"active":false}`)
	if !ExtractBool(raw, "isBuiltIn") {
		t.Error("expected true")
	}
	if ExtractBool(raw, "active") {
		t.Error("expected false")
	}
	if ExtractBool(raw, "missing") {
		t.Error("expected false for missing key")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("", "second", "third"); got != "second" {
		t.Errorf("expected the first non-empty value, got %q", got)
	}
	if got := FirstNonEmpty("first", "second"); got != "first" {
		t.Errorf("expected the earlier value to win over a later non-empty one, got %q", got)
	}
	if got := FirstNonEmpty("", ""); got != "" {
		t.Errorf("expected empty string when every value is empty, got %q", got)
	}
	if got := FirstNonEmpty(); got != "" {
		t.Errorf("expected empty string for no arguments, got %q", got)
	}
}

func TestExpandCombinations(t *testing.T) {
	expansions := []Expansion{
		{Key: "type", Values: []string{"A", "B"}},
		{Key: "sev", Values: []string{"1", "2", "3"}},
	}
	combos := ExpandCombinations(expansions)
	if len(combos) != 6 {
		t.Fatalf("expected 6, got %d", len(combos))
	}
}

func TestExpandCombinationsEmpty(t *testing.T) {
	combos := ExpandCombinations(nil)
	if len(combos) != 1 {
		t.Fatalf("expected 1 empty combo, got %d", len(combos))
	}
}

func TestDataStoreWriteAndReadAll(t *testing.T) {
	dir := t.TempDir()
	ds := NewDataStore(dir)

	w, err := ds.Writer("testTask")
	if err != nil {
		t.Fatal(err)
	}
	items := []json.RawMessage{
		json.RawMessage(`{"a":1}`),
		json.RawMessage(`{"a":2}`),
	}
	if err := w.WriteChunk(items); err != nil {
		t.Fatal(err)
	}

	got, err := ds.ReadAll("testTask")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
}

// TestDataStoreSanitizesTaskDir verifies that task names containing
// characters illegal in Windows path components (e.g. the ":" in
// "getTemplateGroupsScanners:apply") are sanitized to a safe directory
// name, and that Writer / ReadAll / TaskDirExists all agree on that
// name so the round-trip works. Regression test for issue #486.
func TestDataStoreSanitizesTaskDir(t *testing.T) {
	dir := t.TempDir()
	ds := NewDataStore(dir)

	const taskName = "getTemplateGroupsScanners:apply"

	w, err := ds.Writer(taskName)
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	if err := w.WriteOne(json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("WriteOne: %v", err)
	}

	// The on-disk directory must not contain the illegal ":".
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 task dir, got %d", len(entries))
	}
	if got := entries[0].Name(); strings.ContainsAny(got, `\/:*?"<>|`) {
		t.Errorf("task dir %q contains an illegal path character", got)
	}

	// Writer, ReadAll and TaskDirExists must resolve to the same dir.
	if !ds.TaskDirExists(taskName) {
		t.Error("TaskDirExists returned false for sanitized task")
	}
	got, err := ds.ReadAll(taskName)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record round-tripped, got %d", len(got))
	}
}

func TestDataStoreCompletion(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	if ds.IsComplete("task1") {
		t.Error("expected incomplete")
	}
	ds.MarkComplete("task1")
	if !ds.IsComplete("task1") {
		t.Error("expected complete")
	}
}

func TestDataStoreTaskDirExists(t *testing.T) {
	dir := t.TempDir()
	ds := NewDataStore(dir)
	if ds.TaskDirExists("nodir") {
		t.Error("expected false")
	}
	_ = os.MkdirAll(filepath.Join(dir, "existingTask"), 0o755)
	if !ds.TaskDirExists("existingTask") {
		t.Error("expected true")
	}
}

func TestChunkWriterConcurrent(t *testing.T) {
	dir := t.TempDir()
	taskDir := filepath.Join(dir, "testTask")
	w, err := NewChunkWriter(taskDir)
	if err != nil {
		t.Fatal(err)
	}
	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			_ = w.WriteOne(json.RawMessage(`{"concurrent":true}`))
		}()
	}
	wg.Wait()
	entries, _ := os.ReadDir(taskDir)
	if len(entries) != goroutines {
		t.Errorf("expected %d files, got %d", goroutines, len(entries))
	}
}

// #604 regression: a ChunkWriter opened on a directory an earlier run
// already wrote to must APPEND past the highest existing chunk, not
// restart at results.1 and truncate it.
//
// This is the gap that let #604 in. A --run_id resume re-runs
// importProjectData against the first attempt's directory; because the
// index restarted at 0, a resume that wrote FEWER rows than the first
// attempt overwrote the rows it re-wrote and left the first attempt's
// tail in place. The run directory then held a mix of both attempts with
// the stale rows outnumbering the fresh ones, and the migration report
// bucketed a fully-migrated project as Skipped or Failed off the stale
// tail.
func TestChunkWriterAppendsToExistingDir(t *testing.T) {
	taskDir := filepath.Join(t.TempDir(), "importProjectData")

	first, err := NewChunkWriter(taskDir)
	if err != nil {
		t.Fatalf("first NewChunkWriter: %v", err)
	}
	for _, branch := range []string{"main", "develop", "release"} {
		if err := first.WriteOne(json.RawMessage(`{"run":1,"branch":"` + branch + `"}`)); err != nil {
			t.Fatalf("first run WriteOne: %v", err)
		}
	}

	// The resume writes fewer rows than the first attempt — the exact
	// shape that used to corrupt the directory.
	second, err := NewChunkWriter(taskDir)
	if err != nil {
		t.Fatalf("second NewChunkWriter: %v", err)
	}
	if err := second.WriteOne(json.RawMessage(`{"run":2,"branch":"develop"}`)); err != nil {
		t.Fatalf("second run WriteOne: %v", err)
	}

	for _, name := range []string{"results.1.jsonl", "results.2.jsonl", "results.3.jsonl", "results.4.jsonl"} {
		if _, err := os.Stat(filepath.Join(taskDir, name)); err != nil {
			t.Errorf("expected %s to exist after the resume: %v", name, err)
		}
	}

	// Nothing the first attempt wrote may have been destroyed.
	got, err := os.ReadFile(filepath.Join(taskDir, "results.1.jsonl"))
	if err != nil {
		t.Fatalf("reading results.1.jsonl: %v", err)
	}
	if !strings.Contains(string(got), `"run":1`) {
		t.Errorf("results.1.jsonl was overwritten by the resume: %s", got)
	}

	ds := NewDataStore(filepath.Dir(taskDir))
	items, err := ds.ReadAll("importProjectData")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 records (3 from run 1, 1 from run 2), got %d: %v", len(items), items)
	}
	// The resume's row must be LAST, so a reader resolving duplicates by
	// last-one-wins sees the retry rather than the stale first attempt.
	if !strings.Contains(string(items[3]), `"run":2`) {
		t.Errorf("last record should be the resume's row, got %s", items[3])
	}
}

// Only results.N.jsonl participates in the index. A neighbouring file
// must neither raise the starting index nor stop the writer.
func TestChunkWriterIgnoresNonChunkFiles(t *testing.T) {
	taskDir := filepath.Join(t.TempDir(), "task")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"results.jsonl", "results.0.jsonl", "results.abc.jsonl", "notes.txt", "results.7.json"} {
		if err := os.WriteFile(filepath.Join(taskDir, name), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w, err := NewChunkWriter(taskDir)
	if err != nil {
		t.Fatalf("NewChunkWriter: %v", err)
	}
	if err := w.WriteOne(json.RawMessage(`{"fresh":true}`)); err != nil {
		t.Fatalf("WriteOne: %v", err)
	}
	if _, err := os.Stat(filepath.Join(taskDir, "results.1.jsonl")); err != nil {
		t.Errorf("expected the first write to land on results.1.jsonl: %v", err)
	}
}

// #604: chunk files must be read in numeric index order. os.ReadDir
// returns them lexicographically, where results.10.jsonl sorts before
// results.2.jsonl — which would make "the last row wins" resolve to the
// wrong attempt as soon as a task writes ten chunks.
func TestDataStoreReadsChunksInNumericOrder(t *testing.T) {
	dir := t.TempDir()
	ds := NewDataStore(dir)
	w, err := ds.Writer("orderedTask")
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	const chunks = 12
	for i := 1; i <= chunks; i++ {
		if err := w.WriteOne(json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatalf("WriteOne %d: %v", i, err)
		}
	}

	items, err := ds.ReadAll("orderedTask")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(items) != chunks {
		t.Fatalf("expected %d records, got %d", chunks, len(items))
	}
	for i, item := range items {
		want := fmt.Sprintf(`{"n":%d}`, i+1)
		if string(item) != want {
			t.Errorf("record %d = %s, want %s (chunk files read out of order)", i, item, want)
		}
	}
}

// testTaskDef implements TaskMeta for testing.
type testTaskDef struct {
	name string
	eds  []Edition
	deps []string
}

func (t *testTaskDef) TaskName() string        { return t.name }
func (t *testTaskDef) TaskEditions() []Edition { return t.eds }
func (t *testTaskDef) TaskDeps() []string      { return t.deps }

func TestPlanPhasesGeneric(t *testing.T) {
	reg := map[string]*testTaskDef{
		"a": {name: "a"},
		"b": {name: "b", deps: []string{"a"}},
		"c": {name: "c", deps: []string{"a"}},
		"d": {name: "d", deps: []string{"b", "c"}},
	}
	tasks := map[string]bool{"a": true, "b": true, "c": true, "d": true}

	plan, err := PlanPhasesGeneric(tasks, reg)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 3 {
		t.Fatalf("expected 3 phases, got %d", len(plan))
	}
	if plan[0][0] != "a" {
		t.Errorf("expected phase 0 = [a], got %v", plan[0])
	}
	if len(plan[1]) != 2 {
		t.Errorf("expected phase 1 has 2 tasks, got %v", plan[1])
	}
}

func TestPlanPhasesGenericCycle(t *testing.T) {
	reg := map[string]*testTaskDef{
		"a": {name: "a", deps: []string{"b"}},
		"b": {name: "b", deps: []string{"a"}},
	}
	tasks := map[string]bool{"a": true, "b": true}
	_, err := PlanPhasesGeneric(tasks, reg)
	if err == nil {
		t.Error("expected cycle error")
	}
}

func TestFilterByEditionGeneric(t *testing.T) {
	reg := map[string]*testTaskDef{
		"all":     {name: "all", eds: AllEditions},
		"entOnly": {name: "entOnly", eds: []Edition{EditionEnterprise}},
		"noEds":   {name: "noEds"},
	}
	filtered := FilterByEditionGeneric(reg, EditionCommunity)
	if _, ok := filtered["all"]; !ok {
		t.Error("expected 'all' in community filter")
	}
	if _, ok := filtered["entOnly"]; ok {
		t.Error("expected 'entOnly' excluded")
	}
	if _, ok := filtered["noEds"]; !ok {
		t.Error("expected 'noEds' included (empty = all)")
	}
}

func TestResolveDependenciesGeneric(t *testing.T) {
	reg := map[string]*testTaskDef{
		"a": {name: "a"},
		"b": {name: "b", deps: []string{"a"}},
		"c": {name: "c", deps: []string{"b"}},
	}
	result := ResolveDependenciesGeneric([]string{"c"}, reg)
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}
}

func TestResolveDependenciesGenericMissing(t *testing.T) {
	reg := map[string]*testTaskDef{
		"a": {name: "a", deps: []string{"missing"}},
	}
	result := ResolveDependenciesGeneric([]string{"a"}, reg)
	if result != nil {
		t.Error("expected nil for unresolvable")
	}
}

// #536: a target's dependency on an excluded task must not pull that
// task (or its own dependencies) into the result — reproduces
// setGlobalSettings depending on createProjects, which must not force
// project creation when --objects excludes "projects".
func TestResolveDependenciesExcludingGeneric(t *testing.T) {
	reg := map[string]*testTaskDef{
		"generateOrgMappings": {name: "generateOrgMappings"},
		"createProjects":      {name: "createProjects", deps: []string{"generateOrgMappings"}},
		"setGlobalSettings":   {name: "setGlobalSettings", deps: []string{"generateOrgMappings", "createProjects"}},
	}
	excluded := map[string]bool{"createProjects": true}

	result := ResolveDependenciesExcludingGeneric([]string{"setGlobalSettings"}, reg, excluded)
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if result["createProjects"] {
		t.Error("excluded task createProjects must not be in the result")
	}
	if !result["setGlobalSettings"] || !result["generateOrgMappings"] {
		t.Errorf("expected setGlobalSettings and its non-excluded dependency, got %v", result)
	}
}

// An excluded task passed directly as a target is also dropped, not
// just when reached transitively.
func TestResolveDependenciesExcludingGeneric_ExcludedTarget(t *testing.T) {
	reg := map[string]*testTaskDef{
		"a": {name: "a"},
		"b": {name: "b"},
	}
	result := ResolveDependenciesExcludingGeneric([]string{"a", "b"}, reg, map[string]bool{"a": true})
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if result["a"] {
		t.Error("excluded target must not be in the result")
	}
	if !result["b"] {
		t.Error("expected non-excluded target b in the result")
	}
}

// A dependency that's excluded doesn't need to exist in the registry —
// exclusion is checked before the registry lookup that would otherwise
// report it missing.
func TestResolveDependenciesExcludingGeneric_ExcludedDepNotInRegistry(t *testing.T) {
	reg := map[string]*testTaskDef{
		"a": {name: "a", deps: []string{"neverRegistered"}},
	}
	result := ResolveDependenciesExcludingGeneric([]string{"a"}, reg, map[string]bool{"neverRegistered": true})
	if result == nil {
		t.Fatal("expected non-nil — excluded dep should not trigger the missing-dependency error path")
	}
	if !result["a"] {
		t.Error("expected a in the result")
	}
}

// Non-excluded dependencies still resolve normally alongside excluded
// ones — exclusion only vacuously satisfies the excluded names, it
// doesn't disable resolution of the rest of the graph.
func TestResolveDependenciesExcludingGeneric_MixedGraph(t *testing.T) {
	reg := map[string]*testTaskDef{
		"a": {name: "a"},
		"b": {name: "b", deps: []string{"a"}},
		"c": {name: "c", deps: []string{"a", "excludedDep"}},
	}
	result := ResolveDependenciesExcludingGeneric([]string{"b", "c"}, reg, map[string]bool{"excludedDep": true})
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if !result["a"] || !result["b"] || !result["c"] {
		t.Errorf("expected a, b, c in the result, got %v", result)
	}
	if result["excludedDep"] {
		t.Error("excludedDep must not be in the result")
	}
}

func TestExtractArray(t *testing.T) {
	body := []byte(`{"items":[{"id":"1"},{"id":"2"}]}`)
	items, err := ExtractArray(body, "items")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("expected 2, got %d", len(items))
	}
}

func TestExtractTotal(t *testing.T) {
	body := []byte(`{"paging":{"total":42}}`)
	total := ExtractTotal(body, "paging.total")
	if total != 42 {
		t.Errorf("expected 42, got %d", total)
	}
}

func TestTotalPages(t *testing.T) {
	tests := []struct{ total, pageSize, expected int }{
		{0, 500, 0}, {1, 500, 1}, {500, 500, 1}, {501, 500, 2},
	}
	for _, tt := range tests {
		got := TotalPages(tt.total, tt.pageSize)
		if got != tt.expected {
			t.Errorf("TotalPages(%d, %d) = %d, want %d", tt.total, tt.pageSize, got, tt.expected)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate([]byte("hi"), 10); got != "hi" {
		t.Errorf("expected 'hi', got %q", got)
	}
	if got := Truncate([]byte("hello world"), 5); got != "hello..." {
		t.Errorf("expected 'hello...', got %q", got)
	}
}

func TestHTTPErrorMessageDecoding(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"sonarqube json with one msg",
			`{"errors":[{"msg":"This organization is not bound to an ALM application"}]}`,
			"This organization is not bound to an ALM application"},
		{"sonarqube json with multiple msgs",
			`{"errors":[{"msg":"one"},{"msg":"two"}]}`,
			"one; two"},
		{"non-json body falls back to raw",
			`Internal server error`,
			"Internal server error"},
		{"empty errors array falls back to raw",
			`{"errors":[]}`,
			`{"errors":[]}`},
		{"empty body",
			``,
			""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &HTTPError{StatusCode: 400, Method: "GET", URL: "https://x/api/y", Body: tc.body}
			if got := err.Message(); got != tc.want {
				t.Errorf("Message: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHTTPErrorErrorFormat(t *testing.T) {
	err := &HTTPError{
		StatusCode: 400,
		Method:     "GET",
		URL:        "https://sc-staging.io/api/alm_integration/list_repositories?organization=migration-tool-test-gh",
		Body:       `{"errors":[{"msg":"This organization is not bound to an ALM application"}]}`,
	}
	want := "HTTP 400 GET https://sc-staging.io/api/alm_integration/list_repositories?organization=migration-tool-test-gh - This organization is not bound to an ALM application"
	if got := err.Error(); got != want {
		t.Errorf("Error: got %q, want %q", got, want)
	}
}
