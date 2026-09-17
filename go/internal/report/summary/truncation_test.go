// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// writeTruncationArtefact writes the extract-side truncation artefact
// into an extract directory the way extract's flush does, so the report
// collector is exercised against the real on-disk shape rather than a
// hand-built struct.
func writeTruncationArtefact(t *testing.T, extractDir string, state common.TruncationState) {
	t.Helper()
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir extract: %v", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal truncation state: %v", err)
	}
	path := filepath.Join(extractDir, common.TruncationEventsFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// truncationBullets returns the Limitations bullets produced by the
// truncation collector, identified by the handful of phrases only its
// sentences use. Filtering keeps the assertions immune to unrelated
// limitation bullets appearing in the same report.
func truncationBullets(t *testing.T, limitations []string) []string {
	t.Helper()
	var out []string
	for _, l := range limitations {
		if strings.Contains(l, "extract") &&
			(strings.Contains(l, "missing from the extract") ||
				strings.Contains(l, "unaccounted for") ||
				strings.Contains(l, "a surplus rather than a shortfall") ||
				strings.Contains(l, "may be absent from the migration") ||
				strings.Contains(l, "could not be read")) {
			out = append(out, l)
		}
	}
	return out
}

// summaryFor runs the real collector over a temp export dir holding one
// extract run, which is what proves the bullet is actually wired into
// collectLimitations (plan step 11) and not merely reachable.
func summaryFor(t *testing.T, state common.TruncationState) *MigrationSummary {
	t.Helper()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	extractDir := filepath.Join(dir, "2026-08-20-0001")
	writeExtractMeta(t, extractDir, "https://sq.example.com")
	writeTruncationArtefact(t, extractDir, state)

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	return summary
}

// #574: the report's only structural view of an issue count is
// len(what extract wrote), so a project whose fetch hit the
// 10,000-result ceiling used to appear complete. The bullet must name
// the task, the affected project/branch and the exact number of results
// that were left behind — an approximation here would be worse than
// silence, because the operator uses it to decide whether to re-run.
func TestReportsIssueCeilingTruncationWithExactCounts(t *testing.T) {
	summary := summaryFor(t, common.TruncationState{
		Records: []common.TruncationRecord{{
			Endpoint:   "api/issues/search",
			Reason:     common.ReasonPageLimitClamp,
			Scope:      common.TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "alpha", Branch: "main"},
			Total:      14903,
			TotalKnown: true,
			Fetched:    10000,
			Lost:       4903,
		}},
	})

	bullets := truncationBullets(t, summary.Limitations)
	if len(bullets) != 1 {
		t.Fatalf("expected exactly one truncation bullet, got %v (all: %v)", bullets, summary.Limitations)
	}
	got := bullets[0]
	for _, want := range []string{
		"getProjectIssuesFull",
		"10,000-result search ceiling",
		"4,903 result(s) are missing from the extract and cannot be migrated",
		"Affected: alpha@main",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("bullet is missing %q: got %q", want, got)
		}
	}
	if strings.Contains(got, "10,000 result(s)") {
		t.Errorf("the ceiling must not be reported as the lost count: got %q", got)
	}
}

// #574: /api/hotspots/search has no createdAfter/createdBefore and
// silently ignores unknown parameters, so its ceiling truncation can
// never be worked around by slicing. The bullet must say the query
// could not be narrowed rather than implying a re-run would recover the
// remainder, and it must stay attributed per status — the task issues
// one request per status, so two records with different Detail values
// are two real observations.
func TestSaysHotspotTruncationCannotBeDateSliced(t *testing.T) {
	summary := summaryFor(t, common.TruncationState{
		Records: []common.TruncationRecord{
			{
				Endpoint:   "api/hotspots/search",
				Reason:     common.ReasonPageLimitClamp,
				Scope:      common.TruncationScope{Task: "getProjectHotspotsFull", ProjectKey: "alpha", Branch: "main", Detail: "status=TO_REVIEW"},
				Total:      12500,
				TotalKnown: true,
				Fetched:    10000,
				Lost:       2500,
			},
			{
				Endpoint:   "api/hotspots/search",
				Reason:     common.ReasonPageLimitClamp,
				Scope:      common.TruncationScope{Task: "getProjectHotspotsFull", ProjectKey: "alpha", Branch: "main", Detail: "status=REVIEWED"},
				Total:      10600,
				TotalKnown: true,
				Fetched:    10000,
				Lost:       600,
			},
		},
	})

	bullets := truncationBullets(t, summary.Limitations)
	if len(bullets) != 1 {
		t.Fatalf("expected one bullet for one (task, reason) pair, got %v", bullets)
	}
	got := bullets[0]
	for _, want := range []string{
		"getProjectHotspotsFull",
		"could not be narrowed into smaller date windows",
		"3,100 result(s) are missing from the extract",
		"alpha@main (status=REVIEWED)",
		"alpha@main (status=TO_REVIEW)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("bullet is missing %q: got %q", want, got)
		}
	}
}

