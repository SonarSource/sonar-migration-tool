// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// shortenIndexingRetryTiming sets the package-level retry timing to
// near-zero so tests don't burn real seconds in the backoff. Cleanup
// restores the production values.
func shortenIndexingRetryTiming(t *testing.T) {
	t.Helper()
	origInitial := projectIndexingRetryInitialDelay
	origMax := projectIndexingRetryMaxDelay
	origAttempts := projectIndexingRetryMaxAttempts
	projectIndexingRetryInitialDelay = 1 * time.Millisecond
	projectIndexingRetryMaxDelay = 4 * time.Millisecond
	projectIndexingRetryMaxAttempts = 5
	t.Cleanup(func() {
		projectIndexingRetryInitialDelay = origInitial
		projectIndexingRetryMaxDelay = origMax
		projectIndexingRetryMaxAttempts = origAttempts
	})
}

func projectNotFoundErr() error {
	return &sqapi.APIError{
		StatusCode: 404,
		Method:     "POST",
		URL:        "https://sonarcloud.io/api/settings/set",
		Body:       `{"errors":[{"msg":"Project doesn't exist"}]}`,
	}
}

func TestIsProjectNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"404 with project-doesn't-exist message", projectNotFoundErr(), true},
		{
			"404 with unrelated message",
			&sqapi.APIError{StatusCode: 404, Body: `{"errors":[{"msg":"Component not found"}]}`},
			false,
		},
		{
			"500 with project-doesn't-exist message — not a 404",
			&sqapi.APIError{StatusCode: 500, Body: `{"errors":[{"msg":"Project doesn't exist"}]}`},
			false,
		},
		{
			"403 forbidden",
			&sqapi.APIError{StatusCode: 403, Body: `{"errors":[{"msg":"Insufficient privileges"}]}`},
			false,
		},
		{
			"404 message capitalization variant",
			&sqapi.APIError{StatusCode: 404, Body: `{"errors":[{"msg":"PROJECT DOESN'T EXIST"}]}`},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isProjectNotFound(c.err); got != c.want {
				t.Errorf("isProjectNotFound(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestRetryOnProjectNotFoundImmediate(t *testing.T) {
	shortenIndexingRetryTiming(t)
	calls := 0
	err := retryOnProjectNotFound(context.Background(), nil, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Errorf("expected nil err, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call, got %d", calls)
	}
}

func TestRetryOnProjectNotFoundAfterTransientNotFound(t *testing.T) {
	shortenIndexingRetryTiming(t)
	calls := 0
	err := retryOnProjectNotFound(context.Background(), nil, func() error {
		calls++
		if calls < 3 {
			return projectNotFoundErr()
		}
		return nil
	})
	if err != nil {
		t.Errorf("expected nil err after retries, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected exactly 3 calls, got %d", calls)
	}
}

func TestRetryOnProjectNotFoundNon404PassesThrough(t *testing.T) {
	shortenIndexingRetryTiming(t)
	calls := 0
	nonRetryable := errors.New("nope")
	err := retryOnProjectNotFound(context.Background(), nil, func() error {
		calls++
		return nonRetryable
	})
	if !errors.Is(err, nonRetryable) {
		t.Errorf("expected non-retryable error to surface unchanged, got %v", err)
	}
	if calls != 1 {
		t.Errorf("non-retryable error must not retry, got %d calls", calls)
	}
}

func TestRetryOnProjectNotFoundExhaustion(t *testing.T) {
	shortenIndexingRetryTiming(t)
	calls := 0
	err := retryOnProjectNotFound(context.Background(), nil, func() error {
		calls++
		return projectNotFoundErr()
	})
	if !isProjectNotFound(err) {
		t.Errorf("expected the last 404 error to surface, got %v", err)
	}
	if calls != projectIndexingRetryMaxAttempts {
		t.Errorf("expected exactly %d calls, got %d", projectIndexingRetryMaxAttempts, calls)
	}
}

func TestRetryOnProjectNotFoundCtxCancellation(t *testing.T) {
	shortenIndexingRetryTiming(t)
	// Slow the schedule a bit so the cancel actually fires before
	// the next attempt.
	projectIndexingRetryInitialDelay = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	calls := 0
	err := retryOnProjectNotFound(ctx, nil, func() error {
		calls++
		return projectNotFoundErr()
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if calls >= projectIndexingRetryMaxAttempts {
		t.Errorf("ctx cancel should have stopped early, got %d calls", calls)
	}
}

// Each scheduled retry must emit a Debug line so operators running
// with --debug can see the indexing-lag recovery in flight (#334).
func TestRetryOnProjectNotFoundEmitsDebugLog(t *testing.T) {
	shortenIndexingRetryTiming(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	calls := 0
	err := retryOnProjectNotFound(context.Background(), logger, func() error {
		calls++
		if calls < 3 {
			return projectNotFoundErr()
		}
		return nil
	}, "endpoint", "/api/settings/set", "project", "org1_projA")

	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	out := buf.String()
	// Two retries (calls 1 and 2 both returned 404, call 3 succeeded)
	// → two Debug lines.
	if got := strings.Count(out, "SQC project not yet indexed"); got != 2 {
		t.Errorf("expected exactly 2 retry-debug lines, got %d in:\n%s", got, out)
	}
	// Each line carries the caller-supplied context plus the per-
	// attempt fields the helper appends.
	for _, want := range []string{
		`attempt=1`,
		`attempt=2`,
		`endpoint=/api/settings/set`,
		`project=org1_projA`,
		`retry_in=`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected log to contain %q, got:\n%s", want, out)
		}
	}
}

// A nil logger must keep working without panicking — the helper is
// usable from contexts that don't have a logger handy (e.g., direct
// unit tests of internal call sites).
func TestRetryOnProjectNotFoundNilLogger(t *testing.T) {
	shortenIndexingRetryTiming(t)
	calls := 0
	err := retryOnProjectNotFound(context.Background(), nil, func() error {
		calls++
		if calls < 2 {
			return projectNotFoundErr()
		}
		return nil
	}, "project", "anything")
	if err != nil {
		t.Errorf("nil logger should not affect retry success: got err %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls with nil logger, got %d", calls)
	}
}

// Production timing must stay generous enough to absorb the empirical
// 30s+ SQC indexing-lag window observed in #334. Lock it in so a
// future tweak that drops below ~20s shows up in code review.
func TestRetryBudgetCoversIndexingWindow(t *testing.T) {
	if projectIndexingRetryMaxAttempts < 6 {
		t.Errorf("expected >=6 attempts to span the SQC indexing window, got %d", projectIndexingRetryMaxAttempts)
	}
	// Compute the upper bound of the cumulative wait: each delay is
	// doubled up to maxDelay, summed over (maxAttempts-1) sleeps.
	delay := projectIndexingRetryInitialDelay
	var total time.Duration
	for i := 0; i < projectIndexingRetryMaxAttempts-1; i++ {
		total += delay
		delay *= 2
		if delay > projectIndexingRetryMaxDelay {
			delay = projectIndexingRetryMaxDelay
		}
	}
	if total < 20*time.Second {
		t.Errorf("retry budget %s is too short for SQC's >=30s indexing window", total)
	}
}
