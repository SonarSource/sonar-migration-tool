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
	"sync/atomic"
	"testing"

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
	c.LogSummary(logger)

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
}

func TestTaskCounterEmptySummary(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	c := NewTaskCounter("empty")
	c.LogSummary(logger)

	if buf.Len() > 0 {
		t.Errorf("expected no output for empty counter, got: %s", buf.String())
	}
}

func TestProgressLoggerZeroInterval(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// total=0 → interval=0 → Increment should be a no-op
	prog := newProgressLogger(logger, "test", 0)
	prog.Increment()

	if buf.Len() > 0 {
		t.Errorf("expected no output for zero-interval progress, got: %s", buf.String())
	}
}

func TestProgressLoggerLogsAtInterval(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// total=40 with default interval=20 (#326). First log expected at
	// the 20th item; second log at the 40th (which is also the total).
	prog := newProgressLogger(logger, "test", 40)
	for i := range 19 {
		prog.Increment()
		if buf.Len() > 0 {
			t.Fatalf("unexpected log at iteration %d", i)
		}
	}
	prog.Increment() // 20th item: first interval hit
	if !strings.Contains(buf.String(), "test 20/40 - 50%") {
		t.Errorf("expected progress message \"test 20/40 - 50%%\", got: %s", buf.String())
	}
	for i := 20; i < 39; i++ {
		prog.Increment()
	}
	prog.Increment() // 40th item: final
	// Issue #202: message reads "task N/M - X%" — a single readable
	// line operators can scan when tailing the log.
	if !strings.Contains(buf.String(), "test 40/40 - 100%") {
		t.Errorf("expected progress message \"test 40/40 - 100%%\", got: %s", buf.String())
	}
}

// The final-item log MUST fire even when total isn't a clean
// multiple of the interval — e.g. createProjects with 975 items at
// every-10 logs at 970 then jumps to 975 with a 100% line. Issue
// #202 spec calls this out explicitly: operators need an explicit
// "task complete" marker.
func TestProgressLoggerFiresFinalHundredPercent(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	prog := newProgressLogger(logger, "createProjects", 975) // interval=10

	for i := 0; i < 975; i++ {
		prog.Increment()
	}
	out := buf.String()
	// Last regularly-scheduled line lands at 970 (interval × 97).
	if !strings.Contains(out, "createProjects 970/975 - 99%") {
		t.Errorf("expected interval-aligned line at 970, got:\n%s", out)
	}
	// Final line at 975 — fires because (n == total) even though
	// 975 isn't a multiple of 10.
	if !strings.Contains(out, "createProjects 975/975 - 100%") {
		t.Errorf("expected final 100%% line at 975, got:\n%s", out)
	}
}

// Per-task interval overrides take precedence over the size-based
// default (issue #202, retuned in #326). createProjects ships at
// every-10; setProjectSettings / setProjectGroupPermissions /
// importScanHistory at every-20; syncIssueMetadata /
// syncHotspotMetadata at every-10.
func TestProgressLoggerHonoursPerTaskInterval(t *testing.T) {
	cases := []struct {
		task          string
		total         int
		wantInterval  int64
		wantFirstAt   int // iteration count when the first log should fire
		wantFirstLine string
	}{
		{
			task:          "createProjects",
			total:         975,
			wantInterval:  10,
			wantFirstAt:   10,
			wantFirstLine: "createProjects 10/975 - 1%",
		},
		{
			task:          "configurePortfolios",
			total:         87,
			wantInterval:  10,
			wantFirstAt:   10,
			wantFirstLine: "configurePortfolios 10/87 - 11%",
		},
		{
			task:          "setProjectSettings",
			total:         1234,
			wantInterval:  20,
			wantFirstAt:   20,
			wantFirstLine: "setProjectSettings 20/1234 - 1%",
		},
		{
			task:          "setProjectGroupPermissions",
			total:         19778,
			wantInterval:  20,
			wantFirstAt:   20,
			wantFirstLine: "setProjectGroupPermissions 20/19778 - 0%",
		},
		{
			task:          "importScanHistory",
			total:         500,
			wantInterval:  20,
			wantFirstAt:   20,
			wantFirstLine: "importScanHistory 20/500 - 4%",
		},
		{
			task:          "syncIssueMetadata",
			total:         123,
			wantInterval:  10,
			wantFirstAt:   10,
			wantFirstLine: "syncIssueMetadata 10/123 - 8%",
		},
		{
			task:          "syncHotspotMetadata",
			total:         42,
			wantInterval:  10,
			wantFirstAt:   10,
			wantFirstLine: "syncHotspotMetadata 10/42 - 23%",
		},
	}
	for _, c := range cases {
		t.Run(c.task, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			prog := newProgressLogger(logger, c.task, c.total)
			if prog.interval != c.wantInterval {
				t.Errorf("interval: want %d, got %d", c.wantInterval, prog.interval)
			}
			for i := 0; i < c.wantFirstAt-1; i++ {
				prog.Increment()
				if buf.Len() > 0 {
					t.Fatalf("unexpected log at iteration %d for %s", i, c.task)
				}
			}
			prog.Increment() // first interval hit
			if !strings.Contains(buf.String(), c.wantFirstLine) {
				t.Errorf("want first log to contain %q, got: %s", c.wantFirstLine, buf.String())
			}
		})
	}
}

// #326: tasks without a per-task override get the new 20-item default.
// Small batches collapse to total so a short task still emits a final
// "100%" line.
func TestProgressLoggerDefaultCadenceIs20(t *testing.T) {
	cases := []struct {
		name         string
		total        int
		wantInterval int64
	}{
		{"large batch", 5000, 20},
		{"medium batch", 200, 20},
		{"just-above-default", 21, 20},
		{"exact default", 20, 20},
		{"small batch caps to total", 7, 7},
		{"single item", 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			prog := newProgressLogger(logger, "unregisteredTask", c.total)
			if prog.interval != c.wantInterval {
				t.Errorf("interval: want %d, got %d", c.wantInterval, prog.interval)
			}
		})
	}
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
