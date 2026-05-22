package sqapi

import (
	"crypto/tls"
	"fmt"
	"os"
)

// Option is a functional option for configuring a Client.
type Option func(*clientConfig)

// clientConfig holds optional Client configuration assembled from Option values.
type clientConfig struct {
	tlsConfig   *tls.Config
	certErr     error // deferred cert loading error, reported on first request
	maxConns    int
	timeoutSecs int
	retryLogFn  RetryLogFunc
	debugLogFn  DebugLogFunc
}

// DebugLogFunc is invoked once per request/response pair with the verbatim
// HTTP method, URL, sanitized header set, request body, response status, and
// response body. The Authorization header is replaced with "<redacted>"
// before the callback fires.
type DebugLogFunc func(method, url string, headers map[string][]string, reqBody []byte, respStatus int, respBody []byte, err error)

func defaultClientConfig() *clientConfig {
	return &clientConfig{
		maxConns:    50,
		timeoutSecs: 60,
	}
}

// WithClientCert configures mutual TLS using the given PEM certificate file
// and unencrypted private key file.
//
// pemFile and keyFile are paths to PEM-encoded files. password is accepted for
// API compatibility but encrypted keys are not supported in Phase 1 — pass an
// empty string. Encrypted key support will be added in a future phase.
//
// If cert loading fails, the error is deferred and returned on the first
// request made through the client (Options cannot return errors).
func WithClientCert(pemFile, keyFile, _ string) Option {
	return func(cfg *clientConfig) {
		certPEM, err := os.ReadFile(pemFile)
		if err != nil {
			cfg.certErr = fmt.Errorf("reading cert file %q: %w", pemFile, err)
			return
		}
		keyPEM, err := os.ReadFile(keyFile)
		if err != nil {
			cfg.certErr = fmt.Errorf("reading key file %q: %w", keyFile, err)
			return
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			cfg.certErr = fmt.Errorf("loading client certificate: %w", err)
			return
		}
		if cfg.tlsConfig == nil {
			cfg.tlsConfig = &tls.Config{} //nolint:gosec
		}
		cfg.tlsConfig.Certificates = append(cfg.tlsConfig.Certificates, cert)
	}
}

// WithMaxConnections sets the maximum number of idle connections in the pool.
// Defaults to 50.
func WithMaxConnections(n int) Option {
	return func(cfg *clientConfig) {
		cfg.maxConns = n
	}
}

// WithTimeout sets the per-request timeout in seconds. Defaults to 60.
func WithTimeout(seconds int) Option {
	return func(cfg *clientConfig) {
		cfg.timeoutSecs = seconds
	}
}

// WithRetryLogger sets a callback that is invoked when a request is retried
// due to a retryable status code (429, 5xx) or network error.
func WithRetryLogger(fn RetryLogFunc) Option {
	return func(cfg *clientConfig) {
		cfg.retryLogFn = fn
	}
}

// WithDebugLogger installs an HTTP request/response debug logger. When set,
// the client wraps its transport with a RoundTripper that calls fn once per
// request/response pair, exposing the full payloads. Useful for diagnosing
// migration issues against SonarQube Cloud without recompiling the SDK.
func WithDebugLogger(fn DebugLogFunc) Option {
	return func(cfg *clientConfig) {
		cfg.debugLogFn = fn
	}
}
