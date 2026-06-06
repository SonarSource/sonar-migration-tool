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

// Issue #311: runPhase must emit a "Task <name>: Duration hh:mm:ss.xxx"
// INFO line at the end of every task, on both the success and failure
// paths.
func TestRunPhaseEmitsTaskDurationLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	registry := map[string]*TaskDef{
		"quickTask": {
			Name: "quickTask",
			Run: func(_ context.Context, _ *Executor) error {
				return nil
			},
		},
	}

	e := &Executor{
		Sem:    make(chan struct{}, 4),
		Logger: logger,
	}
	tm := &RunTimings{}

	if err := runPhase(context.Background(), e, []string{"quickTask"}, registry, 1, tm); err != nil {
		t.Fatalf("runPhase: %v", err)
	}

	out := buf.String()
	re := regexp.MustCompile(`Task quickTask: Duration \d{2}:\d{2}:\d{2}\.\d{3}`)
	if !re.MatchString(out) {
		t.Errorf("expected hh:mm:ss.xxx task-duration line, got:\n%s", out)
	}
}

// On failure the task still gets a closing duration line — the
// log bookend must not depend on success.
func TestRunPhaseEmitsTaskDurationLogOnFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	registry := map[string]*TaskDef{
		"failingTask": {
			Name: "failingTask",
			Run: func(_ context.Context, _ *Executor) error {
				return context.Canceled // any non-nil error
			},
		},
	}

	e := &Executor{
		Sem:    make(chan struct{}, 4),
		Logger: logger,
	}
	tm := &RunTimings{}

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
