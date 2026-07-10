// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

const testBaseURL = "https://sonar.example.com"

// ---- HTTPClient ----

func TestHTTPClientNotNil(t *testing.T) {
	c := sqapi.NewServerClient(testBaseURL, "token", 10.7)
	assert.NotNil(t, c.HTTPClient())
}

func TestHTTPClientMakesRequest(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := sqapi.NewServerClient(ts.URL, "my-token", 10.7)
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/ping", nil)
	require.NoError(t, err)
	resp, err := c.HTTPClient().Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, "Bearer my-token", gotAuth)
}

func TestHTTPClientSetsUserAgent(t *testing.T) {
	var gotUA string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := sqapi.NewServerClient(ts.URL, "my-token", 10.7)
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/ping", nil)
	require.NoError(t, err)
	resp, err := c.HTTPClient().Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, sqapi.UserAgent, gotUA)
	assert.Equal(t, "sonar-migration-tool", gotUA)
}

// ---- APIError ----

func TestAPIErrorErrorString(t *testing.T) {
	err := &sqapi.APIError{
		StatusCode: 403,
		Method:     http.MethodGet,
		URL:        "https://sonar.example.com/api/projects/search",
		Body:       `{"errors":[{"msg":"Insufficient privileges"}]}`,
	}
	msg := err.Error()
	assert.Contains(t, msg, "403")
	assert.Contains(t, msg, "GET")
	assert.Contains(t, msg, "api/projects/search")
}

func TestAPIErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"single error", `{"errors":[{"msg":"Insufficient privileges"}]}`, "Insufficient privileges"},
		{"multiple errors", `{"errors":[{"msg":"Error 1"},{"msg":"Error 2"}]}`, "Error 1; Error 2"},
		{"empty body", "", ""},
		{"non-JSON body", "Internal Server Error", "Internal Server Error"},
		{"no errors key", `{"status":"error"}`, `{"status":"error"}`},
		{"empty errors array", `{"errors":[]}`, `{"errors":[]}`},
		{"empty msg fields", `{"errors":[{"msg":""}]}`, `{"errors":[{"msg":""}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &sqapi.APIError{StatusCode: 400, Body: tt.body}
			assert.Equal(t, tt.want, err.Message())
		})
	}
}

func TestAPIErrorEndpoint(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"full URL", "https://sonarcloud.io/api/permissions/add_group", "/api/permissions/add_group"},
		{"with query", "https://sonarcloud.io/api/projects/search?org=foo", "/api/projects/search"},
		{"path only", "/api/rules/update", "/api/rules/update"},
		{"invalid URL", "://bad", "://bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &sqapi.APIError{StatusCode: 400, URL: tt.url}
			assert.Equal(t, tt.want, err.Endpoint())
		})
	}
}

// ---- Error predicate functions ----

func TestIsNotFoundTrue(t *testing.T) {
	err := &sqapi.APIError{StatusCode: http.StatusNotFound}
	assert.True(t, sqapi.IsNotFound(err))
}

func TestIsNotFoundFalse(t *testing.T) {
	assert.False(t, sqapi.IsNotFound(errors.New("other error")))
	assert.False(t, sqapi.IsNotFound(&sqapi.APIError{StatusCode: 403}))
}

func TestIsUnauthorizedTrue(t *testing.T) {
	err := &sqapi.APIError{StatusCode: http.StatusUnauthorized}
	assert.True(t, sqapi.IsUnauthorized(err))
}

func TestIsUnauthorizedFalse(t *testing.T) {
	assert.False(t, sqapi.IsUnauthorized(errors.New("other")))
	assert.False(t, sqapi.IsUnauthorized(&sqapi.APIError{StatusCode: 403}))
}

func TestIsForbiddenTrue(t *testing.T) {
	err := &sqapi.APIError{StatusCode: http.StatusForbidden}
	assert.True(t, sqapi.IsForbidden(err))
}

func TestIsForbiddenFalse(t *testing.T) {
	assert.False(t, sqapi.IsForbidden(errors.New("other")))
	assert.False(t, sqapi.IsForbidden(&sqapi.APIError{StatusCode: 404}))
}

func TestIsAlreadyExistsTrue(t *testing.T) {
	err := &sqapi.APIError{StatusCode: 400, Body: `{"errors":[{"msg":"Group with name 'X' already exists"}]}`}
	assert.True(t, sqapi.IsAlreadyExists(err))
}

func TestIsAlreadyExistsCaseInsensitive(t *testing.T) {
	err := &sqapi.APIError{StatusCode: 400, Body: `{"errors":[{"msg":"Quality profile ALREADY EXISTS: java/MyProfile"}]}`}
	assert.True(t, sqapi.IsAlreadyExists(err))
}

func TestIsAlreadyExistsFalse(t *testing.T) {
	assert.False(t, sqapi.IsAlreadyExists(errors.New("other")))
	assert.False(t, sqapi.IsAlreadyExists(&sqapi.APIError{StatusCode: 400, Body: "invalid parameter"}))
	assert.False(t, sqapi.IsAlreadyExists(&sqapi.APIError{StatusCode: 404, Body: "already exists"}))
}

// IsOrgLevelRejection pins the exact SQC 400 the migration tool uses to
// detect "list_definitions says this is settable at org-scope, but
// /api/settings/set rejects the write" — the runtime fallback for
// keys like sonar.coverage.jacoco.xmlReportPaths.
func TestIsOrgLevelRejection(t *testing.T) {
	// SonarCloud serializes the apostrophe in "can't" as the JSON
	// unicode escape ' in the raw response body. The detector
	// must match against the DECODED message, not the raw body, or
	// the substring search will silently fail to find the literal
	// apostrophe.
	err := &sqapi.APIError{
		StatusCode: 400,
		Body:       `{"errors":[{"msg":"Provided property can't be set at organization level: sonar.coverage.jacoco.xmlReportPaths"}]}`,
	}
	assert.True(t, sqapi.IsOrgLevelRejection(err))

	// Literal apostrophe variant — also matches.
	err2 := &sqapi.APIError{
		StatusCode: 400,
		Body:       `{"errors":[{"msg":"Provided property can't be set at organization level: foo"}]}`,
	}
	assert.True(t, sqapi.IsOrgLevelRejection(err2))

	// Different wording, still recognisable.
	err3 := &sqapi.APIError{
		StatusCode: 400,
		Body:       `{"errors":[{"msg":"This setting cannot be set at organization level."}]}`,
	}
	assert.True(t, sqapi.IsOrgLevelRejection(err3))

	// Different 400 (e.g. permission) — must NOT match.
	assert.False(t, sqapi.IsOrgLevelRejection(&sqapi.APIError{StatusCode: 400, Body: "Forbidden"}))
	// Non-API error must NOT match.
	assert.False(t, sqapi.IsOrgLevelRejection(errors.New("network")))
	// 500-level errors must NOT match.
	assert.False(t, sqapi.IsOrgLevelRejection(&sqapi.APIError{
		StatusCode: 500,
		Body:       `{"errors":[{"msg":"can't be set at organization level"}]}`,
	}))
}

// ---- Options ----

func TestWithMaxConnections(t *testing.T) {
	c := sqapi.NewServerClient(testBaseURL, "token", 10.7, sqapi.WithMaxConnections(10))
	assert.NotNil(t, c)
}

func TestWithTimeout(t *testing.T) {
	c := sqapi.NewServerClient(testBaseURL, "token", 10.7, sqapi.WithTimeout(30))
	assert.NotNil(t, c)
}

func TestWithClientCertMissingCertFile(t *testing.T) {
	c := sqapi.NewServerClient(testBaseURL, "token", 10.7,
		sqapi.WithClientCert("/nonexistent/cert.pem", "/nonexistent/key.pem", ""),
	)
	require.Error(t, c.CertErr())
	assert.Contains(t, c.CertErr().Error(), "cert.pem")
}

func TestWithClientCertMissingKeyFile(t *testing.T) {
	certFile := t.TempDir() + "/cert.pem"
	require.NoError(t, os.WriteFile(certFile, []byte("invalid pem content"), 0600))

	c := sqapi.NewServerClient(testBaseURL, "token", 10.7,
		sqapi.WithClientCert(certFile, "/nonexistent/key.pem", ""),
	)
	require.Error(t, c.CertErr())
}

func TestWithClientCertInvalidKeyPair(t *testing.T) {
	dir := t.TempDir()
	certFile := dir + "/cert.pem"
	keyFile := dir + "/key.pem"
	require.NoError(t, os.WriteFile(certFile, []byte("not valid pem"), 0600))
	require.NoError(t, os.WriteFile(keyFile, []byte("not valid pem"), 0600))

	c := sqapi.NewServerClient(testBaseURL, "token", 10.7,
		sqapi.WithClientCert(certFile, keyFile, ""),
	)
	require.Error(t, c.CertErr())
	assert.Contains(t, c.CertErr().Error(), "loading client certificate")
}

// ---- authTransport ----

func TestAuthTransportInjectsHeader(t *testing.T) {
	var gotHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	transport := sqapi.NewAuthTransport(http.DefaultTransport, "Bearer test-token")
	client := &http.Client{Transport: transport}
	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, "Bearer test-token", gotHeader)
}

// ---- retryTransport ----

func TestRetryTransportSuccessFirstAttempt(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	transport := sqapi.NewRetryTransport(http.DefaultTransport, []time.Duration{0, 0})
	client := &http.Client{Transport: transport}
	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRetryTransportRetriesOn503(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()

	transport := sqapi.NewRetryTransport(http.DefaultTransport, []time.Duration{0})
	client := &http.Client{Transport: transport}
	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, attempts)
}

func TestRetryTransportRetriesOn429(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()

	transport := sqapi.NewRetryTransport(http.DefaultTransport, []time.Duration{0, 0})
	client := &http.Client{Transport: transport}
	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRetryTransportExhaustsRetries(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	transport := sqapi.NewRetryTransport(http.DefaultTransport, []time.Duration{0, 0})
	client := &http.Client{Transport: transport}
	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestRetryTransportDoesNotRetryOn4xx(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	transport := sqapi.NewRetryTransport(http.DefaultTransport, []time.Duration{0, 0})
	client := &http.Client{Transport: transport}
	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 1, attempts, "4xx should not trigger a retry")
}

func TestRetryTransportLogsRetries(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()

	var logs []string
	logFn := func(method, url string, status, attempt, total int) {
		logs = append(logs, fmt.Sprintf("%s %s %d %d/%d", method, url, status, attempt, total))
	}
	transport := sqapi.NewRetryTransportWithLogger(http.DefaultTransport, []time.Duration{0, 0}, logFn)
	client := &http.Client{Transport: transport}
	resp, err := client.Get(ts.URL + "/api/test")
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 3, attempts)
	assert.Len(t, logs, 2, "should log 2 retries")
	assert.Contains(t, logs[0], "429")
	assert.Contains(t, logs[1], "429")
}

func TestWithRetryLoggerOption(t *testing.T) {
	called := false
	fn := func(method, url string, status, attempt, total int) { called = true }
	cfg := sqapi.ApplyWithRetryLogger(fn)
	_ = cfg // option was applied (coverage for WithRetryLogger)
	assert.False(t, called, "should not be called until retry happens")
}

// ---- NewPaginator default page size ----

func TestNewPaginatorDefaultPageSize(t *testing.T) {
	var receivedPageSize int
	fetch := func(_ context.Context, _, pageSize int) ([]string, int, error) {
		receivedPageSize = pageSize
		return []string{"a"}, 1, nil
	}
	pag := sqapi.NewPaginator(fetch, 0) // 0 → should default to PageSize (500)
	_, err := pag.All(context.Background())
	require.NoError(t, err)
	assert.Equal(t, sqapi.PageSize, receivedPageSize)
}
