// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"path/filepath"
	"testing"
)

// sourceCorpus writes a getProjectSourceCode fixture containing records for
// two projects across two branches, so scoping can be observed rather than
// assumed.
func sourceCorpus(t *testing.T, dir string) {
	t.Helper()
	writeJSONL(filepath.Join(dir, "extract-01", "getProjectSourceCode"), []map[string]any{
		{
			"key": "proj1:a.go", "projectKey": "proj1", "branch": "main",
			"source": "package a\n", "highlightedLines": []string{"<span>package a</span>"},
		},
		{
			"key": "proj1:b.go", "projectKey": "proj1", "branch": "main",
			"source": "package b\n", "highlightedLines": []string{"<span>package b</span>"},
		},
		// Same project, different branch — must not leak into main.
		{
			"key": "proj1:c.go", "projectKey": "proj1", "branch": "develop",
			"source": "package c\n", "highlightedLines": []string{"<span>package c</span>"},
		},
		// Different project entirely — must not leak either.
		{
			"key": "proj2:d.go", "projectKey": "proj2", "branch": "main",
			"source": "package d\n", "highlightedLines": []string{"<span>package d</span>"},
		},
	})
}

func TestLoadBranchSourceDataScopesToProjectAndBranch(t *testing.T) {
	dir := t.TempDir()
	sourceCorpus(t, dir)
	e := newProjectDataExecutor(t, dir)

	sources, highlights := loadBranchSourceData(e, testServerURL, "proj1", "main")

	if len(sources) != 2 {
		t.Fatalf("got %d sources, want 2 (proj1/main only): %+v", len(sources), sources)
	}
	if len(highlights) != 2 {
		t.Fatalf("got %d highlights, want 2", len(highlights))
	}
	for _, s := range sources {
		if s.Component != "proj1:a.go" && s.Component != "proj1:b.go" {
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

	sources, highlights := loadBranchSourceData(e, testServerURL, "proj1", "main")
	onlySources := loadExtractedSources(e, testServerURL, "proj1", "main")
	onlyHighlights := loadExtractedSyntaxHighlighting(e, testServerURL, "proj1", "main")

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
	writeJSONL(filepath.Join(dir, "extract-01", "getProjectSourceCode"), []map[string]any{
		{
			"key": "", "projectKey": "proj1", "branch": "main",
			"source": "orphan source\n", "highlightedLines": []string{"<span>orphan</span>"},
		},
		{
			"key": "proj1:a.go", "projectKey": "proj1", "branch": "main",
			"source": "package a\n", "highlightedLines": []string{"<span>package a</span>"},
		},
	})
	e := newProjectDataExecutor(t, dir)

	sources, highlights := loadBranchSourceData(e, testServerURL, "proj1", "main")

	if len(sources) != 2 {
		t.Errorf("got %d sources, want 2 — the empty-key record still counts toward source length", len(sources))
	}
	if len(highlights) != 1 {
		t.Fatalf("got %d highlights, want 1 — the empty-key record cannot be highlighted", len(highlights))
	}
	if highlights[0].Component != "proj1:a.go" {
		t.Errorf("highlight component = %q, want proj1:a.go", highlights[0].Component)
	}
}

// A record carrying source but no highlighting yields a source and no
// highlight, rather than an empty-lines highlight entry.
func TestLoadBranchSourceDataSkipsEmptyHighlighting(t *testing.T) {
	dir := t.TempDir()
	writeJSONL(filepath.Join(dir, "extract-01", "getProjectSourceCode"), []map[string]any{
		{"key": "proj1:a.go", "projectKey": "proj1", "branch": "main", "source": "package a\n"},
	})
	e := newProjectDataExecutor(t, dir)

	sources, highlights := loadBranchSourceData(e, testServerURL, "proj1", "main")

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
	writeJSONL(filepath.Join(dir, "extract-01", "getProjectHotspotsFull"), []map[string]any{
		{"key": "hs-project-field", "project": "proj1", "branch": "main"},
		{"key": "hs-projectkey-fallback", "projectKey": "proj1", "branch": "main"},
		{"key": "hs-no-branch", "project": "proj1"},
		{"key": "hs-other-branch", "project": "proj1", "branch": "develop"},
		{"key": "hs-other-project", "project": "proj2", "branch": "main"},
	})
	e := newProjectDataExecutor(t, dir)

	seen := map[string]bool{}
	scope := extractScope{ServerURL: testServerURL, ProjectKey: "proj1", Branch: "main"}
	for _, hdr := range scopedHotspotItems(e, scope) {
		seen[hdr.Key] = true
	}

	for _, want := range []string{"hs-project-field", "hs-projectkey-fallback", "hs-no-branch"} {
		if !seen[want] {
			t.Errorf("expected %q to be in scope", want)
		}
	}
	for _, notWant := range []string{"hs-other-branch", "hs-other-project"} {
		if seen[notWant] {
			t.Errorf("%q must not be in scope", notWant)
		}
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
		{"exact", recordHeader{ProjectKey: "p", Branch: "main"},
			extractScope{ProjectKey: "p", Branch: "main"}, true},
		{"wrong project", recordHeader{ProjectKey: "other", Branch: "main"},
			extractScope{ProjectKey: "p", Branch: "main"}, false},
		{"wrong branch", recordHeader{ProjectKey: "p", Branch: "develop"},
			extractScope{ProjectKey: "p", Branch: "main"}, false},
		{"empty scope branch matches any", recordHeader{ProjectKey: "p", Branch: "develop"},
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
	writeJSONL(filepath.Join(dir, "extract-01", "getProjectSourceCode"), []map[string]any{
		{"key": "proj1:a.go", "projectKey": "proj1", "branch": "main", "source": "a"},
	})
	e := newProjectDataExecutor(t, dir)

	scope := extractScope{ServerURL: "https://other.test/", ProjectKey: "proj1", Branch: "main"}
	count := 0
	for range scopedExtractItems(e, "getProjectSourceCode", scope) {
		count++
	}
	if count != 0 {
		t.Errorf("got %d records for a different server, want 0", count)
	}
}
