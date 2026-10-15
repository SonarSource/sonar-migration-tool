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
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// HTTPError represents an HTTP error response with a status code.
type HTTPError struct {
	StatusCode int
	Method     string
	URL        string
	Body       string
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

	// Remember successful page sizes separately for each API endpoint.
	// The mutex protects concurrent extraction requests.
	pageSizeMu sync.Mutex
	pageSizes  map[string]int

	// onTruncation receives one record per truncated paginated fetch.
	// It lives on the client rather than in PaginatedOpts so a single
	// installation covers every call site, including the ones nobody
	// remembers to wire up — which is exactly where the silent
	// truncation classes hide (#574).
	onTruncation func(TruncationRecord)
}

// NewRawClient wraps an sqapi.Client's HTTP infrastructure.
func NewRawClient(httpClient *http.Client, baseURL string) *RawClient {
	return &RawClient{
		httpClient: httpClient,
		baseURL:    baseURL,
		pageSizes:  make(map[string]int),
	}
}

// pageSizeFor returns the smaller of the requested page size
// and any previously remembered size for the endpoint.
func (r *RawClient) pageSizeFor(path string, requested int) int {
	r.pageSizeMu.Lock()
	defer r.pageSizeMu.Unlock()

	if size, ok := r.pageSizes[path]; ok && size < requested {
		return size
	}
	return requested
}

// rememberPageSize stores a smaller successful page size
// for future requests to the same endpoint.
func (r *RawClient) rememberPageSize(path string, size int) {
	r.pageSizeMu.Lock()
	defer r.pageSizeMu.Unlock()

	if r.pageSizes == nil {
		r.pageSizes = make(map[string]int)
	}

	if current, ok := r.pageSizes[path]; !ok || size < current {
		r.pageSizes[path] = size
	}
}

// nextSmallerPageSize returns the next fallback page size.
// The fallback sequence is 500 -> 200 -> 100 -> 50.
func nextSmallerPageSize(current int) (int, bool) {
	for _, size := range []int{200, 100, 50} {
		if current > size {
			return size, true
		}
	}
	return 0, false
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
	PagesRead  int  // requests that returned items, page 1 included
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
// The page size adapts. An HTTP 5xx restarts the fetch from page 1 at
// the next smaller size (500 -> 200 -> 100 -> 50), discarding the failed
// attempt's items, and a smaller size that works is remembered for
// opts.Path for the rest of the client's life. A 5xx at 50 is returned.
// PageSize in the result is the size that succeeded, so a caller deriving
// a ceiling from PageSize x PageLimit sees it shrink with the fallback.
func (r *RawClient) GetPaginatedResult(ctx context.Context, opts PaginatedOpts) (PageResult, error) {
	opts.applyDefaults()

	requestedSize := opts.MaxPageSize
	pageSize := r.pageSizeFor(opts.Path, requestedSize)

	for {
		attemptOpts := opts
		attemptOpts.MaxPageSize = pageSize

		// Each attempt starts pagination from page 1.
		// Partial results from failed attempts are discarded.
		result, err := r.getPaginatedAtSize(paginatedAttemptContext(ctx, pageSize), attemptOpts)

		if err == nil {
			if pageSize < requestedSize {
				r.rememberPageSize(opts.Path, pageSize)
			}
			return result, nil
		}

		// Only HTTP 5xx errors trigger a smaller page size.
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) ||
			httpErr.StatusCode < 500 ||
			httpErr.StatusCode >= 600 {
			return PageResult{}, err
		}

		nextSize, ok := nextSmallerPageSize(pageSize)
		if !ok {
			slog.Error(
				"paginated API request failed with HTTP 5xx at minimum fallback page size",
				"endpoint", opts.Path,
				"page_size", pageSize,
				"status", httpErr.StatusCode,
				"error", err,
			)
			return PageResult{}, err
		}

		slog.Warn(
			"paginated API request returned HTTP 5xx; retrying with smaller page size",
			"endpoint", opts.Path,
			"status", httpErr.StatusCode,
			"page_size", pageSize,
			"retry_page_size", nextSize,
		)

		pageSize = nextSize
	}
}

