// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe for concurrent writes (from the
// limiter's background recalculation goroutine) and reads (from the test
// goroutine's polling loop).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestDesiredConcurrency(t *testing.T) {
	tests := []struct {
		name             string
		targetRatePerMin int
		avgLatency       time.Duration
		want             int
	}{
		{
			name:             "issue example: 200ms latency at 1500/min",
			targetRatePerMin: 1500,
			avgLatency:       200 * time.Millisecond,
			want:             5,
		},
		{
			name:             "issue example: 2s latency at 1500/min",
			targetRatePerMin: 1500,
			avgLatency:       2 * time.Second,
			want:             50,
		},
		{
			name:             "ceiling edge: extremely high latency clamps to max",
			targetRatePerMin: 1500,
			avgLatency:       10 * time.Minute,
			want:             maxConcurrencyCeiling,
		},
		{
			name:             "floor edge: extremely low latency and low target clamps to min",
			targetRatePerMin: 1,
			avgLatency:       1 * time.Millisecond,
			want:             minConcurrency,
		},
		{
			name:             "floor edge: zero latency clamps to min",
			targetRatePerMin: 1500,
			avgLatency:       0,
			want:             minConcurrency,
		},
		{
			name:             "ceiling rounds up: just over a whole number",
			targetRatePerMin: 60,
			avgLatency:       1001 * time.Millisecond,
			want:             2, // 60*1.001/60 = 1.001 -> ceil -> 2
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := desiredConcurrency(tt.targetRatePerMin, tt.avgLatency)
			if got != tt.want {
				t.Errorf("desiredConcurrency(%d, %v) = %d, want %d", tt.targetRatePerMin, tt.avgLatency, got, tt.want)
			}
		})
	}
}

func TestClampConcurrency(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want int
	}{
		{"below floor clamps to min", -5, minConcurrency},
		{"zero clamps to min", 0, minConcurrency},
		{"exactly min stays min", minConcurrency, minConcurrency},
		{"mid-range value unchanged", 42, 42},
		{"exactly ceiling stays ceiling", maxConcurrencyCeiling, maxConcurrencyCeiling},
		{"above ceiling clamps to max", maxConcurrencyCeiling + 1000, maxConcurrencyCeiling},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clampConcurrency(tt.n)
			if got != tt.want {
				t.Errorf("clampConcurrency(%d) = %d, want %d", tt.n, got, tt.want)
			}
		})
	}
}

func TestLatencyWindow(t *testing.T) {
	w := &latencyWindow{}

	// No samples yet: drain reports ok=false.
	if avg, ok := w.drainAverage(); ok {
		t.Fatalf("drainAverage on empty window = (%v, %v), want ok=false", avg, ok)
	}

	w.add(100 * time.Millisecond)
	w.add(200 * time.Millisecond)
	w.add(300 * time.Millisecond)

	avg, ok := w.drainAverage()
	if !ok {
		t.Fatalf("drainAverage after adding samples returned ok=false")
	}
	wantAvg := 200 * time.Millisecond
	if avg != wantAvg {
		t.Errorf("drainAverage() = %v, want %v", avg, wantAvg)
	}

	// Second drain with no new samples returns ok=false (accumulator reset).
	if avg, ok := w.drainAverage(); ok {
		t.Fatalf("second drainAverage() = (%v, %v), want ok=false after reset", avg, ok)
	}
}

// pollUntil retries fn until it returns true or the deadline elapses,
// avoiding a fixed-sleep race when waiting for a background goroutine's
// side effect to become visible.
func pollUntil(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fn()
}

func TestConcurrencyLimiterDynamicConvergesToDesiredValue(t *testing.T) {
	targetRatePerMin := 1500
	avgLatency := 2 * time.Second
	want := desiredConcurrency(targetRatePerMin, avgLatency)

	l := NewDynamicConcurrencyLimiter(1, targetRatePerMin, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	interval := 15 * time.Millisecond
	l.Start(ctx, interval)
	defer l.Stop()

	// Feed samples averaging to avgLatency on every tick so the limiter
	// keeps converging even if a tick races the observations below.
	stopFeeding := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stopFeeding:
				return
			case <-ticker.C:
				l.Observe(avgLatency)
			}
		}
	}()

	ok := pollUntil(t, 2*time.Second, func() bool {
		return l.Current() == want
	})
	close(stopFeeding)

	if !ok {
		t.Fatalf("Current() = %d after waiting, want %d", l.Current(), want)
	}

	l.Stop() // Stop must be safe to call twice, and not panic/hang.
}

