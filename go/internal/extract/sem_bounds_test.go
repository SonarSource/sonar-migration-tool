// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// forEachDep used to start one goroutine per item with no errgroup limit.
// The item corpora are not per project — getProjectSourceCode and
// getProjectSCMData both fan out over the component tree, i.e. per file per
// branch per project — so a 1,139-project extract reached on the order of a
// million live goroutines, each pinning its item JSON and response buffers.
// That is what drove a customer host to 97% memory.
//
// The fix is an errgroup limit, which also gives g.Go natural backpressure.
func TestForEachDepBoundsGoroutineFanOut(t *testing.T) {
	const (
		slots = 4
		items = 400
	)

	// Block every request until the test releases them, so the maximum
	// number of goroutines the iterator is willing to have alive at once
	// is observable. The semaphore alone does not bound this: it limits
	// who is RUNNING, not how many goroutines exist waiting to run.
	gate := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/thing", func(w http.ResponseWriter, _ *http.Request) {
		<-gate
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e, store := newSemTestExecutor(t, srv, slots)
	seedDeps(t, store, items)

	before := runtime.NumGoroutine()
	done := make(chan error, 1)
	go func() {
		done <- forEachDep(context.Background(), e, "child", "deps",
			func(ctx context.Context, item json.RawMessage, _ *ChunkWriter) error {
				_, err := e.Raw.Get(ctx, "api/thing", nil)
				return err
			})
	}()

	// Let the iterator start as many goroutines as it intends to.
	deadline := time.Now().Add(3 * time.Second)
	peak := 0
	for time.Now().Before(deadline) {
		if n := runtime.NumGoroutine() - before; n > peak {
			peak = n
		}
		time.Sleep(20 * time.Millisecond)
	}

	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("forEachDep: %v", err)
	}

	// Unbounded, this is ~items goroutines (one per item, plus HTTP
	// machinery). Bounded, it stays near the limit. The generous ceiling
	// keeps the test about the order of magnitude, not an exact count.
	const ceiling = slots * 8
	if peak > ceiling {
		t.Errorf("peak %d extra goroutines for %d items with a limit of %d — the fan-out is unbounded",
			peak, items, slots)
	}
	if len(e.Sem) != 0 {
		t.Errorf("semaphore leaked %d of %d slots", len(e.Sem), cap(e.Sem))
	}
	t.Logf("peak %d extra goroutines for %d items (limit %d, ceiling %d)", peak, items, slots, ceiling)
}

// enrichHotspotDetails fans out while its caller (iterateBranches) already
// holds a slot. If it acquired one per hotspot, that would be hold-and-wait
// on a single shared pool: harmless at the default concurrency because the
// project walk is sequential, but a hard permanent deadlock at
// --concurrency 1 — which docs/TROUBLESHOOTING.md recommends for large
// instances. No timeout would ever break it, because the context has no
// deadline.
func TestEnrichHotspotDetailsDoesNotDeadlockAtConcurrencyOne(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hotspots/show", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"key": "h1", "comment": []any{}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// The pathological configuration: exactly one slot for the whole run.
	e, _ := newSemTestExecutor(t, srv, 1)

	enriched := make([]json.RawMessage, 0, 8)
	for i := 0; i < 8; i++ {
		rec, _ := json.Marshal(map[string]any{"key": fmt.Sprintf("h%d", i), "status": "REVIEWED"})
		enriched = append(enriched, rec)
	}

	// Hold the only slot, exactly as iterateBranches does around fn.
	if err := acquireSem(context.Background(), e.Sem); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() { <-e.Sem }()

	done := make(chan error, 1)
	go func() { done <- enrichHotspotDetails(context.Background(), e, enriched) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("enrichHotspotDetails: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("enrichHotspotDetails deadlocked beneath a held slot at --concurrency 1")
	}
}

func newSemTestExecutor(t *testing.T, srv *httptest.Server, slots int) (*Executor, *common.DataStore) {
	t.Helper()
	store := common.NewDataStore(t.TempDir())
	return &Executor{
		Raw:       common.NewRawClient(srv.Client(), srv.URL+"/"),
		Store:     store,
		ServerURL: srv.URL + "/",
		Sem:       make(chan struct{}, slots),
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1})),
	}, store
}

func seedDeps(t *testing.T, store *common.DataStore, n int) {
	t.Helper()
	w, err := store.Writer("deps")
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	for i := 0; i < n; i++ {
		rec, _ := json.Marshal(map[string]any{"key": fmt.Sprintf("item%d", i)})
		if err := w.WriteOne(rec); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}
