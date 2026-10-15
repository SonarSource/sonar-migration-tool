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

// TestAdaptivePaginationConcurrentEndpointMemory verifies that
// concurrent callers can share one RawClient safely while each API
// endpoint maintains its own effective page size.
func TestAdaptivePaginationConcurrentEndpointMemory(t *testing.T) {
	const workers = 16

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

	mux.HandleFunc("/api/projects/search",
		func(w http.ResponseWriter, r *http.Request) {
			size := record(r)

			// Project search succeeds only at ps=100 or below.
			if size > 100 {
				http.Error(
					w, "gateway timeout", http.StatusGatewayTimeout,
				)
				return
			}

			writeAdaptiveTestPage(t, w, r, 25)
		},
	)

	mux.HandleFunc("/api/issues/search",
		func(w http.ResponseWriter, r *http.Request) {
			record(r)

			// This endpoint works at the default ps=500.
			writeAdaptiveTestPage(t, w, r, 25)
		},
	)

	server := httptest.NewServer(mux)
	defer server.Close()

	raw := NewRawClient(server.Client(), server.URL+"/")

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, workers*4)

	for i := 0; i < workers; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()
			<-start

			for _, testCase := range []struct {
				path     string
				wantSize int
			}{
				{"api/projects/search", 100},
				{"api/issues/search", 500},
			} {
				result, err := raw.GetPaginatedResult(
					context.Background(),
					PaginatedOpts{
						Path:      testCase.path,
						ResultKey: "items",
					},
				)

				if err != nil {
					errs <- fmt.Errorf(
						"%s: %w", testCase.path, err,
					)
					continue
				}

				if result.PageSize != testCase.wantSize {
					errs <- fmt.Errorf(
						"%s: page size %d, want %d",
						testCase.path,
						result.PageSize,
						testCase.wantSize,
					)
				}

				if result.Fetched != 25 || result.Truncated {
					errs <- fmt.Errorf(
						"%s: unexpected result: %+v",
						testCase.path, result,
					)
				}
			}
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
	for _, testCase := range []struct {
		path     string
		wantSize int
	}{
		{"api/projects/search", 100},
		{"api/issues/search", 500},
	} {
		result, err := raw.GetPaginatedResult(
			context.Background(),
			PaginatedOpts{
				Path:      testCase.path,
				ResultKey: "items",
			},
		)
		if err != nil {
			t.Fatalf("%s: final fetch: %v", testCase.path, err)
		}
		if result.PageSize != testCase.wantSize {
			t.Errorf(
				"%s: cached page size %d, want %d",
				testCase.path,
				result.PageSize,
				testCase.wantSize,
			)
		}
	}

	mu.Lock()
	projectCalls := append(
		[]int(nil), requests["/api/projects/search"]...,
	)
	issueCalls := append(
		[]int(nil), requests["/api/issues/search"]...,
	)
	mu.Unlock()

	if len(projectCalls) == 0 || len(issueCalls) == 0 {
		t.Fatal("expected calls to both API endpoints")
	}

	if got := projectCalls[len(projectCalls)-1]; got != 100 {
		t.Errorf("last project request used ps=%d, want 100", got)
	}
	if got := issueCalls[len(issueCalls)-1]; got != 500 {
		t.Errorf("last issue request used ps=%d, want 500", got)
	}
}
