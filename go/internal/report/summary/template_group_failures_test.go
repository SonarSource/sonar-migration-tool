// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// #685 — a permission template whose group grants were dropped (group
// missing on the target, or its lookup failed) must not be reported as a
// full migration.

// The store sanitizes ":" in task names (#486), so the on-disk dir uses "_".
func writeTemplateRun(t *testing.T, exportDir, runID string, unresolved []map[string]any) string {
	t.Helper()
	runDir := filepath.Join(exportDir, runID)
	writeTaskJSONL(t, runDir, "createPermissionTemplates", []map[string]any{
		{"name": "Smoke685 Alpha", "cloud_template_id": "tplA", "sonarcloud_org_key": "org1"},
		{"name": "Smoke685 Beta", "cloud_template_id": "tplB", "sonarcloud_org_key": "org1"},
	})
	if unresolved != nil {
		writeTaskJSONL(t, runDir, "setTemplateGroupPermissions_unresolved", unresolved)
	}
	writeRunMeta(t, runDir, map[string]any{
		"started_at": "2026-11-02T18:08:07Z", "completed_at": "2026-11-02T18:09:05Z", "overall_status": "success",
	})
	return runDir
}

func templateRowNames(items []EntityItem) string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name)
	}
	return strings.Join(names, ",")
}

func TestTemplateGroupFailures_MissingGroupMovesTemplateToPartial(t *testing.T) {
	exportDir := t.TempDir()
	runDir := writeTemplateRun(t, exportDir, "2026-11-02-0001", []map[string]any{
		{"cloud_template_id": "tplA", "sonarcloud_org_key": "org1", "group": "dev", "reason": "missing"},
	})
	sum, err := CollectSummary(runDir, exportDir)
	if err != nil {
		t.Fatal(err)
	}
	sec := findSection(sum, "Permission Templates")
	if got := templateRowNames(sec.Succeeded); got != "Smoke685 Beta" {
		t.Errorf("succeeded = %q, want only Beta", got)
	}
	if len(sec.Partial) != 1 || sec.Partial[0].Name != "Smoke685 Alpha" {
		t.Fatalf("partial = %+v, want Alpha", sec.Partial)
	}
	want := "Group permissions not granted for group(s) dev: they do not exist in the target organization. " +
		"Migrate them first (--objects groups), then re-run --objects permission_templates."
	if got := sec.Partial[0].Issues; len(got) != 1 || got[0] != want {
		t.Errorf("issues = %q, want %q", got, want)
	}

	md, err := RenderMarkdown(sum)
	if err != nil {
		t.Fatal(err)
	}
	out := string(md)
	if !strings.Contains(out, "Partial Migration") || !strings.Contains(out, "group(s) dev") {
		t.Errorf("markdown does not surface the partial template and group:\n%s", out)
	}
	if strings.Contains(out, "| Smoke685 Alpha | org1 | Full Migration") {
		t.Errorf("Alpha must not be reported as Full Migration:\n%s", out)
	}
	if desc := sectionCountSummary(*sec); !strings.Contains(desc, "1 succeeded") || !strings.Contains(desc, "1 partial") {
		t.Errorf("section count summary = %q, want 1 succeeded and 1 partial", desc)
	}
}

func TestTemplateGroupFailures_MissingAndLookupFailedOnOneTemplate(t *testing.T) {
	exportDir := t.TempDir()
	runDir := writeTemplateRun(t, exportDir, "2026-11-02-0001", []map[string]any{
		{"cloud_template_id": "tplA", "sonarcloud_org_key": "org1", "group": "qa", "reason": "missing"},
		{"cloud_template_id": "tplA", "sonarcloud_org_key": "org1", "group": "dev", "reason": "missing"},
		{"cloud_template_id": "tplA", "sonarcloud_org_key": "org1", "group": "dev", "reason": "missing"},
		{"cloud_template_id": "tplA", "sonarcloud_org_key": "org1", "group": "ops", "reason": "lookup_failed"},
	})
	sum, err := CollectSummary(runDir, exportDir)
	if err != nil {
		t.Fatal(err)
	}
	sec := findSection(sum, "Permission Templates")
	if len(sec.Partial) != 1 || sec.Partial[0].Name != "Smoke685 Alpha" {
		t.Fatalf("partial = %+v, want exactly one Alpha row", sec.Partial)
	}
	issues := strings.Join(sec.Partial[0].Issues, "\n")
	if !strings.Contains(issues, "group(s) dev, qa: they do not exist") {
		t.Errorf("missing groups not sorted/de-duplicated: %s", issues)
	}
	if !strings.Contains(issues, "group(s) ops: they could not be looked up in the target organization (see the run log), re-run --objects permission_templates.") {
		t.Errorf("lookup_failed issue missing: %s", issues)
	}
}

