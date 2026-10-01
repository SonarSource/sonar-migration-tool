// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"testing"
	"time"
)

// recordedMigrateRun is the real task timeline of a one-project migrate
// (juice-shop, SonarQube Server to SonarQube Cloud, 2026-09-22), taken
// from that run's own log: each task's start offset and the duration it
// reported on completion. Offsets and durations are milliseconds from the
// start of the run, which finished in recordedMigrateTotal.
//
// The offsets come from the "running task" log line, which runPhase emits
// as it hands a task to its errgroup, so for a phase wider than the
// concurrency cap they are queue-entry times rather than execution starts.
// That is what the log records and it is left as recorded; the timeline is
// a historical artefact, not a description of current runPhase behaviour.
//
// It is kept as a fixture because the shape is what makes ETA estimation
// hard, and no synthetic plan reproduces it honestly: ten seconds of many
// cheap tasks, then importProjectData alone for 77 of the 98 seconds, then
// a short burst of issue/hotspot sync at the end. An estimator can look
// fine on an evenly-spread plan and still be badly wrong here.
var recordedMigrateRun = []struct {
	name           string
	startMs, durMs int
}{
	{"generateGateMappings", 2266, 1},
	{"generateGroupMappings", 2266, 4},
	{"generateOrganizationMappings", 2266, 3},
	{"generatePortfolioMappings", 2266, 0},
	{"generateProfileMappings", 2266, 1},
	{"generateProjectMappings", 2266, 3},
	{"generateTemplateMappings", 2266, 2},
	{"compareBuiltInProfiles", 2270, 3548},
	{"createGates", 2270, 0},
	{"createGroups", 2270, 759},
	{"createMigrationGroups", 2270, 771},
	{"createPermissionTemplates", 2270, 1234},
	{"createProfiles", 2270, 1},
	{"createProjects", 2270, 2436},
	{"getMigrationUser", 2271, 487},
	{"getOrgBinding", 2272, 1964},
	{"getOrgRepos", 2759, 200},
	{"setGlobalNewCodePeriod", 3029, 1571},
	{"setGlobalWebhooks", 3041, 0},
	{"updateRuleDescriptions", 3230, 1616},
	{"updateRuleTags", 3231, 1342},
	{"addMigrationGroupToTemplates", 5819, 1942},
	{"addMigrationUserToMigrationGroups", 5819, 1447},
	{"analyzeProfileRules", 5819, 332},
	{"getGateConditions", 5819, 0},
	{"getProfileBackups", 5819, 0},
	{"getProjectIds", 5819, 219},
	{"grantMigrationUserProjectPermissions", 5819, 3052},
	{"setDefaultTemplates", 5819, 744},
	{"setGlobalSettings", 5819, 2295},
	{"setOrgGroupPermissions", 6038, 582},
	{"setProfileGroupPermissions", 6151, 14},
	{"setProfileParent", 6564, 0},
	{"setTemplateGroupPermissions", 6578, 2327},
	{"addGateConditions", 8907, 5},
	{"matchProjectRepos", 8907, 6},
	{"restoreProfiles", 8907, 88},
	{"setNewCodePeriods", 8907, 494},
	{"setProjectGates", 8907, 5},
	{"setProjectGroupPermissions", 8907, 743},
	{"setProjectLinks", 8907, 1},
	{"setProjectProfiles", 8912, 2},
	{"setProjectSettings", 8912, 964},
	{"setProjectSourceLink", 8913, 557},
	{"setProjectTags", 8913, 337},
	{"setProjectWebhooks", 8914, 0},
	{"importProjectData", 9877, 77226},
	{"setDefaultGates", 9877, 0},
	{"setDefaultProfiles", 9877, 0},
	{"setProjectBinding", 9877, 0},
	{"syncHotspotMetadata", 87106, 11025},
	{"syncIssueMetadata", 87106, 1638},
}

const recordedMigrateTotal = 98134 * time.Millisecond

// recordedMigratePhaseStartsMs is when each of that run's six phases
// began, read off the same log. The tracker's ETA follows the plan's
// phases (#621), so the replay hands it the plan in its real shape.
var recordedMigratePhaseStartsMs = []int{2266, 2270, 5819, 8907, 9877, 87106}

