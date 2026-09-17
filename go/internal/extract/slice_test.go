// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// corpusStart is the creation second every corpus below is built
// around. It is a fixed whole second on purpose: the walk's arithmetic
// is second-aligned, and a corpus pinned to time.Now() would build a
// different window tree on every run.
var corpusStart = time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)

// corpusIssue is one issue in the fake instance: the key the dedup set
// keys on, and the creation date the windows select on.
type corpusIssue struct {
	key     string
	created time.Time
}

// issueCorpus is a stand-in for /api/issues/search that honours the
// semantics measured live against SonarQube 2026.4.1, because every one
// of them is a way this walk can silently lose data:
//
//   - createdAfter is INCLUSIVE, createdBefore is EXCLUSIVE, and an
//     omitted bound is unbounded — so an open edge really does mean "no
//     limit on this side", which is what makes the partition total;
//   - the ONLY accepted date format is SQDateLayout: a date-only bound,
//     a sub-second bound, an RFC3339 "Z" or a "+00:00" offset is HTTP
//     400, not a silently-adjusted query;
//   - a zero-width or inverted window is HTTP 400;
//   - ps is capped at 500, and p*ps past 10,000 is a REAL HTTP 400
//     rather than a short page;
//   - paging.total stays truthful above 10,000.
//
// Violations of the first three are reported through t.Errorf as they
// happen, naming the offending query, AND answered with the same 400
// the real server sends — so a test cannot pass because the walk
// quietly recovered from a request no real server would have answered.
type issueCorpus struct {
	t *testing.T

	mu         sync.Mutex
	issues     []corpusIssue // creation-date ascending after start()
	queries    []url.Values
	pages      int
	violations []string

	// ignoreDates reproduces the /api/hotspots/search shape: HTTP 200,
	// unchanged total, date parameters silently dropped.
	ignoreDates bool

	// rejectDatedStatus, when non-zero, answers every request that
	// carries a createdAfter / createdBefore bound with that HTTP
	// status while answering undated ones normally. It is the opposite
	// of ignoreDates and the shape #574 had no guard for: a server
	// whose date parsing differs from the one SQDateLayout was verified
	// against, or a WAF in front of one.
	rejectDatedStatus int

	// rejectEveryStatus, when non-zero, answers EVERY request with that
	// status, dated or not — a server that is simply down, which must
	// keep failing the way it always did.
	rejectEveryStatus int

	// extraTotal is added to the total reported for an UNDATED query
	// only, modelling a project whose own count disagrees with what its
	// windows add up to.
	extraTotal int

	// omitTotalOnInteriorWindows drops paging.total from the response to
	// any window carrying both bounds.
	omitTotalOnInteriorWindows bool

	// failAfterPages answers HTTP 500 once this many data pages (ps=500)
	// have been served. A window fetch reads at most 20 pages, so 20
	// guarantees at least one window reached the sink first.
	failAfterPages int

	// onRequest is called with the running request count, for tests that
	// need to interfere mid-walk.
	onRequest func(count int)
}

func newIssueCorpus(t *testing.T) *issueCorpus {
	t.Helper()
	return &issueCorpus{t: t}
}

// addBurst appends n issues created at the same instant — the shape a
// first analysis leaves behind, and the one date slicing cannot
// subdivide.
func (c *issueCorpus) addBurst(at time.Time, n int, prefix string) {
	for i := 0; i < n; i++ {
		c.issues = append(c.issues, corpusIssue{
			key:     fmt.Sprintf("%s-%d", prefix, i),
			created: at,
		})
	}
}

// addSpread appends n issues, step apart, starting at.
func (c *issueCorpus) addSpread(at time.Time, n int, step time.Duration, prefix string) {
	for i := 0; i < n; i++ {
		c.issues = append(c.issues, corpusIssue{
			key:     fmt.Sprintf("%s-%d", prefix, i),
			created: at.Add(time.Duration(i) * step),
		})
	}
}

// start serves the corpus and returns an executor wired to it, with a
// truncation tracker attached to both channels the slicer uses: the raw
// client's observer for response-level truncation, and Executor.Truncation
// for the walk-level records the client cannot know about.
func (c *issueCorpus) start() (*Executor, *TruncationTracker) {
	c.t.Helper()
	c.sortIssues()

	srv := httptest.NewServer(c)
	c.t.Cleanup(srv.Close)

	e := newTestExecutor(c.t)
	e.ServerURL = "http://test/"
	e.Raw = NewRawClient(srv.Client(), srv.URL+"/")
	tracker := NewTruncationTracker()
	e.Truncation = tracker
	e.Raw.SetTruncationObserver(tracker.Record)
	return e, tracker
}

// sortIssues puts the corpus in creation order, which is the order the
// real endpoint pages in and the order the range probe reads from both
// ends of. Callers that wire the corpus up themselves rather than
// through start() have to call it.
func (c *issueCorpus) sortIssues() {
	sort.SliceStable(c.issues, func(i, j int) bool {
		return c.issues[i].created.Before(c.issues[j].created)
	})
}

func (c *issueCorpus) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/"+issuesSearchAPI {
		http.NotFound(w, r)
		return
	}
	count, failed, hook := c.note(r.URL.Query())
	if hook != nil {
		hook(count)
	}
	if failed {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if status := c.refusalStatus(r.URL.Query()); status != 0 {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]string{{"msg": "Unrecognized value for parameter 'createdAfter'"}},
		})
		return
	}

	query, refusal := c.parseQuery(r.URL.Query())
	if refusal != "" {
		c.reject(w, refusal)
		return
	}
	c.writePage(w, query)
}

// note records one request and reports whether the injected failure is
// now armed. It also hands back the request hook, so nothing outside
// this function reads a field the test goroutine can write.
func (c *issueCorpus) note(q url.Values) (int, bool, func(int)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, q)
	if q.Get("ps") == strconv.Itoa(issuePageSize) {
		c.pages++
	}
	return len(c.queries), c.failAfterPages > 0 && c.pages > c.failAfterPages, c.onRequest
}

// refusalStatus reports the HTTP status this request must be refused
// with, 0 when it is to be answered normally. A refusal keyed on the
// presence of a date bound is the deployment #574 had no guard for:
// the undated query the tool sent before the change still works, and
// only the windows fail.
func (c *issueCorpus) refusalStatus(q url.Values) int {
	if c.rejectEveryStatus != 0 {
		return c.rejectEveryStatus
	}
	if c.rejectDatedStatus != 0 && (q.Get(createdAfterParam) != "" || q.Get(createdBeforeParam) != "") {
		return c.rejectDatedStatus
	}
	return 0
}

// corpusQuery is one parsed /api/issues/search request.
type corpusQuery struct {
	after, before       time.Time
	afterSet, beforeSet bool
	page, pageSize      int
	newestFirst         bool
}

// parseQuery validates a request the way the server does and returns
// the message it would refuse with, empty when the request is legal.
func (c *issueCorpus) parseQuery(q url.Values) (corpusQuery, string) {
	after, afterSet, afterOK := c.bound(q.Get(createdAfterParam))
	before, beforeSet, beforeOK := c.bound(q.Get(createdBeforeParam))
	if !afterOK || !beforeOK {
		return corpusQuery{}, "Invalid date format"
	}
	if afterSet && beforeSet && !after.Before(before) {
		c.violate("window createdAfter=%q is not before createdBefore=%q: the server answers HTTP 400",
			q.Get(createdAfterParam), q.Get(createdBeforeParam))
		return corpusQuery{}, "Start bound cannot be larger or equal to end bound"
	}

	parsed := corpusQuery{
		after: after, afterSet: afterSet,
		before: before, beforeSet: beforeSet,
		page:        atoiOrDefault(q.Get("p"), 1),
		pageSize:    atoiOrDefault(q.Get("ps"), 100),
		newestFirst: q.Get("s") == "CREATION_DATE" && q.Get("asc") == "false",
	}
	if parsed.pageSize > issuePageSize {
		c.violate("ps=%d exceeds the server's maximum of %d", parsed.pageSize, issuePageSize)
		parsed.pageSize = issuePageSize
	}
	// The cap is on the OFFSET, not on the total: this is the real 400
	// the walk has to stay inside, and the reason PageLimit is never
	// relaxed.
	if offset := parsed.page * parsed.pageSize; offset > common.ResultWindowLimit {
		return corpusQuery{}, fmt.Sprintf("Can return only the first %d results. %dth result asked.",
			common.ResultWindowLimit, offset)
	}
	return parsed, ""
}

// writePage answers a legal request: the window's issues, one page of
// them, and a paging total that stays truthful above 10,000.
func (c *issueCorpus) writePage(w http.ResponseWriter, q corpusQuery) {
	selected := c.selected(q)
	if q.newestFirst {
		for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
			selected[i], selected[j] = selected[j], selected[i]
		}
	}

	from := min((q.page-1)*q.pageSize, len(selected))
	to := min(from+q.pageSize, len(selected))
	items := make([]map[string]any, 0, to-from)
	for _, issue := range selected[from:to] {
		items = append(items, map[string]any{
			"key":          issue.key,
			"rule":         "java:S100",
			"creationDate": common.FormatSQDate(issue.created),
		})
	}

	body := map[string]any{issueResultKey: items}
	interior := q.afterSet && q.beforeSet
	if !interior || !c.omitTotalOnInteriorWindows {
		total := len(selected)
		if !q.afterSet && !q.beforeSet {
			total += c.extraTotal
		}
		body["paging"] = map[string]any{"pageIndex": q.page, "pageSize": q.pageSize, "total": total}
	}
	_ = json.NewEncoder(w).Encode(body)
}

