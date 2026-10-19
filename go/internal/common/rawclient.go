// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// HTTPError represents an HTTP error response with a status code.
type HTTPError struct {
	StatusCode int
	Method     string
	URL        string
	Body       string

	// Elapsed is how long the response carrying StatusCode took, from
	// the last time the request was sent. Waiting for a rate-limit slot
	// and the SDK's retries before that attempt do not count.
	Elapsed time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d %s %s - %s", e.StatusCode, e.Method, e.URL, e.Message())
}

// Message returns the human-readable error text extracted from a
// SonarQube JSON error body (the first `errors[*].msg`). Falls back to
// the raw body when the response is not the expected JSON shape, so
// non-API errors are never accidentally hidden.
func (e *HTTPError) Message() string {
	if e.Body == "" {
		return ""
	}
	var obj struct {
		Errors []struct {
			Msg string `json:"msg"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(e.Body), &obj); err != nil || len(obj.Errors) == 0 {
		return e.Body
	}
	msgs := make([]string, 0, len(obj.Errors))
	for _, item := range obj.Errors {
		if item.Msg != "" {
			msgs = append(msgs, item.Msg)
		}
	}
	if len(msgs) == 0 {
		return e.Body
	}
	return strings.Join(msgs, "; ")
}

// IsHTTPError checks whether an error is an HTTPError with one of the given status codes.
func IsHTTPError(err error, codes ...int) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	for _, c := range codes {
		if he.StatusCode == c {
			return true
		}
	}
	return false
}

// RawClient makes HTTP requests against SonarQube endpoints and returns
// raw JSON. It reuses the sqapi.Client's authenticated, retrying HTTP client.
type RawClient struct {
	httpClient *http.Client
	baseURL    string // normalised with trailing slash

	// pageSizes remembers the page size a fallback settled on, per API
	// endpoint. NewSharedRawClient shares one table per server.
	pageSizes *pageSizeTable

	// onTruncation receives one record per truncated paginated fetch.
	// It lives on the client rather than in PaginatedOpts so a single
	// installation covers every call site, including the ones nobody
	// remembers to wire up — which is exactly where the silent
	// truncation classes hide (#574).
	onTruncation func(TruncationRecord)
}

// NewRawClient wraps an sqapi.Client's HTTP infrastructure. Its learned
// page sizes are its own; see NewSharedRawClient.
func NewRawClient(httpClient *http.Client, baseURL string) *RawClient {
	return &RawClient{
		httpClient: httpClient,
		baseURL:    baseURL,
		pageSizes:  newPageSizeTable(),
	}
}

// sharedPageSizes holds one learned-page-size table per server base URL
// for NewSharedRawClient, keyed by sharedPageSizeKey.
var sharedPageSizes sync.Map // base URL -> *pageSizeTable

// sharedPageSizeKey is baseURL without its trailing slashes, so the same
// server shares one table however its URL was written.
func sharedPageSizeKey(baseURL string) string {
	return strings.TrimRight(baseURL, "/")
}

// ForgetSharedPageSizes drops what NewSharedRawClient clients learned for
// baseURL, so the next one starts again at the requested sizes. The table
// otherwise lives as long as the process. Tests use it to keep learned
// sizes from leaking between test servers.
func ForgetSharedPageSizes(baseURL string) {
	sharedPageSizes.Delete(sharedPageSizeKey(baseURL))
}

// NewSharedRawClient is NewRawClient with learned page sizes shared by
// every client this process builds with it for the same base URL. extract
// builds a separate client for the --project_key_regexp pre-flight
// listing and for the run that follows it; sharing means a fallback the
// pre-flight paid a gateway timeout to learn is not paid for again.
func NewSharedRawClient(httpClient *http.Client, baseURL string) *RawClient {
	table, _ := sharedPageSizes.LoadOrStore(sharedPageSizeKey(baseURL), newPageSizeTable())
	return &RawClient{
		httpClient: httpClient,
		baseURL:    baseURL,
		pageSizes:  table.(*pageSizeTable),
	}
}

// fastFallbackMemory is how long a page size learned only from fast
// failures is kept (see pageSizeTable). A variable so tests can shorten it.
var fastFallbackMemory = 5 * time.Minute

// pageSizeTable remembers, per endpoint path, the page size a fallback
// settled on, so later fetches start there.
//
// A size learned from a slow failure (at least slowGatewayTimeout) is
// kept for the life of the table: a gateway timeout is a reliable sign
// the page is too heavy, and finding it again would cost a full gateway
// delay. A size learned only from fast failures expires after
// fastFallbackMemory, and the next fetch then starts at the requested
// size again. A short burst of fast errors, such as a server restarting
// behind a load balancer, therefore cannot lower an endpoint's page size
// for the rest of a long run, and an endpoint that fails fast on large
// pages every time costs only a few fast requests to find out again.
//
// The two are kept apart, so a fast-learned size that expires falls back
// to the slow-learned one rather than to the requested size.
type pageSizeTable struct {
	mu    sync.Mutex
	sizes map[string]learnedPageSize
}

type learnedPageSize struct {
	sticky      int       // learned from a slow failure; 0 when none
	fast        int       // learned from fast failures only; 0 when none
	fastExpires time.Time // when fast stops applying
}

func newPageSizeTable() *pageSizeTable {
	return &pageSizeTable{sizes: make(map[string]learnedPageSize)}
}

// sizeFor returns the smallest of the requested page size and the sizes
// still remembered for path.
func (t *pageSizeTable) sizeFor(path string, requested int, now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.sizes[path]
	size := requested
	if l.sticky > 0 && l.sticky < size {
		size = l.sticky
	}
	if l.fast > 0 && now.Before(l.fastExpires) && l.fast < size {
		size = l.fast
	}
	return size
}

// remember records that a fetch of path succeeded at size after a
// fallback. sticky is whether any failure in that fetch was slow. A
// smaller size replaces a larger one, and re-learning the current
// fast-learned size starts its expiry again.
func (t *pageSizeTable) remember(path string, size int, sticky bool, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.sizes[path]
	switch {
	case sticky:
		if l.sticky == 0 || size < l.sticky {
			l.sticky = size
		}
	case l.fast == 0 || !now.Before(l.fastExpires) || size <= l.fast:
		l.fast, l.fastExpires = size, now.Add(fastFallbackMemory)
	}
	t.sizes[path] = l
}

// pageLimitAt returns the page cap to use at pageSize. A caller sizes
// PageLimit for the page size it asked for, so what it really sets is an
// item ceiling, PageLimit x requested size: 20 pages x 500 is the
// 10,000-result ceiling. After a fallback the cap becomes the number of
// smaller pages that reaches the same ceiling. A cap of 0 (no cap)
// stays 0.
//
// The cap rounds up, so a fallback never reads fewer items than the
// caller's ceiling: 3 pages of 500 become 8 pages of 200, not 7. It never
// rounds past ResultWindowLimit when the caller's ceiling was within it,
// because there the server answers HTTP 400 rather than a short page: a
// 10,000 ceiling at a size that does not divide it keeps the whole pages
// that fit. Every fallback size divides 10,000 and every cap in use, so
// today the ceiling is kept exactly.
func pageLimitAt(limit, requestedSize, pageSize int) int {
	if limit <= 0 || pageSize >= requestedSize {
		return limit
	}
	ceiling := limit * requestedSize
	pages := (ceiling + pageSize - 1) / pageSize // round up
	if ceiling <= ResultWindowLimit && pages*pageSize > ResultWindowLimit {
		pages = ResultWindowLimit / pageSize
	}
	return pages
}

// fallbackPageSizes is the page size sequence a fallback steps through.
var fallbackPageSizes = []int{500, 200, 100, 50}

// nextSmallerPageSize returns the next fallback page size below current:
// 500 -> 200 -> 100 -> 50, entering the sequence at 500 for a fetch that
// asked for more (api/ce/activity asks for 1000).
func nextSmallerPageSize(current int) (int, bool) {
	for _, size := range fallbackPageSizes {
		if current > size {
			return size, true
		}
	}
	return 0, false
}

// pageSizeFallbackStatuses are the statuses that send a paginated fetch
// to a smaller page size: what a server, or a gateway in front of it,
// answers when a page takes too long or is too heavy to build. Another
// 5xx, such as 501 Not Implemented or 505, says nothing about page size
// and is returned at once.
var pageSizeFallbackStatuses = []int{
	http.StatusInternalServerError,
	http.StatusBadGateway,
	http.StatusServiceUnavailable,
	http.StatusGatewayTimeout,
}

func isPageSizeFallbackStatus(code int) bool {
	for _, c := range pageSizeFallbackStatuses {
		if c == code {
			return true
		}
	}
	return false
}

// BaseURL returns the client's base URL.
func (r *RawClient) BaseURL() string {
	return r.baseURL
}

// HTTPClient returns the underlying *http.Client.
func (r *RawClient) HTTPClient() *http.Client {
	return r.httpClient
}

// SetTruncationObserver installs the callback that receives every
// truncation record this client produces. Install it once, before any
// task goroutine starts: the field is read without synchronisation on
// the request path, and a nil observer is the supported default (the
// unconditional Warn in recordTruncation still fires).
func (r *RawClient) SetTruncationObserver(fn func(TruncationRecord)) {
	r.onTruncation = fn
}

// Get performs a GET request and returns the full response body as raw JSON.
func (r *RawClient) Get(ctx context.Context, path string, params url.Values) (json.RawMessage, error) {
	body, err := r.doGet(ctx, path, params)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 && body[0] == '<' {
		return nil, fmt.Errorf("expected JSON but received HTML response from %s (check the server URL)", path)
	}
	return json.RawMessage(body), nil
}

// GetArray performs a GET and extracts an array at the given resultKey.
func (r *RawClient) GetArray(ctx context.Context, path, resultKey string, params url.Values) ([]json.RawMessage, error) {
	body, err := r.doGet(ctx, path, params)
	if err != nil {
		return nil, err
	}
	return ExtractArray(body, resultKey)
}

// GetRaw performs a GET and returns the raw response bytes (for non-JSON, e.g. XML).
func (r *RawClient) GetRaw(ctx context.Context, path string, params url.Values) ([]byte, error) {
	return r.doGet(ctx, path, params)
}

// PaginatedOpts configures a paginated fetch.
type PaginatedOpts struct {
	Path        string
	Params      url.Values // static params (p/ps added per page)
	ResultKey   string     // JSON key containing item array
	TotalKey    string     // dot-path to total count (default "paging.total")
	PageParam   string     // default "p"
	SizeParam   string     // default "ps"
	MaxPageSize int        // 0 = 500
	PageLimit   int        // 0 = no limit

	// Scope attributes any truncation record this fetch produces to the
	// task, project and branch that asked for it. Optional: an
	// unattributed record still beats a silent one.
	Scope TruncationScope

	// StopOnTruncation abandons the walk as soon as page 1 shows the
	// fetch cannot complete, instead of reading PageLimit pages that
	// will be thrown away. The issue slicer uses it so detecting "this
	// project needs slicing" costs one request rather than twenty.
	StopOnTruncation bool

	// SamplingCap declares that PageLimit is a deliberate sample of a
	// firehose rather than a workaround for the result ceiling — the
	// webhook delivery log, where capping is the intent. Truncation is
	// still reported in PageResult, but no record and therefore no
	// report bullet is produced.
	SamplingCap bool

	// SuppressTruncationRecord keeps a clamp out of the artefact
	// because the caller will record a more accurate reason itself.
	// The slicer's entry fetch sets it: a clamp is the trigger
	// to go and fetch the rest, not a loss.
	SuppressTruncationRecord bool
}

func (o *PaginatedOpts) applyDefaults() {
	if o.PageParam == "" {
		o.PageParam = "p"
	}
	if o.SizeParam == "" {
		o.SizeParam = "ps"
	}
	if o.MaxPageSize <= 0 {
		o.MaxPageSize = 500
	}
	if o.TotalKey == "" {
		o.TotalKey = "paging.total"
	}
}

// PageResult is the complete outcome of a paginated fetch: the items,
// plus everything the caller needs to know what was NOT fetched.
//
// PageSize and PageLimit carry the EFFECTIVE values the fetch actually
// used, after applyDefaults. Nothing downstream should re-derive them:
// applyDefaults has a pointer receiver and runs on a by-value copy of
// the opts, so a caller inspecting its own PaginatedOpts sees
// MaxPageSize == 0 and any page arithmetic built on that collapses to
// zero pages.
type PageResult struct {
	Items      []json.RawMessage
	Total      int  // paging total as reported by the server
	TotalKnown bool // false when the total key was absent or unparseable
	Fetched    int  // len(Items)
	PagesRead  int  // page requests that succeeded, page 1 included
	Requests   int  // page requests made, failed ones included; also set on error
	PageSize   int  // effective MaxPageSize
	PageLimit  int  // effective PageLimit (0 = uncapped)
	Truncated  bool // set only when Reason != ""
	Reason     TruncationReason
}

// GetPaginated fetches all pages and returns items as []json.RawMessage.
// It is a thin wrapper over GetPaginatedResult for the call sites that
// only want the items.
func (r *RawClient) GetPaginated(ctx context.Context, opts PaginatedOpts) ([]json.RawMessage, error) {
	res, err := r.GetPaginatedResult(ctx, opts)
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// GetPaginatedResult fetches all pages and reports what it could not
// fetch. The loop is unchanged; what changed in #574 is that it no
// longer computes the page count and then throws it away, which left
// every clamped fetch returning a short slice indistinguishable from a
// complete one.
//
// Detection happens after page 1, where the information is:
//
//   - no parseable total plus a completely full page: TotalPages(0, …)
//     is zero, the loop never runs, one page comes back and pagination
//     stops with no idea how much was left. ReasonUnknownTotal.
//   - the PageLimit clamp fires: the pages past the cap are never read.
//     ReasonPageLimitClamp, and with StopOnTruncation the walk is
//     abandoned right there.
//
// Truncated is set ONLY when a Reason was assigned. It is deliberately
// not derived from Fetched < Total: ExtractArray wraps an object at the
// result key (api/rules/search "actives") as a single element, so that
// comparison would fire on perfectly healthy responses.
//
// The page size adapts. An HTTP 500, 502, 503 or 504 (see
// pageSizeFallbackStatuses) moves the fetch to the next smaller size
// (500 -> 200 -> 100 -> 50) and carries on from where it stopped: items
// already read are kept up to the last offset the smaller pages can
// resume from (see paginationWalk.resize). No item is returned twice or
// skipped, but up to one smaller page of items is requested again. The
// size that works is remembered for opts.Path (see pageSizeTable). A
// failure at 50 is returned; a slow one only after two more tries at 50
// (see minPageSizeSlowRetries). When the fetch started above 50, the
// error is wrapped to name the sizes tried, from the first one down to
// 50. PageSize in the result is the size that succeeded.
// PageLimit grows as the size shrinks (see pageLimitAt), so the item
// ceiling a caller derives from PageSize x PageLimit does not fall with
// the fallback. Every request waits up to twice the client's timeout, so
// a gateway's 504 can arrive before the client gives up (see
// paginatedAttempt).
func (r *RawClient) GetPaginatedResult(ctx context.Context, opts PaginatedOpts) (PageResult, error) {
	opts.applyDefaults()

	requestedSize := opts.MaxPageSize
	startSize := r.pageSizes.sizeFor(opts.Path, requestedSize, time.Now())
	pageSize := startSize
	walk := newPaginationWalk()
	var st fallbackState

	for {
		attemptCtx, client := r.paginatedAttempt(ctx)
		result, err := r.walkPages(attemptCtx, client, opts, requestedSize, pageSize, walk)
		if err == nil {
			if pageSize < startSize {
				r.rememberFallback(opts.Path, pageSize, st.slowFailedAt)
			}
			return result, nil
		}

		next, err := st.next(opts.Path, startSize, pageSize, err)
		if err != nil {
			return PageResult{Requests: walk.requests}, err
		}
		if next < pageSize {
			walk.resize(next)
		}
		pageSize = next
	}
}

// fallbackState is what one paginated fetch carries between attempts.
type fallbackState struct {
	slowFailedAt   int // smallest size that failed slowly; 0 when none did
	minSizeRetries int // retries made at the minimum page size
}

// next decides what a fetch of path does after the attempt at pageSize
// failed with err: try a smaller size, try the minimum size again after
// a slow failure there, or return the error.
func (st *fallbackState) next(path string, startSize, pageSize int, err error) (int, error) {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || !isPageSizeFallbackStatus(httpErr.StatusCode) {
		return 0, err
	}
	slow := httpErr.Elapsed >= slowGatewayTimeout
	if slow {
		st.slowFailedAt = pageSize
	}

	if nextSize, ok := nextSmallerPageSize(pageSize); ok {
		slog.Warn(
			"paginated API request returned HTTP 5xx; retrying with smaller page size",
			"endpoint", path,
			"status", httpErr.StatusCode,
			"page_size", pageSize,
			"retry_page_size", nextSize,
		)
		return nextSize, nil
	}
	if slow && st.minSizeRetries < minPageSizeSlowRetries {
		st.minSizeRetries++
		slog.Warn(
			"paginated API request returned a slow HTTP 5xx at the minimum page size; retrying",
			"endpoint", path,
			"status", httpErr.StatusCode,
			"page_size", pageSize,
			"retry", st.minSizeRetries,
		)
		return pageSize, nil
	}
	return 0, minimumSizeFailure(path, startSize, pageSize, httpErr, err)
}

// minimumSizeFailure logs and returns the error of a fetch of path that
// failed at the minimum page size. When the fetch started above it, the
// error is wrapped to name the sizes tried; it still unwraps to httpErr.
func minimumSizeFailure(path string, startSize, pageSize int, httpErr *HTTPError, err error) error {
	slog.Error(
		"paginated API request failed with HTTP 5xx at minimum fallback page size",
		"endpoint", path,
		"page_size", pageSize,
		"status", httpErr.StatusCode,
		"error", err,
	)
	if pageSize == startSize {
		return err
	}
	return fmt.Errorf("%s failed at every page size from %d down to %d: %w",
		path, startSize, pageSize, err)
}

// rememberFallback records what a fetch of path that fell back learned.
// Only a slow failure is a reliable page-size signal, so the size just
// below the smallest one that failed slowly is kept for good. The size
// the fetch succeeded at is kept too, until it expires, when a fast
// failure took the fetch further down: 500 slow, 200 a fast blip and
// 100 working keeps 200 for good and 100 for fastFallbackMemory, so the
// endpoint goes back to 200 once the blip has passed.
func (r *RawClient) rememberFallback(path string, size, slowFailedAt int) {
	now := time.Now()
	if slowFailedAt == 0 {
		r.pageSizes.remember(path, size, false, now)
		return
	}
	sticky := slowFailedAt
	if below, ok := nextSmallerPageSize(slowFailedAt); ok {
		sticky = below
	}
	r.pageSizes.remember(path, sticky, true, now)
	if size < sticky {
		r.pageSizes.remember(path, size, false, now)
	}
}

// slowGatewayTimeout separates a 5xx that means "this page took too
// long" from a proxy blip. A gateway answers a slow upstream only after
// its own timeout, typically tens of seconds (AWS load balancers and
// nginx send 504 after 60s, Apache httpd 502), so a 500, 502, 503 or 504
// that took at least this long is a page-size signal. A faster one keeps
// the SDK's normal retries, which absorb a burst shorter than about 1
// second (they back off 0.1s, 0.2s and 0.4s). The time is measured from
// the request being sent, so waiting for a rate-limit slot does not
// count. A variable so tests can shorten it.
var slowGatewayTimeout = 5 * time.Second

// minPageSizeSlowRetries is how many more times a fetch already at the
// minimum page size tries again after a slow 5xx. Above the minimum, a
// slow 5xx moves to a smaller size instead of being retried; at the
// minimum there is none, and the SDK does not retry a slow 5xx in a
// paginated attempt, so these retries stand in for it. A server that is
// slow for a while gets three tries at 50, each with the full doubled
// timeout, and each resumes from the page that failed. A fast 5xx has
// already had the SDK's retries and is not tried again here.
const minPageSizeSlowRetries = 2

// paginatedTimeoutFactor is how many times the client's timeout a
// paginated request may wait. Common gateways (AWS load balancers,
// nginx, Azure Front Door) answer a slow upstream with 504 after 60s,
// the client's own default, so the two race and the client usually
// loses: it times out just before the 504 that would trigger the
// fallback. Twice the timeout waits that 504 out with room to spare.
const paginatedTimeoutFactor = 2

// paginatedAttempt returns the context and client for one pagination
// attempt. A slow 5xx is the fallback's signal, so the attempt
//   - waits up to paginatedTimeoutFactor times the client's timeout, so
//     a gateway's answer arrives before the client gives up, and
//   - tells the SDK not to retry a slow 500, 502, 503 or 504 at the same
//     size: an identical retry costs another full gateway delay, and
//     because http.Client.Timeout bounds every SDK attempt together, it
//     can turn a recoverable 5xx into a fatal client timeout.
//
// This holds at the minimum page size too, where there is nothing
// smaller to try: a gateway timeout there is reported as the 5xx it is,
// not as a client timeout that hides it, and GetPaginatedResult retries
// it itself (see minPageSizeSlowRetries).
//
// The longer-timeout client is a copy sharing the same Transport, so
// auth, retries and the connection pool are unchanged, and it is made
// per attempt so it always follows the client as configured now. Every
// request outside GetPaginatedResult uses the client unchanged.
func (r *RawClient) paginatedAttempt(ctx context.Context) (context.Context, *http.Client) {
	patient := *r.httpClient
	patient.Timeout *= paginatedTimeoutFactor
	return sqapi.WithoutRetryOnSlowStatus(ctx, slowGatewayTimeout, pageSizeFallbackStatuses...), &patient
}

// paginationWalk is what one paginated fetch has read so far. It lives
// across attempts, so an attempt at a smaller page size continues from
// where the failed one stopped.
type paginationWalk struct {
	items      []json.RawMessage
	total      int
	totalKnown bool
	started    bool // page 1 was read, so total and totalKnown are set
	nextPage   int  // next page to read, at the current page size
	aligned    bool // every page read was full, so len(items) is the offset reached
	lastFull   bool // the last page read was full
	pagesRead  int  // page requests that succeeded, across attempts
	requests   int  // page requests made, across attempts
}

func newPaginationWalk() *paginationWalk {
	w := &paginationWalk{}
	w.restart()
	return w
}

// restart forgets the items read so far, so the next attempt begins at
// page 1. The request counts are kept.
func (w *paginationWalk) restart() {
	w.items = nil
	w.total, w.totalKnown = 0, false
	w.started = false
	w.nextPage = 1
	w.aligned = true
	w.lastFull = false
}

// resize prepares the walk to continue at newSize. Page p at size s
// starts at offset (p-1) x s, so the walk keeps the items below the
// largest multiple of newSize it has reached and resumes at the page
// that starts there; the few items past that offset are read again as
// part of that page. When a short page was read, len(items) is not an
// offset any more, and the walk restarts from page 1.
func (w *paginationWalk) resize(newSize int) {
	keep := len(w.items) / newSize * newSize
	if !w.started || !w.aligned || keep == 0 {
		w.restart()
		return
	}
	w.items = w.items[:keep]
	w.nextPage = keep/newSize + 1
}

// add records a page that came back with items at pageSize.
func (w *paginationWalk) add(items []json.RawMessage, pageSize int) {
	w.items = append(w.items, items...)
	w.pagesRead++
	w.nextPage++
	w.lastFull = len(items) == pageSize
	w.aligned = w.aligned && w.lastFull
}

// bounds returns how many pages an attempt at pageSize reads, the page
// cap at that size, and whether the cap cut the walk short.
func (w *paginationWalk) bounds(limit, requestedSize, pageSize int) (pages, pageLimit int, clamped bool) {
	pageLimit = pageLimitAt(limit, requestedSize, pageSize)
	pages = TotalPages(w.total, pageSize)
	if !w.totalKnown {
		// Without a total there is no page count. Read enough pages to
		// cover one page at the requested size, stopping at the first
		// short one, so a fallback returns at least as much as the
		// original page would have, instead of less.
		pages = (requestedSize + pageSize - 1) / pageSize
	}
	if pageLimit > 0 && pages > pageLimit {
		// Without a total there is nothing to clamp: the result is
		// either complete (a short page) or unknown_total (a full one).
		return pageLimit, pageLimit, w.totalKnown
	}
	return pages, pageLimit, false
}

// walkPages reads one attempt's pages at pageSize through client,
// continuing from what walk already holds.
func (r *RawClient) walkPages(ctx context.Context, client *http.Client, opts PaginatedOpts,
	requestedSize, pageSize int, walk *paginationWalk) (PageResult, error) {

	if !walk.started {
		if err := r.startWalk(ctx, client, opts, pageSize, walk); err != nil {
			return PageResult{}, err
		}
	}
	pages, pageLimit, clamped := walk.bounds(opts.PageLimit, requestedSize, pageSize)
	if !(clamped && opts.StopOnTruncation) {
		if err := r.readPages(ctx, client, opts, pageSize, pages, walk); err != nil {
			return PageResult{}, err
		}
	}
	return r.finishPaginated(walk.result(pageSize, pageLimit, clamped), opts), nil
}

// startWalk reads page 1, which carries the total.
func (r *RawClient) startWalk(ctx context.Context, client *http.Client, opts PaginatedOpts,
	pageSize int, walk *paginationWalk) error {

	body, items, err := r.fetchPage(ctx, client, opts, 1, pageSize, walk)
	if err != nil {
		return err
	}
	walk.total, walk.totalKnown = ExtractTotalOK(body, opts.TotalKey)
	walk.items = make([]json.RawMessage, 0, walk.total)
	walk.started = true
	walk.add(items, pageSize)
	return nil
}

// readPages reads on from walk.nextPage up to pages. Without a total it
// reads on only while pages come back full. A caller that stops on
// truncation throws a truncated result away, so for it a full first page
// is enough to stop on, as it is at the requested size.
func (r *RawClient) readPages(ctx context.Context, client *http.Client, opts PaginatedOpts,
	pageSize, pages int, walk *paginationWalk) error {

	for walk.nextPage <= pages && (walk.totalKnown || (walk.lastFull && !opts.StopOnTruncation)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, items, err := r.fetchPage(ctx, client, opts, walk.nextPage, pageSize, walk)
		if err != nil {
			return err
		}
		walk.add(items, pageSize)
	}
	return nil
}

// result is the PageResult for what the walk holds, read at pageSize
// under pageLimit.
func (w *paginationWalk) result(pageSize, pageLimit int, clamped bool) PageResult {
	res := PageResult{
		Items:      w.items,
		Total:      w.total,
		TotalKnown: w.totalKnown,
		Fetched:    len(w.items),
		PagesRead:  w.pagesRead,
		Requests:   w.requests,
		PageSize:   pageSize,
		PageLimit:  pageLimit,
	}
	switch {
	case !w.totalKnown && w.lastFull:
		res.Reason = ReasonUnknownTotal
	case clamped:
		res.Reason = ReasonPageLimitClamp
	}
	return res
}

// fetchPage requests one page and returns its body and items.
func (r *RawClient) fetchPage(ctx context.Context, client *http.Client, opts PaginatedOpts,
	page, pageSize int, walk *paginationWalk) ([]byte, []json.RawMessage, error) {

	params := CloneParams(opts.Params)
	params.Set(opts.PageParam, strconv.Itoa(page))
	params.Set(opts.SizeParam, strconv.Itoa(pageSize))
	walk.requests++
	body, err := r.doGetWith(ctx, client, opts.Path, params)
	if err != nil {
		return nil, nil, err
	}
	items, err := ExtractArray(body, opts.ResultKey)
	if err != nil {
		return nil, nil, err
	}
	return body, items, nil
}

// finishPaginated stamps Truncated and emits the record. Both
// suppression flags leave Reason intact for the caller and only keep the
// record out of the artefact, so a suppressed fetch is never a fetch
// whose truncation the caller cannot see.
func (r *RawClient) finishPaginated(res PageResult, opts PaginatedOpts) PageResult {
	if res.Reason == "" {
		return res
	}
	res.Truncated = true
	if opts.SamplingCap || opts.SuppressTruncationRecord {
		return res
	}
	lost := 0
	if res.TotalKnown && res.Total > res.Fetched {
		lost = res.Total - res.Fetched
	}
	r.recordTruncation(TruncationRecord{
		Endpoint:   opts.Path,
		Reason:     res.Reason,
		Scope:      opts.Scope,
		Total:      res.Total,
		TotalKnown: res.TotalKnown,
		Fetched:    res.Fetched,
		Lost:       lost,
		PageSize:   res.PageSize,
		PageLimit:  res.PageLimit,
	})
	return res
}

// recordTruncation ALWAYS logs a Warn and only then offers the record to
// the observer. The warn is unconditional on purpose: migrate and
// regtest share this client and have no tracker installed, so the log is
// the only thing standing between them and a silent truncation.
func (r *RawClient) recordTruncation(rec TruncationRecord) {
	if rec.ObservedAt.IsZero() {
		rec.ObservedAt = time.Now().UTC()
	}
	slog.Default().Warn("API response truncated - not all results were fetched",
		"endpoint", rec.Endpoint,
		"reason", string(rec.Reason),
		"scope", rec.Scope.Label(),
		"total", rec.Total,
		"totalKnown", rec.TotalKnown,
		"fetched", rec.Fetched,
		"lost", rec.Lost)
	if r.onTruncation != nil {
		r.onTruncation(rec)
	}
}

// bodyReadRetryBackoff mirrors sq-api-go/retry.go's unexported
// defaultBackoff (100/200/400ms, 3 retries) for consistency. It is
// duplicated here, rather than imported, because retryTransport's
// RoundTrip-level retry never sees a body-read failure: an error from
// io.ReadAll(resp.Body) happens in the caller, after RoundTrip has
// already returned a success (resp, nil) — e.g. an HTTP/2 GOAWAY
// arriving mid-stream when a proxy/edge recycles the connection after
// hitting its own request quota (issue #616). doGet is GET-only (the
// only HTTP method RawClient exposes), so blindly re-issuing the whole
// request on a body-read failure is always safe: there is no request
// body to rewind, and GET is idempotent by definition.
var bodyReadRetryBackoff = []time.Duration{
	100 * time.Millisecond,
	200 * time.Millisecond,
	400 * time.Millisecond,
}

// sendClock records when a request was last written to the connection,
// through an httptrace hook that fires on every attempt the SDK's retry
// transport makes. It is what HTTPError.Elapsed is measured from, so the
// time a request spends waiting for a rate-limit slot, which happens
// before it is written, never makes a fast response look slow.
type sendClock struct{ last atomic.Pointer[time.Time] }

func (c *sendClock) trace(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			now := time.Now()
			c.last.Store(&now)
		},
	})
}

// since returns the time since the request was last sent, or since
// fallback if it was never seen being sent.
func (c *sendClock) since(fallback time.Time) time.Duration {
	if t := c.last.Load(); t != nil {
		return time.Since(*t)
	}
	return time.Since(fallback)
}

func (r *RawClient) doGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
	return r.doGetWith(ctx, r.httpClient, path, params)
}

// doGetWith is doGet through a given client: GetPaginatedResult passes a
// longer-timeout copy of the RawClient's own (see paginatedAttempt).
func (r *RawClient) doGetWith(ctx context.Context, client *http.Client, path string, params url.Values) ([]byte, error) {
	u := r.baseURL + path
	var sent sendClock
	req, err := http.NewRequestWithContext(sent.trace(ctx), http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if len(params) > 0 {
		req.URL.RawQuery = params.Encode()
	}

	var lastReadErr error
	for attempt := 0; ; attempt++ {
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		elapsed := sent.since(start)
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr == nil {
			if resp.StatusCode >= 400 {
				return nil, &HTTPError{
					StatusCode: resp.StatusCode,
					Method:     http.MethodGet,
					URL:        req.URL.String(),
					Body:       Truncate(body, 500),
					Elapsed:    elapsed,
				}
			}
			return body, nil
		}

		lastReadErr = readErr
		if attempt >= len(bodyReadRetryBackoff) {
			return nil, fmt.Errorf("reading response body from %s (after %d attempts): %w",
				req.URL.String(), attempt+1, lastReadErr)
		}
		select {
		case <-time.After(bodyReadRetryBackoff[attempt]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// ExtractArray extracts a JSON array at the given key from a raw JSON body.
func ExtractArray(body []byte, key string) ([]json.RawMessage, error) {
	// Detect HTML responses returned by reverse proxies, CDNs, or wrong URLs.
	if len(body) > 0 && body[0] == '<' {
		return nil, fmt.Errorf("expected JSON but received HTML response (check the server URL)")
	}
	if key == "" {
		var arr []json.RawMessage
		if err := json.Unmarshal(body, &arr); err != nil {
			return nil, fmt.Errorf("unmarshalling array: %w", err)
		}
		return arr, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("unmarshalling object for key %q: %w", key, err)
	}
	raw, ok := obj[key]
	if !ok {
		return nil, nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		// If the value is an object (not an array), wrap it as a single element.
		// Some SonarQube endpoints (e.g. actives in api/rules/search) return
		// objects at the result key instead of arrays.
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil {
			return []json.RawMessage{raw}, nil
		}
		return nil, fmt.Errorf("unmarshalling array at key %q: %w", key, err)
	}
	return arr, nil
}

// ExtractTotal extracts the total count from a JSON body using a
// dot-path key, returning 0 when the total cannot be read. Kept as the
// convenience form for the many callers that only need the number.
func ExtractTotal(body []byte, dotPath string) int {
	total, _ := ExtractTotalOK(body, dotPath)
	return total
}

// ExtractTotalOK extracts the total count and reports whether it was
// actually there. The distinction matters: a 0 from an unparseable
// body, a 0 from a missing key and a genuine zero are three different
// situations, and treating either of the first two as "nothing here" is
// how a fetch silently drops everything it was supposed to return
// (#574).
func ExtractTotalOK(body []byte, dotPath string) (int, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return 0, false
	}
	parts := SplitDotPath(dotPath)
	current := obj
	for i, part := range parts {
		raw, ok := current[part]
		if !ok {
			return 0, false
		}
		if i == len(parts)-1 {
			var n int
			if err := json.Unmarshal(raw, &n); err != nil {
				return 0, false
			}
			return n, true
		}
		if err := json.Unmarshal(raw, &current); err != nil {
			return 0, false
		}
	}
	return 0, false
}

// SplitDotPath splits a dot-separated path into parts.
func SplitDotPath(path string) []string {
	var parts []string
	start := 0
	for i := range path {
		if path[i] == '.' {
			if i > start {
				parts = append(parts, path[start:i])
			}
			start = i + 1
		}
	}
	if start < len(path) {
		parts = append(parts, path[start:])
	}
	return parts
}

// TotalPages calculates the number of pages needed.
func TotalPages(total, pageSize int) int {
	if total <= 0 || pageSize <= 0 {
		return 0
	}
	return int(math.Ceil(float64(total) / float64(pageSize)))
}

// CloneParams creates a deep copy of url.Values.
func CloneParams(p url.Values) url.Values {
	out := make(url.Values, len(p))
	for k, v := range p {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// Truncate truncates a byte slice to maxLen, appending "..." if truncated.
func Truncate(b []byte, maxLen int) string {
	if len(b) <= maxLen {
		return string(b)
	}
	return string(b[:maxLen]) + "..."
}
