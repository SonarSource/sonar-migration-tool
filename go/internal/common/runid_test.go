// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import "testing"

// TestRunIDAfter_NumericCrossoverPast99 reproduces #542: once a run's
// trailing counter grows to three digits, lexicographic comparison
// wrongly treats a lower two-digit counter as "later".
func TestRunIDAfter_NumericCrossoverPast99(t *testing.T) {
	if RunIDAfter("2026-08-20-99", "2026-08-20-101") {
		t.Error(`RunIDAfter("2026-08-20-99", "2026-08-20-101") = true, want false`)
	}
	if !RunIDAfter("2026-08-20-101", "2026-08-20-99") {
		t.Error(`RunIDAfter("2026-08-20-101", "2026-08-20-99") = false, want true`)
	}
}

func TestRunIDAfter_SameWidthOrderingUnchanged(t *testing.T) {
	if !RunIDAfter("2026-08-20-02", "2026-08-20-01") {
		t.Error("expected -02 to be after -01")
	}
	if RunIDAfter("2026-08-20-01", "2026-08-20-02") {
		t.Error("expected -01 to not be after -02")
	}
}

func TestRunIDAfter_DifferingDatePrefixesCompareChronologically(t *testing.T) {
	if !RunIDAfter("2026-08-21-01", "2026-08-20-99") {
		t.Error("expected a later date (even with a smaller counter) to be after an earlier date")
	}
	if RunIDAfter("2026-08-20-99", "2026-08-21-01") {
		t.Error("expected an earlier date to not be after a later date")
	}
}

func TestRunIDAfter_EqualIDsAreNotAfterEachOther(t *testing.T) {
	if RunIDAfter("2026-08-20-01", "2026-08-20-01") {
		t.Error("an ID must not be considered after itself")
	}
}

func TestRunIDAfter_NonNumericSuffixFallsBackToStringCompare(t *testing.T) {
	// IDs that don't fit "<prefix>-<digits>" (e.g. hand-crafted test
	// fixture names) must still compare deterministically rather than
	// panicking or always returning false.
	if !RunIDAfter("extract-02", "extract-01") {
		t.Error(`RunIDAfter("extract-02", "extract-01") = false, want true`)
	}
	if RunIDAfter("", "") {
		t.Error(`RunIDAfter("", "") = true, want false`)
	}
	if !RunIDAfter("any-id", "") {
		t.Error(`RunIDAfter("any-id", "") = false, want true (first-seen initialization case)`)
	}
}
