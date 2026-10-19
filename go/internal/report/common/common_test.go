// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const (
	testServerURL = "https://sq.example.com/"
	testServerID  = "SQ-TEST"
	testExtractID = "test-extract-01"
	testProj1     = "proj-1"
	testProj2     = "proj-2"
	testPort1     = "port-1"
	errNameGotV      = "name: got %v"
	errExpectMD      = "expected non-empty markdown"
	testDate         = "2026-04-15T10:00:00+0000"
	testGHActions    = "GitHub Actions"
	testSonarWay     = "Sonar way"
	testHookURL      = "https://hook.example.com"
	testProfile1     = "prof-1"
)

// setupExtract creates a minimal extract directory with a given task's JSONL data.
func setupExtract(t *testing.T, tasks map[string][]map[string]any) (string, structure.ExtractMapping) {
	t.Helper()
	dir := t.TempDir()
	extractDir := filepath.Join(dir, testExtractID)

	// Write extract.json metadata.
	meta := map[string]any{"url": testServerURL, "version": 10.7, "edition": "enterprise", "run_id": testExtractID}
	metaData, _ := json.Marshal(meta)
	os.MkdirAll(extractDir, 0o755)
	os.WriteFile(filepath.Join(extractDir, "extract.json"), metaData, 0o644)

	// Write JSONL files for each task.
	for taskName, entries := range tasks {
		taskDir := filepath.Join(extractDir, taskName)
		os.MkdirAll(taskDir, 0o755)
		writeJSONL(t, filepath.Join(taskDir, "results.1.jsonl"), entries)
	}

	mapping := structure.ExtractMapping{testServerURL: testExtractID}
	return dir, mapping
}

func writeJSONL(t *testing.T, path string, entries []map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	for _, entry := range entries {
		data, _ := json.Marshal(entry)
		f.Write(data)
		f.Write([]byte("\n"))
	}
}

func idMap() ServerIDMapping {
	return ServerIDMapping{testServerURL: testServerID}
}

// --- Groups ---

func TestProcessGroups(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getGroups": {
			{"name": "developers", "permissions": []string{"admin"}, "managed": true},
			{"name": "viewers", "permissions": []string{"browse"}, "managed": false},
		},
	})
	groups := ProcessGroups(dir, mapping, idMap())
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if groups[0]["name"] != "developers" {
		t.Errorf(errNameGotV, groups[0]["name"])
	}
}

// --- Plugins ---

func TestProcessPlugins(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getPlugins": {
			{"type": "EXTERNAL", "name": "Custom Plugin", "version": "1.0", "description": "A plugin", "homepageUrl": "https://example.com"},
			{"type": "BUNDLED", "name": "Bundled", "version": "2.0"},
		},
	})
	plugins := ProcessPlugins(dir, mapping, idMap())
	if len(plugins[testServerID]) != 1 {
		t.Fatalf("expected 1 external plugin, got %d", len(plugins[testServerID]))
	}
	if plugins[testServerID][0]["name"] != "Custom Plugin" {
		t.Errorf(errNameGotV, plugins[testServerID][0]["name"])
	}
}

func TestGeneratePluginMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getPlugins": {{"type": "EXTERNAL", "name": "Plugin1", "version": "1.0"}},
	})
	md, _ := GeneratePluginMarkdown(dir, mapping, idMap())
	if md == "" {
		t.Error(errExpectMD)
	}
}

// --- Measures ---

func TestProcessProjectMeasures(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getProjectMeasures": {
			{"projectKey": testProj1, "metric": "coverage", "value": "85.5"},
			{"projectKey": testProj1, "metric": "ncloc", "value": "5000"},
		},
	})
	measures := ProcessProjectMeasures(dir, mapping, idMap())
	if measures[testServerID][testProj1]["coverage"] != "85.5" {
		t.Errorf("coverage: got %v", measures[testServerID][testProj1]["coverage"])
	}
}

func TestExtractMeasureValueWithPeriod(t *testing.T) {
	m := map[string]any{"period": map[string]any{"value": "42"}}
	v := extractMeasureValue(m)
	if v != "42" {
		t.Errorf("got %v", v)
	}
}

// --- Applications ---

