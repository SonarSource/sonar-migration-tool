// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const resumeTestID = "2026-10-30-0001"

func writeRecordedMeta(t *testing.T, dir string, meta map[string]any) {
	t.Helper()
	runDir := filepath.Join(dir, resumeTestID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "extract.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRecordedMeta(t *testing.T, dir string) recordedScope {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, resumeTestID, "extract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec recordedScope
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// #666: resuming a slice with --extract_id alone takes the slice's recorded
// scope, so it is not rewritten as a full extract that hides earlier slices.
func TestRestoreResumeScope_NoFlagsTakesRecordedScope(t *testing.T) {
	dir := t.TempDir()
	writeRecordedMeta(t, dir, map[string]any{
		"partial": true, "project_keys": []string{"a", "b"},
		"objects": []string{"portfolios", "projects"}, "portfolio_key_regexp": "BANK_.+",
	})
	cfg := ExtractConfig{ExportDirectory: dir, ExtractID: resumeTestID}
	restoreResumeScope(&cfg, quietLogger)

	if !isPartialExtract(cfg) {
		t.Error("resumed slice must stay partial")
	}
	if !reflect.DeepEqual(cfg.ProjectKeys, []string{"a", "b"}) {
		t.Errorf("ProjectKeys = %v, want [a b]", cfg.ProjectKeys)
	}
	if cfg.PortfolioKeyRegexp != "BANK_.+" || cfg.PortfolioNameRegexp != "" {
		t.Errorf("portfolio regexps = %q / %q, want BANK_.+ / empty", cfg.PortfolioKeyRegexp, cfg.PortfolioNameRegexp)
	}
	if want := map[string]bool{"portfolios": true, "projects": true}; !reflect.DeepEqual(cfg.Objects, want) {
		t.Errorf("Objects = %v, want %v", cfg.Objects, want)
	}
}

// A resumed slice given scope flags of its own keeps them, and stays partial.
func TestRestoreResumeScope_OwnFlagsKeptAndStayPartial(t *testing.T) {
	dir := t.TempDir()
	writeRecordedMeta(t, dir, map[string]any{
		"partial": true, "project_keys": []string{"a", "b"}, "portfolio_key_regexp": "BANK_.+",
	})
	cfg := ExtractConfig{ExportDirectory: dir, ExtractID: resumeTestID, PortfolioNameRegexp: "Retail.*"}
	restoreResumeScope(&cfg, quietLogger)

	if !reflect.DeepEqual(cfg.ProjectKeys, []string{"a", "b"}) {
		t.Errorf("ProjectKeys = %v, want the recorded project scope", cfg.ProjectKeys)
	}
	if cfg.PortfolioNameRegexp != "Retail.*" || cfg.PortfolioKeyRegexp != "" {
		t.Errorf("portfolio regexps = %q / %q, want the run's own flags only", cfg.PortfolioKeyRegexp, cfg.PortfolioNameRegexp)
	}
	if !isPartialExtract(cfg) {
		t.Error("resumed slice must stay partial")
	}
}

func TestRestoreResumeScope_ProjectFilterRetainsMatchingRecordedKeys(t *testing.T) {
	dir := t.TempDir()
	writeRecordedMeta(t, dir, map[string]any{
		"partial": true, "project_keys": []string{"alpha", "beta", "gamma"},
	})
	cfg := ExtractConfig{ExportDirectory: dir, ExtractID: resumeTestID, ProjectKey: "alpha|gamma|new"}
	restoreResumeScope(&cfg, quietLogger)

	if !reflect.DeepEqual(cfg.ProjectKeys, []string{"alpha", "gamma"}) {
		t.Errorf("ProjectKeys = %v, want recorded keys matching the new project filter", cfg.ProjectKeys)
	}
}

// New, full and unreadable extracts are left alone.
func TestRestoreResumeScope_LeavesOtherRunsAlone(t *testing.T) {
	newRun := ExtractConfig{ExportDirectory: t.TempDir()}
	restoreResumeScope(&newRun, quietLogger)
	if isPartialExtract(newRun) {
		t.Error("a new run without flags must not become partial")
	}

	dir := t.TempDir()
	writeRecordedMeta(t, dir, map[string]any{"partial": false, "project_keys": []string{"a"}})
	full := ExtractConfig{ExportDirectory: dir, ExtractID: resumeTestID}
	restoreResumeScope(&full, quietLogger)
	if isPartialExtract(full) || full.ProjectKeys != nil {
		t.Errorf("a resumed full extract must stay full, got ProjectKeys=%v", full.ProjectKeys)
	}

	missing := ExtractConfig{ExportDirectory: t.TempDir(), ExtractID: resumeTestID}
	restoreResumeScope(&missing, quietLogger)
	if isPartialExtract(missing) {
		t.Error("a run with no extract.json must not become partial")
	}
}

// End to end: RunExtract resumed with --extract_id alone rewrites extract.json
// with the slice's original scope.
func TestRunExtractResumeKeepsSliceScopeInMetadata(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	dir := t.TempDir()
	writeRecordedMeta(t, dir, map[string]any{
		"url": srv.URL, "partial": true, "project_keys": []string{"proj1"},
		"portfolio_key_regexp": "BANK_.+",
	})
	cfg := ExtractConfig{
		URL: srv.URL, Token: testToken, ExportDirectory: dir,
		ExtractID: resumeTestID, TargetTask: "getProjects", Concurrency: 5,
	}
	if _, err := RunExtract(context.Background(), cfg); err != nil {
		t.Fatalf("RunExtract failed: %v", err)
	}

	rec := readRecordedMeta(t, dir)
	if !rec.Partial {
		t.Error("extract.json: partial = false after resume, want true")
	}
	if !reflect.DeepEqual(rec.ProjectKeys, []string{"proj1"}) {
		t.Errorf("extract.json: project_keys = %v, want [proj1]", rec.ProjectKeys)
	}
	if rec.PortfolioKeyRegexp != "BANK_.+" {
		t.Errorf("extract.json: portfolio_key_regexp = %q, want BANK_.+", rec.PortfolioKeyRegexp)
	}
}

func TestWriteMetadataFile_RecordsScope(t *testing.T) {
	dir := t.TempDir()
	m := extractMeta{
		URL: "http://x", Partial: true, Objects: []string{"portfolios"},
		PortfolioNameRegexp: "Retail.*",
	}
	if err := writeMetadataFile(dir, m); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "extract.json"))
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if objs, _ := out["objects"].([]any); len(objs) != 1 || objs[0] != "portfolios" {
		t.Errorf("objects = %v, want [portfolios]", out["objects"])
	}
	if out["portfolio_name_regexp"] != "Retail.*" {
		t.Errorf("portfolio_name_regexp = %v, want Retail.*", out["portfolio_name_regexp"])
	}
	if _, ok := out["portfolio_key_regexp"]; ok {
		t.Error("an unset portfolio_key_regexp must not be written")
	}
}

func TestSortedObjects(t *testing.T) {
	if got := sortedObjects(nil); got != nil {
		t.Errorf("sortedObjects(nil) = %v, want nil", got)
	}
	got := sortedObjects(map[string]bool{"projects": true, "groups": true, "settings": false})
	if want := []string{"groups", "projects"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sortedObjects = %v, want %v", got, want)
	}
}
