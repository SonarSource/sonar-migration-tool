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
)

// TestRunResetGlobalSettings verifies the new task posts /api/settings/reset
// with the keys reported as customized (Inherited=false) by SQC, and
// omits keys that are still at their inherited default.
func TestRunResetGlobalSettings(t *testing.T) {
	var (
		mu          sync.Mutex
		resetKeys   []string
		resetOrg    string
		valuesCalls int
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/settings/values", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		valuesCalls++
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"settings": []map[string]any{
				{"key": "sonar.exclusions", "values": []string{"**/*.gen.java"}, "inherited": false},
				{"key": "sonar.coverage.exclusions", "value": "**/*.test.java", "inherited": false},
				{"key": "sonar.inherited.example", "value": "default-value", "inherited": true},
			},
		})
	})
	mux.HandleFunc("POST /api/settings/reset", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		resetKeys = append(resetKeys, r.FormValue("keys"))
		resetOrg = r.FormValue("organization")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runResetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("runResetGlobalSettings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if valuesCalls != 1 {
		t.Errorf("Settings.Values expected once, got %d", valuesCalls)
	}
	if len(resetKeys) != 1 {
		t.Fatalf("expected 1 Reset call, got %d", len(resetKeys))
	}
	// Inherited setting must not appear; customized ones must be
	// joined with a comma (SonarQube Web API expects a single "keys"
	// parameter, not repeated form values).
	wantKeys := "sonar.exclusions,sonar.coverage.exclusions"
	if resetKeys[0] != wantKeys {
		t.Errorf("Reset keys: got %q want %q", resetKeys[0], wantKeys)
	}
	if resetOrg != testCloudOrg {
		t.Errorf("Reset organization: got %q want %q", resetOrg, testCloudOrg)
	}
}

