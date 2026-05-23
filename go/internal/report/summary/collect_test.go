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
		"cloud_gate_id":  "42",
		"gate_name":      "Shared QG",
		"action":         "remapped",
		"source_metric":  "new_security_rating_with_aica",
		"target_metrics": []string{"new_security_rating"},
	}
	dupDropped := map[string]any{
		"cloud_gate_id": "42",
		"gate_name":     "Shared QG",
		"action":        "dropped",
		"source_metric": "contains_ai_code",
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
	// Each remap/dropped metric should appear exactly once.
	if got := strings.Count(joined, "new_security_rating_with_aica --> new_security_rating"); got != 1 {
		t.Errorf("expected remap line exactly once, got %d occurrences in:\n%s", got, joined)
	}
	if got := strings.Count(joined, "contains_ai_code"); got != 1 {
		t.Errorf("expected dropped metric exactly once, got %d occurrences in:\n%s", got, joined)
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
			"cloud_gate_id":  "42",
			"gate_name":      "Backend QG",
			"action":         "remapped",
			"source_metric":  "new_security_rating_with_aica",
			"target_metrics": []string{"new_security_rating"},
		},
		{
			"cloud_gate_id": "42",
			"gate_name":     "Backend QG",
			"action":        "dropped",
			"source_metric": "contains_ai_code",
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

	// Backend QG should be moved to Partial; Frontend QG should remain
	// in Succeeded untouched.
	if len(gates.Succeeded) != 1 || gates.Succeeded[0].Name != "Frontend QG" {
		t.Errorf("expected Frontend QG to remain in Succeeded, got %+v",
			succeededNames(gates.Succeeded))
	}
	if len(gates.Partial) != 1 {
		t.Fatalf("expected 1 Partial gate, got %d", len(gates.Partial))
	}
	item := gates.Partial[0]
	if item.Name != "Backend QG" {
		t.Errorf("expected Backend QG in Partial, got %q", item.Name)
	}
	joined := strings.Join(item.Issues, " | ")
	if !strings.Contains(joined, "new_security_rating_with_aica --> new_security_rating") {
		t.Errorf("expected remap detail (with -->) in Issues, got %q", joined)
	}
	if !strings.Contains(joined, "contains_ai_code") {
		t.Errorf("expected dropped metric in Issues, got %q", joined)
	}
	if strings.Contains(joined, "#143") {
		t.Errorf("did not expect #143 reference in user-facing message, got %q", joined)
	}
	// Each metric entry should be on its own line within an Issues message.
	if !strings.Contains(joined, "\nnew_security_rating_with_aica -->") &&
		!strings.HasPrefix(item.Issues[0], "Some metrics were mapped") {
		t.Errorf("expected newline-separated metric list, got %q", joined)
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
