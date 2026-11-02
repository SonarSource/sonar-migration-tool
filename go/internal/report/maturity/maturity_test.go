// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package maturity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/report/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const (
	testServerURL = "https://sq.example.com/"
	testExtractID = "test-extract-01"
	testServerID  = "SQ-TEST"
	testProjectKey = "proj-1"
	errExpectMD   = "expected non-empty markdown"
)

func setupExtract(t *testing.T, tasks map[string][]map[string]any) (string, structure.ExtractMapping) {
	t.Helper()
	dir := t.TempDir()
	extractDir := filepath.Join(dir, testExtractID)
	meta := map[string]any{"url": testServerURL, "version": 10.7, "edition": "enterprise", "run_id": testExtractID}
	metaData, _ := json.Marshal(meta)
	os.MkdirAll(extractDir, 0o755)
	os.WriteFile(filepath.Join(extractDir, "extract.json"), metaData, 0o644)

	for taskName, entries := range tasks {
		taskDir := filepath.Join(extractDir, taskName)
		os.MkdirAll(taskDir, 0o755)
		f, _ := os.Create(filepath.Join(taskDir, "results.1.jsonl"))
		for _, entry := range entries {
			data, _ := json.Marshal(entry)
			f.Write(data)
			f.Write([]byte("\n"))
		}
		f.Close()
	}
	return dir, structure.ExtractMapping{testServerURL: {testExtractID}}
}

func idMap() common.ServerIDMapping {
	return common.ServerIDMapping{testServerURL: testServerID}
}

// --- Coverage ---

func TestGenerateCoverageMarkdown(t *testing.T) {
	measures := common.Measures{
		testServerID: {
			testProjectKey: {
				"lines_to_cover": "100", "uncovered_lines": "20",
				"new_lines_to_cover": "50", "new_uncovered_lines": "10",
			},
		},
	}
	md := GenerateCoverageMarkdown(measures)
	if md == "" {
		t.Error(errExpectMD)
	}
}

func TestCalcPercentage(t *testing.T) {
	if calcPercentage(100, 20) != 80 {
		t.Errorf("got %f", calcPercentage(100, 20))
	}
	if calcPercentage(0, 0) != 0 {
		t.Error("expected 0 for zero total")
	}
}

// --- IDE ---

func TestGenerateIDEMarkdown(t *testing.T) {
	users := map[string][]map[string]any{
		testServerID: {
			{"sonar_lint_connection": "2026-04-15", "is_active_sonar_lint": true},
			{"sonar_lint_connection": "", "is_active_sonar_lint": false},
		},
	}
	md := GenerateIDEMarkdown(users)
	if md == "" {
		t.Error(errExpectMD)
	}
}

// --- Scans ---

func TestGenerateScansMarkdown(t *testing.T) {
	scans := common.ProjectScans{
		testServerID: {
			"GitHub Actions": {
				testProjectKey: {"scan_count_30_days": 5, "failed_scans_30_days": 1},
			},
		},
	}
	md := GenerateScansMarkdown(scans)
	if md == "" {
		t.Error(errExpectMD)
	}
}

// --- Portfolios ---

func TestGeneratePortfolioSummaryMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getPortfolioDetails":  {{"key": "p1", "name": "Port 1", "selectionMode": "MANUAL"}},
		"getPortfolioProjects": {{"portfolioKey": "p1", "refKey": testProjectKey}},
	})
	md := GeneratePortfolioSummaryMarkdown(dir, mapping, idMap())
	if md == "" {
		t.Error(errExpectMD)
	}
}

// --- Profiles ---

func TestGenerateProfileSummary(t *testing.T) {
	profileMap := common.ProfileMap{
		testServerID: {
			"java": {
				"Custom": {"name": "Custom", "is_built_in": false, "is_default": true, "root": "sonar way", "projects": map[string]bool{"p1": true}},
			},
		},
	}
	languages := map[string]map[string]any{"java": {"language": "java"}}
	md := GenerateProfileSummary(profileMap, languages)
	if md == "" {
		t.Error(errExpectMD)
	}
}

func TestInheritsSonarWay(t *testing.T) {
	if !inheritsSonarWay(map[string]any{"root": "Sonar Way", "name": "Custom"}) {
		t.Error("expected true")
	}
	if inheritsSonarWay(map[string]any{"root": "", "name": "Custom"}) {
		t.Error("expected false for empty root")
	}
}

// --- Gates ---

func TestGenerateGateMaturityMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getGates": {
			{"name": "Sonar way", "isBuiltIn": true, "isDefault": true, "caycStatus": "compliant"},
		},
	})
	projects := common.Projects{testServerID: {testProjectKey: {"quality_gate": "Sonar way"}}}
	summary, details := GenerateGateMaturityMarkdown(dir, mapping, idMap(), projects)
	if summary == "" || details == "" {
		t.Error("expected non-empty gate markdown")
	}
}

