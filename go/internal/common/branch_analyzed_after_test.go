// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"testing"
	"time"
)

func TestParseBranchAnalyzedAfter(t *testing.T) {
	cutoff, err := ParseBranchAnalyzedAfter("")
	if err != nil || cutoff != nil {
		t.Fatalf("ParseBranchAnalyzedAfter(\"\") = %v, %v; want nil, nil", cutoff, err)
	}

	cutoff, err = ParseBranchAnalyzedAfter("2024-01-15")
	if err != nil {
		t.Fatalf("ParseBranchAnalyzedAfter(\"2024-01-15\") returned error: %v", err)
	}
	want := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	if cutoff == nil || !cutoff.Equal(want) {
		t.Fatalf("ParseBranchAnalyzedAfter(\"2024-01-15\") = %v, want %v", cutoff, want)
	}

	for _, bad := range []string{"15-01-2024", "2024/01/15", "not-a-date", "2024-01-15T00:00:00Z", "2024-13-40"} {
		if _, err := ParseBranchAnalyzedAfter(bad); err == nil {
			t.Errorf("ParseBranchAnalyzedAfter(%q) succeeded, want an explicit format error", bad)
		}
	}
}

func TestIsBranchAnalyzedAfterStale(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		cutoff time.Time
		want   bool
	}{
		{"731 days ago is stale", now.AddDate(0, 0, -731), true},
		{"exactly 730 days ago is not stale", now.AddDate(0, 0, -730), false},
		{"1 day ago is not stale", now.AddDate(0, 0, -1), false},
		{"in the future is not stale", now.AddDate(0, 0, 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBranchAnalyzedAfterStale(tc.cutoff, now); got != tc.want {
				t.Errorf("IsBranchAnalyzedAfterStale(%s, %s) = %v, want %v", tc.cutoff, now, got, tc.want)
			}
		})
	}
}

func TestParseAnalysisDate(t *testing.T) {
	if got := ParseAnalysisDate(""); !got.IsZero() {
		t.Errorf("ParseAnalysisDate(\"\") = %v, want zero time", got)
	}
	if got := ParseAnalysisDate("not-a-date"); !got.IsZero() {
		t.Errorf("ParseAnalysisDate(\"not-a-date\") = %v, want zero time", got)
	}

	rfc3339 := "2024-06-01T12:00:00Z"
	if got := ParseAnalysisDate(rfc3339); got.IsZero() || got.Year() != 2024 {
		t.Errorf("ParseAnalysisDate(%q) = %v, want a parsed 2024 date", rfc3339, got)
	}

	legacy := "2024-06-01T12:00:00+0200"
	if got := ParseAnalysisDate(legacy); got.IsZero() || got.Year() != 2024 {
		t.Errorf("ParseAnalysisDate(%q) = %v, want a parsed 2024 date", legacy, got)
	}
}
