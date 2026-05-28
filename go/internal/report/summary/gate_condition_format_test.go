package summary

import "testing"

func TestFormatGateCondition(t *testing.T) {
	cases := []struct {
		name   string
		metric string
		op     string
		errVal string
		want   string
	}{
		// Rating metrics with GT — render in #143 letter notation.
		{"rating GT 1 → <= A", "new_security_rating", "GT", "1", "new_security_rating <= A"},
		{"rating GT 2 → <= B", "reliability_rating", "GT", "2", "reliability_rating <= B"},
		{"rating GT 3 → <= C", "security_rating", "GT", "3", "security_rating <= C"},
		{"rating GT 4 → <= D", "sqale_rating", "GT", "4", "sqale_rating <= D"},
		{"new rating GT 4 → <= D", "new_maintainability_rating", "GT", "4", "new_maintainability_rating <= D"},

		// Non-rating metric: literal notation regardless of op.
		{"issue count GT 0 → > 0", "new_software_quality_security_issues", "GT", "0", "new_software_quality_security_issues > 0"},
		{"coverage LT 80 → < 80", "coverage", "LT", "80", "coverage < 80"},
		{"duplicated lines GT 2 → > 2", "duplicated_lines_density", "GT", "2", "duplicated_lines_density > 2"},

		// Defensive: rating metric with non-GT op — fall through to literal.
		{"rating LT 4 (not standard) → literal", "security_rating", "LT", "4", "security_rating < 4"},
		// Defensive: rating metric with GT but non-numeric threshold.
		{"rating GT with garbage threshold → literal", "security_rating", "GT", "X", "security_rating > X"},
		// Defensive: unknown op — pass through verbatim.
		{"unknown op stays as-is", "coverage", "FOO", "80", "coverage FOO 80"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatGateCondition(tc.metric, tc.op, tc.errVal)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
