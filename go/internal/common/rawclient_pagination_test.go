// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// pagedServer serves a paginated endpoint holding total items, honouring
// the p/ps the client sends and counting requests. reportTotal controls
// whether the response carries a paging.total at all, which is the
// difference between a known and an unknown total.
func pagedServer(t *testing.T, path string, total int, reportTotal bool) (*RawClient, *int) {
	t.Helper()
	requestCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		page, _ := strconv.Atoi(r.URL.Query().Get("p"))
		size, _ := strconv.Atoi(r.URL.Query().Get("ps"))
		start := (page - 1) * size
		items := make([]map[string]any, 0, size)
		for i := start; i < start+size && i < total; i++ {
			items = append(items, map[string]any{"key": strconv.Itoa(i)})
		}
		body := map[string]any{"items": items}
		if reportTotal {
			body["paging"] = map[string]any{"total": total}
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// The trailing slash is required: doGet concatenates base and path.
	return NewRawClient(srv.Client(), srv.URL+"/"), &requestCount
}

// observeTruncation installs an observer and returns the slice it fills.
func observeTruncation(raw *RawClient) *[]TruncationRecord {
	var records []TruncationRecord
	raw.SetTruncationObserver(func(rec TruncationRecord) {
		records = append(records, rec)
	})
	return &records
}

// captureDefaultLogger redirects slog.Default() for the duration of the
// test, restoring it afterwards. Used to assert on the unconditional
// truncation warning, which is the only signal migrate and regtest get.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// assertFetchOutcome asserts the three fields that together state what a
// fetch returned and whether it admitted losing anything: the item
// count, the Reason, and Truncated — which must follow Reason and never
// a count comparison.
func assertFetchOutcome(t *testing.T, res PageResult, wantFetched int, wantReason TruncationReason) {
	t.Helper()
	if res.Fetched != wantFetched {
		t.Errorf("Fetched: got %d, want %d", res.Fetched, wantFetched)
	}
	if res.Reason != wantReason {
		t.Errorf("Reason: got %q, want %q", res.Reason, wantReason)
	}
	if res.Truncated != (wantReason != "") {
		t.Errorf("Truncated: got %v, want %v", res.Truncated, wantReason != "")
	}
}

// assertKnownTotal asserts the server's total came back intact and
// flagged as known.
func assertKnownTotal(t *testing.T, res PageResult, wantTotal int) {
	t.Helper()
	if !res.TotalKnown || res.Total != wantTotal {
		t.Errorf("Total/TotalKnown: got %d/%v, want %d/true", res.Total, res.TotalKnown, wantTotal)
	}
}

// assertEffectivePaging asserts the effective page settings travelled
// with the result, so no caller re-derives them from a by-value opts
// copy whose MaxPageSize is still zero.
func assertEffectivePaging(t *testing.T, res PageResult, wantSize, wantLimit int) {
	t.Helper()
	if res.PageSize != wantSize || res.PageLimit != wantLimit {
		t.Errorf("effective PageSize/PageLimit: got %d/%d, want %d/%d", res.PageSize, res.PageLimit, wantSize, wantLimit)
	}
}

// firstRecord asserts the observer saw exactly wantRecords truncation(s)
// and hands back the first one for inspection. The bool is false when
// the expectation was zero records, i.e. there is nothing to inspect.
func firstRecord(t *testing.T, records []TruncationRecord, wantRecords int) (TruncationRecord, bool) {
	t.Helper()
	if len(records) != wantRecords {
		t.Fatalf("recorded %d truncation(s), want %d", len(records), wantRecords)
	}
	if wantRecords == 0 {
		return TruncationRecord{}, false
	}
	return records[0], true
}

// clampCase is one side of the page-limit ceiling: the totals just
// below, exactly at, and just above it, with the outcome each must
// produce.
type clampCase struct {
	name        string
	total       int
	wantFetched int
	wantReason  TruncationReason
	wantLost    int
	wantRecords int
}

// TestClampFiresRecordOnlyAboveTheLimit prevents both halves of the
// off-by-one: a fetch of exactly the ceiling's worth of results being
// reported as truncated (a false data-loss bullet in the report, since
// p=20&ps=500 is a legal request that returns HTTP 200), and a fetch one
// item past the ceiling being reported as complete (#574).
func TestClampFiresRecordOnlyAboveTheLimit(t *testing.T) {
	cases := []clampCase{
		{"one result short of the ceiling", 9999, 9999, "", 0, 0},
		{"exactly the ceiling, which is fetchable", 10000, 10000, "", 0, 0},
		{"one result past the ceiling", 10001, 10000, ReasonPageLimitClamp, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runClampCase(t, tc) })
	}
}

