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

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// issuePage504Transport simulates a load balancer returning HTTP 504
// on the second page whenever an issue request uses ps=500.
type issuePage504Transport struct {
	base     http.RoundTripper
	failures atomic.Int64
}

func (t *issuePage504Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/"+issuesSearchAPI &&
		req.URL.Query().Get("ps") == "500" &&
		req.URL.Query().Get("p") == "2" {

		t.failures.Add(1)

		return &http.Response{
			StatusCode: http.StatusGatewayTimeout,
			Status:     "504 Gateway Timeout",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("gateway timeout")),
			Request:    req,
		}, nil
	}

	return t.base.RoundTrip(req)
}

// installIssuePage504 makes the existing test server simulate the
// customer's page-size-dependent HTTP 504 without changing the
// shared issue corpus or its response semantics.
func installIssuePage504(e *Executor) *issuePage504Transport {
	client := e.Raw.HTTPClient()

	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	transport := &issuePage504Transport{base: base}
	client.Transport = transport

	return transport
}

// assertAdaptiveIssueRecovery verifies that every expected issue was
// delivered exactly once and no unexplained loss was recorded.
func assertAdaptiveIssueRecovery(
	t *testing.T,
	sink *issueCollector,
	tracker *TruncationTracker,
	expected int,
) {
	t.Helper()

	if got := sink.delivered(); got != expected {
		t.Errorf("delivered %d issues, want %d", got, expected)
	}

	counts := sink.keyCounts()
	if len(counts) != expected {
		t.Errorf("unique issue keys: got %d, want %d", len(counts), expected)
	}

	for key, count := range counts {
		if count != 1 {
			t.Errorf("issue %s delivered %d times, want once", key, count)
		}
	}

	state := tracker.State()

	if len(state.Records) != 0 {
		t.Errorf("unexpected truncation records: %+v", state.Records)
	}

	audit := onlyReconciliation(t, state)
	if audit.Unique != expected {
		t.Errorf("reconciliation unique: got %d, want %d", audit.Unique, expected)
	}
	if audit.UnexplainedDrift() != 0 {
		t.Errorf("unexplained drift: got %d, want 0", audit.UnexplainedDrift())
	}
}

// TestAdaptiveReSliceKeepsWindowDepth verifies that a window re-sliced
// after a page-size fallback keeps its place in the walk. Re-entered one
// level below maxSliceDepth, it must reach the depth backstop instead of
// restarting the count at zero and bisecting up to 48 levels further.
func TestAdaptiveReSliceKeepsWindowDepth(t *testing.T) {
	const total = 6000

	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, total, time.Second, "deep")

	e, _ := corpus.start()
	installIssuePage504(e)

	var sink issueCollector
	s := &issueSlicer{
		e:         e,
		base:      taskIssueParams(),
		sink:      sink.sink,
		scope:     TruncationScope{Task: issuesTaskName, ProjectKey: "p1", Branch: "main"},
		seen:      make(map[string]bool),
		pageSize:  issuePageSize,
		pageLimit: issuePageLimit,
		ceiling:   windowCeiling(issuePageLimit, issuePageSize),
	}

	// The window fits the 10,000 ceiling it was probed against, but the
	// 504 drops the page size to 200 and the ceiling to 4,000.
	w := issueWindow{start: corpusStart, end: corpusStart.Add(total * time.Second)}
	if err := s.fetchWindowItems(context.Background(), w, total, maxSliceDepth-1); err != nil {
		t.Fatalf("fetchWindowItems: %v", err)
	}

	if s.ceiling != 4000 {
		t.Fatalf("ceiling = %d, want 4000 after the fallback", s.ceiling)
	}
	if s.degradedReason != common.ReasonMaxDepth {
		t.Errorf("degradedReason = %q, want %q: the re-slice must keep the window's depth",
			s.degradedReason, common.ReasonMaxDepth)
	}
}

// TestAdaptiveIssueDateSlicingRecoversAfter504 verifies that a
// later-page HTTP 504 reduces ps from 500 to 200, lowers the
// effective ceiling from 10000 to 4000, and recovers every issue
// through date slicing.
func TestAdaptiveIssueDateSlicingRecoversAfter504(t *testing.T) {
	const total = 6000

	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, total, time.Second, "issue")

	e, tracker := corpus.start()
	transport := installIssuePage504(e)

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
		t.Fatalf("date-based issue extraction failed: %v", err)
	}

	if transport.failures.Load() == 0 {
		t.Fatal("expected an injected HTTP 504 at ps=500, page 2")
	}

	assertAdaptiveIssueRecovery(t, &sink, tracker, total)
}

// TestAdaptiveIssueFacetSlicingRecoversAfter504 verifies that
// a single-second burst is still recovered when a facet cell
// encounters HTTP 504 and its effective page ceiling decreases.
//
// Each type/severity group contains 6000 issues. The rule facet
// divides each group into two smaller, recoverable cells.
func TestAdaptiveIssueFacetSlicingRecoversAfter504(t *testing.T) {
	const perRule = 3000
	const total = perRule * 4

	corpus := newIssueCorpus(t)

	corpus.addBurstTagged(
		corpusStart, perRule,
		"bug1", "BUG", "MAJOR", "java:S1",
	)
	corpus.addBurstTagged(
		corpusStart, perRule,
		"bug2", "BUG", "MAJOR", "java:S2",
	)
	corpus.addBurstTagged(
		corpusStart, perRule,
		"smell1", "CODE_SMELL", "MINOR", "java:S3",
	)
	corpus.addBurstTagged(
		corpusStart, perRule,
		"smell2", "CODE_SMELL", "MINOR", "java:S4",
	)

	e, tracker := corpus.start()
	transport := installIssuePage504(e)

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
		t.Fatalf("facet-based issue extraction failed: %v", err)
	}

	if transport.failures.Load() == 0 {
		t.Fatal("expected an injected HTTP 504 at ps=500, page 2")
	}

	assertAdaptiveIssueRecovery(t, &sink, tracker, total)
}
