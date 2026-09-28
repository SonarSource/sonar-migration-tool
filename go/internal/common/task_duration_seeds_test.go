// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"testing"
	"time"
)

// migrateSeededTasks is the migrate-side half of SeedTaskDurations. The
// extract half (every get* entry) is out of scope for #597, which re-mined
// migrate runs only.
var migrateSeededTasks = []string{
	"importProjectData",
	"syncIssueMetadata",
	"createProjects",
	"setGlobalSettings",
	"grantMigrationUserProjectPermissions",
	"setProjectGroupPermissions",
	"restoreProfiles",
	"syncHotspotMetadata",
	"createProfiles",
}

// #597: the ETA's remaining error after #598 was the seeds' proportions
// relative to each other, not their overall scale. syncIssueMetadata was
// the worst offender: seeded at 492s it carried about a third of all
// seeded work in a migrate, and it finished in seconds. A third of the
// "work remaining" could therefore evaporate at any moment.
//
// This test pins the property that matters rather than the number. It
// fails if any task other than importProjectData — which really is 85-95%
// of a migrate — is ever seeded above a tenth of the total again.
func TestNoSingleMigrateSeedDominatesExceptImportProjectData(t *testing.T) {
	var total time.Duration
	for _, name := range migrateSeededTasks {
		total += ExpectedTaskDuration(name)
	}
	if total <= 0 {
		t.Fatal("total seeded migrate work is zero")
	}
	for _, name := range migrateSeededTasks {
		if name == "importProjectData" {
			continue
		}
		share := float64(ExpectedTaskDuration(name)) / float64(total)
		if share > 0.10 {
			t.Errorf("%s is seeded at %v, %.0f%% of the %v total — re-check it against real run data before raising it",
				name, ExpectedTaskDuration(name), share*100, total)
		}
	}
}

// Every migrate-side seed must stay in the range real runs produced.
// A seed below a second is noise, and one above importProjectData's own
// would re-create the #597 proportion problem in a different task.
func TestMigrateSeedsStayWithinMeasuredRange(t *testing.T) {
	importSeed := ExpectedTaskDuration("importProjectData")
	for _, name := range migrateSeededTasks {
		d := ExpectedTaskDuration(name)
		if d < time.Second {
			t.Errorf("%s seeded at %v, under a second — use DefaultTaskDuration instead of a tiny seed", name, d)
		}
		if name != "importProjectData" && d >= importSeed {
			t.Errorf("%s seeded at %v, at or above importProjectData's %v", name, d, importSeed)
		}
	}
}

// setProfileParent was seeded at 15s by #564 and measured 0.00s in all 17
// runs re-mined for #597, so it was dropped and must fall back to the
// default rather than carry a seed again.
func TestSetProfileParentIsUnseeded(t *testing.T) {
	if _, ok := SeedTaskDurations["setProfileParent"]; ok {
		t.Error("setProfileParent is seeded again; it recorded 0.00s in every re-mined run")
	}
	if got := ExpectedTaskDuration("setProfileParent"); got != DefaultTaskDuration {
		t.Errorf("ExpectedTaskDuration(setProfileParent) = %v, want the %v default", got, DefaultTaskDuration)
	}
}

// SecondsPerProjectIssueSync scales syncIssueMetadata by project count.
// The fit behind it is weak (R^2 0.36), so the value must stay small
// enough that RunMigrate's floor against the seeded constant is what
// governs small runs, and large enough to matter on a big one.
func TestSecondsPerProjectIssueSyncIsPlausible(t *testing.T) {
	if SecondsPerProjectIssueSync <= 0 || SecondsPerProjectIssueSync > 5 {
		t.Errorf("SecondsPerProjectIssueSync = %v, want a small positive rate", SecondsPerProjectIssueSync)
	}
	// A one-project run must not out-seed the constant, or the floor in
	// RunMigrate would never engage and a trivial run would seed under a
	// second of work for the whole task.
	oneProject := time.Duration(SecondsPerProjectIssueSync * float64(time.Second))
	if oneProject >= ExpectedTaskDuration("syncIssueMetadata") {
		t.Errorf("one project seeds %v, at or above the %v constant — the floor in RunMigrate is then dead code",
			oneProject, ExpectedTaskDuration("syncIssueMetadata"))
	}
}