// runClampCase fetches one clampCase against a server holding tc.total
// results and asserts the whole outcome: the result fields, the
// effective page settings, and the truncation record (or its absence).
func runClampCase(t *testing.T, tc clampCase) {
	raw, _ := pagedServer(t, "/api/test/clamp", tc.total, true)
	records := observeTruncation(raw)

	res, err := raw.GetPaginatedResult(context.Background(), PaginatedOpts{
		Path: "api/test/clamp", ResultKey: "items",
		MaxPageSize: 500, PageLimit: 20,
		Scope: TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "alpha", Branch: "main"},
	})
	if err != nil {
		t.Fatalf("GetPaginatedResult failed: %v", err)
	}

	assertFetchOutcome(t, res, tc.wantFetched, tc.wantReason)
	assertKnownTotal(t, res, tc.total)
	assertEffectivePaging(t, res, 500, 20)

	rec, ok := firstRecord(t, *records, tc.wantRecords)
	if !ok {
		return
	}
	assertClampRecord(t, rec, tc)
}

// assertClampRecord asserts the artefact a clamp leaves behind carries
// everything the report bullet needs: the lost count, the cause, the
// endpoint, the attribution and a timestamp.
func assertClampRecord(t *testing.T, rec TruncationRecord, tc clampCase) {
	t.Helper()
	if rec.Lost != tc.wantLost {
		t.Errorf("record Lost: got %d, want %d", rec.Lost, tc.wantLost)
	}
	if rec.Reason != tc.wantReason {
		t.Errorf("record Reason: got %q, want %q", rec.Reason, tc.wantReason)
	}
	if rec.Endpoint != "api/test/clamp" {
		t.Errorf("record Endpoint: got %q, want %q", rec.Endpoint, "api/test/clamp")
	}
	if got := rec.Scope.Label(); got != "getProjectIssuesFull alpha@main" {
		t.Errorf("record scope label: got %q, want %q", got, "getProjectIssuesFull alpha@main")
	}
	if rec.ObservedAt.IsZero() {
		t.Error("record ObservedAt is zero; the artefact needs a timestamp")
	}
}

// TestStopOnTruncationReturnsAfterPageOne prevents the failure where
// detecting "this project is too big for one fetch" costs a full
// PageLimit walk — twenty requests and 10,000 discarded issues — on
// every large project the slicer is about to re-fetch window by window
// anyway (#574).
func TestStopOnTruncationReturnsAfterPageOne(t *testing.T) {
	raw, requestCount := pagedServer(t, "/api/test/stop", 25000, true)
	records := observeTruncation(raw)

	res, err := raw.GetPaginatedResult(context.Background(), PaginatedOpts{
		Path: "api/test/stop", ResultKey: "items",
		MaxPageSize: 500, PageLimit: 20, StopOnTruncation: true,
	})
	if err != nil {
		t.Fatalf("GetPaginatedResult failed: %v", err)
	}

	if *requestCount != 1 {
		t.Errorf("made %d request(s), want 1", *requestCount)
	}
	if res.PagesRead != 1 {
		t.Errorf("PagesRead: got %d, want 1", res.PagesRead)
	}
	if res.Fetched != 500 {
		t.Errorf("Fetched: got %d, want 500 (page 1 is kept, not discarded)", res.Fetched)
	}
	if res.Total != 25000 || !res.TotalKnown {
		t.Errorf("Total/TotalKnown: got %d/%v, want 25000/true", res.Total, res.TotalKnown)
	}
	if !res.Truncated || res.Reason != ReasonPageLimitClamp {
		t.Errorf("Truncated/Reason: got %v/%q, want true/%q", res.Truncated, res.Reason, ReasonPageLimitClamp)
	}
	if len(*records) != 1 {
		t.Fatalf("recorded %d truncation(s), want 1", len(*records))
	}
	if got := (*records)[0].Lost; got != 24500 {
		t.Errorf("record Lost: got %d, want 24500", got)
	}
}

// unknownTotalCase is one response with no parseable total: a
// completely full page, which is indistinguishable from a first page of
// many, and a partial one, which can only be the whole result set.
type unknownTotalCase struct {
	name        string
	total       int
	wantReason  TruncationReason
	wantRecords int
}

// TestUnknownTotalWithAFullPageIsReportedAsTruncation prevents the one
// silent-truncation class this repo has already shipped: a response with
// no parseable total makes TotalPages return zero, the loop never runs,
// a single full page comes back and nothing is flagged — the exact shape
// described in migrate/tasks_compare_profiles.go (#574).
func TestUnknownTotalWithAFullPageIsReportedAsTruncation(t *testing.T) {
	cases := []unknownTotalCase{
		{"a completely full page and no total", 3, ReasonUnknownTotal, 1},
		{"a partial page and no total is complete", 2, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runUnknownTotalCase(t, tc) })
	}
}

// runUnknownTotalCase fetches one unknownTotalCase from a server that
// reports no paging.total and asserts the outcome: one request, a total
// that stays flagged unknown, and a record that refuses to guess how
// much was lost (or no record at all for a partial page).
func runUnknownTotalCase(t *testing.T, tc unknownTotalCase) {
	raw, requestCount := pagedServer(t, "/api/test/nototal", tc.total, false)
	records := observeTruncation(raw)

	res, err := raw.GetPaginatedResult(context.Background(), PaginatedOpts{
		Path: "api/test/nototal", ResultKey: "items", MaxPageSize: 3,
	})
	if err != nil {
		t.Fatalf("GetPaginatedResult failed: %v", err)
	}

	if *requestCount != 1 {
		t.Errorf("made %d request(s), want 1", *requestCount)
	}
	if res.TotalKnown {
		t.Error("TotalKnown = true for a response with no paging.total, want false")
	}
	assertFetchOutcome(t, res, tc.total, tc.wantReason)

	rec, ok := firstRecord(t, *records, tc.wantRecords)
	if !ok {
		return
	}
	if rec.TotalKnown {
		t.Error("record TotalKnown = true, want false — the server never said how many there were")
	}
	// Lost is unknowable here, and guessing it would put a fabricated
	// number in the report.
	if rec.Lost != 0 {
		t.Errorf("record Lost: got %d, want 0 for an unknown total", rec.Lost)
	}
}

