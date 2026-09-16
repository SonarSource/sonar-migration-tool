// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

const (
	// issuePageLimit is the page cap every issue fetch carries, sliced
	// or not: 20 x 500 is exactly the largest offset
	// /api/issues/search will serve, and page 21 is HTTP 400 rather
	// than a short answer. It is never relaxed anywhere in this file —
	// slicing works by narrowing what a page contains, never by
	// reaching past the offset the server allows.
	issuePageLimit = 20

	// issuePageSize is set explicitly rather than left to
	// PaginatedOpts' default so the ceiling arithmetic below and the
	// size the fetch actually uses have one origin.
	issuePageSize = 500

	// maxSliceDepth is a backstop, not the terminator. Bisecting the
	// widest range this code ever starts from (2006 to now, under 2^31
	// seconds) reaches a one-second window in about 31 levels, and a
	// one-second window is what actually stops the recursion. Reaching
	// 48 means the window arithmetic misbehaved, which is why it is
	// reported rather than quietly accepted.
	maxSliceDepth = 48

	// issuesTaskName labels every truncation record and reconciliation
	// this file produces.
	issuesTaskName = "getProjectIssuesFull"

	// issueResultKey / issueTotalKey are the response shape of
	// /api/issues/search.
	issueResultKey = "issues"
	issueTotalKey  = "paging.total"

	createdAfterParam  = "createdAfter"
	createdBeforeParam = "createdBefore"
)

// sliceableEndpoints is the allowlist, the second of three independent
// guards that keep date slicing away from /api/hotspots/search.
//
// That endpoint declares no createdAfter / createdBefore and silently
// ignores unknown parameters: it answers HTTP 200 with an unchanged
// total for every window, so a date slicer pointed at it recurses
// forever over the same truncated set while its log reports
// completeness. It is the worst trap in SPEC-006, and CloudVoyager fell
// into it by reusing the issues path verbatim (#574).
var sliceableEndpoints = map[string]bool{
	issuesSearchAPI: true,
}

// dateSliceableEndpoint reports whether an endpoint is known to honour
// createdAfter / createdBefore.
//
// Guard one is structural — the endpoint below is a constant, the
// slicer is unexported, there is no endpoint parameter and no generic
// entry point, so no caller can aim any of this at hotspots. This is
// the guard that makes the intent checkable: adding
// /api/hotspots/search to the map above fails a test.
//
// It is deliberately NOT called from the walk. Every request in this
// file is hardcoded to issuesSearchAPI, so a runtime
// `if !dateSliceableEndpoint(issuesSearchAPI)` can only ever be false,
// and the branch behind it was unreachable code wearing the costume of
// a safety net — worse than no check, because it reads like one. The
// allowlist is an assertion about intent, and the thing that enforces
// it is TestTheAllowlistExcludesHotspots plus the fact that widening
// this file's reach means adding an endpoint parameter that does not
// exist. The runtime guard with teeth is datesRespected, which catches
// an endpoint that drops the parameters whatever any list says (#574).
func dateSliceableEndpoint(path string) bool {
	return sliceableEndpoints[path]
}

// issueSink receives one window's worth of issues, in fetch order.
//
// The slicer calls it once per window instead of accumulating the whole
// project: the caller enriches and writes each chunk as it arrives, so
// the enriched copy of a window is released before the next window is
// fetched. Peak memory stays at one window — at most 10,000 issues with
// additionalFields=_all, which is exactly the peak the unsliced fetch
// already had — plus the dedup key set.
type issueSink func(items []json.RawMessage) error

// issueWindow is a half-open creation-date range [start, end).
//
// openStart and openEnd mean the corresponding date parameter is NOT
// SENT, and that is the whole design. An omitted bound is unbounded:
// verified live on SonarQube 2026.4.1, where createdBefore=M alone
// returned 8,924 issues, createdAfter=M alone returned 5,979, and the
// two sum to exactly the project's reported total of 14,903 (with the
// tool's real parameter set, 10,269 + 4,393 = 14,662, again exact). So
// the root window emits no date parameters at all and IS the query this
// task sent before #574; a split hands the left child the parent's open
// lower edge and the right child the parent's open upper edge, and only
// interior seams carry both bounds.
//
// The consequences are worth spelling out, because they are what make
// the closing reconciliation an exact check rather than an estimate:
// there is no epoch to get wrong, no time.Now() on a correctness path,
// no clock-skew window, no pre-2006 blind spot, and no gap for an issue
// created while the walk is running — that issue lands in the
// always-open rightmost window. The partition is total by construction.
//
// start and end are arithmetic bounds only, and always second-aligned.
// The API accepts no sub-second precision and answers a zero-width
// window with HTTP 400 ("Start bound cannot be larger or equal to end
// bound"), so a bound that is not a whole second is a bound that cannot
// be sent.
type issueWindow struct {
	start, end         time.Time
	openStart, openEnd bool
}

// rootIssueWindow is the unbounded window. The request it produces
// carries no date parameter at all, which makes it byte-for-byte the
// request this task made before this change.
func rootIssueWindow() issueWindow {
	return issueWindow{openStart: true, openEnd: true}
}

// splittable reports whether the window is wide enough to bisect.
//
// The terminator is "one second wide", never "start == end". A
// zero-width window is HTTP 400, so a walk that only stops at equality
// fails on the request before the one it was going to refuse, and one
// second is the narrowest range the API can express.
func (w issueWindow) splittable() bool {
	return w.end.Sub(w.start) > time.Second
}

// mid is the seam between the two halves of w, second-aligned.
//
// The truncation is not cosmetic. It is why a sub-second bound cannot
// exist anywhere in the walk, and therefore why the two sides of every
// seam format to the same string.
func (w issueWindow) mid() time.Time {
	return w.start.Add(w.end.Sub(w.start) / 2).Truncate(time.Second)
}

