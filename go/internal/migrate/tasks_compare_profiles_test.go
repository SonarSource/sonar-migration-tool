// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
)

// TestRunCompareBuiltInProfiles exercises issue #309: the source built-in
// "Sonar way"/java profile has active rules {S1, S2, S3}; the target
// built-in profile on Cloud has {S2, S4}. Expect 1 rule added (S4) and
// 2 rules removed (S1, S3), reported under the source server/profile key.
func TestRunCompareBuiltInProfiles(t *testing.T) {
	dir := t.TempDir()
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)

	writeJSONL(filepath.Join(dir, "extract-01", "getProfiles"), []map[string]any{
		{"key": "srcprof1", "name": "Sonar way", "language": "java", "isBuiltIn": true},
		{"key": "srcprof2", "name": "Custom Java", "language": "java", "isBuiltIn": false},
	})
	writeJSONL(filepath.Join(dir, "extract-01", "getActiveProfileRules"), []map[string]any{
		{"key": "java:S1", "profileKey": "srcprof1"},
		{"key": "java:S2", "profileKey": "srcprof1"},
		{"key": "java:S3", "profileKey": "srcprof1"},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				{"key": "tgtprof1", "name": "Sonar way", "language": "java", "isBuiltIn": true},
			},
		})
	})
	mux.HandleFunc("GET /api/rules/search", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("qprofile"); got != "tgtprof1" {
			t.Errorf("expected qprofile=tgtprof1, got %q", got)
		}
		// SonarQube Cloud's real shape: flat top-level "total", no
		// "paging" object (see TestRunCompareBuiltInProfilesPaginatesTargetRules).
		json.NewEncoder(w).Encode(map[string]any{
			"total": 2,
			"rules": []map[string]any{
				{"key": "java:S2"},
				{"key": "java:S4"},
			},
		})
	})
	addDefaultCloudHandler(mux)
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	e := newTestExecutor(cloudSrv, apiSrv, dir)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"server_url": testServerURL, "sonarcloud_org_key": testCloudOrg},
	})

	if err := runCompareBuiltInProfiles(context.Background(), e); err != nil {
		t.Fatalf("runCompareBuiltInProfiles: %v", err)
	}

	items, err := e.Store.ReadAll("compareBuiltInProfiles")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 diff record (built-in only), got %d: %v", len(items), items)
	}

	var got BuiltInProfileDiff
	if err := json.Unmarshal(items[0], &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != "Sonar way" || got.Language != "java" {
		t.Errorf("unexpected identity: %+v", got)
	}
	if got.RulesAdded != 1 {
		t.Errorf("RulesAdded: got %d want 1", got.RulesAdded)
	}
	if got.RulesRemoved != 2 {
		t.Errorf("RulesRemoved: got %d want 2", got.RulesRemoved)
	}
	if got.ServerURL != testServerURL {
		t.Errorf("ServerURL: got %q want %q", got.ServerURL, testServerURL)
	}
}

// TestRunCompareBuiltInProfilesNoTargetMatch: when the target org has no
// built-in profile for the source's language, no diff record is emitted —
// the report row keeps its default "Built-in, not migrated" text.
func TestRunCompareBuiltInProfilesNoTargetMatch(t *testing.T) {
	dir := t.TempDir()
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)

	writeJSONL(filepath.Join(dir, "extract-01", "getProfiles"), []map[string]any{
		{"key": "srcprof1", "name": "Sonar way", "language": "python", "isBuiltIn": true},
	})
	writeJSONL(filepath.Join(dir, "extract-01", "getActiveProfileRules"), []map[string]any{
		{"key": "python:S1", "profileKey": "srcprof1"},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				{"key": "tgtprof1", "name": "Sonar way", "language": "java", "isBuiltIn": true},
			},
		})
	})
	addDefaultCloudHandler(mux)
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	e := newTestExecutor(cloudSrv, apiSrv, dir)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"server_url": testServerURL, "sonarcloud_org_key": testCloudOrg},
	})

	if err := runCompareBuiltInProfiles(context.Background(), e); err != nil {
		t.Fatalf("runCompareBuiltInProfiles: %v", err)
	}

	items, err := e.Store.ReadAll("compareBuiltInProfiles")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no diff record when no target language match, got %d: %v", len(items), items)
	}
}