// replayRecordedRun drives the fixture through a Tracker on a virtual
// clock and returns one (eta, trueRemaining) pair per interval tick.
// Deterministic: no sleeps, no wall-clock reads, so the numbers the
// assertions below pin cannot drift with machine speed or CI load.
// replayTick is one interval sample of the replay: what the estimator
// reported, and what was actually left to run.
type replayTick struct {
	elapsed       float64
	percent       float64
	eta           float64
	trueRemaining float64
}

func replayRecordedRun(t *testing.T, interval time.Duration) (ticks []replayTick) {
	t.Helper()

	origin := time.Date(2026, 9, 22, 16, 18, 29, 0, time.UTC)
	vnow := origin

	plan := make([][]string, len(recordedMigratePhaseStartsMs))
	for _, task := range recordedMigrateRun {
		phase := 0
		for i, startMs := range recordedMigratePhaseStartsMs {
			if task.startMs >= startMs {
				phase = i
			}
		}
		plan[phase] = append(plan[phase], task.name)
	}
	tr := NewTracker(testLogger(), plan, ExpectedTaskDuration)
	tr.start = origin
	tr.now = func() time.Time { return vnow }

	type moment struct {
		at   time.Time
		done bool
		name string
	}
	var moments []moment
	for _, task := range recordedMigrateRun {
		start := origin.Add(time.Duration(task.startMs) * time.Millisecond)
		moments = append(moments,
			moment{at: start, name: task.name},
			moment{at: start.Add(time.Duration(task.durMs) * time.Millisecond), done: true, name: task.name})
	}
	// Stable insertion order per timestamp is enough; a start and its own
	// completion can share a timestamp for a 0ms task, and start must win.
	for i := 1; i < len(moments); i++ {
		for j := i; j > 0 && moments[j].at.Before(moments[j-1].at); j-- {
			moments[j], moments[j-1] = moments[j-1], moments[j]
		}
	}

	nextTick := origin.Add(interval)
	record := func() {
		percent, eta, known := tr.snapshot()
		if !known {
			return
		}
		ticks = append(ticks, replayTick{
			elapsed:       vnow.Sub(origin).Seconds(),
			percent:       percent,
			eta:           eta.Seconds(),
			trueRemaining: (recordedMigrateTotal - vnow.Sub(origin)).Seconds(),
		})
	}
	for _, m := range moments {
		for !m.at.Before(nextTick) {
			vnow = nextTick
			record()
			nextTick = nextTick.Add(interval)
		}
		vnow = m.at
		if m.done {
			tr.MarkTaskComplete(m.name)
		} else {
			tr.MarkTaskStarted(m.name)
		}
	}
	return ticks
}

// TestTrackerETAIsCloseToLinearOnRecordedRun holds the one-project run to
// the same bar as #621's 78-project one (see assertCloseToLinear): it is
// the opposite shape, with seeds far too big rather than far too small,
// and the fix for one must not break the other.
func TestTrackerETAIsCloseToLinearOnRecordedRun(t *testing.T) {
	ticks := replayRecordedRun(t, 10*time.Second)
	if len(ticks) < 8 {
		t.Fatalf("got %d ticks, want at least 8 — the fixture should cover a ~98s run", len(ticks))
	}
	assertCloseToLinear(t, ticks, recordedMigrateTotal.Seconds(), normalRun)
}

// TestTrackerProgressNeverGoesBackwardsOnRecordedRun: whatever the ETA
// does, the percentage an operator watches must only ever climb. A
// snapshot that can retreat reads as the run losing ground, and the
// work-accounting in snapshot is easy to break in exactly that way.
func TestTrackerProgressNeverGoesBackwardsOnRecordedRun(t *testing.T) {
	ticks := replayRecordedRun(t, 5*time.Second)
	for i := 1; i < len(ticks); i++ {
		if ticks[i].percent < ticks[i-1].percent-0.01 {
			t.Errorf("percent went backwards at tick %d: %.2f then %.2f", i+1, ticks[i-1].percent, ticks[i].percent)
		}
	}
}
