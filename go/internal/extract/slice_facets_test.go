// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"fmt"
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

// TestFacetCascadeRecoversViaDirectoriesWhenRuleCellStillOverCeiling
// reproduces a real customer repro (a single systematic rule firing on
// every file of a first analysis, split across 2 directories — see
// issue #630's follow-up): a (type, severity, rule) cell that is itself
// still over the ceiling, but splits cleanly into 2 directories each
// under it.
func TestFacetCascadeRecoversViaDirectoriesWhenRuleCellStillOverCeiling(t *testing.T) {
	const (
		perDir = 6000
		total  = perDir * 2
	)
	corpus := newIssueCorpus(t)
	corpus.addBurstFull(corpusStart, perDir, "d1", "CODE_SMELL", "MINOR", "python:S1481", "pkg/a", "")
	corpus.addBurstFull(corpusStart, perDir, "d2", "CODE_SMELL", "MINOR", "python:S1481", "pkg/b", "")
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues must not fail: %v", err)
	}

	if got := sink.delivered(); got != total {
		t.Errorf("delivered: got %d, want %d (full recovery via the directories fallback)", got, total)
	}
	if recs := tracker.State().Records; len(recs) != 0 {
		t.Errorf("nothing was lost, so nothing may be recorded: got %+v", recs)
	}

	sawDirectoriesFacetProbe := false
	for _, q := range corpus.allQueries() {
		if q.Get(facetsParam) == directoriesParam && q.Get(rulesParam) == "python:S1481" {
			sawDirectoriesFacetProbe = true
			break
		}
	}
	if !sawDirectoriesFacetProbe {
		t.Errorf("expected a facets=directories probe scoped to rules=python:S1481, queries: %v", corpus.allQueries())
	}
}

// TestFacetCascadeRecoversViaFilesWhenDirectoriesDoNotHelp reproduces
// the second real customer repro: a project with no subdirectories (so
// the directories facet returns exactly one value covering everything,
// still over the ceiling), split across 100 files each under it. The
// cascade must fall through directories to files rather than giving up
// as soon as directories alone does not resolve the cell.
func TestFacetCascadeRecoversViaFilesWhenDirectoriesDoNotHelp(t *testing.T) {
	const (
		fileCount = 100
		perFile   = 119
		total     = fileCount * perFile
	)
	corpus := newIssueCorpus(t)
	for i := 0; i < fileCount; i++ {
		file := fmt.Sprintf("module_%d.py", i)
		corpus.addBurstFull(corpusStart, perFile, file, "CODE_SMELL", "MINOR", "python:S1481", "src", file)
	}
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues must not fail: %v", err)
	}

	if got := sink.delivered(); got != total {
		t.Errorf("delivered: got %d, want %d (full recovery via the files fallback)", got, total)
	}
	if recs := tracker.State().Records; len(recs) != 0 {
		t.Errorf("nothing was lost, so nothing may be recorded: got %+v", recs)
	}

	sawFilesFacetProbe := false
	for _, q := range corpus.allQueries() {
		if q.Get(facetsParam) == filesParam && q.Get(directoriesParam) == "src" {
			sawFilesFacetProbe = true
			break
		}
	}
	if !sawFilesFacetProbe {
		t.Errorf("expected a facets=files probe scoped to directories=src, queries: %v", corpus.allQueries())
	}
}

// TestFacetCascadeGivesUpCleanlyWhenFilesExceedCap is the third real
// customer repro: the same shape as the 100-files case, but 120 files —
// one more than maxFacetValues allows — so the cascade must still give
// up cleanly (capped delivery, one record) rather than fanning out 120
// follow-up requests.
func TestFacetCascadeGivesUpCleanlyWhenFilesExceedCap(t *testing.T) {
	const (
		fileCount = 120
		perFile   = 100
		total     = fileCount * perFile
	)
	corpus := newIssueCorpus(t)
	for i := 0; i < fileCount; i++ {
		file := fmt.Sprintf("module_%d.py", i)
		corpus.addBurstFull(corpusStart, perFile, file, "CODE_SMELL", "MINOR", "python:S1481", "src", file)
	}
	e, tracker := corpus.start()

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues must not fail: %v", err)
	}

	if got := sink.delivered(); got != common.ResultWindowLimit {
		t.Errorf("delivered: got %d, want %d (capped, 120 files exceeds the fan-out cap)", got, common.ResultWindowLimit)
	}
	rec := onlyRecord(t, tracker.State())
	if rec.Total != total || rec.Fetched != common.ResultWindowLimit || rec.Lost != total-common.ResultWindowLimit {
		t.Errorf("total/fetched/lost: got %d/%d/%d, want %d/%d/%d",
			rec.Total, rec.Fetched, rec.Lost, total, common.ResultWindowLimit, total-common.ResultWindowLimit)
	}
	// Gave up at the files level (120 > maxFacetValues), so no single
	// file was ever chosen — the detail reaches "directories=src" but
	// not a files= filter.
	if !strings.Contains(rec.Scope.Detail, "directories=src") || strings.Contains(rec.Scope.Detail, filesParam+"=") {
		t.Errorf("scope.Detail: got %q, want it to reach directories=src but name no single file", rec.Scope.Detail)
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

// TestFacetCascadeUsesMQRTaxonomyBetweenIntroAndToggleVersions covers
// #630's MQR-mode gap: a server at 10.2.0-10.7.x has MQR with no way to
// turn it off, so the cascade must use impactSoftwareQualities /
// impactSeverities without even asking the setting.
func TestFacetCascadeUsesMQRTaxonomyBetweenIntroAndToggleVersions(t *testing.T) {
	const (
		maintainabilityHigh = 9000
		reliabilityLow      = 6000
		total               = maintainabilityHigh + reliabilityLow
	)
	corpus := newIssueCorpus(t)
	corpus.addBurstTagged(corpusStart, maintainabilityHigh, "m", "MAINTAINABILITY", "HIGH", "")
	corpus.addBurstTagged(corpusStart, reliabilityLow, "r", "RELIABILITY", "LOW", "")
	e, tracker := corpus.start()
	e.Version = common.MustParseVersion("10.5.0")

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues must not fail: %v", err)
	}

	if got := sink.delivered(); got != total {
		t.Errorf("delivered: got %d, want %d", got, total)
	}
	if len(tracker.State().Records) != 0 {
		t.Errorf("nothing was lost, so nothing may be recorded: got %+v", tracker.State().Records)
	}
	assertUsedMQRParams(t, corpus)
}

