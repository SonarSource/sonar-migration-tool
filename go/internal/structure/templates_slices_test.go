// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"path/filepath"
	"sort"
	"testing"
)

func readTaskStrings(t *testing.T, root string, task string) []string {
	t.Helper()
	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}
	items, err := ReadExtractData(root, mapping, task)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(items))
	for _, it := range items {
		got = append(got, string(it.Data))
	}
	sort.Strings(got)
	return got
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// #665: permission template slices accumulate, and a template re-extracted
// by a later slice replaces its older records.
func TestExtractItemsMergesTemplateSlices(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{runA, runB} {
		writeTestJSON(t, filepath.Join(root, id, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	}
	portfolioRecords(root, runA, "getTemplates", `{"id":"t1","v":1}`, `{"id":"t2","v":1}`)
	portfolioRecords(root, runB, "getTemplates", `{"id":"t2","v":2}`, `{"id":"t3","v":2}`)
	assertStrings(t, readTaskStrings(t, root, "getTemplates"),
		[]string{`{"id":"t1","v":1}`, `{"id":"t2","v":2}`, `{"id":"t3","v":2}`})
}

// #665: the permission tasks are keyed on templateId (their own "id" is a
// group id), and a re-extracted template with no permission left in the later
// slice still drops its stale records from the earlier one.
func TestExtractItemsMergesTemplatePermissionSlices(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{runA, runB} {
		writeTestJSON(t, filepath.Join(root, id, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	}
	portfolioRecords(root, runA, "getTemplates", `{"id":"t1"}`, `{"id":"t2"}`)
	portfolioRecords(root, runB, "getTemplates", `{"id":"t2"}`, `{"id":"t3"}`)
	portfolioRecords(root, runA, "getTemplateGroupsScanners",
		`{"id":"g1","templateId":"t1"}`, `{"id":"g1","templateId":"t2"}`)
	portfolioRecords(root, runB, "getTemplateGroupsScanners", `{"id":"g9","templateId":"t3"}`)
	assertStrings(t, readTaskStrings(t, root, "getTemplateGroupsScanners"),
		[]string{`{"id":"g1","templateId":"t1"}`, `{"id":"g9","templateId":"t3"}`})
}

// #665: an extract that did not run getTemplates (e.g. --objects projects)
// supersedes no template of an earlier one.
func TestExtractItemsTemplatesKeptWhenLaterExtractSkippedThem(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{runA, runB} {
		writeTestJSON(t, filepath.Join(root, id, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	}
	portfolioRecords(root, runA, "getTemplates", `{"id":"t1"}`)
	portfolioRecords(root, runB, "getProjects", `{"key":"p"}`)
	assertStrings(t, readTaskStrings(t, root, "getTemplates"), []string{`{"id":"t1"}`})
}
