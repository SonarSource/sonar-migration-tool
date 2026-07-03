// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"testing"
)

// newTimingTestEnv creates the shared executor + log buffer + RunTimings used
// by all timing_log tests.
func newTimingTestEnv(t *testing.T) (*Executor, *bytes.Buffer, *RunTimings) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	e := &Executor{Sem: make(chan struct{}, 4), Logger: logger}
	return e, &buf, &RunTimings{}
}

// Issue #311: runPhase must emit a "Task <name>: Duration hh:mm:ss.xxx"
// INFO line at the end of every task, on both the success and failure
// paths.
func TestRunPhaseEmitsTaskDurationLog(t *testing.T) {
	e, buf, tm := newTimingTestEnv(t)

	registry := map[string]*TaskDef{
		"quickTask": {
			Name: "quickTask",
			Run: func(_ context.Context, _ *Executor) error {
				return nil
			},
		},
	}

	if err := runPhase(context.Background(), e, []string{"quickTask"}, registry, 1, tm); err != nil {
		t.Fatalf("runPhase: %v", err)
	}

	out := buf.String()
	re := regexp.MustCompile(`Task quickTask: Duration \d{2}:\d{2}:\d{2}\.\d{3}`)
	if !re.MatchString(out) {
		t.Errorf("expected hh:mm:ss.xxx task-duration line, got:\n%s", out)
	}
}

// #333: when the injected counter records Success / Fail, runPhase
// emits one combined "task summary" line carrying counts + duration
// instead of the previous separate summary + duration pair.
func TestRunPhaseEmitsMergedSummaryWhenCounterRecorded(t *testing.T) {
	e, buf, tm := newTimingTestEnv(t)

	registry := map[string]*TaskDef{
		"countingTask": {
			Name: "countingTask",
			Run: func(ctx context.Context, _ *Executor) error {
				c := TaskCounterFromContext(ctx)
				c.Success()
				c.Success()
				c.Fail()
				return nil
			},
		},
	}

	if err := runPhase(context.Background(), e, []string{"countingTask"}, registry, 1, tm); err != nil {
		t.Fatalf("runPhase: %v", err)
	}

	out := buf.String()
	if !regexp.MustCompile(`msg="task summary"`).MatchString(out) {
		t.Errorf("expected merged task summary line, got:\n%s", out)
	}
	if !regexp.MustCompile(`task=countingTask`).MatchString(out) {
		t.Errorf("expected task=countingTask attr, got:\n%s", out)
	}
	if !regexp.MustCompile(`succeeded=2 failed=1 total=3`).MatchString(out) {
		t.Errorf("expected counts in merged line, got:\n%s", out)
	}
	if !regexp.MustCompile(`duration=\d{2}:\d{2}:\d{2}\.\d{3}`).MatchString(out) {
		t.Errorf("expected duration attr in merged line, got:\n%s", out)
	}
	// Exactly one closing log entry — no "Task ...: Duration" line.
	if regexp.MustCompile(`Task countingTask: Duration`).MatchString(out) {
		t.Errorf("merged summary should suppress the separate duration line, got:\n%s", out)
	}
}

// On failure the task still gets a closing duration line — the
// log bookend must not depend on success.
func TestRunPhaseEmitsTaskDurationLogOnFailure(t *testing.T) {
	e, buf, tm := newTimingTestEnv(t)

	registry := map[string]*TaskDef{
		"failingTask": {
			Name: "failingTask",
			Run: func(_ context.Context, _ *Executor) error {
				return context.Canceled // any non-nil error
			},
		},
	}

	// Expect runPhase to surface the error.
	if err := runPhase(context.Background(), e, []string{"failingTask"}, registry, 1, tm); err == nil {
		t.Fatalf("expected error from failing task")
	}

	out := buf.String()
	re := regexp.MustCompile(`Task failingTask: Duration \d{2}:\d{2}:\d{2}\.\d{3}`)
	if !re.MatchString(out) {
		t.Errorf("expected hh:mm:ss.xxx duration line even on failure, got:\n%s", out)
	}
}
