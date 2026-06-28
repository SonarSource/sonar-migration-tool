// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/sonar-solutions/sq-api-go/types"
)

func TestLookupMetricReplacement(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		wantOK bool
		want   []ReplacementCondition
	}{
		{"unmapped passes through", "coverage", false, nil},
		{"aica suffix dropped — preserves op/error",
			"new_security_rating_with_aica", true,
			[]ReplacementCondition{{Metric: "new_security_rating"}}},
		{"software_quality_security_rating",
			"software_quality_security_rating", true,
			[]ReplacementCondition{{Metric: "security_rating"}}},
		{"debt ratio rename",
			"software_quality_maintainability_debt_ratio", true,
			[]ReplacementCondition{{Metric: "sqale_debt_ratio"}}},
		{"composite — software_quality_blocker_issues → security_rating + reliability_rating worse than D",
			"software_quality_blocker_issues", true,
			[]ReplacementCondition{
				{Metric: "security_rating", Op: "GT", Error: "4"},
				{Metric: "reliability_rating", Op: "GT", Error: "4"},
			}},
		{"composite — software_quality_low_issues with <= A",
			"software_quality_low_issues", true,
			[]ReplacementCondition{
				{Metric: "security_rating", Op: "GT", Error: "1"},
				{Metric: "reliability_rating", Op: "GT", Error: "1"},
			}},
		{"new_software_quality_blocker_issues → new_security_rating + new_reliability_rating worse than D",
			"new_software_quality_blocker_issues", true,
			[]ReplacementCondition{
				{Metric: "new_security_rating", Op: "GT", Error: "4"},
				{Metric: "new_reliability_rating", Op: "GT", Error: "4"},
			}},
		{"software_quality_reliability_rating → reliability_rating (#232)",
			"software_quality_reliability_rating", true,
			[]ReplacementCondition{{Metric: "reliability_rating"}}},
		{"new_software_quality_maintainability_rating → new_maintainability_rating",
			"new_software_quality_maintainability_rating", true,
			[]ReplacementCondition{{Metric: "new_maintainability_rating"}}},
		{"new_software_quality_reliability_issues → new_reliability_rating <= A",
			"new_software_quality_reliability_issues", true,
			[]ReplacementCondition{{Metric: "new_reliability_rating", Op: "GT", Error: "1"}}},
		{"new_software_quality_security_issues → new_security_rating <= A",
			"new_software_quality_security_issues", true,
			[]ReplacementCondition{{Metric: "new_security_rating", Op: "GT", Error: "1"}}},
		{"new_software_quality_security_rating_with_aica → new_security_rating <= A",
			"new_software_quality_security_rating_with_aica", true,
			[]ReplacementCondition{{Metric: "new_security_rating", Op: "GT", Error: "1"}}},
		{"new_software_quality_security_rating_without_aica → new_security_rating <= A",
			"new_software_quality_security_rating_without_aica", true,
			[]ReplacementCondition{{Metric: "new_security_rating", Op: "GT", Error: "1"}}},
		{"SQS 9.9 wont_fix_issues → SQC accepted_issues (#278)",
			"wont_fix_issues", true,
			[]ReplacementCondition{{Metric: "accepted_issues"}}},
		{"no SQC equivalent → drop",
			"contains_ai_code", true, []ReplacementCondition{}},
		{"new_software_quality_maintainability_issues → new_issues",
			"new_software_quality_maintainability_issues", true,
			[]ReplacementCondition{{Metric: "new_issues"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := LookupMetricReplacement(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v, want %v", ok, tc.wantOK)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("targets length: got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("targets[%d]: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// conditionCall captures a single POST /api/qualitygates/create_condition
// request for assertion.
type conditionCall struct{ metric, op, errVal string }

// mountCreateConditionCapture installs a create_condition handler that records
// every call into the returned slice (guarded by the returned mutex), and
// returns a fresh Executor wired to that mux.
func mountCreateConditionCapture(t *testing.T) (*Executor, *sync.Mutex, *[]conditionCall) {
	t.Helper()
	var (
		mu       sync.Mutex
		recorded []conditionCall
	)
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/qualitygates/create_condition", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		recorded = append(recorded, conditionCall{
			metric: r.FormValue("metric"),
			op:     r.FormValue("op"),
			errVal: r.FormValue("error"),
		})
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(types.QualityGateCondition{ID: 1, Metric: r.FormValue("metric")})
	})
	addDefaultCloudHandler(cloudMux)
	return newCustomCloudTest(t, cloudMux), &mu, &recorded
}

func TestAddGateConditionsAppliesMetricMapping(t *testing.T) {
	e, mu, recPtr := mountCreateConditionCapture(t)

	// Mix of source metrics:
	//  - coverage (unmapped, pass-through, preserves source op/error)
	//  - new_security_rating_with_aica (1:1 rename, preserves source op/error)
	//  - software_quality_blocker_issues (composite: 2 conditions, fixed <= D)
	//  - contains_ai_code (no SQC equivalent → dropped, NO HTTP call)
	w, _ := e.Store.Writer("getGateConditions")
	payload := map[string]any{
		"gate_name":          "Custom",
		"sonarcloud_org_key": "cloud-org1",
		"cloud_gate_id":      "42",
		"was_preexisting":    false,
		"conditions": []map[string]any{
			{"metric": "coverage", "op": "LT", "error": "80"},
			{"metric": "new_security_rating_with_aica", "op": "GT", "error": "1"},
			{"metric": "software_quality_blocker_issues", "op": "GT", "error": "0"},
			{"metric": "contains_ai_code", "op": "GT", "error": "0"},
		},
	}
	b, _ := json.Marshal(payload)
	w.WriteOne(b)

	if err := runAddGateConditions(context.Background(), e); err != nil {
		t.Fatalf("runAddGateConditions: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	recorded := *recPtr

	// Expected: 1 (coverage, LT 80) + 1 (new_security_rating, inherited GT 1)
	//         + 2 (security_rating GT 4, reliability_rating GT 4)
	//         + 0 (contains_ai_code dropped)
	if len(recorded) != 4 {
		t.Fatalf("expected 4 create_condition calls, got %d: %+v", len(recorded), recorded)
	}

	want := map[string]conditionCall{
		"coverage":            {metric: "coverage", op: "LT", errVal: "80"},
		"new_security_rating": {metric: "new_security_rating", op: "GT", errVal: "1"},
		"security_rating":     {metric: "security_rating", op: "GT", errVal: "4"},
		"reliability_rating":  {metric: "reliability_rating", op: "GT", errVal: "4"},
	}

	keys := make([]string, 0, len(recorded))
	for _, c := range recorded {
		keys = append(keys, c.metric)
	}
	sort.Strings(keys)
	wantKeys := []string{"coverage", "new_security_rating", "reliability_rating", "security_rating"}
	for i := range wantKeys {
		if keys[i] != wantKeys[i] {
			t.Fatalf("metrics: got %v, want %v", keys, wantKeys)
		}
	}
	for _, c := range recorded {
		w := want[c.metric]
		if c.op != w.op || c.errVal != w.errVal {
			t.Errorf("%s: got op=%q error=%q, want op=%q error=%q",
				c.metric, c.op, c.errVal, w.op, w.errVal)
		}
		if c.metric == "contains_ai_code" {
			t.Errorf("contains_ai_code should have been dropped, but a CreateCondition call was made: %+v", c)
		}
	}
}

// TestIsObviousMetricRemap covers the suppression list that keeps the
// migration report's "Near Perfect" Issues section free of self-evident
// software_quality_*_rating → *_rating callouts.
func TestIsObviousMetricRemap(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		targets []string
		want    bool
	}{
		{"software_quality_reliability_rating → reliability_rating",
			"software_quality_reliability_rating",
			[]string{"reliability_rating"}, true},
		{"software_quality_security_rating → security_rating",
			"software_quality_security_rating",
			[]string{"security_rating"}, true},
		{"software_quality_maintainability_rating → sqale_rating",
			"software_quality_maintainability_rating",
			[]string{"sqale_rating"}, true},
		{"new_software_quality_reliability_rating → new_reliability_rating",
			"new_software_quality_reliability_rating",
			[]string{"new_reliability_rating"}, true},
		{"new_software_quality_security_rating → new_security_rating",
			"new_software_quality_security_rating",
			[]string{"new_security_rating"}, true},
		{"new_software_quality_maintainability_rating → new_maintainability_rating",
			"new_software_quality_maintainability_rating",
			[]string{"new_maintainability_rating"}, true},
		{"aica suffix drop is NOT obvious enough to suppress",
			"reliability_rating_with_aica",
			[]string{"reliability_rating"}, false},
		{"composite expansion is never obvious",
			"software_quality_blocker_issues",
			[]string{"security_rating", "reliability_rating"}, false},
		{"drop (no targets) is not a remap",
			"contains_ai_code",
			nil, false},
		{"unrelated 1:1 mapping (debt ratio) is not in the suppression list",
			"software_quality_maintainability_debt_ratio",
			[]string{"sqale_debt_ratio"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsObviousMetricRemap(tc.source, tc.targets); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAddGateConditionsCollapsesCollisions covers #234: when a composite
// expansion and a direct passthrough both target the same SQC metric, the
// migrator must POST a single create_condition for that metric, holding the
// more-stringent threshold.
//
// Source mix:
//   - software_quality_blocker_issues > 0  →  security_rating GT 4 + reliability_rating GT 4
//   - reliability_rating worse than B      →  reliability_rating GT 2  (direct, more stringent)
//   - software_quality_medium_issues > 0   →  security_rating GT 2 + reliability_rating GT 2
//
// After collapse:
//   - security_rating GT 2     (medium beats blocker)
//   - reliability_rating GT 2  (direct == medium, ties — first wins; same value)
func TestAddGateConditionsCollapsesCollisions(t *testing.T) {
	e, mu, recPtr := mountCreateConditionCapture(t)

	w, _ := e.Store.Writer("getGateConditions")
	payload := map[string]any{
		"gate_name":          "Custom",
		"sonarcloud_org_key": "cloud-org1",
		"cloud_gate_id":      "42",
		"was_preexisting":    false,
		"conditions": []map[string]any{
			{"metric": "software_quality_blocker_issues", "op": "GT", "error": "0"},
			{"metric": "reliability_rating", "op": "GT", "error": "2"},
			{"metric": "software_quality_medium_issues", "op": "GT", "error": "0"},
		},
	}
	b, _ := json.Marshal(payload)
	w.WriteOne(b)

	if err := runAddGateConditions(context.Background(), e); err != nil {
		t.Fatalf("runAddGateConditions: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	recorded := *recPtr

	if len(recorded) != 2 {
		t.Fatalf("expected 2 create_condition calls after collapse, got %d: %+v", len(recorded), recorded)
	}
	want := map[string]conditionCall{
		"security_rating":    {metric: "security_rating", op: "GT", errVal: "2"},
		"reliability_rating": {metric: "reliability_rating", op: "GT", errVal: "2"},
	}
	for _, c := range recorded {
		w := want[c.metric]
		if w.metric == "" {
			t.Errorf("unexpected create_condition call: %+v", c)
			continue
		}
		if c.op != w.op || c.errVal != w.errVal {
			t.Errorf("%s: got op=%q error=%q, want op=%q error=%q",
				c.metric, c.op, c.errVal, w.op, w.errVal)
		}
	}
}