// safeToRequest is the last gate before any request carrying this
// window's bounds.
//
// It compares the FORMATTED strings rather than the time.Time values,
// because the wire bytes are what the server sees: two instants a
// microsecond apart are two different times and one identical second,
// and that second is an HTTP 400. A window open on either side carries
// at most one bound and cannot be zero-width at all.
//
// Difference is the whole check because bisection cannot invert a
// window: every midpoint is strictly inside its parent.
func (w issueWindow) safeToRequest() bool {
	if w.openStart || w.openEnd {
		return true
	}
	return common.FormatSQDate(w.start) != common.FormatSQDate(w.end)
}

// dated reports whether a request for this window carries at least one
// date parameter, and therefore whether an HTTP error answering it
// could be the server refusing the date bounds themselves.
func (w issueWindow) dated() bool {
	return !w.openStart || !w.openEnd
}

// bounds returns the two bound strings for a truncation record, empty
// for an edge that carries no parameter. A record must never claim a
// bound its request did not send.
func (w issueWindow) bounds() (string, string) {
	start, end := "", ""
	if !w.openStart {
		start = common.FormatSQDate(w.start)
	}
	if !w.openEnd {
		end = common.FormatSQDate(w.end)
	}
	return start, end
}

// label renders the window for a log line, naming the open edges for
// what they are.
func (w issueWindow) label() string {
	start, end := w.bounds()
	if start == "" {
		start = "-inf"
	}
	if end == "" {
		end = "+inf"
	}
	return "[" + start + ", " + end + ")"
}

// splitWindow bisects w at its midpoint into [start, mid) and
// [mid, end).
//
// Both children carry the SAME time.Time value for the seam, so
// FormatSQDate renders a byte-identical string on each side: the left
// window's exclusive upper bound is exactly the right window's
// inclusive lower bound, and nothing is added to either side.
//
// That "nothing" is the point of this function. CloudVoyager's commit
// e5fbecd0 added +1ms to every non-first window start, on the mistaken
// premise that both bounds are inclusive. createdAfter is inclusive and
// createdBefore is exclusive, so the nudge was unnecessary — and
// whenever the midpoint's millisecond component was 999 the addition
// rolled into the next second, leaving an entire second of issues
// between the two windows, silently, on roughly one seam in a thousand.
func splitWindow(w issueWindow) (issueWindow, issueWindow) {
	seam := w.mid()
	left := issueWindow{
		start:     w.start.Truncate(time.Second),
		end:       seam,
		openStart: w.openStart,
	}
	right := issueWindow{
		start:   seam,
		end:     w.end.Truncate(time.Second),
		openEnd: w.openEnd,
	}
	return left, right
}

// windowParams clones the caller's parameters and touches ONLY the two
// date keys.
//
// Rebuilding the parameter set from scratch is the defect this function
// exists to prevent. componentKeys has to survive at every depth — not
// components, which SQ 9.9 silently ignores in favour of the GLOBAL
// issue set, which then gets enriched per project and pollutes every
// project's import (#400) — and so do branch, the version-gated
// issueStatuses / statuses choice and additionalFields=_all. An empty
// components= returns the whole instance's issues.
//
// The Del calls matter as much as the Set calls: an open edge means the
// parameter is absent, not empty, so a stale bound inherited from the
// caller would silently bound a window that is supposed to be open.
func windowParams(base url.Values, w issueWindow) url.Values {
	params := common.CloneParams(base)
	if w.openStart {
		params.Del(createdAfterParam)
	} else {
		params.Set(createdAfterParam, common.FormatSQDate(w.start))
	}
	if w.openEnd {
		params.Del(createdBeforeParam)
	} else {
		params.Set(createdBeforeParam, common.FormatSQDate(w.end))
	}
	return params
}

// windowCeiling is the most items a single window fetch can return, and
// therefore the only threshold the split predicate may use.
//
// One derivation, no second constant: PageLimit x MaxPageSize is what
// the fetch will actually read, and ResultWindowLimit is where the
// server starts answering HTTP 400 whatever the caller asked for. At
// the issue call site both are 10,000, which is why the predicate is
// "total > 10,000" and why exactly 10,000 is not sliced — that offset
// is live-confirmed reachable (p=20&ps=500 returns HTTP 200).
//
// The arguments are the EFFECTIVE values a PageResult reports, never
// the caller's own PaginatedOpts: applyDefaults has a pointer receiver
// and runs on a by-value copy, so opts read back by the caller still
// say MaxPageSize == 0 and any page arithmetic built on that collapses
// to zero pages.
func windowCeiling(pageLimit, pageSize int) int {
	if pageLimit <= 0 || pageSize <= 0 {
		return common.ResultWindowLimit
	}
	if fetchable := pageLimit * pageSize; fetchable < common.ResultWindowLimit {
		return fetchable
	}
	return common.ResultWindowLimit
}

// issueSlicer carries the state of one project+branch issue fetch.
//
// It is deliberately unexported, in package extract, with the endpoint
// hardcoded and no endpoint parameter anywhere in its API — guard one
// of the three described at sliceableEndpoints. There is no API here
// through which any caller could ask for /api/hotspots/search.
type issueSlicer struct {
	e     *Executor
	base  url.Values
	sink  issueSink
	scope TruncationScope

	// pageSize, pageLimit and ceiling are the EFFECTIVE values the
	// fetches use, taken from the first PageResult rather than
	// re-derived (see windowCeiling).
	pageSize  int
	pageLimit int
	ceiling   int

	initialTotal int
	totalKnown   bool

	// seen is the dedup set, and it is a VERIFIER rather than a
	// workaround: a correct half-open partition cannot return the same
	// issue twice, so a duplicate is evidence that the seams or the
	// server's window semantics are not what this code believes. The
	// count is persisted in the reconciliation instead of being
	// quietly absorbed.
	seen       map[string]bool
	unique     int // items handed to the sink, keyless ones included
	duplicates int
	keyless    int

	windows  int
	requests int

	// expectedLoss is the SUM of the Lost values actually recorded — a
	// count, never a flag. Summing it is what lets a dropped seam
	// surface as drift on a project that ALSO has a genuinely atomic
	// second, which is the case a boolean "something was unsplittable"
	// used to hide.
	expectedLoss int

	// degradedReason latches a slicer-internal refusal so the walk's
	// closing record names the true cause; fellBack keeps the fallback
	// fetch to one per walk however many windows trip.
	degradedReason common.TruncationReason
	fellBack       bool
}

