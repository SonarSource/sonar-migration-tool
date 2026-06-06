// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestProgressLoggerZeroInterval(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// total=0 → interval=0 → Increment should be a no-op.
	prog := NewProgressLogger(logger, "test", 0)
	prog.Increment()

	if buf.Len() > 0 {
		t.Errorf("expected no output for zero-interval progress, got: %s", buf.String())
	}
}

func TestProgressLoggerLogsAtInterval(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// total=40 with default interval=20 (#326). First log at the
	// 20th item; second at the 40th (which is also the total).
	prog := NewProgressLogger(logger, "test", 40)
	for i := range 19 {
		prog.Increment()
		if buf.Len() > 0 {
			t.Fatalf("unexpected log at iteration %d", i)
		}
	}
	prog.Increment() // 20th item: first interval hit.
	if !strings.Contains(buf.String(), "test 20/40 - 50%") {
		t.Errorf("expected progress message \"test 20/40 - 50%%\", got: %s", buf.String())
	}
	for i := 20; i < 39; i++ {
		prog.Increment()
	}
	prog.Increment() // 40th item: final.
	if !strings.Contains(buf.String(), "test 40/40 - 100%") {
		t.Errorf("expected progress message \"test 40/40 - 100%%\", got: %s", buf.String())
	}
}

// The final-item log MUST fire even when total isn't a clean
// multiple of the interval — e.g. createProjects with 975 items at
// every-50 logs at 950 then jumps to 975 with a 100% line.
func TestProgressLoggerFiresFinalHundredPercent(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	prog := NewProgressLogger(logger, "createProjects", 975) // interval=50 (#340)

	for i := 0; i < 975; i++ {
		prog.Increment()
	}
	out := buf.String()
	if !strings.Contains(out, "createProjects 950/975 - 97%") {
		t.Errorf("expected interval-aligned line at 950, got:\n%s", out)
	}
	if !strings.Contains(out, "createProjects 975/975 - 100%") {
		t.Errorf("expected final 100%% line at 975, got:\n%s", out)
	}
}

// Per-task interval overrides take precedence over the size-based
// default. Cases sample each bucket of the rate-based tuning rolled
// out in #340 plus the project-data-migration tasks whose tunings
// were locked in by #311 / #326 / #300.
func TestProgressLoggerHonoursPerTaskInterval(t *testing.T) {
	cases := []struct {
		task          string
		total         int
		wantInterval  int64
		wantFirstAt   int
		wantFirstLine string
	}{
		{"configurePortfolios", 87, 10, 10, "configurePortfolios 10/87 - 11%"},
		{"createProjects", 975, 50, 50, "createProjects 50/975 - 5%"},
		{"setProjectSettings", 1234, 50, 50, "setProjectSettings 50/1234 - 4%"},
		{"setProjectGroupPermissions", 19778, 100, 100, "setProjectGroupPermissions 100/19778 - 0%"},
		{"deleteTemplates", 260, 100, 100, "deleteTemplates 100/260 - 38%"},
		{"deleteGroups", 140, 10, 10, "deleteGroups 10/140 - 7%"},
		{"createGroups", 620, 200, 200, "createGroups 200/620 - 32%"},
		{"getProfileBackups", 3800, 500, 500, "getProfileBackups 500/3800 - 13%"},
		{"updateRuleTags", 79910, 1000, 1000, "updateRuleTags 1000/79910 - 1%"},
		{"importProjectData", 500, 20, 20, "importProjectData 20/500 - 4%"},
		// syncIssueMetadata / syncHotspotMetadata use a ProgressLogLabel
		// override (#348) so the operator-visible log reads "Projects
		// issue sync:" / "Projects hotspot sync:" instead of the
		// camelCase task name.
		{"syncIssueMetadata", 123, 10, 10, "Projects issue sync: 10/123 - 8%"},
		{"syncHotspotMetadata", 42, 10, 10, "Projects hotspot sync: 10/42 - 23%"},
	}
	for _, c := range cases {
		t.Run(c.task, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			prog := NewProgressLogger(logger, c.task, c.total)
			if prog.Interval() != c.wantInterval {
				t.Errorf("interval: want %d, got %d", c.wantInterval, prog.Interval())
			}
			for i := 0; i < c.wantFirstAt-1; i++ {
				prog.Increment()
				if buf.Len() > 0 {
					t.Fatalf("unexpected log at iteration %d for %s", i, c.task)
				}
			}
			prog.Increment()
			if !strings.Contains(buf.String(), c.wantFirstLine) {
				t.Errorf("want first log to contain %q, got: %s", c.wantFirstLine, buf.String())
			}
		})
	}
}

