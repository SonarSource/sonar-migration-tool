// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
)

// #667 — a large instance migrated in --project_key_regexp slices, one
// migrate run per slice, must get a final report covering every slice.

// writeSliceRun writes a migrate run dir that created the given source
// projects into org1.
func writeSliceRun(t *testing.T, exportDir, runID string, keys ...string) string {
	t.Helper()
	runDir := filepath.Join(exportDir, runID)
	rows := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, map[string]any{
			"key": k, "name": k, "sonarcloud_org_key": "org1", "cloud_project_key": "org1_" + k,
		})
	}
	writeTaskJSONL(t, runDir, "createProjects", rows)
	writeRunMeta(t, runDir, map[string]any{
		"started_at": "2026-11-02T18:08:07Z", "completed_at": "2026-11-02T18:09:05Z", "overall_status": "success",
	})
	return runDir
}

// writeResetRun writes a reset run dir, recognised by its clear.json.
func writeResetRun(t *testing.T, exportDir, runID string) {
	t.Helper()
	runDir := filepath.Join(exportDir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "clear.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDryRunResetRun(t *testing.T, exportDir, runID string) {
	t.Helper()
	runDir := filepath.Join(exportDir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "clear.json"), []byte(`{"dry_run":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func projectKeys(items []EntityItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.SourceKey)
	}
	sort.Strings(out)
	return out
}

func runIDs(runs []CoveredRun) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.RunID)
	}
	return out
}

func TestCollectCumulativeSummary_UnreadableEarlierRunReturnsSingleRunSummary(t *testing.T) {
	exportDir := t.TempDir()
	broken := filepath.Join(exportDir, "2026-11-02-0003")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunMeta(t, broken, map[string]any{"overall_status": "success"})
	if err := os.Mkdir(filepath.Join(broken, "requests.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	latest := writeSliceRun(t, exportDir, "2026-11-02-0004", "LATEST")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	if len(sum.Runs) != 0 {
		t.Errorf("Runs = %v, want no cumulative runs when the earlier run cannot be read", runIDs(sum.Runs))
	}
	if got := projectKeys(findSection(sum, "Projects").Succeeded); strings.Join(got, ",") != "LATEST" {
		t.Errorf("succeeded projects = %v, want only the latest run", got)
	}
}

func TestCollectCumulativeSummary_SlicesAddUp(t *testing.T) {
	exportDir := t.TempDir()
	writeSliceRun(t, exportDir, "2026-11-02-0003", "VERT0_PROJ0", "VERT0_PROJ1")
	latest := writeSliceRun(t, exportDir, "2026-11-02-0005", "VERT1_PROJ0")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	got := projectKeys(findSection(sum, "Projects").Succeeded)
	want := []string{"VERT0_PROJ0", "VERT0_PROJ1", "VERT1_PROJ0"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("succeeded projects = %v, want %v", got, want)
	}
	if ids := runIDs(sum.Runs); strings.Join(ids, ",") != "2026-11-02-0003,2026-11-02-0005" {
		t.Errorf("Runs = %v, want both runs oldest first", ids)
	}
	if sum.RunID != "2026-11-02-0005" {
		t.Errorf("RunID = %q, want the latest run", sum.RunID)
	}
}

// A reset wipes the target, so the runs before it are not part of the report.
func TestCollectCumulativeSummary_StopsAtLatestReset(t *testing.T) {
	exportDir := t.TempDir()
	writeSliceRun(t, exportDir, "2026-11-02-0001", "BEFORE_RESET")
	writeResetRun(t, exportDir, "2026-11-02-0002")
	writeSliceRun(t, exportDir, "2026-11-02-0003", "VERT0_PROJ0")
	latest := writeSliceRun(t, exportDir, "2026-11-02-0004", "VERT1_PROJ0")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	got := projectKeys(findSection(sum, "Projects").Succeeded)
	if strings.Join(got, ",") != "VERT0_PROJ0,VERT1_PROJ0" {
		t.Errorf("succeeded projects = %v, want only the runs after the reset", got)
	}
}

func TestCollectCumulativeSummary_DryRunResetDoesNotCutOffEarlierRuns(t *testing.T) {
	exportDir := t.TempDir()
	writeSliceRun(t, exportDir, "2026-11-02-0001", "BEFORE_DRY_RUN")
	writeDryRunResetRun(t, exportDir, "2026-11-02-0002")
	latest := writeSliceRun(t, exportDir, "2026-11-02-0003", "AFTER_DRY_RUN")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	got := projectKeys(findSection(sum, "Projects").Succeeded)
	if strings.Join(got, ",") != "AFTER_DRY_RUN,BEFORE_DRY_RUN" {
		t.Errorf("succeeded projects = %v, want runs on both sides of the dry-run reset", got)
	}
}

// Regenerating the report of an older run leaves the newer runs out.
func TestCollectCumulativeSummary_IgnoresNewerRuns(t *testing.T) {
	exportDir := t.TempDir()
	older := writeSliceRun(t, exportDir, "2026-11-02-0003", "VERT0_PROJ0")
	writeSliceRun(t, exportDir, "2026-11-02-0005", "VERT1_PROJ0")

	sum, err := CollectCumulativeSummary(older, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	if got := projectKeys(findSection(sum, "Projects").Succeeded); strings.Join(got, ",") != "VERT0_PROJ0" {
		t.Errorf("succeeded projects = %v, want only VERT0_PROJ0", got)
	}
	if len(sum.Runs) != 0 {
		t.Errorf("Runs = %v, want none for a single run", runIDs(sum.Runs))
	}
}

// A project a later slice migrated again shows that run's outcome only.
func TestCollectCumulativeSummary_NewestRunWinsPerProject(t *testing.T) {
	exportDir := t.TempDir()
	first := filepath.Join(exportDir, "2026-11-02-0003")
	writeTaskJSONL(t, first, "createProjects", []map[string]any{
		{"key": "P1", "name": "P1", "sonarcloud_org_key": "org1", "status": "failed", "error": "boom"},
	})
	writeRunMeta(t, first, map[string]any{"overall_status": "partial"})
	latest := writeSliceRun(t, exportDir, "2026-11-02-0004", "P1")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	proj := findSection(sum, "Projects")
	if got := projectKeys(proj.Succeeded); strings.Join(got, ",") != "P1" {
		t.Errorf("succeeded projects = %v, want P1", got)
	}
	if len(proj.Failed) != 0 {
		t.Errorf("failed projects = %+v, want the older failure superseded", proj.Failed)
	}
}

// Every slice re-creates the global objects, so a later slice finds them
// already there. That expected failure must not hide the earlier success,
// while a real failure in a later run still replaces it.
func TestMergeSections_ExpectedFailureKeepsEarlierSuccess(t *testing.T) {
	group := EntityItem{Name: "devs", Organization: "org1"}
	alreadyThere := group
	alreadyThere.Cause = string(migrate.FailureAlreadyDone)
	broken := EntityItem{Name: "ops", Organization: "org1", Cause: string(migrate.FailureBug)}

	newest := &MigrationSummary{Sections: []Section{{Name: "Groups", Failed: []EntityItem{alreadyThere, broken}}}}
	older := &MigrationSummary{Sections: []Section{{Name: "Groups", Succeeded: []EntityItem{group, {Name: "ops", Organization: "org1"}}}}}

	merged := mergeSections([]*MigrationSummary{newest, older})[0]
	if len(merged.Succeeded) != 1 || merged.Succeeded[0].Name != "devs" {
		t.Errorf("succeeded = %+v, want only devs", merged.Succeeded)
	}
	if len(merged.Failed) != 1 || merged.Failed[0].Name != "ops" {
		t.Errorf("failed = %+v, want only the real ops failure", merged.Failed)
	}
}

func TestRenderMarkdown_ListsCoveredRuns(t *testing.T) {
	exportDir := t.TempDir()
	writeSliceRun(t, exportDir, "2026-11-02-0003", "VERT0_PROJ0")
	latest := writeSliceRun(t, exportDir, "2026-11-02-0005", "VERT1_PROJ0")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	md, err := RenderMarkdown(sum)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	out := string(md)
	for _, want := range []string{"## Migration Runs", "cumulative over 2 migration runs", "| 2026-11-02-0003 |", "| 2026-11-02-0005 |"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	if _, err := RenderPDF(sum); err != nil {
		t.Errorf("RenderPDF: %v", err)
	}
}