func TestProcessApplications(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getApplicationDetails": {
			{"name": "App1", "projects": []any{"p1", "p2"}},
			{"application": map[string]any{"name": "App2", "projects": []any{}}},
		},
	})
	apps := ProcessApplications(dir, mapping, idMap())
	if len(apps) != 2 {
		t.Fatalf("expected 2 apps, got %d", len(apps))
	}
	if apps[0]["project_count"] != 2 {
		t.Errorf("App1 projects: got %v", apps[0]["project_count"])
	}
	if apps[1]["project_count"] != 0 {
		t.Errorf("App2 projects: got %v", apps[1]["project_count"])
	}
}

// --- Portfolios ---

func TestProcessPortfolios(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getPortfolioDetails": {
			{"key": testPort1, "name": "Portfolio 1", "selectionMode": "MANUAL"},
		},
		"getPortfolioProjects": {
			{"portfolioKey": testPort1, "refKey": testProj1},
			{"portfolioKey": testPort1, "refKey": testProj2},
		},
	})
	portfolios := ProcessPortfolios(dir, mapping, idMap())
	if len(portfolios) != 1 {
		t.Fatalf("expected 1 portfolio, got %d", len(portfolios))
	}
	if portfolios[0]["project_count"] != 2 {
		t.Errorf("project_count: got %v", portfolios[0]["project_count"])
	}
}

// --- Tokens ---

func TestProcessTokens(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getUserTokens": {
			{"name": "my-token", "type": "USER_TOKEN", "isExpired": false, "login": "user1", "lastConnectionDate": testDate},
			{"name": "sonarlint-token", "type": "USER_TOKEN", "login": "user1"},
			{"name": "expired", "type": "USER_TOKEN", "isExpired": true, "login": "user2"},
		},
	})
	tokens := ProcessTokens(dir, mapping, idMap())
	entry := tokens[testServerID]
	if entry["total_tokens"] != 2 {
		t.Errorf("total_tokens: got %v (sonarlint should be excluded)", entry["total_tokens"])
	}
	if entry["expired_tokens"] != 1 {
		t.Errorf("expired_tokens: got %v", entry["expired_tokens"])
	}
}

// --- Projects ---

func TestProcessProjectDetails(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getProjectDetails": {
			{
				"key": testProj1, "name": "Project 1", "branch": "main",
				"qualityProfiles": []any{
					map[string]any{"key": "java-prof", "language": "java"},
				},
				"qualityGate": map[string]any{"name": testSonarWay},
			},
		},
		"getUsage": {
			{"projectKey": testProj1, "linesOfCode": float64(15000)},
		},
	})
	projects := ProcessProjectDetails(dir, mapping, idMap())
	if projects[testServerID] == nil || projects[testServerID][testProj1] == nil {
		t.Fatal("expected proj-1 in projects")
	}
	p := projects[testServerID][testProj1]
	if p["name"] != "Project 1" {
		t.Errorf(errNameGotV, p["name"])
	}
	if p["quality_gate"] != testSonarWay {
		t.Errorf("quality_gate: got %v", p["quality_gate"])
	}
	if p["loc"] != 15000 {
		t.Errorf("loc: got %v", p["loc"])
	}
	if p["tier"] != "m" {
		t.Errorf("tier: got %v (expected m for 15000 LOC)", p["tier"])
	}
}

// When extract skipped getUsage (api/projects/license_usage timed out on
// a large instance), projects are sized from their ncloc measure instead.
func TestProcessProjectDetails_NclocFallbackWithoutUsage(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getProjectDetails": {
			{"key": testProj1, "name": "P1", "qualityProfiles": []any{}},
			{"key": testProj2, "name": "P2", "qualityProfiles": []any{}},
		},
		"getProjectMeasures": {
			{"projectKey": testProj1, "metric": "ncloc", "value": "150000"},
			{"projectKey": testProj1, "metric": "coverage", "value": "80.0"},
		},
	})
	projects := ProcessProjectDetails(dir, mapping, idMap())
	p1 := projects[testServerID][testProj1]
	if p1["loc"] != 150000 || p1["tier"] != "l" {
		t.Errorf("proj-1: got loc=%v tier=%v, want 150000 / l", p1["loc"], p1["tier"])
	}
	// No usage and no ncloc measure (never analyzed): stays unsized.
	p2 := projects[testServerID][testProj2]
	if p2["loc"] != 0 || p2["tier"] != "unknown" {
		t.Errorf("proj-2: got loc=%v tier=%v, want 0 / unknown", p2["loc"], p2["tier"])
	}
}

