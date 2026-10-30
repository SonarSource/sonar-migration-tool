// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"path/filepath"
	"sort"
	"testing"
)

// #666: portfolios extracted in slices all end up in the mapping, and a
// portfolio re-extracted by a later slice is mapped from its newest record.
func TestMapPortfolios_CumulativeAcrossSlices(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{runA, runB} {
		writeTestJSON(t, filepath.Join(root, id, "extract.json"),
			map[string]any{"url": streamTestURL, "partial": true})
	}
	portfolioRecords(root, runA, "getPortfolioProjects",
		`{"portfolioKey":"p1","portfolioName":"One","refKey":"proj-a"}`,
		`{"portfolioKey":"p2","portfolioName":"Two (old)","refKey":"proj-b"}`)
	portfolioRecords(root, runB, "getPortfolioProjects",
		`{"portfolioKey":"p2","portfolioName":"Two","refKey":"proj-c"}`,
		`{"portfolioKey":"p3","portfolioName":"Three","refKey":"proj-d"}`)

	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range MapPortfolios(root, mapping) {
		got = append(got, p.SourcePortfolioKey+"="+p.Name)
	}
	sort.Strings(got)
	want := []string{"p1=One", "p2=Two", "p3=Three"}
	if len(got) != len(want) {
		t.Fatalf("portfolios = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("portfolios = %v, want %v", got, want)
			break
		}
	}
}
