package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		want   []replacementCondition
	}{
		{"unmapped passes through", "coverage", false, nil},
		{"aica suffix dropped — preserves op/error",
			"new_security_rating_with_aica", true,
			[]replacementCondition{{Metric: "new_security_rating"}}},
		{"software_quality_security_rating",
			"software_quality_security_rating", true,
			[]replacementCondition{{Metric: "security_rating"}}},
		{"debt ratio rename",
			"software_quality_maintainability_debt_ratio", true,
			[]replacementCondition{{Metric: "sqale_debt_ratio"}}},
		{"composite — software_quality_blocker_issues with fixed <= D",
			"software_quality_blocker_issues", true,
			[]replacementCondition{
				{Metric: "security_review_rating", Op: "GT", Error: "4"},
				{Metric: "reliability_rating", Op: "GT", Error: "4"},
			}},
		{"composite — software_quality_low_issues with <= A",
			"software_quality_low_issues", true,
			[]replacementCondition{
				{Metric: "security_rating", Op: "GT", Error: "1"},
				{Metric: "reliability_rating", Op: "GT", Error: "1"},
			}},
		{"new_software_quality_blocker_issues — duplicate target per issue table",
			"new_software_quality_blocker_issues", true,
			[]replacementCondition{
				{Metric: "new_security_review_rating", Op: "GT", Error: "4"},
				{Metric: "new_security_review_rating", Op: "GT", Error: "4"},
			}},
		{"new_software_quality_maintainability_rating → new_maintainability_rating",
			"new_software_quality_maintainability_rating", true,
			[]replacementCondition{{Metric: "new_maintainability_rating"}}},
		{"new_software_quality_reliability_issues → new_reliability_rating <= A",
			"new_software_quality_reliability_issues", true,
			[]replacementCondition{{Metric: "new_reliability_rating", Op: "GT", Error: "1"}}},
		{"new_software_quality_security_issues → new_security_rating <= A",
			"new_software_quality_security_issues", true,
			[]replacementCondition{{Metric: "new_security_rating", Op: "GT", Error: "1"}}},
		{"new_software_quality_security_rating_with_aica → new_security_rating <= A",
			"new_software_quality_security_rating_with_aica", true,
			[]replacementCondition{{Metric: "new_security_rating", Op: "GT", Error: "1"}}},
		{"new_software_quality_security_rating_without_aica → new_security_rating <= A",
			"new_software_quality_security_rating_without_aica", true,
			[]replacementCondition{{Metric: "new_security_rating", Op: "GT", Error: "1"}}},
		{"no SQC equivalent → drop",
			"contains_ai_code", true, []replacementCondition{}},
		{"new_software_quality_maintainability_issues → new_issues",
			"new_software_quality_maintainability_issues", true,
			[]replacementCondition{{Metric: "new_issues"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lookupMetricReplacement(tc.input)
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

// TestAddGateConditionsAppliesMetricMapping exercises the end-to-end path:
// source conditions on mapped/unmapped/dropped metrics, asserting the
// CreateCondition payloads (metric + op + threshold) the migrator actually
// sends to SQC.
func TestAddGateConditionsAppliesMetricMapping(t *testing.T) {
	type call struct{ metric, op, errVal string }
	var (
		mu       sync.Mutex
		recorded []call
	)
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/qualitygates/create_condition", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		recorded = append(recorded, call{
			metric: r.FormValue("metric"),
			op:     r.FormValue("op"),
			errVal: r.FormValue("error"),
		})
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(types.QualityGateCondition{ID: 1, Metric: r.FormValue("metric")})
	})
	cloudMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	cloudSrv := httptest.NewServer(cloudMux)
	defer cloudSrv.Close()

	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

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

	// Expected: 1 (coverage, LT 80) + 1 (new_security_rating, inherited GT 1)
	//         + 2 (security_review_rating GT 4, reliability_rating GT 4)
	//         + 0 (contains_ai_code dropped)
	if len(recorded) != 4 {
		t.Fatalf("expected 4 create_condition calls, got %d: %+v", len(recorded), recorded)
	}

	want := map[string]call{
		"coverage":               {metric: "coverage", op: "LT", errVal: "80"},
		"new_security_rating":    {metric: "new_security_rating", op: "GT", errVal: "1"},
		"security_review_rating": {metric: "security_review_rating", op: "GT", errVal: "4"},
		"reliability_rating":     {metric: "reliability_rating", op: "GT", errVal: "4"},
	}

	keys := make([]string, 0, len(recorded))
	for _, c := range recorded {
		keys = append(keys, c.metric)
	}
	sort.Strings(keys)
	wantKeys := []string{"coverage", "new_security_rating", "reliability_rating", "security_review_rating"}
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
