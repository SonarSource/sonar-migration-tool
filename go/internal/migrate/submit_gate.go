// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// submitGate enforces a minimum wall-clock interval between consecutive
// scanner-report submissions to the SonarCloud Compute Engine.
//
// Issue #417: when a full migrate submits many analyses concurrently to the
// same SonarCloud organization, the CE can report task SUCCESS for an analysis
// whose source it never persisted — the Code view then shows empty source even
// though the report carried it. A single project analyzed in isolation (the
// `transfer` command) never hits this. Serializing the submit calls with a
// minimum gap keeps only a small number of analyses in the CE pipeline at once
// and avoids the race.
//
// The gate is safe for concurrent use: importProjectData fans out across
// projects, so multiple goroutines call wait() simultaneously. They queue on
// the mutex and are released one at a time, each no sooner than minInterval
// after the previous submit. A nil gate or a non-positive interval disables
// throttling (the wait() call then returns immediately).
type submitGate struct {
	mu          sync.Mutex
	minInterval time.Duration
	last        time.Time
}

// newSubmitGate returns a gate enforcing minInterval between submits. A
// non-positive interval yields a gate that never blocks.
func newSubmitGate(minInterval time.Duration) *submitGate {
	return &submitGate{minInterval: minInterval}
}

// wait blocks until at least minInterval has elapsed since the previous
// submit, then records the current time as the new submit instant. It returns
// ctx.Err() if the context is cancelled while waiting. The mutex is held for
// the duration of the wait so that submits are strictly serialized; callers
// poll the CE task to completion afterwards (outside the lock), so the lock is
// only ever held for the spacing delay, not for the full analysis.
func (g *submitGate) wait(ctx context.Context, logger *slog.Logger) error {
	if g == nil || g.minInterval <= 0 {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.last.IsZero() {
		if d := g.minInterval - time.Since(g.last); d > 0 {
			if logger != nil {
				logger.Debug("throttling CE submit", "wait", d.String())
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
			}
		}
	}
	g.last = time.Now()
	return nil
}
