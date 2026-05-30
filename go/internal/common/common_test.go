package common

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// testTaskDef implements TaskMeta for testing.
type testTaskDef struct {
	name string
	eds  []Edition
	deps []string
}

func (t *testTaskDef) TaskName() string      { return t.name }
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
