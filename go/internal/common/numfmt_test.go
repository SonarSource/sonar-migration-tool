// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import "testing"

// #574: the truncation bullet and the extract console block both
// render counts through FormatCount, so a grouping bug here shows up
// as a wrong number in an operator-facing data-loss claim. The
// boundaries that matter are the ones a naive "insert every three
// characters" loop gets wrong: exactly three digits (no separator at
// all), exactly four (a leading group of one), and a negative.
func TestFormatCountGroupsThousands(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want string
	}{
		{"zero stays bare", 0, "0"},
		{"a single digit stays bare", 7, "7"},
		{"three digits get no separator", 100, "100"},
		{"four digits get a leading group of one", 1000, "1,000"},
		{"the measured atomic-window loss", 7919, "7,919"},
		{"the measured project total", 14903, "14,903"},
		{"six digits get one separator", 123456, "123,456"},
		{"seven digits get two separators", 1000000, "1,000,000"},
		{"a negative keeps its sign outside the groups", -14903, "-14,903"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatCount(tc.in); got != tc.want {
				t.Errorf("FormatCount(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
