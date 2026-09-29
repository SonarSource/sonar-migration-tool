// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// httptest.NewTLSServer presents a certificate signed by its own throwaway
// CA, which is not in the system pool — the same situation as the
// self-signed certificate on a locally-hosted SonarQube Server (#586).

func TestWithInsecureSkipVerifyAllowsUntrustedCert(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := sqapi.NewServerClient(srv.URL, "token", 10.7, sqapi.WithInsecureSkipVerify())
	resp, err := c.HTTPClient().Get(c.BaseURL())
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestDefaultRejectsUntrustedCert is the guard that matters most: it proves
// omitting the option leaves verification on, so WithInsecureSkipVerify
// cannot silently become the default.
func TestDefaultRejectsUntrustedCert(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := sqapi.NewServerClient(srv.URL, "token", 10.7)
	resp, err := c.HTTPClient().Get(c.BaseURL())
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate")
}

// TestInsecureSkipVerifyComposesWithClientCert covers both option orders:
// the two write to different clientConfig fields precisely so neither can
// clobber the other's TLS settings.
func TestInsecureSkipVerifyComposesWithClientCert(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	certPath, keyPath := generateSelfSignedCert(t)
	orders := map[string][]sqapi.Option{
		"cert first": {
			sqapi.WithClientCert(certPath, keyPath, ""),
			sqapi.WithInsecureSkipVerify(),
		},
		"insecure first": {
			sqapi.WithInsecureSkipVerify(),
			sqapi.WithClientCert(certPath, keyPath, ""),
		},
	}
	for name, opts := range orders {
		t.Run(name, func(t *testing.T) {
			c := sqapi.NewServerClient(srv.URL, "token", 10.7, opts...)
			require.NoError(t, c.CertErr())
			resp, err := c.HTTPClient().Get(c.BaseURL())
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}
}
