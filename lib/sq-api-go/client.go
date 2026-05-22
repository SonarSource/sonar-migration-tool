// Package sqapi provides a typed Go client for the SonarQube Server and
// SonarQube Cloud APIs. It is scoped to the endpoints used by the
// sonar-migration-tool.
//
// # Quick start
//
//	// Server ≥10 (Bearer token)
//	client := sqapi.NewServerClient("https://sonar.example.com", "squ_mytoken", 10.7)
//
//	// Server <10 (Basic auth)
//	client := sqapi.NewServerClient("https://sonar.example.com", "squ_mytoken", 9.9)
//
//	// Cloud
//	client := sqapi.NewCloudClient("https://sonarcloud.io", "squ_mytoken")
//
//	// With mTLS
//	client := sqapi.NewServerClient(
//	    "https://sonar.example.com", "squ_mytoken", 9.9,
//	    sqapi.WithClientCert("/path/to/cert.pem", "/path/to/key.pem", ""),
//	)
package sqapi

import (
	"crypto/tls"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is the base HTTP client for SonarQube Server and SonarQube Cloud.
//
// Server and Cloud clients share the same underlying struct — they differ
// only in the Authorization header (determined by version) and base URL.
//
// Construct via NewServerClient or NewCloudClient; do not create directly.
type Client struct {
	httpClient *http.Client
	baseURL    string
	version    float64 // 0.0 = cloud sentinel; otherwise Server version e.g. 9.9, 10.7
	certErr    error   // deferred cert loading error from WithClientCert
}

// BaseURL returns the base URL this client was constructed with.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// Version returns the server version this client was configured for.
// Returns 0.0 for Cloud clients.
func (c *Client) Version() float64 {
	return c.version
}

// IsCloud reports whether this client targets SonarQube Cloud.
func (c *Client) IsCloud() bool {
	return c.version == cloudSentinel
}

// HTTPClient returns the underlying *http.Client for use by endpoint packages.
func (c *Client) HTTPClient() *http.Client {
	return c.httpClient
}

// CertErr returns any deferred certificate loading error from WithClientCert.
// Callers should check this before making requests if they used WithClientCert.
func (c *Client) CertErr() error {
	return c.certErr
}

// NewServerClient creates a Client for a SonarQube Server instance.
//
// baseURL should include the scheme and host, e.g. "https://sonar.example.com".
// version is the server version as a float64, e.g. 9.9 or 10.7, obtained by
// calling ParseServerVersion on the response from /api/server/version.
func NewServerClient(baseURL, token string, version float64, opts ...Option) *Client {
	return newClient(baseURL, token, version, opts...)
}

// NewCloudClient creates a Client for SonarQube Cloud.
//
// baseURL is typically "https://sonarcloud.io" or "https://api.sonarcloud.io"
// depending on which API is being called.
func NewCloudClient(baseURL, token string, opts ...Option) *Client {
	return newClient(baseURL, token, cloudSentinel, opts...)
}

func newClient(baseURL, token string, version float64, opts ...Option) *Client {
	cfg := defaultClientConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	transport := buildTransport(cfg, token, version)

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(cfg.timeoutSecs) * time.Second,
		},
		baseURL: normalizeBaseURL(baseURL),
		version: version,
		certErr: cfg.certErr,
	}
}

// buildTransport constructs the layered RoundTripper stack:
//
//	authTransport → retryTransport → http.Transport (with optional TLS)
func buildTransport(cfg *clientConfig, token string, version float64) http.RoundTripper {
	tlsCfg := cfg.tlsConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec
	} else if tlsCfg.MinVersion == 0 {
		tlsCfg.MinVersion = tls.VersionTLS12
	}

	base := &http.Transport{
		TLSClientConfig: tlsCfg,
		MaxIdleConns:    cfg.maxConns,
		IdleConnTimeout: 90 * time.Second,
	}

	retry := &retryTransport{
		inner:   base,
		backoff: defaultBackoff,
		logFn:   cfg.retryLogFn,
	}

	var rt http.RoundTripper = &authTransport{
		inner:  retry,
		header: buildAuthHeader(token, version),
	}
	if cfg.debugLogFn != nil {
		rt = &debugTransport{inner: rt, fn: cfg.debugLogFn}
	}
	return rt
}

// normalizeBaseURL ensures the base URL ends with exactly one slash.
func normalizeBaseURL(u string) string {
	return strings.TrimRight(u, "/") + "/"
}

// ParseServerVersion parses the plain-text response from /api/server/version
// (e.g. "10.7.0.123") into a float64 (e.g. 10.7).
//
// Only the first two dot-separated components are used, matching the Python
// implementation: float('.'.join(version.split('.')[:2]))
//
// Handles minor versions ≥ 10 correctly (e.g. "10.10.0.1" → 10.10).
func ParseServerVersion(versionText string) (float64, error) {
	versionText = strings.TrimSpace(versionText)
	var major, minor int
	n, err := fmt.Sscanf(versionText, "%d.%d", &major, &minor)
	if err != nil || n < 2 {
		return 0, fmt.Errorf("parsing server version %q: expected at least two numeric components", versionText)
	}
	// Use string round-trip to get exact float representation (e.g. 10.7, not 10.700001).
	digits := len(strconv.Itoa(minor))
	result := float64(major) + float64(minor)/math.Pow(10, float64(digits))
	return result, nil
}
