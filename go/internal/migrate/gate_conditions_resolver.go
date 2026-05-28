package migrate

import "strconv"

// targetCondition is one condition the migrator intends to POST to SQC,
// after a source condition has been remapped (1:1, composite, or
// passthrough) through metricMapping. SourceMetric is carried for logging
// only — the resolver itself works off (Metric, Op, Error).
type targetCondition struct {
	Metric       string
	Op           string
	Error        string
	SourceMetric string
}

// resolveTargetConditions implements #234: when multiple source-side QG
// conditions end up targeting the same SonarQube Cloud metric (e.g. a
// software_quality_blocker_issues composite that expands to a rating
// metric also held by a direct passthrough condition), collapse them to
// a single most-stringent condition per metric.
//
// "Stringent" means "fires the gate for more cases":
//   - For Op=GT / GTE, the smaller threshold is more stringent.
//   - For Op=LT / LTE, the larger threshold is more stringent.
//   - Conditions on the same metric but with incomparable Ops (e.g. GT
//     vs LT) are left alone — the first one wins, the rest are dropped.
//     In practice SQC quality-gate conditions on a given metric always
//     use the same op, so this branch is defensive.
//
// Input order is preserved for metrics that don't collide.
func resolveTargetConditions(conds []targetCondition) []targetCondition {
	if len(conds) <= 1 {
		return conds
	}
	// Track the first appearance order of each metric so the output is
	// deterministic regardless of map iteration order.
	order := make([]string, 0, len(conds))
	byMetric := make(map[string]targetCondition, len(conds))
	for _, c := range conds {
		existing, seen := byMetric[c.Metric]
		if !seen {
			order = append(order, c.Metric)
			byMetric[c.Metric] = c
			continue
		}
		if moreStringent(c, existing) {
			byMetric[c.Metric] = c
		}
	}
	out := make([]targetCondition, 0, len(order))
	for _, m := range order {
		out = append(out, byMetric[m])
	}
	return out
}

// moreStringent reports whether candidate fires the gate for strictly more
// cases than incumbent, given they target the same metric. Conditions with
// incomparable Ops are reported as not more stringent so the incumbent
// stays.
func moreStringent(candidate, incumbent targetCondition) bool {
	if candidate.Op != incumbent.Op {
		return false
	}
	c, cOk := parseConditionThreshold(candidate.Error)
	i, iOk := parseConditionThreshold(incumbent.Error)
	if !cOk || !iOk {
		// Non-numeric threshold (rare for SQC QG conditions). Treat as
		// incomparable.
		return false
	}
	switch candidate.Op {
	case "GT", "GTE":
		return c < i
	case "LT", "LTE":
		return c > i
	}
	return false
}

func parseConditionThreshold(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