// selected applies the window, half-open: createdAfter inclusive,
// createdBefore exclusive, an absent bound unbounded. The result is
// always a copy, so a reversing sort cannot corrupt the corpus.
func (c *issueCorpus) selected(q corpusQuery) []corpusIssue {
	out := make([]corpusIssue, 0, len(c.issues))
	for _, issue := range c.issues {
		if !c.ignoreDates && c.excluded(issue, q) {
			continue
		}
		out = append(out, issue)
	}
	return out
}

// excluded is the half-open window test on one issue.
func (c *issueCorpus) excluded(issue corpusIssue, q corpusQuery) bool {
	if q.afterSet && issue.created.Before(q.after) {
		return true
	}
	return q.beforeSet && !issue.created.Before(q.before)
}

// atoiOrDefault parses a query parameter the lenient way a server does.
func atoiOrDefault(raw string, fallback int) int {
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return n
	}
	return fallback
}

// bound parses a date parameter exactly as the server does: the sole
// accepted layout is SQDateLayout. Anything else is a 400 and a test
// failure naming the value — a date-only bound in particular means
// "< D+1day" on createdBefore and ">= D 00:00" on createdAfter, so the
// two halves of a split overlap by a day and double-count.
func (c *issueCorpus) bound(raw string) (time.Time, bool, bool) {
	if raw == "" {
		return time.Time{}, false, true
	}
	parsed, err := common.ParseSQDate(raw)
	if err != nil {
		c.violate("date bound %q is not layout %q, which the server rejects with HTTP 400",
			raw, common.SQDateLayout)
		return time.Time{}, true, false
	}
	return parsed, true, true
}

// violate reports a request the real server would have refused. It uses
// t.Error rather than t.Fatal deliberately: FailNow is only legal from
// the test goroutine and this runs on the server's.
func (c *issueCorpus) violate(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	c.mu.Lock()
	c.violations = append(c.violations, msg)
	c.mu.Unlock()
	c.t.Error(msg)
}

func (c *issueCorpus) reject(w http.ResponseWriter, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{"msg": msg}},
	})
}

func (c *issueCorpus) requestCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queries)
}

// allQueries returns every query the corpus was asked, in order.
func (c *issueCorpus) allQueries() []url.Values {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]url.Values(nil), c.queries...)
}

// datedQueries returns only the queries that carried a date bound.
func (c *issueCorpus) datedQueries() []url.Values {
	var dated []url.Values
	for _, q := range c.allQueries() {
		if q.Get(createdAfterParam) != "" || q.Get(createdBeforeParam) != "" {
			dated = append(dated, q)
		}
	}
	return dated
}

// issueCollector is an issueSink that keeps each window's chunk
// separately, so a test can assert both the union of everything
// delivered and the fact that it arrived window by window.
type issueCollector struct {
	chunks [][]json.RawMessage
}

func (c *issueCollector) sink(items []json.RawMessage) error {
	c.chunks = append(c.chunks, append([]json.RawMessage(nil), items...))
	return nil
}

func (c *issueCollector) delivered() int {
	n := 0
	for _, chunk := range c.chunks {
		n += len(chunk)
	}
	return n
}

// keyCounts maps each delivered key to how many times it arrived.
// Duplicates are the thing being measured, so they are counted rather
// than collapsed.
func (c *issueCollector) keyCounts() map[string]int {
	counts := make(map[string]int)
	for _, chunk := range c.chunks {
		for _, raw := range chunk {
			counts[extractField(raw, "key")]++
		}
	}
	return counts
}

// taskIssueParams is the parameter set projectIssuesFullTask builds, so
// the slicer tests exercise the real one: componentKeys rather than
// components (#400), the branch, the version-gated status filter and
// additionalFields=_all.
func taskIssueParams() url.Values {
	return url.Values{
		"componentKeys":    {"p1"},
		"branch":           {"main"},
		"ps":               {"500"},
		"additionalFields": {"_all"},
		"issueStatuses":    {"OPEN,CONFIRMED,FALSE_POSITIVE,ACCEPTED"},
	}
}

// recordsByReason indexes a state's records by reason, which is how
// every assertion below wants to read them.
func recordsByReason(state common.TruncationState) map[common.TruncationReason][]common.TruncationRecord {
	byReason := make(map[common.TruncationReason][]common.TruncationRecord)
	for _, rec := range state.Records {
		byReason[rec.Reason] = append(byReason[rec.Reason], rec)
	}
	return byReason
}

// onlyRecord fails unless the state holds exactly one record, and
// returns it.
func onlyRecord(t *testing.T, state common.TruncationState) common.TruncationRecord {
	t.Helper()
	if len(state.Records) != 1 {
		t.Fatalf("expected exactly 1 truncation record, got %d: %+v", len(state.Records), state.Records)
	}
	return state.Records[0]
}

// onlyReconciliation fails unless the state holds exactly one
// reconciliation, and returns it.
func onlyReconciliation(t *testing.T, state common.TruncationState) common.Reconciliation {
	t.Helper()
	if len(state.Reconciliations) != 1 {
		t.Fatalf("expected exactly 1 reconciliation, got %d: %+v",
			len(state.Reconciliations), state.Reconciliations)
	}
	return state.Reconciliations[0]
}

// TestMidpointIsSecondAlignedAndStrictlyInside prevents the two ways
// midpoint arithmetic loses data: a sub-second bound, which SonarQube
// rejects with HTTP 400 rather than rounding, and a midpoint equal to
// one of its parent's bounds, which produces a child window identical
// to its parent and a recursion that never terminates (#574).
func TestMidpointIsSecondAlignedAndStrictlyInside(t *testing.T) {
	cases := []struct {
		name       string
		start, end time.Time
	}{
		{"two-second window, the narrowest splittable one", corpusStart, corpusStart.Add(2 * time.Second)},
		{"odd span, so the halving has a remainder", corpusStart, corpusStart.Add(3 * time.Second)},
		{"sub-minute", corpusStart, corpusStart.Add(59 * time.Second)},
		{"an hour", corpusStart, corpusStart.Add(time.Hour)},
		{"span with a sub-second component", corpusStart, corpusStart.Add(90*time.Minute + 500*time.Millisecond)},
		{"unaligned start at .999", corpusStart.Add(999 * time.Millisecond), corpusStart.Add(4*time.Second + 999*time.Millisecond)},
		{"a year", corpusStart, corpusStart.AddDate(1, 0, 0)},
		{"the default range this walk falls back to", time.Date(2006, time.January, 1, 0, 0, 0, 0, time.UTC), corpusStart},
	}
	for _, tc := range cases {
		w := issueWindow{start: tc.start, end: tc.end}
		mid := w.mid()
		if mid.Nanosecond() != 0 {
			t.Errorf("%s: mid() of [%s, %s) is not second-aligned: %s carries %d nanoseconds",
				tc.name, tc.start, tc.end, mid, mid.Nanosecond())
		}
		if !mid.After(tc.start) || !mid.Before(tc.end) {
			t.Errorf("%s: mid() of [%s, %s) is %s, want strictly inside both bounds",
				tc.name, tc.start, tc.end, mid)
		}
	}
}

// TestOneSecondWindowIsNotSplittable pins the terminator to "one second
// wide" instead of "start == end". A zero-width window is HTTP 400
// ("Start bound cannot be larger or equal to end bound"), so a walk
// that only stops at equality fails on the request before the one it
// meant to refuse, and one second is the narrowest range the API can
// express (#574).
func TestOneSecondWindowIsNotSplittable(t *testing.T) {
	cases := []struct {
		span time.Duration
		want bool
	}{
		{0, false},
		{time.Second, false},
		{time.Second + time.Millisecond, true},
		{2 * time.Second, true},
		{time.Hour, true},
		{-time.Second, false},
	}
	for _, tc := range cases {
		w := issueWindow{start: corpusStart, end: corpusStart.Add(tc.span)}
		if got := w.splittable(); got != tc.want {
			t.Errorf("issueWindow spanning %s: splittable() = %t, want %t", tc.span, got, tc.want)
		}
	}
}

// TestSplitEmitsOneIdenticalMidpointString is the CloudVoyager e5fbecd0
// regression. That commit added +1ms to every non-first window start on
// the mistaken premise that both bounds are inclusive; whenever the
// midpoint's millisecond component was 999 the addition rolled into the
// next second and an entire second of issues fell between the two
// windows, silently, on roughly one seam in a thousand. The left
// window's exclusive upper bound must be byte-for-byte the right
// window's inclusive lower bound (#574).
func TestSplitEmitsOneIdenticalMidpointString(t *testing.T) {
	cases := []struct {
		name       string
		start, end time.Time
	}{
		{"aligned bounds", corpusStart, corpusStart.Add(4 * time.Second)},
		{"midpoint lands on .999", corpusStart.Add(999 * time.Millisecond), corpusStart.Add(4*time.Second + 999*time.Millisecond)},
		{"midpoint lands on .500", corpusStart, corpusStart.Add(3 * time.Second)},
		{"a wide window", corpusStart, corpusStart.AddDate(0, 9, 0)},
	}
	base := taskIssueParams()
	for _, tc := range cases {
		left, right := splitWindow(issueWindow{start: tc.start, end: tc.end})
		leftEnd := common.FormatSQDate(left.end)
		rightStart := common.FormatSQDate(right.start)
		if leftEnd != rightStart {
			t.Errorf("%s: seam of [%s, %s) formats as %q on the left and %q on the right; a whole second falls between them",
				tc.name, tc.start, tc.end, leftEnd, rightStart)
		}
		if !left.end.Equal(right.start) {
			t.Errorf("%s: seam of [%s, %s) is %s on the left and %s on the right; both sides must be the same instant",
				tc.name, tc.start, tc.end, left.end, right.start)
		}
		// The wire parameters are what the server sees, so they are what
		// has to agree.
		sent := windowParams(base, left).Get(createdBeforeParam)
		received := windowParams(base, right).Get(createdAfterParam)
		if sent != received {
			t.Errorf("%s: createdBefore=%q on the left and createdAfter=%q on the right", tc.name, sent, received)
		}
	}
}

