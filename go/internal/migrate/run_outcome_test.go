// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"errors"
	"testing"
)

// Actionable is what keeps a re-run from looking like a regression: the
// classes the migration is content with must not count as failures, and
// an unclassified failure must not be assumed benign.
func TestFailureClassActionable(t *testing.T) {
	for _, tc := range []struct {
		class FailureClass
		want  bool
	}{
		{FailureByDesign, false},
		{FailureAlreadyDone, false},
		{FailureEnvironment, true},
		{FailureBug, true},
		{FailureClass(""), true},
	} {
		if got := tc.class.Actionable(); got != tc.want {
			t.Errorf("FailureClass(%q).Actionable() = %v, want %v", tc.class, got, tc.want)
		}
	}
}

// Outcome must report the tallies the counter accumulated, and Actionable
// must net off the benign classes.
func TestTaskOutcomeActionable(t *testing.T) {
	c := NewTaskCounter("setProjectSourceLink")
	c.Success()
	c.FailWith(FailureAlreadyDone)
	c.FailWith(FailureByDesign)
	c.FailWith(FailureEnvironment)
	c.Fail() // unclassified

	o := c.Outcome()
	if o.Succeeded != 1 {
		t.Errorf("Succeeded = %d, want 1", o.Succeeded)
	}
	if o.Failed != 4 {
		t.Errorf("Failed = %d, want 4", o.Failed)
	}
	// 4 failures minus the by-design and already-done ones.
	if got := o.Actionable(); got != 2 {
		t.Errorf("Actionable() = %d, want 2 (environment + unclassified)", got)
	}
}

// A task that failed every item it touched while returning nil used to be
// recorded ok=true, so the report rendered "OK: Yes" for it and nothing
// in the run metadata disagreed.
func TestComputeStatusDegradesOnActionableFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		retErr error
		tasks  []TaskTiming
		want   string
	}{
		{
			name:  "clean run is a success",
			tasks: []TaskTiming{{Name: "createProjects", OK: true, Succeeded: 2}},
			want:  "success",
		},
		{
			name: "per-item failures that need acting on degrade to partial",
			tasks: []TaskTiming{
				{Name: "createProjects", OK: true, Succeeded: 2},
				// setProjectSourceLink failing 2/2 on 403s: no task
				// error, so the run completed, but it did not do what it
				// was asked.
				{Name: "setProjectSourceLink", OK: false, Failed: 2, ActionableFailures: 2},
			},
			want: "partial",
		},
		{
			name: "expected refusals keep it a success",
			tasks: []TaskTiming{
				{Name: "createProjects", OK: true, Succeeded: 2},
				// A re-run finding its groups already present.
				{Name: "createGroups", OK: true, Failed: 3, ActionableFailures: 0},
			},
			want: "success",
		},
		{
			name:   "a terminal error with some progress is partial",
			retErr: errors.New("boom"),
			tasks: []TaskTiming{
				{Name: "createProjects", OK: true},
				{Name: "createGroups", OK: false},
			},
			want: "partial",
		},
		{
			name:   "a terminal error with no progress is failed",
			retErr: errors.New("boom"),
			tasks:  []TaskTiming{{Name: "createProjects", OK: false}},
			want:   "failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tm := &RunTimings{}
			for _, task := range tc.tasks {
				tm.addTask(task)
			}
			if got := computeStatus(tc.retErr, tm); got != tc.want {
				t.Errorf("computeStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

// A task whose every failure was by-design achieved nothing, but for a
// reason the tool chose — it must stay a warning rather than escalating
// to an error that reads as a broken migration.
func TestAllByDesignTotalFailureDoesNotEscalateToError(t *testing.T) {
	c := NewTaskCounter("setProjectGates")
	// Both source gates were built-in, which are deliberately not
	// migrated: 2 failures, 0 successes, nothing wrong.
	c.FailWith(FailureByDesign)
	c.FailWith(FailureByDesign)

	logs := captureSummary(t, c)
	assertContains(t, logs, `level=WARN`, `failed=2`, `failed_by_design=2`, `failed_actionable=0`)
	assertNotContains(t, logs, `level=ERROR`)
}

// The same shape with an actionable cause must still escalate: a task
// that achieved nothing for a reason worth acting on is an error.
func TestTotalFailureWithActionableCauseEscalatesToError(t *testing.T) {
	c := NewTaskCounter("setProjectSourceLink")
	c.FailWith(FailureEnvironment)
	c.FailWith(FailureEnvironment)

	logs := captureSummary(t, c)
	assertContains(t, logs, `level=ERROR`, `failed=2`, `failed_actionable=2`)
}