// TestObjectResultKeyDoesNotProduceAFalseRecord prevents the failure
// where Truncated is derived from Fetched < Total: api/rules/search
// returns an OBJECT at "actives", which ExtractArray wraps as a single
// element, so that comparison fires on every healthy response and puts a
// phantom data-loss bullet in every migration report (#574).
func TestObjectResultKeyDoesNotProduceAFalseRecord(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/rules/search", func(w http.ResponseWriter, r *http.Request) {
		// The Cloud shape: a top-level "total" and an object at the
		// result key rather than an array.
		if _, err := w.Write([]byte(`{"total":5,"actives":{"java:S100":[{"severity":"MAJOR"}]}}`)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	raw := NewRawClient(srv.Client(), srv.URL+"/")
	records := observeTruncation(raw)

	res, err := raw.GetPaginatedResult(context.Background(), PaginatedOpts{
		Path: "api/rules/search", ResultKey: "actives", TotalKey: "total", MaxPageSize: 500,
	})
	if err != nil {
		t.Fatalf("GetPaginatedResult failed: %v", err)
	}

	if res.Fetched != 1 || res.Total != 5 {
		t.Errorf("Fetched/Total: got %d/%d, want 1/5 (the object is wrapped as one element)", res.Fetched, res.Total)
	}
	if res.Truncated {
		t.Error("Truncated = true for an object result key; Truncated must follow Reason, never Fetched < Total")
	}
	if res.Reason != "" {
		t.Errorf("Reason: got %q, want empty", res.Reason)
	}
	if len(*records) != 0 {
		t.Errorf("recorded %d truncation(s), want 0", len(*records))
	}
}

// TestSamplingCapSuppressesTheRecord prevents the failure where the
// webhook-deliveries task, whose ten-page cap is a deliberate sample of
// a delivery-log firehose, puts a false data-loss bullet in the report
// of every instance with a busy webhook (#574).
func TestSamplingCapSuppressesTheRecord(t *testing.T) {
	raw, requestCount := pagedServer(t, "/api/webhooks/deliveries", 20000, true)
	records := observeTruncation(raw)

	res, err := raw.GetPaginatedResult(context.Background(), PaginatedOpts{
		Path: "api/webhooks/deliveries", ResultKey: "items",
		MaxPageSize: 500, PageLimit: 10, SamplingCap: true,
		Scope: TruncationScope{Task: "getWebhookDeliveries"},
	})
	if err != nil {
		t.Fatalf("GetPaginatedResult failed: %v", err)
	}

	if *requestCount != 10 {
		t.Errorf("made %d request(s), want 10 (the cap must still apply)", *requestCount)
	}
	// The caller still learns the fetch was capped; only the artefact
	// and therefore the report stay clean.
	if !res.Truncated || res.Reason != ReasonPageLimitClamp {
		t.Errorf("Truncated/Reason: got %v/%q, want true/%q", res.Truncated, res.Reason, ReasonPageLimitClamp)
	}
	if len(*records) != 0 {
		t.Errorf("recorded %d truncation(s), want 0 — a deliberate sample is not data loss", len(*records))
	}
}

// TestNoObserverBehavesExactlyAsBefore prevents two failures at once:
// the 23 existing GetPaginated call sites changing behaviour because a
// truncation seam was added underneath them, and a truncated fetch
// staying silent at the call sites that have no observer — migrate and
// regtest share this client, so the unconditional warning is the only
// thing they get (#574).
func TestNoObserverBehavesExactlyAsBefore(t *testing.T) {
	logs := captureDefaultLogger(t)

	// Deliberately the same shape as TestRawClientPageLimit: total 5000
	// over 500-item pages is ten pages, capped at three.
	raw, requestCount := pagedServer(t, "/api/test/paged", 5000, true)

	items, err := raw.GetPaginated(context.Background(), PaginatedOpts{
		Path: "api/test/paged", ResultKey: "items", MaxPageSize: 500, PageLimit: 3,
	})
	if err != nil {
		t.Fatalf("GetPaginated failed: %v", err)
	}
	if *requestCount != 3 {
		t.Errorf("made %d request(s), want 3 (page limit)", *requestCount)
	}
	if len(items) != 1500 {
		t.Errorf("returned %d item(s), want 1500", len(items))
	}

	out := logs.String()
	for _, want := range []string{"truncated", string(ReasonPageLimitClamp), "lost=3500"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("truncation warning is missing %q; got: %s", want, out)
		}
	}
}