// TestFacetCascadeUsesMQRTaxonomyWhenSettingSaysSo covers the >= 10.8.0
// case: MQR is an instance toggle, queried live via api/settings/values
// (#630).
func TestFacetCascadeUsesMQRTaxonomyWhenSettingSaysSo(t *testing.T) {
	const total = 15000
	corpus := newIssueCorpus(t)
	corpus.mqrSetting = "true"
	corpus.addBurstTagged(corpusStart, total, "burst", "SECURITY", "BLOCKER", "")
	e, tracker := corpus.start()
	e.Version = common.MustParseVersion("2026.4.0")

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues must not fail: %v", err)
	}

	// A single (quality, severity) cell at 15000 is itself still over
	// the ceiling — the point here is only that the probe used the MQR
	// param names, not that this particular shape recovers fully.
	rec := onlyRecord(t, tracker.State())
	wantDetail := "impactSoftwareQualities=SECURITY impactSeverities=BLOCKER"
	if rec.Scope.Detail != wantDetail {
		t.Errorf("scope.Detail: got %q, want %q", rec.Scope.Detail, wantDetail)
	}
	assertUsedMQRParams(t, corpus)
}

// TestFacetCascadeDefaultsToStandardWhenSettingIsAbsent covers a server
// new enough to have the toggle (>= 10.8.0) that nonetheless answers
// with no matching setting (e.g. never explicitly configured) — the
// cascade must default to Standard Experience's types/severities
// rather than guessing MQR (#630).
func TestFacetCascadeDefaultsToStandardWhenSettingIsAbsent(t *testing.T) {
	const (
		bugMajor   = 9000
		smellMinor = 6000
		total      = bugMajor + smellMinor
	)
	corpus := newIssueCorpus(t) // mqrSetting left "" — the setting does not exist in the response
	corpus.addBurstTagged(corpusStart, bugMajor, "bug", "BUG", "MAJOR", "")
	corpus.addBurstTagged(corpusStart, smellMinor, "smell", "CODE_SMELL", "MINOR", "")
	e, tracker := corpus.start()
	e.Version = common.MustParseVersion("2026.4.0")

	var sink issueCollector
	if err := fetchProjectIssues(ctx(t), e, "p1", "main", taskIssueParams(), sink.sink); err != nil {
		t.Fatalf("fetchProjectIssues must not fail: %v", err)
	}

	if got := sink.delivered(); got != total {
		t.Errorf("delivered: got %d, want %d", got, total)
	}
	if len(tracker.State().Records) != 0 {
		t.Errorf("nothing was lost, so nothing may be recorded: got %+v", tracker.State().Records)
	}
	for _, q := range corpus.allQueries() {
		if q.Get(impactSoftwareQualitiesParam) != "" || q.Get(impactSeveritiesParam) != "" {
			t.Errorf("expected Standard taxonomy only, got an MQR-tagged query: %v", q)
		}
	}
}

// assertUsedMQRParams fails unless at least one query carried the MQR
// taxonomy's type param, proving the cascade actually chose it rather
// than happening to recover via some other path.
func assertUsedMQRParams(t *testing.T, corpus *issueCorpus) {
	t.Helper()
	for _, q := range corpus.allQueries() {
		if q.Get(impactSoftwareQualitiesParam) != "" {
			return
		}
	}
	t.Errorf("expected at least one impactSoftwareQualities-scoped query, got: %v", corpus.allQueries())
}
