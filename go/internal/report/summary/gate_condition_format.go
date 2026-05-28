package summary

import (
	"fmt"
	"strings"
)

// isRatingMetric reports whether the metric's threshold is the
// integer-encoded rating value 1..5 (A..E). Quality-gate conditions on
// rating metrics are expressed on the wire as `GT <numeric>` but the
// SonarQube documentation in #143 reads them as `metric <= <letter>`.
// The report renders the documentation form so operators don't have to
// translate.
//
// Detection is suffix-based so it covers the full family — the canonical
// names (reliability_rating, security_rating, ...), the new_* variants,
// the software_quality_*_rating MQR-mode aliases, and the AICA suffix
// variants — without enumerating ~40 metric names.
func isRatingMetric(metric string) bool {
	base := metric
	if s := strings.TrimSuffix(base, "_with_aica"); s != base {
		base = s
	} else if s := strings.TrimSuffix(base, "_without_aica"); s != base {
		base = s
	}
	return strings.HasSuffix(base, "_rating")
}

// ratingLetter converts the numeric threshold the API uses (1..5) to the
// A..E letter that documentation and the SonarQube UI both use.
func ratingLetter(errorVal string) (string, bool) {
	switch errorVal {
	case "1":
		return "A", true
	case "2":
		return "B", true
	case "3":
		return "C", true
	case "4":
		return "D", true
	case "5":
		return "E", true
	}
	return "", false
}

// formatGateCondition renders one quality-gate condition in the #143
// notation: `metric <= LETTER` for a rating condition expressed as
// `GT numeric`, the literal `metric OP value` form otherwise.
//
// The function never panics on unknown ops or non-numeric thresholds —
// it falls back to whatever the input carried so the user still sees
// something self-descriptive.
func formatGateCondition(metric, op, errorVal string) string {
	if isRatingMetric(metric) && op == "GT" {
		if letter, ok := ratingLetter(errorVal); ok {
			return fmt.Sprintf("%s <= %s", metric, letter)
		}
	}
	return fmt.Sprintf("%s %s %s", metric, opSymbol(op), errorVal)
}

// opSymbol maps an API op token to a human-readable symbol. Unknown
// tokens are returned as-is so the operator at least sees the raw value.
func opSymbol(op string) string {
	switch op {
	case "GT":
		return ">"
	case "LT":
		return "<"
	case "GTE":
		return ">="
	case "LTE":
		return "<="
	case "EQ":
		return "="
	case "NE":
		return "!="
	}
	return op
}
