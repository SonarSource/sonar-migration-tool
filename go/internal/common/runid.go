// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GenerateRunID returns an ISO-date-prefixed run ID (issue #108) for a
// new run/extract under directory. Format: "YYYY-MM-DD-NNNN" where NNNN
// is the next sequence number for the current day, zero-padded to four
// digits so lexicographic and numeric ordering agree up to 9999 runs/day
// (#542 — the previous two-digit padding let a 3-digit run number like
// "-101" sort lexicographically *before* "-99").
//
// The next sequence number is the highest existing one for today, plus
// one. An earlier (count-of-dirs + 1) approach broke once the numbering
// had ANY gap — e.g. dirs -0010..-0019 with none below would yield
// count=10, colliding with the existing -0011 and silently reusing its
// task outputs. See the #359 follow-up regression report. This one
// function backs migrate, extract, and wizard's run/extract ID
// generation — they used to keep three hand-synced copies in sync,
// which is exactly the kind of drift that let this wizard's copy fall
// behind with the buggy count-based approach until #542.
func GenerateRunID(directory string) string {
	today := time.Now().UTC().Format("2006-01-02")
	prefix := today + "-"
	entries, _ := os.ReadDir(directory)
	maxN := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		n, err := strconv.Atoi(e.Name()[len(prefix):])
		if err == nil && n > maxN {
			maxN = n
		}
	}
	return fmt.Sprintf("%s-%04d", today, maxN+1)
}

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

// runIDPattern matches exactly the shape GenerateRunID produces:
// "YYYY-MM-DD-N" where N is one or more digits (unpadded past 9999,
// per the #542 fix — see GenerateRunID).
var runIDPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d+$`)

// IsValidRunID reports whether id has exactly the shape GenerateRunID
// produces. Callers that treat directory names as candidate run IDs
// (e.g. structure.buildURLMappings) should use this to reject anything
// else before it ever reaches RunIDAfter: since real run IDs always
// start with a digit and any letter sorts above any digit in ASCII, a
// non-conforming name (e.g. a directory an attacker planted, like
// "zzz-evil") would otherwise win RunIDAfter's plain-string-compare
// fallback over every legitimate dated run (#550).
func IsValidRunID(id string) bool {
	return runIDPattern.MatchString(id)
}