// fetchProjectIssues fetches every issue the caller's parameters select
// and hands them to sink, one window at a time.
//
// The fast path is the point of the shape. A project under the result
// ceiling costs exactly the requests it cost before #574, with no date
// parameter on any of them and no probe of any kind: the trigger for
// slicing is the clamp itself, not an unconditional count query. Only a
// project whose first page proves the fetch cannot complete pays for
// anything more, and there the single page already read is the only
// wasted work in the change.
func fetchProjectIssues(ctx context.Context, e *Executor, projectKey, branch string,
	params url.Values, sink issueSink) error {

	s := &issueSlicer{
		e:    e,
		base: params,
		sink: sink,
		scope: TruncationScope{
			Task:       issuesTaskName,
			ProjectKey: projectKey,
			Branch:     branch,
		},
		seen: make(map[string]bool),
	}

	// StopOnTruncation: page 1 already says whether this project needs
	// slicing, so detecting it costs one request instead of twenty
	// pages the windows are about to re-read.
	res, err := s.fetchWindow(ctx, rootIssueWindow(), true)
	if err != nil {
		return err
	}
	s.pageSize, s.pageLimit = res.PageSize, res.PageLimit
	s.ceiling = windowCeiling(res.PageLimit, res.PageSize)
	s.initialTotal, s.totalKnown = res.Total, res.TotalKnown

	if !res.Truncated {
		return sink(res.Items)
	}

	if res.Reason == common.ReasonPageLimitClamp && res.TotalKnown {
		return s.sliceProjectIssues(ctx)
	}

	// Truncated in a shape the walk cannot partition — in practice a
	// response with no parseable total, so there is no number to
	// bisect toward. The entry fetch suppressed its own record because
	// the slicer normally writes a more precise one; this is the path
	// where it has to write that record itself, or the truncation the
	// fetch just detected would vanish.
	if err := s.degrade(ctx, res.Reason, rootIssueWindow()); err != nil {
		return err
	}
	s.recordDegradation()
	return nil
}

// sliceProjectIssues recovers a project the flat fetch cannot: it walks
// half-open creation-date windows, narrowing each one until it holds
// few enough issues for a single capped fetch to return all of them.
func (s *issueSlicer) sliceProjectIssues(ctx context.Context) error {
	s.e.Logger.Info("slicing issues by creation date - the project is past the result ceiling",
		"project", s.scope.ProjectKey, "branch", s.scope.Branch,
		"total", s.initialTotal, "ceiling", s.ceiling)

	// Sized up front: the key set is the one structure whose size grows
	// with the project, and it is about to take every key in it.
	s.seen = make(map[string]bool, s.initialTotal)

	root := s.probeIssueRange(ctx)

	respected, err := s.datesRespected(ctx, root)
	if err != nil {
		return s.handleWalkError(ctx, err)
	}
	if !respected {
		// Guard three of three, and the only one that catches an
		// endpoint nobody remembered to keep off the allowlist.
		if err := s.degrade(ctx, common.ReasonDatesIgnored, root); err != nil {
			return err
		}
		s.recordDegradation()
		s.reconcile(ctx)
		return nil
	}

	if err := s.fetchIssueWindow(ctx, root, 0); err != nil {
		return s.handleWalkError(ctx, err)
	}
	if s.degradedReason != "" {
		s.recordDegradation()
	}
	s.reconcile(ctx)
	return nil
}

// fetchIssueWindow handles one window: probe it, then split it, fetch
// it, or declare it atomic.
func (s *issueSlicer) fetchIssueWindow(ctx context.Context, w issueWindow, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Both of these are backstops rather than terminators — the walk
	// stops on one-second windows and every bound is second-aligned, so
	// reaching either means the arithmetic misbehaved. Neither returns
	// an error: isNonFatalHTTPErr covers 403 and 404 only, so a
	// diagnostic error from here would abort a multi-hour extract over
	// a case that costs, at worst, the completeness of one project.
	if !w.safeToRequest() || depth >= maxSliceDepth {
		return s.degrade(ctx, common.ReasonMaxDepth, w)
	}

	total, err := s.probeWindowTotal(ctx, w)
	if err != nil {
		return err
	}
	if total == 0 {
		// Genuinely empty, not "we could not tell": probeWindowTotal
		// turns an unreadable total into an error.
		return nil
	}

	if total > s.ceiling {
		if w.splittable() {
			left, right := splitWindow(w)
			if err := s.fetchIssueWindow(ctx, left, depth+1); err != nil {
				return err
			}
			return s.fetchIssueWindow(ctx, right, depth+1)
		}
		return s.fetchAtomicWindow(ctx, w, total, depth)
	}
	return s.fetchWindowItems(ctx, w, total)
}

