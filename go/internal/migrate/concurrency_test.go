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

func TestConcurrencyLimiterLogsDebugOnTickWithSamples(t *testing.T) {
	buf := &syncBuffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
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
		t.Fatalf("expected DEBUG log not found in output: %s", buf.String())
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