// #574 / plan F10 — the false-causal-claim regression. A
// reconciliation shortfall and a genuinely unfetchable leaf window
// share the task name "getProjectIssuesFull". Grouped by task alone
// they would be summed into one sentence blaming the 10,000-result
// ceiling for both, reporting 12,007 lost issues when only 12,000 are
// known to be missing and asserting absence for a discrepancy whose
// cause was, by definition, not identified. The count_drift bullet must
// stay a separate, purely accounting statement.
func TestCountDriftBulletDoesNotClaimTheCeiling(t *testing.T) {
	summary := summaryFor(t, common.TruncationState{
		Records: []common.TruncationRecord{
			{
				Endpoint:    "api/issues/search",
				Reason:      common.ReasonAtomicWindow,
				Scope:       common.TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "alpha", Branch: "main"},
				Total:       22000,
				TotalKnown:  true,
				Fetched:     10000,
				Lost:        12000,
				WindowStart: "2025-12-30T03:02:17+0000",
				WindowEnd:   "2025-12-30T03:02:18+0000",
			},
			{
				Endpoint:   "api/issues/search",
				Reason:     common.ReasonCountDrift,
				Scope:      common.TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "alpha", Branch: "main"},
				Total:      22000,
				TotalKnown: true,
				Lost:       7,
			},
		},
	})

	bullets := truncationBullets(t, summary.Limitations)
	if len(bullets) != 2 {
		t.Fatalf("expected one bullet per (task, reason) pair, got %d: %v", len(bullets), bullets)
	}

	var drift, atomic string
	for _, b := range bullets {
		switch {
		case strings.Contains(b, "could not be reconciled"):
			drift = b
		case strings.Contains(b, "share one creation timestamp"):
			atomic = b
		}
	}
	if drift == "" || atomic == "" {
		t.Fatalf("expected a drift bullet and an atomic-window bullet, got %v", bullets)
	}

	if !strings.Contains(drift, "the source issue count changed during extraction and could not be reconciled (7 issue(s) unaccounted for)") {
		t.Errorf("drift bullet must state the accounting discrepancy verbatim: got %q", drift)
	}
	for _, forbidden := range []string{"10,000", "ceiling", "missing from the extract"} {
		if strings.Contains(drift, forbidden) {
			t.Errorf("drift bullet must not contain %q — it is an accounting statement, not a data-loss claim: got %q", forbidden, drift)
		}
	}
	if !strings.Contains(atomic, "12,000 issue(s) are missing from the extract") {
		t.Errorf("atomic bullet must report 12,000, not 12,007: got %q", atomic)
	}
	if strings.Contains(atomic, "12,007") {
		t.Errorf("the drift must never be summed into the ceiling loss: got %q", atomic)
	}
}

