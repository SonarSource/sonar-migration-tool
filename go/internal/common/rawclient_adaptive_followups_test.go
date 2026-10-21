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
	for _, status := range []int{http.StatusNotImplemented, http.StatusHTTPVersionNotSupported} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var log pageLog
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.add(r)
				http.Error(w, http.StatusText(status), status)
			}))
			t.Cleanup(ts.Close)

			res, err := NewRawClient(ts.Client(), ts.URL+"/").GetPaginatedResult(context.Background(), projectSearchOpts())

			if !IsHTTPError(err, status) {
				t.Fatalf("err = %v, want HTTP %d", err, status)
			}
			if got := log.get(); !reflect.DeepEqual(got, []string{"1:500"}) {
				t.Errorf("requests = %v, want only [1:500]", got)
			}
			if res.Requests != 1 {
				t.Errorf("Requests = %d, want 1, set on error too", res.Requests)
			}
		})
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
	if res.PageSize != 50 {
		t.Errorf("PageSize = %d, want 50, the size of the last attempt", res.PageSize)
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
		name         string
		total        int
		stop         bool
		wantItems    int
		wantReason   TruncationReason
		wantRequests int
	}{
		// 300 items fit the original page: read until the short page.
		{"fits the original page", 300, false, 300, "", 3},
		// 400 items: two full pages, then an empty one shows the end.
		{"ends on a page boundary", 400, false, 400, "", 4},
		// 900 items: the original page would have held 500 and reported
		// unknown_total. Three pages of 200 cover those 500.
		{"more than the original page", 900, false, 600, ReasonUnknownTotal, 4},
		// A caller that stops on truncation gets the verdict it would
		// have had at 500: 300 items are complete, not truncated after
		// one full page of 200.
		{"stop on truncation, fits the original page", 300, true, 300, "", 3},
		{"stop on truncation, more than the original page", 900, true, 600, ReasonUnknownTotal, 4},
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

			opts := projectSearchOpts()
			opts.StopOnTruncation = tc.stop
			res, err := NewRawClient(ts.Client(), ts.URL+"/").GetPaginatedResult(context.Background(), opts)

			if err != nil {
				t.Fatalf("GetPaginatedResult: %v", err)
			}
			if res.PageSize != 200 || res.Reason != tc.wantReason {
				t.Errorf("PageSize=%d Reason=%q, want 200 / %q", res.PageSize, res.Reason, tc.wantReason)
			}
			if res.Requests != tc.wantRequests {
				t.Errorf("Requests = %d, want %d (the failed ps=500 one included)", res.Requests, tc.wantRequests)
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
	shortFallbackMemory(t, 300*time.Millisecond)
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
	time.Sleep(350 * time.Millisecond)
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

	type learn struct {
		size   int
		sticky bool
		at     time.Time
	}
	type check struct {
		requested int
		at        time.Time
		want      int
	}
	cases := []struct {
		name   string
		learn  []learn
		checks []check
	}{
		{"fast-learned size expires",
			[]learn{{200, false, now}},
			[]check{{500, now, 200}, {500, later, 500}}},
		{"slow-learned size is kept",
			[]learn{{200, true, now}},
			[]check{{500, later, 200}}},
		{"expired fast size falls back to the slow one",
			[]learn{{200, true, now}, {100, false, now}},
			[]check{{500, now, 100}, {500, later, 200}}},
		{"a larger size never replaces a smaller one",
			[]learn{{100, true, now}, {200, true, now}},
			[]check{{500, now, 100}}},
		{"a fast size larger than the slow one does not apply",
			[]learn{{100, true, now}, {200, false, now}},
			[]check{{500, now, 100}}},
		{"re-learning the fast size starts its expiry again",
			[]learn{{200, false, now}, {200, false, now.Add(40 * time.Second)}},
			[]check{{500, now.Add(90 * time.Second), 200}}},
		{"never above the requested size",
			[]learn{{200, true, now}},
			[]check{{100, now, 100}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tbl := newPageSizeTable()
			for _, l := range tc.learn {
				tbl.remember("p", l.size, l.sticky, l.at)
			}
			for _, c := range tc.checks {
				if got := tbl.sizeFor("p", c.requested, c.at); got != c.want {
					t.Errorf("sizeFor(%d) at +%v = %d, want %d", c.requested, c.at.Sub(now), got, c.want)
				}
			}
		})
	}
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
	t.Cleanup(func() { ForgetSharedPageSizes(ts.URL) })
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

// A slow failure at 500 followed by a fast blip at 200 keeps 200 for good
// and 100 only until it expires, so the endpoint goes back to 200 once
// the blip has passed instead of staying at 100 for the whole run.
func TestAdaptivePaginationSlowThenFastKeepsSizeBelowSlowFailure(t *testing.T) {
	var mu sync.Mutex
	blip := true
	ts, counter := countingServer(t, func(w http.ResponseWriter, r *http.Request, size, _ int) {
		mu.Lock()
		fail200 := blip
		if size == 100 {
			blip = false
		}
		mu.Unlock()
		switch {
		case size == 500:
			delayedStatus(w, r, 2*testSlowGatewayTimeout, http.StatusGatewayTimeout)
		case size == 200 && fail200:
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		default:
			writeAdaptiveTestPage(t, w, r, 30)
		}
	})
	raw := sdkRawClient(t, ts.URL, 5*time.Second)
	shortFallbackMemory(t, 300*time.Millisecond)
	captureDefaultLogger(t)

	fetch := func() int {
		t.Helper()
		res, err := raw.GetPaginatedResult(context.Background(), projectSearchOpts())
		if err != nil {
			t.Fatalf("GetPaginatedResult: %v", err)
		}
		return res.PageSize
	}

	if got := fetch(); got != 100 {
		t.Fatalf("first fetch PageSize = %d, want 100", got)
	}
	if got := fetch(); got != 100 {
		t.Errorf("fetch within the memory PageSize = %d, want 100", got)
	}
	time.Sleep(350 * time.Millisecond)
	if got := fetch(); got != 200 {
		t.Errorf("fetch after the fast size expired PageSize = %d, want the 200 kept below the slow failure", got)
	}
	if got := counter.count(500); got != 1 {
		t.Errorf("ps=500 requests = %d, want 1: 500 failed slowly and is not tried again", got)
	}
}

// A cap the smaller size does not divide still reads every item up to
// the caller's ceiling: 3 x 500 = 1,500 is 8 pages of 200, not 7.
func TestAdaptivePaginationOddCapLosesNothing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ps") == "500" {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		writeAdaptiveTestPage(t, w, r, 1500)
	}))
	t.Cleanup(ts.Close)
	captureDefaultLogger(t)

	opts := projectSearchOpts()
	opts.PageLimit = 3
	res, err := NewRawClient(ts.Client(), ts.URL+"/").GetPaginatedResult(context.Background(), opts)

	if err != nil {
		t.Fatalf("GetPaginatedResult: %v", err)
	}
	if res.Truncated {
		t.Errorf("Truncated with Reason %q, want a complete result", res.Reason)
	}
	assertCompleteUniqueKeys(t, res.Items, 1500)
}

