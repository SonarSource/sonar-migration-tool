package sqapi

import (
	"bytes"
	"io"
	"net/http"
)

// debugTransport wraps another RoundTripper and invokes fn for every
// request/response pair with the full payloads. The Authorization header is
// redacted before fn sees it. Buffer sizes are unbounded — only use when
// diagnosing a migration run.
type debugTransport struct {
	inner http.RoundTripper
	fn    DebugLogFunc
}

func (t *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Capture the request body, leaving a fresh reader for the inner transport.
	var reqBody []byte
	if req.Body != nil {
		buf, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			t.fn(req.Method, req.URL.String(), redactHeaders(req.Header), nil, 0, nil, err)
			return nil, err
		}
		reqBody = buf
		req.Body = io.NopCloser(bytes.NewReader(buf))
	}

	resp, err := t.inner.RoundTrip(req)

	if err != nil {
		t.fn(req.Method, req.URL.String(), redactHeaders(req.Header), reqBody, 0, nil, err)
		return resp, err
	}

	// Drain the response body so we can log it; restore for the caller.
	var respBody []byte
	if resp.Body != nil {
		buf, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.fn(req.Method, req.URL.String(), redactHeaders(req.Header), reqBody, resp.StatusCode, nil, readErr)
			resp.Body = io.NopCloser(bytes.NewReader(nil))
			return resp, nil
		}
		respBody = buf
		resp.Body = io.NopCloser(bytes.NewReader(buf))
	}

	t.fn(req.Method, req.URL.String(), redactHeaders(req.Header), reqBody, resp.StatusCode, respBody, nil)
	return resp, nil
}

// redactHeaders returns a copy of h with the Authorization header replaced
// by "<redacted>" so credentials never leak into a log.
func redactHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		if http.CanonicalHeaderKey(k) == "Authorization" {
			out[k] = []string{"<redacted>"}
			continue
		}
		out[k] = append([]string(nil), v...)
	}
	return out
}
