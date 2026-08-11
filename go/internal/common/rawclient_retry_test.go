// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// sonarUnexpectedError is verbatim what SonarQube Cloud returns from
// GET /api/alm_integration/show_bound_organization when the organization
// has no ALM binding. It is a 500, so the retry transport retries it
// through the whole schedule and then gives up.
const sonarUnexpectedError = `{"errors":[{"msg":"An unexpected error occurred. Please try again later."}]}`

// TestRawClientSurfacesHTTPErrorAfterExhaustedRetries is the end-to-end
// guard for issue #505. It drives the real production transport stack
// (auth → user-agent → retry → http.Transport, built by
// sqapi.NewCloudClient) and asserts that a 500 which survives every
// retry reaches the caller as a *HTTPError carrying the true status and
// body — not as the opaque "http: read on closed response body" that the
// drained-and-closed give-up path used to produce.
func TestRawClientSurfacesHTTPErrorAfterExhaustedRetries(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(sonarUnexpectedError))
	}))
	defer ts.Close()

	client := sqapi.NewCloudClient(ts.URL, "test-token")
	raw := NewRawClient(client.HTTPClient(), ts.URL+"/")

	_, err := raw.Get(context.Background(), "api/alm_integration/show_bound_organization",
		url.Values{"organization": {"latest-unbound"}})

	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if !IsHTTPError(err, http.StatusInternalServerError) {
		t.Fatalf("expected a *HTTPError with status 500, got %T: %v", err, err)
	}

	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("expected *HTTPError, got %T", err)
	}
	if he.Body != sonarUnexpectedError {
		t.Errorf("HTTPError.Body = %q, want %q", he.Body, sonarUnexpectedError)
	}
	const wantMsg = "An unexpected error occurred. Please try again later."
	if got := he.Message(); got != wantMsg {
		t.Errorf("HTTPError.Message() = %q, want %q", got, wantMsg)
	}
	if attempts.Load() < 2 {
		t.Errorf("expected the 500 to be retried, saw %d attempt(s)", attempts.Load())
	}
}

// TestRawClientSucceedsAfterTransient500 verifies the retry path still
// works through the same production stack: a 500 followed by a 200
// returns the 200 payload intact.
func TestRawClientSucceedsAfterTransient500(t *testing.T) {
	const payload = `{"organization":"acme","alm":"github","key":"acme-gh"}`

	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(sonarUnexpectedError))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer ts.Close()

	client := sqapi.NewCloudClient(ts.URL, "test-token")
	raw := NewRawClient(client.HTTPClient(), ts.URL+"/")

	body, err := raw.Get(context.Background(), "api/alm_integration/show_bound_organization",
		url.Values{"organization": {"latest-bound"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != payload {
		t.Errorf("body = %q, want %q", string(body), payload)
	}
	if attempts.Load() != 2 {
		t.Errorf("expected exactly one retry, saw %d attempt(s)", attempts.Load())
	}
}