// TestRootWindowSendsNoDateParams prevents the walk acquiring an outer
// bound. The root window IS the query this task sent before #574: an
// omitted bound is unbounded, so a root that sends no date parameter at
// all cannot exclude an issue, whatever a clock or a range probe says.
// The Del calls matter as much as the absence — a stale bound inherited
// from the caller would silently bound a window meant to be open (#574).
func TestRootWindowSendsNoDateParams(t *testing.T) {
	base := taskIssueParams()
	base.Set(createdAfterParam, "2020-01-01T00:00:00+0000")
	base.Set(createdBeforeParam, "2021-01-01T00:00:00+0000")

	params := windowParams(base, rootIssueWindow())
	for _, key := range []string{createdAfterParam, createdBeforeParam} {
		if got := params.Get(key); got != "" {
			t.Errorf("the root window must send no %s, got %q", key, got)
		}
	}
	for key, want := range map[string]string{
		"componentKeys":    "p1",
		"branch":           "main",
		"additionalFields": "_all",
		"issueStatuses":    "OPEN,CONFIRMED,FALSE_POSITIVE,ACCEPTED",
	} {
		if got := params.Get(key); got != want {
			t.Errorf("root window params %s: got %q, want %q", key, got, want)
		}
	}
	// The caller's map must come back untouched: it is reused for the
	// next window.
	if got := base.Get(createdAfterParam); got != "2020-01-01T00:00:00+0000" {
		t.Errorf("windowParams mutated the caller's params: createdAfter is now %q", got)
	}
}

// TestOuterEdgesStayOpenThroughEveryLevelOfBisection is the invariant
// the whole design rests on. The leftmost window of the tree must never
// acquire a createdAfter and the rightmost must never acquire a
// createdBefore, at any depth: that is what makes the partition total
// by construction, and what means an issue created during the run lands
// in a window rather than in a gap (#574).
func TestOuterEdgesStayOpenThroughEveryLevelOfBisection(t *testing.T) {
	base := taskIssueParams()
	leftmost := issueWindow{start: corpusStart, end: corpusStart.AddDate(1, 0, 0), openStart: true, openEnd: true}
	rightmost := leftmost

	for depth := 1; depth <= 20; depth++ {
		left, right := splitWindow(leftmost)
		leftmost = left
		assertLeftmostEdgeStaysOpen(t, base, leftmost, depth)
		// The interior sibling carries both bounds, and only interior
		// windows ever do.
		if right.openStart || (depth == 1 && !right.openEnd) {
			t.Fatalf("depth %d: unexpected edges on the right sibling: %s", depth, right.label())
		}

		_, r := splitWindow(rightmost)
		rightmost = r
		assertRightmostEdgeStaysOpen(t, base, rightmost, depth)
	}
}

// assertLeftmostEdgeStaysOpen checks the invariant that makes the
// partition total: however deep the bisection goes, the window on the
// far left never acquires a lower bound, so no issue can be older than
// the range the walk covers.
func assertLeftmostEdgeStaysOpen(t *testing.T, base url.Values, w issueWindow, depth int) {
	t.Helper()
	if !w.openStart {
		t.Fatalf("depth %d: the leftmost window closed its lower edge: %s", depth, w.label())
	}
	if w.openEnd {
		t.Fatalf("depth %d: the leftmost window kept an open upper edge, so it overlaps its sibling: %s",
			depth, w.label())
	}
	if got := windowParams(base, w).Get(createdAfterParam); got != "" {
		t.Fatalf("depth %d: the leftmost window sent createdAfter=%q", depth, got)
	}
}

// assertRightmostEdgeStaysOpen is the mirror of the above: the window
// on the far right never acquires an upper bound, so an issue created
// while the walk is still running still lands inside it.
func assertRightmostEdgeStaysOpen(t *testing.T, base url.Values, w issueWindow, depth int) {
	t.Helper()
	if !w.openEnd {
		t.Fatalf("depth %d: the rightmost window closed its upper edge: %s", depth, w.label())
	}
	if w.openStart {
		t.Fatalf("depth %d: the rightmost window kept an open lower edge: %s", depth, w.label())
	}
	if got := windowParams(base, w).Get(createdBeforeParam); got != "" {
		t.Fatalf("depth %d: the rightmost window sent createdBefore=%q", depth, got)
	}
}

// TestWindowCeilingFollowsTheEffectivePageCap keeps the split threshold
// and the fetch that has to satisfy it on one derivation. A threshold
// higher than what a single capped fetch can return would leave windows
// that are never split and never complete; a second hardcoded constant
// would drift from PageLimit the first time anyone changed it (#574).
func TestWindowCeilingFollowsTheEffectivePageCap(t *testing.T) {
	cases := []struct {
		pageLimit, pageSize, want int
	}{
		{issuePageLimit, issuePageSize, common.ResultWindowLimit},
		{20, 500, 10000},
		{3, 500, 1500},
		{100, 500, common.ResultWindowLimit},
		{0, 500, common.ResultWindowLimit},
		{20, 0, common.ResultWindowLimit},
	}
	for _, tc := range cases {
		if got := windowCeiling(tc.pageLimit, tc.pageSize); got != tc.want {
			t.Errorf("windowCeiling(%d, %d) = %d, want %d", tc.pageLimit, tc.pageSize, got, tc.want)
		}
	}
}

// TestTheAllowlistExcludesHotspots guards the second of the three
// independent guards against SPEC-006's worst trap.
// /api/hotspots/search declares no createdAfter/createdBefore and
// silently ignores unknown parameters, so a date slicer pointed at it
// recurses forever over the same truncated set while its log reports
// completeness — which is exactly what CloudVoyager did by reusing the
// issues path verbatim (#574).
func TestTheAllowlistExcludesHotspots(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{issuesSearchAPI, true},
		{"api/hotspots/search", false},
		{"api/measures/component_tree", false},
		{"api/project_analyses/search", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := dateSliceableEndpoint(tc.path); got != tc.want {
			t.Errorf("dateSliceableEndpoint(%q) = %t, want %t", tc.path, got, tc.want)
		}
	}
}

// TestUnderTheCeilingMakesNoExtraRequestAndSendsNoDates is the
// no-regression contract for the overwhelming majority of projects. The
// slicing trigger is the clamp itself, not a probe, so a project the old
// code handled correctly must cost exactly the same requests, carry no
// date parameter on any of them, and arrive in one chunk (#574).
func TestUnderTheCeilingMakesNoExtraRequestAndSendsNoDates(t *testing.T) {
	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, 1000, time.Second, "iss")
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues: %v", err)
	}

	// 1,000 issues at 500 per page is two pages, which is what the
	// unsliced fetch cost.
	if got := corpus.requestCount(); got != 2 {
		t.Errorf("requests: got %d, want 2 (the same as before slicing existed): %v", got, corpus.allQueries())
	}
	if dated := corpus.datedQueries(); len(dated) != 0 {
		t.Errorf("a project under the ceiling must send no date parameter, got %v", dated)
	}
	if len(sink.chunks) != 1 {
		t.Errorf("expected one chunk, got %d", len(sink.chunks))
	}
	if got := sink.delivered(); got != 1000 {
		t.Errorf("delivered: got %d, want 1000", got)
	}
	if tracker.HasRecords() {
		t.Errorf("an untruncated fetch must record nothing, got %+v", tracker.State().Records)
	}
	if recs := tracker.State().Reconciliations; len(recs) != 0 {
		t.Errorf("an unsliced fetch must not reconcile, got %+v", recs)
	}
}

// TestExactlyTenThousandIsNotSliced pins the predicate to "total >
// ceiling", never ">=". Offset 10,000 is live-confirmed reachable
// (p=20&ps=500 returns HTTP 200), so a project with exactly 10,000
// issues is completely fetchable and slicing it would spend dozens of
// requests to arrive at the same set (#574).
func TestExactlyTenThousandIsNotSliced(t *testing.T) {
	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, common.ResultWindowLimit, time.Second, "iss")
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues: %v", err)
	}

	if got := corpus.requestCount(); got != issuePageLimit {
		t.Errorf("requests: got %d, want %d (20 pages, no slicing)", got, issuePageLimit)
	}
	if dated := corpus.datedQueries(); len(dated) != 0 {
		t.Errorf("exactly %d issues must not be sliced, got dated queries %v", common.ResultWindowLimit, dated)
	}
	if got := sink.delivered(); got != common.ResultWindowLimit {
		t.Errorf("delivered: got %d, want %d", got, common.ResultWindowLimit)
	}
	if tracker.HasRecords() {
		t.Errorf("a complete fetch must record nothing, got %+v", tracker.State().Records)
	}
}

