// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

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

func TestEnrichRawOverwrite(t *testing.T) {
	raw := json.RawMessage(`{"key":"old"}`)
	enriched := EnrichRaw(raw, map[string]any{"key": "new"})

	var obj map[string]any
	_ = json.Unmarshal(enriched, &obj)
	if obj["key"] != "new" {
		t.Errorf("expected overwritten key=new, got %v", obj["key"])
	}
}

func TestExtractField(t *testing.T) {
	raw := json.RawMessage(`{"key":"myProject","name":"My Project"}`)
	if got := extractField(raw, "key"); got != "myProject" {
		t.Errorf("expected myProject, got %q", got)
	}
	if got := extractField(raw, "missing"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestExtractBool(t *testing.T) {
	raw := json.RawMessage(`{"isBuiltIn":true,"active":false}`)
	if !extractBool(raw, "isBuiltIn") {
		t.Error("expected true")
	}
	if extractBool(raw, "active") {
		t.Error("expected false")
	}
	if extractBool(raw, "missing") {
		t.Error("expected false for missing key")
	}
}

func TestExpandCombinations(t *testing.T) {
	expansions := []Expansion{
		{Key: "type", Values: []string{"A", "B"}},
		{Key: "sev", Values: []string{"1", "2", "3"}},
	}
	combos := expandCombinations(expansions)
	if len(combos) != 6 {
		t.Fatalf("expected 6 combos, got %d", len(combos))
	}
	// Verify all combos have both keys.
	for _, c := range combos {
		if _, ok := c["type"]; !ok {
			t.Error("missing 'type' key")
		}
		if _, ok := c["sev"]; !ok {
			t.Error("missing 'sev' key")
		}
	}
}

func TestExpandCombinationsEmpty(t *testing.T) {
	combos := expandCombinations(nil)
	if len(combos) != 1 {
		t.Fatalf("expected 1 empty combo, got %d", len(combos))
	}
}

func TestEnrichAll(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"a":1}`),
		json.RawMessage(`{"a":2}`),
	}
	enriched := enrichAll(items, map[string]any{"url": "x"})
	if len(enriched) != 2 {
		t.Fatalf("expected 2, got %d", len(enriched))
	}
	var obj map[string]any
	_ = json.Unmarshal(enriched[0], &obj)
	if obj["url"] != "x" {
		t.Errorf("expected enriched url, got %v", obj["url"])
	}
}

func newTestExecutor(t *testing.T) *Executor {
	t.Helper()
	dir := t.TempDir()
	store := NewDataStore(dir)
	logger := slog.New(slog.NewTextHandler(&discardWriter{}, nil))
	return &Executor{
		Store:  store,
		Sem:    make(chan struct{}, 2),
		Logger: logger,
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestHandleNonFatalErr_HTTP403(t *testing.T) {
	e := newTestExecutor(t)
	item := json.RawMessage(`{"key":"proj1"}`)
	err := &common.HTTPError{StatusCode: 403, Method: "GET", URL: "/api/test"}

	got := handleNonFatalErr(e, "testTask", item, err)
	if got != nil {
		t.Errorf("expected nil for 403 error, got %v", got)
	}
	if !e.IsSkipped("proj1") {
		t.Error("expected proj1 to be recorded as skipped")
	}
}

func TestHandleNonFatalErr_HTTP404(t *testing.T) {
	e := newTestExecutor(t)
	item := json.RawMessage(`{"key":"proj2"}`)
	err := &common.HTTPError{StatusCode: 404, Method: "GET", URL: "/api/test"}

	got := handleNonFatalErr(e, "testTask", item, err)
	if got != nil {
		t.Errorf("expected nil for 404 error, got %v", got)
	}
	if !e.IsSkipped("proj2") {
		t.Error("expected proj2 to be recorded as skipped")
	}
}

func TestHandleNonFatalErr_HTTP500(t *testing.T) {
	e := newTestExecutor(t)
	item := json.RawMessage(`{"key":"proj3"}`)
	err := &common.HTTPError{StatusCode: 500, Method: "GET", URL: "/api/test"}

	got := handleNonFatalErr(e, "testTask", item, err)
	if got == nil {
		t.Error("expected 500 error to be returned")
	}
}

func TestHandleNonFatalErr_NonHTTPError(t *testing.T) {
	e := newTestExecutor(t)
	item := json.RawMessage(`{"key":"proj4"}`)
	err := fmt.Errorf("network timeout")

	got := handleNonFatalErr(e, "testTask", item, err)
	if got == nil {
		t.Error("expected non-HTTP error to be returned")
	}
}

func TestHandleNonFatalErr_EmptyKey(t *testing.T) {
	e := newTestExecutor(t)
	item := json.RawMessage(`{"name":"no-key"}`)
	err := &common.HTTPError{StatusCode: 403, Method: "GET", URL: "/api/test"}

	got := handleNonFatalErr(e, "testTask", item, err)
	if got != nil {
		t.Errorf("expected nil for 403, got %v", got)
	}
}

func TestRunWithSem_Success(t *testing.T) {
	e := newTestExecutor(t)
	w, _ := e.Store.Writer("testRunWithSem")
	item := json.RawMessage(`{"key":"proj1"}`)

	err := runWithSem(context.Background(), e, "testTask", "", item, w,
		func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
			return w.WriteOne(item)
		})
	if err != nil {
		t.Fatal(err)
	}

	items, err := e.Store.ReadAll("testRunWithSem")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("expected 1 item, got %d", len(items))
	}
}

func TestRunWithSem_NonFatalError(t *testing.T) {
	e := newTestExecutor(t)
	w, _ := e.Store.Writer("testRunWithSemSkip")
	item := json.RawMessage(`{"key":"proj-skip"}`)

	err := runWithSem(context.Background(), e, "testTask", "", item, w,
		func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
			return &common.HTTPError{StatusCode: 403, Method: "GET", URL: "/api/test"}
		})
	if err != nil {
		t.Errorf("expected nil for non-fatal error, got %v", err)
	}
	if !e.IsSkipped("proj-skip") {
		t.Error("expected proj-skip to be recorded as skipped")
	}
}

func TestRunWithSem_CancelledContext(t *testing.T) {
	e := newTestExecutor(t)
	// Fill the semaphore so acquireSem must block.
	e.Sem <- struct{}{}
	e.Sem <- struct{}{}

	w, _ := e.Store.Writer("testRunWithSemCancel")
	item := json.RawMessage(`{"key":"proj1"}`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runWithSem(ctx, e, "testTask", "", item, w,
		func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
			return nil
		})
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}
