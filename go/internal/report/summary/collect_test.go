// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectSummaryEmpty(t *testing.T) {
	dir := t.TempDir()
	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}
	if len(summary.Sections) != len(sectionDefs) {
		t.Errorf("expected %d sections, got %d", len(sectionDefs), len(summary.Sections))
	}
}

func TestCollectSummaryWithData(t *testing.T) {
	dir := t.TempDir()

	// Write createProjects output (successes)
	writeTaskJSONL(t, dir, "createProjects", []map[string]any{
		{"name": "Project A", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_projA"},
		{"name": "Project B", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_projB"},
	})

	// Write generateProjectMappings (includes a skipped item)
	writeTaskJSONL(t, dir, "generateProjectMappings", []map[string]any{
		{"name": "Project A", "sonarcloud_org_key": "org1"},
		{"name": "Project B", "sonarcloud_org_key": "org1"},
		{"name": "Project C", "sonarcloud_org_key": "SKIPPED", "sonarqube_org_key": "old-org"},
	})

	// Write createGates output
	writeTaskJSONL(t, dir, "createGates", []map[string]any{
		{"name": "Custom Gate", "sonarcloud_org_key": "org1", "cloud_gate_id": "gate-1"},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	// Projects section
	projSection := findSection(summary, "Projects")
	if projSection == nil {
		t.Fatal("missing Projects section")
	}
	if len(projSection.Succeeded) != 2 {
		t.Errorf("expected 2 succeeded projects, got %d", len(projSection.Succeeded))
	}
	if len(projSection.Skipped) != 1 {
		t.Errorf("expected 1 skipped project, got %d", len(projSection.Skipped))
	}
	if projSection.Skipped[0].Name != "Project C" {
		t.Errorf("expected skipped project 'Project C', got %q", projSection.Skipped[0].Name)
	}

	// Gates section
	gateSection := findSection(summary, "Quality Gates")
	if gateSection == nil {
		t.Fatal("missing Quality Gates section")
	}
	if len(gateSection.Succeeded) != 1 {
		t.Errorf("expected 1 succeeded gate, got %d", len(gateSection.Succeeded))
	}
}

// TestCollectSummaryDedupesDuplicateCreates is a regression for #165.
// When a single source-side entity exists under multiple SonarQube
// Server orgs that map to the same SonarCloud org, the create* task
// writes one JSONL record per source-org pairing — all referring to
// the same cloud entity. The report previously listed each duplicate
// as its own row and inflated the migrated-count by N-1.
func TestCollectSummaryDedupesDuplicateCreates(t *testing.T) {
	dir := t.TempDir()

	// Same cloud gate, same org, written three times (one per source org).
	writeTaskJSONL(t, dir, "createGates", []map[string]any{
		{"name": "3 - Corp base", "sonarcloud_org_key": "fubar", "cloud_gate_id": "42"},
		{"name": "3 - Corp base", "sonarcloud_org_key": "fubar", "cloud_gate_id": "42"},
		{"name": "3 - Corp base", "sonarcloud_org_key": "fubar", "cloud_gate_id": "42"},
		// A different gate in the same org — must remain.
		{"name": "Custom Gate", "sonarcloud_org_key": "fubar", "cloud_gate_id": "43"},
		// Same gate name in a different org — must remain (distinct cloud entity).
		{"name": "3 - Corp base", "sonarcloud_org_key": "other", "cloud_gate_id": "99"},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	gates := findSection(summary, "Quality Gates")
	if gates == nil {
		t.Fatal("missing Quality Gates section")
	}
	if len(gates.Succeeded) != 3 {
		var got []string
		for _, g := range gates.Succeeded {
			got = append(got, g.Organization+"/"+g.Name+"#"+g.Detail)
		}
		t.Fatalf("expected 3 unique rows after dedup, got %d: %v", len(gates.Succeeded), got)
	}
	// Find the fubar/3 - Corp base row — must appear exactly once.
	found := 0
	for _, g := range gates.Succeeded {
		if g.Organization == "fubar" && g.Name == "3 - Corp base" {
			found++
		}
	}
	if found != 1 {
		t.Errorf("expected exactly one row for fubar/3 - Corp base, got %d", found)
	}
}

func TestCollectSummaryWithScanHistory(t *testing.T) {
	dir := t.TempDir()

	writeTaskJSONL(t, dir, "createProjects", []map[string]any{
		{"name": "Proj1", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj1"},
		{"name": "Proj2", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj2"},
	})

	writeTaskJSONL(t, dir, "importScanHistory", []map[string]any{
		{"cloud_project_key": "org1_proj1", "status": "success", "branch": "main"},
		{"cloud_project_key": "org1_proj2", "status": "failed", "branch": "main", "error": "CE failed"},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	projSection := findSection(summary, "Projects")
	if projSection == nil {
		t.Fatal("missing Projects section")
	}

	// Check scan history is attached
	for _, item := range projSection.Succeeded {
		if item.Name == "Proj1" {
			if item.Detail != "org1_proj1|scan:success" {
				t.Errorf("expected scan:success in detail, got %q", item.Detail)
			}
		}
		if item.Name == "Proj2" {
			if item.Detail != "org1_proj2|scan:failed" {
				t.Errorf("expected scan:failed in detail, got %q", item.Detail)
			}
		}
	}
}

func TestCollectSummaryWithFailures(t *testing.T) {
	dir := t.TempDir()

	// Write a requests.log with a failure entry
	logEntry := map[string]any{
		"process_type": "request_completed",
		"status":       "failure",
		"payload": map[string]any{
			"method": "POST",
			"url":    "/api/projects/create",
			"status": float64(400),
			"data": map[string]any{
				"name":         "FailProj",
				"organization": "org1",
			},
			"response": `{"errors":[{"msg":"already exists"}]}`,
		},
	}
	logBytes, _ := json.Marshal(logEntry)
	os.WriteFile(filepath.Join(dir, "requests.log"), logBytes, 0o644)

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	projSection := findSection(summary, "Projects")
	if len(projSection.Failed) != 1 {
		t.Errorf("expected 1 failed project, got %d", len(projSection.Failed))
	}
	if projSection.Failed[0].Name != "FailProj" {
		t.Errorf("expected 'FailProj', got %q", projSection.Failed[0].Name)
	}
}

func TestExtractRunID(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/app/files/04-27-2026-02", "04-27-2026-02"},
		{"files/run-01", "run-01"},
		{"single", "single"},
	}
	for _, tc := range cases {
		got := extractRunID(tc.path)
		if got != tc.want {
			t.Errorf("extractRunID(%q): got %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestParseScanHistory(t *testing.T) {
	detail, status := parseScanHistory("org1_proj|scan:success")
	if detail != "org1_proj" || status != "success" {
		t.Errorf("got detail=%q status=%q", detail, status)
	}

	detail2, status2 := parseScanHistory("org1_proj")
	if detail2 != "org1_proj" || status2 != "" {
		t.Errorf("no scan: got detail=%q status=%q", detail2, status2)
	}
}

func TestScanStatusLabel(t *testing.T) {
	if scanStatusLabel("success") != "Yes" {
		t.Error("expected Yes")
	}
	if scanStatusLabel("failed") != "Failed" {
		t.Error("expected Failed")
	}
	if scanStatusLabel("skipped") != "No" {
		t.Error("expected No")
	}
	if scanStatusLabel("") != "" {
		t.Error("expected empty")
	}
}

func TestCollectSummaryGateBuiltInAndUnused(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)

	// Extract data: 3 gates — 1 built-in, 1 used, 1 unused.
	extractDir := filepath.Join(dir, "extract-01")
	writeExtractMeta(t, extractDir, "https://sq.example.com")
	writeTaskJSONL(t, extractDir, "getGates", []map[string]any{
		{"name": "Sonar way", "isBuiltIn": true},
		{"name": "Used Gate", "isBuiltIn": false},
		{"name": "Unused Gate", "isBuiltIn": false},
	})

	// Mapping says only "Used Gate" is migrated.
	writeTaskJSONL(t, runDir, "generateGateMappings", []map[string]any{
		{"name": "Used Gate", "sonarcloud_org_key": "org1"},
	})
	writeTaskJSONL(t, runDir, "createGates", []map[string]any{
		{"name": "Used Gate", "sonarcloud_org_key": "org1", "cloud_gate_id": "42"},
	})

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	gates := findSection(summary, "Quality Gates")
	if gates == nil {
		t.Fatal("missing Quality Gates section")
	}
	if len(gates.Succeeded) != 1 || gates.Succeeded[0].Name != "Used Gate" {
		t.Errorf("expected 1 succeeded gate (Used Gate), got %+v", gates.Succeeded)
	}

	counts := map[string]int{}
	for _, item := range gates.Skipped {
		counts[item.SkipReason]++
	}
	if counts[SkipReasonBuiltIn] != 1 {
		t.Errorf("expected 1 built-in skipped, got %d", counts[SkipReasonBuiltIn])
	}
	if counts[SkipReasonUnused] != 1 {
		t.Errorf("expected 1 unused skipped, got %d", counts[SkipReasonUnused])
	}
}

func TestCollectSummaryProfileBuiltInAndUnused(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)

	extractDir := filepath.Join(dir, "extract-01")
	writeExtractMeta(t, extractDir, "https://sq.example.com")
	writeTaskJSONL(t, extractDir, "getProfiles", []map[string]any{
		{"name": "Sonar way", "language": "java", "isBuiltIn": true},
		{"name": "Custom", "language": "java", "isBuiltIn": false},
		{"name": "Unused", "language": "java", "isBuiltIn": false},
		// Same name on different language — independent.
		{"name": "Custom", "language": "js", "isBuiltIn": false},
	})

	writeTaskJSONL(t, runDir, "generateProfileMappings", []map[string]any{
		{"name": "Custom", "language": "java", "sonarcloud_org_key": "org1"},
	})

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	profiles := findSection(summary, "Quality Profiles")
	if profiles == nil {
		t.Fatal("missing Quality Profiles section")
	}
	counts := map[string]int{}
	for _, item := range profiles.Skipped {
		counts[item.SkipReason]++
	}
	if counts[SkipReasonBuiltIn] != 1 {
		t.Errorf("expected 1 built-in profile skipped, got %d (skipped: %+v)", counts[SkipReasonBuiltIn], profiles.Skipped)
	}
	// Custom/js and Unused/java are both unused.
	if counts[SkipReasonUnused] != 2 {
		t.Errorf("expected 2 unused profiles skipped, got %d", counts[SkipReasonUnused])
	}
}

func TestCollectSummaryPartialGateCondition(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)

	// A gate was created successfully.
	writeTaskJSONL(t, runDir, "createGates", []map[string]any{
		{"name": "My Gate", "sonarcloud_org_key": "org1", "cloud_gate_id": "42"},
	})

	// A condition failed for that gate.
	logEntry := map[string]any{
		"process_type": "request_completed",
		"status":       "failure",
		"payload": map[string]any{
			"method": "POST",
			"url":    "/api/qualitygates/create_condition",
			"status": float64(400),
			"data": map[string]any{
				"gateId":       "42",
				"organization": "org1",
				"metric":       "unknown_metric",
				"op":           "GT",
				"error":        "10",
			},
			"response": `{"errors":[{"msg":"metric does not exist"}]}`,
		},
	}
	logBytes, _ := json.Marshal(logEntry)
	os.WriteFile(filepath.Join(runDir, "requests.log"), logBytes, 0o644)

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	gates := findSection(summary, "Quality Gates")
	if len(gates.Partial) != 1 {
		t.Fatalf("expected 1 partial gate entry, got %d", len(gates.Partial))
	}
	item := gates.Partial[0]
	if len(item.Issues) == 0 {
		t.Fatal("expected at least one issue")
	}
	if !strings.Contains(item.Issues[0], "unknown_metric") {
		t.Errorf("expected metric in issue, got %q", item.Issues[0])
	}
	if !strings.Contains(item.Issues[0], "metric does not exist") {
		t.Errorf("expected error message in issue, got %q", item.Issues[0])
	}
}

func TestCollectSummaryPartialProfileChangeParent(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)

	writeTaskJSONL(t, runDir, "createProfiles", []map[string]any{
		{"name": "Child", "language": "java", "sonarcloud_org_key": "org1", "cloud_profile_key": "p1"},
	})

	logEntry := map[string]any{
		"process_type": "request_completed",
		"status":       "failure",
		"payload": map[string]any{
			"method": "POST",
			"url":    "/api/qualityprofiles/change_parent",
			"status": float64(400),
			"data": map[string]any{
				"language":             "java",
				"qualityProfile":       "Child",
				"parentQualityProfile": "Parent",
				"organization":         "org1",
			},
			"response": `{"errors":[{"msg":"parent not found"}]}`,
		},
	}
	logBytes, _ := json.Marshal(logEntry)
	os.WriteFile(filepath.Join(runDir, "requests.log"), logBytes, 0o644)

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	profiles := findSection(summary, "Quality Profiles")
	if len(profiles.Partial) != 1 {
		t.Fatalf("expected 1 partial profile entry, got %d", len(profiles.Partial))
	}
	item := profiles.Partial[0]
	if item.Name != "Child" {
		t.Errorf("expected profile name 'Child', got %q", item.Name)
	}
	if item.Detail != "p1" {
		t.Errorf("expected cloud_profile_key 'p1' to be carried over, got %q", item.Detail)
	}
	if len(item.Issues) == 0 || !strings.Contains(item.Issues[0], "Set parent profile") {
		t.Errorf("expected 'Set parent profile' in issue, got %v", item.Issues)
	}
}

func TestCollectSummaryPortfolioHierarchyPartial(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)

	// Extract describes a top-level portfolio "Top" with one subportfolio
	// "Mid" which itself has one subportfolio "Leaf"; plus a stand-alone
	// portfolio "Solo" with no children.
	extractDir := filepath.Join(dir, "extract-01")
	writeExtractMeta(t, extractDir, "https://sq.example.com")
	writeTaskJSONL(t, extractDir, "getPortfolioDetails", []map[string]any{
		{
			"key":  "topKey",
			"name": "Top",
			"subViews": []map[string]any{
				{
					"key":       "midKey",
					"name":      "Mid",
					"qualifier": "VW",
					"subViews": []map[string]any{
						{"key": "leafKey", "name": "Leaf", "qualifier": "SVW", "selectionMode": "REGEXP", "regexp": ".*"},
					},
				},
			},
		},
		{"key": "soloKey", "name": "Solo"},
	})
	// Empty-portfolio detection requires non-empty getPortfolioProjects
	// entries for portfolios we want to keep out of the Skipped bucket.
	writeTaskJSONL(t, extractDir, "getPortfolioProjects", []map[string]any{
		{"portfolioKey": "topKey", "refKey": "proj-a"},
		{"portfolioKey": "midKey", "refKey": "proj-a"},
		{"portfolioKey": "leafKey", "refKey": "proj-a"},
		{"portfolioKey": "soloKey", "refKey": "proj-a"},
	})

	// createPortfolios JSONL — all four portfolios were created successfully.
	writeTaskJSONL(t, runDir, "createPortfolios", []map[string]any{
		{"name": "Top", "server_url": "https://sq.example.com", "source_portfolio_key": "topKey", "cloud_portfolio_id": "1"},
		{"name": "Mid", "server_url": "https://sq.example.com", "source_portfolio_key": "midKey", "cloud_portfolio_id": "2"},
		{"name": "Leaf", "server_url": "https://sq.example.com", "source_portfolio_key": "leafKey", "cloud_portfolio_id": "3"},
		{"name": "Solo", "server_url": "https://sq.example.com", "source_portfolio_key": "soloKey", "cloud_portfolio_id": "4"},
	})

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	portfolios := findSection(summary, "Portfolios")
	if portfolios == nil {
		t.Fatal("missing Portfolios section")
	}

	succeededNames := map[string]bool{}
	for _, item := range portfolios.Succeeded {
		succeededNames[item.Name] = true
	}
	nearPerfectNames := map[string]bool{}
	for _, item := range portfolios.NearPerfect {
		nearPerfectNames[item.Name] = true
	}
	partialNames := map[string]bool{}
	for _, item := range portfolios.Partial {
		partialNames[item.Name] = true
	}

	// Top has nested depth ≥ 2 (Mid itself has Leaf) → Partial (orange).
	if !partialNames["Top"] {
		t.Errorf("expected Top in Partial, got partials=%v nearPerfect=%v", partialNames, nearPerfectNames)
	}
	// Mid has direct subportfolios with uniform REGEXP mode (Leaf only) →
	// NearPerfect (yellow). Per #229: depth=1 + same mode is the combinable
	// path with perimeter perfectly replicated.
	if !nearPerfectNames["Mid"] {
		t.Errorf("expected Mid in NearPerfect, got %v", nearPerfectNames)
	}
	// Leaf (subportfolio with no children) and Solo (standalone) → Success.
	if !succeededNames["Leaf"] {
		t.Errorf("expected Leaf in Succeeded, got %v", succeededNames)
	}
	if !succeededNames["Solo"] {
		t.Errorf("expected Solo in Succeeded, got %v", succeededNames)
	}
	if succeededNames["Top"] || succeededNames["Mid"] {
		t.Errorf("Top/Mid should not remain in Succeeded, got %v", succeededNames)
	}

	// Issue text should explain why each portfolio left the green bucket.
	for _, item := range portfolios.Partial {
		if len(item.Issues) == 0 {
			t.Errorf("portfolio %q has no issues attached", item.Name)
			continue
		}
		if item.Name == "Top" && !strings.Contains(item.Issues[0], "nested subportfolios depth") {
			t.Errorf("Top: expected nested-depth message, got %q", item.Issues[0])
		}
	}
	for _, item := range portfolios.NearPerfect {
		if item.Name == "Mid" && (len(item.Issues) == 0 || !strings.Contains(item.Issues[0], "uniform selection mode")) {
			t.Errorf("Mid: expected uniform-mode message, got %v", item.Issues)
		}
	}
}

// Portfolios composed of applications (qualifier APP) are migrated as a flat
// list of the projects enclosed in those apps — apps-only routes to
// NearPerfect (yellow) with a distinct Issues line. A portfolio that also
// has a subportfolio with no parseable selection mode (mixed) routes to
// Partial (orange dominates) and carries both messages.
func TestCollectSummaryPortfolioApplicationsClassification(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)

	extractDir := filepath.Join(dir, "extract-01")
	writeExtractMeta(t, extractDir, "https://sq.example.com")
	writeTaskJSONL(t, extractDir, "getPortfolioDetails", []map[string]any{
		{
			"key":  "appsKey",
			"name": "AppsPortfolio",
			"subViews": []map[string]any{
				{"key": "app1", "name": "App1", "qualifier": "APP"},
				{"key": "app2", "name": "App2", "qualifier": "APP"},
			},
		},
		{
			"key":  "mixedKey",
			"name": "MixedPortfolio",
			"subViews": []map[string]any{
				{"key": "vw1", "name": "SubP", "qualifier": "SVW"},
				{"key": "app3", "name": "App3", "qualifier": "APP"},
			},
		},
		{"key": "plainKey", "name": "PlainPortfolio"},
	})
	writeTaskJSONL(t, extractDir, "getPortfolioProjects", []map[string]any{
		{"portfolioKey": "appsKey", "refKey": "proj-a"},
		{"portfolioKey": "mixedKey", "refKey": "proj-a"},
		{"portfolioKey": "plainKey", "refKey": "proj-a"},
	})

	writeTaskJSONL(t, runDir, "createPortfolios", []map[string]any{
		{"name": "AppsPortfolio", "server_url": "https://sq.example.com", "source_portfolio_key": "appsKey", "cloud_portfolio_id": "1"},
		{"name": "MixedPortfolio", "server_url": "https://sq.example.com", "source_portfolio_key": "mixedKey", "cloud_portfolio_id": "2"},
		{"name": "PlainPortfolio", "server_url": "https://sq.example.com", "source_portfolio_key": "plainKey", "cloud_portfolio_id": "3"},
	})

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	portfolios := findSection(summary, "Portfolios")
	if portfolios == nil {
		t.Fatal("missing Portfolios section")
	}

	nearPerfectByName := map[string]EntityItem{}
	for _, it := range portfolios.NearPerfect {
		nearPerfectByName[it.Name] = it
	}
	partialByName := map[string]EntityItem{}
	for _, it := range portfolios.Partial {
		partialByName[it.Name] = it
	}

	apps, ok := nearPerfectByName["AppsPortfolio"]
	if !ok {
		t.Fatalf("AppsPortfolio should be NearPerfect (apps-only is yellow), got partial=%v nearPerfect=%v", partialByName, nearPerfectByName)
	}
	if len(apps.Issues) != 1 || !strings.Contains(apps.Issues[0], "applications") {
		t.Errorf("AppsPortfolio issues: %v", apps.Issues)
	}

	mixed, ok := partialByName["MixedPortfolio"]
	if !ok {
		t.Fatalf("MixedPortfolio should be Partial (subportfolio with no mode + app = mixed-modes orange)")
	}
	if len(mixed.Issues) != 2 {
		t.Errorf("MixedPortfolio should carry both apps + mixed-modes issues, got %v", mixed.Issues)
	}

	for _, it := range portfolios.Succeeded {
		if it.Name == "PlainPortfolio" {
			return
		}
	}
	t.Errorf("PlainPortfolio should remain in Succeeded")
}

// Empty portfolios — those with no resolved projects in the source
// extract — go to Skipped with the standardised message, even if they
// were already created on SonarQube Cloud and landed in Succeeded.
func TestCollectSummaryPortfolioEmpty(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)

	extractDir := filepath.Join(dir, "extract-01")
	writeExtractMeta(t, extractDir, "https://sq.example.com")
	// "NonEmpty" has one resolved project, "Empty" has none.
	writeTaskJSONL(t, extractDir, "getPortfolioProjects", []map[string]any{
		{"portfolioKey": "neKey", "refKey": "proj-a"},
	})
	writeTaskJSONL(t, extractDir, "getPortfolioDetails", []map[string]any{
		{"key": "neKey", "name": "NonEmpty"},
		{"key": "emptyKey", "name": "Empty"},
	})

	writeTaskJSONL(t, runDir, "generatePortfolioMappings", []map[string]any{
		{"name": "NonEmpty", "server_url": "https://sq.example.com", "source_portfolio_key": "neKey"},
		{"name": "Empty", "server_url": "https://sq.example.com", "source_portfolio_key": "emptyKey"},
	})
	writeTaskJSONL(t, runDir, "createPortfolios", []map[string]any{
		{"name": "NonEmpty", "server_url": "https://sq.example.com", "source_portfolio_key": "neKey", "cloud_portfolio_id": "1"},
		// Migrate might still have created an empty portfolio in an
		// older binary; assert the report flips it to Skipped anyway.
		{"name": "Empty", "server_url": "https://sq.example.com", "source_portfolio_key": "emptyKey", "cloud_portfolio_id": "2"},
	})

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	portfolios := findSection(summary, "Portfolios")
	if portfolios == nil {
		t.Fatal("missing Portfolios section")
	}

	for _, it := range portfolios.Succeeded {
		if it.Name == "Empty" {
			t.Errorf("Empty portfolio should not be in Succeeded")
		}
	}
	var emptyRow *EntityItem
	for i := range portfolios.Skipped {
		if portfolios.Skipped[i].Name == "Empty" {
			emptyRow = &portfolios.Skipped[i]
			break
		}
	}
	if emptyRow == nil {
		t.Fatalf("Empty portfolio should be in Skipped, got %+v", portfolios.Skipped)
	}
	if emptyRow.SkipReason != SkipReasonEmpty {
		t.Errorf("expected SkipReason=%q, got %q", SkipReasonEmpty, emptyRow.SkipReason)
	}
	if !strings.Contains(emptyRow.Detail, "empty") {
		t.Errorf("expected message to mention empty, got %q", emptyRow.Detail)
	}
}

func TestCollectSummaryPortfolioPATCHFailureMovesToFailed(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)

	// One portfolio created successfully.
	writeTaskJSONL(t, runDir, "createPortfolios", []map[string]any{
		{"name": "Backend", "server_url": "https://sq.example.com", "source_portfolio_key": "backendKey", "cloud_portfolio_id": "cloud-backend-1"},
	})

	// Its follow-up PATCH (configurePortfolios) failed with a 400.
	logEntry := map[string]any{
		"process_type": "request_completed",
		"status":       "failure",
		"payload": map[string]any{
			"method":   "PATCH",
			"url":      "https://api.sc.example.com/enterprises/portfolios/cloud-backend-1",
			"status":   float64(400),
			"response": `{"errorCode":"BadRequestBody","message":"selection regex requires regularExpression"}`,
		},
	}
	logBytes, _ := json.Marshal(logEntry)
	os.WriteFile(filepath.Join(runDir, "requests.log"), logBytes, 0o644)

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	portfolios := findSection(summary, "Portfolios")
	if portfolios == nil {
		t.Fatal("missing Portfolios section")
	}
	if len(portfolios.Succeeded) != 0 {
		t.Errorf("expected 0 Succeeded portfolios after failure, got %d (names=%v)",
			len(portfolios.Succeeded), portfolioNames(portfolios.Succeeded))
	}
	if len(portfolios.Failed) != 1 {
		t.Fatalf("expected 1 Failed portfolio, got %d", len(portfolios.Failed))
	}
	item := portfolios.Failed[0]
	if item.Name != "Backend" {
		t.Errorf("expected failed name 'Backend', got %q", item.Name)
	}
	if !strings.Contains(item.ErrorMessage, "selection regex requires regularExpression") {
		t.Errorf("expected SQC error in ErrorMessage, got %q", item.ErrorMessage)
	}
}

func portfolioNames(items []EntityItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Name
	}
	return out
}

// TestCollectSummaryQualityGateMappingDedupesAcrossSourceOrgs verifies
// that when the same SQS source gate is migrated into a single SQC gate
// from several source orgs (gates.csv carries one row per source-org
// pairing, all sharing the same cloud_gate_id), the Details column shows
// each mapping decision exactly once — not repeated per source org.
func TestCollectSummaryQualityGateMappingDedupesAcrossSourceOrgs(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)

	writeTaskJSONL(t, runDir, "createGates", []map[string]any{
		{"name": "Shared QG", "sonarcloud_org_key": "org1", "cloud_gate_id": "42"},
	})

	// Three identical sidecar entries for the same (cloud_gate_id,
	// source_metric, target_metric) tuple — emulates addGateConditions
	// recording the same remap from three different source SQS orgs that
	// all migrated into a single target SQC org.
	notesDir := filepath.Join(runDir, "addGateConditions.notes")
	os.MkdirAll(notesDir, 0o755)
	dup := map[string]any{
		"cloud_gate_id": "42",
		"gate_name":     "Shared QG",
		"action":        "remapped",
		"source":        map[string]string{"metric": "new_security_rating_with_aica", "op": "GT", "error": "1"},
		"targets":       []map[string]string{{"metric": "new_security_rating", "op": "GT", "error": "1"}},
	}
	dupDropped := map[string]any{
		"cloud_gate_id": "42",
		"gate_name":     "Shared QG",
		"action":        "dropped",
		"source":        map[string]string{"metric": "contains_ai_code", "op": "GT", "error": "0"},
	}
	f, _ := os.Create(filepath.Join(notesDir, "results.1.jsonl"))
	for i := 0; i < 3; i++ {
		b, _ := json.Marshal(dup)
		f.Write(b)
		f.Write([]byte("\n"))
		b, _ = json.Marshal(dupDropped)
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	gates := findSection(summary, "Quality Gates")
	if len(gates.Partial) != 1 {
		t.Fatalf("expected 1 Partial gate, got %d", len(gates.Partial))
	}
	joined := strings.Join(gates.Partial[0].Issues, "\n")
	// Each remap / dropped condition should appear exactly once, with the
	// full #143-style "metric <= LETTER" notation on both sides for rating
	// conditions.
	if got := strings.Count(joined, "new_security_rating_with_aica <= A --> new_security_rating <= A"); got != 1 {
		t.Errorf("expected remap line exactly once, got %d occurrences in:\n%s", got, joined)
	}
	if got := strings.Count(joined, "contains_ai_code > 0"); got != 1 {
		t.Errorf("expected dropped condition exactly once, got %d occurrences in:\n%s", got, joined)
	}
}

func TestCollectSummaryQualityGateMetricMappingMovesToPartial(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)

	// Two gates created. Only one of them has a sidecar mapping note.
	writeTaskJSONL(t, runDir, "createGates", []map[string]any{
		{"name": "Backend QG", "sonarcloud_org_key": "org1", "cloud_gate_id": "42"},
		{"name": "Frontend QG", "sonarcloud_org_key": "org1", "cloud_gate_id": "99"},
	})

	// addGateConditions.notes — sidecar JSONL recording per-condition
	// remap (#143) + dropped decisions for the Backend QG.
	notesDir := filepath.Join(runDir, "addGateConditions.notes")
	os.MkdirAll(notesDir, 0o755)
	lines := []map[string]any{
		{
			"cloud_gate_id": "42",
			"gate_name":     "Backend QG",
			"action":        "remapped",
			"source":        map[string]string{"metric": "new_security_rating_with_aica", "op": "GT", "error": "1"},
			"targets":       []map[string]string{{"metric": "new_security_rating", "op": "GT", "error": "1"}},
		},
		{
			"cloud_gate_id": "42",
			"gate_name":     "Backend QG",
			"action":        "dropped",
			"source":        map[string]string{"metric": "contains_ai_code", "op": "GT", "error": "0"},
		},
	}
	f, _ := os.Create(filepath.Join(notesDir, "results.1.jsonl"))
	for _, line := range lines {
		b, _ := json.Marshal(line)
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	gates := findSection(summary, "Quality Gates")
	if gates == nil {
		t.Fatal("missing Quality Gates section")
	}

	// Backend QG has a dropped condition, so orange dominates: it lands
	// in Partial, not NearPerfect. Frontend QG stays in Succeeded.
	if len(gates.Succeeded) != 1 || gates.Succeeded[0].Name != "Frontend QG" {
		t.Errorf("expected Frontend QG to remain in Succeeded, got %+v",
			succeededNames(gates.Succeeded))
	}
	if len(gates.NearPerfect) != 0 {
		t.Errorf("expected 0 NearPerfect gates (Backend QG has a drop), got %+v",
			succeededNames(gates.NearPerfect))
	}
	if len(gates.Partial) != 1 {
		t.Fatalf("expected 1 Partial gate, got %d", len(gates.Partial))
	}
	item := gates.Partial[0]
	if item.Name != "Backend QG" {
		t.Errorf("expected Backend QG in Partial, got %q", item.Name)
	}
	joined := strings.Join(item.Issues, " | ")
	if !strings.Contains(joined, "new_security_rating_with_aica <= A --> new_security_rating <= A") {
		t.Errorf("expected remap detail (full condition --> full condition) in Issues, got %q", joined)
	}
	if !strings.Contains(joined, "contains_ai_code > 0") {
		t.Errorf("expected dropped condition in Issues, got %q", joined)
	}
	if strings.Contains(joined, "#143") {
		t.Errorf("did not expect #143 reference in user-facing message, got %q", joined)
	}
	// Each condition entry should be on its own line within an Issues message.
	if !strings.Contains(joined, "\nnew_security_rating_with_aica <=") &&
		!strings.HasPrefix(item.Issues[0], "Some metrics were mapped") {
		t.Errorf("expected newline-separated condition list, got %q", joined)
	}
}

// TestCollectSummaryQualityGateRemapOnlyMovesToNearPerfect covers the #227
// yellow criterion: a quality gate whose conditions were all close-equivalent
// remaps (#143) and had no dropped conditions lands in NearPerfect, not
// Partial.
func TestCollectSummaryQualityGateRemapOnlyMovesToNearPerfect(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	os.MkdirAll(runDir, 0o755)

	writeTaskJSONL(t, runDir, "createGates", []map[string]any{
		{"name": "Ratings QG", "sonarcloud_org_key": "org1", "cloud_gate_id": "77"},
	})

	notesDir := filepath.Join(runDir, "addGateConditions.notes")
	os.MkdirAll(notesDir, 0o755)
	lines := []map[string]any{
		{
			"cloud_gate_id": "77",
			"gate_name":     "Ratings QG",
			"action":        "remapped",
			"source":        map[string]string{"metric": "new_security_rating_with_aica", "op": "GT", "error": "1"},
			"targets":       []map[string]string{{"metric": "new_security_rating", "op": "GT", "error": "1"}},
		},
	}
	f, _ := os.Create(filepath.Join(notesDir, "results.1.jsonl"))
	for _, line := range lines {
		b, _ := json.Marshal(line)
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	gates := findSection(summary, "Quality Gates")
	if gates == nil {
		t.Fatal("missing Quality Gates section")
	}

	if len(gates.Succeeded) != 0 {
		t.Errorf("expected gate to leave Succeeded, got %+v",
			succeededNames(gates.Succeeded))
	}
	if len(gates.Partial) != 0 {
		t.Errorf("expected 0 Partial gates (no drop), got %+v",
			succeededNames(gates.Partial))
	}
	if len(gates.NearPerfect) != 1 || gates.NearPerfect[0].Name != "Ratings QG" {
		t.Fatalf("expected Ratings QG in NearPerfect, got %+v",
			succeededNames(gates.NearPerfect))
	}
	joined := strings.Join(gates.NearPerfect[0].Issues, " | ")
	if !strings.Contains(joined, "new_security_rating_with_aica <= A --> new_security_rating <= A") {
		t.Errorf("expected full-condition remap detail in NearPerfect Issues, got %q", joined)
	}
}

func succeededNames(items []EntityItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

// TestCollectGlobalSettingsRoutesOutcomesByStatus ensures the report
// reads the new outcomes[] schema (one entry per setting × org) and
// routes each outcome to the right Section bucket by Status, with
// Detail forwarded verbatim. The migrate task is responsible for the
// per-row wording — the report no longer composes a single string for
// the whole record.
func TestCollectGlobalSettingsRoutesOutcomesByStatus(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "setGlobalSettings", []map[string]any{
		{
			"key":   "sonar.cleanasyoucode.enabled",
			"value": "true",
			"outcomes": []map[string]any{
				{"org": "orgA", "status": "applied", "detail": "Applied (value=true)"},
				{"org": "orgB", "status": "applied-to-projects",
					"detail": "Applied to all projects (value=true) (failed: orgB_projX)"},
				{"org": "orgC", "status": "skipped", "reason": "not-on-sqc",
					"detail": "Skipped (not on SQC)"},
				{"org": "orgD", "status": "failed", "reason": "boom",
					"detail": "Failed: boom"},
			},
		},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	sec := findSection(summary, "Global Settings")
	if sec == nil {
		t.Fatal("missing Global Settings section")
	}

	if len(sec.Succeeded) != 2 {
		t.Errorf("Succeeded: want 2 (applied + applied-to-projects), got %d", len(sec.Succeeded))
	}
	// Per-row Detail must differ between the direct-apply row and
	// the fan-out row — the whole point of the schema change.
	if sec.Succeeded[0].Detail == sec.Succeeded[1].Detail {
		t.Errorf("Detail must be per-row, both Succeeded rows had %q", sec.Succeeded[0].Detail)
	}
	hasFanOutWording := false
	for _, it := range sec.Succeeded {
		if strings.Contains(it.Detail, "Applied to all projects") {
			hasFanOutWording = true
		}
	}
	if !hasFanOutWording {
		t.Errorf("fan-out row's Detail must say \"Applied to all projects\", got %+v", sec.Succeeded)
	}

	if len(sec.Skipped) != 1 || sec.Skipped[0].Organization != "orgC" {
		t.Fatalf("Skipped: want one entry for orgC, got %+v", sec.Skipped)
	}
	if sec.Skipped[0].SkipReason != "not-on-sqc" {
		t.Errorf("Skipped[0].SkipReason: want 'not-on-sqc', got %q", sec.Skipped[0].SkipReason)
	}
	if sec.Skipped[0].Detail != "Skipped (not on SQC)" {
		t.Errorf("Skipped[0].Detail: want verbatim per-row text, got %q", sec.Skipped[0].Detail)
	}

	if len(sec.Failed) != 1 || sec.Failed[0].Organization != "orgD" {
		t.Fatalf("Failed: want one entry for orgD, got %+v", sec.Failed)
	}
	if sec.Failed[0].ErrorMessage != "boom" {
		t.Errorf("Failed[0].ErrorMessage: want 'boom', got %q", sec.Failed[0].ErrorMessage)
	}
}

// Mixed fan-out outcomes (some projects succeeded, others failed)
// must land in the Section's Partial bucket — not Succeeded — so the
// report distinguishes them from clean applies. Per-row Detail still
// drives the wording.
func TestCollectGlobalSettingsPartialOutcomeLandsInPartialBucket(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "setGlobalSettings", []map[string]any{
		{
			"key":   "sonar.html.file.suffixes",
			"value": "html",
			"outcomes": []map[string]any{
				{"org": "orgA", "status": "partial",
					"detail": "Applied to 1 of 2 projects (values=[.html]) (failed: orgA_projX)"},
			},
		},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	sec := findSection(summary, "Global Settings")
	if sec == nil {
		t.Fatal("missing Global Settings section")
	}
	if len(sec.Succeeded) != 0 {
		t.Errorf("Succeeded must NOT contain partial outcomes, got %+v", sec.Succeeded)
	}
	if len(sec.Partial) != 1 || sec.Partial[0].Organization != "orgA" {
		t.Fatalf("Partial: want one entry for orgA, got %+v", sec.Partial)
	}
	if !strings.Contains(sec.Partial[0].Detail, "Applied to 1 of 2 projects") {
		t.Errorf("Partial[0].Detail: want N/M count text, got %q", sec.Partial[0].Detail)
	}
}

// The Global Settings section must surface setGlobalNewCodePeriod's
// outcomes alongside setGlobalSettings's, so the report documents the
// org-level NCD migration on the same page. Issue #136 reporting
// follow-up.
func TestCollectGlobalSettingsIncludesNewCodePeriod(t *testing.T) {
	dir := t.TempDir()
	// Regular global setting record.
	writeTaskJSONL(t, dir, "setGlobalSettings", []map[string]any{
		{
			"key": "sonar.cleanasyoucode.enabled", "value": "true",
			"outcomes": []map[string]any{
				{"org": "orgA", "status": "applied",
					"detail": "Applied (value=true)"},
			},
		},
	})
	// NCD record from the dedicated task.
	writeTaskJSONL(t, dir, "setGlobalNewCodePeriod", []map[string]any{
		{
			"key": "newCodePeriod", "value": "61",
			"outcomes": []map[string]any{
				{"org": "orgA", "status": "applied",
					"detail": "Applied (defaultLeakPeriodType=days, defaultLeakPeriod=61)"},
				{"org": "orgB", "status": "applied",
					"detail": "Applied (defaultLeakPeriodType=days, defaultLeakPeriod=61)"},
			},
		},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	sec := findSection(summary, "Global Settings")
	if sec == nil {
		t.Fatal("missing Global Settings section")
	}
	// 1 regular setting × 1 org + 1 NCD × 2 orgs = 3 succeeded rows.
	if len(sec.Succeeded) != 3 {
		t.Fatalf("Succeeded: want 3, got %d: %+v", len(sec.Succeeded), sec.Succeeded)
	}
	// Look for a newCodePeriod row to confirm the NCD task's output
	// reached the section.
	foundNCD := false
	for _, it := range sec.Succeeded {
		if it.Name == "newCodePeriod" {
			foundNCD = true
			if !strings.Contains(it.Detail, "defaultLeakPeriodType=days") {
				t.Errorf("NCD Detail must include the type+value, got %q", it.Detail)
			}
		}
	}
	if !foundNCD {
		t.Errorf("Global Settings section is missing the newCodePeriod row, got %+v", sec.Succeeded)
	}
}

// Section-level note rows (Organization="", SkipReason="sqs-only")
// must route to the Skipped bucket so the PDF places them at the
// bottom of the Global Settings section with the "Not applicable on
// SonarQube Cloud" label. Issue #200.
func TestCollectGlobalSettingsRoutesSQSOnlyNotesToSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "setGlobalSettings", []map[string]any{
		// Normal customized setting → succeeded per-org.
		{
			"key": "sonar.exclusions", "values": []string{"a"},
			"outcomes": []map[string]any{
				{"org": "orgA", "status": "applied", "detail": "Applied (values=[a])"},
			},
		},
		// SQS-only note row (one entry, no Organization).
		{
			"key": "sonar.technicalDebt.ratingGrid", "value": "0.03,0.07,0.2,0.5",
			"outcomes": []map[string]any{
				{"org": "", "status": "skipped", "reason": SkipReasonSQSOnly,
					"detail": "Not customizable on SonarQube Cloud — SQS value 0.03,0.07,0.2,0.5 will revert to the platform default 0.05,0.1,0.2,0.5."},
			},
		},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	sec := findSection(summary, "Global Settings")
	if sec == nil {
		t.Fatal("missing Global Settings section")
	}
	if len(sec.Succeeded) != 1 {
		t.Errorf("Succeeded: want 1 (sonar.exclusions/orgA), got %d", len(sec.Succeeded))
	}
	if len(sec.Skipped) != 1 {
		t.Fatalf("Skipped: want one entry for the SQS-only note, got %d: %+v", len(sec.Skipped), sec.Skipped)
	}
	row := sec.Skipped[0]
	if row.SkipReason != SkipReasonSQSOnly {
		t.Errorf("Skipped[0].SkipReason: want SkipReasonSQSOnly, got %q", row.SkipReason)
	}
	if row.Organization != "" {
		t.Errorf("Section-level note must have empty Organization, got %q", row.Organization)
	}
	if !strings.Contains(row.Detail, "revert to the platform default") {
		t.Errorf("Detail must carry the user-facing note text, got %q", row.Detail)
	}
}

// When the SQS instance had applications configured, the summary
// must include a Migration-limitations entry mentioning that
// Applications don't exist on SQC (issue #154). The count must match
// the number of records in the getApplications extract.
func TestCollectSummaryMentionsUnmigratedApplications(t *testing.T) {
	dir := t.TempDir()
	runID := "run-apps"
	runDir := filepath.Join(dir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	extractDir := filepath.Join(dir, "extract-01")
	writeExtractMeta(t, extractDir, "https://sq.example.com")
	writeTaskJSONL(t, extractDir, "getApplications", []map[string]any{
		{"key": "app1", "name": "Application 1"},
		{"key": "app2", "name": "Application 2"},
		{"key": "app3", "name": "Application 3"},
	})

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	if len(summary.Limitations) == 0 {
		t.Fatalf("Limitations: want a non-empty list when SQS had apps, got empty")
	}
	found := false
	for _, msg := range summary.Limitations {
		if strings.Contains(msg, "Applications do not exist on SonarQube Cloud") &&
			strings.Contains(msg, "3 SQS applications") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Limitations must mention applications count, got %v", summary.Limitations)
	}
}

// TestCollectSummaryNCDLimitations is a regression for #134 + #135.
// Seeds a getNewCodePeriods extract with:
//
//   - one inherited:true record → must be ignored (defaults aren't
//     overrides).
//   - one per-branch override (branchKey="feature-x" while the
//     project's main branch is "main") → must count as a per-branch
//     limitation (#134).
//   - two project-main-branch records of type REFERENCE_BRANCH (one
//     for projA, one for projB) → both projects must count as
//     having an unsupported NCD type (#135).
//   - one SPECIFIC_ANALYSIS record on a third project's main branch
//     → counts as an unsupported NCD type project (#135).
//   - one NUMBER_OF_DAYS record on a project's main branch → must
//     NOT count (it's the supported case).
func TestCollectSummaryNCDLimitations(t *testing.T) {
	dir := t.TempDir()
	runID := "run-ncd"
	runDir := filepath.Join(dir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	extractDir := filepath.Join(dir, "extract-01")
	writeExtractMeta(t, extractDir, "https://sq.example.com")
	writeTaskJSONL(t, extractDir, "getNewCodePeriods", []map[string]any{
		// main-branch records carry the project-level NCD. inherited=true
		// here means the branch inherits from the project — NOT that
		// the project is inheriting from somewhere upstream. Both of
		// these are supported (NUMBER_OF_DAYS) so neither counts.
		{"projectKey": "projA", "branchKey": "main", "type": "NUMBER_OF_DAYS", "value": "44", "inherited": true},
		{"projectKey": "projD", "branchKey": "main", "type": "NUMBER_OF_DAYS", "value": "30"},
		// Per-branch override (inherited:false on a non-main branch) → #134.
		{"projectKey": "projA", "branchKey": "feature-x", "type": "NUMBER_OF_DAYS", "value": "7"},
		// Reflected non-main record (branch inherits project setting) —
		// must NOT count toward #134 because it isn't an explicit
		// override.
		{"projectKey": "projD", "branchKey": "feature-y", "type": "NUMBER_OF_DAYS", "value": "30", "inherited": true},
		// Project main-branch unsupported types → #135 (two distinct projects).
		{"projectKey": "projB", "branchKey": "main", "type": "REFERENCE_BRANCH", "value": "main"},
		{"projectKey": "projC", "branchKey": "main", "type": "SPECIFIC_ANALYSIS", "value": "abc123"},
	})

	// createProjects in the run directory carries the main_branch per
	// project — collectLimitations consults it to decide which records
	// are per-branch vs. project-main-branch.
	writeTaskJSONL(t, runDir, "createProjects", []map[string]any{
		{"key": "projA", "server_url": "https://sq.example.com", "main_branch": "main"},
		{"key": "projB", "server_url": "https://sq.example.com", "main_branch": "main"},
		{"key": "projC", "server_url": "https://sq.example.com", "main_branch": "main"},
		{"key": "projD", "server_url": "https://sq.example.com", "main_branch": "main"},
	})

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	var perBranch, unsupportedType string
	for _, msg := range summary.Limitations {
		if strings.Contains(msg, "per-branch new-code-definition") {
			perBranch = msg
		}
		if strings.Contains(msg, "reference_branch or specific_analysis") {
			unsupportedType = msg
		}
	}
	if perBranch == "" {
		t.Fatalf("expected per-branch NCD limitation bullet, got %v", summary.Limitations)
	}
	if !strings.Contains(perBranch, "1 branch-level") {
		t.Errorf("per-branch bullet should mention 1 branch-level entry, got %q", perBranch)
	}
	if unsupportedType == "" {
		t.Fatalf("expected unsupported-type NCD limitation bullet, got %v", summary.Limitations)
	}
	if !strings.Contains(unsupportedType, "2 project(s)") {
		t.Errorf("unsupported-type bullet should mention 2 project(s) (projB, projC), got %q", unsupportedType)
	}
}

// TestCollectSummaryNCDFallbackMovesProjectToPartial is the updated
// regression for issues #135 / #240 — when a project's SQS NCD type
// isn't supported at SQC project scope (REFERENCE_BRANCH or
// SPECIFIC_ANALYSIS), the project now moves from Succeeded into
// Partial with an explanatory Issue describing what was substituted.
func TestCollectSummaryNCDFallbackMovesProjectToPartial(t *testing.T) {
	dir := t.TempDir()
	runID := "run-ncd-fallback"
	runDir := filepath.Join(dir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeExtractMeta(t, filepath.Join(dir, "extract-01"), "https://sq.example.com")

	writeTaskJSONL(t, runDir, "createProjects", []map[string]any{
		{"name": "Project A", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_projA"},
		{"name": "Project B", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_projB"},
	})
	writeTaskJSONL(t, runDir, "setNewCodePeriods", []map[string]any{
		// projA had REFERENCE_BRANCH on SQS — fell back to org default,
		// marker written for the report.
		{"projectKey": "projA", "cloud_project_key": "org1_projA", "type": "REFERENCE_BRANCH", "source_ncd_type": "REFERENCE_BRANCH", "ncd_fallback": true},
		// projB had NUMBER_OF_DAYS — applied normally, no marker.
		{"projectKey": "projB", "cloud_project_key": "org1_projB", "type": "NUMBER_OF_DAYS", "value": "30"},
	})

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	projSection := findSection(summary, "Projects")
	if projSection == nil {
		t.Fatal("missing Projects section")
	}

	// Project A → Partial with the explanatory Issue.
	var sawA bool
	for _, p := range projSection.Partial {
		if p.Name != "Project A" {
			continue
		}
		sawA = true
		joined := strings.Join(p.Issues, " | ")
		if !strings.Contains(joined, "reference branch") {
			t.Errorf("Project A Partial Issues must mention the substituted NCD type, got %q", joined)
		}
		if !strings.Contains(joined, "replaced by the org default") {
			t.Errorf("Project A Partial Issues must mention org-default substitution, got %q", joined)
		}
	}
	if !sawA {
		t.Errorf("Project A must appear in Partial, got Partial=%v", projSection.Partial)
	}

	// Project B (supported NCD type) → stays in Succeeded.
	var sawB bool
	for _, p := range projSection.Succeeded {
		if p.Name == "Project B" {
			sawB = true
		}
		if p.Name == "Project A" {
			t.Errorf("Project A must NOT remain in Succeeded after Partial move")
		}
	}
	if !sawB {
		t.Errorf("Project B must remain in Succeeded, got Succeeded=%v", projSection.Succeeded)
	}
}

// When the SQS instance had no applications, the limitations list
// must NOT include the applications entry — otherwise every report
// would carry an irrelevant note.
func TestCollectSummaryNoApplicationsLimitation(t *testing.T) {
	dir := t.TempDir()
	runID := "run-no-apps"
	runDir := filepath.Join(dir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	extractDir := filepath.Join(dir, "extract-01")
	writeExtractMeta(t, extractDir, "https://sq.example.com")
	// getApplications dir exists but is empty.
	writeTaskJSONL(t, extractDir, "getApplications", nil)

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	for _, msg := range summary.Limitations {
		if strings.Contains(msg, "Applications") {
			t.Errorf("Limitations must NOT mention applications when none extracted, got %q", msg)
		}
	}
}

// --- helpers ---

func findSection(s *MigrationSummary, name string) *Section {
	for i := range s.Sections {
		if s.Sections[i].Name == name {
			return &s.Sections[i]
		}
	}
	return nil
}

func writeExtractMeta(t *testing.T, extractDir, url string) {
	t.Helper()
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir extract: %v", err)
	}
	b, _ := json.Marshal(map[string]any{"url": url})
	if err := os.WriteFile(filepath.Join(extractDir, "extract.json"), b, 0o644); err != nil {
		t.Fatalf("write extract.json: %v", err)
	}
}

func writeTaskJSONL(t *testing.T, dir, taskName string, items []map[string]any) {
	t.Helper()
	taskDir := filepath.Join(dir, taskName)
	os.MkdirAll(taskDir, 0o755)
	f, err := os.Create(filepath.Join(taskDir, "results.1.jsonl"))
	if err != nil {
		t.Fatalf("create %s: %v", taskName, err)
	}
	defer f.Close()
	for _, item := range items {
		b, _ := json.Marshal(item)
		f.Write(b)
		f.Write([]byte("\n"))
	}
}

// writeRunMeta marshals the run_meta.json object (shared contract A) into
// runDir with the two-space indentation the migrate engine uses.
func writeRunMeta(t *testing.T, runDir string, meta map[string]any) {
	t.Helper()
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("marshal run_meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "run_meta.json"), b, 0o644); err != nil {
		t.Fatalf("write run_meta.json: %v", err)
	}
}

// writeRunEvents writes one JSON object per line to run_events.jsonl
// (shared contract A) in runDir.
func writeRunEvents(t *testing.T, runDir string, events []map[string]any) {
	t.Helper()
	f, err := os.Create(filepath.Join(runDir, "run_events.jsonl"))
	if err != nil {
		t.Fatalf("create run_events.jsonl: %v", err)
	}
	defer f.Close()
	for _, ev := range events {
		b, _ := json.Marshal(ev)
		f.Write(b)
		f.Write([]byte("\n"))
	}
}

// writeRequestsLog writes one request_completed JSON object per line to
// requests.log in runDir, matching the shape parseLogFile expects.
func writeRequestsLog(t *testing.T, runDir string, entries []map[string]any) {
	t.Helper()
	f, err := os.Create(filepath.Join(runDir, "requests.log"))
	if err != nil {
		t.Fatalf("create requests.log: %v", err)
	}
	defer f.Close()
	for _, e := range entries {
		b, _ := json.Marshal(e)
		f.Write(b)
		f.Write([]byte("\n"))
	}
}

// TestCollectSummaryRuntimeTelemetry seeds a run directory with the full set
// of migrate-engine telemetry files (run_meta.json + run_events.jsonl +
// requests.log) and asserts CollectSummary harvests every runtime field:
// OverallStatus, Phases (slowest-first), Tasks, Failures, the retry and
// branch-skip ledgers, and the per-branch Branches list.
func TestCollectSummaryRuntimeTelemetry(t *testing.T) {
	runDir := t.TempDir()

	// run_meta.json: two phases (phase 1 is slower so it must sort first),
	// three tasks one of which failed.
	writeRunMeta(t, runDir, map[string]any{
		"started_at":     "2026-06-05T12:00:00Z",
		"completed_at":   "2026-06-05T12:01:30Z",
		"overall_status": "partial",
		"phases": []map[string]any{
			{"index": 0, "tasks": 2, "duration_seconds": 10.0},
			{"index": 1, "tasks": 3, "duration_seconds": 80.0},
		},
		"tasks": []map[string]any{
			{"phase": 0, "name": "createProjects", "duration_seconds": 8.0, "ok": true},
			{"phase": 1, "name": "importScanHistory", "duration_seconds": 70.0, "ok": false, "err": "CE task failed"},
			{"phase": 1, "name": "addGateConditions", "duration_seconds": 5.0, "ok": true},
		},
	})

	// run_events.jsonl: a retry, a packaged report, a branch skip, and a
	// CE-task submission (one event per recognised message string).
	writeRunEvents(t, runDir, []map[string]any{
		{
			"time": "2026-06-05T12:00:05Z", "level": "WARN", "message": "retrying request",
			"attrs": map[string]any{"method": "POST", "endpoint": "/api/ce/submit",
				"status": "503", "attempt": 2, "maxAttempts": 5},
		},
		{
			"time": "2026-06-05T12:00:20Z", "level": "INFO", "message": "report packaged",
			"attrs": map[string]any{"project": "projA", "sourceBranch": "main", "targetBranch": "main",
				"projectVersion": "1.0", "zipSizeBytes": 2048, "components": 30, "issues": 100,
				"externalIssues": 4, "sources": 25, "activeRules": 250},
		},
		{
			"time": "2026-06-05T12:00:40Z", "level": "WARN",
			"message": "skipping branch: source code not retrievable",
			"attrs":   map[string]any{"project": "projA", "branch": "feature-x", "findings": 12},
		},
		{
			"time": "2026-06-05T12:00:55Z", "level": "INFO", "message": "CE task submitted",
			"attrs": map[string]any{"project": "projA", "targetBranch": "main", "taskId": "AY-task-1"},
		},
	})

	// requests.log: one POST failure row.
	writeRequestsLog(t, runDir, []map[string]any{
		{
			"process_type": "request_completed",
			"status":       "failure",
			"payload": map[string]any{
				"method": "POST",
				"url":    "/api/projects/create",
				"status": float64(400),
				"data":   map[string]any{"name": "FailProj", "organization": "org1"},
				"response": `{"errors":[{"msg":"already exists"}]}`,
			},
		},
	})

	summary, err := CollectSummary(runDir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	assertRuntimeMeta(t, summary)
	assertRuntimeFailures(t, summary)
	assertRuntimeWarnings(t, summary)
	assertRuntimeBranches(t, summary)
}

// assertRuntimeMeta checks the run-level status, elapsed time, and the
// slowest-first ordering of Phases and Tasks parsed from run_meta.json.
func assertRuntimeMeta(t *testing.T, summary *MigrationSummary) {
	t.Helper()
	if summary.OverallStatus != "partial" {
		t.Errorf("OverallStatus: want %q, got %q", "partial", summary.OverallStatus)
	}
	if summary.TotalElapsed != 90*time.Second {
		t.Errorf("TotalElapsed: want 90s, got %s", summary.TotalElapsed)
	}

	// Phases: slowest-first. Phase 1 (80s) must precede Phase 0 (10s).
	if len(summary.Phases) != 2 {
		t.Fatalf("Phases: want 2, got %d", len(summary.Phases))
	}
	if summary.Phases[0].Phase != "Phase 1" || summary.Phases[0].Duration < summary.Phases[1].Duration {
		t.Errorf("Phases must be slowest-first ('Phase 1' leads), got %+v", summary.Phases)
	}

	// Tasks: slowest-first; importScanHistory (70s, ok=false) leads.
	if len(summary.Tasks) != 3 {
		t.Fatalf("Tasks: want 3, got %d", len(summary.Tasks))
	}
	if summary.Tasks[0].Task != "importScanHistory" {
		t.Errorf("Tasks must be slowest-first; want 'importScanHistory' first, got %q", summary.Tasks[0].Task)
	}
	if summary.Tasks[0].OK {
		t.Errorf("importScanHistory task should carry OK=false")
	}
}

// assertRuntimeFailures checks the single requests.log failure row surfaced
// in summary.Failures.
func assertRuntimeFailures(t *testing.T, summary *MigrationSummary) {
	t.Helper()
	if len(summary.Failures) != 1 {
		t.Fatalf("Failures: want 1, got %d (%+v)", len(summary.Failures), summary.Failures)
	}
	fr := summary.Failures[0]
	if fr.EntityType != "Project" || fr.EntityName != "FailProj" {
		t.Errorf("Failure row: want Project/FailProj, got %s/%s", fr.EntityType, fr.EntityName)
	}
	if !strings.Contains(fr.ErrorMessage, "already exists") {
		t.Errorf("Failure ErrorMessage: want 'already exists', got %q", fr.ErrorMessage)
	}
}

// assertRuntimeWarnings checks the retry and branch-skip ledgers built from
// run_events.jsonl.
func assertRuntimeWarnings(t *testing.T, summary *MigrationSummary) {
	t.Helper()
	if len(summary.Warnings.Retries) != 1 {
		t.Fatalf("Warnings.Retries: want 1, got %d", len(summary.Warnings.Retries))
	}
	retry := summary.Warnings.Retries[0]
	if retry.Method != "POST" || retry.Endpoint != "/api/ce/submit" {
		t.Errorf("Retry row: want POST /api/ce/submit, got %s %s", retry.Method, retry.Endpoint)
	}
	if retry.MaxAttempt != 2 || retry.LastStatus != "503" {
		t.Errorf("Retry row: want maxAttempt=2 lastStatus=503, got %d %q", retry.MaxAttempt, retry.LastStatus)
	}

	if len(summary.Warnings.BranchSkips) != 1 {
		t.Fatalf("Warnings.BranchSkips: want 1, got %d", len(summary.Warnings.BranchSkips))
	}
	skip := summary.Warnings.BranchSkips[0]
	if skip.Branch != "feature-x" || skip.Findings != 12 {
		t.Errorf("BranchSkip: want feature-x/12, got %s/%d", skip.Branch, skip.Findings)
	}
}

// assertRuntimeBranches checks the per-branch Branches list: feature-x
// (skipped) and main (packaged, with the CE task id).
func assertRuntimeBranches(t *testing.T, summary *MigrationSummary) {
	t.Helper()
	if len(summary.Branches) != 2 {
		t.Fatalf("Branches: want 2, got %d (%+v)", len(summary.Branches), summary.Branches)
	}
	byName := map[string]BranchStat{}
	for _, b := range summary.Branches {
		byName[b.Branch] = b
	}
	if feature := byName["feature-x"]; feature.Status != "skipped" {
		t.Errorf("feature-x branch: want status 'skipped', got %+v", feature)
	}
	main, ok := byName["main"]
	if !ok {
		t.Fatalf("main branch missing from Branches: %+v", summary.Branches)
	}
	if main.Status != "packaged" {
		t.Errorf("main branch: want status 'packaged' (report packaged wins over submitted), got %q", main.Status)
	}
	if main.Issues != 100 || main.TaskID != "AY-task-1" {
		t.Errorf("main branch: want issues=100 taskId=AY-task-1, got issues=%d taskId=%q", main.Issues, main.TaskID)
	}
}

// TestCollectSummaryRuntimeAbsent is the predictive-safety guard: when the run
// directory has NO run_meta.json / run_events.jsonl / requests.log,
// CollectSummary must still return a non-nil summary with zero-valued runtime
// fields (no panic, no error) so the predictive pipeline degrades cleanly.
func TestCollectSummaryRuntimeAbsent(t *testing.T) {
	runDir := t.TempDir()

	summary, err := CollectSummary(runDir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	if summary == nil {
		t.Fatal("expected non-nil summary even with no telemetry files")
	}

	if summary.OverallStatus != "" {
		t.Errorf("OverallStatus: want empty, got %q", summary.OverallStatus)
	}
	if !summary.StartedAt.IsZero() || !summary.CompletedAt.IsZero() {
		t.Errorf("timestamps must be zero with no run_meta.json, got start=%v end=%v",
			summary.StartedAt, summary.CompletedAt)
	}
	if summary.TotalElapsed != 0 {
		t.Errorf("TotalElapsed: want 0, got %s", summary.TotalElapsed)
	}
	if len(summary.Phases) != 0 || len(summary.Tasks) != 0 {
		t.Errorf("Phases/Tasks must be empty, got %d/%d", len(summary.Phases), len(summary.Tasks))
	}
	if len(summary.Failures) != 0 {
		t.Errorf("Failures: want 0, got %d", len(summary.Failures))
	}
	if len(summary.Branches) != 0 {
		t.Errorf("Branches: want 0, got %d", len(summary.Branches))
	}
	if len(summary.Warnings.Retries) != 0 || len(summary.Warnings.BranchSkips) != 0 ||
		len(summary.Warnings.GateConditions) != 0 || len(summary.Warnings.MetricRemaps) != 0 {
		t.Errorf("WarningLedger must be empty, got %+v", summary.Warnings)
	}
	if summary.Throughput != (ThroughputStats{}) {
		t.Errorf("Throughput must be zero-valued, got %+v", summary.Throughput)
	}
}
