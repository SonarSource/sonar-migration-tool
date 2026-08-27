// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"reflect"
	"testing"
	"time"
)

// #530: buildPhaseBreakdown must sum each task's duration into the report
// phase migrate.CategorizeTask assigns its name to, and always return all
// 4 entries in runPhaseOrder — even when a phase has zero tasks.
func TestBuildPhaseBreakdown(t *testing.T) {
	tasks := []TaskTiming{
		// General (default bucket) -> "Global objects provisioning".
		{Task: "createProfiles", Duration: 5 * time.Second},
		{Task: "createPortfolios", Duration: 5 * time.Second},
		// ProjectConfig -> "Projects configuration provisioning".
		{Task: "createProjects", Duration: 30 * time.Second},
		{Task: "setProjectSettings", Duration: 15 * time.Second},
		// ProjectData -> "Project data migration".
		{Task: "importProjectData", Duration: 40 * time.Second},
		// IssueSync -> "Issue and Hotspot sync".
		{Task: "syncIssueMetadata", Duration: 12 * time.Second},
		{Task: "syncHotspotMetadata", Duration: 8 * time.Second},
	}

	got := buildPhaseBreakdown(tasks)
	want := []PhaseBreakdownEntry{
		{Name: "Global objects provisioning", Duration: 10 * time.Second},
		{Name: "Projects configuration provisioning", Duration: 45 * time.Second},
		{Name: "Project data migration", Duration: 40 * time.Second},
		{Name: "Issue and Hotspot sync", Duration: 20 * time.Second},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildPhaseBreakdown(...) = %+v, want %+v", got, want)
	}
}

// An empty (or nil) task list must still yield all 4 zero-duration
// entries, not an empty slice — the Run metadata section always shows a
// stable set of rows.
func TestBuildPhaseBreakdown_EmptyTasksYieldsZeroedEntries(t *testing.T) {
	got := buildPhaseBreakdown(nil)
	if len(got) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(got), got)
	}
	for _, e := range got {
		if e.Duration != 0 {
			t.Errorf("expected zero duration for %q, got %v", e.Name, e.Duration)
		}
	}
}

// An unrecognized task name falls into the default (General) bucket,
// mirroring migrate.CategorizeTask's own default case.
func TestBuildPhaseBreakdown_UnknownTaskFallsBackToGeneral(t *testing.T) {
	got := buildPhaseBreakdown([]TaskTiming{{Task: "someBrandNewTask", Duration: 3 * time.Second}})
	for _, e := range got {
		if e.Name == "Global objects provisioning" {
			if e.Duration != 3*time.Second {
				t.Errorf("General bucket duration = %v, want 3s", e.Duration)
			}
			continue
		}
		if e.Duration != 0 {
			t.Errorf("expected zero duration for %q, got %v", e.Name, e.Duration)
		}
	}
}