// --- Languages ---

func TestGenerateLanguageMarkdown(t *testing.T) {
	measures := common.Measures{
		testServerID: {
			testProjectKey: {"ncloc_language_distribution": "java=5000;python=2000"},
		},
	}
	profileMap := common.ProfileMap{}
	md, languages := GenerateLanguageMarkdown(measures, profileMap)
	if md == "" {
		t.Error(errExpectMD)
	}
	if languages["java"] == nil {
		t.Error("expected java in languages")
	}
	if languages["java"]["loc"] != 5000 {
		t.Errorf("java loc: got %v", languages["java"]["loc"])
	}
}

// --- Usage ---

func TestGenerateUsageMarkdown(t *testing.T) {
	projects := common.Projects{
		testServerID: {
			testProjectKey: {"tier": "m", "loc": 15000, "30_day_scans": 10},
		},
	}
	scans := common.ProjectScans{
		testServerID: {
			"CI": {testProjectKey: {"scan_count_30_days": 10}},
		},
	}
	md := GenerateUsageMarkdown(projects, scans)
	if md == "" {
		t.Error(errExpectMD)
	}
}

// --- Issues ---

func TestGenerateIssueMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getProjectIssueTypes": {
			{"projectKey": testProjectKey, "severity": "CRITICAL", "issueType": "BUG", "total": float64(5)},
			{"projectKey": testProjectKey, "severity": "MAJOR", "issueType": "CODE_SMELL", "total": float64(10)},
		},
		"getProjectResolvedIssueTypes": {},
		"getProjectRecentIssueTypes":   {},
	})
	overview, vulns, bugs, smells := GenerateIssueMarkdown(dir, mapping, idMap())
	if overview == "" {
		t.Error("expected non-empty overview")
	}
	if vulns == "" || bugs == "" || smells == "" {
		t.Error("expected non-empty detail sections")
	}
}

// --- Permissions ---

func TestGeneratePermissionsMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getUserPermissions":  {},
		"getGroupPermissions": {},
		"getProfileUsers":     {},
		"getProfileGroups":    {},
		"getGateUsers":        {},
		"getGateGroups":       {},
	})
	md, _ := GeneratePermissionsMarkdown(dir, mapping)
	if md == "" {
		t.Error(errExpectMD)
	}
}

// --- Full Maturity Report ---

func TestGenerateMaturityReport(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getServerInfo":                    {{"System": map[string]any{"Version": "10.7", "Server ID": testServerID, "Edition": "enterprise"}}},
		"getProjectDetails":                {{"key": testProjectKey, "name": "P1", "qualityProfiles": []any{}, "qualityGate": map[string]any{"name": "default"}}},
		"getProjectAnalyses":               {},
		"getProjectBindings":               {},
		"getBindings":                      {},
		"getBranches":                      {},
		"getProjectPullRequests":           {},
		"getDefaultTemplates":              {},
		"getTemplates":                     {},
		"getPlugins":                       {},
		"getRules":                         {},
		"getProfileRules":                  {},
		"getProfiles":                      {},
		"getPortfolioDetails":              {},
		"getPortfolioProjects":             {},
		"getGates":                         {},
		"getApplicationDetails":            {},
		"getUsers":                         {},
		"getProjectSettings":               {},
		"getUsage":                         {},
		"getProjectMeasures":               {},
		"getUserTokens":                    {},
		"getTasks":                         {},
		"getProjectTasks":                  {},
		"getWebhooks":                      {},
		"getProjectWebhooks":               {},
		"getWebhookDeliveries":             {},
		"getProjectWebhookDeliveries":      {},
		"getProjectIssueTypes":             {},
		"getProjectResolvedIssueTypes":     {},
		"getProjectRecentIssueTypes":       {},
		"getUserPermissions":               {},
		"getGroupPermissions":              {},
		"getProfileUsers":                  {},
		"getProfileGroups":                 {},
		"getGateUsers":                     {},
		"getGateGroups":                    {},
		"getGroups":                        {},
	})

	md := GenerateMaturityReport(dir, mapping)
	if !strings.Contains(md, "SonarQube Maturity Assessment") {
		t.Error("missing report title")
	}
	if !strings.Contains(md, "Adoption") {
		t.Error("missing Adoption section")
	}
	if !strings.Contains(md, "Governance") {
		t.Error("missing Governance section")
	}
	if !strings.Contains(md, "Workflow Integration") {
		t.Error("missing Workflow section")
	}
	if !strings.Contains(md, "Automation") {
		t.Error("missing Automation section")
	}
}
