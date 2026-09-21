// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"fmt"
	"time"
)

// BranchAnalyzedAfterLayout is the only format --branch_analyzed_after (and
// its config-file equivalent) accepts (#583).
const BranchAnalyzedAfterLayout = "2006-01-02"

// BranchAnalyzedAfterWarnAgeDays is the age, in days, beyond which a
// --branch_analyzed_after cutoff is unlikely to exclude many branches — past
// this point the tool warns rather than silently doing nothing useful (#583).
const BranchAnalyzedAfterWarnAgeDays = 730

// ForcedMainBranchLogMessage is logged whenever --branch_analyzed_after
// would otherwise have excluded every branch of a project and the main
// branch was force-included anyway (#583). Both the emitting call sites
// (extract's buildBranchMap, migrate's filterBranchesByAnalyzedAfter) and
// the report pipeline that parses run logs (report/summary's
// eventAggregator) key off this exact string, so it lives here once
// rather than being duplicated/hand-copied at each site.
//
// NOTE: only the migrate-phase emission reaches the report. run_events.jsonl
// — the file report/summary's collectRunEvents reads — is written solely by
// RunMigrate's teeing event handler; extract logs to slog.Default() with no
// collector, so an extract-only force-inclusion appears on stderr only.
const ForcedMainBranchLogMessage = "force-including main branch: does not meet --branch_analyzed_after filter"

// ParseBranchAnalyzedAfter parses the --branch_analyzed_after flag or the
// branch_analyzed_after config-file value (#583). An empty string means
// "unset" — no filter, every branch is selected — and returns a nil cutoff
// with no error. Any non-empty value must be exactly BranchAnalyzedAfterLayout
// (YYYY-MM-DD); anything else is an explicit, operator-facing error.
func ParseBranchAnalyzedAfter(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(BranchAnalyzedAfterLayout, raw)
	if err != nil {
		return nil, fmt.Errorf("invalid branch_analyzed_after value %q: expected YYYY-MM-DD format", raw)
	}
	return &t, nil
}

// IsBranchAnalyzedAfterStale reports whether cutoff is more than
// BranchAnalyzedAfterWarnAgeDays days in the past relative to now (#583) —
// callers should log an advisory in this case, since the filter is unlikely
// to exclude much.
func IsBranchAnalyzedAfterStale(cutoff, now time.Time) bool {
	return now.Sub(cutoff) > BranchAnalyzedAfterWarnAgeDays*24*time.Hour
}

// ParseAnalysisDate parses a branch's analysisDate as returned by
// api/project_branches/list, in RFC3339 or the legacy UTC-offset format
// SonarQube also emits. Returns the zero time on an empty or unparseable
// input, matching a branch that was never analyzed.
func ParseAnalysisDate(dateStr string) time.Time {
	if dateStr == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, dateStr)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05-0700", dateStr)
	}
	if err != nil {
		return time.Time{}
	}
	return t
}