// fetchWindowItems reads a window the probe said would fit, and absorbs
// it.
func (s *issueSlicer) fetchWindowItems(ctx context.Context, w issueWindow, probed int) error {
	res, err := s.fetchWindow(ctx, w, false)
	if err != nil {
		return err
	}
	s.windows++
	if err := s.absorb(res.Items); err != nil {
		return err
	}
	if !res.Truncated {
		return nil
	}

	// The window fitted when it was probed and did not when it was
	// fetched: issues were created inside it in between, which the
	// always-open rightmost window is precisely where to expect. It is
	// a real loss, so it is recorded — with the reason the fetch itself
	// found, and never as atomic_window. That reason is a claim about a
	// single second, and the per-second counts it carries are the
	// measurement this change exists to collect.
	lost := 0
	if res.TotalKnown && res.Total > res.Fetched {
		lost = res.Total - res.Fetched
	}
	s.e.Logger.Warn("an issue window grew past the result ceiling between the probe and the fetch",
		"project", s.scope.ProjectKey, "branch", s.scope.Branch, "window", w.label(),
		"probed", probed, "total", res.Total, "fetched", res.Fetched, "lost", lost)
	start, end := w.bounds()
	s.record(common.TruncationRecord{
		Endpoint:    issuesSearchAPI,
		Reason:      res.Reason,
		Scope:       s.scope,
		Total:       res.Total,
		TotalKnown:  res.TotalKnown,
		Fetched:     res.Fetched,
		Lost:        lost,
		PageSize:    res.PageSize,
		PageLimit:   res.PageLimit,
		WindowStart: start,
		WindowEnd:   end,
	})
	return nil
}

// fetchAtomicWindow is the seam, and the one place this change admits
// defeat in writing.
//
// It is called for exactly one shape: a window one second wide that
// still holds more issues than the result window will return. Date
// bisection has nothing left to subdivide, because the narrowest range
// the API can express already contains too much. This is not a corner
// case — a migrated project stamps every issue of its first analysis
// with one timestamp, and the measured example holds 8,916 of 14,903
// issues in a single second, so a project two and a half times its size
// would lose twelve thousand of them here.
//
// The contract is "return everything the API will give for this window,
// and record exactly what it will not". Today that is the capped fetch
// plus a record naming the second and the missing count. A secondary
// partition axis replaces this body and nothing else: types x
// severities was measured to partition exactly inside a single second
// on SonarQube 2026.4.1 (a 15-cell cross product summing to 8,916 with
// zero delta, dominant cell 40.9%, so headroom to about 24,500 issues
// per second), and the clone-and-set parameter discipline, the dedup
// verifier, the reconciliation and the artefact all work unchanged
// around it — UnexplainedDrift becomes its free correctness test. It is
// deferred because that engaged path cannot be proven against any real
// server available to us, and because every atomic_window record this
// version writes measures the distribution the follow-up needs (#574).
func (s *issueSlicer) fetchAtomicWindow(ctx context.Context, w issueWindow, total, depth int) error {
	// An atomic_window record is a claim about ONE creation second, and
	// the report states it in those words. A window with an open edge
	// cannot support that claim: the request it sends omits a bound, so
	// it selects everything on that side, and the record would either
	// name no second at all or name one it does not measure. Close the
	// edge before declaring defeat, so the record always carries the
	// exact closed second it is about (#574).
	if w.openStart || w.openEnd {
		return s.closeAtomicWindow(ctx, w, total, depth)
	}

	res, err := s.fetchWindow(ctx, w, false)
	if err != nil {
		return err
	}
	s.windows++
	if err := s.absorb(res.Items); err != nil {
		return err
	}

	lost := total - res.Fetched
	if lost < 0 {
		lost = 0
	}
	s.e.Logger.Warn("more issues share one creation second than the API will return - date slicing cannot subdivide further",
		"project", s.scope.ProjectKey, "branch", s.scope.Branch, "window", w.label(),
		"total", total, "fetched", res.Fetched, "lost", lost)
	start, end := w.bounds()
	s.record(common.TruncationRecord{
		Endpoint:    issuesSearchAPI,
		Reason:      common.ReasonAtomicWindow,
		Scope:       s.scope,
		Total:       total,
		TotalKnown:  true,
		Fetched:     res.Fetched,
		Lost:        lost,
		PageSize:    res.PageSize,
		PageLimit:   res.PageLimit,
		WindowStart: start,
		WindowEnd:   end,
	})
	return nil
}

// closeAtomicWindow turns an atomic candidate that still has an open
// edge into one that does not, without dropping whatever the open edge
// was covering.
//
// The open edge exists so the partition is total: the leftmost window
// of the tree never sends createdAfter and the rightmost never sends
// createdBefore, so nothing can fall outside the walk. That is exactly
// why an outermost leaf must not simply have its bound closed and be
// done with: the arithmetic bound came from a creation-date probe,
// which is a hint with no correctness authority, while the open request
// is the thing that actually selects the data.
//
// So there are two possibilities, and they are distinguished rather
// than guessed at:
//
//   - The open side reaches past the arithmetic bound — issues created
//     since the range probe ran, or a probe whose answer was simply
//     wrong. The window is not atomic at all, it is mis-bounded. Widen
//     it to cover what is really there, keep the edge open, and hand it
//     back to the walk: it is now at least two seconds wide, so it
//     bisects instead of returning here.
//   - Nothing lies beyond the arithmetic bound. The open request and
//     the closed one select the same issues, so closing the edge costs
//     nothing and buys a record that names the second it is a claim
//     about.
//
// The closed window is then re-probed rather than reusing the open
// window's count, so Total, Fetched and Lost are all statements about
// the same bounds the record prints. If that re-probe comes back under
// the ceiling the window was never atomic — a second that shrank under
// the ceiling while the walk ran is a fetchable second, and saying so
// is more honest than filing an API limit that no longer applies.
func (s *issueSlicer) closeAtomicWindow(ctx context.Context, w issueWindow, openTotal, depth int) error {
	if widened, ok := s.widenOpenEdge(ctx, w); ok {
		s.e.Logger.Debug("an issue window's open edge reached past its arithmetic bound - widening instead of declaring it atomic",
			"project", s.scope.ProjectKey, "branch", s.scope.Branch,
			"window", w.label(), "widened", widened.label())
		return s.fetchIssueWindow(ctx, widened, depth+1)
	}

	closed := issueWindow{start: w.start, end: w.end}
	total, err := s.probeWindowTotal(ctx, closed)
	if err != nil {
		return err
	}
	if total < openTotal {
		// The open edge still selects more than the closed second even
		// though the creation-date probe could not say where the rest
		// lies — a probe that failed, or a count that moved between
		// the two requests. The residue is deliberately NOT absorbed
		// into the atomic record, whose numbers have to be about the
		// second it names; the closing reconciliation is what surfaces
		// it, as unexplained drift.
		s.e.Logger.Warn("closing an issue window's open edge left issues outside it - the reconciliation will account for them",
			"project", s.scope.ProjectKey, "branch", s.scope.Branch,
			"window", w.label(), "closed", closed.label(),
			"openTotal", openTotal, "inSecond", total)
	}
	if total == 0 {
		return nil
	}
	if total > s.ceiling {
		return s.fetchAtomicWindow(ctx, closed, total, depth)
	}
	return s.fetchWindowItems(ctx, closed, total)
}

