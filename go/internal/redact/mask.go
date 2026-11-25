// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package redact

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maskSuffix = "***"

var (
	sensitiveKeys = map[string]bool{
		"user": true, "username": true, "email": true, "login": true,
		"author": true, "scmauthor": true, "assignee": true, "code": true,
		"userlogin": true, "useremail": true, "authorlogin": true, "authoremail": true,
	}
	keyNormalizer = strings.NewReplacer("_", "", "-", "", ".", "")
	emailPattern  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)
	exactEmail    = regexp.MustCompile(`^` + emailPattern.String() + `$`)
	noreplyPrefix = regexp.MustCompile(`^\d+\+`)
)

func normalizeKey(key string) string {
	return keyNormalizer.Replace(strings.ToLower(key))
}

func isSensitiveKey(key string) bool { return sensitiveKeys[normalizeKey(key)] }

func isCodeKey(key string) bool { return normalizeKey(key) == "code" }

// maskByKey masks s for a key already known to be sensitive.
func maskByKey(key, s string) string {
	if s == "" {
		return s
	}
	if isCodeKey(key) {
		return fmt.Sprintf("<redacted code, %d bytes>", len(s))
	}
	return maskPlain(s)
}

// maskPlain masks a user-ish value: e-mail-shaped values use the e-mail rule.
func maskPlain(s string) string {
	if s == "" {
		return s
	}
	if exactEmail.MatchString(s) {
		return maskEmail(s)
	}
	return keepPrefix(s, keepCount(utf8.RuneCountInString(s))) + maskSuffix
}

// keepCount is min(3, round(n/3)), with values of 2 runes or fewer fully hidden.
func keepCount(n int) int {
	if n <= 2 {
		return 0
	}
	return min(3, int(math.Round(float64(n)/3)))
}

// keepPrefix returns the first n runes of s, lowercased so that values differing
// only in case correlate.
func keepPrefix(s string, n int) string {
	if n == 0 {
		return ""
	}
	return strings.ToLower(firstRunes(s, n))
}

func firstRunes(s string, n int) string {
	i := 0
	for ; n > 0 && i < len(s); n-- {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

func maskEmail(e string) string {
	at := strings.LastIndexByte(e, '@')
	local, domain := e[:at], e[at:]
	if stripped := noreplyPrefix.ReplaceAllString(local, ""); stripped != "" {
		local = stripped
	}
	return maskSuffix + keepPrefix(local, min(3, utf8.RuneCountInString(local))) + maskSuffix + domain
}

func maskEmailsInText(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	return emailPattern.ReplaceAllStringFunc(s, maskEmail)
}
