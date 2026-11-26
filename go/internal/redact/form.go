// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package redact

import (
	"fmt"
	"net/url"
	"strings"
)

// formFreeTextKeys are form fields that carry user-authored prose (issue and
// hotspot comments). They are only treated as free text inside form bodies:
// "text" is too generic to be a sensitive key for log attributes.
var formFreeTextKeys = map[string]bool{"text": true, "markdownnote": true}

// IsForm reports whether body looks like an application/x-www-form-urlencoded
// payload: a single line of key=value pairs joined by '&'.
func IsForm(body []byte) bool {
	s := string(body)
	if s == "" || strings.ContainsAny(s, " \t\r\n{[<") {
		return false
	}
	for _, pair := range strings.Split(s, "&") {
		k, _, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return false
		}
		for _, r := range k {
			if !isFormKeyRune(r) {
				return false
			}
		}
	}
	return true
}

func isFormKeyRune(r rune) bool {
	return r == '_' || r == '-' || r == '.' || r == '[' || r == ']' ||
		(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// Form redacts an application/x-www-form-urlencoded body: sensitive fields
// (login, user, email, ... plus comment text) are masked by key, and e-mail
// addresses in any other value are masked after URL-decoding, so %40-encoded
// addresses are caught. Pairs that need no masking keep their original
// encoding. Returns body unchanged when disabled or not a form.
func Form(body []byte) []byte {
	if !Enabled() || !IsForm(body) {
		return body
	}
	pairs := strings.Split(string(body), "&")
	for i, pair := range pairs {
		rawKey, rawVal, _ := strings.Cut(pair, "=")
		key, err := url.QueryUnescape(rawKey)
		if err != nil {
			key = rawKey
		}
		val, err := url.QueryUnescape(rawVal)
		if err != nil {
			val = rawVal
		}
		if masked := maskFormValue(key, val); masked != val {
			pairs[i] = rawKey + "=" + url.QueryEscape(masked)
		}
	}
	return []byte(strings.Join(pairs, "&"))
}

func maskFormValue(key, val string) string {
	if formFreeTextKeys[normalizeKey(key)] {
		if val == "" {
			return val
		}
		return fmt.Sprintf("<redacted comment, %d bytes>", len(val))
	}
	if isSensitiveKey(key) {
		return maskByKey(key, val)
	}
	return maskEmailsInText(val)
}