// #574: a slicing walk that dies part-way has already written real
// chunks to disk. Reporting it as a plain ceiling loss would tell the
// operator the data is unreachable, when in fact the run needs
// re-running to complete a set that is partly there. The bullet must
// say both halves of that, and must not guess at a cause the slicer did
// not establish.
func TestIncompleteSliceBulletSaysTheSetIsIncomplete(t *testing.T) {
	summary := summaryFor(t, common.TruncationState{
		Records: []common.TruncationRecord{{
			Endpoint:   "api/issues/search",
			Reason:     common.ReasonIncompleteSlice,
			Scope:      common.TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "beta", Branch: "develop"},
			Total:      14903,
			TotalKnown: true,
			Fetched:    6200,
			Lost:       8703,
		}},
	})

	bullets := truncationBullets(t, summary.Limitations)
	if len(bullets) != 1 {
		t.Fatalf("expected exactly one truncation bullet, got %v", bullets)
	}
	got := bullets[0]
	for _, want := range []string{
		"stopped part-way with an error",
		"already written are on disk but the set is incomplete",
		"8,703 issue(s) unaccounted for",
		"Affected: beta@develop",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("bullet is missing %q: got %q", want, got)
		}
	}
	if strings.Contains(got, "ceiling") {
		t.Errorf("an interrupted walk is not a ceiling hit: got %q", got)
	}
}

// #574: a run that truncated nothing writes no artefact at all, so the
// collector must treat a missing file as "nothing was truncated" rather
// than as an error — and must add no bullet. A clean migration report
// gaining a data-loss limitation would destroy the operator's trust in
// the section for every run.
func TestNoBulletWithoutTheArtefact(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	extractDir := filepath.Join(dir, "2026-08-20-0001")
	writeExtractMeta(t, extractDir, "https://sq.example.com")

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	if bullets := truncationBullets(t, summary.Limitations); len(bullets) != 0 {
		t.Errorf("a run with no truncation artefact must add no bullet, got %v", bullets)
	}
}

// #574: records are grouped by (task, reason) and the scope list is
// capped the way formatLimitationUserList caps logins (#475). Without
// the cap, an instance where hundreds of projects hit the ceiling
// produced one unreadable multi-page bullet; without the grouping, it
// produced one bullet per project.
func TestGroupsByTaskAndReasonAndCapsTheScopeList(t *testing.T) {
	state := common.TruncationState{}
	for i := 1; i <= 12; i++ {
		state.Records = append(state.Records, common.TruncationRecord{
			Endpoint:   "api/hotspots/search",
			Reason:     common.ReasonPageLimitClamp,
			Scope:      common.TruncationScope{Task: "getProjectHotspotsFull", ProjectKey: fmt.Sprintf("proj%02d", i), Branch: "main"},
			Total:      10100,
			TotalKnown: true,
			Fetched:    10000,
			Lost:       100,
		})
	}
	// A second task with the same reason must not be folded in.
	state.Records = append(state.Records, common.TruncationRecord{
		Endpoint:   "api/measures/component_tree",
		Reason:     common.ReasonPageLimitClamp,
		Scope:      common.TruncationScope{Task: "getProjectMeasures", ProjectKey: "alpha", Branch: "main"},
		Total:      10500,
		TotalKnown: true,
		Fetched:    10000,
		Lost:       500,
	})

	summary := summaryFor(t, state)
	bullets := truncationBullets(t, summary.Limitations)
	if len(bullets) != 2 {
		t.Fatalf("expected one bullet per task, got %d: %v", len(bullets), bullets)
	}

	var hotspots, measures string
	for _, b := range bullets {
		switch {
		case strings.Contains(b, "getProjectHotspotsFull"):
			hotspots = b
		case strings.Contains(b, "getProjectMeasures"):
			measures = b
		}
	}
	if hotspots == "" || measures == "" {
		t.Fatalf("expected a bullet for each task, got %v", bullets)
	}

	if !strings.Contains(hotspots, "1,200 result(s) are missing from the extract") {
		t.Errorf("expected the 12 records summed to 1,200: got %q", hotspots)
	}
	if !strings.Contains(hotspots, "first 10: proj01@main, proj02@main, proj03@main, proj04@main, proj05@main, proj06@main, proj07@main, proj08@main, proj09@main, proj10@main (and 2 more; see extract_truncation.json in the extract directory)") {
		t.Errorf("expected the scope list capped at 10 with a remainder count: got %q", hotspots)
	}
	for _, beyondCap := range []string{"proj11", "proj12"} {
		if strings.Contains(hotspots, beyondCap) {
			t.Errorf("expected %s to be omitted past the cap: got %q", beyondCap, hotspots)
		}
	}
	if !strings.Contains(measures, "500 result(s) are missing from the extract") {
		t.Errorf("the second task's count must stay its own: got %q", measures)
	}
}

