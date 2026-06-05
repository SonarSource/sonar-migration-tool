// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// newTestLogger wires the teeing eventHandler around a TextHandler whose
// output lands in buf, capturing INFO+ records into c. Returns the logger so
// tests can exercise the real production wiring from RunMigrate.
func newTestLogger(buf *bytes.Buffer, c *eventCollector, level slog.Level) *slog.Logger {
	base := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})
	return slog.New(newEventHandler(base, c))
}

// TestEventHandler_Passthrough asserts that an Info log is both forwarded to
// the inner TextHandler (visible in the buffer) AND captured exactly once by
// the collector.
func TestEventHandler_Passthrough(t *testing.T) {
	var buf bytes.Buffer
	c := &eventCollector{}
	logger := newTestLogger(&buf, c, slog.LevelInfo)

	logger.Info("hello world", "k", "v")

	// Passthrough: the inner TextHandler must have emitted the line.
	out := buf.String()
	if !strings.Contains(out, "hello world") {
		t.Errorf("inner handler output missing message: %q", out)
	}
	if !strings.Contains(out, "k=v") {
		t.Errorf("inner handler output missing attr: %q", out)
	}

	// Capture: exactly one LogEvent collected.
	events := c.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 captured event, got %d: %+v", len(events), events)
	}
	e := events[0]
	if e.Message != "hello world" {
		t.Errorf("message: got %q, want %q", e.Message, "hello world")
	}
	if e.Level != slog.LevelInfo.String() {
		t.Errorf("level: got %q, want %q", e.Level, slog.LevelInfo.String())
	}
	if got := e.Attrs["k"]; got != "v" {
		t.Errorf("attr k: got %v, want %q", got, "v")
	}
}

// TestEventHandler_LevelFilterParity asserts that at LevelInfo a Debug record
// is captured by neither the inner handler nor the collector — the teeing
// handler delegates Enabled() to the inner handler, so the filter is shared.
func TestEventHandler_LevelFilterParity(t *testing.T) {
	var buf bytes.Buffer
	c := &eventCollector{}
	logger := newTestLogger(&buf, c, slog.LevelInfo)

	logger.Debug("this should be dropped", "k", "v")

	if out := buf.String(); strings.Contains(out, "this should be dropped") {
		t.Errorf("inner handler captured a Debug record at LevelInfo: %q", out)
	}
	if events := c.snapshot(); len(events) != 0 {
		t.Errorf("collector captured a Debug record at LevelInfo: %+v", events)
	}

	// Sanity: an Info record at the same level is captured by both.
	logger.Info("kept")
	if out := buf.String(); !strings.Contains(out, "kept") {
		t.Errorf("inner handler dropped an Info record at LevelInfo: %q", out)
	}
	if events := c.snapshot(); len(events) != 1 {
		t.Errorf("collector should hold exactly the 1 Info record, got %+v", events)
	}
}

// TestEventHandler_Concurrency launches 200 goroutines that each log 50 unique
// messages through the same logger. After the WaitGroup join the collector
// must hold exactly 200*50 events, with the full expected set present and no
// torn data. Run under `go test -race` to catch data races on the collector.
func TestEventHandler_Concurrency(t *testing.T) {
	const (
		goroutines    = 200
		perGoroutine  = 50
		expectedTotal = goroutines * perGoroutine
	)

	var buf bytes.Buffer
	c := &eventCollector{}
	logger := newTestLogger(&buf, c, slog.LevelInfo)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				logger.Info("concurrent", "g", g, "i", i)
			}
		}(g)
	}
	wg.Wait()

	events := c.snapshot()
	if len(events) != expectedTotal {
		t.Fatalf("event count: got %d, want %d (drops or duplicates)", len(events), expectedTotal)
	}

	// Build the full expected set of (g,i) pairs and verify exact coverage
	// with no torn attr data.
	seen := make(map[string]int, expectedTotal)
	for _, e := range events {
		if e.Message != "concurrent" {
			t.Fatalf("torn message: got %q, want %q", e.Message, "concurrent")
		}
		// Numeric attrs are stored as their original int64 here (the JSON
		// float64 conversion only happens on decode), so format them
		// directly into a stable key.
		key := fmt.Sprintf("%v:%v", e.Attrs["g"], e.Attrs["i"])
		seen[key]++
	}
	if len(seen) != expectedTotal {
		t.Fatalf("unique (g,i) pairs: got %d, want %d", len(seen), expectedTotal)
	}
	for g := 0; g < goroutines; g++ {
		for i := 0; i < perGoroutine; i++ {
			key := fmt.Sprintf("%d:%d", g, i)
			switch n := seen[key]; {
			case n == 0:
				t.Fatalf("missing event for (g=%d,i=%d)", g, i)
			case n > 1:
				t.Fatalf("duplicate event for (g=%d,i=%d): seen %d times", g, i, n)
			}
		}
	}
}

// TestEventHandler_WithAttrsTees asserts that a derived logger (via With /
// WithAttrs) still tees into the same collector and that the bound attrs
// appear on the captured events.
func TestEventHandler_WithAttrsTees(t *testing.T) {
	var buf bytes.Buffer
	c := &eventCollector{}
	logger := newTestLogger(&buf, c, slog.LevelInfo)

	derived := logger.With("k", "v")
	derived.Info("derived message", "extra", "payload")

	// Inner handler still gets the bound + per-call attrs.
	out := buf.String()
	if !strings.Contains(out, "k=v") {
		t.Errorf("inner handler missing bound attr k=v: %q", out)
	}
	if !strings.Contains(out, "extra=payload") {
		t.Errorf("inner handler missing per-call attr: %q", out)
	}

	events := c.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 captured event, got %d: %+v", len(events), events)
	}
	e := events[0]
	if e.Message != "derived message" {
		t.Errorf("message: got %q, want %q", e.Message, "derived message")
	}
	if got := e.Attrs["k"]; got != "v" {
		t.Errorf("bound attr k: got %v, want %q", got, "v")
	}
	if got := e.Attrs["extra"]; got != "payload" {
		t.Errorf("per-call attr extra: got %v, want %q", got, "payload")
	}

	// A logger with a WithGroup still tees, and per-call attrs land under the
	// dotted group path on the captured event (matching prefixGroups). Using a
	// per-call attr (rather than a chained With) keeps the assertion on the
	// single, unambiguous group prefix that flattenAttr applies at Handle time.
	grouped := logger.WithGroup("grp")
	grouped.Info("grouped message", "nested", "x")
	events = c.snapshot()
	if len(events) != 2 {
		t.Fatalf("expected 2 captured events after group log, got %d", len(events))
	}
	last := events[1]
	if got := last.Attrs["grp.nested"]; got != "x" {
		t.Errorf("grouped attr grp.nested: got %v, want %q (attrs=%+v)", got, "x", last.Attrs)
	}
}
