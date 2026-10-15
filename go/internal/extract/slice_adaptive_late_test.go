// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// lateSlice504Transport injects an HTTP 504 only after the issue
// slicer has entered either a date window or a facet cell.
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
// HTTP 504 during a date-window fetch causes re-slicing using
// the new 4,000-issue ceiling, without losing or duplicating
// any issues.
func TestAdaptiveLateDateFallbackRecoversAllIssues(t *testing.T) {
	const total = 12000

	corpus := newIssueCorpus(t)

	// Spread issues across time. The initial 10,000 ceiling
	// forces date slicing into approximately 6,000-issue
	// windows, which then require further slicing at ps=200.
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
}

// TestAdaptiveLateFacetFallbackRecoversAllIssues proves that
// a previously valid 6,000-issue facet cell is partitioned
// further after its effective ceiling falls to 4,000.
//
// The rule facet splits each 6,000-issue cell into two
// 3,000-issue cells.
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
}
