// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

// Package redact masks personal and source-code data in log output (#713).
//
// Redaction is a process-wide switch, on by default, controlled by the
// --redact_logs flag / "redact_logs" config field. It applies to what the
// tool LOGS only; extracted and migrated data is never altered.
//
// Sensitive keys are matched case-insensitively with '_', '-' and '.'
// ignored, so userName, user_name and user-name are equivalent. They cover
// users and logins (user, userName, login, email, author, scmAuthor,
// assignee, their plurals, scmAccounts, submitterLogin, noteLogin,
// externalIdentity and the userLogin/userEmail/authorLogin/authorEmail
// compounds), source code (code, replaced by a size placeholder) and
// user-authored comments (markdown, htmlText, htmlComment, comment). See
// mask.go for the authoritative list.
package redact

import (
	"fmt"
	"log/slog"
	"sync/atomic"
)

var enabled atomic.Bool

func init() { enabled.Store(true) }

// SetEnabled turns log redaction on or off for the whole process.
func SetEnabled(v bool) { enabled.Store(v) }

// Enabled reports whether log redaction is on (default true).
func Enabled() bool { return enabled.Load() }

// Value masks s according to the sensitive attribute key it was logged under
// (case-insensitive; "user", "userName", "email", "login", "author",
// "scmAuthor", "assignee", "code" plus snake_case variants). Keys that are
// not sensitive return s unchanged. Returns s unchanged when disabled.
func Value(key, s string) string {
	if !Enabled() || !isSensitiveKey(key) {
		return s
	}
	return maskByKey(key, s)
}

// Text masks e-mail addresses embedded in free text. It returns s unchanged
// when disabled.
func Text(s string) string {
	if !Enabled() {
		return s
	}
	return maskEmailsInText(s)
}

// JSON redacts a JSON document by key (recursively, objects and arrays).
// Input that is not valid JSON is returned unchanged.
func JSON(body []byte) []byte { return redactJSON(body, false) }

// SCMJSON is JSON plus handling of the /api/sources/scm shape, where authors
// are positional ([[line, author, date, revision], ...] under "scm").
func SCMJSON(body []byte) []byte { return redactJSON(body, true) }

// SourceCode replaces a raw source-file body with a placeholder such as
// "<redacted source code, N bytes>". Returns body unchanged when disabled.
func SourceCode(body []byte) []byte {
	if !Enabled() {
		return body
	}
	return []byte(fmt.Sprintf("<redacted source code, %d bytes>", len(body)))
}

// NewHandler wraps h so every attribute (including groups and WithAttrs
// attributes) and the record message are redacted by key and by e-mail
// pattern while Enabled() is true. The switch is read at log time, so it can
// be flipped after the logger is installed.
func NewHandler(h slog.Handler) slog.Handler { return &handler{raw: h, red: h} }