// TestRunDeleteGatesDestroysOnlyNonBuiltIn verifies that deleteGates
// enumerates every gate in the org via /api/qualitygates/list and
// posts /api/qualitygates/destroy for each gate whose IsBuiltIn flag
// is false (or whose name isn't the canonical "Sonar way"). This is
// the behaviour the rewritten task ships for issue #213 — previously
// it iterated createGates output, so any gate the migration didn't
// create (or any gate created post-migration by an admin) survived
// reset.
func TestRunDeleteGatesDestroysOnlyNonBuiltIn(t *testing.T) {
	var (
		mu          sync.Mutex
		destroyedID []string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualitygates/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"default": "Sonar way",
			"qualitygates": []map[string]any{
				{"id": 1, "name": "Sonar way", "isBuiltIn": true, "isDefault": true},
				{"id": 42, "name": "Custom Gate", "isBuiltIn": false, "isDefault": false},
				{"id": 43, "name": "Admin-added Gate", "isBuiltIn": false, "isDefault": false},
			},
		})
	})
	mux.HandleFunc("POST /api/qualitygates/destroy", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		destroyedID = append(destroyedID, r.FormValue("id"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runDeleteGates(context.Background(), e); err != nil {
		t.Fatalf("runDeleteGates: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"42", "43"}
	if len(destroyedID) != len(want) {
		t.Fatalf("destroyed: got %v (%d) want %v (%d) — built-in must NOT be destroyed",
			destroyedID, len(destroyedID), want, len(want))
	}
	gotSet := map[string]bool{}
	for _, id := range destroyedID {
		gotSet[id] = true
	}
	for _, id := range want {
		if !gotSet[id] {
			t.Errorf("expected destroy id=%q, not seen in %v", id, destroyedID)
		}
	}
	if gotSet["1"] {
		t.Errorf("built-in gate (id=1) must never be destroyed")
	}
}

// TestRunDeleteGatesAcceptsNumericDefaultField pins the exact SQC
// shape that broke production: /api/qualitygates/list returns the
// "default" field as a NUMBER (gate id), not a string. The original
// types.QualityGatesListResponse declared DefaultGate as string and
// json.Unmarshal failed on the whole response, so List returned no
// gates and deleteGates was a silent no-op. Issue #213 follow-up.
func TestRunDeleteGatesAcceptsNumericDefaultField(t *testing.T) {
	destroyed := []string{}
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualitygates/list", func(w http.ResponseWriter, r *http.Request) {
		// Write the raw SonarCloud shape: "default" is a numeric id.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"default": 42,
			"qualitygates": [
				{"id": 1, "name": "Sonar way", "isBuiltIn": true, "isDefault": false},
				{"id": 42, "name": "3 - Corp base", "isBuiltIn": false, "isDefault": true}
			]
		}`))
	})
	mux.HandleFunc("POST /api/qualitygates/destroy", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		destroyed = append(destroyed, r.FormValue("id"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runDeleteGates(context.Background(), e); err != nil {
		t.Fatalf("runDeleteGates: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(destroyed) != 1 || destroyed[0] != "42" {
		t.Fatalf("destroyed: got %v, want [\"42\"] — list response with numeric default must still parse", destroyed)
	}
}

// TestRunDeleteGatesBuiltInByName guards against the SonarCloud
// response that lists a gate named "Sonar way" without setting
// IsBuiltIn=true. The name fallback in isBuiltInGate must keep
// deleteGates from destroying it.
func TestRunDeleteGatesBuiltInByName(t *testing.T) {
	destroyCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualitygates/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"qualitygates": []map[string]any{
				// isBuiltIn omitted/false but name matches.
				{"id": 1, "name": "Sonar way", "isBuiltIn": false, "isDefault": true},
			},
		})
	})
	mux.HandleFunc("POST /api/qualitygates/destroy", func(w http.ResponseWriter, r *http.Request) {
		destroyCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runDeleteGates(context.Background(), e); err != nil {
		t.Fatalf("runDeleteGates: %v", err)
	}
	if destroyCalls != 0 {
		t.Errorf("destroy must NOT be called for a gate named \"Sonar way\"; got %d call(s)", destroyCalls)
	}
}

// TestRunResetDefaultGates verifies that the task finds the built-in
// "Sonar way" gate and posts /api/qualitygates/set_as_default with
// its id, so a subsequent deleteGates call can destroy the previously
// custom default. Regression for issue #213 — the task was a no-op
// before, which left the custom default gate undeletable.
func TestRunResetDefaultGates(t *testing.T) {
	var (
		mu             sync.Mutex
		setDefaultID   string
		setDefaultOrg  string
		setDefaultHits int
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualitygates/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"default": "Custom Gate",
			"qualitygates": []map[string]any{
				{"id": 1, "name": "Sonar way", "isBuiltIn": true, "isDefault": false},
				{"id": 42, "name": "Custom Gate", "isBuiltIn": false, "isDefault": true},
			},
		})
	})
	mux.HandleFunc("POST /api/qualitygates/set_as_default", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		setDefaultID = r.FormValue("id")
		setDefaultOrg = r.FormValue("organization")
		setDefaultHits++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runResetDefaultGates(context.Background(), e); err != nil {
		t.Fatalf("runResetDefaultGates: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if setDefaultHits != 1 {
		t.Fatalf("expected one set_as_default call, got %d", setDefaultHits)
	}
	if setDefaultID != "1" {
		t.Errorf("set_as_default id: got %q want 1 (built-in Sonar way)", setDefaultID)
	}
	if setDefaultOrg != testCloudOrg {
		t.Errorf("set_as_default organization: got %q want %q", setDefaultOrg, testCloudOrg)
	}
}

// TestRunResetDefaultGatesNameFallback exercises the name-based
// fallback in isBuiltInGate. Some SonarCloud responses have shipped
// without the isBuiltIn flag on the Sonar way gate; the task must
// still find it by name so the custom default can be cleared.
func TestRunResetDefaultGatesNameFallback(t *testing.T) {
	setDefaultID := ""
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualitygates/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"default": "Custom Gate",
			"qualitygates": []map[string]any{
				// IsBuiltIn flag missing / false; only the name marks it.
				{"id": 7, "name": "Sonar way", "isBuiltIn": false, "isDefault": false},
				{"id": 99, "name": "Custom Gate", "isBuiltIn": false, "isDefault": true},
			},
		})
	})
	mux.HandleFunc("POST /api/qualitygates/set_as_default", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		setDefaultID = r.FormValue("id")
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runResetDefaultGates(context.Background(), e); err != nil {
		t.Fatalf("runResetDefaultGates: %v", err)
	}
	if setDefaultID != "7" {
		t.Errorf("set_as_default id: got %q want 7 (Sonar way matched by name)", setDefaultID)
	}
}

// TestRunResetDefaultGatesSkipsWhenAlreadyDefault confirms the task
// does NOT call set_as_default when the built-in gate is already the
// org's default — saves a redundant API call.
func TestRunResetDefaultGatesSkipsWhenAlreadyDefault(t *testing.T) {
	setDefaultHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualitygates/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"qualitygates": []map[string]any{
				{"id": 1, "name": "Sonar way", "isBuiltIn": true, "isDefault": true},
			},
		})
	})
	mux.HandleFunc("POST /api/qualitygates/set_as_default", func(w http.ResponseWriter, r *http.Request) {
		setDefaultHits++
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runResetDefaultGates(context.Background(), e); err != nil {
		t.Fatalf("runResetDefaultGates: %v", err)
	}
	if setDefaultHits != 0 {
		t.Errorf("set_as_default must not be called when built-in already default; got %d", setDefaultHits)
	}
}

// TestRunResetDefaultProfilesPerLanguage verifies the task lists the
// org's profiles via /api/qualityprofiles/search, identifies every
// language whose current default is non-built-in, and posts
// /api/qualityprofiles/set_default with the built-in for that
// language. Defaults are per-language on SonarCloud, so the task
// must dispatch one set_default call per language that needs
// restoring. Issue #214.
func TestRunResetDefaultProfilesPerLanguage(t *testing.T) {
	var (
		mu          sync.Mutex
		setDefault  []map[string]string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				// Java — built-in present, custom is current default. Needs restore.
				{"key": "j1", "name": "Sonar way", "language": "java", "isBuiltIn": true, "isDefault": false},
				{"key": "j2", "name": "Custom Java", "language": "java", "isBuiltIn": false, "isDefault": true},
				// Python — built-in already default. No-op for this language.
				{"key": "p1", "name": "Sonar way", "language": "py", "isBuiltIn": true, "isDefault": true},
				{"key": "p2", "name": "Custom Py", "language": "py", "isBuiltIn": false, "isDefault": false},
				// JS — built-in present, custom is current default. Needs restore.
				{"key": "js1", "name": "Sonar way", "language": "js", "isBuiltIn": true, "isDefault": false},
				{"key": "js2", "name": "Custom JS", "language": "js", "isBuiltIn": false, "isDefault": true},
			},
		})
	})
	mux.HandleFunc("POST /api/qualityprofiles/set_default", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		setDefault = append(setDefault, map[string]string{
			"language":       r.FormValue("language"),
			"qualityProfile": r.FormValue("qualityProfile"),
			"organization":   r.FormValue("organization"),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runResetDefaultProfiles(context.Background(), e); err != nil {
		t.Fatalf("runResetDefaultProfiles: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(setDefault) != 2 {
		t.Fatalf("expected 2 set_default calls (java + js), got %d: %v", len(setDefault), setDefault)
	}
	// Build a language -> profile map to assert without depending on call order.
	got := map[string]string{}
	for _, call := range setDefault {
		got[call["language"]] = call["qualityProfile"]
		if call["organization"] != testCloudOrg {
			t.Errorf("set_default organization: got %q want %q", call["organization"], testCloudOrg)
		}
	}
	if got["java"] != "Sonar way" {
		t.Errorf("java default: got %q want \"Sonar way\"", got["java"])
	}
	if got["js"] != "Sonar way" {
		t.Errorf("js default: got %q want \"Sonar way\"", got["js"])
	}
	if _, restored := got["py"]; restored {
		t.Errorf("py must NOT be restored: built-in is already default")
	}
}

// TestRunDeleteProfilesDeletesOnlyNonBuiltIn confirms the rewritten
// deleteProfiles enumerates via search and deletes every non-built-in
// profile (regardless of whether the migration created it), and
// never touches a built-in. Issue #214.
func TestRunDeleteProfilesDeletesOnlyNonBuiltIn(t *testing.T) {
	var (
		mu       sync.Mutex
		deleted  []map[string]string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				{"key": "k1", "name": "Sonar way", "language": "java", "isBuiltIn": true, "isDefault": true},
				{"key": "k2", "name": "Custom Java", "language": "java", "isBuiltIn": false, "isDefault": false},
				{"key": "k3", "name": "Admin Java", "language": "java", "isBuiltIn": false, "isDefault": false},
				{"key": "k4", "name": "Sonar way", "language": "py", "isBuiltIn": true, "isDefault": true},
			},
		})
	})
	mux.HandleFunc("POST /api/qualityprofiles/delete", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		deleted = append(deleted, map[string]string{
			"language":       r.FormValue("language"),
			"qualityProfile": r.FormValue("qualityProfile"),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runDeleteProfiles(context.Background(), e); err != nil {
		t.Fatalf("runDeleteProfiles: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 2 {
		t.Fatalf("expected 2 deletes (Custom Java + Admin Java), got %d: %v", len(deleted), deleted)
	}
	deletedNames := map[string]bool{}
	for _, d := range deleted {
		deletedNames[d["qualityProfile"]] = true
		if d["language"] != "java" {
			t.Errorf("unexpected language %q in delete: %v", d["language"], d)
		}
	}
	if !deletedNames["Custom Java"] || !deletedNames["Admin Java"] {
		t.Errorf("expected Custom Java + Admin Java both deleted, got %v", deletedNames)
	}
	if deletedNames["Sonar way"] {
		t.Error("built-in \"Sonar way\" must never be deleted")
	}
}

// TestRunResetGlobalSettingsAllInherited verifies that when an org has
// no customized settings (everything inherited), the task still
// succeeds and does NOT call POST /api/settings/reset (which would 400
// with an empty keys list).
func TestRunResetGlobalSettingsAllInherited(t *testing.T) {
	resetCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/settings/values", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"settings": []map[string]any{
				{"key": "sonar.foo", "value": "default", "inherited": true},
			},
		})
	})
	mux.HandleFunc("POST /api/settings/reset", func(w http.ResponseWriter, r *http.Request) {
		resetCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runResetGlobalSettings(context.Background(), e); err != nil {
		t.Fatalf("runResetGlobalSettings: %v", err)
	}
	if resetCalls != 0 {
		t.Errorf("Reset should not be called when no customized settings; got %d calls", resetCalls)
	}
}

// TestRunResetPermissionTemplatesPromotesBuiltInForEveryQualifier
// pins the #368 behavior: when the built-in "Default Template" is not
// the current default for some qualifier(s), the task must call
// /api/permissions/set_default_template once per missing qualifier,
// targeting the built-in's templateId.
func TestRunResetPermissionTemplatesPromotesBuiltInForEveryQualifier(t *testing.T) {
	var (
		mu       sync.Mutex
		setCalls []map[string]string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/permissions/search_templates", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"permissionTemplates": []map[string]any{
				{"id": "tpl-default", "name": "Default Template"},
				{"id": "tpl-custom-proj", "name": "Custom Project Template"},
				{"id": "tpl-custom-app", "name": "Mobile Apps"},
			},
			"defaultTemplates": []map[string]any{
				// Built-in is currently default ONLY for portfolios.
				{"templateId": "tpl-custom-proj", "qualifier": "TRK"},
				{"templateId": "tpl-default", "qualifier": "VW"},
				{"templateId": "tpl-custom-app", "qualifier": "APP"},
			},
		})
	})
	mux.HandleFunc("POST /api/permissions/set_default_template", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		setCalls = append(setCalls, map[string]string{
			"templateId":   r.FormValue("templateId"),
			"qualifier":    r.FormValue("qualifier"),
			"organization": r.FormValue("organization"),
		})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runResetPermissionTemplates(context.Background(), e); err != nil {
		t.Fatalf("runResetPermissionTemplates: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Expect exactly two set_default_template calls (TRK + APP); VW is
	// already default and must be skipped to avoid a redundant API call.
	if len(setCalls) != 2 {
		t.Fatalf("expected 2 set_default_template calls (TRK + APP), got %d: %v", len(setCalls), setCalls)
	}
	gotQualifiers := map[string]bool{}
	for _, c := range setCalls {
		if c["templateId"] != "tpl-default" {
			t.Errorf("templateId: got %q, want \"tpl-default\" (the built-in)", c["templateId"])
		}
		if c["organization"] != testCloudOrg {
			t.Errorf("organization: got %q, want %q", c["organization"], testCloudOrg)
		}
		gotQualifiers[c["qualifier"]] = true
	}
	if !gotQualifiers["TRK"] || !gotQualifiers["APP"] {
		t.Errorf("expected qualifiers {TRK, APP}, got %v", gotQualifiers)
	}
	if gotQualifiers["VW"] {
		t.Error("VW must NOT be re-promoted: built-in was already default for it")
	}
}

// TestRunResetPermissionTemplatesSkipsWhenAlreadyDefault confirms the
// task makes ZERO set_default_template calls when the built-in is
// already the current default for every qualifier — guards against
// redundant API noise.
func TestRunResetPermissionTemplatesSkipsWhenAlreadyDefault(t *testing.T) {
	setCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/permissions/search_templates", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"permissionTemplates": []map[string]any{
				{"id": "tpl-default", "name": "Default Template"},
			},
			"defaultTemplates": []map[string]any{
				{"templateId": "tpl-default", "qualifier": "TRK"},
				{"templateId": "tpl-default", "qualifier": "VW"},
				{"templateId": "tpl-default", "qualifier": "APP"},
			},
		})
	})
	mux.HandleFunc("POST /api/permissions/set_default_template", func(w http.ResponseWriter, r *http.Request) {
		setCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runResetPermissionTemplates(context.Background(), e); err != nil {
		t.Fatalf("runResetPermissionTemplates: %v", err)
	}
	if setCalls != 0 {
		t.Errorf("set_default_template must not be called when built-in already default for all qualifiers; got %d", setCalls)
	}
}

// TestRunDeleteTemplatesEnumeratesAndDeletes confirms the rewritten
// deleteTemplates enumerates via search_templates and deletes every
// non-built-in template, regardless of whether the migration created
// it. Issue #368.
func TestRunDeleteTemplatesEnumeratesAndDeletes(t *testing.T) {
	var (
		mu      sync.Mutex
		deleted []string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/permissions/search_templates", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"permissionTemplates": []map[string]any{
				{"id": "tpl-default", "name": "Default Template"},
				{"id": "tpl-1", "name": "DEFAULT"},
				{"id": "tpl-2", "name": "Mobile Apps"},
				// Trimmed + alternate case must still match the built-in.
				{"id": "tpl-default-alt", "name": "  default template  "},
			},
			"defaultTemplates": []map[string]any{
				{"templateId": "tpl-default", "qualifier": "TRK"},
				{"templateId": "tpl-default", "qualifier": "VW"},
				{"templateId": "tpl-default", "qualifier": "APP"},
			},
		})
	})
	mux.HandleFunc("POST /api/permissions/delete_template", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		deleted = append(deleted, r.FormValue("templateId"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newDeleteTest(t, mux)

	if err := runDeleteTemplates(context.Background(), e); err != nil {
		t.Fatalf("runDeleteTemplates: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 2 {
		t.Fatalf("expected 2 deletes (\"DEFAULT\" + \"Mobile Apps\"), got %d: %v", len(deleted), deleted)
	}
	deletedSet := map[string]bool{}
	for _, id := range deleted {
		deletedSet[id] = true
	}
	if !deletedSet["tpl-1"] || !deletedSet["tpl-2"] {
		t.Errorf("expected both \"DEFAULT\" (tpl-1) and \"Mobile Apps\" (tpl-2) deleted, got %v", deletedSet)
	}
	if deletedSet["tpl-default"] || deletedSet["tpl-default-alt"] {
		t.Error("built-in \"Default Template\" must never be deleted (case-insensitive name match)")
	}
}
