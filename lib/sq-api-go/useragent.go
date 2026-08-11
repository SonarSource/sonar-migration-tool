// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi

import "net/http"

// UserAgent is sent with every request made through Client, so SMT traffic
// is distinguishable from other Go HTTP clients in SonarQube/SonarCloud logs.
const UserAgent = "sonar-migration-tool"

// userAgentTransport is an http.RoundTripper that sets a fixed User-Agent
// header on every request, overriding Go's default (Go-http-client/1.1).
type userAgentTransport struct {
	inner http.RoundTripper
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request so we never mutate the caller's copy.
	r2 := req.Clone(req.Context())
	r2.Header.Set("User-Agent", UserAgent)
	return t.inner.RoundTrip(r2)
}