// #574: an artefact that exists but cannot be parsed must never read as
// a clean run. Silently treating a corrupt file as "nothing was
// truncated" is the exact silent-loss shape this feature exists to end,
// so the report says it cannot confirm completeness instead.
func TestUnreadableArtefactSaysTheReportCannotConfirm(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	extractDir := filepath.Join(dir, "2026-08-20-0001")
	writeExtractMeta(t, extractDir, "https://sq.example.com")
	if err := os.WriteFile(filepath.Join(extractDir, common.TruncationEventsFile), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write artefact: %v", err)
	}

	summary, err := CollectSummary(runDir, dir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	bullets := truncationBullets(t, summary.Limitations)
	if len(bullets) != 1 {
		t.Fatalf("expected one degraded bullet, got %v", bullets)
	}
	for _, want := range []string{
		filepath.Join("2026-08-20-0001", common.TruncationEventsFile),
		"could not be read",
		"cannot confirm whether every source item reached the extract",
	} {
		if !strings.Contains(bullets[0], want) {
			t.Errorf("bullet is missing %q: got %q", want, bullets[0])
		}
	}
}

// windowedClampClaim is one of the two sentences a mixed
// page_limit_clamp group must produce, expressed as a case so the loop
// body can move into a helper without losing the reason each phrase is
// required or forbidden.
type windowedClampClaim struct {
	name            string
	marker          string
	wants           []string
	forbidden       []string
	forbiddenReason string
}

// onlyTruncationBulletContaining returns the single bullet carrying
// marker, failing when zero or several do. Demanding exactly one keeps
// the two clamp claims distinguishable: a run that emitted the same
// sentence twice must not read as a clean split.
func onlyTruncationBulletContaining(t *testing.T, bullets []string, marker string) string {
	t.Helper()
	var found []string
	for _, b := range bullets {
		if strings.Contains(b, marker) {
			found = append(found, b)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one bullet containing %q, got %d: %v", marker, len(found), bullets)
	}
	return found[0]
}

// assertWindowedClampClaim checks one clamp claim's exact phrases, both
// the ones it must state and the ones that would give the operator the
// other cause's remedy.
func assertWindowedClampClaim(t *testing.T, bullets []string, tc windowedClampClaim) {
	t.Helper()
	got := onlyTruncationBulletContaining(t, bullets, tc.marker)
	for _, want := range tc.wants {
		if !strings.Contains(got, want) {
			t.Errorf("clamp bullet is missing %q: got %q", want, got)
		}
	}
	for _, forbidden := range tc.forbidden {
		if strings.Contains(got, forbidden) {
			t.Errorf("clamp bullet must not contain %q: %s: got %q", forbidden, tc.forbiddenReason, got)
		}
	}
}

// assertNoTruncationBulletContains fails when any bullet carries one of
// the phrases, reporting why that phrase is wrong wherever it appears.
func assertNoTruncationBulletContains(t *testing.T, bullets []string, why string, phrases ...string) {
	t.Helper()
	for _, phrase := range phrases {
		for _, b := range bullets {
			if strings.Contains(b, phrase) {
				t.Errorf("%s (found %q): got %q", why, phrase, b)
			}
		}
	}
}

// #574: page_limit_clamp means two different things, and the bullet
// used to state one cause for both while carrying the other's evidence
// beside it. A clamp with no window bounds is a query that could never
// be narrowed; a clamp WITH window bounds is a date window the slicer
// had already probed and found small enough, which grew past the
// ceiling before the fetch because issues were created during the run.
// The first is permanent, the second is recovered by re-running, so
// summing them into one sentence gives the operator the wrong cause
// and the wrong remedy for half the loss.
func TestWindowedAndUnwindowedClampsAreSeparateClaims(t *testing.T) {
	summary := summaryFor(t, common.TruncationState{
		Records: []common.TruncationRecord{
			{
				Endpoint:   "api/issues/search",
				Reason:     common.ReasonPageLimitClamp,
				Scope:      common.TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "alpha", Branch: "main"},
				Total:      14903,
				TotalKnown: true,
				Fetched:    10000,
				Lost:       4903,
			},
			{
				Endpoint:    "api/issues/search",
				Reason:      common.ReasonPageLimitClamp,
				Scope:       common.TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "beta", Branch: "main"},
				Total:       10250,
				TotalKnown:  true,
				Fetched:     10000,
				Lost:        250,
				WindowStart: "2025-09-05T17:29:07+0000",
				WindowEnd:   "2025-09-05T17:29:08+0000",
			},
		},
	})

	bullets := truncationBullets(t, summary.Limitations)
	if len(bullets) != 2 {
		t.Fatalf("expected one bullet per window-presence, got %d: %v", len(bullets), bullets)
	}

	cases := []windowedClampClaim{
		{
			name:   "the windowed clamp blames issues created during the run and points at a re-run",
			marker: "still fitted when the slicer probed it",
			wants: []string{
				"issues created during the extraction",
				"250 result(s) are missing from the extract",
				"re-running the extract recovers them",
				"beta@main 2025-09-05T17:29:07+0000 to 2025-09-05T17:29:08+0000",
			},
			forbidden:       []string{"could not be narrowed"},
			forbiddenReason: "a windowed clamp WAS narrowed into a date window, so it must not claim otherwise",
		},
		{
			name:   "the unwindowed clamp keeps its own count and offers no re-run remedy",
			marker: "could not be narrowed into smaller date windows",
			wants: []string{
				"4,903 result(s) are missing from the extract and cannot be migrated",
			},
			forbidden:       []string{"re-running"},
			forbiddenReason: "a query that cannot be narrowed is not fixed by a re-run",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertWindowedClampClaim(t, bullets, tc)
		})
	}

	t.Run("neither bullet sums the two losses", func(t *testing.T) {
		assertNoTruncationBulletContains(t, bullets,
			"the two causes must never be summed into one claim", "5,153", "5153")
	})
}

