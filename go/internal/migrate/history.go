// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

// resolveMigrateHistory mirrors resolveFastSync for the migrate_history
// tri-state (#554): the target block's value wins when explicitly set,
// else the top-level value, else the default (false — no history
// migration, the pre-#554 behavior).
func resolveMigrateHistory(target, top *FlexibleBool) bool {
	if target != nil && target.Set {
		return target.Value
	}
	if top != nil && top.Set {
		return top.Value
	}
	return false
}

// historySnapshot is one extracted historical analysis point for a
// project+branch: its date, the project version recorded at that analysis,
// and the project-level measures as of that point (#554).
type historySnapshot struct {
	Date           time.Time
	ProjectVersion string
	Measures       []scanreport.MeasureInput
}

// loadExtractedAnalysisHistory reads the getProjectAnalysisHistory extract
// records for one project+branch (written only when extract ran with
// --migrate_history), sorted oldest to newest. Returns nil when no history
// was extracted for this project+branch — including every run where
// --migrate_history wasn't passed to extract, which is what keeps this
// feature a true no-op for everyone who doesn't opt in.
func loadExtractedAnalysisHistory(e *Executor, serverURL, serverKey, branch string) []historySnapshot {
	scope := extractScope{ServerURL: serverURL, ProjectKey: serverKey, Branch: branch}
	var out []historySnapshot
	for item := range scopedExtractItems(e, "getProjectAnalysisHistory", scope) {
		date := parseISODate(extractField(item.Data, "date"))
		if date.IsZero() {
			continue
		}
		out = append(out, historySnapshot{
			Date:           date,
			ProjectVersion: extractField(item.Data, "projectVersion"),
			Measures:       extractHistoryMeasures(item.Data, serverKey),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

// extractHistoryMeasures parses the "measures":[{"metric":..,"value":..}]
// array a getProjectAnalysisHistory record carries into MeasureInputs
// attributed to the project's root component (there are no file components
// in a historical snapshot report — see migrateBranchHistory).
func extractHistoryMeasures(data json.RawMessage, cloudProjectKey string) []scanreport.MeasureInput {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	raw, ok := obj["measures"]
	if !ok {
		return nil
	}
	var arr []struct {
		Metric string `json:"metric"`
		Value  string `json:"value"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]scanreport.MeasureInput, 0, len(arr))
	for _, m := range arr {
		if m.Metric == "" || m.Value == "" {
			continue
		}
		out = append(out, scanreport.MeasureInput{Component: cloudProjectKey, MetricKey: m.Metric, Value: m.Value})
	}
	return out
}

// migrateBranchHistory replays a project's extracted historical analysis
// snapshots as separate, backdated analyses on the target, oldest to
// newest (#554, PoC).
//
// Main branch only, by design: the create-analysis handshake (see
// preCreateBranchAnalysis) exists specifically to anchor a NON-main branch
// on the target before its first report is accepted, and generalizing that
// per historical point for non-main branches is real additional complexity
// this PoC deliberately doesn't take on (see the PR description's "known
// limitations"). The main branch needs no handshake — same reasoning the
// regular current-snapshot import already relies on — so replaying its
// history is just N extra plain submissions before the regular one.
//
// Must run BEFORE the regular current-snapshot import for this branch: the
// Compute Engine requires each new analysis to be dated after the branch's
// most recent one. The regular import now backdates its own submission to
// the source's true last-analysis date (#557 review feedback) rather than
// "now" — still guaranteed later than every historical point here, because
// selectBoundedHistoryPoints drops that same most-recent analysis before
// building this candidate list. Best-effort: a failure here is logged and
// does NOT fail or block the regular import that follows it, so
// --migrate_history can never turn a transfer that used to succeed into one
// that fails.
func migrateBranchHistory(ctx context.Context, e *Executor, bctx branchImportContext, branch branchInfo, targetBranch string) {
	if !e.MigrateHistory || !branch.IsMain {
		return
	}
	snapshots := loadExtractedAnalysisHistory(e, bctx.ServerURL, bctx.ServerKey, branch.Name)
	if len(snapshots) == 0 {
		return
	}

	// The placeholder file the snapshot report carries has to name a language
	// whose quality profile actually exists in THIS target organization: the
	// CE validates every qprofile key in the metadata against the org and
	// rejects the whole report otherwise ("Quality profiles with following
	// keys don't exist in organization [...]"). Resolve it per run instead of
	// assuming any particular language or profile key is present.
	placeholder, ok := resolveHistoryPlaceholderProfile(ctx, e, bctx.OrgKey)
	if !ok {
		e.Logger.Warn("project history migration skipped: no usable quality profile in the target organization",
			"project", bctx.CloudKey, "branch", targetBranch, "org", bctx.OrgKey)
		return
	}

	e.Logger.Info("migrating project history (PoC, #554)",
		"project", bctx.CloudKey, "branch", targetBranch, "points", len(snapshots),
		"placeholder_language", placeholder.Language)

	for i, snap := range snapshots {
		if err := submitHistoricalSnapshot(ctx, e, bctx, targetBranch, snap, placeholder); err != nil {
			// Stop rather than skip-and-continue: submitting a later
			// historical date after a skipped earlier one would still be
			// chronologically valid, but a submission failure here is far
			// more likely a CE rejection than a transient blip (the earlier
			// points, if any, just succeeded against the same endpoint), so
			// every remaining point on this branch would likely fail the
			// same way. Give up early rather than hammering the CE.
			e.Logger.Warn("project history migration stopped early for this branch",
				"project", bctx.CloudKey, "branch", targetBranch,
				"point", i+1, "of", len(snapshots), "date", snap.Date.Format(time.RFC3339), "err", err)
			return
		}
	}
}

// historyPlaceholderLangs are the languages the placeholder file may be
// stamped with, most-preferred first, each paired with the file extension it
// must carry.
//
// The extension is not decoration and is not derivable from the language key:
// SonarQube Cloud assigns a file's language from its extension, and a
// mismatch between that and the declared quality profile is exactly the #474
// "file with language X but no matching quality profile" rejection. Several
// language keys are not their own extension (apex→.cls, cobol→.cbl,
// web→.html), so the pairing is explicit and a language may only be used as
// a placeholder if it appears here.
//
// All six are ordinary, universally shipped languages with a stock "Sonar
// way" profile in every organization, which keeps the placeholder as
// unremarkable as possible. The file is never meant to be read.
var historyPlaceholderLangs = []struct{ Lang, Ext string }{
	{"js", "js"}, {"py", "py"}, {"java", "java"},
	{"ts", "ts"}, {"go", "go"}, {"xml", "xml"},
}

// historyPlaceholder is the resolved, target-org-specific identity of the
// throwaway file component a historical snapshot report is built around.
type historyPlaceholder struct {
	Language string
	Ext      string
	QProfile scanreport.QProfileInfo
}

// resolveHistoryPlaceholderProfile picks a language for the placeholder file
// whose quality profile really exists in orgKey, and returns that profile.
//
// The key must be the TARGET organization's profile key, not the source
// server's: profile keys are instance-scoped, and submitting a source key
// makes the CE reject the report outright. buildSCProfileMap is the same
// lookup the regular current-snapshot import already uses for this.
func resolveHistoryPlaceholderProfile(ctx context.Context, e *Executor, orgKey string) (historyPlaceholder, bool) {
	return pickHistoryPlaceholder(buildSCProfileMap(ctx, e, orgKey))
}

// pickHistoryPlaceholder is the pure selection half of
// resolveHistoryPlaceholderProfile, split out so the choice can be tested
// without a live organization.
func pickHistoryPlaceholder(byLang map[string]scanreport.QProfileInfo) (historyPlaceholder, bool) {
	for _, c := range historyPlaceholderLangs {
		if p, ok := byLang[c.Lang]; ok {
			return historyPlaceholder{Language: c.Lang, Ext: c.Ext, QProfile: p}, true
		}
	}
	// The organization has profiles, but none for a language we can safely
	// name a placeholder file after. Refusing here is deliberate: guessing an
	// extension from an unknown language key is what produces the #474
	// whole-report rejection, and skipping history is far better than
	// submitting reports the Compute Engine will refuse.
	return historyPlaceholder{}, false
}

// retargetMeasures returns a copy of measures with every entry's Component
// rewritten to newComponent. loadExtractedAnalysisHistory attributes
// measures to the project's own cloud key (there is no file component at
// extraction time to attribute them to instead); submitHistoricalSnapshot
// retargets them onto its placeholder file component just before building
// the report.
func retargetMeasures(measures []scanreport.MeasureInput, newComponent string) []scanreport.MeasureInput {
	out := make([]scanreport.MeasureInput, len(measures))
	for i, m := range measures {
		m.Component = newComponent
		out[i] = m
	}
	return out
}

// snapshotLineCount picks the placeholder file's declared Lines: the largest
// of "lines" (total lines), "ncloc", "lines_to_cover" and "duplicated_lines"
// seen in this point's own measures, or 1 if none of them carry a positive
// value. Taking the max across all four (rather than just preferring "lines"
// when present) keeps the invariant buildSyntheticLineCoverage and
// buildSyntheticDuplication depend on — Lines must be >= the highest line
// number any LineCoverage or Duplication record names — true even on a
// point whose "lines" measure is missing or, in principle, smaller than
// lines_to_cover/duplicated_lines.
func snapshotLineCount(measures []scanreport.MeasureInput) int32 {
	var best int32
	for _, m := range measures {
		switch m.MetricKey {
		case "lines", "ncloc", "lines_to_cover", "duplicated_lines":
			if v, err := strconv.ParseInt(m.Value, 10, 32); err == nil && int32(v) > best {
				best = int32(v)
			}
		}
	}
	if best > 0 {
		return best
	}
	return 1
}

// buildSyntheticLineCoverage synthesizes per-line LineCoverage records that
// reproduce this point's lines_to_cover/uncovered_lines/conditions_to_cover/
// uncovered_conditions as an aggregate, live-verified necessary during #557:
// pushing those four as plain project-level Measures (fix 1's first attempt)
// is silently inert — the Compute Engine computes every coverage-domain
// figure (including `coverage` itself) exclusively from each file's real
// per-line coverage data, never from a pushed aggregate, no matter how that
// aggregate is framed. `statements`, pushed through the identical code path
// in the same report, persists; `lines_to_cover` does not — that asymmetry
// is what exposed this.
//
// The placeholder file is never meant to be viewed, so which specific lines
// are marked covered/uncovered is arbitrary; only the totals matter. Lines
// 1..linesToCover each get a Hits record (the first uncoveredLines of them
// false, the rest true); if there is any condition coverage to report at
// all, it is attributed entirely to line 1 (creating one if linesToCover is
// itself 0) since the CE only cares about the summed totals, not which line
// carried which condition.
func buildSyntheticLineCoverage(measures []scanreport.MeasureInput) []*pb.LineCoverage {
	linesToCover := measureIntValue(measures, "lines_to_cover")
	uncoveredLines := clamp(measureIntValue(measures, "uncovered_lines"), linesToCover)
	conditionsToCover := measureIntValue(measures, "conditions_to_cover")
	uncoveredConditions := clamp(measureIntValue(measures, "uncovered_conditions"), conditionsToCover)
	if linesToCover == 0 && conditionsToCover == 0 {
		return nil
	}

	n := linesToCover
	if n == 0 {
		n = 1 // no coverable lines, but there's condition data that still needs a line to live on
	}
	out := buildCoverageLines(n, linesToCover, uncoveredLines)
	if conditionsToCover > 0 {
		out[0].Conditions = conditionsToCover
		out[0].HasCoveredConditions = &pb.LineCoverage_CoveredConditions{CoveredConditions: conditionsToCover - uncoveredConditions}
	}
	return out
}

// measureIntValue returns the positive integer value of the first measure
// under key, or 0 if it's absent, unparseable, or not positive.
func measureIntValue(measures []scanreport.MeasureInput, key string) int32 {
	for _, m := range measures {
		if m.MetricKey != key {
			continue
		}
		if v, err := strconv.ParseInt(m.Value, 10, 32); err == nil && v > 0 {
			return int32(v)
		}
	}
	return 0
}

// clamp caps v at max, guarding against a source recording more "uncovered"
// than "to cover" for a metric pair (shouldn't happen, but a report that
// claims more uncovered lines/conditions than exist to cover is invalid).
func clamp(v, max int32) int32 {
	if v > max {
		return max
	}
	return v
}

// buildCoverageLines builds n sequential LineCoverage records, lines 1..n:
// the first linesToCover of them carry a Hits value (the first
// uncoveredLines of those false, the rest true), and any remainder (present
// only when linesToCover is 0 but the caller still needs a line to attach
// condition data to) carries no Hits value at all.
func buildCoverageLines(n, linesToCover, uncoveredLines int32) []*pb.LineCoverage {
	out := make([]*pb.LineCoverage, n)
	for i := int32(0); i < n; i++ {
		lc := &pb.LineCoverage{Line: i + 1}
		if i < linesToCover {
			lc.HasHits = &pb.LineCoverage_Hits{Hits: i >= uncoveredLines}
		}
		out[i] = lc
	}
	return out
}

// measureFloatValue returns the float value of the first measure under key,
// or 0 if key is empty, absent, or unparseable — used for ratings (e.g.
// "3.0"), which are always a small whole number but stored as a decimal
// string, unlike the plain positive-integer contract measureIntValue has.
func measureFloatValue(measures []scanreport.MeasureInput, key string) float64 {
	if key == "" {
		return 0
	}
	for _, m := range measures {
		if m.MetricKey != key {
			continue
		}
		if v, err := strconv.ParseFloat(m.Value, 64); err == nil {
			return v
		}
	}
	return 0
}

// ratingToSeverity maps a SonarQube A-E rating (1.0-5.0) to the classic
// issue severity whose presence alone would produce that rating: ratings
// threshold on the single WORST severity present, not an average or a
// count, so reproducing one only needs one issue at the right severity.
func ratingToSeverity(rating float64) string {
	switch {
	case rating < 2:
		return "INFO"
	case rating < 3:
		return "MINOR"
	case rating < 4:
		return "MAJOR"
	case rating < 5:
		return "CRITICAL"
	default:
		return "BLOCKER"
	}
}

// distributeInt splits total as evenly as possible across n buckets (each
// gets total/n, with the remainder added to the last bucket), so the
// buckets always sum back to exactly total. Returns nil for n<=0.
func distributeInt(total int64, n int) []int64 {
	if n <= 0 {
		return nil
	}
	out := make([]int64, n)
	base := total / int64(n)
	for i := range out {
		out[i] = base
	}
	out[n-1] += total - base*int64(n)
	return out
}

// syntheticIssueEngineID identifies every placeholder Issue/AdHocRule this
// tool fabricates for historical points, so they're recognizable as
// synthetic rather than real findings.
const syntheticIssueEngineID = "smt-history"

// syntheticIssueType names one of the three issue-derived measure/rating
// pairs buildSyntheticIssues reconstructs. RatingMetric is empty for code
// smells, which have no count-driven rating of their own.
type syntheticIssueType struct {
	CountMetric  string
	RatingMetric string
	Type         string
	RuleID       string
}

var syntheticIssueTypes = []syntheticIssueType{
	{CountMetric: "bugs", RatingMetric: "reliability_rating", Type: "BUG", RuleID: "bug"},
	{CountMetric: "vulnerabilities", RatingMetric: "security_rating", Type: "VULNERABILITY", RuleID: "vulnerability"},
	{CountMetric: "code_smells", Type: "CODE_SMELL", RuleID: "code_smell"},
}

// buildSeverities returns count severities, all "MINOR" except the last,
// which carries the severity that alone would reproduce rating when
// hasRating is true — see ratingToSeverity's doc for why only one issue
// needs to vary.
func buildSeverities(count int32, rating float64, hasRating bool) []string {
	out := make([]string, count)
	for i := range out {
		out[i] = "MINOR"
	}
	if hasRating {
		out[count-1] = ratingToSeverity(rating)
	}
	return out
}

// buildEfforts distributes sqaleIndex minutes across count issues when typ
// is CODE_SMELL — confirmed live that maintainability debt (sqale_index) is
// driven purely by code-smell effort, not bug/vulnerability effort — or
// returns nil otherwise, leaving those issues' Effort field unset.
func buildEfforts(typ string, count, sqaleIndex int32) []int64 {
	if typ != "CODE_SMELL" || sqaleIndex == 0 {
		return nil
	}
	return distributeInt(int64(sqaleIndex), int(count))
}

// buildIssuesForType builds one ExternalIssueInput per entry in severities,
// all on componentKey at a fixed placeholder location — arbitrary, since the
// file they attach to is never viewed. efforts, when non-nil, must be the
// same length as severities.
func buildIssuesForType(t syntheticIssueType, componentKey string, severities []string, efforts []int64) []scanreport.ExternalIssueInput {
	out := make([]scanreport.ExternalIssueInput, len(severities))
	for i, sev := range severities {
		iss := scanreport.ExternalIssueInput{
			EngineID:  syntheticIssueEngineID,
			RuleID:    t.RuleID,
			Message:   "synthetic historical " + t.RuleID,
			Severity:  sev,
			Type:      t.Type,
			StartLine: 1,
			EndLine:   1,
			Component: componentKey,
		}
		if efforts != nil {
			iss.Effort = fmt.Sprintf("%dmin", efforts[i])
		}
		out[i] = iss
	}
	return out
}

// buildSyntheticIssues synthesizes placeholder ExternalIssues (and the
// AdHocRules they reference) that reproduce this point's bugs/
// vulnerabilities/code_smells counts, reliability_rating/security_rating and
// sqale_index (technical debt) as aggregates — live-verified necessary the
// same way coverage/duplication were: the CE derives every one of these
// exclusively by counting real Issue-shaped entries in the report, never
// from a pushed Measure.
//
// ExternalIssue (not native Issue) is deliberate: it needs no active rule in
// the target's resolved quality profile, sidestepping the #474 "rule must
// be active" constraint entirely — at the cost of these being visibly
// synthetic (ad-hoc rule, no real code location) rather than real findings,
// the same honesty trade-off as the placeholder file itself. There is no
// SonarQube API that returns "what issues existed as of a past analysis" —
// only the aggregate counts/ratings/debt this function reconstructs are
// available historically, never the original issues themselves.
//
// Security hotspots are deliberately NOT reconstructed here: hotspot
// conversion (convertHotspotsForReport) uses native Issues specifically
// because hotspots need a real active rule in the target's profile — a
// different, larger mechanism this function has no access to.
func buildSyntheticIssues(measures []scanreport.MeasureInput, componentKey string) ([]scanreport.ExternalIssueInput, []scanreport.AdHocRuleInput) {
	sqaleIndex := measureIntValue(measures, "sqale_index")

	var issues []scanreport.ExternalIssueInput
	var rules []scanreport.AdHocRuleInput
	for _, t := range syntheticIssueTypes {
		count := measureIntValue(measures, t.CountMetric)
		if count == 0 {
			continue
		}
		rules = append(rules, scanreport.AdHocRuleInput{
			EngineID:    syntheticIssueEngineID,
			RuleID:      t.RuleID,
			Name:        "Synthetic " + t.RuleID,
			Description: "Placeholder rule for #554 project history migration; not a real finding.",
			Severity:    "MAJOR",
			Type:        t.Type,
		})
		severities := buildSeverities(count, measureFloatValue(measures, t.RatingMetric), t.RatingMetric != "")
		efforts := buildEfforts(t.Type, count, sqaleIndex)
		issues = append(issues, buildIssuesForType(t, componentKey, severities, efforts)...)
	}
	return issues, rules
}

// buildSyntheticDuplication synthesizes same-file duplication blocks that
// reproduce this point's duplicated_lines/duplicated_blocks as an aggregate
// — live-verified necessary the same way coverage was: pushing
// duplicated_lines/duplicated_blocks/duplicated_files as plain Measures is
// silently inert, and duplicated_lines_density (itself a formula over
// duplicated_lines/lines) only computes once real per-block Duplication
// data is present. Confirmed live: a same-file self-duplication (one origin
// range + one duplicate range, with Duplicate.OtherFileRef left unset —
// SonarCloud rejects a Duplicate whose OtherFileRef explicitly names its own
// file) of 50+50 lines produced duplicated_lines=100, duplicated_blocks=2,
// duplicated_files=1, exactly as expected.
//
// duplicatedBlocks total occurrences (origin + every duplicate) share
// duplicatedLines total lines as evenly as distributeInt allows, laid out as
// sequential, non-overlapping ranges starting at line 1 — arbitrary, since
// the placeholder file is never viewed. Fewer than 2 lines or 2 occurrences
// can't form a valid duplication (a lone block isn't a duplicate of
// anything), so those return nil; more occurrences than lines is clamped
// down to one line per occurrence rather than emitting a malformed range.
func buildSyntheticDuplication(measures []scanreport.MeasureInput) []*pb.Duplication {
	duplicatedLines := measureIntValue(measures, "duplicated_lines")
	duplicatedBlocks := measureIntValue(measures, "duplicated_blocks")
	if duplicatedLines < 2 || duplicatedBlocks < 2 {
		return nil
	}
	if duplicatedBlocks > duplicatedLines {
		duplicatedBlocks = duplicatedLines
	}

	sizes := distributeInt(int64(duplicatedLines), int(duplicatedBlocks))
	ranges := make([]*pb.TextRange, len(sizes))
	line := int32(1)
	for i, size := range sizes {
		ranges[i] = &pb.TextRange{StartLine: line, EndLine: line + int32(size) - 1}
		line += int32(size)
	}

	dup := &pb.Duplication{OriginPosition: ranges[0]}
	for _, r := range ranges[1:] {
		dup.Duplicate = append(dup.Duplicate, &pb.Duplicate{Range: r})
	}
	return []*pb.Duplication{dup}
}

// submitHistoricalSnapshot builds and submits one minimal, backdated scanner
// report for a single historical point: the project root component, one
// placeholder file carrying the point's measures plus synthesized coverage/
// duplication/issue data (see buildSyntheticLineCoverage, buildSyntheticDuplication,
// buildSyntheticIssues), and no real quality profile dependency (the
// placeholder's own profile is resolved separately in resolveHistoryPlaceholderProfile,
// and the synthetic issues use ad-hoc rules specifically to avoid needing
// another one).
func submitHistoricalSnapshot(ctx context.Context, e *Executor, bctx branchImportContext, targetBranch string, snap historySnapshot, placeholder historyPlaceholder) error {
	// A lone PROJECT component with a raw measure attached directly to it is
	// not a shape the real scanner ever produces — measures normally live on
	// FILE components and the CE aggregates the project total from them — and
	// submitting one that way was rejected by the CE ("issue whilst
	// processing the report", live-verified). One placeholder FILE component
	// gives the measures somewhere valid to live; per the issue's own PoC
	// design ("fake ones can be created just to let SQC/SonarCloud accept
	// the report"), its content is never meant to be seen.
	placeholderName := "__history_snapshot__." + placeholder.Ext
	placeholderKey := bctx.CloudKey + ":" + placeholderName
	// The placeholder's declared Lines must cover the largest coverage/size
	// input this point pushes (lines_to_cover, statements, etc.): a file
	// cannot have more lines-to-cover than it has lines. A hardcoded Lines: 1
	// was live-verified to make the CE silently drop the coverage measure
	// entirely — but raising Lines alone was NOT sufficient to make coverage
	// appear: it took real per-line data (buildSyntheticLineCoverage below)
	// for the CE to compute it at all. Kept as a guard regardless, since an
	// undersized Lines is still a real (if secondary) way for a file's
	// coverage data to be rejected as inconsistent.
	root, fileComps, cr := scanreport.BuildComponents(bctx.CloudKey, []scanreport.ComponentInput{
		{Key: placeholderKey, Name: placeholderName, Path: placeholderName, Language: placeholder.Language, Lines: snapshotLineCount(snap.Measures)},
	})
	if len(fileComps) == 0 {
		return fmt.Errorf("building historical report: placeholder component was not created")
	}
	fileRef := fileComps[0].Ref
	extIssues, adHocRules := buildSyntheticIssues(snap.Measures, placeholderKey)

	reportData := &scanreport.ReportData{
		Metadata: scanreport.BuildMetadata(scanreport.MetadataInput{
			AnalysisDate: snap.Date,
			OrgKey:       bctx.OrgKey,
			ProjectKey:   bctx.CloudKey,
			BranchName:   targetBranch,
			BranchType:   pb.Metadata_BRANCH,
			QProfiles:    []scanreport.QProfileInfo{placeholder.QProfile},
			// Keyed by LANGUAGE despite the name — countFilesByExt, which the
			// regular import feeds this field from, counts c.Language.
			FileCountByExt: map[string]int32{placeholder.Language: 1},
			ProjectVersion: snap.ProjectVersion,
		}, root.Ref),
		RootComponent:  root,
		FileComponents: fileComps,
		Measures:       scanreport.BuildMeasures(retargetMeasures(snap.Measures, placeholderKey), cr),
		Coverage:       map[int32][]*pb.LineCoverage{fileRef: buildSyntheticLineCoverage(snap.Measures)},
		Duplications:   map[int32][]*pb.Duplication{fileRef: buildSyntheticDuplication(snap.Measures)},
		// ExternalIssue, not native Issue: needs no active rule in the target's
		// resolved quality profile (see buildSyntheticIssues). No ActiveRules
		// entry is set for the same reason nothing was needed before this: a
		// native rule activation has nothing to do with ad-hoc ones.
		ExternalIssues: scanreport.BuildExternalIssues(extIssues, cr),
		AdHocRules:     scanreport.BuildAdHocRules(adHocRules),
		Sources:        map[int32]string{fileRef: ""},
		Changesets: map[int32]*pb.Changesets{
			fileRef: scanreport.BuildDefaultChangesets(fileRef, 1, snap.Date),
		},
	}

	zipBytes, err := scanreport.PackageReport(reportData)
	if err != nil {
		return fmt.Errorf("packaging historical report: %w", err)
	}

	cfg := scanreport.SubmitConfig{
		CloudURL:       e.CloudURL,
		ProjectKey:     bctx.CloudKey,
		OrgKey:         bctx.OrgKey,
		BranchName:     targetBranch,
		ProjectVersion: snap.ProjectVersion,
		IsMain:         true,
	}
	result, err := scanreport.SubmitReport(ctx, e.Raw.HTTPClient(), cfg, zipBytes)
	if err != nil {
		return fmt.Errorf("submitting historical report: %w", err)
	}
	if err := scanreport.PollCETask(ctx, e.Raw.HTTPClient(), e.CloudURL, result.TaskID, e.Logger); err != nil {
		return fmt.Errorf("CE task failed: %w", err)
	}

	e.Logger.Info("historical analysis migrated",
		"project", bctx.CloudKey, "branch", targetBranch,
		"date", snap.Date.Format(time.RFC3339), "taskId", result.TaskID)
	return nil
}
