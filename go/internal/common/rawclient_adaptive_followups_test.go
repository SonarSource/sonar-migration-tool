// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// pageLog records every request a test server sees as "p:ps".
type pageLog struct {
	mu   sync.Mutex
	reqs []string
}

func (l *pageLog) add(r *http.Request) (page, size int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("p"))
	size, _ = strconv.Atoi(r.URL.Query().Get("ps"))
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, strconv.Itoa(page)+":"+strconv.Itoa(size))
	return page, size
}

func (l *pageLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.reqs...)
}

// writeUntotalledPage answers like writeAdaptiveTestPage, but with no
// paging object, so the client cannot know the total.
func writeUntotalledPage(t *testing.T, w http.ResponseWriter, page, size, total int) {
	t.Helper()
	items := make([]map[string]string, 0, size)
	for i := (page - 1) * size; i < page*size && i < total; i++ {
		items = append(items, map[string]string{"key": strconv.Itoa(i)})
	}
	if err := json.NewEncoder(w).Encode(map[string]any{"items": items}); err != nil {
		t.Errorf("encoding page: %v", err)
	}
}

// shortFallbackMemory makes fast-learned page sizes expire after d for
// the duration of the test.
func shortFallbackMemory(t *testing.T, d time.Duration) {
	t.Helper()
	prev := fastFallbackMemory
	fastFallbackMemory = d
	t.Cleanup(func() { fastFallbackMemory = prev })
}

// A slow 502 or 503 is a gateway timeout too (Apache httpd sends 502
// after ProxyTimeout), so like a slow 504 it steps the page size down
// after one request instead of being retried at the same size.
func TestAdaptivePaginationSlow502And503StepDownAfterOneRequest(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			ts, counter := countingServer(t, func(w http.ResponseWriter, r *http.Request, size, _ int) {
				if size == 500 {
					delayedStatus(w, r, 2*testSlowGatewayTimeout, status)
					return
				}
				writeAdaptiveTestPage(t, w, r, 30)
			})

			res, err := sdkRawClient(t, ts.URL, 5*time.Second).GetPaginatedResult(context.Background(), projectSearchOpts())

			if err != nil {
				t.Fatalf("GetPaginatedResult: %v", err)
			}
			if got := counter.count(500); got != 1 {
				t.Errorf("ps=500 requests = %d, want 1", got)
			}
			if res.PageSize != 200 {
				t.Errorf("PageSize = %d, want 200", res.PageSize)
			}
			assertCompleteUniqueKeys(t, res.Items, 30)
		})
	}
}

// A 5xx that says nothing about page size is returned at once.
func TestAdaptivePaginationNonFallbackStatusIsReturned(t *testing.T) {
	var log pageLog
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		http.Error(w, "not implemented", http.StatusNotImplemented)
	}))
	t.Cleanup(ts.Close)

	_, err := NewRawClient(ts.Client(), ts.URL+"/").GetPaginatedResult(context.Background(), projectSearchOpts())

	if !IsHTTPError(err, http.StatusNotImplemented) {
		t.Fatalf("err = %v, want HTTP 501", err)
	}
	if got := log.get(); !reflect.DeepEqual(got, []string{"1:500"}) {
		t.Errorf("requests = %v, want only [1:500]", got)
	}
}

// When every size fails, the error says so, and still unwraps to the
// HTTPError of the last request.
func TestAdaptivePaginationErrorNamesEverySize(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
	}))
	t.Cleanup(ts.Close)

	res, err := NewRawClient(ts.Client(), ts.URL+"/").GetPaginatedResult(context.Background(), projectSearchOpts())

	if err == nil || !strings.Contains(err.Error(), "failed at every page size from 500 down to 50") {
		t.Fatalf("err = %v, want it to name every size tried", err)
	}
	if !IsHTTPError(err, http.StatusGatewayTimeout) {
		t.Errorf("err = %v, want it to unwrap to HTTP 504", err)
	}
	if res.Requests != 4 {
		t.Errorf("Requests = %d, want 4 (one per size), set on error too", res.Requests)
	}
}

// api/ce/activity asks for 1000; its fallback enters the sequence at 500
// rather than skipping it.
func TestAdaptivePaginationLargeRequestStepsTo500(t *testing.T) {
	var log pageLog
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, size := log.add(r); size > 500 {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		writeAdaptiveTestPage(t, w, r, 30)
	}))
	t.Cleanup(ts.Close)

	opts := projectSearchOpts()
	opts.MaxPageSize = 1000
	res, err := NewRawClient(ts.Client(), ts.URL+"/").GetPaginatedResult(context.Background(), opts)

	if err != nil {
		t.Fatalf("GetPaginatedResult: %v", err)
	}
	if want := []string{"1:1000", "1:500"}; !reflect.DeepEqual(log.get(), want) {
		t.Errorf("requests = %v, want %v", log.get(), want)
	}
	if res.PageSize != 500 {
		t.Errorf("PageSize = %d, want 500", res.PageSize)
	}
}

