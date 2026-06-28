// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/sonar-solutions/sq-api-go/types"
)

// TestAddGateConditionsOverrideClearsExisting verifies that when a quality
// gate already existed on SonarQube Cloud (was_preexisting=true on the
// getGateConditions input), addGateConditions wipes the gate's previous
// conditions via /api/qualitygates/delete_condition before applying the
// migrated source set. The end state on SQC matches the source — never a
// union of the two.
func TestAddGateConditionsOverrideClearsExisting(t *testing.T) {
	// Track which condition IDs were deleted and which CreateCondition
	// payloads were sent, so we can assert the override semantics.
	var (
		mu          sync.Mutex
		deletedIDs  []string
		createdCnds []map[string]string
	)

	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("GET /api/qualitygates/show", func(w http.ResponseWriter, r *http.Request) {
		// Pretend the existing target gate has two stale conditions.
		_ = json.NewEncoder(w).Encode(types.QualityGate{
			ID:   42,
			Name: r.URL.Query().Get("name"),
			Conditions: []types.QualityGateCondition{
				{ID: 900, Metric: "old_metric_a", Op: "GT", Error: "1"},
				{ID: 901, Metric: "old_metric_b", Op: "LT", Error: "50"},
			},
		})
	})
	cloudMux.HandleFunc("POST /api/qualitygates/delete_condition", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		deletedIDs = append(deletedIDs, r.FormValue("id"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	cloudMux.HandleFunc("POST /api/qualitygates/create_condition", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		createdCnds = append(createdCnds, map[string]string{
			"metric": r.FormValue("metric"),
			"op":     r.FormValue("op"),
			"error":  r.FormValue("error"),
		})
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(types.QualityGateCondition{ID: 1, Metric: r.FormValue("metric")})
	})
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	// Pre-existing gate "Custom Gate" — was_preexisting=true.
	w, _ := e.Store.Writer("getGateConditions")
	payload := map[string]any{
		"gate_name":          "Custom Gate",
		"sonarcloud_org_key": "cloud-org1",
		"cloud_gate_id":      "42",
		"was_preexisting":    true,
		"conditions": []map[string]any{
			{"metric": "coverage", "op": "LT", "error": "80"},
			{"metric": "new_bugs", "op": "GT", "error": "0"},
		},
	}
	b, _ := json.Marshal(payload)
	w.WriteOne(b)

	if err := runAddGateConditions(context.Background(), e); err != nil {
		t.Fatalf("runAddGateConditions: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Both pre-existing conditions must have been deleted.
	if len(deletedIDs) != 2 {
		t.Fatalf("expected 2 delete_condition calls, got %d (ids=%v)", len(deletedIDs), deletedIDs)
	}
	got := map[string]bool{deletedIDs[0]: true, deletedIDs[1]: true}
	if !got["900"] || !got["901"] {
		t.Errorf("expected delete of condition ids 900 and 901, got %v", deletedIDs)
	}
	// The migrated source conditions must have been added afterwards.
	if len(createdCnds) != 2 {
		t.Fatalf("expected 2 create_condition calls, got %d", len(createdCnds))
	}
	metrics := map[string]bool{createdCnds[0]["metric"]: true, createdCnds[1]["metric"]: true}
	if !metrics["coverage"] || !metrics["new_bugs"] {
		t.Errorf("expected coverage and new_bugs in created conditions, got %v", createdCnds)
	}
}

// TestAddGateConditionsFreshGateSkipsClear verifies that on a freshly
// created gate (was_preexisting=false) addGateConditions does NOT call
// /api/qualitygates/show or /api/qualitygates/delete_condition — there is
// nothing to clear.
func TestAddGateConditionsFreshGateSkipsClear(t *testing.T) {
	var showCalled, deleteCalled bool

	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("GET /api/qualitygates/show", func(w http.ResponseWriter, _ *http.Request) {
		showCalled = true
		w.WriteHeader(http.StatusOK)
	})
	cloudMux.HandleFunc("POST /api/qualitygates/delete_condition", func(w http.ResponseWriter, _ *http.Request) {
		deleteCalled = true
		w.WriteHeader(http.StatusNoContent)
	})
	cloudMux.HandleFunc("POST /api/qualitygates/create_condition", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		_ = json.NewEncoder(w).Encode(types.QualityGateCondition{ID: 1, Metric: r.FormValue("metric")})
	})
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	w, _ := e.Store.Writer("getGateConditions")
	payload := map[string]any{
		"gate_name":          "Fresh Gate",
		"sonarcloud_org_key": "cloud-org1",
		"cloud_gate_id":      "42",
		"was_preexisting":    false,
		"conditions": []map[string]any{
			{"metric": "coverage", "op": "LT", "error": "80"},
		},
	}
	b, _ := json.Marshal(payload)
	w.WriteOne(b)

	if err := runAddGateConditions(context.Background(), e); err != nil {
		t.Fatalf("runAddGateConditions: %v", err)
	}

	if showCalled {
		t.Error("show endpoint must not be called for a freshly created gate")
	}
	if deleteCalled {
		t.Error("delete_condition must not be called for a freshly created gate")
	}
}
