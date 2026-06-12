// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

func TestLoadCSVToJSONL(t *testing.T) {
	dir := t.TempDir()

	// Create a test CSV.
	csvContent := "name,value\nfoo,bar\nbaz,qux\n"
	if err := os.WriteFile(filepath.Join(dir, "test.csv"), []byte(csvContent), 0o644); err != nil {
		t.Fatal(err)
	}

	runDir := filepath.Join(dir, "run-01")
	os.MkdirAll(runDir, 0o755)
	store := common.NewDataStore(runDir)

	e := &Executor{
		Store:     store,
		ExportDir: dir,
	}

	if err := loadCSVToJSONL(e, "testTask", "test.csv"); err != nil {
		t.Fatalf("loadCSVToJSONL: %v", err)
	}

	items, err := store.ReadAll("testTask")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Verify first item.
	var row map[string]any
	json.Unmarshal(items[0], &row)
	if row["name"] != "foo" {
		t.Errorf("expected name=foo, got %v", row["name"])
	}
}

// #381: when Executor.ResetConfirmedOrgs is set, loadCSVToJSONL must
// rewrite the sonarcloud_org_key of every un-confirmed row to the
// SKIPPED sentinel so the existing shouldSkipOrg path naturally
// excludes those orgs from every per-org reset task. Rows whose
// cloud org IS in the confirmed set must pass through untouched.
// A nil map (the migrate-time default) leaves every row alone.
func TestLoadCSVToJSONLResetConfirmedOrgsFilter(t *testing.T) {
	cases := []struct {
		name      string
		confirmed map[string]bool
		wantOrgs  []string
	}{
		{
			name:      "nil map — no rewrite (migrate-time default)",
			confirmed: nil,
			wantOrgs:  []string{"cloud-a", "cloud-b", "cloud-c"},
		},
		{
			name:      "subset confirmed — others rewritten to SKIPPED",
			confirmed: map[string]bool{"cloud-a": true, "cloud-c": true},
			wantOrgs:  []string{"cloud-a", "SKIPPED", "cloud-c"},
		},
		{
			name:      "none confirmed — every row goes to SKIPPED",
			confirmed: map[string]bool{},
			wantOrgs:  []string{"SKIPPED", "SKIPPED", "SKIPPED"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			csvContent := "sonarqube_org_key,sonarcloud_org_key\norg1,cloud-a\norg2,cloud-b\norg3,cloud-c\n"
			if err := os.WriteFile(filepath.Join(dir, "test.csv"), []byte(csvContent), 0o644); err != nil {
				t.Fatal(err)
			}
			// loadCSVToJSONL also reads organizations.csv for the
			// enrichment join — write a self-referential one that
			// matches the test rows.
			if err := os.WriteFile(filepath.Join(dir, "organizations.csv"), []byte(csvContent), 0o644); err != nil {
				t.Fatal(err)
			}
			runDir := filepath.Join(dir, "run-01")
			os.MkdirAll(runDir, 0o755)
			store := common.NewDataStore(runDir)

			e := &Executor{
				Store:              store,
				ExportDir:          dir,
				ResetConfirmedOrgs: c.confirmed,
			}
			if err := loadCSVToJSONL(e, "testTask", "test.csv"); err != nil {
				t.Fatalf("loadCSVToJSONL: %v", err)
			}
			items, err := store.ReadAll("testTask")
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != len(c.wantOrgs) {
				t.Fatalf("expected %d items, got %d", len(c.wantOrgs), len(items))
			}
			for i, raw := range items {
				var row map[string]any
				_ = json.Unmarshal(raw, &row)
				got, _ := row["sonarcloud_org_key"].(string)
				if got != c.wantOrgs[i] {
					t.Errorf("row %d: sonarcloud_org_key = %q, want %q", i, got, c.wantOrgs[i])
				}
			}
		})
	}
}

