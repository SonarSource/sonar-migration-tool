// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// One entry per completed call, capturing the form the tool sent and — for
// failures only — the error body the report needs. Success bodies are large
// and carry nothing useful, so they must not be captured.
func TestRequestLogTransportRecordsCalls(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("key") == "bad.key" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"msg":"Setting 'bad.key' cannot be set on a Project"}]}`))
			return
		}
		// A 200 WITH a body, so "success bodies are not captured" is
		// actually exercised — a 204 would make the assertion vacuous.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ignored":"payload that must not reach requests.log"}`))
	})
	mux.HandleFunc("GET /api/big", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var entries []RequestLogEntry
	rt := &requestLogTransport{
		inner: srv.Client().Transport,
		fn:    func(e RequestLogEntry) { entries = append(entries, e) },
	}
	client := &http.Client{Transport: rt}

	post := func(key string) {
		form := url.Values{"key": {key}, "value": {"v"}, "component": {"org_proj"}}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/settings/set",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	post("good.key")
	post("bad.key")

	resp, err := client.Get(srv.URL + "/api/big")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if len(entries) != 3 {
		t.Fatalf("expected one entry per call, got %d", len(entries))
	}

	ok, bad, big := entries[0], entries[1], entries[2]

	if ok.Status != http.StatusOK || ok.URL != "/api/settings/set" || ok.Method != http.MethodPost {
		t.Errorf("success entry wrong: %+v", ok)
	}
	if ok.Data["key"] != "good.key" || ok.Data["component"] != "org_proj" {
		t.Errorf("form fields not captured: %+v", ok.Data)
	}
	if ok.Response != "" {
		t.Errorf("success bodies must not be captured, got %q", ok.Response)
	}
	if strings.Contains(ok.Response, "must not reach") {
		t.Error("the success payload leaked into the log entry")
	}

	if bad.Status != http.StatusBadRequest {
		t.Errorf("failure status = %d", bad.Status)
	}
	if !strings.Contains(bad.Response, "cannot be set on a Project") {
		t.Errorf("failure body must be captured, got %q", bad.Response)
	}

	// The response body must still be readable by the caller after the
	// transport peeked at it.
	if big.Data != nil {
		t.Errorf("a GET with no form body must record no data, got %+v", big.Data)
	}
	if len(body) != 100 {
		t.Errorf("caller lost response bytes: got %d, want 100", len(body))
	}
}

// A transport-level failure has no status but must still be recorded, or the
// report loses every connection-level error.
func TestRequestLogTransportRecordsTransportErrors(t *testing.T) {
	var entries []RequestLogEntry
	rt := &requestLogTransport{
		inner: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, io.ErrUnexpectedEOF
		}),
		fn: func(e RequestLogEntry) { entries = append(entries, e) },
	}
	client := &http.Client{Transport: rt}
	_, err := client.Get("http://example.invalid/api/x")
	if err == nil {
		t.Fatal("expected the transport error to surface to the caller")
	}
	if len(entries) != 1 {
		t.Fatalf("expected the failure to be recorded, got %d entries", len(entries))
	}
	if entries[0].Err == "" {
		t.Error("transport errors must record the error text")
	}
	if entries[0].Status != 0 {
		t.Errorf("a transport failure has no status, got %d", entries[0].Status)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
