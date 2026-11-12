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

// writeResetRun writes a reset run dir, recognised by its clear.json, that
// completed for every organization.
func writeResetRun(t *testing.T, exportDir, runID string) {
	t.Helper()
	writeClearJSON(t, exportDir, runID, `{"dry_run":false,"completed":true}`)
}

func writeDryRunResetRun(t *testing.T, exportDir, runID string) {
	t.Helper()
	writeClearJSON(t, exportDir, runID, `{"dry_run":true,"completed":false}`)
}

func writeClearJSON(t *testing.T, exportDir, runID, meta string) {
	t.Helper()
	runDir := filepath.Join(exportDir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "clear.json"), []byte(meta), 0o644); err != nil {
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

// A reset that failed or was cancelled after its first phase may have
// deleted entities of its organizations, so the runs before it are no
// longer reported as migrated there.
func TestCollectCumulativeSummary_IncompleteResetCutsOffEarlierRuns(t *testing.T) {
	exportDir := t.TempDir()
	writeSliceRun(t, exportDir, "2026-11-02-0001", "BEFORE_RESET")
	writeClearJSON(t, exportDir, "2026-11-02-0002", `{"dry_run":false,"completed":false,"organizations":["org1"]}`)
	writeTaskJSONL(t, filepath.Join(exportDir, "2026-11-02-0002"), "deleteProjects", []map[string]any{{"key": "org1_BEFORE_RESET"}})
	latest := writeSliceRun(t, exportDir, "2026-11-02-0003", "AFTER_RESET")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	if got := projectKeys(findSection(sum, "Projects").Succeeded); strings.Join(got, ",") != "AFTER_RESET" {
		t.Errorf("succeeded projects = %v, want only the run after the partial reset", got)
	}
}

// A reset that failed before any phase ran deleted nothing.
func TestCollectCumulativeSummary_ResetFailedBeforeFirstPhaseKeepsEarlierRuns(t *testing.T) {
	exportDir := t.TempDir()
	writeSliceRun(t, exportDir, "2026-11-02-0001", "BEFORE_RESET")
	writeClearJSON(t, exportDir, "2026-11-02-0002", `{"dry_run":false,"completed":false,"organizations":["org1"]}`)
	latest := writeSliceRun(t, exportDir, "2026-11-02-0003", "AFTER_RESET")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	got := projectKeys(findSection(sum, "Projects").Succeeded)
	if strings.Join(got, ",") != "AFTER_RESET,BEFORE_RESET" {
		t.Errorf("succeeded projects = %v, want runs on both sides of the reset", got)
	}
}

// A clear.json from before resets recorded dry_run and completed counts as
// a reset of every organization when task output sits next to it.
func TestCollectCumulativeSummary_LegacyResetCutsOffEarlierRuns(t *testing.T) {
	exportDir := t.TempDir()
	writeSliceRun(t, exportDir, "2026-11-02-0001", "BEFORE_RESET")
	writeClearJSON(t, exportDir, "2026-11-02-0002", `{"run_id":"2026-11-02-0002"}`)
	writeTaskJSONL(t, filepath.Join(exportDir, "2026-11-02-0002"), "deleteProjects", []map[string]any{{"key": "org1_BEFORE_RESET"}})
	latest := writeSliceRun(t, exportDir, "2026-11-02-0003", "AFTER_RESET")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	if got := projectKeys(findSection(sum, "Projects").Succeeded); strings.Join(got, ",") != "AFTER_RESET" {
		t.Errorf("succeeded projects = %v, want only the run after the legacy reset", got)
	}
}

// A legacy clear.json alone, as an old build's dry run left it, wiped
// nothing.
func TestCollectCumulativeSummary_LegacyDryRunResetKeepsEarlierRuns(t *testing.T) {
	exportDir := t.TempDir()
	writeSliceRun(t, exportDir, "2026-11-02-0001", "BEFORE_RESET")
	writeClearJSON(t, exportDir, "2026-11-02-0002", `{"run_id":"2026-11-02-0002"}`)
	latest := writeSliceRun(t, exportDir, "2026-11-02-0003", "AFTER_RESET")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	got := projectKeys(findSection(sum, "Projects").Succeeded)
	if strings.Join(got, ",") != "AFTER_RESET,BEFORE_RESET" {
		t.Errorf("succeeded projects = %v, want runs on both sides of the legacy dry run", got)
	}
}

// A reset of some organizations wipes only their entities: the earlier
// run's entities in the other organizations stay in the report.
func TestCollectCumulativeSummary_OrgScopedResetKeepsOtherOrgs(t *testing.T) {
	exportDir := t.TempDir()
	first := filepath.Join(exportDir, "2026-11-02-0001")
	writeTaskJSONL(t, first, "createProjects", []map[string]any{
		{"key": "IN_ORG1", "name": "IN_ORG1", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_IN_ORG1"},
		{"key": "IN_ORG2", "name": "IN_ORG2", "sonarcloud_org_key": "org2", "cloud_project_key": "org2_IN_ORG2"},
	})
	writeRunMeta(t, first, map[string]any{"overall_status": "success"})
	writeClearJSON(t, exportDir, "2026-11-02-0002", `{"dry_run":false,"completed":true,"organizations":["org1"]}`)
	latest := writeSliceRun(t, exportDir, "2026-11-02-0003", "LATEST")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	got := projectKeys(findSection(sum, "Projects").Succeeded)
	if strings.Join(got, ",") != "IN_ORG2,LATEST" {
		t.Errorf("succeeded projects = %v, want the org2 project kept and the org1 one wiped", got)
	}
	if ids := runIDs(sum.Runs); strings.Join(ids, ",") != "2026-11-02-0001,2026-11-02-0003" {
		t.Errorf("Runs = %v, want both runs", ids)
	}
}

// A run whose every organization a later reset wiped is left out.
func TestCollectCumulativeSummary_OrgScopedResetOfEveryOrgDropsRun(t *testing.T) {
	exportDir := t.TempDir()
	writeSliceRun(t, exportDir, "2026-11-02-0001", "BEFORE_RESET")
	writeClearJSON(t, exportDir, "2026-11-02-0002", `{"dry_run":false,"completed":true,"organizations":["org1"]}`)
	latest := writeSliceRun(t, exportDir, "2026-11-02-0003", "AFTER_RESET")

	sum, err := CollectCumulativeSummary(latest, exportDir)
	if err != nil {
		t.Fatalf("CollectCumulativeSummary: %v", err)
	}
	if got := projectKeys(findSection(sum, "Projects").Succeeded); strings.Join(got, ",") != "AFTER_RESET" {
		t.Errorf("succeeded projects = %v, want only AFTER_RESET", got)
	}
	if len(sum.Runs) != 0 {
		t.Errorf("Runs = %v, want the wiped run left out", runIDs(sum.Runs))
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

// Permission templates migrated in slices (--permission_template_regexp) add
// up in the final report, and a template re-migrated by a later slice is
// listed once (#665).
func TestMergeSections_PermissionTemplateSlicesAddUp(t *testing.T) {
	newest := &MigrationSummary{Sections: []Section{{Name: "Permission Templates", Succeeded: []EntityItem{
		{Name: "Team B", Organization: "org1"}, {Name: "Team A", Organization: "org1"},
	}}}}
	older := &MigrationSummary{Sections: []Section{{Name: "Permission Templates", Succeeded: []EntityItem{
		{Name: "Team A", Organization: "org1"}, {Name: "Default", Organization: "org1"},
	}}}}

	merged := mergeSections([]*MigrationSummary{newest, older})[0]
	got := make([]string, 0, len(merged.Succeeded))
	for _, item := range merged.Succeeded {
		got = append(got, item.Name)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "Default,Team A,Team B" {
		t.Errorf("succeeded templates = %v, want Default, Team A, Team B", got)
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

// A later slice reports the gates no project of its own uses, with no
// organization. A gate an earlier slice migrated is not unused.
func TestMergeSections_UnusedRowDoesNotHideOrDuplicateEarlierSuccess(t *testing.T) {
	unused := EntityItem{Name: "strict", Detail: "Not used by any migrated project", SkipReason: SkipReasonUnused}
	neverUsed := EntityItem{Name: "legacy", Detail: "Not used by any migrated project", SkipReason: SkipReasonUnused}
	newest := &MigrationSummary{Sections: []Section{{Name: "Quality Gates", Skipped: []EntityItem{unused, neverUsed}}}}
	older := &MigrationSummary{Sections: []Section{{Name: "Quality Gates",
		Succeeded: []EntityItem{{Name: "strict", Organization: "org1"}},
		Skipped:   []EntityItem{neverUsed},
	}}}

	merged := mergeSections([]*MigrationSummary{newest, older})[0]
	if len(merged.Succeeded) != 1 || merged.Succeeded[0].Name != "strict" {
		t.Errorf("succeeded = %+v, want the earlier strict success", merged.Succeeded)
	}
	if len(merged.Skipped) != 1 || merged.Skipped[0].Name != "legacy" {
		t.Errorf("skipped = %+v, want only legacy, once", merged.Skipped)
	}
}

// A failure read from requests.log carries no language: a later slice's
// "already exists" on a profile must still match the earlier success.
func TestMergeSections_ProfileExpectedFailureWithoutLanguage(t *testing.T) {
	alreadyThere := EntityItem{Name: "Corp way", Organization: "org1", Cause: string(migrate.FailureAlreadyDone)}
	newest := &MigrationSummary{Sections: []Section{{Name: sectionQualityProfiles, Failed: []EntityItem{alreadyThere}}}}
	older := &MigrationSummary{Sections: []Section{{Name: sectionQualityProfiles,
		Succeeded: []EntityItem{{Name: "Corp way", Language: "java", Organization: "org1"}},
	}}}

	merged := mergeSections([]*MigrationSummary{newest, older})[0]
	if len(merged.Succeeded) != 1 || merged.Succeeded[0].Language != "java" {
		t.Errorf("succeeded = %+v, want the earlier java success", merged.Succeeded)
	}
	if len(merged.Failed) != 0 {
		t.Errorf("failed = %+v, want the expected failure dropped", merged.Failed)
	}
}

// A real profile failure without a language is the profile of that name
// the runs report, so a later success replaces it.
func TestMergeSections_ProfileFailureWithoutLanguageSupersededByLaterSuccess(t *testing.T) {
	newest := &MigrationSummary{Sections: []Section{{Name: sectionQualityProfiles,
		Succeeded: []EntityItem{{Name: "Corp way", Language: "java", Organization: "org1"}},
	}}}
	older := &MigrationSummary{Sections: []Section{{Name: sectionQualityProfiles,
		Failed: []EntityItem{{Name: "Corp way", Organization: "org1", Cause: string(migrate.FailureBug)}},
	}}}

	merged := mergeSections([]*MigrationSummary{newest, older})[0]
	if len(merged.Succeeded) != 1 || len(merged.Failed) != 0 {
		t.Errorf("succeeded = %+v, failed = %+v, want only the later success", merged.Succeeded, merged.Failed)
	}
}