// Without a readable total, a fetch at the requested size reads one
// page. After a fallback it must still return at least as many items as
// that page held, not just one smaller page.
func TestAdaptivePaginationWithoutTotalKeepsOriginalPageWorth(t *testing.T) {
	cases := []struct {
		name       string
		total      int
		wantItems  int
		wantReason TruncationReason
	}{
		// 300 items fit the original page: read until the short page.
		{"fits the original page", 300, 300, ""},
		// 900 items: the original page would have held 500 and reported
		// unknown_total. Three pages of 200 cover those 500.
		{"more than the original page", 900, 600, ReasonUnknownTotal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page, _ := strconv.Atoi(r.URL.Query().Get("p"))
				size, _ := strconv.Atoi(r.URL.Query().Get("ps"))
				if size == 500 {
					http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
					return
				}
				writeUntotalledPage(t, w, page, size, tc.total)
			}))
			t.Cleanup(ts.Close)
			captureDefaultLogger(t)

			res, err := NewRawClient(ts.Client(), ts.URL+"/").GetPaginatedResult(context.Background(), projectSearchOpts())

			if err != nil {
				t.Fatalf("GetPaginatedResult: %v", err)
			}
			if res.PageSize != 200 || res.Reason != tc.wantReason {
				t.Errorf("PageSize=%d Reason=%q, want 200 / %q", res.PageSize, res.Reason, tc.wantReason)
			}
			assertCompleteUniqueKeys(t, res.Items, tc.wantItems)
		})
	}
}

// Requests counts the pages of attempts a fallback set aside; PagesRead
// counts the ones that succeeded.
func TestAdaptivePaginationCountsDiscardedRequests(t *testing.T) {
	var log pageLog
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if page, size := log.add(r); page == 2 && size == 500 {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		writeAdaptiveTestPage(t, w, r, 650)
	}))
	t.Cleanup(ts.Close)

	res, err := NewRawClient(ts.Client(), ts.URL+"/").GetPaginatedResult(context.Background(), projectSearchOpts())

	if err != nil {
		t.Fatalf("GetPaginatedResult: %v", err)
	}
	if got := log.get(); len(got) != res.Requests || res.Requests != 4 {
		t.Errorf("Requests = %d, server saw %v, want 4 of each", res.Requests, got)
	}
	if res.PagesRead != 3 {
		t.Errorf("PagesRead = %d, want 3", res.PagesRead)
	}
}

// A short page read mid-walk means item counts no longer line up with
// page offsets, so a later fallback restarts from page 1 rather than
// resuming at a wrong offset.
func TestAdaptivePaginationRestartsAfterShortPage(t *testing.T) {
	var log pageLog
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, size := log.add(r)
		switch {
		case size == 500 && page == 1:
			// One item short of a full page, as if one was deleted.
			items := make([]map[string]string, 0, 499)
			for i := 0; i < 499; i++ {
				items = append(items, map[string]string{"key": strconv.Itoa(i)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "paging": map[string]int{"total": 650}})
		case size == 500:
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
		default:
			writeAdaptiveTestPage(t, w, r, 650)
		}
	}))
	t.Cleanup(ts.Close)
	captureDefaultLogger(t)

	res, err := NewRawClient(ts.Client(), ts.URL+"/").GetPaginatedResult(context.Background(), projectSearchOpts())

	if err != nil {
		t.Fatalf("GetPaginatedResult: %v", err)
	}
	want := []string{"1:500", "2:500", "1:200", "2:200", "3:200", "4:200"}
	if got := log.get(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	assertCompleteUniqueKeys(t, res.Items, 650)
}

// A size learned from fast failures only is forgotten after
// fastFallbackMemory, so a short burst of errors cannot hold an endpoint
// at a small page size for the rest of the run.
func TestAdaptivePaginationFastFallbackExpires(t *testing.T) {
	shortFallbackMemory(t, 50*time.Millisecond)
	var log pageLog
	var mu sync.Mutex
	failing := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, size := log.add(r)
		mu.Lock()
		fail := failing && size == 500
		mu.Unlock()
		if fail {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		writeAdaptiveTestPage(t, w, r, 30)
	}))
	t.Cleanup(ts.Close)
	captureDefaultLogger(t)
	raw := NewRawClient(ts.Client(), ts.URL+"/")

	fetch := func() int {
		t.Helper()
		res, err := raw.GetPaginatedResult(context.Background(), projectSearchOpts())
		if err != nil {
			t.Fatalf("GetPaginatedResult: %v", err)
		}
		return res.PageSize
	}

	if got := fetch(); got != 200 {
		t.Fatalf("burst fetch PageSize = %d, want 200", got)
	}
	mu.Lock()
	failing = false
	mu.Unlock()
	if got := fetch(); got != 200 {
		t.Errorf("fetch within the memory PageSize = %d, want the remembered 200", got)
	}
	time.Sleep(60 * time.Millisecond)
	if got := fetch(); got != 500 {
		t.Errorf("fetch after the memory expired PageSize = %d, want 500 again", got)
	}
}

