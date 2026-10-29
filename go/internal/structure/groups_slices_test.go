// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

// #669: group slices accumulate, and a group re-extracted by a later slice
// replaces its older records. getGroups is keyed by id, getUserGroups by the
// groupId it was enriched with.
func TestExtractItemsMergesGroupSlices(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{runA, runB} {
		writeTestJSON(t, filepath.Join(root, id, "extract.json"),
			map[string]any{"url": streamTestURL, "partial": true})
	}
	portfolioRecords(root, runA, "getGroups", `{"id":"g1","name":"a","v":1}`, `{"id":"g2","name":"b","v":1}`)
	portfolioRecords(root, runB, "getGroups", `{"id":"g2","name":"b","v":2}`, `{"id":"g3","name":"c","v":2}`)
	portfolioRecords(root, runA, "getUserGroups", `{"login":"u1","groupId":"g1"}`, `{"login":"old","groupId":"g2"}`)
	portfolioRecords(root, runB, "getUserGroups", `{"login":"new","groupId":"g2"}`)

	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}
	for task, want := range map[string][]string{
		"getGroups": {
			`{"id":"g1","name":"a","v":1}`, `{"id":"g2","name":"b","v":2}`, `{"id":"g3","name":"c","v":2}`,
		},
		"getUserGroups": {`{"login":"u1","groupId":"g1"}`, `{"login":"new","groupId":"g2"}`},
	} {
		var got []string
		for it := range ExtractItems(root, mapping, task) {
			got = append(got, string(it.Data))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", task, got, want)
		}
	}
}

// A later extract that ran no group task (for example --objects projects)
// leaves the earlier group records alone.
func TestExtractItemsGroupsSurviveLaterExtractWithoutThem(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{runA, runB} {
		writeTestJSON(t, filepath.Join(root, id, "extract.json"),
			map[string]any{"url": streamTestURL, "partial": true})
	}
	portfolioRecords(root, runA, "getGroups", `{"id":"g1","name":"a"}`)
	projectRecords(root, runB, `{"key":"a"}`)
	mapping, _ := GetUniqueExtracts(root)
	if got := collect(ExtractItems(root, mapping, "getGroups")); len(got) != 1 {
		t.Errorf("got %d group records, want 1", len(got))
	}
}

func TestRecordGroupKeyPrecedence(t *testing.T) {
	for raw, want := range map[string]string{
		`{"groupId":"g","id":"i"}`: "g",
		`{"id":"i","name":"n"}`:    "i",
		`{"x":1}`:                  "",
	} {
		if got := recordGroupKey(json.RawMessage(raw)); got != want {
			t.Errorf("recordGroupKey(%s) = %q, want %q", raw, got, want)
		}
	}
}

// Groups extracted in slices all end up in the mapping, and a group
// re-extracted by a later slice is mapped from its newest record.
func TestMapGroups_CumulativeAcrossSlices(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{runA, runB} {
		writeTestJSON(t, filepath.Join(root, id, "extract.json"),
			map[string]any{"url": streamTestURL, "partial": true})
	}
	portfolioRecords(root, runA, "getGroups",
		`{"id":"g1","name":"one","description":"d1","permissions":["admin"]}`,
		`{"id":"g2","name":"two","description":"old","permissions":["admin"]}`)
	portfolioRecords(root, runB, "getGroups",
		`{"id":"g2","name":"two","description":"new","permissions":["admin"]}`,
		`{"id":"g3","name":"three","description":"d3","permissions":["admin"]}`)

	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}
	orgs := map[string]string{streamTestURL + "p": "org"}
	var got []string
	for _, g := range MapGroups(orgs, mapping, nil, nil, root) {
		got = append(got, g.Name+"="+g.Description)
	}
	sort.Strings(got)
	want := []string{"one=d1", "three=d3", "two=new"}
	if !slices.Equal(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}
