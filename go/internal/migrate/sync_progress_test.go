// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"
)

func progressAfter(issues, hotspots int) *syncProgress {
	p := newSyncProgress(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), 329)
	p.issuesDone.Store(int64(issues))
	p.hotspotsDone.Store(int64(hotspots))
	return p
}

// The two examples from #702.
func TestSyncProgressPercent(t *testing.T) {
	cases := []struct {
		issues, hotspots int
		want             float64
	}{
		{0, 0, 0},
		{121, 0, 18.4}, // 50 * 121/329 = 18.39
		{329, 0, 50},
		{329, 57, 58.66}, // 50 + 50 * 57/329
		{329, 329, 100},
	}
	for _, c := range cases {
		if got := progressAfter(c.issues, c.hotspots).Percent(); math.Abs(got-c.want) > 0.05 {
			t.Errorf("issues=%d hotspots=%d: want %.2f, got %.2f", c.issues, c.hotspots, c.want, got)
		}
	}
}

func TestSyncProgressNoProjectsIsComplete(t *testing.T) {
	p := newSyncProgress(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), 0)
	if p.Percent() != 100 {
		t.Errorf("want 100, got %v", p.Percent())
	}
}

func TestSyncProgressETA(t *testing.T) {
	p := progressAfter(0, 0)
	if _, ok := p.ETA(); ok {
		t.Error("ETA must be unknown before any project completes")
	}

	// 25% done after 10 minutes -> 30 minutes left.
	p = newSyncProgress(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), 4)
	p.issuesDone.Store(2)
	p.now = func() time.Time { return p.start.Add(10 * time.Minute) }
	eta, ok := p.ETA()
	if !ok || eta != 30*time.Minute {
		t.Errorf("want 30m, got %v (known=%v)", eta, ok)
	}

	p.issuesDone.Store(4)
	p.hotspotsDone.Store(4)
	if eta, ok := p.ETA(); !ok || eta != 0 {
		t.Errorf("want 0 when complete, got %v (known=%v)", eta, ok)
	}
}

func TestSyncProgressRunLogsPeriodicallyAndAtStop(t *testing.T) {
	var buf bytes.Buffer
	p := newSyncProgress(slog.New(slog.NewTextHandler(&buf, nil)), 2)
	p.IssuesDone()

	stop := p.Run(context.Background(), 10*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	stop()

	out := buf.String()
	if strings.Count(out, "sync-issues progress: 25.0% (issues 1/2 projects, hotspots 0/2 projects)") < 2 {
		t.Errorf("expected periodic and final progress lines, got:\n%s", out)
	}
	if !strings.Contains(out, "ETA ") {
		t.Errorf("expected an ETA in the line, got:\n%s", out)
	}
}