// widenOpenEdge asks whether w's open edge selects anything beyond its
// arithmetic bound, and returns the window that does cover it.
//
// One one-row creation-date probe, carrying w's own parameters, so the
// answer is about this window rather than about the project. A probe
// that fails or returns nothing answers "no" — the walk then closes the
// edge, and any residue it thereby leaves behind is caught by the
// closing reconciliation as unexplained drift rather than being
// absorbed silently.
//
// Termination: widening always moves the bound strictly outward past a
// real issue's creation second, and the leaf the widened window
// bisects down to has that issue inside it, so the next probe answers
// "no". A server creating issues faster than the walk descends is
// bounded by maxSliceDepth, which is what it is for.
func (s *issueSlicer) widenOpenEdge(ctx context.Context, w issueWindow) (issueWindow, bool) {
	if w.openEnd {
		// end is EXCLUSIVE and newest is the inclusive second of a real
		// issue, hence +1s — the same conversion probeIssueRange makes,
		// and for the same reason.
		if newest, ok := s.probeCreationDate(ctx, w, false); ok && !newest.Before(w.end) {
			w.end = newest.Add(time.Second)
			return w, true
		}
	}
	if w.openStart {
		// start is INCLUSIVE, so the oldest issue's own second is the
		// bound: no offset, and none is wanted.
		if oldest, ok := s.probeCreationDate(ctx, w, true); ok && oldest.Before(w.start) {
			w.start = oldest
			return w, true
		}
	}
	return w, false
}

// dateBoundRejected marks an HTTP failure of a request that carried a
// createdAfter / createdBefore bound, so the walk can tell "this server
// will not accept our date parameters" apart from "this server is
// broken".
//
// It is a wrapper rather than a sentinel because the underlying error
// still has to reach the log and, when the undated fallback fails too,
// the caller: Unwrap keeps errors.Is/As working all the way down to
// *common.HTTPError.
type dateBoundRejected struct {
	window issueWindow
	err    error
}

func (e *dateBoundRejected) Error() string {
	return fmt.Sprintf("issues window %s was rejected by the server: %v", e.window.label(), e.err)
}

func (e *dateBoundRejected) Unwrap() error { return e.err }

// asDateBoundRejection tags err when it is an HTTP error answering a
// request that carried a date bound, and returns it untouched
// otherwise.
//
// This is the fourth guard, and the one #574 was missing. Three guards
// covered a server that silently IGNORES createdAfter / createdBefore
// (HTTP 200, unchanged total); none covered a server that REJECTS
// them. FormatSQDate emits one hardcoded layout, verified against
// exactly one server version out of the 9.9-to-2026.x range this tool
// supports, and a WAF or proxy in front of the instance can refuse the
// parameters just as effectively as a version whose date parsing
// differs. Before this, such an error escaped the slicer,
// isNonFatalHTTPErr did not cover it (403 and 404 only),
// iterateBranches wrapped it and executePhases failed the ENTIRE run —
// every remaining project and phase lost, where before #574 the same
// deployment extracted 10,000 issues and exited 0.
//
// 403 and 404 are excluded so they keep today's behaviour exactly: the
// call site already treats them as "this project's issues are not
// available to us", which is a statement about permissions, not about
// dates. Every other status is included, and that is safe rather than
// sloppy, because the label is only written once the UNDATED fallback
// has SUCCEEDED — a server fault that is nothing to do with the date
// parameters fails that fallback too and falls straight through to
// today's error path.
//
// An error on an UNDATED request is never tagged: w.dated() is the
// whole precondition, so the entry fetch, the fallback fetch and the
// reconciliation probe are all untouched.
func asDateBoundRejection(w issueWindow, err error) error {
	if err == nil || !w.dated() || isNonFatalHTTPErr(err) {
		return err
	}
	var he *common.HTTPError
	if !errors.As(err, &he) {
		return err
	}
	return &dateBoundRejected{window: w, err: err}
}

// handleWalkError decides what a walk-ending error means.
//
// A rejected date bound is a degradation, not a failure: log it, fetch
// the project the way this task fetched it before #574, record the
// shortfall under the reason that names the true cause, and return nil
// so the run continues. Anything else keeps today's behaviour — the
// issues already written stay on disk, an incomplete_slice record says
// what is missing around them, and the error propagates.
func (s *issueSlicer) handleWalkError(ctx context.Context, err error) error {
	var rejected *dateBoundRejected
	if !errors.As(err, &rejected) {
		s.recordIncompleteSlice()
		return err
	}
	s.e.Logger.Warn("the issue search refused its date parameters - falling back to the undated fetch",
		"project", s.scope.ProjectKey, "branch", s.scope.Branch,
		"window", rejected.window.label(), "err", rejected.err)
	if derr := s.degrade(ctx, common.ReasonDatesRejected, rejected.window); derr != nil {
		// The undated fallback failed too, so this was never about the
		// dates. Report it the way any other mid-walk failure is
		// reported.
		s.recordIncompleteSlice()
		return derr
	}
	s.recordDegradation()
	s.reconcile(ctx)
	return nil
}

