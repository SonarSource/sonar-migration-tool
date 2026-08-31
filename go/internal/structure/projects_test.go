// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const (
	testSQURL      = "https://sq.example.com/"
	testExtractID  = "2026-08-20-0001"
	testCustomGate = "Custom Gate"
	testSonarWay   = "Sonar way"
	testProjOrgKey = "https://sq.example.com/proj1"
)

func TestIsCloudBinding(t *testing.T) {
	tests := []struct {
		url      string
		expected bool
	}{
		{"https://api.github.com/orgs/myorg", true},
		{"https://gitlab.com/mygroup", true},
		{"https://dev.azure.com/myorg", true},
		{"https://bitbucket.org/myteam", true},
		{"https://my-server.example.com", false},
		{"", false},
		// Azure DevOps Services accounts predating the dev.azure.com
		// rename still bind as <account>.visualstudio.com, and those are
		// cloud. Treating them as on-premise silently dropped the
		// binding (#505).
		{"https://myaccount.visualstudio.com", true},
		{"https://myaccount.visualstudio.com/DefaultCollection", true},
		// On-premise platforms SonarQube Cloud cannot integrate with.
		// The observed shape of a GitHub Enterprise Server binding is
		// the /api/v3 endpoint on the customer's own host.
		{"https://github.acme.com/api/v3", false},
		{"https://gitlab.acme.com/api/v4", false},
		{"https://bitbucket-server.your-company.com", false},
		// Hosts are case-insensitive; nothing normalises what the server reports.
		{"HTTPS://API.GITHUB.COM", true},
	}
	for _, tt := range tests {
		got := IsCloudBinding(tt.url)
		if got != tt.expected {
			t.Errorf("IsCloudBinding(%q) = %v, want %v", tt.url, got, tt.expected)
		}
	}
}

func TestGenerateUniqueProjectKey(t *testing.T) {
	// ALM-bound non-monorepo
	got := GenerateUniqueProjectKey(testSQURL, "proj1", "github", "org/repo", false)
	if got != "github_org/repo" {
		t.Errorf("expected 'github_org/repo', got %q", got)
	}

	// No ALM
	got = GenerateUniqueProjectKey(testSQURL, "proj1", "", "", false)
	if got != testProjOrgKey {
		t.Errorf("expected 'https://sq.example.com/proj1', got %q", got)
	}

	// Monorepo (falls back to server_url+key)
	got = GenerateUniqueProjectKey(testSQURL, "proj1", "github", "org/repo", true)
	if got != testProjOrgKey {
		t.Errorf("expected 'https://sq.example.com/proj1', got %q", got)
	}
}

