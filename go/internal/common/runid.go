// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"strconv"
	"strings"
)

// RunIDAfter reports whether run/extract ID a is more recent than b.
// Run IDs are "<date-prefix>-<NN>" where NN is zero-padded to a minimum
// of two digits but grows unpadded past 99 (see generateRunID in
// internal/migrate and internal/wizard), so a plain string compare
// misorders once NN reaches three digits (e.g. "...-99" > "...-101",
// #542). When both IDs share the same prefix, compare NN numerically;
// the date prefix itself (fixed-width YYYY-MM-DD or the legacy
// MM-DD-YYYY) still sorts correctly as a string, so differing prefixes
// and anything that isn't in "<prefix>-<digits>" shape fall back to a
// plain string compare.
func RunIDAfter(a, b string) bool {
	aPrefix, aNum, aOK := splitRunSuffix(a)
	bPrefix, bNum, bOK := splitRunSuffix(b)
	if aOK && bOK && aPrefix == bPrefix {
		return aNum > bNum
	}
	return a > b
}

// splitRunSuffix splits id into everything before the last "-" and the
// trailing numeric counter after it. ok is false when id has no "-" or
// the trailing segment isn't a decimal integer.
func splitRunSuffix(id string) (prefix string, num int, ok bool) {
	i := strings.LastIndex(id, "-")
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil {
		return "", 0, false
	}
	return id[:i], n, true
}