// slowGatewayTimeout separates a 504 that means "this page took too
// long" from a proxy blip. A gateway answers 504 for a slow upstream only
// after its own timeout, typically tens of seconds (AWS load balancers
// and nginx default to 60s), so a 504 that took at least this long is a
// page-size signal. A faster one keeps the SDK's normal retries, which
// absorb a brief burst instead of letting it lower the endpoint's page
// size for the rest of the run. A variable so tests can shorten it.
var slowGatewayTimeout = 5 * time.Second

// paginatedAttemptContext tells the SDK not to retry a slow 504 at the
// same page size while a smaller one remains. Here a slow 504 is the
// fallback's signal: an identical retry costs another full load-balancer
// delay, and because http.Client.Timeout bounds every SDK attempt
// together, it can turn a recoverable 504 into a fatal client timeout.
// At the minimum size there is nothing smaller to try, so the SDK's
// normal retries stay in force.
func paginatedAttemptContext(ctx context.Context, pageSize int) context.Context {
	if _, ok := nextSmallerPageSize(pageSize); ok {
		return sqapi.WithoutRetryOnSlowStatus(ctx, slowGatewayTimeout, http.StatusGatewayTimeout)
	}
	return ctx
}

// getPaginatedAtSize performs one complete pagination attempt
// using a fixed page size.
func (r *RawClient) getPaginatedAtSize(ctx context.Context, opts PaginatedOpts) (PageResult, error) {
	opts.applyDefaults()

	params := CloneParams(opts.Params)
	params.Set(opts.PageParam, "1")
	params.Set(opts.SizeParam, strconv.Itoa(opts.MaxPageSize))

	body, err := r.doGet(ctx, opts.Path, params)
	if err != nil {
		return PageResult{}, err
	}
	items, err := ExtractArray(body, opts.ResultKey)
	if err != nil {
		return PageResult{}, err
	}
	total, totalKnown := ExtractTotalOK(body, opts.TotalKey)
	pages := TotalPages(total, opts.MaxPageSize)
	clamped := opts.PageLimit > 0 && pages > opts.PageLimit
	if clamped {
		pages = opts.PageLimit
	}

	all := make([]json.RawMessage, 0, total)
	all = append(all, items...)

	res := PageResult{
		Items:      all,
		Total:      total,
		TotalKnown: totalKnown,
		Fetched:    len(all),
		PagesRead:  1,
		PageSize:   opts.MaxPageSize,
		PageLimit:  opts.PageLimit,
	}
	switch {
	case !totalKnown && len(items) == opts.MaxPageSize:
		res.Reason = ReasonUnknownTotal
	case clamped:
		res.Reason = ReasonPageLimitClamp
	}
	if res.Reason != "" && opts.StopOnTruncation {
		return r.finishPaginated(res, opts), nil
	}

	for page := 2; page <= pages; page++ {
		if err := ctx.Err(); err != nil {
			return PageResult{}, err
		}
		params := CloneParams(opts.Params)
		params.Set(opts.PageParam, strconv.Itoa(page))
		params.Set(opts.SizeParam, strconv.Itoa(opts.MaxPageSize))
		body, err := r.doGet(ctx, opts.Path, params)
		if err != nil {
			return PageResult{}, err
		}
		pageItems, err := ExtractArray(body, opts.ResultKey)
		if err != nil {
			return PageResult{}, err
		}
		res.Items = append(res.Items, pageItems...)
		res.PagesRead++
	}
	res.Fetched = len(res.Items)
	return r.finishPaginated(res, opts), nil
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

func (r *RawClient) doGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
	u := r.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if len(params) > 0 {
		req.URL.RawQuery = params.Encode()
	}

	var lastReadErr error
	for attempt := 0; ; attempt++ {
		resp, err := r.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr == nil {
			if resp.StatusCode >= 400 {
				return nil, &HTTPError{
					StatusCode: resp.StatusCode,
					Method:     http.MethodGet,
					URL:        req.URL.String(),
					Body:       Truncate(body, 500),
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