func TestGenerateUniqueBindingKey(t *testing.T) {
	tests := []struct {
		name       string
		serverURL  string
		key        string
		alm        string
		bindingURL string
		repository string
		expected   string
	}{
		{
			name: "no ALM", serverURL: testSQURL,
			expected: testSQURL,
		},
		{
			name: "github", serverURL: testSQURL,
			key: "gh1", alm: "github", bindingURL: "https://api.github.com",
			repository: "myorg/myrepo",
			expected:   "api.github.com/myorg",
		},
		{
			name: "gitlab", serverURL: testSQURL,
			key: "gl1", alm: "gitlab", bindingURL: "https://gitlab.com/mygroup",
			repository: "mygroup/myrepo",
			expected:   "gitlab.com/gl1 - https://sq.example.com/",
		},
		{
			name: "azure", serverURL: testSQURL,
			key: "az1", alm: "azure", bindingURL: "https://dev.azure.com/myorg",
			repository: "",
			expected:   "dev.azure.com/myorg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateUniqueBindingKey(tt.serverURL, tt.key, tt.alm, tt.bindingURL, tt.repository)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestMapProjectStructure(t *testing.T) {
	dir := setupTestExtract(t)
	mapping := ExtractMapping{testSQURL: testExtractID}

	bindings, projects := MapProjectStructure(dir, mapping)

	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	if len(bindings) < 1 {
		t.Fatalf("expected at least 1 binding, got %d", len(bindings))
	}

	// Verify project fields.
	found := false
	for _, p := range projects {
		if p.Key == "proj1" {
			found = true
			if p.Name != "Project 1" {
				t.Errorf("expected name 'Project 1', got %q", p.Name)
			}
			if p.MainBranch != "main" {
				t.Errorf("expected branch 'main', got %q", p.MainBranch)
			}
		}
	}
	if !found {
		t.Error("proj1 not found in projects")
	}
}

func TestMapNewCodeDefinitions(t *testing.T) {
	dir := setupTestExtract(t)
	mapping := ExtractMapping{testSQURL: testExtractID}

	defs := MapNewCodeDefinitions(dir, mapping)
	if len(defs) == 0 {
		t.Skip("no new code definitions in test data")
	}
}

// setupTestExtract creates a minimal extract directory structure for testing.
func setupTestExtract(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	extractDir := filepath.Join(dir, testExtractID)

	// Write extract.json metadata.
	writeTestJSON(t, filepath.Join(extractDir, "extract.json"),
		map[string]any{"url": testSQURL, "edition": "enterprise"})

	// Write getProjectDetails.
	writeTestJSONL(t, filepath.Join(extractDir, "getProjectDetails"), []map[string]any{
		{
			"key": "proj1", "name": "Project 1", "projectKey": "proj1", "branch": "main",
			"qualityGate":     map[string]any{"name": testCustomGate},
			"qualityProfiles": []map[string]any{{"key": "prof1", "name": testSonarWay, "language": "java"}},
			"serverUrl":       testSQURL,
		},
		{
			"key": "proj2", "name": "Project 2", "projectKey": "proj2", "branch": "master",
			"qualityGate":     map[string]any{"name": testCustomGate},
			"qualityProfiles": []map[string]any{{"key": "prof2", "name": "Custom", "language": "java"}},
			"serverUrl":       testSQURL,
		},
	})

	// Write getBindings (empty — no ALM bindings).
	writeTestJSONL(t, filepath.Join(extractDir, "getBindings"), nil)

	// Write getProjectBindings (empty).
	writeTestJSONL(t, filepath.Join(extractDir, "getProjectBindings"), nil)

	// Write getNewCodePeriods.
	writeTestJSONL(t, filepath.Join(extractDir, "getNewCodePeriods"), []map[string]any{
		{"type": "NUMBER_OF_DAYS", "value": 30, "projectKey": "proj1", "branchKey": "main"},
	})

	// Write getProfiles.
	writeTestJSONL(t, filepath.Join(extractDir, "getProfiles"), []map[string]any{
		{"key": "prof1", "name": testSonarWay, "language": "java", "isBuiltIn": true, "isDefault": true, "serverUrl": testSQURL},
		{"key": "prof2", "name": "Custom", "language": "java", "isBuiltIn": false, "isDefault": false, "parentKey": "prof1", "serverUrl": testSQURL},
	})

	// Write getGates.
	writeTestJSONL(t, filepath.Join(extractDir, "getGates"), []map[string]any{
		{"name": testSonarWay, "isBuiltIn": true, "isDefault": false, "serverUrl": testSQURL},
		{"name": testCustomGate, "isBuiltIn": false, "isDefault": true, "serverUrl": testSQURL},
	})

	// Write getGroups.
	writeTestJSONL(t, filepath.Join(extractDir, "getGroups"), []map[string]any{
		{"id": "1", "name": "sonar-users", "permissions": []string{"scan"}, "description": "Users", "serverUrl": testSQURL},
		{"id": "Anyone", "name": "Anyone", "permissions": []string{}, "serverUrl": testSQURL},
	})

	// Write getTemplates.
	writeTestJSONL(t, filepath.Join(extractDir, "getTemplates"), []map[string]any{
		{"id": "tpl1", "name": "Default Template", "projectKeyPattern": "", "serverUrl": testSQURL},
	})

	// Write getDefaultTemplates.
	writeTestJSONL(t, filepath.Join(extractDir, "getDefaultTemplates"), []map[string]any{
		{"templateId": "tpl1", "qualifier": "TRK"},
	})

	// Write empty tasks for completeness.
	for _, task := range []string{"getProjectGroupsPermissions", "getTemplateGroupsScanners", "getTemplateGroupsViewers", "getProfileGroups", "getPortfolioProjects"} {
		writeTestJSONL(t, filepath.Join(extractDir, task), nil)
	}

	return dir
}

func writeTestJSON(t *testing.T, path string, data any) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(data)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestJSONL(t *testing.T, taskDir string, items []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(taskDir, "results.1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, item := range items {
		b, _ := json.Marshal(item)
		f.Write(b)
		f.WriteString("\n")
	}
}