// TestSlicesAndRecoversEveryIssueWithZeroDuplicates is the whole point
// of the change, on the measured shape of a real migrated project:
// 14,903 issues of which 8,916 share the single second its first
// analysis stamped. Before this, 4,903 of them were dropped and the run
// reported success. Duplicates are asserted at zero because the dedup
// set is a verifier, not a workaround — a half-open partition cannot
// return the same issue twice, so a duplicate would mean the seams are
// not what this code believes (#574).
func TestSlicesAndRecoversEveryIssueWithZeroDuplicates(t *testing.T) {
	const (
		burst  = 8916
		spread = 5987
		total  = burst + spread
	)
	corpus := newIssueCorpus(t)
	corpus.addBurst(corpusStart, burst, "burst")
	corpus.addSpread(corpusStart.Add(time.Hour), spread, time.Hour, "spread")
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues: %v", err)
	}

	counts := sink.keyCounts()
	if len(counts) != total {
		t.Errorf("distinct issue keys delivered: got %d, want %d", len(counts), total)
	}
	for key, n := range counts {
		if n != 1 {
			t.Errorf("issue %s was delivered %d times; a half-open partition must deliver each issue once", key, n)
		}
	}
	if got := sink.delivered(); got != total {
		t.Errorf("delivered: got %d, want %d", got, total)
	}
	if len(sink.chunks) < 2 {
		t.Errorf("expected the issues to arrive window by window, got %d chunk(s)", len(sink.chunks))
	}

	state := tracker.State()
	if len(state.Records) != 0 {
		t.Errorf("nothing was lost, so nothing may be recorded: got %+v", state.Records)
	}
	audit := onlyReconciliation(t, state)
	if audit.TotalBefore != total || audit.Unique != total {
		t.Errorf("reconciliation: before=%d unique=%d, want %d for both", audit.TotalBefore, audit.Unique, total)
	}
	if audit.Duplicates != 0 || audit.Keyless != 0 {
		t.Errorf("reconciliation: duplicates=%d keyless=%d, want 0 for both", audit.Duplicates, audit.Keyless)
	}
	if audit.Drift() != 0 || audit.UnexplainedDrift() != 0 {
		t.Errorf("reconciliation: drift=%d unexplained=%d, want 0 for both", audit.Drift(), audit.UnexplainedDrift())
	}
	if audit.Windows < 2 || audit.Requests < audit.Windows {
		t.Errorf("reconciliation: windows=%d requests=%d look implausible", audit.Windows, audit.Requests)
	}
}

// TestNeverIssuesAZeroWidthWindow covers both halves of the guard: the
// predicate, and every request a real walk makes. A zero-width window
// is HTTP 400 on a real server, and the comparison has to be on the
// FORMATTED strings rather than the time.Time values, because two
// instants a microsecond apart are two different times and one
// identical second (#574).
func TestNeverIssuesAZeroWidthWindow(t *testing.T) {
	cases := []struct {
		name string
		w    issueWindow
		want bool
	}{
		{"identical bounds", issueWindow{start: corpusStart, end: corpusStart}, false},
		{"bounds inside one second", issueWindow{start: corpusStart, end: corpusStart.Add(time.Millisecond)}, false},
		{"one second apart", issueWindow{start: corpusStart, end: corpusStart.Add(time.Second)}, true},
		{"open lower edge", issueWindow{start: corpusStart, end: corpusStart, openStart: true}, true},
		{"open upper edge", issueWindow{start: corpusStart, end: corpusStart, openEnd: true}, true},
	}
	for _, tc := range cases {
		if got := tc.w.safeToRequest(); got != tc.want {
			t.Errorf("%s: safeToRequest() = %t, want %t", tc.name, got, tc.want)
		}
	}

	// The corpus reports any zero-width or inverted window through
	// t.Error and answers it with the same 400 the server sends, at any
	// depth of the walk.
	corpus := newIssueCorpus(t)
	corpus.addBurst(corpusStart, 8916, "burst")
	corpus.addSpread(corpusStart.Add(time.Second), 3500, time.Second, "spread")
	e, _ := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues: %v", err)
	}
	if len(corpus.datedQueries()) == 0 {
		t.Fatal("no window was ever requested, so this test proved nothing")
	}
}

// TestNeverSendsADateOnlyOrSubSecondBound guards the format the API
// actually accepts. A date-only createdBefore means "< D+1day" while a
// date-only createdAfter means ">= D 00:00", so the two halves of a
// split overlap by a whole day and double-count (measured: 14,904
// issues across a split of a 14,903-issue project). An RFC3339 "Z", a
// "+00:00" offset, a fractional second and a missing offset are all
// HTTP 400 (#574).
func TestNeverSendsADateOnlyOrSubSecondBound(t *testing.T) {
	corpus := newIssueCorpus(t)
	corpus.addBurst(corpusStart, 8916, "burst")
	corpus.addSpread(corpusStart.Add(time.Second), 3500, time.Second, "spread")
	e, _ := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues: %v", err)
	}

	seen := 0
	for _, q := range corpus.allQueries() {
		seen += assertQueryDateBoundsAreWireLegal(t, q)
	}
	if seen == 0 {
		t.Fatal("no date bound was ever sent, so this test proved nothing")
	}
}