// License usage, which counts a project's largest branch, wins over the
// main-branch ncloc measure for every project it covers.
func TestProcessProjectDetails_UsageWinsOverNcloc(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getProjectDetails": {
			{"key": testProj1, "name": "P1", "qualityProfiles": []any{}},
			{"key": testProj2, "name": "P2", "qualityProfiles": []any{}},
		},
		"getUsage": {
			{"projectKey": testProj1, "linesOfCode": float64(15000)},
		},
		"getProjectMeasures": {
			{"projectKey": testProj1, "metric": "ncloc", "value": "900"},
			{"projectKey": testProj2, "metric": "ncloc", "value": "2000"},
		},
	})
	projects := ProcessProjectDetails(dir, mapping, idMap())
	if p := projects[testServerID][testProj1]; p["loc"] != 15000 || p["tier"] != "m" {
		t.Errorf("proj-1: got loc=%v tier=%v, want 15000 / m from getUsage", p["loc"], p["tier"])
	}
	if p := projects[testServerID][testProj2]; p["loc"] != 2000 || p["tier"] != "s" {
		t.Errorf("proj-2: got loc=%v tier=%v, want 2000 / s from ncloc", p["loc"], p["tier"])
	}
}

// --- Servers ---

func TestGenerateServerMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getServerInfo": {
			{"System": map[string]any{"Version": "10.7", "Server ID": testServerID, "Edition": "enterprise", "Lines of Code": float64(50000)}},
		},
		"getProjectDetails": {
			{"key": testProj1, "name": "P1", "qualityProfiles": []any{}, "qualityGate": map[string]any{"name": "default"}},
		},
		"getUsers":           {{"login": "user1"}, {"login": "user2"}},
		"getProjectSettings": {},
		"getUsage":           {},
	})
	md, idMapping, projects := GenerateServerMarkdown(dir, mapping)
	if md == "" {
		t.Error(errExpectMD)
	}
	if idMapping[testServerURL] != testServerID {
		t.Errorf("ID mapping: got %v", idMapping[testServerURL])
	}
	if len(projects[testServerID]) != 1 {
		t.Errorf("expected 1 project, got %d", len(projects[testServerID]))
	}
}

// --- Gates ---

func TestGenerateGateMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getGates": {
			{"name": testSonarWay, "isBuiltIn": true, "isDefault": true, "caycStatus": "compliant"},
			{"name": "Custom Gate", "isBuiltIn": false, "isDefault": false},
		},
	})
	projects := Projects{testServerID: {
		testProj1: {"quality_gate": testSonarWay},
		testProj2: {"quality_gate": testSonarWay},
	}}
	active, unused := GenerateGateMarkdown(dir, mapping, idMap(), projects)
	if active == "" {
		t.Error("expected non-empty active gates")
	}
	if unused == "" {
		t.Error("expected non-empty unused gates (Custom Gate has 0 projects)")
	}
}

// --- Pipelines ---

func TestProcessScanDetails(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getProjectAnalyses": {
			{"date": "2026-04-10T10:00:00+0000", "detectedCI": testGHActions, "projectKey": testProj1, "events": []any{}},
			{"date": testDate, "detectedCI": testGHActions, "projectKey": testProj1, "events": []any{}},
		},
	})
	scans := ProcessScanDetails(dir, mapping, idMap())
	if scans[testServerID] == nil || scans[testServerID][testGHActions] == nil {
		t.Fatal("expected GitHub Actions scans")
	}
	scanData := scans[testServerID][testGHActions][testProj1]
	if scanData["total_scans"] != 2 {
		t.Errorf("total_scans: got %v", scanData["total_scans"])
	}
}

// --- Users ---

func TestGenerateUserMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getUsers": {
			{"login": "user1", "externalIdentity": "ext1", "lastConnectionDate": testDate, "externalProvider": "saml"},
			{"login": "user2", "lastConnectionDate": "2024-01-01T10:00:00+0000"},
		},
		"getGroups": {
			{"name": "devs", "permissions": []any{"admin"}, "managed": false},
		},
	})
	md, users, groups := GenerateUserMarkdown(dir, mapping, idMap())
	if md == "" {
		t.Error(errExpectMD)
	}
	if len(users[testServerID]) != 2 {
		t.Errorf("expected 2 users, got %d", len(users[testServerID]))
	}
	if len(groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(groups))
	}
}

// --- Bindings ---

func TestGenerateDevOpsMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getProjectBindings": {
			{"key": "binding-1", "projectKey": testProj1, "alm": "github"},
		},
		"getBindings": {
			{"key": "binding-1", "alm": "github", "url": "https://github.com"},
		},
		"getBranches":             {},
		"getProjectPullRequests":  {},
	})
	md, _ := GenerateDevOpsMarkdown(dir, mapping, idMap())
	if md == "" {
		t.Error(errExpectMD)
	}
}

// --- Permissions ---

func TestGeneratePermissionTemplateMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getDefaultTemplates": {
			{"templateId": "tmpl-1", "qualifier": "TRK"},
		},
		"getTemplates": {
			{"id": "tmpl-1", "name": "Default", "description": "Default template", "projectKeyPattern": ""},
		},
	})
	projects := Projects{testServerID: {testProj1: {"key": testProj1}}}
	md, templates := GeneratePermissionTemplateMarkdown(dir, mapping, idMap(), projects, false)
	if md == "" {
		t.Error(errExpectMD)
	}
	if len(templates) != 1 {
		t.Errorf("expected 1 template, got %d", len(templates))
	}
}

// --- Webhooks ---

func TestProcessWebhooks(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getWebhooks": {
			{"name": "hook1", "url": testHookURL, "hasSecret": true},
		},
		"getProjectWebhooks":          {},
		"getWebhookDeliveries":        {},
		"getProjectWebhookDeliveries": {},
	})
	webhooks := ProcessWebhooks(dir, mapping, idMap())
	if webhooks[testServerID] == nil || webhooks[testServerID]["hook1"] == nil {
		t.Fatal("expected hook1")
	}
	if webhooks[testServerID]["hook1"]["url"] != testHookURL {
		t.Errorf("url: got %v", webhooks[testServerID]["hook1"]["url"])
	}
}

func TestGenerateWebhookMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getWebhooks":                 {{"name": "hook1", "url": testHookURL}},
		"getProjectWebhooks":          {},
		"getWebhookDeliveries":        {},
		"getProjectWebhookDeliveries": {},
	})
	md := GenerateWebhookMarkdown(dir, mapping, idMap())
	if md == "" {
		t.Error(errExpectMD)
	}
}

// --- Tasks ---

func TestProcessTasks(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getTasks": {
			{"type": "REPORT", "submittedAt": testDate, "startedAt": "2026-04-15T10:00:01+0000", "executionTimeMs": float64(500), "status": "SUCCESS"},
			{"type": "REPORT", "submittedAt": "2026-04-15T11:00:00+0000", "startedAt": "2026-04-15T11:00:02+0000", "executionTimeMs": float64(300), "status": "FAILED"},
		},
		"getProjectTasks": {},
	})
	tasks := ProcessTasks(dir, mapping, idMap())
	if tasks[testServerID] == nil || tasks[testServerID]["REPORT"] == nil {
		t.Fatal("expected REPORT tasks")
	}
	entry := tasks[testServerID]["REPORT"]
	if entry["total"] != 2 {
		t.Errorf("total: got %v", entry["total"])
	}
	if entry["succeeded"] != 1 {
		t.Errorf("succeeded: got %v", entry["succeeded"])
	}
	if entry["failed"] != 1 {
		t.Errorf("failed: got %v", entry["failed"])
	}
}

func TestGenerateTaskMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getTasks":        {{"type": "REPORT", "submittedAt": testDate, "startedAt": "2026-04-15T10:00:01+0000", "executionTimeMs": float64(500), "status": "SUCCESS"}},
		"getProjectTasks": {},
	})
	md := GenerateTaskMarkdown(dir, mapping, idMap())
	if md == "" {
		t.Error(errExpectMD)
	}
}

// --- Profiles ---

func TestGenerateProfileMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getRules":        {{"key": "java:S100", "repo": "java"}},
		"getProfileRules": {{"java:S100": []any{map[string]any{"qProfile": testProfile1}}}},
		"getProfiles":     {{"key": testProfile1, "language": "java", "name": "Custom Java", "isBuiltIn": false, "isDefault": false}},
		"getPlugins":      {},
	})
	projects := Projects{testServerID: {
		testProj1: {"key": testProj1, "profiles": []string{testProfile1}, "rules": 0, "template_rules": 0, "plugin_rules": 0},
	}}
	plugins := map[string][]map[string]any{}
	active, inactive, profileMap, _ := GenerateProfileMarkdown(dir, mapping, idMap(), projects, plugins)
	if active == "" && inactive == "" {
		t.Error("expected at least one non-empty profile section")
	}
	if profileMap == nil {
		t.Error("expected non-nil profile map")
	}
}

