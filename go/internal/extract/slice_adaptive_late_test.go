// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// lateSlice504Transport injects an HTTP 504 only after the issue
// slicer has entered a date window, a type/severity facet cell, or a
// files-level facet cell.
//
// The initial root request is never affected.
type lateSlice504Transport struct {
	base     http.RoundTripper
	target   string
	failures atomic.Int64
}

func (tr *lateSlice504Transport) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	q := req.URL.Query()

	if req.URL.Path != "/"+issuesSearchAPI ||
		q.Get("ps") != "500" ||
		q.Get("p") != "2" {
		return tr.base.RoundTrip(req)
	}

	var shouldFail bool

	switch tr.target {
	case "date":
		// At least one date bound proves slicing has started.
		shouldFail = q.Get(createdAfterParam) != "" ||
			q.Get(createdBeforeParam) != ""

	case "facet":
		// Both filters prove the request is inside the
		// type/severity facet extraction path.
		shouldFail = q.Get(typesParam) != "" &&
			q.Get(severitiesParam) != ""

	case "file":
		// The files filter is the facet cascade's last level.
		shouldFail = q.Get(filesParam) != ""
	}

	if !shouldFail {
		return tr.base.RoundTrip(req)
	}

	tr.failures.Add(1)

	return &http.Response{
		StatusCode: http.StatusGatewayTimeout,
		Status:     "504 Gateway Timeout",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("gateway timeout")),
		Request:    req,
	}, nil
}

func installLateSlice504(
	e *Executor,
	target string,
) *lateSlice504Transport {
	client := e.Raw.HTTPClient()

	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	tr := &lateSlice504Transport{
		base:   base,
		target: target,
	}

	client.Transport = tr
	return tr
}

// assertLateSliceFallback verifies that HTTP 504 really occurred
// inside the requested slicing path, and that the smaller page
// size was subsequently used for that same path.
func assertLateSliceFallback(
	t *testing.T,
	corpus *issueCorpus,
	tr *lateSlice504Transport,
) {
	t.Helper()

	if got := tr.failures.Load(); got != 1 {
		t.Fatalf(
			"late HTTP 504 injections: got %d, want 1",
			got,
		)
	}

	foundSmallerPage := false

	for _, q := range corpus.allQueries() {
		if q.Get("ps") != "200" {
			continue
		}

		switch tr.target {
		case "date":
			if q.Get(createdAfterParam) != "" ||
				q.Get(createdBeforeParam) != "" {
				foundSmallerPage = true
			}

		case "facet":
			if q.Get(typesParam) != "" &&
				q.Get(severitiesParam) != "" {
				foundSmallerPage = true
			}
		}
	}

	if !foundSmallerPage {
		t.Errorf(
			"no successful ps=200 request found in %s slicing",
			tr.target,
		)
	}
}

// TestAdaptiveLateDateFallbackRecoversAllIssues proves that an
// HTTP 504 during a date-window fetch steps the window down to
// ps=200 with the 10,000-issue ceiling intact, without losing or
// duplicating any issues.
func TestAdaptiveLateDateFallbackRecoversAllIssues(t *testing.T) {
	const total = 12000

	corpus := newIssueCorpus(t)

	// Spread issues across time. The initial 10,000 ceiling
	// forces date slicing into approximately 6,000-issue
	// windows, which are then read whole at ps=200.
	corpus.addSpread(
		corpusStart,
		total,
		time.Second,
		"late-date",
	)

	e, tracker := corpus.start()
	tr := installLateSlice504(e, "date")

	var sink issueCollector

	err := fetchProjectIssues(
		context.Background(),
		e,
		"p1",
		"main",
		taskIssueParams(),
		sink.sink,
	)

	if err != nil {
		t.Fatalf("late date fallback failed: %v", err)
	}

	assertLateSliceFallback(t, corpus, tr)
	assertAdaptiveIssueRecovery(t, &sink, tracker, total)

	assertAuditCountsEveryRequest(t, corpus, tr, tracker)
}

// assertAuditCountsEveryRequest checks that the slicer's reconciliation
// counts every request it made, the pages of the attempt a fallback set
// aside and the injected 504 included. The 504 never reached the corpus
// server, so it is added back here.
func assertAuditCountsEveryRequest(t *testing.T, corpus *issueCorpus, tr *lateSlice504Transport, tracker *TruncationTracker) {
	t.Helper()
	audit := onlyReconciliation(t, tracker.State())
	if want := len(corpus.allQueries()) + int(tr.failures.Load()); audit.Requests != want {
		t.Errorf("reconciliation requests = %d, want %d (every request made)", audit.Requests, want)
	}
}

