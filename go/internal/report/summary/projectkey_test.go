// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
)

// runProjectKeyTest writes rows to generateProjectMappings and runs
// collectProjectKeyReport with the given pattern. Reduces the
// 3-line dir+writeTaskJSONL+collect block to a single call.
func runProjectKeyTest(t *testing.T, rows []map[string]any, pattern string) *ProjectKeyReport {
	t.Helper()
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "generateProjectMappings", rows)
	return collectProjectKeyReport(common.NewDataStore(dir), pattern)
}

func TestCollectProjectKeyReport(t *testing.T) {
	t.Run("default pattern, unique keys → nil", func(t *testing.T) {
		got := runProjectKeyTest(t, []map[string]any{
			{"key": "proj-a", "sonarcloud_org_key": "org1"},
			{"key": "proj-b", "sonarcloud_org_key": "org2"},
		}, migrate.DefaultProjectKeyPattern)
		if got != nil {
			t.Fatalf("expected nil report, got %+v", got)
		}
	})

	t.Run("static-prefix pattern collides same key across orgs", func(t *testing.T) {
		got := runProjectKeyTest(t, []map[string]any{
			{"key": "shared", "sonarcloud_org_key": "org1"},
			{"key": "shared", "sonarcloud_org_key": "org2"},
			{"key": "unique", "sonarcloud_org_key": "org1"},
		}, "ACME_CORP_<ORIGINAL_PROJECT_KEY>")
		if got == nil {
			t.Fatal("expected a report, got nil")
		}
		if len(got.Collisions) != 1 {
			t.Fatalf("expected 1 collision, got %d", len(got.Collisions))
		}
		c := got.Collisions[0]
		if c.TargetKey != "ACME_CORP_shared" {
			t.Errorf("collision target key = %q", c.TargetKey)
		}
		if len(c.Sources) != 2 {
			t.Errorf("expected 2 colliding sources, got %d", len(c.Sources))
		}
	})

	t.Run("default pattern keeps same key in different orgs distinct", func(t *testing.T) {
		// org1_shared vs org2_shared — no collision under the default pattern.
		got := runProjectKeyTest(t, []map[string]any{
			{"key": "shared", "sonarcloud_org_key": "org1"},
			{"key": "shared", "sonarcloud_org_key": "org2"},
		}, migrate.DefaultProjectKeyPattern)
		if got != nil {
			t.Fatalf("expected nil report (org prefix disambiguates), got %+v", got)
		}
	})

	t.Run("over-length key is flagged", func(t *testing.T) {
		longKey := strings.Repeat("x", migrate.MaxProjectKeyLength)
		got := runProjectKeyTest(t, []map[string]any{
			{"key": longKey, "sonarcloud_org_key": "org1"}, // org1_ + 400 x's > 400
		}, migrate.DefaultProjectKeyPattern)
		if got == nil || len(got.TooLong) != 1 {
			t.Fatalf("expected 1 over-length key, got %+v", got)
		}
		if got.TooLong[0].Length <= migrate.MaxProjectKeyLength {
			t.Errorf("over-length entry length = %d, want > %d", got.TooLong[0].Length, migrate.MaxProjectKeyLength)
		}
	})

	t.Run("SKIPPED and empty orgs are ignored", func(t *testing.T) {
		got := runProjectKeyTest(t, []map[string]any{
			{"key": "shared", "sonarcloud_org_key": "SKIPPED"},
			{"key": "shared", "sonarcloud_org_key": ""},
		}, "ACME_CORP_<ORIGINAL_PROJECT_KEY>")
		if got != nil {
			t.Fatalf("expected nil (all rows skipped), got %+v", got)
		}
	})
}
