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
		"assignees": true, "authors": true, "logins": true, "scmaccounts": true,
		"submitterlogin": true, "notelogin": true, "externalidentity": true,
		"markdown": true, "htmltext": true, "htmlcomment": true, "comment": true,
	}
	// freeTextKeys hold user-authored prose (issue and hotspot comments); they
	// are replaced by a size placeholder like source code.
	freeTextKeys  = map[string]bool{"markdown": true, "htmltext": true, "htmlcomment": true, "comment": true}
	keyNormalizer = strings.NewReplacer("_", "", "-", "", ".", "")
	emailPattern  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)
	exactEmail    = regexp.MustCompile(`^` + emailPattern.String() + `$`)
	noreplyPrefix = regexp.MustCompile(`^\d+\+`)
	// homePathPattern matches the user-name segment of a home directory.
	homePathPattern = regexp.MustCompile(`(/Users/|/home/|[A-Za-z]:\\{1,2}Users\\{1,2})([^/\\\s"']+)`)
	// almIDPattern matches ALM integration identifiers such as
	// "Client Id:Iv23li..." or "App Id:946159" in system-info text.
	almIDPattern = regexp.MustCompile(`((?:Client|App|Workspace) Id:\s*)([^,"'\s\\]+)`)
	// azureOrgPattern matches the organization segment of an Azure DevOps URL.
	azureOrgPattern = regexp.MustCompile(`(dev\.azure\.com/)[^/"'\s\\,]+`)
)

func normalizeKey(key string) string {
	return keyNormalizer.Replace(strings.ToLower(key))
}

func isSensitiveKey(key string) bool { return sensitiveKeys[normalizeKey(key)] }

func isCodeKey(key string) bool { return normalizeKey(key) == "code" }

func isFreeTextKey(key string) bool { return freeTextKeys[normalizeKey(key)] }

// maskByKey masks s for a key already known to be sensitive.
func maskByKey(key, s string) string {
	if s == "" {
		return s
	}
	if isCodeKey(key) {
		return fmt.Sprintf("<redacted code, %d bytes>", len(s))
	}
	if isFreeTextKey(key) {
		return fmt.Sprintf("<redacted comment, %d bytes>", len(s))
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
	return maskSuffix + keepPrefix(local, keepCount(utf8.RuneCountInString(local))) + maskSuffix + domain
}

// maskEmailsInText masks e-mail addresses, home-directory user names and ALM
// integration identifiers embedded in free text.
func maskEmailsInText(s string) string {
	if strings.Contains(s, "@") {
		s = emailPattern.ReplaceAllStringFunc(s, maskEmail)
	}
	if strings.Contains(s, "Users") || strings.Contains(s, "/home/") {
		s = homePathPattern.ReplaceAllString(s, "${1}"+maskSuffix)
	}
	if strings.Contains(s, " Id:") {
		s = almIDPattern.ReplaceAllStringFunc(s, maskALMID)
	}
	if strings.Contains(s, "dev.azure.com/") {
		s = azureOrgPattern.ReplaceAllString(s, "${1}"+maskSuffix)
	}
	return s
}

func maskALMID(m string) string {
	parts := almIDPattern.FindStringSubmatch(m)
	return parts[1] + keepPrefix(parts[2], keepCount(utf8.RuneCountInString(parts[2]))) + maskSuffix
}
