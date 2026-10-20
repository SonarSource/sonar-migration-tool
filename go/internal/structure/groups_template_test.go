// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"path/filepath"
	"sort"
	"testing"
)

// Issue #648: a permission template mapped to several organizations must
// contribute its groups to every one of them, on every run.
func TestAddTemplateGroupsCoversEveryTemplateOrg(t *testing.T) {
	dir := setupTestExtract(t)
	extractDir := filepath.Join(dir, testExtractID)
	writeTestJSONL(t, filepath.Join(extractDir, "getTemplateGroupsScanners"), []map[string]any{
		{"templateId": "tpl1", "name": "scanners-only", "description": "Scan", "serverUrl": testSQURL},
	})
	writeTestJSONL(t, filepath.Join(extractDir, "getTemplateGroupsViewers"), []map[string]any{
		{"templateId": "tpl1", "name": "viewers-only", "description": "Browse", "serverUrl": testSQURL},
	})
	mapping := ExtractMapping{testSQURL: testExtractID}
	projectOrgMapping := map[string]string{
		testProjOrgKey:                 "org1",
		"https://sq.example.com/proj2": "org2",
	}

	// Map iteration order is random, so repeat to catch an order-dependent result.
	for run := 0; run < 50; run++ {
		templates := MapTemplates(projectOrgMapping, mapping, dir)
		groups := MapGroups(projectOrgMapping, mapping, nil, templates, dir)

		var got []string
		for _, g := range groups {
			if g.Name == "scanners-only" || g.Name == "viewers-only" {
				got = append(got, g.SonarQubeOrgKey+"/"+g.Name)
			}
		}
		sort.Strings(got)
		want := []string{"org1/scanners-only", "org1/viewers-only", "org2/scanners-only", "org2/viewers-only"}
		if len(got) != len(want) {
			t.Fatalf("run %d: got %v, want %v", run, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("run %d: got %v, want %v", run, got, want)
			}
		}
	}
}
