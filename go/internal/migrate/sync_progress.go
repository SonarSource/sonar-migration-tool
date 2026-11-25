// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// syncProgressInterval is how often the standalone sync-issues command logs
// its run-wide progress (#702).
const syncProgressInterval = 30 * time.Second

// syncProgress tracks the run-wide progress of the standalone sync-issues
// command across ALL projects (#702), which the per-project and per-batch
// progress lines cannot show.
//
// The run is two phases over the same project list: every project's issues
// first, then every project's hotspots. Each phase is worth half of the
// total, so with N projects:
//
//	percent = 50 * issuesDone/N + 50 * hotspotsDone/N
//
// (121 issue-synced projects of 329 and no hotspots started is 18.3%;
// all 329 issue-synced plus 57 hotspot-synced is 58.6%.)
//
// The ETA extrapolates from the time the completed work took: with fraction f
// done after elapsed time t, the remaining time is t * (1-f) / f. It is
// unknown until some work has completed.
type syncProgress struct {
	logger       *slog.Logger
	total        int
	issuesDone   atomic.Int64
	hotspotsDone atomic.Int64
	start        time.Time
	now          func() time.Time
}

func newSyncProgress(logger *slog.Logger, totalProjects int) *syncProgress {
	return &syncProgress{logger: logger, total: totalProjects, start: time.Now(), now: time.Now}
}

// IssuesDone records that one project's issue sync has completed.
func (p *syncProgress) IssuesDone() { p.issuesDone.Add(1) }

// HotspotsDone records that one project's hotspot sync has completed.
func (p *syncProgress) HotspotsDone() { p.hotspotsDone.Add(1) }

// Percent is the overall completion, in [0, 100].
func (p *syncProgress) Percent() float64 {
	if p.total == 0 {
		return 100
	}
	n := float64(p.total)
	return 50*float64(p.issuesDone.Load())/n + 50*float64(p.hotspotsDone.Load())/n
}

// ETA is the estimated time left, and false while it cannot be estimated.
func (p *syncProgress) ETA() (time.Duration, bool) {
	pct := p.Percent()
	if pct <= 0 {
		return 0, false
	}
	if pct >= 100 {
		return 0, true
	}
	elapsed := p.now().Sub(p.start)
	return time.Duration(float64(elapsed) * (100 - pct) / pct), true
}

// Log writes one progress line.
func (p *syncProgress) Log() {
	eta := "unknown"
	if d, ok := p.ETA(); ok {
		eta = d.Round(time.Second).String()
	}
	p.logger.Info(fmt.Sprintf("sync-issues progress: %.1f%% (issues %d/%d projects, hotspots %d/%d projects), ETA %s",
		p.Percent(), p.issuesDone.Load(), p.total, p.hotspotsDone.Load(), p.total, eta),
		"elapsed", p.now().Sub(p.start).Round(time.Second).String())
}

// Run logs progress every interval until ctx is done or the returned stop
// function is called, then writes one final line.
func (p *syncProgress) Run(ctx context.Context, interval time.Duration) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				p.Log()
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		p.Log()
	}
}
