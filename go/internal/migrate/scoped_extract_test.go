// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"path/filepath"
	"testing"
)

// Fixture field names and values, kept as constants so the literals are
// not repeated across every record in every table.
const (
	fieldKey        = "key"
	fieldProjectKey = "projectKey"
	fieldProject    = "project"
	fieldBranch     = "branch"
	fieldSource     = "source"
	fieldHighlights = "highlightedLines"

	extractRun     = "extract-01"
	taskSourceCode = "getProjectSourceCode"
	taskHotspots   = "getProjectHotspotsFull"

	projMain   = "proj1"
	branchMain = "main"
	branchDev  = "develop"
	fileA      = "proj1:a.go"
	srcA       = "package a\n"

	hsNoBranch     = "hs-no-branch"
	hsOtherProject = "hs-other-project"
)

// sourceCorpus writes a getProjectSourceCode fixture containing records for
// two projects across two branches, so scoping can be observed rather than
// assumed.
func sourceCorpus(t *testing.T, dir string) {
	t.Helper()
	writeJSONL(filepath.Join(dir, extractRun, taskSourceCode), []map[string]any{
		{
			fieldKey: fileA, fieldProjectKey: projMain, fieldBranch: branchMain,
			fieldSource: srcA, fieldHighlights: []string{"<span>package a</span>"},
		},
		{
			fieldKey: "proj1:b.go", fieldProjectKey: projMain, fieldBranch: branchMain,
			fieldSource: "package b\n", fieldHighlights: []string{"<span>package b</span>"},
		},
		// Same project, different branch — must not leak into main.
		{
			fieldKey: "proj1:c.go", fieldProjectKey: projMain, fieldBranch: branchDev,
			fieldSource: "package c\n", fieldHighlights: []string{"<span>package c</span>"},
		},
		// Different project entirely — must not leak either.
		{
			fieldKey: "proj2:d.go", fieldProjectKey: "proj2", fieldBranch: branchMain,
			fieldSource: "package d\n", fieldHighlights: []string{"<span>package d</span>"},
		},
	})
}

func TestLoadBranchSourceDataScopesToProjectAndBranch(t *testing.T) {
	dir := t.TempDir()
	sourceCorpus(t, dir)
	e := newProjectDataExecutor(t, dir)

	sources, highlights := loadBranchSourceData(e, testServerURL, projMain, branchMain)

	if len(sources) != 2 {
		t.Fatalf("got %d sources, want 2 (proj1/main only): %+v", len(sources), sources)
	}
	if len(highlights) != 2 {
		t.Fatalf("got %d highlights, want 2", len(highlights))
	}
	for _, s := range sources {
		if s.Component != fileA && s.Component != "proj1:b.go" {
			t.Errorf("unexpected component leaked into scope: %q", s.Component)
		}
	}
}

// The merged loader must return exactly what the two separate loaders did.
// Those thin adapters delegate to it, so this pins the contract the existing
// per-loader tests rely on.
func TestLoadBranchSourceDataMatchesIndividualLoaders(t *testing.T) {
	dir := t.TempDir()
	sourceCorpus(t, dir)
	e := newProjectDataExecutor(t, dir)

	sources, highlights := loadBranchSourceData(e, testServerURL, projMain, branchMain)
	onlySources := loadExtractedSources(e, testServerURL, projMain, branchMain)
	onlyHighlights := loadExtractedSyntaxHighlighting(e, testServerURL, projMain, branchMain)

	if len(sources) != len(onlySources) {
		t.Fatalf("sources: merged %d, individual %d", len(sources), len(onlySources))
	}
	for i := range sources {
		if sources[i] != onlySources[i] {
			t.Errorf("source %d: merged %+v, individual %+v", i, sources[i], onlySources[i])
		}
	}

	if len(highlights) != len(onlyHighlights) {
		t.Fatalf("highlights: merged %d, individual %d", len(highlights), len(onlyHighlights))
	}
	for i := range highlights {
		if highlights[i].Component != onlyHighlights[i].Component {
			t.Errorf("highlight %d: merged %q, individual %q",
				i, highlights[i].Component, onlyHighlights[i].Component)
		}
	}
}

// The two original loaders disagreed on empty keys and that divergence is
// load-bearing: a source record with no key still contributes its length to
// the #425 purged-source decision, but cannot produce a HighlightInput
// because highlighting is keyed by component. Merging them must not
// accidentally align the two.
func TestLoadBranchSourceDataKeepsEmptyKeySourceButNotHighlight(t *testing.T) {
	dir := t.TempDir()
	writeJSONL(filepath.Join(dir, extractRun, taskSourceCode), []map[string]any{
		{
			fieldKey: "", fieldProjectKey: projMain, fieldBranch: branchMain,
			fieldSource: "orphan source\n", fieldHighlights: []string{"<span>orphan</span>"},
		},
		{
			fieldKey: fileA, fieldProjectKey: projMain, fieldBranch: branchMain,
			fieldSource: srcA, fieldHighlights: []string{"<span>package a</span>"},
		},
	})
	e := newProjectDataExecutor(t, dir)

	sources, highlights := loadBranchSourceData(e, testServerURL, projMain, branchMain)

	if len(sources) != 2 {
		t.Errorf("got %d sources, want 2 — the empty-key record still counts toward source length", len(sources))
	}
	if len(highlights) != 1 {
		t.Fatalf("got %d highlights, want 1 — the empty-key record cannot be highlighted", len(highlights))
	}
	if highlights[0].Component != fileA {
		t.Errorf("highlight component = %q, want proj1:a.go", highlights[0].Component)
	}
}

