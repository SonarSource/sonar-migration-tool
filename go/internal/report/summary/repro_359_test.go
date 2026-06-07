// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"strings"
	"testing"
)

// #359: end-to-end check that the post-fix migration_summary surfaces
// the project-data outcome in the exact wording the issue spec asks
// for, with NO leftover "was skipped" duplication and NO sync-stats
// line on projects whose data was skipped or failed.
func TestCollectSummary_ProjectDataAndSyncStats(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "createProjects", []map[string]any{
		{"name": "ProjOK", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_projok"},
		{"name": "ProjOKMismatch", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj_mm"},
		{"name": "ProjNeverAnalyzed", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_skipped"},
		{"name": "ProjPurged", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_purged"},
		{"name": "ProjFailed", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_failed"},
	})
	writeTaskJSONL(t, dir, "importProjectData", []map[string]any{
		{"cloud_project_key": "org1_projok", "branch": "main", "status": "success"},
		{"cloud_project_key": "org1_proj_mm", "branch": "main", "status": "success"},
		{"cloud_project_key": "org1_skipped", "branch": "main", "status": "skipped"},
		{"cloud_project_key": "org1_purged", "branch": "main", "status": "skipped",
			"error": "source code not retrievable for this branch (line measures may remain, but source text is gone — likely purged by SonarQube housekeeping)"},
		{"cloud_project_key": "org1_failed", "branch": "main", "status": "failed", "error": "CE task failed: 500"},
	})
	writeTaskJSONL(t, dir, "syncIssueMetadata", []map[string]any{
		{"cloud_project_key": "org1_projok", "synced": float64(42), "line_mismatch": float64(0), "not_found": float64(0), "actionable": float64(42)},
		{"cloud_project_key": "org1_proj_mm", "synced": float64(36), "line_mismatch": float64(2), "not_found": float64(43), "actionable": float64(81)},
		// Skipped-import projects: defensive — sync records may exist (older fashion) or not. Either way they must not surface a sync line.
		{"cloud_project_key": "org1_skipped", "synced": float64(0), "line_mismatch": float64(0), "not_found": float64(0), "actionable": float64(0)},
		{"cloud_project_key": "org1_purged", "synced": float64(0), "line_mismatch": float64(0), "not_found": float64(4), "actionable": float64(4)},
	})

	summary, err := CollectSummary(dir, "")
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	projSection := findSection(summary, "Projects")
	if projSection == nil {
		t.Fatal("missing Projects section")
	}

	// Collect ALL project EntityItems across buckets so the lookups below
	// can find them regardless of which routing they took.
	all := map[string]EntityItem{}
	bucket := map[string]string{}
	for _, b := range []struct {
		items []EntityItem
		name  string
	}{
		{projSection.Succeeded, "Succeeded"},
		{projSection.NearPerfect, "NearPerfect"},
		{projSection.Partial, "Partial"},
	} {
		for _, it := range b.items {
			all[it.Name] = it
			bucket[it.Name] = b.name
		}
	}

	type expectation struct {
		bucket      string
		mustContain []string
		mustNot     []string
	}
	cases := map[string]expectation{
		"ProjOK": {
			bucket: "Succeeded",
			mustContain: []string{
				"100% of issues with manual changes synced (42/42)",
			},
			mustNot: []string{
				"Project data migration", "Project data: ",
				"Issue sync had unresolved",
			},
		},
		"ProjOKMismatch": {
			bucket: "NearPerfect",
			mustContain: []string{
				"44% of issues with manual changes synced (36/81)",
				"Issue sync had unresolved counterparts",
			},
			mustNot: []string{
				"Project data migration", // import was successful, no skip line
			},
		},
		"ProjNeverAnalyzed": {
			bucket: "Partial",
			mustContain: []string{
				"Project data migration skipped: Source project was provisioned but never analyzed",
			},
			mustNot: []string{
				"Project data migration was skipped", // legacy duplicate must be gone
				"synced",                             // sync line must be gone
				"Issue sync had unresolved",
			},
		},
		"ProjPurged": {
			bucket: "Partial",
			mustContain: []string{
				"Project data migration skipped: source code not retrievable for this branch",
			},
			mustNot: []string{
				"Project data migration was skipped",
				"synced",
				"Issue sync had unresolved",
			},
		},
		"ProjFailed": {
			bucket: "Partial",
			mustContain: []string{
				"Project data migration skipped: API error when migrating project data: CE task failed: 500",
			},
			mustNot: []string{
				"Project data migration was skipped",
				"synced",
				"Issue sync had unresolved",
			},
		},
	}

	for name, exp := range cases {
		t.Run(name, func(t *testing.T) {
			item, ok := all[name]
			if !ok {
				t.Fatalf("project %q not found in any bucket", name)
			}
			if bucket[name] != exp.bucket {
				t.Errorf("expected bucket %s, got %s", exp.bucket, bucket[name])
			}
			rendered := partialDetails(item, false, false, true)
			for _, s := range exp.mustContain {
				if !strings.Contains(rendered, s) {
					t.Errorf("missing expected substring %q in:\n%s", s, rendered)
				}
			}
			for _, s := range exp.mustNot {
				if strings.Contains(rendered, s) {
					t.Errorf("unwanted substring %q present in:\n%s", s, rendered)
				}
			}
		})
	}
}
