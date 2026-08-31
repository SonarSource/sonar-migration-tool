// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// A customer reported: "we are having all quality gates but not having any
// conditions there" after a transfer.
//
// The clear ran before the replacement set was parsed, so any upstream reason
// for an empty set turned into "delete the target's conditions and leave the
// gate empty". getGateConditions deliberately emits conditions:[] when the
// target gate already existed, and that is the normal case on any re-run, so
// the destructive path was the hot path.
func TestAddGateConditionsNeverClearsIntoAnEmptySet(t *testing.T) {
	var mu sync.Mutex
	var deletes, creates int

	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("GET /api/qualitygates/show", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 77, "name": "Corp base",
			"conditions": []map[string]any{
				{"id": 900, "metric": "coverage", "op": "LT", "error": "80"},
				{"id": 901, "metric": "new_bugs", "op": "GT", "error": "0"},
			},
		})
	})
	cloudMux.HandleFunc("POST /api/qualitygates/delete_condition", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		deletes++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	cloudMux.HandleFunc("POST /api/qualitygates/create_condition", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		creates++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	// A pre-existing target gate whose migrated condition set is empty.
	gw, _ := e.Store.Writer("getGateConditions")
	rec, _ := json.Marshal(map[string]any{
		"gate_name": "Corp base", "sonarcloud_org_key": "org1",
		"cloud_gate_id": "77", "was_preexisting": true,
		"conditions": []map[string]any{},
	})
	if err := gw.WriteOne(rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := runAddGateConditions(context.Background(), e); err != nil {
		t.Fatalf("runAddGateConditions: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if deletes != 0 {
		t.Errorf("the target gate's %d existing conditions were deleted with nothing to put back", deletes)
	}
	if creates != 0 {
		t.Errorf("expected no creates for an empty set, got %d", creates)
	}
}

// The normal override case must still replace the conditions wholesale.
func TestAddGateConditionsStillOverridesWhenThereIsSomethingToWrite(t *testing.T) {
	var mu sync.Mutex
	var deletes, creates int

	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("GET /api/qualitygates/show", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 77, "name": "Corp base",
			"conditions": []map[string]any{{"id": 900, "metric": "coverage", "op": "LT", "error": "80"}},
		})
	})
	cloudMux.HandleFunc("POST /api/qualitygates/delete_condition", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		deletes++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	cloudMux.HandleFunc("POST /api/qualitygates/create_condition", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		creates++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	gw, _ := e.Store.Writer("getGateConditions")
	rec, _ := json.Marshal(map[string]any{
		"gate_name": "Corp base", "sonarcloud_org_key": "org1",
		"cloud_gate_id": "77", "was_preexisting": true,
		"conditions": []map[string]any{
			{"metric": "new_coverage", "op": "LT", "error": "90"},
		},
	})
	if err := gw.WriteOne(rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := runAddGateConditions(context.Background(), e); err != nil {
		t.Fatalf("runAddGateConditions: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if deletes == 0 {
		t.Error("override semantics lost: the pre-existing condition was not cleared")
	}
	if creates == 0 {
		t.Error("the migrated condition was not created")
	}
}

// A project naming a gate that has no target counterpart used to be skipped
// with no log at any level, no counter and no output record — so a run that
// bound ZERO projects looked exactly like a clean one, and the report then
// called the gate "not used by any migrated project".
func TestSetProjectGatesReportsUnresolvedGateName(t *testing.T) {
	var mu sync.Mutex
	var selects int

	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/qualitygates/select", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		selects++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	var buf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// createGates knows about "Migrated gate" but not "Sonar way" — the
	// built-in, which structure deliberately omits from gates.csv while
	// still recording it as the project's gate_name.
	gw, _ := e.Store.Writer("createGates")
	g, _ := json.Marshal(map[string]any{
		"name": "Migrated gate", "sonarcloud_org_key": "org1", "cloud_gate_id": "77",
	})
	_ = gw.WriteOne(g)

	pw, _ := e.Store.Writer("createProjects")
	for _, p := range []map[string]any{
		{"key": "p1", "server_url": testServerURL, "sonarcloud_org_key": "org1",
			"cloud_project_key": "org1_p1", "gate_name": "Sonar way"},
		{"key": "p2", "server_url": testServerURL, "sonarcloud_org_key": "org1",
			"cloud_project_key": "org1_p2", "gate_name": "Sonar way"},
		{"key": "p3", "server_url": testServerURL, "sonarcloud_org_key": "org1",
			"cloud_project_key": "org1_p3", "gate_name": "Migrated gate"},
	} {
		b, _ := json.Marshal(p)
		_ = pw.WriteOne(b)
	}

	if err := runSetProjectGates(context.Background(), e); err != nil {
		t.Fatalf("runSetProjectGates: %v", err)
	}

	mu.Lock()
	got := selects
	mu.Unlock()
	if got != 1 {
		t.Errorf("expected the one resolvable project to be bound, got %d selects", got)
	}

	out := buf.String()
	if !strings.Contains(out, "no target quality gate matches the source gate") {
		t.Errorf("the unresolved gate must be reported, got:\n%s", out)
	}
	if !strings.Contains(out, "Sonar way") {
		t.Errorf("the warning must name the gate, got:\n%s", out)
	}
	// One line per gate name, not one per project.
	if n := strings.Count(out, "no target quality gate matches the source gate"); n != 1 {
		t.Errorf("expected one warning per gate name, got %d for 2 affected projects", n)
	}
	if !strings.Contains(out, "projects_affected=2") {
		t.Errorf("the summary must carry the affected project count, got:\n%s", out)
	}
}
