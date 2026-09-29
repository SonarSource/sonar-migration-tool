// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"path/filepath"
	"regexp"
	"testing"
)

// TestProjectsInScope covers #597's per-project ETA seeding input: the
// upfront project count RunMigrate uses to size syncIssueMetadata must come
// purely from already-extracted data, and must honour --project_key the
// same way runCreateProjects does (#536). No API calls, so it is safe to
// call before a single migrate task has run.
func TestProjectsInScope(t *testing.T) {
	dir := t.TempDir()
	writeExtractMetaJSON(t, dir, extractRun, testServerURL)
	writeJSONL(filepath.Join(dir, extractRun, "getProjects"), []map[string]any{
		{"key": projMain},
		{"key": "proj2"},
		{"key": "other-thing"},
		// A record with no key is not a project we can migrate.
		{"name": "keyless"},
	})

	cloudSrv := newMockCloudServer()
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	if got := projectsInScope(e); got != 3 {
		t.Errorf("unfiltered: got %d, want 3 (the keyless record is not counted)", got)
	}

	e.ProjectKeyRe = regexp.MustCompile("^proj")
	if got := projectsInScope(e); got != 2 {
		t.Errorf("with --project_key ^proj: got %d, want 2", got)
	}

	e.ProjectKeyRe = regexp.MustCompile("^nothing-matches$")
	if got := projectsInScope(e); got != 0 {
		t.Errorf("with a pattern matching nothing: got %d, want 0", got)
	}
}

// A missing or unreadable extract must yield 0 rather than an error: the
// caller only uses the count to size a seed, and a run with no extract
// fails later for much better reasons.
func TestProjectsInScopeReturnsZeroWithoutAnExtract(t *testing.T) {
	cloudSrv := newMockCloudServer()
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	if got := projectsInScope(e); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}
