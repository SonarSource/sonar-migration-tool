// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestAdaptivePaginationDoesNotRetryClientErrors verifies that
// HTTP 4xx responses are returned immediately without reducing ps.
func TestAdaptivePaginationDoesNotRetryClientErrors(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusTooManyRequests,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var mu sync.Mutex
			var requests []string

			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					requests = append(requests, r.URL.Query().Get("ps"))
					mu.Unlock()

					http.Error(w, "request rejected", status)
				},
			))
			defer server.Close()

			raw := NewRawClient(server.Client(), server.URL+"/")

			_, err := raw.GetPaginatedResult(
				context.Background(),
				PaginatedOpts{
					Path:      "api/projects/search",
					ResultKey: "items",
				},
			)

			if err == nil {
				t.Fatal("expected an HTTP error")
			}

			var httpErr *HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("expected HTTPError, got %T: %v", err, err)
			}
			if httpErr.StatusCode != status {
				t.Errorf(
					"status: got %d, want %d",
					httpErr.StatusCode, status,
				)
			}

			mu.Lock()
			got := append([]string(nil), requests...)
			mu.Unlock()

			if !reflect.DeepEqual(got, []string{"500"}) {
				t.Errorf(
					"requests: got %v, want [500]; 4xx must not trigger fallback",
					got,
				)
			}
		})
	}
}

// TestAdaptivePaginationStopsAtMinimumPageSize verifies that
// persistent HTTP 5xx errors stop after trying 500, 200, 100 and 50.
// It also verifies the ERROR log required for a failed extraction.
func TestAdaptivePaginationStopsAtMinimumPageSize(t *testing.T) {
	var mu sync.Mutex
	var requests []string

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requests = append(requests, r.URL.Query().Get("ps"))
			mu.Unlock()

			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
		},
	))
	defer server.Close()

	raw := NewRawClient(server.Client(), server.URL+"/")
	logs := captureDefaultLogger(t)

	_, err := raw.GetPaginatedResult(
		context.Background(),
		PaginatedOpts{
			Path:      "api/projects/search",
			ResultKey: "items",
		},
	)

	if err == nil {
		t.Fatal("expected extraction to fail after ps=50")
	}

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusGatewayTimeout {
		t.Errorf(
			"status: got %d, want 504",
			httpErr.StatusCode,
		)
	}

	mu.Lock()
	got := append([]string(nil), requests...)
	mu.Unlock()

	want := []string{"500", "200", "100", "50"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("page sizes: got %v, want %v", got, want)
	}

	if !strings.Contains(
		logs.String(),
		"paginated API request failed with HTTP 5xx at minimum fallback page size",
	) {
		t.Error("expected an ERROR log when ps=50 also fails")
	}
}

// endpointSizeCase is one endpoint and the page size it should settle
// on in TestAdaptivePaginationConcurrentEndpointMemory.
type endpointSizeCase struct {
	path     string
	wantSize int
}

var concurrentEndpointCases = []endpointSizeCase{
	{"api/projects/search", 100},
	{"api/issues/search", 500},
}

// concurrentEndpointServer serves project search only at ps=100 or
// below and issue search at any size, 25 items each. calls returns a
// copy of the page sizes requested on a server path, in arrival order.
func concurrentEndpointServer(t *testing.T) (server *httptest.Server, calls func(path string) []int) {
	t.Helper()

	var mu sync.Mutex
	requests := make(map[string][]int)
	record := func(r *http.Request) int {
		size, _ := strconv.Atoi(r.URL.Query().Get("ps"))
		mu.Lock()
		requests[r.URL.Path] = append(requests[r.URL.Path], size)
		mu.Unlock()
		return size
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/search", func(w http.ResponseWriter, r *http.Request) {
		// Project search succeeds only at ps=100 or below.
		if record(r) > 100 {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		writeAdaptiveTestPage(t, w, r, 25)
	})
	mux.HandleFunc("/api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		// This endpoint works at the default ps=500.
		record(r)
		writeAdaptiveTestPage(t, w, r, 25)
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	calls = func(path string) []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), requests[path]...)
	}
	return server, calls
}

// fetchAndCheckPageSize fetches tc.path once and reports the first way
// the result misses tc.wantSize or a complete 25-item page.
func fetchAndCheckPageSize(raw *RawClient, tc endpointSizeCase) error {
	result, err := raw.GetPaginatedResult(context.Background(), PaginatedOpts{
		Path:      tc.path,
		ResultKey: "items",
	})
	switch {
	case err != nil:
		return fmt.Errorf("%s: %w", tc.path, err)
	case result.PageSize != tc.wantSize:
		return fmt.Errorf("%s: page size %d, want %d", tc.path, result.PageSize, tc.wantSize)
	case result.Fetched != 25 || result.Truncated:
		return fmt.Errorf("%s: unexpected result: %+v", tc.path, result)
	}
	return nil
}

// checkEndpoints fetches every concurrent endpoint case once, sending
// each failure to errs.
func checkEndpoints(raw *RawClient, errs chan<- error) {
	for _, tc := range concurrentEndpointCases {
		if err := fetchAndCheckPageSize(raw, tc); err != nil {
			errs <- err
		}
	}
}

// TestAdaptivePaginationConcurrentEndpointMemory verifies that
// concurrent callers can share one RawClient safely while each API
// endpoint maintains its own effective page size.
func TestAdaptivePaginationConcurrentEndpointMemory(t *testing.T) {
	const workers = 16

	server, calls := concurrentEndpointServer(t)
	raw := NewRawClient(server.Client(), server.URL+"/")

	var wg sync.WaitGroup
	start := make(chan struct{})
	// At most one error per worker per case, so no send can block.
	errs := make(chan error, workers*len(concurrentEndpointCases))

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			checkEndpoints(raw, errs)
		}()
	}

	// Release all workers at the same time.
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	// After concurrent fallbacks, the cache must still remember
	// 100 for projects and 500 for issues.
	for _, tc := range concurrentEndpointCases {
		if err := fetchAndCheckPageSize(raw, tc); err != nil {
			t.Errorf("final fetch: %v", err)
		}
		got := calls("/" + tc.path)
		if len(got) == 0 {
			t.Fatalf("no requests reached %s", tc.path)
		}
		if last := got[len(got)-1]; last != tc.wantSize {
			t.Errorf("last %s request used ps=%d, want %d", tc.path, last, tc.wantSize)
		}
	}
}
