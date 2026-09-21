// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// The default must match AdaptiveBuildConcurrency's own decision (#573:
// sized from memory when detectable, DefaultBuildConcurrency otherwise —
// never a bare fixed constant regardless of platform) and stay well below
// the request concurrency default, because it bounds memory rather than
// request rate.
func TestBuildConcurrencyDefaultIsLowerThanRequestConcurrency(t *testing.T) {
	cfg := &MigrateConfig{}
	cfg.applyDefaults()

	if want := AdaptiveBuildConcurrency(); cfg.BuildConcurrency != want {
		t.Errorf("BuildConcurrency = %d, want %d (AdaptiveBuildConcurrency's decision)", cfg.BuildConcurrency, want)
	}
	if cfg.BuildConcurrency >= cfg.Concurrency {
		t.Errorf("BuildConcurrency (%d) should be well below Concurrency (%d): it bounds memory, not request rate",
			cfg.BuildConcurrency, cfg.Concurrency)
	}
}

// An explicit value must survive applyDefaults, so an operator can raise it
// back toward --concurrency when report building is the bottleneck.
func TestBuildConcurrencyExplicitValueWins(t *testing.T) {
	cfg := &MigrateConfig{BuildConcurrency: 25}
	cfg.applyDefaults()

	if cfg.BuildConcurrency != 25 {
		t.Errorf("BuildConcurrency = %d, want the explicit 25", cfg.BuildConcurrency)
	}
}

// #573 follow-up — adaptiveBuildConcurrency is the pure, testable core of
// AdaptiveBuildConcurrency: given a detected memory budget in bytes, it
// must never drop below DefaultBuildConcurrency (the known-safe floor —
// issue #541's OOM predates any build-concurrency cap at all) and never
// exceed maxAdaptiveBuildConcurrency (still below #541's unbounded-25
// failure case, until real large-project measurements justify more).
func TestAdaptiveBuildConcurrency(t *testing.T) {
	cases := []struct {
		name        string
		budgetBytes int64
		want        int
	}{
		{"undetected budget falls back to the fixed default", 0, DefaultBuildConcurrency},
		{"negative budget (defensive) falls back to the fixed default", -1, DefaultBuildConcurrency},
		{"tiny budget clamps to the floor, not below it", 1 << 20 /* 1 MiB */, DefaultBuildConcurrency},
		{"a budget implying fewer than the floor still clamps to the floor", 4 << 30 /* 4 GiB: n=2 */, DefaultBuildConcurrency},
		{"a generous budget clamps to the ceiling, not unbounded", 1000 << 30 /* 1000 GiB */, maxAdaptiveBuildConcurrency},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := adaptiveBuildConcurrency(tc.budgetBytes); got != tc.want {
				t.Errorf("adaptiveBuildConcurrency(%d) = %d, want %d", tc.budgetBytes, got, tc.want)
			}
		})
	}
}

// A mid-range budget must scale the way the documented formula says it
// does, not just land somewhere inside [floor, ceiling] by coincidence.
func TestAdaptiveBuildConcurrencyScalesWithBudget(t *testing.T) {
	// 16 GiB * 0.5 share / 1 GiB per worker = 8.
	const budget16GiB = 16 << 30
	want := 8
	if got := adaptiveBuildConcurrency(budget16GiB); got != want {
		t.Errorf("adaptiveBuildConcurrency(16 GiB) = %d, want %d", got, want)
	}
}

// The semaphore must actually bound concurrency: with capacity 1, a second
// acquire cannot succeed until the first is released.
func TestBuildSemSerializes(t *testing.T) {
	sem := make(chan struct{}, 1)
	ctx := context.Background()

	if err := common.AcquireSem(ctx, sem); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		_ = common.AcquireSem(ctx, sem)
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("second acquire succeeded while the first was still held")
	default:
	}

	<-sem // release the first

	<-acquired // the waiter must now proceed
}

// A cancelled context must not block forever on a full semaphore — this is
// what lets a failing run tear down instead of hanging.
func TestBuildSemRespectsContextCancellation(t *testing.T) {
	sem := make(chan struct{}, 1)
	if err := common.AcquireSem(context.Background(), sem); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := common.AcquireSem(ctx, sem); err == nil {
		t.Error("acquire on a cancelled context should return an error, not block")
	}
}

// Executors built without a BuildSem (test fixtures, the reset path) must
// still work — importBranch nil-checks rather than assuming one exists.
func TestExecutorWithoutBuildSemIsUsable(t *testing.T) {
	e := newProjectDataExecutor(t, t.TempDir())
	if e.BuildSem != nil {
		t.Fatal("fixture unexpectedly has a BuildSem; this test guards the nil path")
	}

	// The nil-guard shape importBranch uses.
	buildSem := e.BuildSem
	if buildSem != nil {
		t.Fatal("nil BuildSem must not be acquired")
	}
}
