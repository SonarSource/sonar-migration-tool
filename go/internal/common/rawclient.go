package common

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

// GetPaginated fetches all pages and returns items as []json.RawMessage.
func (r *RawClient) GetPaginated(ctx context.Context, opts PaginatedOpts) ([]json.RawMessage, error) {
	opts.applyDefaults()

	params := CloneParams(opts.Params)
	params.Set(opts.PageParam, "1")
	params.Set(opts.SizeParam, strconv.Itoa(opts.MaxPageSize))

	body, err := r.doGet(ctx, opts.Path, params)
	if err != nil {
		return nil, err
	}
	items, err := ExtractArray(body, opts.ResultKey)
	if err != nil {
		return nil, err
	}
	total := ExtractTotal(body, opts.TotalKey)
	pages := TotalPages(total, opts.MaxPageSize)
	if opts.PageLimit > 0 && pages > opts.PageLimit {
		pages = opts.PageLimit
	}

	all := make([]json.RawMessage, 0, total)
	all = append(all, items...)

	for page := 2; page <= pages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		params := CloneParams(opts.Params)
		params.Set(opts.PageParam, strconv.Itoa(page))
		params.Set(opts.SizeParam, strconv.Itoa(opts.MaxPageSize))
		body, err := r.doGet(ctx, opts.Path, params)
		if err != nil {
			return nil, err
		}
		pageItems, err := ExtractArray(body, opts.ResultKey)
		if err != nil {
			return nil, err
		}
		all = append(all, pageItems...)
	}
	return all, nil
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

// ExtractTotal extracts the total count from a JSON body using a dot-path key.
func ExtractTotal(body []byte, dotPath string) int {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return 0
	}
	parts := SplitDotPath(dotPath)
	current := obj
	for i, part := range parts {
		raw, ok := current[part]
		if !ok {
			return 0
		}
		if i == len(parts)-1 {
			var n int
			if err := json.Unmarshal(raw, &n); err != nil {
				return 0
			}
			return n
		}
		if err := json.Unmarshal(raw, &current); err != nil {
			return 0
		}
	}
	return 0
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
