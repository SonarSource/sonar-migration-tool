// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	neturl "net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sonar-solutions/sonar-migration-tool/internal/redact"
	sqapi "github.com/sonar-solutions/sq-api-go"
)

// debugBodyWriter is where pretty-printed request/response bodies are
// streamed. Slog's text handler quotes string values and escapes
// newlines, so embedding a multi-line JSON body as a slog field renders
// as \n-escaped soup. Splitting bodies onto their own writer keeps the
// JSON actually readable. Indirected via a package var so tests can
// capture it.
var debugBodyWriter io.Writer = os.Stderr

// NewHTTPDebugLogger returns a DebugLogFunc that emits one slog Debug entry
// per HTTP request/response pair containing method, URL, headers, duration,
// and response status. When the request or response has a body, it is
// written to debugBodyWriter (os.Stderr by default) as a separate block:
// JSON bodies are pretty-printed, non-JSON bodies are written verbatim.
// Authorization headers are already redacted by the SDK before this
// callback runs.
//
// Shared by extract and migrate so --debug produces the same shape of
// per-request log regardless of which command emitted it.
func NewHTTPDebugLogger(logger *slog.Logger) sqapi.DebugLogFunc {
	return func(method, url string, headers map[string][]string, reqBody []byte, respStatus int, respBody []byte, duration time.Duration, err error) {
		args := []any{
			"method", method,
			"url", redactURL(url),
			"headers", redactHeaders(headers),
			"duration_ms", duration.Milliseconds(),
		}
		if err != nil {
			args = append(args, "err", err.Error())
			logger.Debug("http request failed", args...)
		} else {
			args = append(args, "response_status", respStatus)
			logger.Debug("http request", args...)
		}
		if len(reqBody) > 0 {
			writeBody("request_body", url, reqBody)
		}
		if len(respBody) > 0 {
			writeBody("response_body", url, respBody)
		}
	}
}

func writeBody(label, url string, body []byte) {
	fmt.Fprintf(debugBodyWriter, "  %s:\n%s\n", label, indentBlock(formatBody(redactBody(url, body)), "    "))
}

// redactURL masks sensitive query values and e-mail addresses embedded in a
// logged URL. A no-op when redaction is disabled.
func redactURL(raw string) string {
	if !redact.Enabled() {
		return raw
	}
	queryStart := strings.IndexByte(raw, '?')
	fragmentStart := strings.IndexByte(raw, '#')
	if queryStart >= 0 && (fragmentStart < 0 || queryStart < fragmentStart) {
		queryEnd := len(raw)
		if fragmentStart > queryStart {
			queryEnd = fragmentStart
		}
		raw = raw[:queryStart+1] + redactQuery(raw[queryStart+1:queryEnd]) + raw[queryEnd:]
	}
	return redact.Text(raw)
}

// redactQuery preserves the raw query structure while masking decoded values.
// Unlike url.Values, it tolerates semicolon separators and malformed escapes.
func redactQuery(raw string) string {
	var result strings.Builder
	for start := 0; start < len(raw); {
		end := start
		for end < len(raw) && raw[end] != '&' && raw[end] != ';' {
			end++
		}
		pair := raw[start:end]
		if key, value, ok := strings.Cut(pair, "="); ok {
			decodedKey := unescapeQueryPart(key)
			decodedValue := unescapeQueryPart(value)
			maskedValue := redact.Text(redact.Value(decodedKey, decodedValue))
			if maskedValue != decodedValue {
				result.WriteString(key)
				result.WriteByte('=')
				result.WriteString(neturl.QueryEscape(maskedValue))
			} else {
				result.WriteString(pair)
			}
		} else {
			result.WriteString(pair)
		}
		if end < len(raw) {
			result.WriteByte(raw[end])
		}
		start = end + 1
	}
	return result.String()
}

