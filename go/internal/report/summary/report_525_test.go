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
)

// Issue #525: createProjects writes an explicit "status":"failed" record
// (with a tool-authored "error" message) when a target project key is
// already owned by a different SonarQube Cloud organization, instead of
// silently omitting the row. This file covers the report-side plumbing.

// A createProjects record marked "status":"failed" must land in Failed
// with its explicit error message, and must NOT also appear in Succeeded.
func TestCollectSummary_ExplicitProjectFailure(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "createProjects", []map[string]any{
		{
			"key": "src-proj-1", "name": "FailProj", "sonarcloud_org_key": "org1",
			"cloud_project_key": "org1_FailProj",
			"status":            "failed",
			"error":             `project key "org1_FailProj" already exists under a different SonarQube Cloud organization than "org1"`,
		},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	projSection := findSection(summary, "Projects")
	if projSection == nil {
		t.Fatal("missing Projects section")
	}
	if len(projSection.Succeeded) != 0 {
		t.Errorf("explicit failure must not also appear in Succeeded, got %+v", projSection.Succeeded)
	}
	if len(projSection.Failed) != 1 {
		t.Fatalf("expected 1 failed project, got %+v", projSection.Failed)
	}
	got := projSection.Failed[0]
	if got.Name != "FailProj" {
		t.Errorf("Name = %q, want FailProj", got.Name)
	}
	if got.SourceKey != "src-proj-1" {
		t.Errorf("SourceKey = %q, want src-proj-1", got.SourceKey)
	}
	if !strings.Contains(got.ErrorMessage, "different SonarQube Cloud organization") {
		t.Errorf("ErrorMessage = %q, want the explicit cross-org diagnosis", got.ErrorMessage)
	}
}

// A normal (no "status" field) createProjects record for one project and
// an explicit failure for another must classify independently: the
// former in Succeeded, the latter in Failed.
func TestCollectSummary_ExplicitProjectFailure_DoesNotAffectOtherProjects(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "createProjects", []map[string]any{
		{"key": "src-ok", "name": "OkProj", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_OkProj"},
		{
			"key": "src-fail", "name": "FailProj", "sonarcloud_org_key": "org1",
			"cloud_project_key": "org1_FailProj", "status": "failed",
			"error": "already exists under a different organization",
		},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	projSection := findSection(summary, "Projects")
	if projSection == nil {
		t.Fatal("missing Projects section")
	}
	if len(projSection.Succeeded) != 1 || projSection.Succeeded[0].Name != "OkProj" {
		t.Errorf("expected OkProj in Succeeded, got %+v", projSection.Succeeded)
	}
	if len(projSection.Failed) != 1 || projSection.Failed[0].Name != "FailProj" {
		t.Errorf("expected FailProj in Failed, got %+v", projSection.Failed)
	}
}

// When requests.log independently produced a generic Failed row for the
// SAME project (matched by Name), the explicit createProjects diagnosis
// must replace it, not duplicate it.
func TestCollectSummary_ExplicitProjectFailure_ReplacesGenericRequestsLogRow(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "createProjects", []map[string]any{
		{
			"key": "src-fail", "name": "FailProj", "sonarcloud_org_key": "org1",
			"cloud_project_key": "org1_FailProj", "status": "failed",
			"error": "project key \"org1_FailProj\" already exists under a different SonarQube Cloud organization",
		},
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
			"response": `{"errors":[{"msg":"Could not create Project, key already exists: org1_FailProj"}]}`,
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
	if projSection == nil {
		t.Fatal("missing Projects section")
	}
	if len(projSection.Failed) != 1 {
		t.Fatalf("expected exactly 1 failed row (no duplicate), got %d: %+v", len(projSection.Failed), projSection.Failed)
	}
	if !strings.Contains(projSection.Failed[0].ErrorMessage, "different SonarQube Cloud organization") {
		t.Errorf("expected the explicit diagnosis to win, got ErrorMessage = %q", projSection.Failed[0].ErrorMessage)
	}
	if strings.Contains(projSection.Failed[0].ErrorMessage, "Could not create Project") {
		t.Errorf("generic requests.log message should have been replaced, got %q", projSection.Failed[0].ErrorMessage)
	}
}