// --- Pipelines markdown ---

func TestGeneratePipelineMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getProjectAnalyses": {
			{"date": "2026-04-10T10:00:00+0000", "detectedCI": testGHActions, "projectKey": testProj1, "events": []any{}},
		},
	})
	overview, details, scans := GeneratePipelineMarkdown(dir, mapping, idMap())
	if overview == "" {
		t.Error("expected non-empty overview")
	}
	if details == "" {
		t.Error("expected non-empty details")
	}
	if scans == nil {
		t.Error("expected non-nil scans")
	}
}

// --- Applications markdown ---

func TestGenerateApplicationMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getApplicationDetails": {
			{"name": "App1", "projects": []any{"p1"}},
			{"name": "Empty", "projects": []any{}},
		},
	})
	active, inactive := GenerateApplicationMarkdown(dir, mapping, idMap())
	if active == "" {
		t.Error("expected non-empty active apps")
	}
	if inactive == "" {
		t.Error("expected non-empty inactive apps")
	}
}

// --- Portfolios markdown ---

func TestGeneratePortfolioMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getPortfolioDetails": {
			{"key": testPort1, "name": "Active Port", "selectionMode": "MANUAL"},
			{"key": "port-2", "name": "Empty Port", "selectionMode": "NONE"},
		},
		"getPortfolioProjects": {
			{"portfolioKey": testPort1, "refKey": testProj1},
		},
	})
	active, inactive := GeneratePortfolioMarkdown(dir, mapping, idMap())
	if active == "" {
		t.Error("expected non-empty active portfolios")
	}
	if inactive == "" {
		t.Error("expected non-empty inactive portfolios")
	}
}

// --- Selection modes ---

func TestExtractSelectionModes(t *testing.T) {
	portfolio := map[string]any{
		"selectionMode": "MANUAL",
		"subViews": []any{
			map[string]any{"selectionMode": "REGEXP"},
		},
	}
	modes := extractSelectionModes(portfolio)
	if len(modes) != 2 {
		t.Errorf("expected 2 modes, got %d: %v", len(modes), modes)
	}
}

// --- Token markdown ---

func TestGenerateTokenMarkdown(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getUserTokens": {{"name": "tok", "type": "USER_TOKEN", "login": "u1"}},
	})
	md := GenerateTokenMarkdown(dir, mapping, idMap())
	if md == "" {
		t.Error(errExpectMD)
	}
}

// --- Branches ---

func TestProcessProjectBranches(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getBranches": {
			{"projectKey": testProj1, "name": "main", "excludedFromPurge": true},
			{"projectKey": testProj1, "name": "develop", "excludedFromPurge": true},
			{"projectKey": testProj2, "name": "main", "excludedFromPurge": true},
		},
	})
	branches := ProcessProjectBranches(dir, mapping, idMap())
	if !branches[testServerID][testProj1] {
		t.Error("proj-1 should have multiple branches")
	}
	if branches[testServerID][testProj2] {
		t.Error("proj-2 should not (only 1 branch)")
	}
}

// --- Pull Requests ---

func TestProcessProjectPullRequests(t *testing.T) {
	dir, mapping := setupExtract(t, map[string][]map[string]any{
		"getProjectPullRequests": {
			{"projectKey": testProj1, "analysisDate": testDate, "status": map[string]any{"qualityGateStatus": "OK"}},
		},
	})
	prs := ProcessProjectPullRequests(dir, mapping, idMap())
	if !prs[testServerID][testProj1] {
		t.Error("proj-1 should have PRs")
	}
}

// --- Date parsing ---

func TestParseSQDate(t *testing.T) {
	_, ok := parseSQDate(testDate)
	if !ok {
		t.Error("expected valid date")
	}
	_, ok = parseSQDate("")
	if ok {
		t.Error("expected invalid for empty")
	}
	_, ok = parseSQDate("not-a-date")
	if ok {
		t.Error("expected invalid for bad format")
	}
}

func TestAssignTier(t *testing.T) {
	tests := []struct {
		loc  int
		want string
	}{
		{600000, "xl"}, {100000, "l"}, {50000, "m"}, {5000, "s"}, {500, "xs"}, {0, "xs"},
	}
	for _, tt := range tests {
		got := assignTier(tt.loc)
		if got != tt.want {
			t.Errorf("assignTier(%d) = %q, want %q", tt.loc, got, tt.want)
		}
	}
}