// fetchWindow runs one capped fetch for w.
//
// The mechanical clamp record is always suppressed here because the
// slicer knows the real reason and writes it itself: a clamp on the
// entry fetch is the trigger to go and fetch the rest rather than a
// loss, and recording both the clamp and the true cause would
// double-count the same missing issues in the artefact's totals.
func (s *issueSlicer) fetchWindow(ctx context.Context, w issueWindow, stopOnTruncation bool) (common.PageResult, error) {
	if !w.safeToRequest() {
		// Unreachable: fetchIssueWindow gates every window before it
		// gets here, and the root window is open on both edges. Kept
		// loud rather than dropped, so a future refactor that adds a
		// request path around that gate fails here instead of sending
		// a request the server answers with HTTP 400.
		return common.PageResult{}, fmt.Errorf("refusing a zero-width issue window %s", w.label())
	}
	res, err := s.e.Raw.GetPaginatedResult(ctx, PaginatedOpts{
		Path:                     issuesSearchAPI,
		Params:                   windowParams(s.base, w),
		ResultKey:                issueResultKey,
		MaxPageSize:              issuePageSize,
		PageLimit:                issuePageLimit,
		StopOnTruncation:         stopOnTruncation,
		SuppressTruncationRecord: true,
		Scope:                    s.scope,
	})
	// PagesRead is zero on failure even though a request was made, so
	// the audit count must not trust it blindly.
	s.requests += max(1, res.PagesRead)
	return res, asDateBoundRejection(w, err)
}

// probeWindowTotal asks how many issues a window holds, with one
// one-row request.
//
// This is what keeps an internal node cheap: it learns that it must
// split from a single ps=1 response, instead of paginating twenty heavy
// pages it is about to throw away. Clone-and-set again, deleting
// nothing, so the probe counts the same selection the fetch will read —
// one row of additionalFields=_all payload is a negligible price for
// that.
//
// A response with no parseable total is an ERROR for this window, never
// an empty window. ExtractTotal returns 0 for an unmarshal failure, for
// a missing key and for a genuine zero alike, and treating either of
// the first two as "nothing here" is precisely how a window walk drops
// a project's issues while reporting success.
func (s *issueSlicer) probeWindowTotal(ctx context.Context, w issueWindow) (int, error) {
	if !w.safeToRequest() {
		// Unreachable for the same reason as in fetchWindow, and loud
		// for the same reason.
		return 0, fmt.Errorf("refusing a zero-width issue window %s", w.label())
	}
	params := windowParams(s.base, w)
	params.Set("p", "1")
	params.Set("ps", "1")

	s.requests++
	body, err := s.e.Raw.Get(ctx, issuesSearchAPI, params)
	if err != nil {
		return 0, asDateBoundRejection(w, err)
	}
	total, ok := common.ExtractTotalOK(body, issueTotalKey)
	if !ok {
		return 0, fmt.Errorf("issues window %s: response carried no parseable %s", w.label(), issueTotalKey)
	}
	return total, nil
}

// probeIssueRange asks for the oldest and newest creation dates in the
// caller's selection, with two one-row requests.
//
// The answer feeds midpoint ARITHMETIC ONLY and has no correctness
// authority whatsoever: the root window's edges stay open whatever it
// says, so a wrong, collapsed or failed probe costs extra recursion
// levels and nothing else. Nothing can fall outside a partition whose
// outermost windows have no bounds.
//
// A range too narrow to bisect is treated as no answer at all. Such an
// answer is not necessarily wrong — a project whose every issue was
// stamped by one first analysis really does sit inside a single second
// — but a root that cannot be split would be reported as one atomic
// window with no bounds to name, and the exact second is the most
// valuable thing that record carries. Bisecting the default range
// instead costs about sixty one-row probes and arrives at the same
// second with its bounds attached.
func (s *issueSlicer) probeIssueRange(ctx context.Context) issueWindow {
	oldest, oldestKnown := s.probeCreationDate(ctx, rootIssueWindow(), true)
	newest, newestKnown := s.probeCreationDate(ctx, rootIssueWindow(), false)
	// newest is the INCLUSIVE creation second of a real issue and
	// end is an EXCLUSIVE bound, so the range has to reach one second
	// past it. This +1s is NOT the CloudVoyager e5fbecd0 mistake and
	// must not be "fixed" back: that bug offset a SPLIT MIDPOINT,
	// moving a seam away from the value both sides share and dropping
	// the second between them. This converts an inclusive data point
	// into the exclusive bound of the range that has to contain it.
	// Without it the rightmost leaf converges to [newest-1s, newest),
	// one second wide and therefore declared unsplittable, while the
	// request it actually sends drops createdBefore on the open edge
	// and so selects TWO real seconds — which, when they jointly
	// exceed the ceiling, drops recoverable issues and files them as
	// an unavoidable atomic_window (#574).
	probed := issueWindow{start: oldest, end: newest.Add(time.Second), openStart: true, openEnd: true}
	if oldestKnown && newestKnown && probed.splittable() {
		return probed
	}

	start, end := defaultIssueRange()
	s.e.Logger.Debug("creation-date range probe unusable - bisecting the default range instead",
		"project", s.scope.ProjectKey, "branch", s.scope.Branch,
		"oldestKnown", oldestKnown, "newestKnown", newestKnown,
		"start", common.FormatSQDate(start), "end", common.FormatSQDate(end))
	return issueWindow{start: start, end: end, openStart: true, openEnd: true}
}