// TestRunCompareBuiltInProfilesMultiOrgFanOut reproduces the real-world
// setup that made compareBuiltInProfiles emit nothing: one source SQS
// server bound to several target orgs (GitHub, GitLab, a consolidated
// "others" org) PLUS a trailing organizations.csv row for an unbound ALM
// (no sonarcloud_org_key at all, e.g. an unbound Bitbucket Server). Naively
// keeping the LAST generateOrganizationMappings row per server_url (as
// buildServerOrgLookup does) picks that empty-org row and the whole
// comparison silently no-ops. firstValidOrgPerServer must skip the empty
// row and use one of the bound orgs instead.
func TestRunCompareBuiltInProfilesMultiOrgFanOut(t *testing.T) {
	dir := t.TempDir()
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)

	writeJSONL(filepath.Join(dir, "extract-01", "getProfiles"), []map[string]any{
		{"key": "srcprof1", "name": "Sonar way", "language": "python", "isBuiltIn": true},
	})
	writeJSONL(filepath.Join(dir, "extract-01", "getActiveProfileRules"), []map[string]any{
		{"key": "python:S1", "profileKey": "srcprof1"},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				{"key": "tgtprof1", "name": "Sonar way", "language": "python", "isBuiltIn": true},
			},
		})
	})
	mux.HandleFunc("GET /api/rules/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"total": 2,
			"rules": []map[string]any{
				{"key": "python:S1"},
				{"key": "python:S2"},
			},
		})
	})
	addDefaultCloudHandler(mux)
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	e := newTestExecutor(cloudSrv, apiSrv, dir)
	// Mirrors a real organizations.csv fan-out: several bound orgs from the
	// same source server, then a trailing unbound-ALM row with no target
	// org at all — the exact shape that made buildServerOrgLookup pick "".
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"server_url": testServerURL, "sonarcloud_org_key": "latest-gh"},
		{"server_url": testServerURL, "sonarcloud_org_key": "latest-others"},
		{"server_url": testServerURL, "sonarcloud_org_key": ""},
	})

	if err := runCompareBuiltInProfiles(context.Background(), e); err != nil {
		t.Fatalf("runCompareBuiltInProfiles: %v", err)
	}

	items, err := e.Store.ReadAll("compareBuiltInProfiles")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 diff record despite multi-org fan-out, got %d: %v", len(items), items)
	}
	var got BuiltInProfileDiff
	if err := json.Unmarshal(items[0], &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RulesAdded != 1 {
		t.Errorf("RulesAdded: got %d want 1", got.RulesAdded)
	}
}

// TestRunCompareBuiltInProfilesMultipleBuiltInsSameLanguage reproduces a
// real-world report bug: a platform can ship more than one built-in
// profile for the same language (e.g. "Sonar way" and "Sonar agentic AI",
// both isBuiltIn=true for java). Matching the target profile by language
// alone pairs every source built-in of that language against whichever
// target built-in happens to come first in the search results — here,
// asserting each source profile is diffed against its OWN like-named
// target counterpart, not a shared/wrong one.
func TestRunCompareBuiltInProfilesMultipleBuiltInsSameLanguage(t *testing.T) {
	dir := t.TempDir()
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)

	writeJSONL(filepath.Join(dir, "extract-01", "getProfiles"), []map[string]any{
		{"key": "srcway", "name": "Sonar way", "language": "java", "isBuiltIn": true},
		{"key": "srcai", "name": "Sonar agentic AI", "language": "java", "isBuiltIn": true},
	})
	writeJSONL(filepath.Join(dir, "extract-01", "getActiveProfileRules"), []map[string]any{
		{"key": "java:S1", "profileKey": "srcway"},
		{"key": "java:S2", "profileKey": "srcway"},
		{"key": "java:S3", "profileKey": "srcway"},
		{"key": "java:S1", "profileKey": "srcai"},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				// "Sonar agentic AI" listed FIRST on purpose — a
				// language-only match would wrongly latch onto this one
				// for the "Sonar way" source profile too.
				{"key": "tgtai", "name": "Sonar agentic AI", "language": "java", "isBuiltIn": true},
				{"key": "tgtway", "name": "Sonar way", "language": "java", "isBuiltIn": true},
			},
		})
	})
	mux.HandleFunc("GET /api/rules/search", func(w http.ResponseWriter, r *http.Request) {
		var rules []map[string]any
		switch r.URL.Query().Get("qprofile") {
		case "tgtway":
			rules = []map[string]any{{"key": "java:S1"}, {"key": "java:S2"}, {"key": "java:S4"}}
		case "tgtai":
			rules = []map[string]any{{"key": "java:S1"}}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"total": len(rules),
			"rules": rules,
		})
	})
	addDefaultCloudHandler(mux)
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	e := newTestExecutor(cloudSrv, apiSrv, dir)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"server_url": testServerURL, "sonarcloud_org_key": testCloudOrg},
	})

	if err := runCompareBuiltInProfiles(context.Background(), e); err != nil {
		t.Fatalf("runCompareBuiltInProfiles: %v", err)
	}

	items, err := e.Store.ReadAll("compareBuiltInProfiles")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 diff records, got %d: %v", len(items), items)
	}
	byName := map[string]BuiltInProfileDiff{}
	for _, raw := range items {
		var d BuiltInProfileDiff
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		byName[d.Name] = d
	}
	way := byName["Sonar way"]
	// source {S1,S2,S3} vs target {S1,S2,S4}: 1 added (S4), 1 removed (S3).
	if way.RulesAdded != 1 || way.RulesRemoved != 1 {
		t.Errorf("Sonar way: got added=%d removed=%d, want added=1 removed=1", way.RulesAdded, way.RulesRemoved)
	}
	ai := byName["Sonar agentic AI"]
	// source {S1} vs target {S1}: identical.
	if ai.RulesAdded != 0 || ai.RulesRemoved != 0 {
		t.Errorf("Sonar agentic AI: got added=%d removed=%d, want added=0 removed=0", ai.RulesAdded, ai.RulesRemoved)
	}
}

