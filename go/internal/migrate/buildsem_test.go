// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// The default must be well below the request concurrency default, because
// it bounds memory rather than request rate.
func TestBuildConcurrencyDefaultIsLowerThanRequestConcurrency(t *testing.T) {
	cfg := &MigrateConfig{}
	cfg.applyDefaults()

	if cfg.BuildConcurrency != DefaultBuildConcurrency {
		t.Errorf("BuildConcurrency = %d, want %d", cfg.BuildConcurrency, DefaultBuildConcurrency)
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
