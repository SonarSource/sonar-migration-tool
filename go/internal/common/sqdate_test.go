// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// sqDatePattern is the shape SonarQube accepts: seconds precision, a
// four-digit numeric offset, no fractional part.
var sqDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{4}$`)

// TestFormatSQDateEmitsTheOnlyLayoutTheAPIAccepts prevents the failure
// where a date bound is formatted with time.RFC3339 (or any layout with
// a "Z", a fractional second or a "+00:00" offset) and SonarQube rejects
// every windowed request with HTTP 400 — which, if the error were ever
// swallowed, would look like a project with no issues. It also prevents
// the subtler failure of a non-UTC time being rendered with a
// UTC-looking suffix, giving a wrong instant the server accepts happily
// (#574).
func TestFormatSQDateEmitsTheOnlyLayoutTheAPIAccepts(t *testing.T) {
	cases := []struct {
		in   time.Time
		want string
	}{
		// The measured oldest issue in the reference project.
		{time.Date(2025, 12, 30, 3, 2, 17, 0, time.UTC), "2025-12-30T03:02:17+0000"},
		// Same instant, expressed +0100: must normalise, not relabel.
		{time.Date(2025, 12, 30, 4, 2, 17, 0, time.FixedZone("CET", 3600)), "2025-12-30T03:02:17+0000"},
		// Same instant, expressed -0500.
		{time.Date(2025, 12, 29, 22, 2, 17, 0, time.FixedZone("EST", -5*3600)), "2025-12-30T03:02:17+0000"},
		// Sub-second input: the fraction must be dropped, never emitted.
		{time.Date(2026, 9, 15, 4, 58, 6, 999000000, time.UTC), "2026-09-15T04:58:06+0000"},
	}
	for _, tc := range cases {
		got := FormatSQDate(tc.in)
		if got != tc.want {
			t.Errorf("FormatSQDate(%s) = %q, want %q", tc.in.Format(time.RFC3339Nano), got, tc.want)
		}
		if !sqDatePattern.MatchString(got) {
			t.Errorf("FormatSQDate(%s) = %q, want a match for %s", tc.in.Format(time.RFC3339Nano), got, sqDatePattern)
		}
		for _, banned := range []string{"Z", ".", "+00:00"} {
			if strings.Contains(got, banned) {
				t.Errorf("FormatSQDate(%s) = %q, must not contain %q (HTTP 400 from the API)",
					tc.in.Format(time.RFC3339Nano), got, banned)
			}
		}
	}
}

// TestParseSQDateRoundTrips prevents the failure where the creation
// dates read back from a response cannot be parsed with the same layout
// used to send them, which would break the date-range probe that seeds
// the midpoint arithmetic (#574).
func TestParseSQDateRoundTrips(t *testing.T) {
	// Exactly the wire format observed on api/issues/search.
	const wire = "2025-12-30T03:02:17+0000"

	parsed, err := ParseSQDate(wire)
	if err != nil {
		t.Fatalf("ParseSQDate(%q) failed: %v", wire, err)
	}
	if got := FormatSQDate(parsed); got != wire {
		t.Errorf("round trip of %q: got %q, want %q", wire, got, wire)
	}
	if got := parsed.UTC(); got.Hour() != 3 || got.Minute() != 2 || got.Second() != 17 {
		t.Errorf("ParseSQDate(%q) = %s, want 03:02:17 UTC", wire, got)
	}
	if _, err := ParseSQDate("2025-12-30T03:02:17Z"); err == nil {
		t.Error("ParseSQDate accepted an RFC3339 Z suffix; the API rejects that format")
	}
}
