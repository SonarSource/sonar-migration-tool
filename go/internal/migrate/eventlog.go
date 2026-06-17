// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LogEvent is a single captured slog record, serialized one-per-line into
// run_events.jsonl.
type LogEvent struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// PhaseTiming records how long a single execution phase took and how many
// tasks it contained.
type PhaseTiming struct {
	Index    int     `json:"index"`
	Tasks    int     `json:"tasks"`
	Duration float64 `json:"duration_seconds"`
}

// TaskTiming records the per-task outcome and wall-clock duration within a
// phase.
type TaskTiming struct {
	Phase    int     `json:"phase"`
	Name     string  `json:"name"`
	Duration float64 `json:"duration_seconds"`
	OK       bool    `json:"ok"`
	Err      string  `json:"err,omitempty"`
}

// RunMeta is the single-object summary written to run_meta.json.
type RunMeta struct {
	StartedAt     time.Time     `json:"started_at"`
	CompletedAt   time.Time     `json:"completed_at"`
	OverallStatus string        `json:"overall_status"`
	Phases        []PhaseTiming `json:"phases"`
	Tasks         []TaskTiming  `json:"tasks"`
	// ProjectKeyPattern records the target-key renaming pattern (issue #138)
	// so the report can re-derive every project's target key and surface
	// collisions / over-length keys.
	ProjectKeyPattern string `json:"project_key_pattern,omitempty"`
}

// eventCollector accumulates LogEvents from the teeing slog handler. Safe for
// concurrent use across the parallel task goroutines.
type eventCollector struct {
	mu     sync.Mutex
	events []LogEvent
}

// add appends an event under the lock.
func (c *eventCollector) add(e LogEvent) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

// snapshot returns a copy of the collected events, safe to read without
// further locking.
func (c *eventCollector) snapshot() []LogEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]LogEvent, len(c.events))
	copy(out, c.events)
	return out
}

// eventHandler is a teeing slog.Handler: it captures INFO+ records into a
// shared collector while always delegating to an inner handler so stderr
// output is unchanged.
type eventHandler struct {
	inner     slog.Handler
	collector *eventCollector
	attrs     []slog.Attr
	groups    []string
}

// newEventHandler wraps inner so that records are tee'd into c.
func newEventHandler(inner slog.Handler, c *eventCollector) *eventHandler {
	return &eventHandler{inner: inner, collector: c}
}

// Enabled delegates to the inner handler.
func (h *eventHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle captures the record (INFO+ only) into the shared collector, then
// always forwards to the inner handler so stderr output is preserved.
func (h *eventHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelInfo {
		m := make(map[string]any)
		for _, a := range h.attrs {
			flattenAttr(h.groups, a, m)
		}
		r.Attrs(func(a slog.Attr) bool {
			flattenAttr(h.groups, a, m)
			return true
		})
		if len(m) == 0 {
			m = nil
		}
		h.collector.add(LogEvent{
			Time:    r.Time,
			Level:   r.Level.String(),
			Message: r.Message,
			Attrs:   m,
		})
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs returns a copy-on-write handler with the additional attrs, also
// recording them (group-prefixed) so they appear on captured events.
func (h *eventHandler) WithAttrs(as []slog.Attr) slog.Handler {
	nc := *h
	nc.inner = h.inner.WithAttrs(as)
	prefixed := make([]slog.Attr, 0, len(h.attrs)+len(as))
	prefixed = append(prefixed, h.attrs...)
	for _, a := range as {
		prefixed = append(prefixed, slog.Attr{Key: prefixGroups(h.groups, a.Key), Value: a.Value})
	}
	nc.attrs = prefixed
	return &nc
}

// WithGroup returns a copy-on-write handler that nests subsequent attrs under
// name. An empty name is a no-op per the slog.Handler contract.
func (h *eventHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nc := *h
	nc.inner = h.inner.WithGroup(name)
	groups := make([]string, 0, len(h.groups)+1)
	groups = append(groups, h.groups...)
	groups = append(groups, name)
	nc.groups = groups
	return &nc
}

// flattenAttr writes a single slog.Attr into m, resolving any LogValuer,
// prefixing the key with the dotted group path when groups is non-empty.
func flattenAttr(groups []string, a slog.Attr, m map[string]any) {
	a.Value = a.Value.Resolve()
	m[prefixGroups(groups, a.Key)] = a.Value.Any()
}

// prefixGroups joins the group path and the key with dots, e.g.
// groups=["a","b"], key="c" => "a.b.c". With no groups it returns key.
func prefixGroups(groups []string, key string) string {
	if len(groups) == 0 {
		return key
	}
	return strings.Join(groups, ".") + "." + key
}

// RunTimings accumulates phase- and task-level timing during a run. Safe for
// concurrent use.
type RunTimings struct {
	mu          sync.Mutex
	StartedAt   time.Time
	CompletedAt time.Time
	phases      []PhaseTiming
	tasks       []TaskTiming
}

// addPhase records a completed phase timing.
func (t *RunTimings) addPhase(p PhaseTiming) {
	t.mu.Lock()
	t.phases = append(t.phases, p)
	t.mu.Unlock()
}

// addTask records a completed task timing.
func (t *RunTimings) addTask(tt TaskTiming) {
	t.mu.Lock()
	t.tasks = append(t.tasks, tt)
	t.mu.Unlock()
}

// phasesSnapshot returns a copy of the recorded phase timings.
func (t *RunTimings) phasesSnapshot() []PhaseTiming {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]PhaseTiming, len(t.phases))
	copy(out, t.phases)
	return out
}

// tasksSnapshot returns a copy of the recorded task timings.
func (t *RunTimings) tasksSnapshot() []TaskTiming {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TaskTiming, len(t.tasks))
	copy(out, t.tasks)
	return out
}

// writeRunEvents encodes the collector's events to <runDir>/run_events.jsonl,
// one JSON object per line.
func writeRunEvents(runDir string, c *eventCollector) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range c.snapshot() {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(runDir, "run_events.jsonl"), buf.Bytes(), 0o644)
}

// computeStatus maps the run's terminal error and recorded task outcomes to an
// overall status string: "success" when retErr is nil, "partial" when at least
// one task succeeded, otherwise "failed".
func computeStatus(retErr error, tm *RunTimings) string {
	if retErr == nil {
		return "success"
	}
	for _, t := range tm.tasksSnapshot() {
		if t.OK {
			return "partial"
		}
	}
	return "failed"
}

// errString returns the error message, or "" when err is nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