// The defaults the docs quote. Tests replace most of them, so pin them
// here.
func TestAdaptivePaginationDefaults(t *testing.T) {
	if slowGatewayTimeout != 5*time.Second {
		t.Errorf("slowGatewayTimeout = %v, want 5s", slowGatewayTimeout)
	}
	if fastFallbackMemory != 5*time.Minute {
		t.Errorf("fastFallbackMemory = %v, want 5m", fastFallbackMemory)
	}
	if paginatedTimeoutFactor != 2 {
		t.Errorf("paginatedTimeoutFactor = %d, want 2", paginatedTimeoutFactor)
	}
	if minPageSizeSlowRetries != 2 {
		t.Errorf("minPageSizeSlowRetries = %d, want 2 (three tries at 50)", minPageSizeSlowRetries)
	}
	if want := []int{500, 200, 100, 50}; !reflect.DeepEqual(fallbackPageSizes, want) {
		t.Errorf("fallbackPageSizes = %v, want %v", fallbackPageSizes, want)
	}
}

// ForgetSharedPageSizes resets what shared clients learned, however the
// base URL is written.
func TestForgetSharedPageSizes(t *testing.T) {
	const base = "https://forget.example.com"
	NewSharedRawClient(http.DefaultClient, base+"/").pageSizes.remember("api/x", 200, true, time.Now())
	if got := NewSharedRawClient(http.DefaultClient, base).pageSizes.sizeFor("api/x", 500, time.Now()); got != 200 {
		t.Fatalf("shared size = %d, want 200 with or without the trailing slash", got)
	}
	ForgetSharedPageSizes(base + "/")
	if got := NewSharedRawClient(http.DefaultClient, base).pageSizes.sizeFor("api/x", 500, time.Now()); got != 500 {
		t.Errorf("after forgetting, size = %d, want 500", got)
	}
}