// unescapeQueryPart decodes valid query escapes while leaving malformed '%'s
// as literal characters, so a bad escape cannot prevent key-based redaction.
func unescapeQueryPart(raw string) string {
	var escaped strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '%' {
			if i+2 < len(raw) && isHexByte(raw[i+1]) && isHexByte(raw[i+2]) {
				escaped.WriteString(raw[i : i+3])
				i += 2
			} else {
				escaped.WriteString("%25")
			}
		} else {
			escaped.WriteByte(raw[i])
		}
	}
	value, _ := neturl.QueryUnescape(escaped.String())
	return value
}

func isHexByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// secretHeaders carry credentials or session tokens and are never logged.
var secretHeaders = []string{"Proxy-Authorization", "Cookie", "Set-Cookie"}

// redactHeaders masks e-mail addresses in header values and hides credential
// and session headers. Authorization headers are already redacted by the SDK
// and are passed through untouched.
func redactHeaders(headers map[string][]string) map[string][]string {
	if !redact.Enabled() {
		return headers
	}
	out := make(map[string][]string, len(headers))
	for k, vals := range headers {
		if strings.EqualFold(k, "Authorization") {
			out[k] = vals
			continue
		}
		if isSecretHeader(k) {
			out[k] = []string{"<redacted>"}
			continue
		}
		masked := make([]string, len(vals))
		for i, v := range vals {
			masked[i] = redact.Text(v)
		}
		out[k] = masked
	}
	return out
}

func isSecretHeader(name string) bool {
	for _, h := range secretHeaders {
		if strings.EqualFold(name, h) {
			return true
		}
	}
	return false
}

// redactBody masks a request/response body for the debug log (#713): raw
// source files collapse to a size placeholder, SCM and other JSON bodies are
// redacted by key, form-encoded bodies are redacted field by field, and any
// remaining text has e-mail addresses masked.
// Binary payloads are returned as-is: formatBody already reduces them to a
// "<binary, N bytes>" placeholder. A no-op when redaction is disabled.
func redactBody(url string, body []byte) []byte {
	if !redact.Enabled() {
		return body
	}
	switch {
	case strings.Contains(url, "/api/sources/raw"):
		return redact.SourceCode(body)
	case strings.Contains(url, "/api/sources/scm"):
		return redact.SCMJSON(body)
	case json.Valid(body):
		return redact.JSON(body)
	case redact.IsForm(body):
		return redact.Form(body)
	case isPrintableText(body):
		return []byte(redact.Text(string(body)))
	default:
		return body
	}
}

// formatBody returns the body indented as JSON when it parses, otherwise
// the raw string. Binary payloads (e.g. /api/sources/raw, ZIPs, PDFs)
// are replaced with a compact "<binary, N bytes>" placeholder so the
// debug log doesn't garble the terminal with control bytes.
func formatBody(body []byte) string {
	var v any
	if err := json.Unmarshal(body, &v); err == nil {
		pretty, err := json.MarshalIndent(v, "", "  ")
		if err == nil {
			return string(pretty)
		}
	}
	if !isPrintableText(body) {
		return fmt.Sprintf("<binary, %d bytes>", len(body))
	}
	return string(body)
}

// isPrintableText reports whether body is safe to write to the
// terminal verbatim: valid UTF-8 with no NUL byte and no ASCII C0
// control bytes other than TAB / LF / CR. A NUL byte alone is enough
// to classify the payload as binary — text bodies never contain one.
func isPrintableText(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	for _, b := range body {
		if b == 0 {
			return false
		}
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			return false
		}
	}
	return true
}

// indentBlock prefixes every line of s with prefix so the body block is
// visually offset from the surrounding log entries. Uses strings.Builder
// to stay O(n) — naive concatenation across multi-MB JSON responses
// (e.g. getRules) was quadratic and effectively hung the debug logger.
func indentBlock(s, prefix string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s) + len(prefix)*(strings.Count(s, "\n")+1))
	b.WriteString(prefix)
	for i := 0; i < len(s); i++ {
		b.WriteByte(s[i])
		if s[i] == '\n' && i != len(s)-1 {
			b.WriteString(prefix)
		}
	}
	return b.String()
}
