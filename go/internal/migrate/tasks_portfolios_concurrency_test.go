// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Issue #308: runConfigurePortfolios fans the per-portfolio closure
// out concurrently, so the shared branchID cache passed to
// resolveProjectBranchRefs is touched from multiple goroutines.
// Without the mutex this test panics with "fatal error: concurrent
// map writes" under `go test -race` (and intermittently without it).
// Guard the cache and the test passes.
func TestResolveProjectBranchRefsConcurrent(t *testing.T) {
	// Mock cloud endpoint that returns a deterministic branch UUID per
	// project so the goroutines can validate they got back the right
	// value end-to-end (the test of cache integrity under contention).
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("GET /api/project_branches/list", func(w http.ResponseWriter, r *http.Request) {
		project := r.URL.Query().Get("project")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"branches": []map[string]any{
				{"name": "main", "isMain": true, "branchId": "uuid-" + project},
			},
		})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()

	apiSrv := httptest.NewServer(http.NewServeMux())
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	// 10 unique project keys repeated to N goroutines so the same
	// keys race for cache entries.
	const (
		uniqueKeys  = 10
		goroutines  = 50
		keysPerCall = 20
	)
	keys := make([]string, keysPerCall)
	for i := range keys {
		keys[i] = fmt.Sprintf("proj-%d", i%uniqueKeys)
	}

	cache := map[string]string{}
	var mu sync.Mutex

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refs, err := resolveProjectBranchRefs(context.Background(), e, cache, &mu, keys)
			if err != nil {
				errs <- err
				return
			}
			if len(refs) != keysPerCall {
				errs <- fmt.Errorf("want %d refs, got %d", keysPerCall, len(refs))
				return
			}
			// Each returned ref must carry the deterministic UUID we
			// served, proving the cache wasn't corrupted mid-flight.
			for j, ref := range refs {
				want := "uuid-" + keys[j]
				if ref.BranchID != want {
					errs <- fmt.Errorf("ref[%d]: want %s, got %s", j, want, ref.BranchID)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// After every goroutine finished, the cache must hold exactly the
	// uniqueKeys entries (no duplicates, no garbage).
	mu.Lock()
	defer mu.Unlock()
	if len(cache) != uniqueKeys {
		t.Errorf("cache size: want %d, got %d", uniqueKeys, len(cache))
	}
}