func TestApplyTemplateGroupResolutionFailures_OrgAndNearPerfect(t *testing.T) {
	runDir := t.TempDir()
	writeTaskJSONL(t, runDir, "createPermissionTemplates", []map[string]any{
		{"name": "T", "cloud_template_id": "id1", "sonarcloud_org_key": "org1"},
	})
	writeTaskJSONL(t, runDir, "setTemplateGroupPermissions_unresolved", []map[string]any{
		{"cloud_template_id": "id1", "sonarcloud_org_key": "org1", "group": "dev", "reason": "missing"},
	})
	store := common.NewDataStore(runDir)
	succ := []EntityItem{{Name: "T", Organization: "org2"}}
	near := []EntityItem{{Name: "T", Organization: "org1", Issues: []string{"default"}}}
	s, n, p := applyTemplateGroupResolutionFailures(store, succ, near, nil)
	if len(s) != 1 || len(n) != 0 || len(p) != 1 {
		t.Fatalf("succeeded=%d near=%d partial=%d, want org2 row kept and org1 row moved to partial", len(s), len(n), len(p))
	}
	if len(p[0].Issues) != 2 || p[0].Issues[0] != "default" {
		t.Errorf("issues = %q, want the earlier issue kept plus the group one", p[0].Issues)
	}
}

func TestTemplateGroupFailures_NoUnresolvedDirLeavesTemplatesUntouched(t *testing.T) {
	exportDir := t.TempDir()
	runDir := writeTemplateRun(t, exportDir, "2026-11-02-0001", nil)
	sum, err := CollectSummary(runDir, exportDir)
	if err != nil {
		t.Fatal(err)
	}
	sec := findSection(sum, "Permission Templates")
	if len(sec.Succeeded) != 2 || len(sec.Partial) != 0 {
		t.Errorf("succeeded=%d partial=%d, want 2 and 0", len(sec.Succeeded), len(sec.Partial))
	}
}

// The newest run wins per template: a later run that granted the groups
// clears the earlier Partial, and a later run with unresolved groups makes
// an earlier Full Migration Partial. This holds with no change to the merge
// because a re-run of createPermissionTemplates emits a row for an existing
// template (the "already exists" lookup path), so the later run always
// reports its own, non-weak, record of it.
func TestTemplateGroupFailures_CumulativeLaterRunWins(t *testing.T) {
	unresolved := []map[string]any{
		{"cloud_template_id": "tplA", "sonarcloud_org_key": "org1", "group": "dev", "reason": "missing"},
	}

	t.Run("later run fixes it", func(t *testing.T) {
		exportDir := t.TempDir()
		writeTemplateRun(t, exportDir, "2026-11-02-0001", unresolved)
		latest := writeTemplateRun(t, exportDir, "2026-11-02-0002", nil)
		sum, err := CollectCumulativeSummary(latest, exportDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(sum.Runs) != 2 {
			t.Fatalf("Runs = %v, want both runs", runIDs(sum.Runs))
		}
		sec := findSection(sum, "Permission Templates")
		if len(sec.Partial) != 0 {
			t.Errorf("partial = %+v, want the earlier partial superseded", sec.Partial)
		}
		if got := templateRowNames(sec.Succeeded); !strings.Contains(got, "Smoke685 Alpha") || len(sec.Succeeded) != 2 {
			t.Errorf("succeeded = %q, want Alpha and Beta", got)
		}
	})

	t.Run("later run drops groups", func(t *testing.T) {
		exportDir := t.TempDir()
		writeTemplateRun(t, exportDir, "2026-11-02-0001", nil)
		latest := writeTemplateRun(t, exportDir, "2026-11-02-0002", unresolved)
		sum, err := CollectCumulativeSummary(latest, exportDir)
		if err != nil {
			t.Fatal(err)
		}
		sec := findSection(sum, "Permission Templates")
		if len(sec.Partial) != 1 || sec.Partial[0].Name != "Smoke685 Alpha" {
			t.Errorf("partial = %+v, want Alpha", sec.Partial)
		}
		if got := templateRowNames(sec.Succeeded); got != "Smoke685 Beta" {
			t.Errorf("succeeded = %q, want only Beta", got)
		}
	})
}
