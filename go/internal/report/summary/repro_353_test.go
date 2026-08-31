// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #353: end-to-end check that user permissions on source objects
// surface as a per-row "Permissions granted to N users have been
// dropped" line in the migration report. The object's bucket
// (Succeeded / Perfect) MUST NOT change — this is informational
// because SonarQube Cloud's API can't grant permissions to
// individual users.
func TestCollectSummary_DroppedUserPerms(t *testing.T) {
	dir := t.TempDir()
	runID := "run-01"
	runDir := filepath.Join(dir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir runDir: %v", err)
	}
	extractDir := filepath.Join(dir, "2026-08-20-0001")
	writeExtractMeta(t, extractDir, "https://sq.example.com")

	// ── Quality Gates ───────────────────────────────────────────
	// Gate "MyGate" has two distinct user permissions; the extract
	// carries gateName=MyGate. EntityItem.Name matches directly so
	// no source→cloud translation is needed.
	writeTaskJSONL(t, extractDir, "getGates", []map[string]any{
		{"name": "MyGate", "isBuiltIn": false},
	})
	writeTaskJSONL(t, runDir, "generateGateMappings", []map[string]any{
		{"name": "MyGate", "sonarcloud_org_key": "org1"},
	})
	writeTaskJSONL(t, runDir, "createGates", []map[string]any{
		{"name": "MyGate", "sonarcloud_org_key": "org1", "cloud_gate_id": "42"},
	})
	writeTaskJSONL(t, extractDir, "getGateUsers", []map[string]any{
		{"gateName": "MyGate", "login": "alice"},
		{"gateName": "MyGate", "login": "bob"},
	})

	// ── Projects ────────────────────────────────────────────────
	// Source project key SrcProj1, cloud key cloud_org1_proj1.
	// getProjectUsersScanners has alice; getProjectUsersViewers has
	// alice (dedup) + bob. Distinct: 2.
	writeTaskJSONL(t, runDir, "generateProjectMappings", []map[string]any{
		{"key": "SrcProj1", "name": "MyProject", "sonarcloud_org_key": "org1"},
	})
	writeTaskJSONL(t, runDir, "createProjects", []map[string]any{
		{"key": "SrcProj1", "name": "MyProject", "sonarcloud_org_key": "org1",
			"cloud_project_key": "cloud_org1_proj1"},
	})
	writeTaskJSONL(t, extractDir, "getProjectUsersScanners", []map[string]any{
		{"project": "SrcProj1", "login": "alice"},
	})
	writeTaskJSONL(t, extractDir, "getProjectUsersViewers", []map[string]any{
		{"project": "SrcProj1", "login": "alice"}, // dedup
		{"project": "SrcProj1", "login": "bob"},
	})

	// ── Quality Profiles ────────────────────────────────────────
	// createProfiles writes the source UUID under `source_profile_key`
	// (NOT `key` — that's reserved for the input mapping). The
	// user-perm extract carries the same UUID under `profileKey`.
	// Regression: an earlier version of this PR keyed off `key` and
	// produced no marker for the user's "Olivier Way" profile.
	writeTaskJSONL(t, runDir, "createProfiles", []map[string]any{
		{
			"name":               "Olivier Way",
			"language":           "py",
			"source_profile_key": "uuid-olivier-way",
			"cloud_profile_key":  "cloud_olivier_way",
			"sonarcloud_org_key": "org1",
		},
	})
	writeTaskJSONL(t, runDir, "generateProfileMappings", []map[string]any{
		{"name": "Olivier Way", "language": "py", "sonarcloud_org_key": "org1"},
	})
	writeTaskJSONL(t, extractDir, "getProfileUsers", []map[string]any{
		{"profileKey": "uuid-olivier-way", "login": "olivier"},
	})

	// ── Permission Templates ────────────────────────────────────
	// Same shape — source UUID lives under `source_template_key`,
	// extract carries it as `templateId`.
	writeTaskJSONL(t, runDir, "createPermissionTemplates", []map[string]any{
		{
			"name":                "Banking Template",
			"source_template_key": "uuid-banking-tpl",
			"cloud_template_id":   "cloud_banking_tpl",
			"sonarcloud_org_key":  "org1",
		},
	})
	writeTaskJSONL(t, runDir, "generateTemplateMappings", []map[string]any{
		{"name": "Banking Template", "sonarcloud_org_key": "org1"},
	})
	writeTaskJSONL(t, extractDir, "getTemplateUsersScanners", []map[string]any{
		{"templateId": "uuid-banking-tpl", "login": "charlie"},
		{"templateId": "uuid-banking-tpl", "login": "dave"},
	})

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	t.Run("quality gates carry the user-perm line", func(t *testing.T) {
		gates := findSection(summary, "Quality Gates")
		if gates == nil || len(gates.Succeeded) != 1 {
			t.Fatalf("expected 1 succeeded gate, got %+v", gates)
		}
		item := gates.Succeeded[0]
		if !strings.Contains(item.Detail, "|userPerms:2") {
			t.Errorf("expected |userPerms:2 marker, got Detail=%q", item.Detail)
		}
		rendered := partialDetails(item, false, false, false)
		if !strings.Contains(rendered, "Permissions granted to 2 users have been dropped in the migration") {
			t.Errorf("expected actual-tense user-perm line, got: %s", rendered)
		}
	})

	t.Run("projects carry the user-perm line", func(t *testing.T) {
		projects := findSection(summary, "Projects")
		if projects == nil || len(projects.Succeeded) != 1 {
			t.Fatalf("expected 1 succeeded project, got %+v", projects)
		}
		item := projects.Succeeded[0]
		if !strings.Contains(item.Detail, "|userPerms:2") {
			t.Errorf("expected |userPerms:2 marker, got Detail=%q", item.Detail)
		}
		rendered := partialDetails(item, false, false, true)
		if !strings.Contains(rendered, "Permissions granted to 2 users have been dropped in the migration") {
			t.Errorf("expected user-perm line, got: %s", rendered)
		}
	})

	t.Run("quality profiles carry the user-perm line — regression for 'Olivier Way'", func(t *testing.T) {
		profiles := findSection(summary, "Quality Profiles")
		if profiles == nil || len(profiles.Succeeded) != 1 {
			t.Fatalf("expected 1 succeeded profile, got %+v", profiles)
		}
		item := profiles.Succeeded[0]
		if !strings.Contains(item.Detail, "|userPerms:1") {
			t.Errorf("expected |userPerms:1 marker (source_profile_key→cloud_profile_key translation), got Detail=%q", item.Detail)
		}
		rendered := partialDetails(item, false, false, false)
		if !strings.Contains(rendered, "Permissions granted to 1 user have been dropped in the migration") {
			t.Errorf("expected user-perm line, got: %s", rendered)
		}
	})

	t.Run("permission templates carry the user-perm line", func(t *testing.T) {
		tpls := findSection(summary, "Permission Templates")
		if tpls == nil || len(tpls.Succeeded) != 1 {
			t.Fatalf("expected 1 succeeded template, got %+v", tpls)
		}
		item := tpls.Succeeded[0]
		if !strings.Contains(item.Detail, "|userPerms:2") {
			t.Errorf("expected |userPerms:2 marker (source_template_key→cloud_template_id translation), got Detail=%q", item.Detail)
		}
		rendered := partialDetails(item, false, false, false)
		if !strings.Contains(rendered, "Permissions granted to 2 users have been dropped in the migration") {
			t.Errorf("expected user-perm line, got: %s", rendered)
		}
	})

	t.Run("entity bucket stays Succeeded (Perfect)", func(t *testing.T) {
		// Object status MUST NOT change because of user permissions —
		// it's an informational comment, not a fail/partial signal.
		for _, sectionName := range []string{"Quality Gates", "Projects"} {
			s := findSection(summary, sectionName)
			if s == nil {
				continue
			}
			if len(s.NearPerfect) != 0 {
				t.Errorf("%s: user-perm marker must not demote to NearPerfect: %+v", sectionName, s.NearPerfect)
			}
			if len(s.Partial) != 0 {
				t.Errorf("%s: user-perm marker must not demote to Partial: %+v", sectionName, s.Partial)
			}
		}
	})

	t.Run("predictive flips tense to future", func(t *testing.T) {
		// successDetails called directly with predictive=true should
		// switch to "will be dropped".
		gates := findSection(summary, "Quality Gates")
		item := gates.Succeeded[0]
		rendered := partialDetails(item, true /*predictive*/, false, false)
		if !strings.Contains(rendered, "Permissions granted to 2 users will be dropped in the migration") {
			t.Errorf("expected future-tense user-perm line, got: %s", rendered)
		}
	})
}
