package migrate

// replacementCondition describes how a single SonarQube Cloud condition is
// derived from a SonarQube Server condition during migration. Op and Error
// override the source values when non-empty; otherwise the source op /
// threshold is preserved as-is.
type replacementCondition struct {
	Metric string
	Op     string // "" = inherit source condition op
	Error  string // "" = inherit source condition threshold
}

// Convenience helpers for the table below.
func keep(metric string) []replacementCondition {
	return []replacementCondition{{Metric: metric}}
}

func ratingWorseThan(metric string, letter string) replacementCondition {
	// SonarQube rating quality-gate conditions use op=GT with the numeric
	// rating value as the threshold. "metric <= D" in the issue table means
	// "fire when the rating is worse than D", i.e. value greater than 4.
	letterToValue := map[string]string{
		"A": "1",
		"B": "2",
		"C": "3",
		"D": "4",
	}
	v, ok := letterToValue[letter]
	if !ok {
		v = "1"
	}
	return replacementCondition{Metric: metric, Op: "GT", Error: v}
}

// metricMapping maps a SonarQube Server quality-gate metric to one or more
// SonarQube Cloud conditions, following the table maintained in issue #143.
//
// Semantics:
//
//   - A non-empty slice replaces the source condition with the listed target
//     conditions (one CreateCondition call per element). Per-entry Op/Error
//     overrides the source values; "" means inherit.
//   - An empty slice means the source metric has no SQC equivalent — the
//     condition is dropped and the gate is migrated with one fewer
//     condition. The migration logs this at Warn level.
//
// Metrics that already exist verbatim on SQC are intentionally absent: the
// caller leaves the condition unchanged when lookupMetricReplacement
// returns ok=false.
var metricMapping = map[string][]replacementCondition{
	// *_with_aica / *_without_aica variants — drop the AICA suffix,
	// preserve op + threshold.
	"new_maintainability_rating_with_aica":     keep("new_maintainability_rating"),
	"new_maintainability_rating_without_aica":  keep("new_maintainability_rating"),
	"new_reliability_rating_with_aica":         keep("new_reliability_rating"),
	"new_reliability_rating_without_aica":      keep("new_reliability_rating"),
	"new_security_rating_with_aica":            keep("new_security_rating"),
	"new_security_rating_without_aica":         keep("new_security_rating"),
	"new_security_review_rating_with_aica":     keep("new_security_review_rating"),
	"new_security_review_rating_without_aica":  keep("new_security_review_rating"),
	"reliability_rating_with_aica":             keep("reliability_rating"),
	"reliability_rating_without_aica":          keep("reliability_rating"),
	"security_rating_with_aica":                keep("security_rating"),
	"security_rating_without_aica":             keep("security_rating"),
	"security_review_rating_with_aica":         keep("security_review_rating"),
	"security_review_rating_without_aica":      keep("security_review_rating"),
	"sqale_rating_with_aica":                   keep("sqale_rating"),
	"sqale_rating_without_aica":                keep("sqale_rating"),

	// software_quality_* (SQS-only) → classic SQC equivalents, preserve
	// op + threshold.
	"software_quality_maintainability_debt_ratio":          keep("sqale_debt_ratio"),
	"software_quality_maintainability_rating":              keep("sqale_rating"),
	"software_quality_maintainability_rating_with_aica":    keep("sqale_rating"),
	"software_quality_maintainability_rating_without_aica": keep("sqale_rating"),
	"software_quality_reliability_rating_with_aica":        keep("reliability_rating"),
	"software_quality_reliability_rating_without_aica":     keep("reliability_rating"),
	"software_quality_security_rating":                     keep("security_rating"),
	"software_quality_security_rating_with_aica":           keep("security_rating"),
	"software_quality_security_rating_without_aica":        keep("security_rating"),

	// new_software_quality_* → new_* equivalents, preserve op + threshold.
	"new_software_quality_maintainability_debt_ratio":          keep("new_sqale_debt_ratio"),
	"new_software_quality_maintainability_issues":              keep("new_issues"),
	"new_software_quality_maintainability_rating":              keep("new_maintainability_rating"),
	"new_software_quality_maintainability_rating_with_aica":    keep("new_maintainability_rating"),
	"new_software_quality_maintainability_rating_without_aica": keep("new_maintainability_rating"),
	"new_software_quality_reliability_rating":                  keep("new_reliability_rating"),
	"new_software_quality_reliability_rating_with_aica":        keep("new_reliability_rating"),
	"new_software_quality_reliability_rating_without_aica":     keep("new_reliability_rating"),
	"new_software_quality_security_rating":                     keep("new_security_rating"),

	// new_software_quality_*_issues — table specifies a single rating
	// condition with a fixed threshold (≤ A).
	"new_software_quality_reliability_issues": {ratingWorseThan("new_reliability_rating", "A")},
	"new_software_quality_security_issues":    {ratingWorseThan("new_security_rating", "A")},

	// new_software_quality_security_rating_with_aica / _without_aica →
	// new_security_rating with a fixed ≤ A threshold per the table.
	"new_software_quality_security_rating_with_aica":    {ratingWorseThan("new_security_rating", "A")},
	"new_software_quality_security_rating_without_aica": {ratingWorseThan("new_security_rating", "A")},

	// Composite mappings for software_quality_*_issues. The right-hand side
	// of the issue table is "metricA <= X + metricB <= X".
	"software_quality_blocker_issues": {
		ratingWorseThan("security_review_rating", "D"),
		ratingWorseThan("reliability_rating", "D"),
	},
	"software_quality_high_issues": {
		ratingWorseThan("security_review_rating", "C"),
		ratingWorseThan("reliability_rating", "C"),
	},
	"software_quality_medium_issues": {
		ratingWorseThan("security_review_rating", "B"),
		ratingWorseThan("reliability_rating", "B"),
	},
	"software_quality_low_issues": {
		ratingWorseThan("security_rating", "A"),
		ratingWorseThan("reliability_rating", "A"),
	},
	"software_quality_info_issues": {
		ratingWorseThan("security_rating", "A"),
		ratingWorseThan("reliability_rating", "A"),
	},

	// new_software_quality_*_issues — the issue table shows the same
	// metric repeated twice (e.g. "new_security_review_rating <= D +
	// new_security_review_rating <= D"). Follow the table literally — if
	// SQC rejects the duplicate, the second CreateCondition surfaces a
	// warning in the run log.
	"new_software_quality_blocker_issues": {
		ratingWorseThan("new_security_review_rating", "D"),
		ratingWorseThan("new_security_review_rating", "D"),
	},
	"new_software_quality_high_issues": {
		ratingWorseThan("new_security_review_rating", "C"),
		ratingWorseThan("new_security_review_rating", "C"),
	},
	"new_software_quality_medium_issues": {
		ratingWorseThan("new_security_review_rating", "B"),
		ratingWorseThan("new_security_review_rating", "B"),
	},
	"new_software_quality_low_issues": {
		ratingWorseThan("new_security_review_rating", "A"),
		ratingWorseThan("new_security_review_rating", "A"),
	},
	"new_software_quality_info_issues": {
		ratingWorseThan("new_security_review_rating", "A"),
		ratingWorseThan("new_security_review_rating", "A"),
	},

	// Source metrics with no meaningful SQC equivalent — drop the condition.
	"contains_ai_code": {},
	"effort_to_reach_software_quality_maintainability_rating_a": {},
	"filename_size":          {},
	"filename_size_rating":   {},
	"ncloc_with_aica":        {},
	"ncloc_without_aica":     {},
	"new_software_quality_maintainability_remediation_effort": {},
	"new_software_quality_reliability_remediation_effort":     {},
	"new_software_quality_security_remediation_effort":        {},
	"prioritized_rule_issues":                                 {},
	"releasability_rating":                                    {},
	"releasability_rating_with_aica":                          {},
	"releasability_rating_without_aica":                       {},
	"software_quality_maintainability_issues":                 {},
	"software_quality_maintainability_remediation_effort":     {},
	"software_quality_reliability_issues":                     {},
	"software_quality_reliability_rating":                     {},
	"software_quality_reliability_remediation_effort":         {},
	"software_quality_security_issues":                        {},
	"software_quality_security_remediation_effort":            {},
}

// lookupMetricReplacement returns the list of SonarQube Cloud target
// conditions that should replace a SonarQube Server quality-gate condition
// on the given source metric. The bool indicates whether the source metric
// is known to the mapping table:
//
//   - ok=true,  len(targets)>0  → expand to the listed target conditions
//   - ok=true,  len(targets)==0 → drop the condition (no SQC equivalent)
//   - ok=false                  → metric exists verbatim on SQC, pass through
func lookupMetricReplacement(sourceMetric string) (targets []replacementCondition, ok bool) {
	t, ok := metricMapping[sourceMetric]
	return t, ok
}