// TestAdaptiveLateFacetFallbackRecoversAllIssues proves that an
// HTTP 504 inside a 6,000-issue facet cell steps it down to ps=200
// without losing or duplicating any issues.
func TestAdaptiveLateFacetFallbackRecoversAllIssues(t *testing.T) {
	const perRule = 3000
	const total = perRule * 4

	corpus := newIssueCorpus(t)

	corpus.addBurstTagged(
		corpusStart,
		perRule,
		"late-bug-1",
		"BUG",
		"MAJOR",
		"java:S1",
	)

	corpus.addBurstTagged(
		corpusStart,
		perRule,
		"late-bug-2",
		"BUG",
		"MAJOR",
		"java:S2",
	)

	corpus.addBurstTagged(
		corpusStart,
		perRule,
		"late-smell-1",
		"CODE_SMELL",
		"MINOR",
		"java:S3",
	)

	corpus.addBurstTagged(
		corpusStart,
		perRule,
		"late-smell-2",
		"CODE_SMELL",
		"MINOR",
		"java:S4",
	)

	e, tracker := corpus.start()
	tr := installLateSlice504(e, "facet")

	var sink issueCollector

	err := fetchProjectIssues(
		context.Background(),
		e,
		"p1",
		"main",
		taskIssueParams(),
		sink.sink,
	)

	if err != nil {
		t.Fatalf("late facet fallback failed: %v", err)
	}

	assertLateSliceFallback(t, corpus, tr)
	assertAdaptiveIssueRecovery(t, &sink, tracker, total)
	assertAuditCountsEveryRequest(t, corpus, tr, tracker)
}

// TestAdaptiveFacetCellStaysCompleteAfterFallback proves that a facet
// cell that fitted the ceiling when probed still fits after a fallback:
// the raised page cap reads it whole at ps=200, so it is never
// partitioned by a finer facet and nothing is recorded as lost.
func TestAdaptiveFacetCellStaysCompleteAfterFallback(t *testing.T) {
	const perRule = 3000
	const total = perRule * 4

	corpus := newIssueCorpus(t)
	for i, cell := range []struct{ typ, sev, rule string }{
		{"BUG", "MAJOR", "java:S1"},
		{"BUG", "MAJOR", "java:S2"},
		{"CODE_SMELL", "MINOR", "java:S3"},
		{"CODE_SMELL", "MINOR", "java:S4"},
	} {
		corpus.addBurstTagged(corpusStart, perRule, fmt.Sprintf("whole-%d", i), cell.typ, cell.sev, cell.rule)
	}

	e, tracker := corpus.start()
	tr := installLateSlice504(e, "facet")

	var sink issueCollector
	if err := fetchProjectIssues(context.Background(), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("facet extraction failed: %v", err)
	}

	// The 504 fired inside a type/severity cell, and that cell was then
	// fetched at ps=200, so the checks below are not vacuous.
	assertLateSliceFallback(t, corpus, tr)

	// Each 6,000-issue cell is 30 pages at ps=200. Reading past page 20
	// is what the raised cap buys; a rule filter would mean the cell was
	// found over a lowered ceiling and partitioned.
	pastOldCap := false
	for _, q := range corpus.allQueries() {
		if q.Get(rulesParam) != "" {
			t.Errorf("cell partitioned by rule after the fallback: %v", q)
		}
		if q.Get("ps") == "200" && q.Get(typesParam) != "" && q.Get("p") == "30" {
			pastOldCap = true
		}
	}
	if !pastOldCap {
		t.Error("no type/severity cell was read to page 30 at ps=200")
	}
	assertAdaptiveIssueRecovery(t, &sink, tracker, total)
}

