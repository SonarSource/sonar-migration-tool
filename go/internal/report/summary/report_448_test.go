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

// Issue #448: the Projects section's Name column shows the source
// project key beneath the name. This file covers the data-plumbing
// side — EntityItem.SourceKey must reach every bucket (Succeeded,
// Skipped, Failed, Partial) that a project can land in.

// TestCollectSummary_ProjectSourceKey_Succeeded covers the direct path:
// createProjects JSONL carries the "key" field (via EnrichRaw
// preserving generateProjectMappings' input fields), and
// collectSucceeded must copy it into EntityItem.SourceKey.
func TestCollectSummary_ProjectSourceKey_Succeeded(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "createProjects", []map[string]any{
		{"key": "src-proj-1", "name": "Proj1", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj1"},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	projSection := findSection(summary, "Projects")
	if projSection == nil || len(projSection.Succeeded) != 1 {
		t.Fatalf("expected 1 succeeded project, got %+v", projSection)
	}
	if got := projSection.Succeeded[0].SourceKey; got != "src-proj-1" {
		t.Errorf("SourceKey = %q, want %q", got, "src-proj-1")
	}
}

// TestCollectSummary_ProjectSourceKey_Skipped covers the org-skipped
// path: collectSkipped reads generateProjectMappings directly (a row
// whose org mapping is empty/SKIPPED never reaches createProjects),
// which also carries the "key" field.
func TestCollectSummary_ProjectSourceKey_Skipped(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "generateProjectMappings", []map[string]any{
		{"key": "src-proj-2", "name": "Proj2", "sonarcloud_org_key": "", "sonarqube_org_key": "sq-org"},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	projSection := findSection(summary, "Projects")
	if projSection == nil || len(projSection.Skipped) != 1 {
		t.Fatalf("expected 1 skipped project, got %+v", projSection)
	}
	if got := projSection.Skipped[0].SourceKey; got != "src-proj-2" {
		t.Errorf("SourceKey = %q, want %q", got, "src-proj-2")
	}
}

// TestCollectSummary_ProjectSourceKey_Failed covers the harder path:
// a Failed row's EntityItem comes from the analysis-report ledger
// (analysis.ReportRow), which has no key field at all, so
// attachFailedSourceKeys must look it up by Name against
// generateProjectMappings.
func TestCollectSummary_ProjectSourceKey_Failed(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "generateProjectMappings", []map[string]any{
		{"key": "src-proj-3", "name": "FailProj", "sonarcloud_org_key": "org1"},
	})

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
	if err := os.WriteFile(filepath.Join(dir, "requests.log"), logBytes, 0o644); err != nil {
		t.Fatalf("write requests.log: %v", err)
	}

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	projSection := findSection(summary, "Projects")
	if projSection == nil || len(projSection.Failed) != 1 {
		t.Fatalf("expected 1 failed project, got %+v", projSection)
	}
	if got := projSection.Failed[0].SourceKey; got != "src-proj-3" {
		t.Errorf("SourceKey = %q, want %q", got, "src-proj-3")
	}
}

// TestCollectPartial_CarriesSourceKey covers collectPartial's
// carry-over path: a config-failure row that matches a Succeeded
// entity by name must inherit that entity's SourceKey (mirroring the
// existing Detail carry-over), so a project rerouted to Partial keeps
// its source-key line in the report.
func TestCollectPartial_CarriesSourceKey(t *testing.T) {
	def := sectionDef{Name: "Projects"}
	succeeded := []EntityItem{
		{Name: "proj1", Detail: "cloud-1", SourceKey: "src-1"},
	}
	failures := []configFailure{
		{Section: "Projects", Operation: "Project tags not migrated", EntityName: "proj1"},
	}

	partial := collectPartial(def, failures, succeeded)
	if len(partial) != 1 {
		t.Fatalf("expected 1 partial entry, got %d", len(partial))
	}
	if got := partial[0].SourceKey; got != "src-1" {
		t.Errorf("SourceKey = %q, want %q", got, "src-1")
	}
}

// TestRenderMarkdown_ProjectSourceKey covers the Markdown Name-cell
// rendering end to end: a Projects row with a SourceKey must render
// "Name<br>**KEY**" (name, then a line break, then the key in Markdown
// bold — matching the Details column's target-project-key styling).
// A row without a SourceKey (any other section) must render exactly
// as before.
func TestRenderMarkdown_ProjectSourceKey(t *testing.T) {
	summary := &MigrationSummary{
		RunID:       "issue-448",
		GeneratedAt: time.Now(),
		Sections: []Section{
			{
				Name: "Projects",
				Succeeded: []EntityItem{
					{Name: "Proj1", Organization: "org1", Detail: "org1_proj1", SourceKey: "src-proj-1"},
				},
			},
			{
				Name: "Quality Gates",
				Succeeded: []EntityItem{
					{Name: "Gate1", Organization: "org1", Detail: "42"},
				},
			},
		},
	}

	out, err := RenderMarkdown(summary)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	md := string(out)
	if !strings.Contains(md, "Proj1<br>**src-proj-1**") {
		t.Errorf("expected Name cell 'Proj1<br>**src-proj-1**' in Markdown output, got:\n%s", md)
	}
	if strings.Contains(md, "Gate1<br>") || strings.Contains(md, "**42**") {
		t.Errorf("Quality Gates row (no SourceKey) must render unchanged, got:\n%s", md)
	}
}