// sqWireDateLayout is the only shape /api/issues/search accepts for a
// date bound. Date-only is accepted by the server but double-counts the
// split day, so it is banned here too.
var sqWireDateLayout = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{4}$`)

// assertQueryDateBoundsAreWireLegal checks both bounds of one request
// and returns how many it found, so the caller can prove the walk
// actually sent some.
func assertQueryDateBoundsAreWireLegal(t *testing.T, q url.Values) int {
	t.Helper()
	seen := 0
	for _, key := range []string{createdAfterParam, createdBeforeParam} {
		bound := q.Get(key)
		if bound == "" {
			continue
		}
		seen++
		if !sqWireDateLayout.MatchString(bound) {
			t.Errorf("%s=%q does not match the only layout the API accepts", key, bound)
		}
		for _, bad := range []string{"Z", ".", "+00:00", " "} {
			if strings.Contains(bound, bad) {
				t.Errorf("%s=%q contains %q, which the API rejects with HTTP 400", key, bound, bad)
			}
		}
	}
	return seen
}

// TestAtomicSecondRecordsTruncationAndStops is the test that proves the
// warning still works after slicing was added. 22,000 issues sharing
// one creation second is the shape date bisection cannot subdivide: the
// narrowest range the API can express already holds too much. The run
// must fetch the ceiling's worth, terminate in a bounded number of
// requests, and say exactly which second and exactly how many issues it
// could not reach — that record is the only measurement of this
// distribution anyone will have (#574).
func TestAtomicSecondRecordsTruncationAndStops(t *testing.T) {
	const total = 22000
	corpus := newIssueCorpus(t)
	corpus.addBurst(corpusStart, total, "burst")
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues must not fail on an atomic second: %v", err)
	}

	if got := sink.delivered(); got != common.ResultWindowLimit {
		t.Errorf("delivered: got %d, want %d (everything the API will return)", got, common.ResultWindowLimit)
	}
	// Every issue shares one second, so the range probe cannot produce a
	// splittable range and the walk bisects the default one instead:
	// about two one-row probes per level for some 31 levels, plus 20
	// data pages. Bounded is the property under test, not the exact
	// number.
	if got := corpus.requestCount(); got > 150 {
		t.Errorf("requests: got %d, want a bounded walk (<= 150)", got)
	}

	state := tracker.State()
	rec := onlyRecord(t, state)
	if rec.Reason != common.ReasonAtomicWindow {
		t.Errorf("reason: got %q, want %q", rec.Reason, common.ReasonAtomicWindow)
	}
	if rec.Endpoint != issuesSearchAPI {
		t.Errorf("endpoint: got %q, want %q", rec.Endpoint, issuesSearchAPI)
	}
	want := TruncationScope{Task: issuesTaskName, ProjectKey: "p1", Branch: "main"}
	if rec.Scope != want {
		t.Errorf("scope: got %+v, want %+v", rec.Scope, want)
	}
	if rec.Total != total || !rec.TotalKnown {
		t.Errorf("total: got %d (known=%t), want %d (known=true)", rec.Total, rec.TotalKnown, total)
	}
	if rec.Fetched != common.ResultWindowLimit || rec.Lost != total-common.ResultWindowLimit {
		t.Errorf("fetched/lost: got %d/%d, want %d/%d",
			rec.Fetched, rec.Lost, common.ResultWindowLimit, total-common.ResultWindowLimit)
	}
	if got, want := rec.WindowStart, common.FormatSQDate(corpusStart); got != want {
		t.Errorf("windowStart: got %q, want %q - the record has to name the exact second", got, want)
	}
	if got, want := rec.WindowEnd, common.FormatSQDate(corpusStart.Add(time.Second)); got != want {
		t.Errorf("windowEnd: got %q, want %q", got, want)
	}
	if state.TotalLost != total-common.ResultWindowLimit {
		t.Errorf("totalLost: got %d, want %d", state.TotalLost, total-common.ResultWindowLimit)
	}
}

// TestRefusesAnEndpointThatIgnoresDateParams is the runtime half of the
// hotspots trap, and the guard that catches any endpoint with that
// behaviour even if someone widens the allowlist. An endpoint that
// answers HTTP 200 with an unchanged total for both halves of a split
// has dropped the parameters, and recursing on it would loop forever
// over the same truncated set while claiming completeness. The refusal
// must cost a couple of probes, fall back to the capped fetch, record
// the true reason — and NOT fail the extract, because
// isNonFatalHTTPErr covers 403 and 404 only, so an error from here
// would abort a multi-hour run (#574).
func TestRefusesAnEndpointThatIgnoresDateParams(t *testing.T) {
	const total = 12000
	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, total, time.Minute, "iss")
	corpus.ignoreDates = true
	e, tracker := corpus.start()

	var logs bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&logs, nil))

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("a refusal must not fail the extract: %v", err)
	}

	if dated := corpus.datedQueries(); len(dated) != 2 {
		t.Errorf("the refusal must cost exactly the two depth-0 probes, got %d dated requests: %v", len(dated), dated)
	}
	if got := corpus.requestCount(); got > 40 {
		t.Errorf("requests: got %d, want a bounded refusal (<= 40)", got)
	}
	if got := sink.delivered(); got != common.ResultWindowLimit {
		t.Errorf("delivered: got %d, want the capped fetch's %d", got, common.ResultWindowLimit)
	}

	rec := onlyRecord(t, tracker.State())
	if rec.Reason != common.ReasonDatesIgnored {
		t.Errorf("reason: got %q, want %q", rec.Reason, common.ReasonDatesIgnored)
	}
	if rec.Total != total || rec.Fetched != common.ResultWindowLimit || rec.Lost != total-common.ResultWindowLimit {
		t.Errorf("record: total=%d fetched=%d lost=%d, want %d/%d/%d",
			rec.Total, rec.Fetched, rec.Lost, total, common.ResultWindowLimit, total-common.ResultWindowLimit)
	}
	for _, want := range []string{"ignored its date parameters", "falling back to the capped fetch"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log does not mention %q:\n%s", want, logs.String())
		}
	}
}

// TestBenignChurnDoesNotDisableSlicing prevents the strictest form of
// the partition self-check from switching the feature off on a busy
// instance. Issues are created and closed while a walk runs, so two
// halves that fail to SUM to their parent is the normal state of the
// world; only both halves reporting the whole parent is the
// unambiguous ignores-dates signature. The residual still has to
// surface somewhere, which is what the closing reconciliation is for
// (#574).
func TestBenignChurnDoesNotDisableSlicing(t *testing.T) {
	const realTotal = 12000
	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, realTotal, time.Minute, "iss")
	// The project's own count is one higher than its windows can add up
	// to, so left+right != parent at depth 0.
	corpus.extraTotal = 1
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues: %v", err)
	}

	counts := sink.keyCounts()
	if len(counts) != realTotal {
		t.Errorf("distinct issue keys delivered: got %d, want %d - a one-issue disagreement must not disable slicing",
			len(counts), realTotal)
	}
	if len(corpus.datedQueries()) <= 2 {
		t.Errorf("the walk never recursed past the depth-0 probes: %v", corpus.datedQueries())
	}

	state := tracker.State()
	if byReason := recordsByReason(state); len(byReason[common.ReasonDatesIgnored]) != 0 {
		t.Errorf("churn must never be reported as an endpoint that ignores dates: %+v", byReason[common.ReasonDatesIgnored])
	}
	// The one issue the source claims and never produced is not
	// swallowed: it is reported as an accounting residual, which is a
	// statement about the count and not a claim that data is absent.
	rec := onlyRecord(t, state)
	if rec.Reason != common.ReasonCountDrift {
		t.Errorf("reason: got %q, want %q", rec.Reason, common.ReasonCountDrift)
	}
	if rec.Lost != 1 {
		t.Errorf("unaccounted issues: got %d, want 1", rec.Lost)
	}
}

// TestWindowParamsPreserveComponentKeysBranchStatusesAndAdditionalFields
// is the #400 regression, at every depth of the walk. The project
// filter has to stay componentKeys: SQ 9.9 silently ignores components
// and answers with the GLOBAL issue set, which then gets enriched per
// project and pollutes every project's import. An empty components=
// returns the whole instance. The branch, the version-gated status
// filter and additionalFields=_all are just as mandatory, which is why
// this clones the caller's parameters and touches only the two date
// keys (#574).
func TestWindowParamsPreserveComponentKeysBranchStatusesAndAdditionalFields(t *testing.T) {
	base := taskIssueParams()
	interior := issueWindow{start: corpusStart, end: corpusStart.Add(time.Hour)}

	cases := []struct {
		name       string
		w          issueWindow
		wantAfter  string
		wantBefore string
	}{
		{"interior window carries both bounds", interior, common.FormatSQDate(interior.start), common.FormatSQDate(interior.end)},
		{"leftmost window carries only the upper bound", issueWindow{start: interior.start, end: interior.end, openStart: true}, "", common.FormatSQDate(interior.end)},
		{"rightmost window carries only the lower bound", issueWindow{start: interior.start, end: interior.end, openEnd: true}, common.FormatSQDate(interior.start), ""},
	}
	for _, tc := range cases {
		params := windowParams(base, tc.w)
		for key, want := range map[string]string{
			"componentKeys":    "p1",
			"branch":           "main",
			"additionalFields": "_all",
			"issueStatuses":    "OPEN,CONFIRMED,FALSE_POSITIVE,ACCEPTED",
			"ps":               "500",
		} {
			if got := params.Get(key); got != want {
				t.Errorf("%s: %s = %q, want %q", tc.name, key, got, want)
			}
		}
		if _, ok := params["components"]; ok {
			t.Errorf("%s: params must never carry components (SQ 9.9 answers with the global issue set): %v", tc.name, params)
		}
		if got := params.Get(createdAfterParam); got != tc.wantAfter {
			t.Errorf("%s: createdAfter = %q, want %q", tc.name, got, tc.wantAfter)
		}
		if got := params.Get(createdBeforeParam); got != tc.wantBefore {
			t.Errorf("%s: createdBefore = %q, want %q", tc.name, got, tc.wantBefore)
		}
	}
}

// TestProbePreservesEveryCallerParam extends the #400 guard to the
// cheap probes, which is where it is easiest to get wrong: a probe
// built from scratch, or one with the payload parameters stripped for
// speed, would count a different selection than the fetch reads — and
// with componentKeys missing it would count the whole instance and
// slice a project into a hundred windows of somebody else's issues
// (#574).
func TestProbePreservesEveryCallerParam(t *testing.T) {
	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, 12000, time.Minute, "iss")
	e, _ := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues: %v", err)
	}

	rangeProbes := 0
	for i, q := range corpus.allQueries() {
		for key, want := range map[string]string{
			"componentKeys":    "p1",
			"branch":           "main",
			"additionalFields": "_all",
			"issueStatuses":    "OPEN,CONFIRMED,FALSE_POSITIVE,ACCEPTED",
		} {
			if got := q.Get(key); got != want {
				t.Errorf("request %d (%s): %s = %q, want %q", i+1, q.Encode(), key, got, want)
			}
		}
		if _, ok := q["components"]; ok {
			t.Errorf("request %d carries components: %s", i+1, q.Encode())
		}
		if q.Get("s") == "CREATION_DATE" {
			rangeProbes++
			if got := q.Get("ps"); got != "1" {
				t.Errorf("the range probe must read one row, got ps=%q", got)
			}
		}
	}
	if rangeProbes != 2 {
		t.Errorf("range probes: got %d, want 2 (oldest and newest)", rangeProbes)
	}
}

// TestUnparseableTotalIsAnErrorNotAnEmptyWindow prevents the quietest
// failure in the whole design. ExtractTotal returns 0 for an unmarshal
// failure, for a missing key and for a genuine zero alike, so a walk
// that reads 0 as "this window is empty" skips every window of a
// project whose responses it cannot parse and reports success with
// nothing written (#574).
func TestUnparseableTotalIsAnErrorNotAnEmptyWindow(t *testing.T) {
	const total = 25000
	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, total, 10*time.Second, "iss")
	corpus.omitTotalOnInteriorWindows = true
	e, tracker := corpus.start()

	var sink issueCollector
	err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink)
	if err == nil {
		t.Fatal("a window with no parseable total must be an error, not an empty window")
	}
	if !strings.Contains(err.Error(), issueTotalKey) {
		t.Errorf("the error must name what could not be read, got %q", err)
	}

	delivered := sink.delivered()
	if delivered == 0 || delivered >= total {
		t.Errorf("delivered %d of %d: the windows read before the failure must be kept, and the set must not look complete",
			delivered, total)
	}
	rec := onlyRecord(t, tracker.State())
	if rec.Reason != common.ReasonIncompleteSlice {
		t.Errorf("reason: got %q, want %q", rec.Reason, common.ReasonIncompleteSlice)
	}
	if rec.Lost != total-delivered {
		t.Errorf("lost: got %d, want %d (total %d - delivered %d)", rec.Lost, total-delivered, total, delivered)
	}
}

// TestErrorMidSliceRecordsIncompleteSlice prevents a half-written issue
// set being reported as a clean one. The issues already handed to the
// sink are on disk and stay there — discarding them would be a second
// loss on top of the first — so what the run owes the operator is a
// statement of what is missing around them (#574).
func TestErrorMidSliceRecordsIncompleteSlice(t *testing.T) {
	const total = 25000
	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, total, 10*time.Second, "iss")
	// A window fetch reads at most 20 pages, so failing after 20
	// guarantees at least one whole window reached the sink first.
	corpus.failAfterPages = issuePageLimit
	e, tracker := corpus.start()

	var sink issueCollector
	err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink)
	if err == nil {
		t.Fatal("expected the mid-walk HTTP 500 to be returned")
	}

	delivered := sink.delivered()
	if delivered == 0 {
		t.Fatal("the windows written before the failure must not be discarded")
	}
	rec := onlyRecord(t, tracker.State())
	if rec.Reason != common.ReasonIncompleteSlice {
		t.Errorf("reason: got %q, want %q", rec.Reason, common.ReasonIncompleteSlice)
	}
	if rec.Total != total || !rec.TotalKnown {
		t.Errorf("total: got %d (known=%t), want %d (known=true)", rec.Total, rec.TotalKnown, total)
	}
	if rec.Fetched != delivered || rec.Lost != total-delivered {
		t.Errorf("record: fetched=%d lost=%d, want %d and %d", rec.Fetched, rec.Lost, delivered, total-delivered)
	}
}

// TestReconciliationDoesNotDoubleCountAtomicLoss pins the arithmetic
// that decides whether a run gets a second, contradictory data-loss
// bullet. expectedLoss is the SUM of what was already recorded, so a
// project whose only loss is a genuinely atomic second must reconcile
// to exactly zero residual drift: the 12,000 issues it could not reach
// are reported once, by the record that knows which second they were
// in, and never again by the count check (#574).
func TestReconciliationDoesNotDoubleCountAtomicLoss(t *testing.T) {
	const (
		burst  = 22000
		spread = 3000
		total  = burst + spread
		lost   = burst - common.ResultWindowLimit
	)
	corpus := newIssueCorpus(t)
	corpus.addBurst(corpusStart, burst, "burst")
	corpus.addSpread(corpusStart.Add(time.Second), spread, time.Second, "spread")
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues: %v", err)
	}

	state := tracker.State()
	rec := onlyRecord(t, state)
	if rec.Reason != common.ReasonAtomicWindow {
		t.Fatalf("reason: got %q, want %q (records: %+v)", rec.Reason, common.ReasonAtomicWindow, state.Records)
	}
	if rec.Lost != lost {
		t.Errorf("lost: got %d, want %d", rec.Lost, lost)
	}
	if state.TotalLost != lost {
		t.Errorf("totalLost: got %d, want %d - the same loss must not be counted twice", state.TotalLost, lost)
	}

	audit := onlyReconciliation(t, state)
	if audit.ExpectedLoss != lost {
		t.Errorf("expectedLoss: got %d, want %d (a sum of recorded losses, not a flag)", audit.ExpectedLoss, lost)
	}
	if want := common.ResultWindowLimit + spread; audit.Unique != want {
		t.Errorf("unique: got %d, want %d", audit.Unique, want)
	}
	if audit.Drift() != 0 || audit.UnexplainedDrift() != 0 {
		t.Errorf("drift=%d unexplained=%d, want 0 for both: before=%d unique=%d expectedLoss=%d",
			audit.Drift(), audit.UnexplainedDrift(), audit.TotalBefore, audit.Unique, audit.ExpectedLoss)
	}
	if got := sink.delivered(); got != common.ResultWindowLimit+spread {
		t.Errorf("delivered: got %d, want %d", got, common.ResultWindowLimit+spread)
	}
}

// TestContextCancellationStopsFurtherRequests keeps a cancelled run
// from walking a whole project's worth of windows. Ctrl-C, or a failure
// in a sibling task, has to stop the walk within a request or two — and
// what was already written still has to be accounted for, which is why
// the incomplete-slice record is asserted here too (#574).
func TestContextCancellationStopsFurtherRequests(t *testing.T) {
	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, 25000, 10*time.Second, "iss")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	corpus.onRequest = func(count int) {
		if count == 6 {
			cancel()
		}
	}
	e, tracker := corpus.start()

	var sink issueCollector
	err := fetchProjectIssues(ctx, e, "p1", "main", taskIssueParams(), sink.sink)
	if err == nil {
		t.Fatal("expected the cancelled context to be returned")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error: got %v, want a context cancellation", err)
	}
	if got := corpus.requestCount(); got > 10 {
		t.Errorf("requests after cancellation: got %d, want the walk to stop within a request or two of 6", got)
	}
	if !tracker.HasRecords() {
		t.Error("a cancelled walk still owes an account of what it did not fetch")
	}
}

// atomicWant is what a case expects the walk to record about a second
// it could not subdivide. A nil *atomicWant means the case must record
// nothing at all.
type atomicWant struct {
	second time.Time
	total  int
	lost   int
}

// assertDeliveredOnce fails unless exactly want issues reached the
// sink and no key reached it twice. A half-open partition cannot
// return the same issue twice, so a duplicate is evidence the seams
// are not what the walk believes they are.
func assertDeliveredOnce(t *testing.T, sink *issueCollector, want, inProject int) {
	t.Helper()
	counts := sink.keyCounts()
	if len(counts) != want {
		t.Errorf("distinct issue keys delivered: got %d, want %d (of %d in the project)",
			len(counts), want, inProject)
	}
	if got := sink.delivered(); got != want {
		t.Errorf("delivered: got %d, want %d", got, want)
	}
	for key, n := range counts {
		if n != 1 {
			t.Errorf("issue %s was delivered %d times; a half-open partition delivers each issue once", key, n)
		}
	}
}

// assertAtomicSecond fails unless rec names exactly the closed second
// it is a claim about, and carries that second's own numbers rather
// than a total smeared across its neighbour.
func assertAtomicSecond(t *testing.T, rec common.TruncationRecord, want atomicWant) {
	t.Helper()
	if rec.Reason != common.ReasonAtomicWindow {
		t.Fatalf("reason: got %q, want %q", rec.Reason, common.ReasonAtomicWindow)
	}
	if got, w := rec.WindowStart, common.FormatSQDate(want.second); got != w {
		t.Errorf("windowStart: got %q, want %q", got, w)
	}
	if got, w := rec.WindowEnd, common.FormatSQDate(want.second.Add(time.Second)); got != w {
		t.Errorf("windowEnd: got %q, want %q", got, w)
	}
	if rec.Total != want.total {
		t.Errorf("total: got %d, want %d - the record must count one second, not two", rec.Total, want.total)
	}
	if rec.Lost != want.lost {
		t.Errorf("lost: got %d, want %d", rec.Lost, want.lost)
	}
}

// assertNothingRecorded fails unless the walk accounted for every
// issue: no record at all, and a reconciliation that balances.
func assertNothingRecorded(t *testing.T, state common.TruncationState) {
	t.Helper()
	if len(state.Records) != 0 {
		t.Fatalf("every issue was reachable, so nothing may be recorded: got %+v", state.Records)
	}
	audit := onlyReconciliation(t, state)
	if audit.Drift() != 0 || audit.UnexplainedDrift() != 0 {
		t.Errorf("drift=%d unexplained=%d, want 0 for both: before=%d unique=%d expectedLoss=%d",
			audit.Drift(), audit.UnexplainedDrift(), audit.TotalBefore, audit.Unique, audit.ExpectedLoss)
	}
}

// TestRightmostWindowCoversExactlyOneSecond is the regression for the
// worst kind of loss this walk can produce: silent AND mislabelled.
//
// The probed range used to end at the newest issue's creation second,
// which is the INCLUSIVE instant of a real issue, while a window end is
// EXCLUSIVE. Every right child inherits its parent's end and
// splittable() is pure arithmetic, so the rightmost leaf converged to
// [newest-1s, newest) — one second wide, therefore declared
// unsplittable — while the request it actually sent dropped
// createdBefore on its open edge and so selected TWO real seconds. When
// those two seconds jointly exceeded the ceiling, recoverable issues
// were dropped AND filed as atomic_window: the report then called
// avoidable loss an unavoidable API limit, and expectedLoss absorbed
// the difference so UnexplainedDrift() read 0 and the reconciliation
// logged a clean walk.
//
// The one-second-gap case is the control. It delivers everything with
// or without the fix, which is what proves the failure is the
// rightmost leaf doubling up on two adjacent seconds and not the size
// of the bursts (#574).
func TestRightmostWindowCoversExactlyOneSecond(t *testing.T) {
	// Neither burst exceeds the ceiling on its own, so nothing in the
	// first two cases is genuinely atomic and every issue is reachable.
	const burst = 6000

	cases := []struct {
		name          string
		build         func(*issueCorpus)
		wantDelivered int
		wantAtomic    *atomicWant
	}{
		{
			name: "two full bursts in adjacent seconds, the newest of them on the open edge",
			build: func(c *issueCorpus) {
				c.addSpread(corpusStart, 10, time.Second, "spread")
				c.addBurst(corpusStart.Add(20*time.Second), burst, "burst20")
				c.addBurst(corpusStart.Add(21*time.Second), burst, "burst21")
			},
			wantDelivered: 10 + 2*burst,
		},
		{
			name: "the control: the same bursts one second apart, which never doubled up",
			build: func(c *issueCorpus) {
				c.addSpread(corpusStart, 10, time.Second, "spread")
				c.addBurst(corpusStart.Add(20*time.Second), burst, "burst20")
				c.addBurst(corpusStart.Add(22*time.Second), burst, "burst22")
			},
			wantDelivered: 10 + 2*burst,
		},
		{
			name: "a genuinely atomic newest second is reported alone, not smeared over its neighbour",
			build: func(c *issueCorpus) {
				c.addSpread(corpusStart, 10, time.Second, "spread")
				c.addBurst(corpusStart.Add(20*time.Second), 900, "burst20")
				c.addBurst(corpusStart.Add(21*time.Second), 11000, "burst21")
			},
			wantDelivered: 10 + 900 + common.ResultWindowLimit,
			wantAtomic: &atomicWant{
				second: corpusStart.Add(21 * time.Second),
				total:  11000,
				lost:   11000 - common.ResultWindowLimit,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			corpus := newIssueCorpus(t)
			tc.build(corpus)
			inProject := len(corpus.issues)
			e, tracker := corpus.start()

			var sink issueCollector
			if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
				t.Fatalf("fetchProjectIssues: %v", err)
			}

			assertDeliveredOnce(t, &sink, tc.wantDelivered, inProject)
			state := tracker.State()
			if tc.wantAtomic == nil {
				assertNothingRecorded(t, state)
				return
			}
			assertAtomicSecond(t, onlyRecord(t, state), *tc.wantAtomic)
		})
	}
}

// TestProbedRangeTurnsAnInclusiveInstantIntoAnExclusiveBound pins the
// conversion the case above depends on, at both places the walk makes
// it.
//
// The creation dates the probe reads back are INCLUSIVE instants of
// real issues, and time.Now() truncated to a second is an instant
// inside a second issues can still be created in, while
// issueWindow.end is EXCLUSIVE. This +1s is NOT the CloudVoyager
// e5fbecd0 bug, and a test says so as well as a comment in the source:
// that bug offset a SPLIT MIDPOINT, moving a seam off the value both
// sides share and dropping the second between them. Converting a
// probed data point into the bound of the range that has to contain it
// is the opposite operation (#574).
func TestProbedRangeTurnsAnInclusiveInstantIntoAnExclusiveBound(t *testing.T) {
	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, 5, time.Second, "iss")
	e, _ := corpus.start()

	s := &issueSlicer{
		e:     e,
		base:  taskIssueParams(),
		scope: TruncationScope{Task: issuesTaskName, ProjectKey: "p1", Branch: "main"},
	}
	root := s.probeIssueRange(ctx(t))

	if !root.openStart || !root.openEnd {
		t.Errorf("the probed root must keep both edges open, got %s", root.label())
	}
	if want := corpusStart; !root.start.Equal(want) {
		t.Errorf("root start: got %s, want %s (createdAfter is inclusive, so no offset)", root.start, want)
	}
	newest := corpusStart.Add(4 * time.Second)
	if want := newest.Add(time.Second); !root.end.Equal(want) {
		t.Errorf("root end: got %s, want %s - the newest issue's second (%s) must be INSIDE the range",
			root.end, want, newest)
	}

	// The same conversion on the fallback range, which is what a
	// project whose every issue shares one second bisects instead. The
	// reference instant is read BEFORE the call, never after: reading
	// it after would let a second tick in between and fail a correct
	// implementation.
	before := time.Now().UTC().Truncate(time.Second)
	start, end := defaultIssueRange()
	if !end.After(before) {
		t.Errorf("defaultIssueRange end: got %s, want strictly after the current second %s - issues can still be created in it",
			end, before)
	}
	if end.Nanosecond() != 0 {
		t.Errorf("defaultIssueRange end %s is not second-aligned; a sub-second bound is HTTP 400", end)
	}
	if want := time.Date(2006, time.January, 1, 0, 0, 0, 0, time.UTC); !start.Equal(want) {
		t.Errorf("defaultIssueRange start: got %s, want %s", start, want)
	}
}

// assertClosedOneSecondWindow fails unless the record's bounds are both
// present, both parse, and span exactly the one second starting at
// want.
func assertClosedOneSecondWindow(t *testing.T, rec common.TruncationRecord, want time.Time) {
	t.Helper()
	if rec.WindowStart == "" || rec.WindowEnd == "" {
		t.Fatalf("an atomic_window record claims one creation second, so both bounds must be set: start=%q end=%q",
			rec.WindowStart, rec.WindowEnd)
	}
	start, err := common.ParseSQDate(rec.WindowStart)
	if err != nil {
		t.Fatalf("windowStart %q does not parse: %v", rec.WindowStart, err)
	}
	end, err := common.ParseSQDate(rec.WindowEnd)
	if err != nil {
		t.Fatalf("windowEnd %q does not parse: %v", rec.WindowEnd, err)
	}
	if got := end.Sub(start); got != time.Second {
		t.Errorf("the recorded window spans %s, want exactly 1s: [%s, %s)", got, rec.WindowStart, rec.WindowEnd)
	}
	if !start.Equal(want) {
		t.Errorf("the record names %s, want the second the burst is in, %s", start, want)
	}
}

// TestAtomicWindowRecordAlwaysNamesOneClosedSecond keeps the report
// sentence true. atomic_window makes the report say "more than 10,000
// issues share one creation timestamp", and a record written for a
// window with an OPEN edge cannot support that: the request omits a
// bound, so it selects everything on that side, and the record either
// names no second at all or names one it never measured. Closing the
// edge before the claim is made is what turns that sentence into
// something the numbers back up (#574).
func TestAtomicWindowRecordAlwaysNamesOneClosedSecond(t *testing.T) {
	const burst = 22000

	cases := []struct {
		name  string
		build func(*issueCorpus)
		want  time.Time
	}{
		{
			name: "the atomic second is the oldest, so its window is the leftmost leaf and its lower edge is open",
			build: func(c *issueCorpus) {
				c.addBurst(corpusStart, burst, "burst")
				c.addSpread(corpusStart.Add(time.Second), 3000, time.Second, "spread")
			},
			want: corpusStart,
		},
		{
			name: "the atomic second is the newest, so its window is the rightmost leaf and its upper edge is open",
			build: func(c *issueCorpus) {
				c.addSpread(corpusStart, 3000, time.Second, "spread")
				c.addBurst(corpusStart.Add(3000*time.Second), burst, "burst")
			},
			want: corpusStart.Add(3000 * time.Second),
		},
		{
			name: "the whole project shares one second, so the walk bisects the default range down to an interior leaf",
			build: func(c *issueCorpus) {
				c.addBurst(corpusStart, burst, "burst")
			},
			want: corpusStart,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			corpus := newIssueCorpus(t)
			tc.build(corpus)
			e, tracker := corpus.start()

			var sink issueCollector
			if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
				t.Fatalf("fetchProjectIssues must not fail on an atomic second: %v", err)
			}

			state := tracker.State()
			atomic := recordsByReason(state)[common.ReasonAtomicWindow]
			if len(atomic) != 1 {
				t.Fatalf("expected exactly one atomic_window record, got %d: %+v", len(atomic), state.Records)
			}
			assertClosedOneSecondWindow(t, atomic[0], tc.want)
			assertAtomicSecond(t, atomic[0], atomicWant{
				second: tc.want,
				total:  burst,
				lost:   burst - common.ResultWindowLimit,
			})
		})
	}
}

// assertDegradeRecord fails unless the walk left exactly the one
// record the case expects, carrying the project's own numbers.
func assertDegradeRecord(t *testing.T, state common.TruncationState,
	reason common.TruncationReason, total, fetched, lost int) {
	t.Helper()
	rec := onlyRecord(t, state)
	if rec.Reason != reason {
		t.Errorf("reason: got %q, want %q", rec.Reason, reason)
	}
	if rec.Total != total || !rec.TotalKnown {
		t.Errorf("total: got %d (known=%t), want %d (known=true)", rec.Total, rec.TotalKnown, total)
	}
	if rec.Fetched != fetched || rec.Lost != lost {
		t.Errorf("record: fetched=%d lost=%d, want %d and %d", rec.Fetched, rec.Lost, fetched, lost)
	}
}

// dateRefusalCase is one shape of server refusal and what the walk is
// expected to do about it.
type dateRefusalCase struct {
	name          string
	datedStatus   int
	everyStatus   int
	wantErr       bool
	wantNonFatal  bool
	wantDelivered int
	wantReason    common.TruncationReason
	wantLost      int
}

// TestRejectedDateBoundsDegradeInsteadOfFailingTheRun is the
// no-regression contract against deployments this tool was never
// tested on.
//
// #574 guarded the server that silently IGNORES createdAfter /
// createdBefore but not the one that REJECTS them, and the second is
// at least as likely: FormatSQDate emits one hardcoded layout verified
// against exactly one server version out of the 9.9-to-2026.x range
// this tool supports, and a WAF or proxy in front of an instance can
// refuse the parameters just as effectively. Such an error escaped the
// slicer, missed isNonFatalHTTPErr (403 and 404 only), was wrapped by
// iterateBranches and failed the ENTIRE run in executePhases — every
// remaining project and phase lost — where the same deployment before
// #574 extracted 10,000 issues and exited 0.
//
// A genuine 403/404, and any failure that is not specific to the date
// bounds, must keep today's behaviour exactly (#574).
func TestRejectedDateBoundsDegradeInsteadOfFailingTheRun(t *testing.T) {
	const total = 12000

	cases := []dateRefusalCase{
		{
			name:          "HTTP 400 on every dated request degrades to the undated fetch and the run continues",
			datedStatus:   http.StatusBadRequest,
			wantDelivered: common.ResultWindowLimit,
			wantReason:    common.ReasonDatesRejected,
			wantLost:      total - common.ResultWindowLimit,
		},
		{
			// Narrowed after review: a rate limit is transient, and one
			// of them on one window used to end the walk and report
			// that the source refuses creation-date filters entirely.
			// The operator was then told date slicing is impossible on
			// their instance and left holding 10,000 issues of a
			// project a re-run would have extracted in full.
			name:        "HTTP 429 is transient, so it keeps today's error path instead of blaming the date filter",
			datedStatus: http.StatusTooManyRequests,
			wantErr:     true,
			wantReason:  common.ReasonIncompleteSlice,
			wantLost:    total,
		},
		{
			name:         "HTTP 403 keeps today's behaviour: the error propagates for the call site to skip the project",
			datedStatus:  http.StatusForbidden,
			wantErr:      true,
			wantNonFatal: true,
			wantReason:   common.ReasonIncompleteSlice,
			wantLost:     total,
		},
		{
			name:        "a server refusing every request, dated or not, still fails the way it always did",
			everyStatus: http.StatusBadRequest,
			wantErr:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runDateRefusalCase(t, tc, total)
		})
	}
}

// runDateRefusalCase drives one server-refusal shape and checks what
// the walk did about it: whether the error propagated, whether the
// call site can still recognise it as skippable, how many issues
// reached the sink, and which reason was recorded.
func runDateRefusalCase(t *testing.T, tc dateRefusalCase, total int) {
	t.Helper()
	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, total, time.Minute, "iss")
	corpus.rejectDatedStatus = tc.datedStatus
	corpus.rejectEveryStatus = tc.everyStatus
	e, tracker := corpus.start()

	var logs bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&logs, nil))

	var sink issueCollector
	err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink)
	switch {
	case tc.wantErr && err == nil:
		t.Fatal("expected the error to propagate so the call site decides what to do with it")
	case !tc.wantErr && err != nil:
		t.Fatalf("a rejected date bound must not fail the extract: %v", err)
	}
	if tc.wantNonFatal && !isNonFatalHTTPErr(err) {
		t.Errorf("error %v is no longer recognised as non-fatal, so the call site would abort the whole run", err)
	}
	if got := sink.delivered(); got != tc.wantDelivered {
		t.Errorf("delivered: got %d, want %d", got, tc.wantDelivered)
	}

	state := tracker.State()
	if tc.wantReason == "" {
		if len(state.Records) != 0 {
			t.Errorf("a server that answers nothing has nothing to report about yet: got %+v", state.Records)
		}
		return
	}
	assertDegradeRecord(t, state, tc.wantReason, total, tc.wantDelivered, tc.wantLost)
	if tc.wantReason != common.ReasonDatesRejected {
		return
	}
	for _, want := range []string{"refused its date parameters", "falling back to the undated fetch"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log does not mention %q:\n%s", want, logs.String())
		}
	}
}

// TestWalkLevelRecordDoesNotDoubleCountWindowLoss guards #574. A
// walk-level record (incomplete_slice, or a degradation) reports the
// whole-project shortfall, and record() has already summed the Lost of
// every per-window record into expectedLoss. Before this guard,
// unaccounted() returned initialTotal-unique without subtracting what
// was already booked, so a project that hit an atomic second AND then
// failed reported that second's loss twice: totalLost and the console
// block were inflated, and Drift() went negative, which made the
// migration report tell an operator who genuinely lost data that the
// walk had collected a surplus and nothing was missing.
func TestWalkLevelRecordDoesNotDoubleCountWindowLoss(t *testing.T) {
	corpus := newIssueCorpus(t)
	// An atomic second the walk must record, then more data behind it
	// so the walk keeps going and can fail afterwards.
	corpus.addBurst(corpusStart, 22000, "burst")
	corpus.addSpread(corpusStart.Add(time.Hour), 3000, time.Minute, "later")
	// 20 pages is exactly one clamped window, so the atomic record is
	// written before the failure lands.
	corpus.failAfterPages = 22
	e, tracker := corpus.start()

	var sink issueCollector
	_ = fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink)

	state := tracker.State()
	var booked int
	for _, rec := range state.Records {
		booked += rec.Lost
	}
	delivered := sink.delivered()
	shortfall := 25000 - delivered

	if booked > shortfall {
		t.Errorf("recorded loss %d exceeds the real shortfall %d (delivered %d of 25000) — a loss is being counted twice",
			booked, shortfall, delivered)
	}
	if got := state.TotalLost; got > shortfall {
		t.Errorf("totalLost: got %d, want no more than the real shortfall %d", got, shortfall)
	}
	for _, recon := range state.Reconciliations {
		if d := recon.UnexplainedDrift(); d < 0 {
			t.Errorf("UnexplainedDrift is %d: a negative residual renders as a surplus, telling the operator nothing is missing when data was lost", d)
		}
	}
}

// TestTransientErrorOnADatedRequestIsNotCalledDatesRejected guards
// #574. asDateBoundRejection tagged every non-403/404 HTTP error on a
// dated request as a date-parameter refusal. A single 429 or 5xx on one
// window therefore ended the walk, fell back to the undated capped
// fetch, and recorded dates_rejected — after which the report told the
// operator their instance rejects creation-date filters entirely, and
// left them holding 10,000 issues of a project a re-run would have
// extracted in full. Only a refusal of the request itself (4xx, not
// 429) can be a refusal of its parameters.
func TestTransientErrorOnADatedRequestIsNotCalledDatesRejected(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"rate limited", http.StatusTooManyRequests},
		{"request timeout", http.StatusRequestTimeout},
		{"too early", http.StatusTooEarly},
		{"internal server error", http.StatusInternalServerError},
		{"bad gateway", http.StatusBadGateway},
		{"gateway timeout", http.StatusGatewayTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			corpus := newIssueCorpus(t)
			corpus.addSpread(corpusStart, 25000, time.Minute, "iss")
			corpus.rejectDatedStatus = tc.status
			e, tracker := corpus.start()

			var sink issueCollector
			err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink)
			if err == nil {
				t.Fatalf("a transient %d must keep today's error path, not degrade silently", tc.status)
			}
			for _, rec := range tracker.State().Records {
				if rec.Reason == common.ReasonDatesRejected {
					t.Errorf("a transient %d was recorded as %q; the report would claim the source refuses date filters",
						tc.status, common.ReasonDatesRejected)
				}
			}
		})
	}
}

// TestPersistentDateRefusalIsStillCalledDatesRejected is the other half
// of the pair above (#574): narrowing the tag to 4xx must not stop a
// genuine parameter refusal from being detected, because that is the
// case the degrade path exists for.
func TestPersistentDateRefusalIsStillCalledDatesRejected(t *testing.T) {
	corpus := newIssueCorpus(t)
	corpus.addSpread(corpusStart, 25000, time.Minute, "iss")
	corpus.rejectDatedStatus = http.StatusBadRequest
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("a rejected date bound must degrade, not fail the run: %v", err)
	}
	if got := sink.delivered(); got != common.ResultWindowLimit {
		t.Errorf("delivered: got %d, want the pre-#574 clamped fetch of %d", got, common.ResultWindowLimit)
	}
	var found bool
	for _, rec := range tracker.State().Records {
		if rec.Reason == common.ReasonDatesRejected {
			found = true
		}
	}
	if !found {
		t.Error("an HTTP 400 on every dated request must still record dates_rejected")
	}
}

// TestNoWalkRecordWhenEveryLostIssueIsAlreadyNamed guards #574.
// unaccounted() clamps its residual at zero, and the report renders a
// Lost of zero as "an unknown number of issue(s)" — the honest
// rendering when the server never gave a total, but a false claim of
// further loss when the arithmetic has just proved the residual is
// exactly nothing. A walk whose whole shortfall is already named by
// per-window records must add no walk-level bullet on top of them.
//
// Driven against the slicer directly rather than through a corpus: the
// state under test is "every missing issue is already booked AND the
// walk still ends in a degradation", which an end-to-end fixture
// reaches only by coincidence.
func TestNoWalkRecordWhenEveryLostIssueIsAlreadyNamed(t *testing.T) {
	cases := []struct {
		name         string
		totalKnown   bool
		initialTotal int
		unique       int
		expectedLoss int
		wantRecords  int
	}{
		{
			name:         "every missing issue already named by a window record",
			totalKnown:   true,
			initialTotal: 22000,
			unique:       10000,
			expectedLoss: 12000,
			wantRecords:  0,
		},
		{
			name:         "a residual the window records do not explain is still reported",
			totalKnown:   true,
			initialTotal: 25000,
			unique:       10000,
			expectedLoss: 12000,
			wantRecords:  1,
		},
		{
			name:         "no total from the server, so an unknown loss stays reportable",
			totalKnown:   false,
			initialTotal: 0,
			unique:       10000,
			expectedLoss: 0,
			wantRecords:  1,
		},
		{
			name:         "nothing booked yet, so the walk-level record is the only evidence",
			totalKnown:   true,
			initialTotal: 25000,
			unique:       10000,
			expectedLoss: 0,
			wantRecords:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, write := range []struct {
				what string
				fn   func(*issueSlicer)
			}{
				{"incomplete_slice", (*issueSlicer).recordIncompleteSlice},
				{"degradation", (*issueSlicer).recordDegradation},
			} {
				tracker := common.NewTruncationTracker()
				s := &issueSlicer{
					e:              &Executor{Truncation: tracker},
					scope:          TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "p1", Branch: "main"},
					initialTotal:   tc.initialTotal,
					totalKnown:     tc.totalKnown,
					unique:         tc.unique,
					expectedLoss:   tc.expectedLoss,
					degradedReason: common.ReasonMaxDepth,
				}
				write.fn(s)
				if got := len(tracker.State().Records); got != tc.wantRecords {
					t.Errorf("%s: wrote %d record(s), want %d", write.what, got, tc.wantRecords)
				}
			}
		})
	}
}
