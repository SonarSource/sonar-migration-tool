// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"testing"
	"time"
)

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestSelectBranchesAnalyzedAfter_NilCutoffIsNoOp(t *testing.T) {
	branches := []BranchDateInfo{
		{Name: "main", IsMain: true, AnalysisDate: date("2020-01-01")},
		{Name: "feature/x", AnalysisDate: date("2020-01-01")},
	}
	res := SelectBranchesAnalyzedAfter(branches, nil)
	if len(res.Kept) != len(branches) || res.ForcedMainBranch != "" {
		t.Fatalf("nil cutoff must be a no-op passthrough, got %+v", res)
	}
}

func TestSelectBranchesAnalyzedAfter_ExcludesOlderNonMainBranches(t *testing.T) {
	cutoff := date("2024-01-01")
	branches := []BranchDateInfo{
		{Name: "main", IsMain: true, AnalysisDate: date("2024-06-01")},
		{Name: "old-feature", AnalysisDate: date("2023-01-01")},
		{Name: "new-feature", AnalysisDate: date("2024-06-01")},
	}
	res := SelectBranchesAnalyzedAfter(branches, &cutoff)
	if res.ForcedMainBranch != "" {
		t.Fatalf("main met the cutoff, should not be forced, got %+v", res)
	}
	if len(res.Kept) != 2 {
		t.Fatalf("Kept = %+v, want main and new-feature only", res.Kept)
	}
	names := map[string]bool{}
	for _, b := range res.Kept {
		names[b.Name] = true
	}
	if !names["main"] || !names["new-feature"] || names["old-feature"] {
		t.Fatalf("Kept = %+v, want {main, new-feature}", res.Kept)
	}
}

func TestSelectBranchesAnalyzedAfter_BoundaryDateIsKept(t *testing.T) {
	cutoff := date("2024-01-01")
	branches := []BranchDateInfo{
		{Name: "main", IsMain: true, AnalysisDate: date("2024-01-01")},
		{Name: "on-boundary", AnalysisDate: date("2024-01-01")},
	}
	res := SelectBranchesAnalyzedAfter(branches, &cutoff)
	if res.ForcedMainBranch != "" {
		t.Fatalf("branch analyzed exactly on the cutoff must be kept without forcing, got %+v", res)
	}
	if len(res.Kept) != 2 {
		t.Fatalf("Kept = %+v, want both branches kept (on-or-after semantics)", res.Kept)
	}
}

func TestSelectBranchesAnalyzedAfter_ForcesMainWhenEverythingWouldBeExcluded(t *testing.T) {
	cutoff := date("2024-01-01")
	branches := []BranchDateInfo{
		{Name: "main", IsMain: true, AnalysisDate: date("2020-01-01")},
		{Name: "old-feature", AnalysisDate: date("2020-01-01")},
	}
	res := SelectBranchesAnalyzedAfter(branches, &cutoff)
	if res.ForcedMainBranch != "main" {
		t.Fatalf("ForcedMainBranch = %q, want %q", res.ForcedMainBranch, "main")
	}
	if !res.ForcedMainDate.Equal(date("2020-01-01")) {
		t.Fatalf("ForcedMainDate = %v, want %v", res.ForcedMainDate, date("2020-01-01"))
	}
	if len(res.Kept) != 1 || res.Kept[0].Name != "main" {
		t.Fatalf("Kept = %+v, want only main", res.Kept)
	}
}

func TestSelectBranchesAnalyzedAfter_ForcesNeverAnalyzedMain(t *testing.T) {
	cutoff := date("2024-01-01")
	branches := []BranchDateInfo{
		{Name: "main", IsMain: true}, // zero AnalysisDate: never analyzed
	}
	res := SelectBranchesAnalyzedAfter(branches, &cutoff)
	if res.ForcedMainBranch != "main" {
		t.Fatalf("a never-analyzed main must still be force-included, got %+v", res)
	}
	if len(res.Kept) != 1 {
		t.Fatalf("Kept = %+v, want only main", res.Kept)
	}
}

func TestSelectBranchesAnalyzedAfter_NoMainPresentDoesNotPanic(t *testing.T) {
	cutoff := date("2024-01-01")
	branches := []BranchDateInfo{
		{Name: "old-feature", AnalysisDate: date("2020-01-01")},
	}
	res := SelectBranchesAnalyzedAfter(branches, &cutoff)
	if res.ForcedMainBranch != "" {
		t.Fatalf("no main branch present, nothing to force, got %+v", res)
	}
	if len(res.Kept) != 0 {
		t.Fatalf("Kept = %+v, want empty (no main to fall back to)", res.Kept)
	}
}

func TestSelectBranchesAnalyzedAfter_MultipleBranchesSurviveNormally(t *testing.T) {
	cutoff := date("2024-01-01")
	branches := []BranchDateInfo{
		{Name: "main", IsMain: true, AnalysisDate: date("2024-06-01")},
		{Name: "release/1.0", AnalysisDate: date("2024-03-01")},
		{Name: "old-feature", AnalysisDate: date("2020-01-01")},
	}
	res := SelectBranchesAnalyzedAfter(branches, &cutoff)
	if res.ForcedMainBranch != "" {
		t.Fatalf("main and release/1.0 both met the cutoff, nothing should be forced, got %+v", res)
	}
	if len(res.Kept) != 2 {
		t.Fatalf("Kept = %+v, want {main, release/1.0}", res.Kept)
	}
}
