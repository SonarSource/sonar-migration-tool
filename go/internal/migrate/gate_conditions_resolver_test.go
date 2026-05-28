package migrate

import (
	"reflect"
	"testing"
)

// TestResolveTargetConditions exercises the #234 collision-resolution rules:
// when multiple source conditions produce target conditions on the same SQC
// metric, keep one — the most stringent. "More stringent" means "fires the
// gate for more cases" (lower threshold for GT, higher threshold for LT).
func TestResolveTargetConditions(t *testing.T) {
	cases := []struct {
		name string
		in   []targetCondition
		want []targetCondition
	}{
		{
			name: "no collision passes through unchanged",
			in: []targetCondition{
				{Metric: "security_rating", Op: "GT", Error: "4", SourceMetric: "software_quality_blocker_issues"},
				{Metric: "reliability_rating", Op: "GT", Error: "4", SourceMetric: "software_quality_blocker_issues"},
			},
			want: []targetCondition{
				{Metric: "security_rating", Op: "GT", Error: "4", SourceMetric: "software_quality_blocker_issues"},
				{Metric: "reliability_rating", Op: "GT", Error: "4", SourceMetric: "software_quality_blocker_issues"},
			},
		},
		{
			// #234 example: blocker_issues expansion (reliability_rating GT 4)
			// collides with a direct reliability_rating GT 2. The direct
			// passthrough is more stringent (B < D), so it wins.
			name: "blocker expansion + direct reliability GT 2 → direct wins",
			in: []targetCondition{
				{Metric: "security_rating", Op: "GT", Error: "4", SourceMetric: "software_quality_blocker_issues"},
				{Metric: "reliability_rating", Op: "GT", Error: "4", SourceMetric: "software_quality_blocker_issues"},
				{Metric: "reliability_rating", Op: "GT", Error: "2", SourceMetric: "reliability_rating"},
			},
			want: []targetCondition{
				{Metric: "security_rating", Op: "GT", Error: "4", SourceMetric: "software_quality_blocker_issues"},
				{Metric: "reliability_rating", Op: "GT", Error: "2", SourceMetric: "reliability_rating"},
			},
		},
		{
			// #234 example: medium expansion (security_rating GT 2) collides
			// with direct security_rating GT 3. Expansion is more stringent
			// (B < C), so it wins.
			name: "medium expansion + direct security_rating GT 3 → expansion wins",
			in: []targetCondition{
				{Metric: "security_rating", Op: "GT", Error: "2", SourceMetric: "software_quality_medium_issues"},
				{Metric: "reliability_rating", Op: "GT", Error: "2", SourceMetric: "software_quality_medium_issues"},
				{Metric: "security_rating", Op: "GT", Error: "3", SourceMetric: "security_rating"},
			},
			want: []targetCondition{
				{Metric: "security_rating", Op: "GT", Error: "2", SourceMetric: "software_quality_medium_issues"},
				{Metric: "reliability_rating", Op: "GT", Error: "2", SourceMetric: "software_quality_medium_issues"},
			},
		},
		{
			// Two direct passthroughs on the same metric — collapse to the
			// more stringent one (chosen behaviour per #234 follow-up).
			name: "two direct security_rating conditions collapse to most stringent",
			in: []targetCondition{
				{Metric: "security_rating", Op: "GT", Error: "4", SourceMetric: "security_rating"},
				{Metric: "security_rating", Op: "GT", Error: "3", SourceMetric: "security_rating"},
			},
			want: []targetCondition{
				{Metric: "security_rating", Op: "GT", Error: "3", SourceMetric: "security_rating"},
			},
		},
		{
			// LT semantics: for a coverage condition, higher threshold is
			// more stringent (catches more low-coverage cases).
			name: "LT — higher threshold is more stringent",
			in: []targetCondition{
				{Metric: "coverage", Op: "LT", Error: "70", SourceMetric: "coverage"},
				{Metric: "coverage", Op: "LT", Error: "85", SourceMetric: "coverage"},
			},
			want: []targetCondition{
				{Metric: "coverage", Op: "LT", Error: "85", SourceMetric: "coverage"},
			},
		},
		{
			// Mixed ops on the same metric — incomparable, the first wins.
			// Defensive: SQC gates don't actually mix GT and LT on the same
			// metric, but the resolver shouldn't panic if they do.
			name: "incomparable ops — first wins",
			in: []targetCondition{
				{Metric: "weird_metric", Op: "GT", Error: "5", SourceMetric: "src"},
				{Metric: "weird_metric", Op: "LT", Error: "5", SourceMetric: "src"},
			},
			want: []targetCondition{
				{Metric: "weird_metric", Op: "GT", Error: "5", SourceMetric: "src"},
			},
		},
		{
			name: "empty input → empty output",
			in:   nil,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveTargetConditions(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}
