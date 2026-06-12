// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"

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
// per HTTP request/response pair containing method, URL, headers, and
// response status. When the request or response has a body, it is written
// to debugBodyWriter (os.Stderr by default) as a separate block: JSON
// bodies are pretty-printed, non-JSON bodies are written verbatim.
// Authorization headers are already redacted by the SDK before this
// callback runs.
//
// Shared by extract and migrate so --debug produces the same shape of
// per-request log regardless of which command emitted it.
func NewHTTPDebugLogger(logger *slog.Logger) sqapi.DebugLogFunc {
	return func(method, url string, headers map[string][]string, reqBody []byte, respStatus int, respBody []byte, err error) {
		args := []any{
			"method", method,
			"url", url,
			"headers", headers,
		}
		if err != nil {
			args = append(args, "err", err.Error())
			logger.Debug("http request failed", args...)
		} else {
			args = append(args, "response_status", respStatus)
			logger.Debug("http request", args...)
		}
		if len(reqBody) > 0 {
			writeBody("request_body", reqBody)
		}
		if len(respBody) > 0 {
			writeBody("response_body", respBody)
		}
	}
}

func writeBody(label string, body []byte) {
	fmt.Fprintf(debugBodyWriter, "  %s:\n%s\n", label, indentBlock(formatBody(body), "    "))
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