// #574: the slicer clamps a NEGATIVE unexplained drift to zero before
// writing its count_drift record, so the bullet rendered a surplus
// ("the walk collected three issues more than the source said it had")
// as "an unknown number of issues unaccounted for". That reads as
// missing data, which is the exact opposite of what happened. The
// signed residual lives in the reconciliation, so the bullet reads it
// from there and names the surplus for what it is.
func TestCountDriftSurplusIsNotReportedAsAShortfall(t *testing.T) {
	scope := common.TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "alpha", Branch: "main"}
	summary := summaryFor(t, common.TruncationState{
		Records: []common.TruncationRecord{{
			Endpoint:   "api/issues/search",
			Reason:     common.ReasonCountDrift,
			Scope:      scope,
			Total:      14903,
			TotalKnown: true,
			Fetched:    14906,
			// What the slicer writes for a negative residual: the
			// magnitude is lost on the way to the artefact.
			Lost: 0,
		}},
		Reconciliations: []common.Reconciliation{{
			Scope:       scope,
			TotalBefore: 14903,
			TotalAfter:  14903,
			Unique:      14906,
		}},
	})

	bullets := truncationBullets(t, summary.Limitations)
	if len(bullets) != 1 {
		t.Fatalf("expected exactly one truncation bullet, got %v (all: %v)", bullets, summary.Limitations)
	}
	got := bullets[0]
	for _, want := range []string{
		"collected 3 issue(s) more than the source reported",
		"a surplus rather than a shortfall",
		"nothing is missing on this account",
		"not a confirmed loss of data",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("surplus bullet is missing %q: got %q", want, got)
		}
	}
	for _, forbidden := range []string{"unaccounted for", "an unknown number", "missing from the extract"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("a surplus must never be worded as missing data (%q): got %q", forbidden, got)
		}
	}
}

