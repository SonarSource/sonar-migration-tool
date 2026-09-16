// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import "strconv"

// FormatCount renders a count with thousands separators, so the
// truncation path writes "14,903" the way the rest of the migration
// report writes large numbers rather than "14903" (#574).
//
// It lives in common because the two renderers of a truncation record
// sit in different packages and must agree: the migration report's
// Limitations bullet (internal/report/summary) and extract's
// end-of-run console block (internal/extract). The report package's
// own unexported addCommas covers table cells there and is not
// reachable from either.
//
// Negative input keeps its sign; a count is never negative today, but
// returning "-1,234" beats returning "-,1234" if one ever is.
func FormatCount(n int) string {
	s := strconv.Itoa(n)
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	// Insertion positions are computed against the ORIGINAL string and
	// applied right to left, so every insert lands past the ones still
	// to come and the indices stay valid as the string grows.
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return sign + s
}