// TestRunCompareBuiltInProfilesPaginatesTargetRules reproduces the bug
// behind wrong added/removed counts on large built-in profiles (e.g. java
// "Sonar way", 600+ rules): SonarQube Cloud's api/rules/search responds
// with a flat top-level "total" and no "paging" object at all — unlike
// SonarQube Server, which nests it under "paging.total" (the
// PaginatedOpts default). Without TotalKey:"total", ExtractTotal reads 0
// from the Cloud response, GetPaginated computes 0 pages, and silently
// stops after page 1 (capped at ps=500) — undercounting any profile with
// more than 500 active rules. This profile has 3 pages of 500/500/1.
func TestRunCompareBuiltInProfilesPaginatesTargetRules(t *testing.T) {
	dir := t.TempDir()
	writeExtractMetaJSON(t, dir, "extract-01", testServerURL)

	writeJSONL(filepath.Join(dir, "extract-01", "getProfiles"), []map[string]any{
		{"key": "srcprof1", "name": "Sonar way", "language": "java", "isBuiltIn": true},
	})
	// Source has 500 of the target's rules plus one the target dropped.
	var sourceRules []map[string]any
	for i := 0; i < 500; i++ {
		sourceRules = append(sourceRules, map[string]any{"key": fmt.Sprintf("java:S%d", i), "profileKey": "srcprof1"})
	}
	sourceRules = append(sourceRules, map[string]any{"key": "java:REMOVED", "profileKey": "srcprof1"})
	writeJSONL(filepath.Join(dir, "extract-01", "getActiveProfileRules"), sourceRules)

	// Target has the same 500 plus 501 new ones, spread across 3 pages of
	// up to 500 each (1001 rules total) to exercise real multi-page fetch.
	const targetTotal = 1001
	var targetRules []map[string]any
	for i := 0; i < 500; i++ {
		targetRules = append(targetRules, map[string]any{"key": fmt.Sprintf("java:S%d", i)})
	}
	for i := 0; i < targetTotal-500; i++ {
		targetRules = append(targetRules, map[string]any{"key": fmt.Sprintf("java:NEW%d", i)})
	}
	if len(targetRules) != targetTotal {
		t.Fatalf("test setup: built %d target rules, want %d", len(targetRules), targetTotal)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				{"key": "tgtprof1", "name": "Sonar way", "language": "java", "isBuiltIn": true},
			},
		})
	})
	mux.HandleFunc("GET /api/rules/search", func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("p"))
		if page < 1 {
			page = 1
		}
		const pageSize = 500
		start := (page - 1) * pageSize
		end := start + pageSize
		if end > len(targetRules) {
			end = len(targetRules)
		}
		var pageRules []map[string]any
		if start < len(targetRules) {
			pageRules = targetRules[start:end]
		}
		// Real SonarQube Cloud shape: flat "total", NO "paging" wrapper.
		json.NewEncoder(w).Encode(map[string]any{
			"total": targetTotal,
			"p":     page,
			"ps":    pageSize,
			"rules": pageRules,
		})
	})
	addDefaultCloudHandler(mux)
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	e := newTestExecutor(cloudSrv, apiSrv, dir)
	writeTaskJSONL(t, e, "generateOrganizationMappings", []map[string]any{
		{"server_url": testServerURL, "sonarcloud_org_key": testCloudOrg},
	})

	if err := runCompareBuiltInProfiles(context.Background(), e); err != nil {
		t.Fatalf("runCompareBuiltInProfiles: %v", err)
	}

	items, err := e.Store.ReadAll("compareBuiltInProfiles")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 diff record, got %d: %v", len(items), items)
	}
	var got BuiltInProfileDiff
	if err := json.Unmarshal(items[0], &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// source={S0..S499, REMOVED} (501), target={S0..S499, NEW0..NEW500} (1001).
	// added = 501 (NEW0..NEW500), removed = 1 (REMOVED).
	if got.RulesAdded != 501 {
		t.Errorf("RulesAdded: got %d want 501 (pagination truncation would report far fewer)", got.RulesAdded)
	}
	if got.RulesRemoved != 1 {
		t.Errorf("RulesRemoved: got %d want 1", got.RulesRemoved)
	}
}

func TestDiffRuleKeySets(t *testing.T) {
	source := map[string]bool{"a": true, "b": true, "c": true}
	target := map[string]bool{"b": true, "d": true}

	added, removed := diffRuleKeySets(source, target)
	if added != 1 {
		t.Errorf("added: got %d want 1", added)
	}
	if removed != 2 {
		t.Errorf("removed: got %d want 2", removed)
	}
}
