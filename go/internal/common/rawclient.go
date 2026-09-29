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
	"time"
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

	// onTruncation receives one record per truncated paginated fetch.
	// It lives on the client rather than in PaginatedOpts so a single
	// installation covers every call site, including the ones nobody
	// remembers to wire up — which is exactly where the silent
	// truncation classes hide (#574).
	onTruncation func(TruncationRecord)
}

// NewRawClient wraps an sqapi.Client's HTTP infrastructure.
func NewRawClient(httpClient *http.Client, baseURL string) *RawClient {
	return &RawClient{httpClient: httpClient, baseURL: baseURL}
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
	// The slicer's entry fetch sets it: there, a clamp is the trigger
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
// plus everything the caller needs in order to know what was NOT
// fetched.
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
func (r *RawClient) GetPaginatedResult(ctx context.Context, opts PaginatedOpts) (PageResult, error) {
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

func (r *RawClient) doGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
	u := r.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if len(params) > 0 {
		req.URL.RawQuery = params.Encode()
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
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
// body, a 0 from a missing key and a genuine 0 are three different
// situations, and treating the first two as "this window is empty" is
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
