// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"strconv"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// TestFacetCascadeRecoversAtomicSecondViaTypesAndSeverities is the
// primary case #630 asks for: a one-second burst over the ceiling, but
// split across (type, severity) cells that are each individually under
// it. Every issue must now be recovered — before this change,
// fetchAtomicWindow capped delivery at common.ResultWindowLimit and
// recorded the rest as lost.
func TestFacetCascadeRecoversAtomicSecondViaTypesAndSeverities(t *testing.T) {
	const (
		bugMajor   = 9000
		smellMinor = 6000
		total      = bugMajor + smellMinor
	)
	corpus := newIssueCorpus(t)
	corpus.addBurstTagged(corpusStart, bugMajor, "bug", "BUG", "MAJOR", "")
	corpus.addBurstTagged(corpusStart, smellMinor, "smell", "CODE_SMELL", "MINOR", "")
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues must not fail: %v", err)
	}

	if got := sink.delivered(); got != total {
		t.Errorf("delivered: got %d, want %d (full recovery via types x severities)", got, total)
	}
	for key, n := range sink.keyCounts() {
		if n != 1 {
			t.Errorf("issue %s delivered %d times, want 1", key, n)
		}
	}

	state := tracker.State()
	if len(state.Records) != 0 {
		t.Errorf("nothing was lost, so nothing may be recorded: got %+v", state.Records)
	}
	audit := onlyReconciliation(t, state)
	if audit.TotalBefore != total || audit.Unique != total {
		t.Errorf("reconciliation: before=%d unique=%d, want %d for both", audit.TotalBefore, audit.Unique, total)
	}
	if audit.Drift() != 0 || audit.UnexplainedDrift() != 0 {
		t.Errorf("reconciliation: drift=%d unexplained=%d, want 0 for both", audit.Drift(), audit.UnexplainedDrift())
	}
}

// TestFacetCascadeFallsBackToRulesWhenSeverityCellStillOverCeiling
// covers #630's explicit ask: when a (type, severity) cell is itself
// still over the ceiling, the cascade falls back to splitting by rule.
func TestFacetCascadeFallsBackToRulesWhenSeverityCellStillOverCeiling(t *testing.T) {
	const (
		perRule = 9000
		total   = perRule * 2
	)
	corpus := newIssueCorpus(t)
	corpus.addBurstTagged(corpusStart, perRule, "r1", "BUG", "MAJOR", "java:S1")
	corpus.addBurstTagged(corpusStart, perRule, "r2", "BUG", "MAJOR", "java:S2")
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues must not fail: %v", err)
	}

	if got := sink.delivered(); got != total {
		t.Errorf("delivered: got %d, want %d (full recovery via the rules fallback)", got, total)
	}
	state := tracker.State()
	if len(state.Records) != 0 {
		t.Errorf("nothing was lost, so nothing may be recorded: got %+v", state.Records)
	}

	// Confirm the rules fallback was actually exercised, not just that
	// the end result happens to look complete.
	sawRulesFacetProbe := false
	for _, q := range corpus.allQueries() {
		if q.Get(facetsParam) == rulesParam && q.Get(typesParam) == "BUG" && q.Get(severitiesParam) == "MAJOR" {
			sawRulesFacetProbe = true
			break
		}
	}
	if !sawRulesFacetProbe {
		t.Errorf("expected a facets=rules probe scoped to types=BUG&severities=MAJOR, queries: %v", corpus.allQueries())
	}
}

// TestFacetCascadeTerminalGiveUpWhenRuleCellStillOverCeiling covers the
// cascade's true terminal case: a single (type, severity, rule) cell
// alone exceeds the ceiling. The existing atomic_window contract still
// applies — capped delivery, one record — now scoped to the exact
// facet cell via Scope.Detail rather than the whole second.
func TestFacetCascadeTerminalGiveUpWhenRuleCellStillOverCeiling(t *testing.T) {
	const total = 12000
	corpus := newIssueCorpus(t)
	corpus.addBurstTagged(corpusStart, total, "burst", "BUG", "MAJOR", "java:S1")
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues must not fail: %v", err)
	}

	if got := sink.delivered(); got != common.ResultWindowLimit {
		t.Errorf("delivered: got %d, want %d (capped, facet slicing also exhausted)", got, common.ResultWindowLimit)
	}

	state := tracker.State()
	rec := onlyRecord(t, state)
	if rec.Reason != common.ReasonAtomicWindow {
		t.Errorf("reason: got %q, want %q", rec.Reason, common.ReasonAtomicWindow)
	}
	wantDetail := "types=BUG severities=MAJOR rules=java:S1"
	if rec.Scope.Detail != wantDetail {
		t.Errorf("scope.Detail: got %q, want %q", rec.Scope.Detail, wantDetail)
	}
	if rec.Total != total || !rec.TotalKnown {
		t.Errorf("total: got %d (known=%t), want %d (known=true)", rec.Total, rec.TotalKnown, total)
	}
	if rec.Fetched != common.ResultWindowLimit || rec.Lost != total-common.ResultWindowLimit {
		t.Errorf("fetched/lost: got %d/%d, want %d/%d",
			rec.Fetched, rec.Lost, common.ResultWindowLimit, total-common.ResultWindowLimit)
	}
}

// TestFacetCascadeGivesUpCleanlyWhenRulesFacetTooLarge covers the
// defensive cap mirroring sonar-tools' own _MAX_FACETS=100 safeguard
// (#630): a (type, severity) cell spread evenly across >= maxFacetValues
// distinct rules is evidence the rules dimension will not usefully
// partition the second, so the cascade gives up on that cell rather
// than firing 100+ follow-up requests.
func TestFacetCascadeGivesUpCleanlyWhenRulesFacetTooLarge(t *testing.T) {
	const (
		ruleCount = maxFacetValues + 1
		perRule   = 150 // ruleCount * perRule > ceiling, forcing the rules fallback
		total     = ruleCount * perRule
	)
	corpus := newIssueCorpus(t)
	for i := 0; i < ruleCount; i++ {
		rule := "java:S" + strconv.Itoa(i)
		corpus.addBurstTagged(corpusStart, perRule, "r"+strconv.Itoa(i), "BUG", "MAJOR", rule)
	}
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues must not fail: %v", err)
	}

	if got := sink.delivered(); got != common.ResultWindowLimit {
		t.Errorf("delivered: got %d, want %d (capped, too many distinct rules to fan out)", got, common.ResultWindowLimit)
	}
	rec := onlyRecord(t, tracker.State())
	if rec.Reason != common.ReasonAtomicWindow {
		t.Errorf("reason: got %q, want %q", rec.Reason, common.ReasonAtomicWindow)
	}
	if !strings.HasPrefix(rec.Scope.Detail, "types=BUG severities=MAJOR") {
		t.Errorf("scope.Detail: got %q, want it scoped to types=BUG severities=MAJOR (no single rule chosen)", rec.Scope.Detail)
	}
	if rec.Total != total {
		t.Errorf("total: got %d, want %d", rec.Total, total)
	}

	// Exactly one rules-facet probe: the cap must fire on first sight of
	// >= maxFacetValues distinct values, not after fetching each one.
	rulesProbes := 0
	for _, q := range corpus.allQueries() {
		if q.Get(facetsParam) == rulesParam {
			rulesProbes++
		}
	}
	if rulesProbes != 1 {
		t.Errorf("expected exactly 1 rules-facet probe, got %d: %v", rulesProbes, corpus.allQueries())
	}
}