func TestLoadCSVToJSONLMissingFile(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run-01")
	os.MkdirAll(runDir, 0o755)
	store := common.NewDataStore(runDir)

	e := &Executor{
		Store:     store,
		ExportDir: dir,
	}

	// Missing CSV should result in empty output, not error (LoadCSV returns nil for missing).
	err := loadCSVToJSONL(e, "testTask", "nonexistent.csv")
	if err != nil {
		t.Fatalf("expected no error for missing CSV, got %v", err)
	}
}

func TestForEachMigrateItem(t *testing.T) {
	dir := t.TempDir()
	store := common.NewDataStore(dir)

	// Write test dependency data.
	w, _ := store.Writer("dep")
	w.WriteChunk([]json.RawMessage{
		json.RawMessage(`{"key":"a"}`),
		json.RawMessage(`{"key":"b"}`),
	})

	e := &Executor{
		Store:  store,
		Sem:    make(chan struct{}, 5),
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	var count atomic.Int32
	err := forEachMigrateItem(context.Background(), e, "test", "dep",
		func(_ context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			count.Add(1)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if count.Load() != 2 {
		t.Errorf("expected 2 iterations, got %d", count.Load())
	}
}

// Issue #338: forEachMigrateItemSerial must process items one at a
// time. A barrier inside the per-item callback counts concurrent
// callers and would record >1 if the helper accidentally fell back
// to fan-out concurrency.
func TestForEachMigrateItemSerial(t *testing.T) {
	dir := t.TempDir()
	store := common.NewDataStore(dir)

	w, _ := store.Writer("dep")
	w.WriteChunk([]json.RawMessage{
		json.RawMessage(`{"key":"a"}`),
		json.RawMessage(`{"key":"b"}`),
		json.RawMessage(`{"key":"c"}`),
		json.RawMessage(`{"key":"d"}`),
		json.RawMessage(`{"key":"e"}`),
	})

	e := &Executor{
		Store:  store,
		Sem:    make(chan struct{}, 8),
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	var (
		inFlight    atomic.Int32
		maxInFlight atomic.Int32
		order       []string
		orderMu     sync.Mutex
	)
	err := forEachMigrateItemSerial(context.Background(), e, "test", "dep", nil,
		func(_ context.Context, item json.RawMessage, _ *common.ChunkWriter) error {
			n := inFlight.Add(1)
			for {
				cur := maxInFlight.Load()
				if n <= cur || maxInFlight.CompareAndSwap(cur, n) {
					break
				}
			}
			// Hold long enough that any accidental concurrency would
			// pile up in the gauge.
			time.Sleep(5 * time.Millisecond)
			orderMu.Lock()
			order = append(order, extractField(item, "key"))
			orderMu.Unlock()
			inFlight.Add(-1)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if maxInFlight.Load() != 1 {
		t.Errorf("expected max in-flight = 1 (serial), got %d", maxInFlight.Load())
	}
	wantOrder := []string{"a", "b", "c", "d", "e"}
	if len(order) != len(wantOrder) {
		t.Fatalf("expected %d items processed, got %d (%v)", len(wantOrder), len(order), order)
	}
	for i, k := range wantOrder {
		if order[i] != k {
			t.Errorf("order[%d]: want %s, got %s (full: %v)", i, k, order[i], order)
		}
	}
}

func TestForEachMigrateItemFiltered(t *testing.T) {
	dir := t.TempDir()
	store := common.NewDataStore(dir)

	w, _ := store.Writer("dep")
	w.WriteChunk([]json.RawMessage{
		json.RawMessage(`{"key":"a","skip":true}`),
		json.RawMessage(`{"key":"b","skip":false}`),
	})

	e := &Executor{
		Store:  store,
		Sem:    make(chan struct{}, 5),
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	var keys []string
	err := forEachMigrateItemFiltered(context.Background(), e, "test", "dep",
		func(item json.RawMessage) bool {
			return !extractBool(item, "skip")
		},
		func(_ context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			keys = append(keys, extractField(item, "key"))
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "b" {
		t.Errorf("expected [b], got %v", keys)
	}
}

func TestForEachExtractItem(t *testing.T) {
	dir := t.TempDir()

	// Set up extract data with rule details.
	setupExtractData(dir)

	store := common.NewDataStore(filepath.Join(dir, "run-test"))
	os.MkdirAll(filepath.Join(dir, "run-test"), 0o755)

	e := &Executor{
		Store:     store,
		ExportDir: dir,
		Mapping:   structure.ExtractMapping{testServerURL: "extract-01"},
		Sem:       make(chan struct{}, 5),
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	var count int
	err := forEachExtractItem(context.Background(), e, "testExtract", "getRuleDetails",
		func(_ context.Context, item structure.ExtractItem, w *common.ChunkWriter) error {
			count++
			key := extractField(item.Data, "key")
			if key == "" {
				t.Error("expected non-empty key")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 extract item, got %d", count)
	}
}

func TestBuildServerOrgLookup(t *testing.T) {
	dir := t.TempDir()
	store := common.NewDataStore(dir)

	// Write org mapping data.
	w, _ := store.Writer("generateOrganizationMappings")
	w.WriteChunk([]json.RawMessage{
		json.RawMessage(`{"server_url":"https://sq.test/","sonarcloud_org_key":"cloud-org1"}`),
	})

	e := &Executor{Store: store}
	lookup := buildServerOrgLookup(e)

	if lookup["https://sq.test/"] != "cloud-org1" {
		t.Errorf("expected cloud-org1, got %v", lookup["https://sq.test/"])
	}
	if lookup["unknown"] != "" {
		t.Errorf("expected empty for unknown, got %v", lookup["unknown"])
	}
}

func TestUnsupportedLanguages(t *testing.T) {
	if !unsupportedLanguages["c++"] {
		t.Error("expected c++ to be unsupported")
	}
	if unsupportedLanguages["java"] {
		t.Error("expected java to be supported")
	}
}

func TestValidPermissions(t *testing.T) {
	if !validPermissions["scan"] {
		t.Error("expected scan to be valid")
	}
	if validPermissions["delete"] {
		t.Error("expected delete to be invalid")
	}
}

func TestLogAPIWarnWithAPIError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	apiErr := &sqapi.APIError{
		StatusCode: 403,
		Method:     "POST",
		URL:        "https://sonarcloud.io/api/permissions/add_group",
		Body:       `{"errors":[{"msg":"Insufficient privileges"}]}`,
	}
	logAPIWarn(logger, "operation failed", apiErr, "project", "proj1")

	output := buf.String()
	if !strings.Contains(output, "Insufficient privileges") {
		t.Errorf("expected parsed error message, got: %s", output)
	}
	if !strings.Contains(output, "status=403") {
		t.Errorf("expected status=403, got: %s", output)
	}
	if !strings.Contains(output, "endpoint=/api/permissions/add_group") {
		t.Errorf("expected endpoint, got: %s", output)
	}
	if !strings.Contains(output, "project=proj1") {
		t.Errorf("expected project attr, got: %s", output)
	}
}

func TestLogAPIWarnWithPlainError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	logAPIWarn(logger, "something failed", errors.New("connection refused"), "task", "test")

	output := buf.String()
	if !strings.Contains(output, "connection refused") {
		t.Errorf("expected plain error, got: %s", output)
	}
	if strings.Contains(output, "status=") {
		t.Errorf("should not have status for plain error, got: %s", output)
	}
}

func TestTaskCounterFailAndSummary(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	c := NewTaskCounter("testTask")
	c.Success()
	c.Success()
	c.Fail()
	// #333: LogSummary now carries duration alongside the counts.
	c.LogSummary(logger, 54139*time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "succeeded=2") {
		t.Errorf("expected succeeded=2, got: %s", output)
	}
	if !strings.Contains(output, "failed=1") {
		t.Errorf("expected failed=1, got: %s", output)
	}
	if !strings.Contains(output, "total=3") {
		t.Errorf("expected total=3, got: %s", output)
	}
	if !strings.Contains(output, "duration=00:00:54.139") {
		t.Errorf("expected duration=00:00:54.139 attribute, got: %s", output)
	}
	// The combined log must be a single "task summary" line — not two
	// separate lines (the regression #333 patched).
	if strings.Count(output, "\n") != 1 {
		t.Errorf("expected exactly one log line, got: %s", output)
	}
}

// #333: an empty counter (no Success/Fail recorded) falls back to the
// plain "Task X: Duration ..." line so every task still emits exactly
// one closing log entry.
func TestTaskCounterEmptySummary(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	c := NewTaskCounter("empty")
	c.LogSummary(logger, 250*time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "Task empty: Duration 00:00:00.250") {
		t.Errorf("expected standalone duration line, got: %s", output)
	}
	if strings.Contains(output, "succeeded=") {
		t.Errorf("empty counter should not emit succeeded/failed attrs, got: %s", output)
	}
}
// #300: runProjectSyncLoop applies fn to every item concurrently and
// emits a "<label>: N/M - X%" progress line every `interval`
// completions, including a final 100% line at the end of the batch.
func TestRunProjectSyncLoop(t *testing.T) {
	t.Run("issue sync cadence at every 20", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		e := &Executor{Sem: make(chan struct{}, 4), Logger: logger}

		items := make([]int, 40)
		var applied atomic.Int64
		runProjectSyncLoop(context.Background(), e, items, "Issue sync:", 20,
			func(_ context.Context, _ int) { applied.Add(1) })

		if applied.Load() != 40 {
			t.Errorf("want apply called 40 times, got %d", applied.Load())
		}
		out := buf.String()
		if !strings.Contains(out, "Issue sync: 20/40 - 50%") {
			t.Errorf("missing mid-batch progress line, got:\n%s", out)
		}
		if !strings.Contains(out, "Issue sync: 40/40 - 100%") {
			t.Errorf("missing final 100%% line, got:\n%s", out)
		}
	})

	// Issue #348: the production caller (syncProjectIssues) builds
	// the label as "Project key <cloudKey> issue sync:" so the
	// operator can disentangle interleaved per-project lines when
	// several projects sync in parallel.
	t.Run("issue sync label carries project key (#348)", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		e := &Executor{Sem: make(chan struct{}, 4), Logger: logger}

		const cloudKey = "myorg_some_project_key"
		label := "Project key " + cloudKey + " issue sync:"
		items := make([]int, 20)
		runProjectSyncLoop(context.Background(), e, items, label, 20,
			func(_ context.Context, _ int) {})

		out := buf.String()
		want := "Project key myorg_some_project_key issue sync: 20/20 - 100%"
		if !strings.Contains(out, want) {
			t.Errorf("want log line %q, got:\n%s", want, out)
		}
	})

	t.Run("hotspot sync cadence at every 10", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		e := &Executor{Sem: make(chan struct{}, 4), Logger: logger}

		items := make([]int, 30)
		runProjectSyncLoop(context.Background(), e, items, "Hotspot sync:", 10,
			func(_ context.Context, _ int) {})

		out := buf.String()
		if !strings.Contains(out, "Hotspot sync: 10/30 - 33%") {
			t.Errorf("missing first cadence line, got:\n%s", out)
		}
		if !strings.Contains(out, "Hotspot sync: 30/30 - 100%") {
			t.Errorf("missing final 100%% line, got:\n%s", out)
		}
	})

	t.Run("cancelled context short-circuits remaining work", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		e := &Executor{Sem: make(chan struct{}, 1), Logger: logger}

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // pre-cancel so every goroutine sees gctx.Err() != nil

		items := make([]int, 5)
		var applied atomic.Int64
		runProjectSyncLoop(ctx, e, items, "Issue sync:", 20,
			func(_ context.Context, _ int) { applied.Add(1) })

		if applied.Load() != 0 {
			t.Errorf("cancelled ctx: want 0 apply calls, got %d", applied.Load())
		}
	})

	t.Run("empty input does not panic", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		e := &Executor{Sem: make(chan struct{}, 4), Logger: logger}
		runProjectSyncLoop(context.Background(), e, []int{}, "Issue sync:", 20,
			func(_ context.Context, _ int) { t.Fatal("apply should not be called") })
	})
}
// #326: sortMigrateItems orders items by (orgField, sortField) for tasks
// in the registry, and is a no-op for tasks not in the registry.
func TestSortMigrateItems(t *testing.T) {
	t.Run("org-bucketed alphabetical", func(t *testing.T) {
		items := []json.RawMessage{
			json.RawMessage(`{"sonarcloud_org_key":"org-b","cloud_project_key":"banana"}`),
			json.RawMessage(`{"sonarcloud_org_key":"org-a","cloud_project_key":"zebra"}`),
			json.RawMessage(`{"sonarcloud_org_key":"org-b","cloud_project_key":"apple"}`),
			json.RawMessage(`{"sonarcloud_org_key":"org-a","cloud_project_key":"alpha"}`),
		}
		sortMigrateItems("createProjects", items)

		gotOrgs := make([]string, len(items))
		gotKeys := make([]string, len(items))
		for i, it := range items {
			gotOrgs[i] = extractField(it, "sonarcloud_org_key")
			gotKeys[i] = extractField(it, "cloud_project_key")
		}
		wantOrgs := []string{"org-a", "org-a", "org-b", "org-b"}
		wantKeys := []string{"alpha", "zebra", "apple", "banana"}
		for i := range items {
			if gotOrgs[i] != wantOrgs[i] || gotKeys[i] != wantKeys[i] {
				t.Errorf("position %d: got (%s, %s), want (%s, %s)",
					i, gotOrgs[i], gotKeys[i], wantOrgs[i], wantKeys[i])
			}
		}
	})

	t.Run("enterprise-wide alphabetical (no org bucketing)", func(t *testing.T) {
		items := []json.RawMessage{
			json.RawMessage(`{"name":"Charlie"}`),
			json.RawMessage(`{"name":"Alice"}`),
			json.RawMessage(`{"name":"Bob"}`),
		}
		sortMigrateItems("configurePortfolios", items)
		want := []string{"Alice", "Bob", "Charlie"}
		for i, it := range items {
			if got := extractField(it, "name"); got != want[i] {
				t.Errorf("position %d: got %s, want %s", i, got, want[i])
			}
		}
	})

	t.Run("unregistered task is a no-op", func(t *testing.T) {
		items := []json.RawMessage{
			json.RawMessage(`{"name":"Charlie"}`),
			json.RawMessage(`{"name":"Alice"}`),
			json.RawMessage(`{"name":"Bob"}`),
		}
		sortMigrateItems("notInRegistry", items)
		// Order preserved exactly.
		want := []string{"Charlie", "Alice", "Bob"}
		for i, it := range items {
			if got := extractField(it, "name"); got != want[i] {
				t.Errorf("position %d: got %s, want %s (sort should be no-op)", i, got, want[i])
			}
		}
	})

	t.Run("empty input does not panic", func(t *testing.T) {
		sortMigrateItems("createProjects", nil)
		sortMigrateItems("createProjects", []json.RawMessage{})
	})
}

// #326: sortExtractItems orders extract items by the spec's sortField.
// Extract records don't carry orgField, so any spec.orgField is ignored
// at this layer.
func TestSortExtractItems(t *testing.T) {
	t.Run("alphabetical by sort field", func(t *testing.T) {
		items := []structure.ExtractItem{
			{Data: json.RawMessage(`{"projectKey":"omega"}`)},
			{Data: json.RawMessage(`{"projectKey":"alpha"}`)},
			{Data: json.RawMessage(`{"projectKey":"mu"}`)},
		}
		sortExtractItems("setProjectSettings", items)
		want := []string{"alpha", "mu", "omega"}
		for i, it := range items {
			if got := extractField(it.Data, "projectKey"); got != want[i] {
				t.Errorf("position %d: got %s, want %s", i, got, want[i])
			}
		}
	})

	t.Run("unregistered task is a no-op", func(t *testing.T) {
		items := []structure.ExtractItem{
			{Data: json.RawMessage(`{"projectKey":"omega"}`)},
			{Data: json.RawMessage(`{"projectKey":"alpha"}`)},
		}
		sortExtractItems("notInRegistry", items)
		if extractField(items[0].Data, "projectKey") != "omega" {
			t.Errorf("expected input order preserved for unregistered task")
		}
	})
}
