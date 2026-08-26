// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// TestGenerateRunID_HandlesNumberingGaps is the single, authoritative
// test for GenerateRunID (#542) — this function used to have three
// hand-synced copies (migrate, extract, wizard), each with its own
// near-identical test, which is exactly the drift that let wizard's
// copy fall behind with a buggy count-based algorithm until it was
// caught and fixed alongside this consolidation.
//
// The gap-handling case is a regression test for #359: an earlier
// (count-of-dirs + 1) approach broke as soon as the numbering had any
// gap — e.g. dirs -0010..-0019 with none below would yield count=10,
// colliding with the existing -0011 and silently reusing its task
// outputs. The fix returns max(N)+1 where N is the existing suffix on
// today's dirs.
func TestGenerateRunID_HandlesNumberingGaps(t *testing.T) {
	t.Run("empty directory yields -0001", testGenerateRunID_EmptyDir)
	t.Run("dirs -10..-19 with gaps below yields -0020 (not -11 collision)", testGenerateRunID_GapBelow)
	t.Run("non-contiguous numbering still returns max+1", testGenerateRunID_NonContiguous)
	t.Run("dirs from other days are ignored", testGenerateRunID_ForeignDayIgnored)
	t.Run("dirs with non-numeric suffix are ignored", testGenerateRunID_NonNumericSuffixIgnored)
	// #542 — the sequence number is zero-padded to four digits so that
	// among IDs generated after this fix, lexicographic order keeps
	// matching numeric order past the 99th run of a day (a
	// pre-existing, non-padded "-99" dir from before this fix is a
	// separate, unavoidable transition case handled by RunIDAfter
	// instead, not by GenerateRunID's own output).
	t.Run("crossing the 99->100 boundary still zero-pads to four digits", testGenerateRunID_CrossesPast99)
}

func mkRunDir(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
}

func testGenerateRunID_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().UTC().Format("2006-01-02")
	got := GenerateRunID(dir)
	want := today + "-0001"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func testGenerateRunID_GapBelow(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().UTC().Format("2006-01-02")
	for i := 10; i <= 19; i++ {
		mkRunDir(t, dir, fmt.Sprintf("%s-%02d", today, i))
	}
	got := GenerateRunID(dir)
	want := today + "-0020"
	if got != want {
		t.Errorf("want %q (max+1), got %q", want, got)
	}
}

func testGenerateRunID_NonContiguous(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().UTC().Format("2006-01-02")
	for _, n := range []int{1, 3, 7, 42} {
		mkRunDir(t, dir, fmt.Sprintf("%s-%02d", today, n))
	}
	got := GenerateRunID(dir)
	want := today + "-0043"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func testGenerateRunID_ForeignDayIgnored(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().UTC().Format("2006-01-02")
	// Other days don't participate in the count.
	mkRunDir(t, dir, "2020-01-01-99")
	got := GenerateRunID(dir)
	want := today + "-0001"
	if got != want {
		t.Errorf("foreign-day dir should not affect count: want %q, got %q", want, got)
	}
}

func testGenerateRunID_NonNumericSuffixIgnored(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().UTC().Format("2006-01-02")
	mkRunDir(t, dir, today+"-rc1")
	got := GenerateRunID(dir)
	// Only well-formed dirs influence max; rc1 is skipped.
	if !strings.HasPrefix(got, today+"-") {
		t.Errorf("expected today-prefixed ID, got %q", got)
	}
	if got != today+"-0001" {
		t.Errorf("non-numeric suffix should be ignored: want %q, got %q", today+"-0001", got)
	}
}

func testGenerateRunID_CrossesPast99(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().UTC().Format("2006-01-02")
	mkRunDir(t, dir, today+"-99")
	got := GenerateRunID(dir)
	want := today + "-0100"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

// TestGenerateRunID_ISOFormatShape pins the overall shape produced for
// a fresh directory: an ISO YYYY-MM-DD date prefix (not the historical
// MM-DD-YYYY format), followed by a four-digit, zero-padded sequence
// number (#108, #542).
func TestGenerateRunID_ISOFormatShape(t *testing.T) {
	dir := t.TempDir()
	id := GenerateRunID(dir)
	if id == "" {
		t.Fatal("GenerateRunID should return non-empty string")
	}
	if id[len(id)-5:] != "-0001" {
		t.Errorf("expected -0001 suffix, got %q", id)
	}
	// We don't hard-code today's date (test would drift) but pin the
	// shape: id must be at least YYYY-MM-DD-NNNN = 15 chars, must
	// start with today's UTC year, and the 5th character must be a
	// hyphen.
	wantYear := time.Now().UTC().Format("2006")
	if len(id) < 15 {
		t.Fatalf("id too short for ISO format YYYY-MM-DD-NNNN: %q", id)
	}
	if id[:4] != wantYear {
		t.Errorf("id must start with current UTC year %q, got %q (full id %q)", wantYear, id[:4], id)
	}
	if id[4] != '-' {
		t.Errorf("id[4] must be '-' for ISO format, got %q (full id %q)", string(id[4]), id)
	}
}
