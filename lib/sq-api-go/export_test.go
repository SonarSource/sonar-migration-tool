// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

// This file is in package sqapi (not sqapi_test) so it can access unexported
// identifiers. It is compiled only during 'go test' because the filename ends
// in _test.go. These exports are not part of the public API.
package sqapi

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"
)

var (
	BuildAuthHeader = buildAuthHeader
	TotalPages      = totalPages
	Jitter          = jitter
)

// BodySnippetMax exposes the classifier's 429 body peek window so tests
// can build payloads that are provably longer than it.
const BodySnippetMax = bodySnippetMax

// BuildTransport exposes buildTransport for testing TLS config branches.
func BuildTransport(token string, version float64, tlsMinVersion uint16) http.RoundTripper {
	cfg := defaultClientConfig()
	if tlsMinVersion > 0 {
		cfg.tlsConfig = &tls.Config{MinVersion: tlsMinVersion} //nolint:gosec
	} else {
		cfg.tlsConfig = &tls.Config{} //nolint:gosec
	}
	return buildTransport(cfg, token, version)
}

// NewAuthTransport wraps inner with the authorization header injection transport.
func NewAuthTransport(inner http.RoundTripper, header string) http.RoundTripper {
	return &authTransport{inner: inner, header: header}
}

// NewRetryTransport wraps inner with the retry transport using the given backoff schedule.
func NewRetryTransport(inner http.RoundTripper, backoff []time.Duration) http.RoundTripper {
	return &retryTransport{inner: inner, backoff: backoff}
}

// NewRetryTransportWithLogger wraps inner with the retry transport and a log callback.
func NewRetryTransportWithLogger(inner http.RoundTripper, backoff []time.Duration, logFn RetryLogFunc) http.RoundTripper {
	return &retryTransport{inner: inner, backoff: backoff, logFn: logFn}
}

// ApplyWithRetryLogger exercises the WithRetryLogger option for coverage.
func ApplyWithRetryLogger(fn RetryLogFunc) *clientConfig {
	cfg := defaultClientConfig()
	WithRetryLogger(fn)(cfg)
	return cfg
}

// Classify429 exposes classify429 for tests.
func Classify429(headers http.Header, body []byte) RateLimitKind {
	return classify429(headers, body)
}

// ParseRetryAfterHeader exposes parseRetryAfter for tests.
func ParseRetryAfterHeader(headers http.Header) time.Duration {
	return parseRetryAfter(headers)
}

// RetryTransportConfig lets tests build a retry transport with every
// behavior toggle exposed — separate 5xx / SQC-429 / non-SQC-429
// schedules plus an optional observer and a freshly-allocated shared
// gate.
type RetryTransportConfig struct {
	Inner         http.RoundTripper
	Backoff       []time.Duration
	SQCBackoff    []time.Duration
	NonSQCBackoff []time.Duration
	Logger        RetryLogFunc
	Recovery      RecoveryLogFunc
	Observer      RateLimitObserver
}

// NewRetryTransportFull constructs a retryTransport using cfg.
func NewRetryTransportFull(cfg RetryTransportConfig) http.RoundTripper {
	return &retryTransport{
		inner:         cfg.Inner,
		backoff:       cfg.Backoff,
		sqcBackoff:    cfg.SQCBackoff,
		nonSQCBackoff: cfg.NonSQCBackoff,
		logFn:         cfg.Logger,
		recoveryFn:    cfg.Recovery,
		observer:      cfg.Observer,
		gate:          &rateLimitGate{},
	}
}

// NewRateLimitGate returns a freshly-allocated rate-limit gate for
// testing the gate primitive in isolation.
func NewRateLimitGate() *RateLimitGate {
	return &RateLimitGate{inner: &rateLimitGate{}}
}

// RateLimitGate is a test wrapper around the unexported gate so tests
// can drive WaitIfBlocked / Extend without depending on the retry
// transport.
type RateLimitGate struct {
	inner *rateLimitGate
}

func (g *RateLimitGate) WaitIfBlocked(ctx context.Context)    { g.inner.waitIfBlocked(ctx) }
func (g *RateLimitGate) Extend(until time.Time) time.Duration { return g.inner.extend(until) }
