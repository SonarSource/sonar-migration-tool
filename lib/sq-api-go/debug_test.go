// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// TestDebugLoggerSeesAuthAndUserAgent pins that the headers passed to a
// DebugLogFunc are the ones actually sent on the wire — authTransport and
// userAgentTransport inject their headers on a cloned request, so
// debugTransport must sit inside them in the transport stack or it only
// ever sees the caller's original, unmodified headers.
func TestDebugLoggerSeesAuthAndUserAgent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	var gotHeaders map[string][]string
	var calls int
	c := sqapi.NewServerClient(ts.URL, "my-token", 10.7, sqapi.WithDebugLogger(
		func(method, url string, headers map[string][]string, reqBody []byte, respStatus int, respBody []byte, duration time.Duration, err error) {
			calls++
			gotHeaders = headers
		},
	))

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/ping", nil)
	require.NoError(t, err)
	resp, err := c.HTTPClient().Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, 1, calls)
	assert.Equal(t, []string{"<redacted>"}, gotHeaders["Authorization"])
	assert.Equal(t, []string{sqapi.UserAgent}, gotHeaders["User-Agent"])
}

// TestDebugTransportSurfacesBodyReadError pins that a body read that fails
// mid-stream (e.g. an HTTP/2 GOAWAY or a dropped connection) is still an
// error for the caller, not an empty body: callers such as RawClient.doGet
// retry on a body-read error (#616), and --debug must not hide it.
func TestDebugTransportSurfacesBodyReadError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, bw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = bw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\npartial")
		_ = bw.Flush()
		_ = conn.Close()
	}))
	defer ts.Close()

	var logged error
	c := sqapi.NewServerClient(ts.URL, "tok", 10.7, sqapi.WithDebugLogger(
		func(method, url string, headers map[string][]string, reqBody []byte, respStatus int, respBody []byte, duration time.Duration, err error) {
			logged = err
		},
	))

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/x", nil)
	require.NoError(t, err)
	resp, err := c.HTTPClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	assert.Equal(t, "partial", string(body))
	assert.ErrorIs(t, readErr, io.ErrUnexpectedEOF)
	assert.ErrorIs(t, logged, io.ErrUnexpectedEOF, "the failed read must still be logged")
}