// #574: the predictive report pipes every Limitations bullet through
// toPredictiveTense, whose rules turn "were not " into "will not be ".
// That rewrote the settled "4,903 result(s) were not extracted" into
// "will not be extracted", forecasting a loss that had already
// happened in an extract that had already run. Truncation is past
// tense in both report modes, so no lead may contain a phrase any rule
// matches. This test walks every reason, both window shapes, and the
// unreadable-artefact sentence.
func TestTruncationLeadsSurvivePredictiveTense(t *testing.T) {
	reasons := []common.TruncationReason{
		common.ReasonPageLimitClamp,
		common.ReasonAtomicWindow,
		common.ReasonDatesIgnored,
		common.ReasonMaxDepth,
		common.ReasonIncompleteSlice,
		common.ReasonCountDrift,
		common.ReasonUnknownTotal,
		common.TruncationReason("a_reason_from_the_future"),
	}
	// A slice, not a map: subtest names have to come out in the same
	// order on every run.
	groups := []struct {
		shape string
		group *truncationGroup
	}{
		{"a known shortfall", &truncationGroup{lost: 7919}},
		{"an unknown residual", &truncationGroup{lost: 0}},
		{"a surplus", &truncationGroup{surplus: 3}},
		{"both directions", &truncationGroup{lost: 4, surplus: 3}},
		{"a capped scope list", &truncationGroup{lost: 1200, scopes: []string{"alpha@main", "beta@main"}}},
	}

	cases := []struct {
		name string
		line string
	}{}
	for _, reason := range reasons {
		for _, windowed := range []bool{false, true} {
			for _, g := range groups {
				key := truncationGroupKey{task: "getProjectIssuesFull", reason: reason, windowed: windowed}
				cases = append(cases, struct {
					name string
					line string
				}{
					name: fmt.Sprintf("%s with %s (windowed=%v)", reason, g.shape, windowed),
					line: truncationLead(key, g.group),
				})
			}
		}
	}
	cases = append(cases, struct {
		name string
		line string
	}{
		name: "the unreadable-artefact sentence",
		line: "The extract's truncation record (2026-08-20-0001/extract_truncation.json) could not be read (unexpected end of JSON input), so this report cannot confirm whether every source item reached the extract.",
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toPredictiveTense(tc.line, true); got != tc.line {
				t.Errorf("the predictive rewriter rephrased a settled loss as a forecast:\n  before: %s\n  after:  %s", tc.line, got)
			}
		})
	}
}

// TestDatesRejectedBulletSaysTheSourceRefusedTheFilter guards #574. When
// the source answers a creation-date filter with an HTTP error but
// serves the same query without one, the slicer degrades to the capped
// fetch and records ReasonDatesRejected. That reason had no case in
// truncationLead, so it fell through to the generic default and the
// report named the machine-readable reason string instead of the cause.
// The bullet must state the refusal, and must not claim the source
// ignored the filter — that is the opposite failure, and it has its own
// reason and its own sentence.
func TestDatesRejectedBulletSaysTheSourceRefusedTheFilter(t *testing.T) {
	summary := summaryFor(t, common.TruncationState{
		Records: []common.TruncationRecord{
			{
				Endpoint:   "api/issues/search",
				Reason:     common.ReasonDatesRejected,
				Scope:      common.TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "alpha", Branch: "main"},
				Total:      26162,
				TotalKnown: true,
				Fetched:    10000,
				Lost:       16162,
			},
		},
	})

	bullets := truncationBullets(t, summary.Limitations)
	if len(bullets) != 1 {
		t.Fatalf("expected one bullet, got %v", bullets)
	}
	got := bullets[0]
	for _, want := range []string{"rejected", "16,162", "fell back"} {
		if !strings.Contains(got, want) {
			t.Errorf("bullet should contain %q, got: %s", want, got)
		}
	}
	if strings.Contains(got, string(common.ReasonDatesRejected)) {
		t.Errorf("bullet should name the cause, not the raw reason string, got: %s", got)
	}
	if strings.Contains(got, "ignores them") {
		t.Errorf("bullet must not claim the source ignored the filter; it rejected it. got: %s", got)
	}
}
