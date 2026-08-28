// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RequestLogEntry is one completed HTTP request, after any retries.
//
// The migration tool persists these as requests.log, which the report
// pipeline parses to build the Failed bucket, the Failure Ledger and
// final_analysis_report.csv. Those consumers existed for some time with
// no producer, so every failure they were meant to surface was invisible
// in the customer-facing report.
type RequestLogEntry struct {
	Method   string
	URL      string
	Status   int
	Data     map[string]string
	Response string
	Err      string
}

// RequestLogFunc receives one entry per completed request. It is called
// from the transport, so implementations must be safe for concurrent use
// and must not block for long.
type RequestLogFunc func(entry RequestLogEntry)

// maxLoggedResponseBytes caps how much of an error body is captured. Error
// bodies are small; a runaway HTML error page must not bloat the log.
const maxLoggedResponseBytes = 4096

// requestLogTransport records every completed request. It sits outside
// retryTransport so one entry is emitted per logical call rather than one
// per attempt.
type requestLogTransport struct {
	inner http.RoundTripper
	fn    RequestLogFunc
}

func (t *requestLogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	entry := RequestLogEntry{
		Method: req.Method,
		URL:    req.URL.Path,
		Data:   formFields(req),
	}

	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		entry.Err = err.Error()
		t.fn(entry)
		return resp, err
	}

	entry.Status = resp.StatusCode
	// Only capture the body for failures: success bodies are large
	// (issue searches, source listings) and carry nothing the report uses.
	if resp.StatusCode >= 400 {
		consumed, readErr := io.ReadAll(io.LimitReader(resp.Body, maxLoggedResponseBytes))
		if readErr == nil {
			entry.Response = string(consumed)
		}
		restoreConsumedBody(resp, consumed)
	}
	t.fn(entry)
	return resp, nil
}

// formFields reads a url-encoded request body without consuming the copy
// the transport will transmit, relying on GetBody the same way
// rewindRequestBody does for retries. Returns nil for bodyless requests
// and for content types that are not form-encoded.
func formFields(req *http.Request) map[string]string {
	if req.Body == nil || req.Body == http.NoBody || req.GetBody == nil {
		return nil
	}
	if ct := req.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		return nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, maxLoggedResponseBytes))
	if err != nil {
		return nil
	}
	values, err := url.ParseQuery(string(bytes.TrimSpace(raw)))
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		if len(v) == 0 {
			continue
		}
		out[k] = redactFormValue(k, v[0], values)
	}
	return out
}

// sensitiveFormKeys are form fields whose value must never be written to
// disk.
var sensitiveFormKeys = map[string]bool{
	"password":            true,
	"token":               true,
	"secret":              true,
	"privateKey":          true,
	"clientSecret":        true,
	"personalAccessToken": true,
}

// redactFormValue blanks values that must not be persisted.
//
// requests.log is written to the run directory in plaintext and is
// routinely attached to support tickets, so it must not become a
// credential leak. Two cases matter: form fields that are inherently
// secret, and /api/settings/set writes whose *key* names a secured
// SonarQube setting (those always end in ".secured", and SonarQube itself
// stops returning their values for the same reason).
func redactFormValue(field, value string, all url.Values) string {
	if value == "" {
		return value
	}
	if sensitiveFormKeys[field] {
		return "<redacted>"
	}
	if field == "value" || field == "values" || field == "fieldValues" {
		key := all.Get("key")
		if strings.HasSuffix(key, ".secured") || sensitiveFormKeys[key] {
			return "<redacted>"
		}
	}
	return value
}
