package summary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
					"key":  "midKey",
					"name": "Mid",
					"subViews": []map[string]any{
						{"key": "leafKey", "name": "Leaf"},
					},
				},
			},
		},
		{"key": "soloKey", "name": "Solo"},
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
	partialNames := map[string]bool{}
	for _, item := range portfolios.Partial {
		partialNames[item.Name] = true
	}

	// Top and Mid have subportfolios → Partial.
	if !partialNames["Top"] || !partialNames["Mid"] {
		t.Errorf("expected Top and Mid in Partial, got %v", partialNames)
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

	// Issue text should explain the hierarchy.
	for _, item := range portfolios.Partial {
		if len(item.Issues) == 0 {
			t.Errorf("portfolio %q has no issues attached", item.Name)
			continue
		}
		if !strings.Contains(item.Issues[0], "subportfolios") {
			t.Errorf("portfolio %q: issue does not mention subportfolios: %q", item.Name, item.Issues[0])
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