// A size learned from a slow gateway timeout is kept for good: finding
// it again would cost another full gateway delay.
func TestAdaptivePaginationSlowFallbackIsKept(t *testing.T) {
	shortFallbackMemory(t, time.Millisecond)
	ts, counter := countingServer(t, func(w http.ResponseWriter, r *http.Request, size, _ int) {
		if size == 500 {
			delayedStatus(w, r, 2*testSlowGatewayTimeout, http.StatusGatewayTimeout)
			return
		}
		writeAdaptiveTestPage(t, w, r, 30)
	})
	raw := sdkRawClient(t, ts.URL, 5*time.Second)

	for i := 0; i < 2; i++ {
		if _, err := raw.GetPaginatedResult(context.Background(), projectSearchOpts()); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := counter.count(500); got != 1 {
		t.Errorf("ps=500 requests = %d, want 1: the slow-learned 200 must not expire", got)
	}
}

func TestPageSizeTable(t *testing.T) {
	shortFallbackMemory(t, time.Minute)
	now := time.Now()
	later := now.Add(2 * time.Minute)

	t.Run("fast-learned size expires", func(t *testing.T) {
		tbl := newPageSizeTable()
		tbl.remember("p", 200, false, now)
		if got := tbl.sizeFor("p", 500, now); got != 200 {
			t.Errorf("before expiry = %d, want 200", got)
		}
		if got := tbl.sizeFor("p", 500, later); got != 500 {
			t.Errorf("after expiry = %d, want 500", got)
		}
	})
	t.Run("slow-learned size is kept", func(t *testing.T) {
		tbl := newPageSizeTable()
		tbl.remember("p", 200, true, now)
		if got := tbl.sizeFor("p", 500, later); got != 200 {
			t.Errorf("= %d, want 200", got)
		}
	})
	t.Run("expired fast size falls back to the slow one", func(t *testing.T) {
		tbl := newPageSizeTable()
		tbl.remember("p", 200, true, now)
		tbl.remember("p", 100, false, now)
		if got := tbl.sizeFor("p", 500, now); got != 100 {
			t.Errorf("before expiry = %d, want 100", got)
		}
		if got := tbl.sizeFor("p", 500, later); got != 200 {
			t.Errorf("after expiry = %d, want the slow-learned 200", got)
		}
	})
	t.Run("a larger size never replaces a smaller one", func(t *testing.T) {
		tbl := newPageSizeTable()
		tbl.remember("p", 100, true, now)
		tbl.remember("p", 200, true, now)
		if got := tbl.sizeFor("p", 500, now); got != 100 {
			t.Errorf("= %d, want 100", got)
		}
	})
	t.Run("never above the requested size", func(t *testing.T) {
		tbl := newPageSizeTable()
		tbl.remember("p", 200, true, now)
		if got := tbl.sizeFor("p", 100, now); got != 100 {
			t.Errorf("= %d, want 100", got)
		}
	})
}

// Clients built with NewSharedRawClient for the same server share what
// either learned; NewRawClient clients do not.
func TestNewSharedRawClientSharesLearnedSizes(t *testing.T) {
	var log pageLog
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, size := log.add(r); size == 500 {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		writeAdaptiveTestPage(t, w, r, 30)
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { sharedPageSizes.Delete(ts.URL + "/") })
	captureDefaultLogger(t)

	first := NewSharedRawClient(ts.Client(), ts.URL+"/")
	if _, err := first.GetPaginatedResult(context.Background(), projectSearchOpts()); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	before := len(log.get())

	second := NewSharedRawClient(ts.Client(), ts.URL+"/")
	if _, err := second.GetPaginatedResult(context.Background(), projectSearchOpts()); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got := log.get()[before:]; !reflect.DeepEqual(got, []string{"1:200"}) {
		t.Errorf("shared client requests = %v, want [1:200]", got)
	}

	before = len(log.get())
	private := NewRawClient(ts.Client(), ts.URL+"/")
	if _, err := private.GetPaginatedResult(context.Background(), projectSearchOpts()); err != nil {
		t.Fatalf("private fetch: %v", err)
	}
	if got := log.get()[before:]; got[0] != "1:500" {
		t.Errorf("private client requests = %v, want to start at 1:500", got)
	}
}

// HTTPError.Elapsed is measured from the request being sent, so a fast
// 504 that waited for a rate-limit slot first is not mistaken for slow.
func TestHTTPErrorElapsedExcludesWaitBeforeSend(t *testing.T) {
	const wait = 150 * time.Millisecond
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
	}))
	t.Cleanup(ts.Close)
	client := &http.Client{Transport: waitThenSend{wait: wait, next: http.DefaultTransport}}

	_, err := NewRawClient(client, ts.URL+"/").Get(context.Background(), "api/server/version", nil)

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want an HTTPError", err)
	}
	if httpErr.Elapsed >= wait {
		t.Errorf("Elapsed = %v, want well under the %v spent before sending", httpErr.Elapsed, wait)
	}
}

// waitThenSend waits before handing the request on, as throttleTransport
// does while it waits for a rate-limit slot.
type waitThenSend struct {
	wait time.Duration
	next http.RoundTripper
}

func (w waitThenSend) RoundTrip(r *http.Request) (*http.Response, error) {
	time.Sleep(w.wait)
	return w.next.RoundTrip(r)
}
