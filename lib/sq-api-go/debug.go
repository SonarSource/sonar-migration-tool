// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi

import (
	"bytes"
	"io"
	"net/http"
	"time"
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
			t.fn(req.Method, req.URL.String(), redactHeaders(req.Header), nil, 0, nil, 0, err)
			return nil, err
		}
		reqBody = buf
		req.Body = io.NopCloser(bytes.NewReader(buf))
	}

	// Clock starts here so the logged duration covers only the network
	// round trip (and, below, draining the response body), not the request
	// body buffering above.
	start := time.Now()
	resp, err := t.inner.RoundTrip(req)

	if err != nil {
		t.fn(req.Method, req.URL.String(), redactHeaders(req.Header), reqBody, 0, nil, time.Since(start), err)
		return resp, err
	}

	// Drain the response body so we can log it; restore for the caller.
	var respBody []byte
	if resp.Body != nil {
		buf, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.fn(req.Method, req.URL.String(), redactHeaders(req.Header), reqBody, resp.StatusCode, nil, time.Since(start), readErr)
			resp.Body = io.NopCloser(bytes.NewReader(nil))
			return resp, nil
		}
		respBody = buf
		resp.Body = io.NopCloser(bytes.NewReader(buf))
	}

	t.fn(req.Method, req.URL.String(), redactHeaders(req.Header), reqBody, resp.StatusCode, respBody, time.Since(start), nil)
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