// defaultIssueRange is the arithmetic fallback for an unusable range
// probe. 2006-01-01 predates SonarQube's first release, and the upper
// bound is only a hint as well: both edges of the root window are open,
// so an issue created after this instant — including during the run —
// still lands in the always-open rightmost window.
//
// The upper bound carries the same +1s as probeIssueRange, for the same
// reason: the current second is a second issues can be created IN, and
// truncating time.Now() to it and then using it as an EXCLUSIVE bound
// leaves the rightmost leaf one second short of the data it is supposed
// to cover (#574).
func defaultIssueRange() (time.Time, time.Time) {
	return time.Date(2006, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Now().UTC().Truncate(time.Second).Add(time.Second)
}

// probeCreationDate returns the creation date of the first issue in
// creation order, ascending (the oldest) or descending (the newest),
// within w's selection.
//
// It takes a window rather than always asking about the whole project
// because the atomic-window path needs the same question answered about
// one leaf: "does this window's OPEN edge reach past its arithmetic
// bound?" is the same query with the window's own parameters on it.
// Passing rootIssueWindow() asks about the caller's unfiltered
// selection, which is what the range probe wants.
//
// One non-paginated request with ps=1. CloudVoyager's equivalent went
// through its paginator at ps=1 and asked for page after page until it
// fell off the 10,000-result cliff; the file was deleted rather than
// fixed. A single request cannot do that.
func (s *issueSlicer) probeCreationDate(ctx context.Context, w issueWindow, oldestFirst bool) (time.Time, bool) {
	params := windowParams(s.base, w)
	params.Set("p", "1")
	params.Set("ps", "1")
	params.Set("s", "CREATION_DATE")
	params.Set("asc", strconv.FormatBool(oldestFirst))

	s.requests++
	body, err := s.e.Raw.Get(ctx, issuesSearchAPI, params)
	if err != nil {
		s.e.Logger.Debug("creation-date probe failed - the walk will bisect the default range",
			"project", s.scope.ProjectKey, "branch", s.scope.Branch, "window", w.label(), "asc", oldestFirst, "err", err)
		return time.Time{}, false
	}
	items, err := common.ExtractArray(body, issueResultKey)
	if err != nil || len(items) == 0 {
		s.e.Logger.Debug("creation-date probe returned no issue - the walk will bisect the default range",
			"project", s.scope.ProjectKey, "branch", s.scope.Branch, "window", w.label(), "asc", oldestFirst, "err", err)
		return time.Time{}, false
	}
	created, err := common.ParseSQDate(extractField(items[0], "creationDate"))
	if err != nil {
		s.e.Logger.Debug("creation-date probe returned an unparseable date - the walk will bisect the default range",
			"project", s.scope.ProjectKey, "branch", s.scope.Branch, "window", w.label(), "asc", oldestFirst, "err", err)
		return time.Time{}, false
	}
	return created.Truncate(time.Second), true
}

// datesRespected is the depth-0 partition self-check: two one-row
// probes, one per half of the root split.
//
// It refuses on ONE signature only — both halves reporting at least the
// parent's total. With an exact partition and a parent over the
// ceiling, that is impossible unless the server dropped the
// parameters, which is exactly what /api/hotspots/search does: HTTP
// 200, unchanged total, unknown parameters silently ignored.
//
// It deliberately does NOT refuse when the halves fail to SUM to the
// parent. On a live instance issues are created and closed while the
// walk runs, so a few units of disagreement is the normal state of the
// world, and treating that as a fault would disable the feature for
// every busy customer. The disagreement is logged at Debug; the closing
// reconciliation is where the numbers have to add up.
func (s *issueSlicer) datesRespected(ctx context.Context, root issueWindow) (bool, error) {
	if !root.splittable() {
		// Nothing to verify: a root this narrow emits no seam, so
		// there is no partition to check.
		return true, nil
	}
	left, right := splitWindow(root)
	leftTotal, err := s.probeWindowTotal(ctx, left)
	if err != nil {
		return false, err
	}
	rightTotal, err := s.probeWindowTotal(ctx, right)
	if err != nil {
		return false, err
	}

	if leftTotal >= s.initialTotal && rightTotal >= s.initialTotal {
		s.e.Logger.Warn("the issue search ignored its date parameters - both halves of the split reported the whole project",
			"project", s.scope.ProjectKey, "branch", s.scope.Branch,
			"total", s.initialTotal, "left", leftTotal, "right", rightTotal,
			"seam", common.FormatSQDate(root.mid()))
		return false, nil
	}
	if leftTotal+rightTotal != s.initialTotal {
		s.e.Logger.Debug("issue split does not sum to the project total - treating the difference as churn",
			"project", s.scope.ProjectKey, "branch", s.scope.Branch,
			"total", s.initialTotal, "left", leftTotal, "right", rightTotal)
	}
	return true, nil
}

// absorb dedups one window's items and hands the new ones to the sink.
//
// Items with no key are kept and counted, never dropped: an issue the
// response gave us without an identifier is still an issue, and
// discarding it to protect a bookkeeping invariant is how a slicer ends
// up claiming a completeness it does not have.
func (s *issueSlicer) absorb(items []json.RawMessage) error {
	fresh := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		key := extractField(raw, "key")
		if key == "" {
			s.keyless++
			fresh = append(fresh, raw)
			continue
		}
		if s.seen[key] {
			s.duplicates++
			continue
		}
		s.seen[key] = true
		fresh = append(fresh, raw)
	}
	s.unique += len(fresh)
	if len(fresh) == 0 {
		return nil
	}
	return s.sink(fresh)
}

// degrade absorbs a slicer-internal refusal, and never returns an error
// of its own.
//
// Every refusal in this file lands here: the endpoint proved it ignores
// the date parameters, the depth backstop fired, or a window came out
// zero-width. None of them is a reason to fail the extract — the
// fallback is the request this task made before #574, so the task still
// writes what it always wrote — and the reason is latched so the walk's
// closing record names the true cause instead of the mechanical clamp
// that produced it.
//
// The fallback runs at most once per walk. Anything the walk already
// delivered is deduped out of it.
func (s *issueSlicer) degrade(ctx context.Context, reason common.TruncationReason, w issueWindow) error {
	s.e.Logger.Warn("issue date slicing refused - falling back to the capped fetch",
		"project", s.scope.ProjectKey, "branch", s.scope.Branch,
		"reason", string(reason), "window", w.label())
	if s.degradedReason == "" {
		s.degradedReason = reason
	}
	if s.fellBack {
		return nil
	}
	s.fellBack = true

	res, err := s.fetchWindow(ctx, rootIssueWindow(), false)
	if err != nil {
		return err
	}
	s.windows++
	return s.absorb(res.Items)
}