// #348: NewProgressLogger uses ProgressLogLabel when the task name has
// an override there, falling back to the raw task name otherwise. Verify
// both branches at once: a task with an override (syncIssueMetadata),
// and an unregistered task name (raw passthrough).
func TestProgressLoggerHonoursLabelOverride(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Task with override.
	prog := NewProgressLogger(logger, "syncIssueMetadata", 100)
	for i := 0; i < 10; i++ {
		prog.Increment()
	}
	if !strings.Contains(buf.String(), "Projects issue sync: 10/100 - 10%") {
		t.Errorf("expected override label in log, got: %s", buf.String())
	}

	// Task without override — raw name comes through.
	buf.Reset()
	prog = NewProgressLogger(logger, "unregisteredTask", 100)
	for i := 0; i < 20; i++ {
		prog.Increment()
	}
	if !strings.Contains(buf.String(), "unregisteredTask 20/100 - 20%") {
		t.Errorf("expected raw task name in log, got: %s", buf.String())
	}
}

// #340 rule: every entry in ProgressLogInterval must use one of the
// allowed cadence values {10, 20, 50, 100, 200, 500, 1000}. Locks
// future tunings to the documented set so reviewers can grep this
// test before merging a new entry.
func TestProgressLogIntervalUsesAllowedValues(t *testing.T) {
	allowed := map[int64]bool{
		10: true, 20: true, 50: true, 100: true,
		200: true, 500: true, 1000: true,
	}
	for task, n := range ProgressLogInterval {
		if !allowed[n] {
			t.Errorf("task %q has disallowed interval %d (must be one of 10, 20, 50, 100, 200, 500, 1000)", task, n)
		}
	}
}

// Tasks without a per-task override get the 20-item default.
// Small batches collapse to total so a short task still emits a
// final "100%" line.
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
			prog := NewProgressLogger(logger, "unregisteredTask", c.total)
			if prog.Interval() != c.wantInterval {
				t.Errorf("interval: want %d, got %d", c.wantInterval, prog.Interval())
			}
		})
	}
}

// NewProgressLoggerWithInterval honours its explicit interval and
// caps it at total so a small batch still emits a final 100% line
// via the (n == total) branch in Increment.
func TestProgressLoggerWithExplicitInterval(t *testing.T) {
	t.Run("interval honoured", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		prog := NewProgressLoggerWithInterval(logger, "Issue sync:", 557, 20)
		if prog.Interval() != 20 {
			t.Errorf("interval: want 20, got %d", prog.Interval())
		}
		for i := 0; i < 19; i++ {
			prog.Increment()
			if buf.Len() > 0 {
				t.Fatalf("unexpected log at iteration %d", i)
			}
		}
		prog.Increment()
		if !strings.Contains(buf.String(), "Issue sync: 20/557 - 3%") {
			t.Errorf("want first log to contain \"Issue sync: 20/557 - 3%%\", got: %s", buf.String())
		}
	})

	t.Run("interval capped at total for small batches", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		prog := NewProgressLoggerWithInterval(logger, "Hotspot sync:", 7, 20)
		if prog.Interval() != 7 {
			t.Errorf("interval: want 7 (capped to total), got %d", prog.Interval())
		}
		for i := 0; i < 7; i++ {
			prog.Increment()
		}
		if !strings.Contains(buf.String(), "Hotspot sync: 7/7 - 100%") {
			t.Errorf("want final 100%% line, got: %s", buf.String())
		}
	})

	t.Run("non-positive interval normalised to 1", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		prog := NewProgressLoggerWithInterval(logger, "test:", 5, 0)
		if prog.Interval() != 1 {
			t.Errorf("interval: want 1 (normalised), got %d", prog.Interval())
		}
	})
}