// A record carrying source but no highlighting yields a source and no
// highlight, rather than an empty-lines highlight entry.
func TestLoadBranchSourceDataSkipsEmptyHighlighting(t *testing.T) {
	dir := t.TempDir()
	writeJSONL(filepath.Join(dir, extractRun, taskSourceCode), []map[string]any{
		{fieldKey: fileA, fieldProjectKey: projMain, fieldBranch: branchMain, fieldSource: srcA},
	})
	e := newProjectDataExecutor(t, dir)

	sources, highlights := loadBranchSourceData(e, testServerURL, projMain, branchMain)

	if len(sources) != 1 {
		t.Errorf("got %d sources, want 1", len(sources))
	}
	if len(highlights) != 0 {
		t.Errorf("got %d highlights, want 0", len(highlights))
	}
}

// Hotspot records name their project "project" rather than "projectKey",
// and a record with no branch belongs to every branch. Both quirks are
// pre-existing behaviour that the scoped reader has to keep.
func TestScopedHotspotItemsHandlesProjectKeyFallbackAndEmptyBranch(t *testing.T) {
	dir := t.TempDir()
	writeJSONL(filepath.Join(dir, extractRun, taskHotspots), []map[string]any{
		{fieldKey: "hs-project-field", fieldProject: projMain, fieldBranch: branchMain},
		{fieldKey: "hs-projectkey-fallback", fieldProjectKey: projMain, fieldBranch: branchMain},
		{fieldKey: hsNoBranch, fieldProject: projMain},
		{fieldKey: "hs-other-branch", fieldProject: projMain, fieldBranch: branchDev},
		{fieldKey: hsOtherProject, fieldProject: "proj2", fieldBranch: branchMain},
	})
	e := newProjectDataExecutor(t, dir)

	seen := map[string]bool{}
	scope := extractScope{ServerURL: testServerURL, ProjectKey: projMain, Branch: branchMain}
	for _, hdr := range scopedHotspotItems(e, scope) {
		seen[hdr.Key] = true
	}

	for _, want := range []string{"hs-project-field", "hs-projectkey-fallback", hsNoBranch} {
		if !seen[want] {
			t.Errorf("expected %q to be in scope", want)
		}
	}
	for _, notWant := range []string{"hs-other-branch", hsOtherProject} {
		if seen[notWant] {
			t.Errorf("%q must not be in scope", notWant)
		}
	}
}

// An empty scope Branch must span every branch of the project. This is how
// loadMatchableHotspots reads: the metadata sync covers all branches, so
// scoping it to one would silently drop hotspots.
func TestScopedHotspotItemsEmptyScopeBranchSpansAllBranches(t *testing.T) {
	dir := t.TempDir()
	writeJSONL(filepath.Join(dir, extractRun, taskHotspots), []map[string]any{
		{fieldKey: "hs-main", fieldProject: projMain, fieldBranch: branchMain},
		{fieldKey: "hs-develop", fieldProject: projMain, fieldBranch: branchDev},
		{fieldKey: hsNoBranch, fieldProject: projMain},
		{fieldKey: hsOtherProject, fieldProject: "proj2", fieldBranch: branchMain},
	})
	e := newProjectDataExecutor(t, dir)

	seen := map[string]bool{}
	scope := extractScope{ServerURL: testServerURL, ProjectKey: projMain}
	for _, hdr := range scopedHotspotItems(e, scope) {
		seen[hdr.Key] = true
	}

	for _, want := range []string{"hs-main", "hs-develop", hsNoBranch} {
		if !seen[want] {
			t.Errorf("expected %q in an unscoped-branch read", want)
		}
	}
	if seen[hsOtherProject] {
		t.Error("another project must never be in scope")
	}
}

// An empty scope Branch matches every branch; this is what run-scoped and
// project-scoped readers rely on.
func TestRecordHeaderMatches(t *testing.T) {
	tests := []struct {
		name  string
		hdr   recordHeader
		scope extractScope
		want  bool
	}{
		{"exact", recordHeader{ProjectKey: "p", Branch: branchMain},
			extractScope{ProjectKey: "p", Branch: branchMain}, true},
		{"wrong project", recordHeader{ProjectKey: "other", Branch: branchMain},
			extractScope{ProjectKey: "p", Branch: branchMain}, false},
		{"wrong branch", recordHeader{ProjectKey: "p", Branch: branchDev},
			extractScope{ProjectKey: "p", Branch: branchMain}, false},
		{"empty scope branch matches any", recordHeader{ProjectKey: "p", Branch: branchDev},
			extractScope{ProjectKey: "p"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.hdr.matches(tc.scope); got != tc.want {
				t.Errorf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}

// Records from another SonarQube server must never be in scope, even when
// the project key and branch happen to coincide.
func TestScopedExtractItemsFiltersByServerURL(t *testing.T) {
	dir := t.TempDir()
	writeJSONL(filepath.Join(dir, extractRun, taskSourceCode), []map[string]any{
		{fieldKey: fileA, fieldProjectKey: projMain, fieldBranch: branchMain, fieldSource: "a"},
	})
	e := newProjectDataExecutor(t, dir)

	scope := extractScope{ServerURL: "https://other.test/", ProjectKey: projMain, Branch: branchMain}
	count := 0
	for range scopedExtractItems(e, taskSourceCode, scope) {
		count++
	}
	if count != 0 {
		t.Errorf("got %d records for a different server, want 0", count)
	}
}
