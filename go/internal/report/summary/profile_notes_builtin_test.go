// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import "testing"

func TestFormatBuiltInProfileDiff(t *testing.T) {
	cases := []struct {
		name           string
		added, removed int
		want           string
	}{
		{"identical", 0, 0, ""},
		{"added only", 5, 0, "5 rule(s) added"},
		{"removed only", 0, 3, "3 rule(s) removed"},
		{"both", 5, 3, "5 rule(s) added, 3 rule(s) removed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatBuiltInProfileDiff(tc.added, tc.removed); got != tc.want {
				t.Errorf("formatBuiltInProfileDiff(%d, %d) = %q, want %q", tc.added, tc.removed, got, tc.want)
			}
		})
	}
}