func TestConcurrencyLimiterFixedIsANoOp(t *testing.T) {
	l := NewFixedConcurrencyLimiter(7)

	if got := l.Current(); got != 7 {
		t.Fatalf("Current() = %d, want 7", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	interval := 5 * time.Millisecond
	l.Start(ctx, interval)

	// Feed observations and wait past several tick intervals: Current()
	// must never change, and no goroutine should have been started.
	for i := 0; i < 20; i++ {
		l.Observe(999 * time.Second)
	}
	time.Sleep(interval * 10)

	if got := l.Current(); got != 7 {
		t.Fatalf("Current() = %d after observations/ticks, want unchanged 7", got)
	}

	// Stop on a fixed (never-really-started) limiter must not block or panic.
	done := make(chan struct{})
	go func() {
		l.Stop()
		l.Stop() // idempotent
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() on fixed limiter blocked")
	}
}

func TestConcurrencyLimiterLogsInfoOnTickWithSamples(t *testing.T) {
	buf := &syncBuffer{}
	// LevelInfo (not LevelDebug): the recalculation log must be visible
	// without --debug, since operators watching a live migration need it.
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(handler)

	targetRatePerMin := 1500
	avgLatency := 2 * time.Second
	want := desiredConcurrency(targetRatePerMin, avgLatency)

	l := NewDynamicConcurrencyLimiter(1, targetRatePerMin, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l.Observe(avgLatency)

	interval := 10 * time.Millisecond
	l.Start(ctx, interval)
	defer l.Stop()

	ok := pollUntil(t, 2*time.Second, func() bool {
		return strings.Contains(buf.String(), "concurrency recalculated")
	})
	if !ok {
		t.Fatalf("expected INFO log not found in output: %s", buf.String())
	}

	logged := buf.String()
	if !strings.Contains(logged, "concurrency recalculated") {
		t.Errorf("log missing message, got: %s", logged)
	}
	if !strings.Contains(logged, "avg_latency_ms=2000") {
		t.Errorf("log missing avg_latency_ms=2000, got: %s", logged)
	}
	if !strings.Contains(logged, "target_rate_per_min=1500") {
		t.Errorf("log missing target_rate_per_min=1500, got: %s", logged)
	}
	if !strings.Contains(logged, "current="+strconv.Itoa(want)) {
		t.Errorf("log missing current=%d, got: %s", want, logged)
	}
}

func TestConcurrencyLimiterSkipsRecalculationWithNoSamples(t *testing.T) {
	initial := 3
	l := NewDynamicConcurrencyLimiter(initial, 1500, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	interval := 10 * time.Millisecond
	l.Start(ctx, interval)
	defer l.Stop()

	// No Observe calls: let several ticks elapse and confirm Current()
	// never moves away from the clamped initial value.
	time.Sleep(interval * 8)

	if got := l.Current(); got != clampConcurrency(initial) {
		t.Fatalf("Current() = %d after ticks with no samples, want unchanged %d", got, clampConcurrency(initial))
	}
}

// #573 — a live migrate run showed a 50-minute importProjectData task
// stayed locked at whatever ConcurrencyLimiter.Current() was the instant
// its errgroup called SetLimit, even though the background recalculation
// kept computing new values every 30s the entire time: errgroup's
// documented contract forbids changing SetLimit after any Go() call.
// DynamicGate exists to re-read Current() on every admission instead.
// These tests guard against that regression returning.

func TestDynamicGateNeverExceedsCurrentLimit(t *testing.T) {
	limiter := NewFixedConcurrencyLimiter(2)
	gate := NewDynamicGate(limiter)
	ctx := context.Background()

	if err := gate.Acquire(ctx); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := gate.Acquire(ctx); err != nil {
		t.Fatalf("second Acquire: %v", err)
	}

	// At the limit: a third Acquire must block.
	thirdDone := make(chan error, 1)
	go func() { thirdDone <- gate.Acquire(ctx) }()
	select {
	case err := <-thirdDone:
		t.Fatalf("third Acquire returned (err=%v) while already at the limit", err)
	case <-time.After(100 * time.Millisecond):
	}

	gate.Release()
	select {
	case err := <-thirdDone:
		if err != nil {
			t.Fatalf("third Acquire after a Release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("third Acquire did not succeed after a Release freed a slot")
	}
	gate.Release()
}

// TestDynamicGateRespondsToLiveLimitChange is the core #573 regression
// guard: a gate built on a limiter whose value changes mid-flight must
// admit more work as soon as the limit rises, without needing to be
// reconstructed — exactly what errgroup.SetLimit cannot do once Go() has
// been called.
func TestDynamicGateRespondsToLiveLimitChange(t *testing.T) {
	limiter := NewFixedConcurrencyLimiter(1)
	gate := NewDynamicGate(limiter)
	ctx := context.Background()

	if err := gate.Acquire(ctx); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- gate.Acquire(ctx) }()
	select {
	case err := <-secondDone:
		t.Fatalf("second Acquire returned (err=%v) before the limit was raised", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Raise the limit live — no Release, no new gate, no restart of
	// whatever fan-out is using it.
	limiter.current.Store(2)

	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second Acquire after the limit was raised: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire did not succeed after the live limit was raised to 2")
	}
}

func TestDynamicGateAcquireRespectsContextCancellation(t *testing.T) {
	limiter := NewFixedConcurrencyLimiter(0) // never admits
	gate := NewDynamicGate(limiter)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := gate.Acquire(ctx); err == nil {
		t.Fatal("expected Acquire to return an error for an already-canceled context")
	}
}