// recordDegradation writes the single record a degraded walk leaves
// behind, after the walk has finished and the counts are final.
//
// One record per walk, carrying the walk's own numbers rather than the
// refusing window's: what the operator needs is that this project's
// issue set is short and by how much, and only the project-level
// arithmetic makes that number true. Writing it while the walk was
// still running would have fixed a count that was still moving.
func (s *issueSlicer) recordDegradation() {
	s.record(common.TruncationRecord{
		Endpoint:   issuesSearchAPI,
		Reason:     s.degradedReason,
		Scope:      s.scope,
		Total:      s.initialTotal,
		TotalKnown: s.totalKnown,
		Fetched:    s.unique,
		Lost:       s.unaccounted(),
		PageSize:   s.pageSize,
		PageLimit:  s.pageLimit,
	})
}

// recordIncompleteSlice is the record that keeps a failed walk honest.
//
// The issues already handed to the sink are on disk and stay there —
// discarding them would be a second loss on top of the first — so what
// is needed is a statement of what is missing around them. It is
// written on ANY error during the walk, including one that struck
// before the first window was written: there the count is the whole
// project, which is the honest number, because the entry fetch's single
// page was discarded rather than written.
func (s *issueSlicer) recordIncompleteSlice() {
	s.record(common.TruncationRecord{
		Endpoint:   issuesSearchAPI,
		Reason:     common.ReasonIncompleteSlice,
		Scope:      s.scope,
		Total:      s.initialTotal,
		TotalKnown: s.totalKnown,
		Fetched:    s.unique,
		Lost:       s.unaccounted(),
		PageSize:   s.pageSize,
		PageLimit:  s.pageLimit,
	})
}

// unaccounted is how many of the project's issues never reached the
// sink, and 0 when the server never told us how many there were — an
// unknown loss is reported as unknown, never as zero.
func (s *issueSlicer) unaccounted() int {
	if !s.totalKnown || s.initialTotal <= s.unique {
		return 0
	}
	return s.initialTotal - s.unique
}

// reconcile is the check neither CloudVoyager nor SPEC-006 has: after
// the walk, ask the source how many issues it has now, and see whether
// the numbers add up.
//
// The re-probe is what separates a seam bug from a busy server. Churn
// is the movement in the source's own count while the walk ran, and it
// is the honest tolerance: a drift of seven is fully explained by seven
// issues closing mid-walk and is evidence of nothing. Only the part of
// the drift that churn cannot explain produces a record, and the raw
// numbers are persisted either way so a reader can re-derive the
// arithmetic and disagree with the verdict.
//
// There is deliberately no branch here on "was anything unsplittable".
// Ordering that case first is what let the normal shape of a migrated
// project swallow every real fault: expectedLoss is a sum, the residual
// is computed once, and a dropped seam shows up even on a project that
// also has a genuinely atomic second.
func (s *issueSlicer) reconcile(ctx context.Context) {
	after := s.initialTotal
	if total, err := s.probeWindowTotal(ctx, rootIssueWindow()); err == nil {
		after = total
	} else {
		s.e.Logger.Debug("closing issue-count probe failed - reconciling against the opening total",
			"project", s.scope.ProjectKey, "branch", s.scope.Branch, "err", err)
	}

	audit := common.Reconciliation{
		Scope:        s.scope,
		TotalBefore:  s.initialTotal,
		TotalAfter:   after,
		Unique:       s.unique,
		Duplicates:   s.duplicates,
		Keyless:      s.keyless,
		ExpectedLoss: s.expectedLoss,
		Windows:      s.windows,
		Requests:     s.requests,
	}
	s.e.Truncation.RecordReconciliation(audit)

	s.e.Logger.Info("issue slicing complete",
		"project", s.scope.ProjectKey, "branch", s.scope.Branch,
		"total", s.initialTotal, "collected", s.unique, "windows", s.windows,
		"requests", s.requests, "duplicates", s.duplicates, "keyless", s.keyless,
		"expectedLoss", s.expectedLoss, "churn", audit.Churn(), "drift", audit.Drift())

	if s.duplicates > 0 {
		// Not a loss, so not a record — but a half-open partition
		// cannot return the same issue twice, so this is the dedup set
		// reporting that the seams are not doing what this code
		// believes they do.
		s.e.Logger.Warn("the issue windows returned the same issue more than once - the partition is not exact",
			"project", s.scope.ProjectKey, "branch", s.scope.Branch, "duplicates", s.duplicates)
	}

	drift := audit.UnexplainedDrift()
	if drift == 0 {
		return
	}
	lost := drift
	if lost < 0 {
		lost = 0
	}
	s.e.Logger.Warn("the issue count could not be reconciled after slicing",
		"project", s.scope.ProjectKey, "branch", s.scope.Branch,
		"total", s.initialTotal, "collected", s.unique,
		"expectedLoss", s.expectedLoss, "churn", audit.Churn(), "unexplainedDrift", drift)
	s.record(common.TruncationRecord{
		Endpoint:   issuesSearchAPI,
		Reason:     common.ReasonCountDrift,
		Scope:      s.scope,
		Total:      s.initialTotal,
		TotalKnown: s.totalKnown,
		Fetched:    s.unique,
		Lost:       lost,
		PageSize:   s.pageSize,
		PageLimit:  s.pageLimit,
	})
}

// record hands a truncation record to the run's tracker and adds its
// loss to the walk's expected total.
//
// It goes to the executor's tracker rather than through the raw
// client's observer because these records describe the WALK, not a
// single response: the client cannot know that a clamp inside a
// one-second window is an API limit no retry will fix. It is the same
// tracker the observer feeds, so both channels land in one artefact,
// and every method on it is nil-safe for executors built without one.
func (s *issueSlicer) record(rec common.TruncationRecord) {
	s.expectedLoss += rec.Lost
	s.e.Truncation.Record(rec)
}