// TestAdaptiveLeafCellStaysCompleteAfterFallback proves that a
// files-level cell, with no finer facet left, is read whole and once
// after a fallback: no ps=200 page of either file is requested twice,
// and nothing is recorded as lost.
func TestAdaptiveLeafCellStaysCompleteAfterFallback(t *testing.T) {
	const perFile = 6000

	corpus := newIssueCorpus(t)
	corpus.addBurstFull(corpusStart, perFile, "leaf-a", "BUG", "MAJOR", "java:S1", "src", "src/A.java")
	corpus.addBurstFull(corpusStart, perFile, "leaf-b", "BUG", "MAJOR", "java:S1", "src", "src/B.java")

	e, tracker := corpus.start()
	tr := installLateSlice504(e, "file")

	var sink issueCollector
	if err := fetchProjectIssues(context.Background(), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("leaf extraction failed: %v", err)
	}
	if got := tr.failures.Load(); got != 1 {
		t.Fatalf("late HTTP 504 injections: got %d, want 1", got)
	}

	// 30 ps=200 pages per file, each read at most once. The first file
	// failed on page 2 at ps=500 and resumed from the 400 items its page
	// 1 held, at page 3 of 200, so its pages 1 and 2 at ps=200 are never
	// requested. The second file starts at the remembered ps=200.
	assertFilePagesReadOnce(t, corpus, 2*perFile/200-2)
	assertAdaptiveIssueRecovery(t, &sink, tracker, 2*perFile)
}

// TestAdaptiveTruncatedCellWithoutTotalIsPartitioned proves that a facet
// cell whose fetch comes back truncated with no readable total is
// partitioned and given up on using its probed count, rather than
// aborting the whole extraction. Without a total, a fetch can read only
// its first page, so each 3,000-issue leaf cell keeps 500 issues and
// records the other 2,500 as lost.
func TestAdaptiveTruncatedCellWithoutTotalIsPartitioned(t *testing.T) {
	const perCell = 3000
	const total = perCell * 4

	corpus := newIssueCorpus(t)
	corpus.omitTotalOnFilteredFetches = true
	for i, cell := range []struct{ typ, sev, rule string }{
		{"BUG", "MAJOR", "java:S1"},
		{"BUG", "MAJOR", "java:S2"},
		{"CODE_SMELL", "MINOR", "java:S3"},
		{"CODE_SMELL", "MINOR", "java:S4"},
	} {
		corpus.addBurstFull(corpusStart, perCell, fmt.Sprintf("nototal-%d", i), cell.typ, cell.sev, cell.rule,
			"src", fmt.Sprintf("src/F%d.java", i))
	}

	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(context.Background(), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("a cell without a readable total aborted the extraction: %v", err)
	}

	assertDeliveredOnce(t, &sink, 4*issuePageSize, total)
	state := tracker.State()
	if lost := recordedLoss(state); sink.delivered()+lost != total {
		t.Errorf("delivered %d + recorded lost %d = %d, want every one of %d issues accounted for",
			sink.delivered(), lost, sink.delivered()+lost, total)
	}
	if drift := onlyReconciliation(t, state).UnexplainedDrift(); drift != 0 {
		t.Errorf("unexplained drift: got %d, want 0", drift)
	}
}

// assertFilePagesReadOnce checks that no ps=200 page of a files-level
// cell was requested more than once, and that wantDistinct pages were.
func assertFilePagesReadOnce(t *testing.T, corpus *issueCorpus, wantDistinct int) {
	t.Helper()
	pageReads := make(map[string]int)
	for _, q := range corpus.allQueries() {
		if q.Get("ps") == "200" && q.Get(filesParam) != "" {
			pageReads[q.Get(filesParam)+" p="+q.Get("p")]++
		}
	}
	for page, n := range pageReads {
		if n != 1 {
			t.Errorf("%s read %d times at ps=200, want once", page, n)
		}
	}
	if len(pageReads) != wantDistinct {
		t.Errorf("distinct ps=200 file pages = %d, want %d", len(pageReads), wantDistinct)
	}
}

// recordedLoss sums the issues every truncation record declares lost.
func recordedLoss(state common.TruncationState) int {
	lost := 0
	for _, rec := range state.Records {
		lost += rec.Lost
	}
	return lost
}

// adopt follows the page size and cap a fetch reports, a failed one's
// last attempt included, and keeps them when a fetch reports none.
func TestIssueSlicerAdopt(t *testing.T) {
	s := &issueSlicer{pageSize: 500, pageLimit: 20}

	s.adopt(common.PageResult{Requests: 4, PageSize: 50, PageLimit: 200})
	if s.pageSize != 50 || s.pageLimit != 200 || s.requests != 4 {
		t.Errorf("after a failed fetch at 50: size=%d limit=%d requests=%d, want 50/200/4",
			s.pageSize, s.pageLimit, s.requests)
	}

	s.adopt(common.PageResult{})
	if s.pageSize != 50 || s.pageLimit != 200 || s.requests != 5 {
		t.Errorf("after a fetch that reported nothing: size=%d limit=%d requests=%d, want 50/200/5",
			s.pageSize, s.pageLimit, s.requests)
	}
}
