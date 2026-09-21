// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import "time"

// BranchDateInfo is the minimal per-branch shape --branch_analyzed_after
// filtering needs (#583), independent of extract's json.RawMessage records
// and migrate's branchInfo struct so both can build one of these and share
// the same filter logic.
type BranchDateInfo struct {
	Name         string
	IsMain       bool
	AnalysisDate time.Time // zero if the branch was never analyzed
}

// BranchFilterResult is the outcome of applying a --branch_analyzed_after
// cutoff to one project's branches (#583).
type BranchFilterResult struct {
	Kept []BranchDateInfo
	// ForcedMainBranch is the main branch's name when it was kept despite
	// not meeting the cutoff, because the filter would otherwise have
	// excluded every branch of the project. Empty when no forcing occurred.
	ForcedMainBranch string
	ForcedMainDate   time.Time
}

// SelectBranchesAnalyzedAfter keeps every branch whose AnalysisDate is on or
// after cutoff. A nil cutoff is a no-op: it returns every branch unchanged,
// unforced — the "--branch_analyzed_after not specified" case (#583).
//
// The project's main branch is always present in the result. If the date
// rule alone would have excluded it too (and so excluded every branch of
// the project), main is force-included and reported via ForcedMainBranch.
//
// This is intentionally the only thing this function does: a pure,
// independent, order-agnostic predicate over one project's branch list. A
// future #582 (--branches glob/regex filter) is expected to be its own,
// equally independent function over the same []BranchDateInfo shape — the
// two compose by simply chaining one after the other, in either order,
// since both are expected to guarantee "main survives its own pass."
func SelectBranchesAnalyzedAfter(branches []BranchDateInfo, cutoff *time.Time) BranchFilterResult {
	if cutoff == nil {
		return BranchFilterResult{Kept: branches}
	}

	var kept []BranchDateInfo
	var main *BranchDateInfo
	mainKept := false
	for i := range branches {
		b := branches[i]
		if b.IsMain {
			m := b
			main = &m
		}
		if !b.AnalysisDate.IsZero() && !b.AnalysisDate.Before(*cutoff) {
			kept = append(kept, b)
			if b.IsMain {
				mainKept = true
			}
		}
	}

	if !mainKept && main != nil {
		kept = append(kept, *main)
		return BranchFilterResult{Kept: kept, ForcedMainBranch: main.Name, ForcedMainDate: main.AnalysisDate}
	}
	return BranchFilterResult{Kept: kept}
}
