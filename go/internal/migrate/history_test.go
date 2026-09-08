// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

func TestResolveMigrateHistory(t *testing.T) {
	set := func(v bool) *FlexibleBool { return &FlexibleBool{Set: true, Value: v} }

	cases := []struct {
		name       string
		target     *FlexibleBool
		top        *FlexibleBool
		wantResult bool
	}{
		{"neither set defaults false", nil, nil, false},
		{"top-level true, no target", nil, set(true), true},
		{"target true wins over unset top-level", set(true), nil, true},
		{"target false wins over top-level true", set(false), set(true), false},
		{"target unset falls back to top-level true", &FlexibleBool{Set: false}, set(true), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveMigrateHistory(c.target, c.top)
			if got != c.wantResult {
				t.Errorf("resolveMigrateHistory(%v, %v) = %v, want %v", c.target, c.top, got, c.wantResult)
			}
		})
	}
}

func TestExtractHistoryMeasures(t *testing.T) {
	data := json.RawMessage(`{
		"projectKey": "proj",
		"branch": "main",
		"date": "2024-05-14T10:45:33Z",
		"measures": [
			{"metric": "ncloc", "value": "1000"},
			{"metric": "bugs", "value": "3"},
			{"metric": "empty_value", "value": ""},
			{"value": "no-metric-key"}
		]
	}`)

	got := extractHistoryMeasures(data, "my_cloud_key")
	if len(got) != 2 {
		t.Fatalf("expected 2 valid measures, got %d: %+v", len(got), got)
	}
	byMetric := map[string]string{}
	for _, m := range got {
		if m.Component != "my_cloud_key" {
			t.Errorf("expected component %q, got %q", "my_cloud_key", m.Component)
		}
		byMetric[m.MetricKey] = m.Value
	}
	if byMetric["ncloc"] != "1000" || byMetric["bugs"] != "3" {
		t.Errorf("unexpected measures map: %v", byMetric)
	}
}

func TestExtractHistoryMeasuresNoMeasuresField(t *testing.T) {
	data := json.RawMessage(`{"projectKey": "proj"}`)
	if got := extractHistoryMeasures(data, "proj"); got != nil {
		t.Errorf("expected nil for a record with no measures field, got %v", got)
	}
}

// TestSnapshotLineCount pins the placeholder file's declared size: it must
// cover the largest size input this point carries (lines_to_cover,
// duplicated_lines, etc.), not a fixed 1 — a file that has fewer lines than
// its own lines_to_cover made the CE silently drop the coverage measure with
// no error, found live-verifying #557.
func TestSnapshotLineCount(t *testing.T) {
	m := func(metric, value string) scanreport.MeasureInput {
		return scanreport.MeasureInput{MetricKey: metric, Value: value}
	}

	cases := []struct {
		name     string
		measures []scanreport.MeasureInput
		want     int32
	}{
		{"lines present, preferred over ncloc", []scanreport.MeasureInput{m("lines", "1000"), m("ncloc", "500")}, 1000},
		{"no lines, falls back to ncloc", []scanreport.MeasureInput{m("ncloc", "500")}, 500},
		{"neither present, falls back to 1", []scanreport.MeasureInput{m("bugs", "3")}, 1},
		{"no measures at all", nil, 1},
		{"unparseable lines value ignored, falls back to ncloc", []scanreport.MeasureInput{m("lines", "not-a-number"), m("ncloc", "42")}, 42},
		{"zero lines ignored, falls back to ncloc", []scanreport.MeasureInput{m("lines", "0"), m("ncloc", "42")}, 42},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := snapshotLineCount(c.measures); got != c.want {
				t.Errorf("snapshotLineCount(%v) = %d, want %d", c.measures, got, c.want)
			}
		})
	}
}

// TestBuildSyntheticLineCoverage pins the per-line coverage records that let
// the Compute Engine actually compute lines_to_cover/uncovered_lines/
// conditions_to_cover/uncovered_conditions/coverage for a historical point —
// pushing those four as plain Measures (fix 1's first attempt) turned out to
// be silently inert, live-verified during #557: the CE derives every
// coverage-domain aggregate exclusively from real per-line data like this.
// Each case is its own top-level function (rather than a nested t.Run
// closure) so its branching doesn't compound into one large function.
func TestBuildSyntheticLineCoverage(t *testing.T) {
	t.Run("no coverage measures at all yields nothing", testLineCoverageNoMeasures)
	t.Run("lines and conditions both present", testLineCoverageLinesAndConditions)
	t.Run("conditions with no coverable lines still gets one carrier record", testLineCoverageConditionsOnly)
	t.Run("uncovered counts clamp to their cover totals", testLineCoverageClamping)
}

func measureIn(metric, value string) scanreport.MeasureInput {
	return scanreport.MeasureInput{MetricKey: metric, Value: value}
}

func testLineCoverageNoMeasures(t *testing.T) {
	got := buildSyntheticLineCoverage([]scanreport.MeasureInput{measureIn("ncloc", "500")})
	if got != nil {
		t.Fatalf("got %d records, want nil", len(got))
	}
}

func testLineCoverageLinesAndConditions(t *testing.T) {
	got := buildSyntheticLineCoverage([]scanreport.MeasureInput{
		measureIn("lines_to_cover", "10"), measureIn("uncovered_lines", "3"),
		measureIn("conditions_to_cover", "4"), measureIn("uncovered_conditions", "1"),
	})
	if len(got) != 10 {
		t.Fatalf("got %d records, want 10", len(got))
	}
	covered, uncovered := countHitRecords(t, got)
	if covered != 7 || uncovered != 3 {
		t.Errorf("covered=%d uncovered=%d, want 7/3", covered, uncovered)
	}
	if got[0].GetConditions() != 4 {
		t.Errorf("record 0 conditions = %d, want 4", got[0].GetConditions())
	}
	if got[0].HasCoveredConditions == nil || got[0].GetCoveredConditions() != 3 {
		t.Errorf("record 0 covered_conditions = %d, want 3", got[0].GetCoveredConditions())
	}
	assertNoConditionData(t, got[1:])
}

// countHitRecords checks every record carries the expected line number and a
// Hits value, and tallies how many are covered vs not.
func countHitRecords(t *testing.T, got []*pb.LineCoverage) (covered, uncovered int32) {
	t.Helper()
	for i, lc := range got {
		if lc.GetLine() != int32(i+1) {
			t.Errorf("record %d: line = %d, want %d", i, lc.GetLine(), i+1)
		}
		if lc.HasHits == nil {
			t.Fatalf("record %d: expected a Hits value, got none", i)
		}
		if lc.GetHits() {
			covered++
		} else {
			uncovered++
		}
	}
	return covered, uncovered
}

// assertNoConditionData checks none of the given records carry condition data.
func assertNoConditionData(t *testing.T, records []*pb.LineCoverage) {
	t.Helper()
	for i, lc := range records {
		if lc.GetConditions() != 0 || lc.HasCoveredConditions != nil {
			t.Errorf("record %d: expected no condition data, got conditions=%d covered=%v", i, lc.GetConditions(), lc.HasCoveredConditions)
		}
	}
}

func testLineCoverageConditionsOnly(t *testing.T) {
	got := buildSyntheticLineCoverage([]scanreport.MeasureInput{
		measureIn("conditions_to_cover", "5"), measureIn("uncovered_conditions", "2"),
	})
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].HasHits != nil {
		t.Errorf("expected no Hits value on a line with no coverable-lines input, got %v", got[0].GetHits())
	}
	if got[0].GetConditions() != 5 || got[0].GetCoveredConditions() != 3 {
		t.Errorf("conditions=%d covered=%d, want 5/3", got[0].GetConditions(), got[0].GetCoveredConditions())
	}
}

func testLineCoverageClamping(t *testing.T) {
	got := buildSyntheticLineCoverage([]scanreport.MeasureInput{
		measureIn("lines_to_cover", "2"), measureIn("uncovered_lines", "9999"),
		measureIn("conditions_to_cover", "2"), measureIn("uncovered_conditions", "9999"),
	})
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	for i, lc := range got {
		if lc.GetHits() {
			t.Errorf("record %d: expected uncovered (clamped), got hits=true", i)
		}
	}
	if got[0].GetCoveredConditions() != 0 {
		t.Errorf("covered_conditions = %d, want 0 (clamped)", got[0].GetCoveredConditions())
	}
}

// TestHistoricalReportShape builds the same protobuf pieces
// submitHistoricalSnapshot assembles (without actually submitting over the
// network — that's covered by the live end-to-end verification) and asserts
// its shape: one placeholder file component carrying the measures, and a
// metadata analysis date matching the snapshot's backdated date rather than
// time.Now().
func TestHistoricalReportShape(t *testing.T) {
	snap := historySnapshot{
		Date:           time.Date(2022, 3, 1, 0, 0, 0, 0, time.UTC),
		ProjectVersion: "2.5",
		Measures: []scanreport.MeasureInput{
			{Component: "cloud_key", MetricKey: "ncloc", Value: "500"},
		},
	}
	placeholder := historyPlaceholder{
		Language: "js",
		Ext:      "js",
		QProfile: scanreport.QProfileInfo{Key: "target-org-js-key", Name: "Sonar way", Language: "js"},
	}

	placeholderName := "__history_snapshot__." + placeholder.Ext
	placeholderKey := "cloud_key:" + placeholderName
	root, fileComps, cr := scanreport.BuildComponents("cloud_key", []scanreport.ComponentInput{
		{Key: placeholderKey, Name: placeholderName, Path: placeholderName, Language: placeholder.Language, Lines: 1},
	})
	if len(fileComps) != 1 {
		t.Fatalf("expected exactly 1 placeholder file component, got %d", len(fileComps))
	}
	if root.GetType() != pb.Component_PROJECT {
		t.Errorf("expected root component type PROJECT, got %v", root.GetType())
	}

	md := scanreport.BuildMetadata(scanreport.MetadataInput{
		AnalysisDate:   snap.Date,
		ProjectKey:     "cloud_key",
		BranchName:     "main",
		BranchType:     pb.Metadata_BRANCH,
		QProfiles:      []scanreport.QProfileInfo{placeholder.QProfile},
		FileCountByExt: map[string]int32{placeholder.Language: 1},
		ProjectVersion: snap.ProjectVersion,
	}, root.Ref)
	if md.AnalysisDate != snap.Date.UnixMilli() {
		t.Errorf("expected metadata analysis date %d (the backdated snapshot date), got %d",
			snap.Date.UnixMilli(), md.AnalysisDate)
	}
	if md.AnalysisUuid != "" {
		t.Errorf("expected no analysis UUID for a main-branch history point, got %q", md.AnalysisUuid)
	}
	// Regression guard for the bug this PoC shipped with first: the report
	// carried a quality profile key hardcoded from the developer's own
	// organization, so the Compute Engine rejected every historical report
	// with "Quality profiles with following keys don't exist in organization".
	// The key in the metadata must be the one resolved from the TARGET org.
	gotProfile, ok := md.QprofilesPerLanguage[placeholder.Language]
	if !ok {
		t.Fatalf("expected a qprofile entry for language %q, got %v", placeholder.Language, md.QprofilesPerLanguage)
	}
	if gotProfile.GetKey() != placeholder.QProfile.Key {
		t.Errorf("expected the target organization's profile key %q in metadata, got %q",
			placeholder.QProfile.Key, gotProfile.GetKey())
	}

	measures := scanreport.BuildMeasures(retargetMeasures(snap.Measures, placeholderKey), cr)
	if len(measures[fileComps[0].Ref]) != 1 {
		t.Fatalf("expected 1 measure on the placeholder file component ref, got %d", len(measures[fileComps[0].Ref]))
	}
}

// TestResolveHistoryPlaceholderProfilePrefersCommonLanguage pins the
// placeholder-language choice: it must come from the target organization's
// own profile map, never from a constant.
func TestResolveHistoryPlaceholderProfilePrefersCommonLanguage(t *testing.T) {
	tests := []struct {
		name     string
		byLang   map[string]scanreport.QProfileInfo
		wantLang string
		wantOK   bool
	}{
		{
			name:   "no profiles in org means history cannot be migrated",
			byLang: map[string]scanreport.QProfileInfo{},
			wantOK: false,
		},
		{
			name: "prefers js when available",
			byLang: map[string]scanreport.QProfileInfo{
				"js":   {Key: "k-js", Language: "js"},
				"abap": {Key: "k-abap", Language: "abap"},
			},
			wantLang: "js", wantOK: true,
		},
		{
			name: "falls back to the next preferred language",
			byLang: map[string]scanreport.QProfileInfo{
				"abap": {Key: "k-abap", Language: "abap"},
				"py":   {Key: "k-py", Language: "py"},
			},
			wantLang: "py", wantOK: true,
		},
		{
			// Refusing is deliberate. A language key is not reliably its own
			// file extension (apex->.cls, cobol->.cbl, web->.html), and Cloud
			// derives a file's language from its extension — guessing one
			// recreates the #474 whole-report rejection. Skipping history
			// beats submitting reports the CE will refuse.
			name: "refuses when the org has profiles but none we can name a file for",
			byLang: map[string]scanreport.QProfileInfo{
				"cobol": {Key: "k-cobol", Language: "cobol"},
				"abap":  {Key: "k-abap", Language: "abap"},
			},
			wantOK: false,
		},
		{
			// Every supported placeholder language must carry an extension
			// the target maps back to that same language.
			name: "picks a supported language even when unsupported ones sort first",
			byLang: map[string]scanreport.QProfileInfo{
				"abap": {Key: "k-abap", Language: "abap"},
				"xml":  {Key: "k-xml", Language: "xml"},
			},
			wantLang: "xml", wantOK: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pickHistoryPlaceholder(tc.byLang)
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got.Language != tc.wantLang {
				t.Errorf("language: got %q, want %q", got.Language, tc.wantLang)
			}
			if got.QProfile.Key != tc.byLang[tc.wantLang].Key {
				t.Errorf("profile key: got %q, want %q", got.QProfile.Key, tc.byLang[tc.wantLang].Key)
			}
		})
	}
}

// TestRatingToSeverity pins the A-E rating -> classic severity mapping:
// ratingToSeverity thresholds on the single WORST severity that alone would
// reproduce the rating, so every boundary (just under and at each integer)
// must land on the expected side.
func TestRatingToSeverity(t *testing.T) {
	cases := []struct {
		name   string
		rating float64
		want   string
	}{
		{"1.0 is INFO", 1.0, "INFO"},
		{"1.99 is INFO", 1.99, "INFO"},
		{"2.0 is MINOR", 2.0, "MINOR"},
		{"2.99 is MINOR", 2.99, "MINOR"},
		{"3.0 is MAJOR", 3.0, "MAJOR"},
		{"3.99 is MAJOR", 3.99, "MAJOR"},
		{"4.0 is CRITICAL", 4.0, "CRITICAL"},
		{"4.99 is CRITICAL", 4.99, "CRITICAL"},
		{"5.0 is BLOCKER", 5.0, "BLOCKER"},
		{"above 5.0 is BLOCKER (default branch)", 6.0, "BLOCKER"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ratingToSeverity(c.rating); got != c.want {
				t.Errorf("ratingToSeverity(%v) = %q, want %q", c.rating, got, c.want)
			}
		})
	}
}

// TestDistributeInt pins the even-split-with-remainder-on-the-last-bucket
// contract every synthetic-issue/duplication helper below depends on: the
// buckets must always sum back to exactly total. Each case is its own
// top-level function, called via t.Run, matching TestBuildSyntheticLineCoverage's
// split style so no single test function's branching compounds.
func TestDistributeInt(t *testing.T) {
	t.Run("n<=0 returns nil", testDistributeIntNonPositiveN)
	t.Run("evenly divisible splits equally", testDistributeIntEven)
	t.Run("remainder goes entirely to the last bucket", testDistributeIntRemainder)
	t.Run("n=1 puts everything in the single bucket", testDistributeIntSingleBucket)
	t.Run("total=0 returns all-zero buckets", testDistributeIntZeroTotal)
}

func testDistributeIntNonPositiveN(t *testing.T) {
	if got := distributeInt(10, 0); got != nil {
		t.Errorf("distributeInt(10, 0) = %v, want nil", got)
	}
	if got := distributeInt(10, -3); got != nil {
		t.Errorf("distributeInt(10, -3) = %v, want nil", got)
	}
}

func testDistributeIntEven(t *testing.T) {
	got := distributeInt(10, 5)
	want := []int64{2, 2, 2, 2, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("distributeInt(10, 5) = %v, want %v", got, want)
	}
}

func testDistributeIntRemainder(t *testing.T) {
	got := distributeInt(10, 3)
	want := []int64{3, 3, 4} // base=10/3=3 each, remainder (1) on the LAST bucket
	if !reflect.DeepEqual(got, want) {
		t.Errorf("distributeInt(10, 3) = %v, want %v", got, want)
	}
	assertInt64SliceSums(t, got, 10)
}

func testDistributeIntSingleBucket(t *testing.T) {
	got := distributeInt(10, 1)
	want := []int64{10}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("distributeInt(10, 1) = %v, want %v", got, want)
	}
}

func testDistributeIntZeroTotal(t *testing.T) {
	got := distributeInt(0, 4)
	want := []int64{0, 0, 0, 0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("distributeInt(0, 4) = %v, want %v", got, want)
	}
}

// assertInt64SliceSums checks got sums to want, regardless of how it is split.
func assertInt64SliceSums(t *testing.T, got []int64, want int64) {
	t.Helper()
	var sum int64
	for _, v := range got {
		sum += v
	}
	if sum != want {
		t.Errorf("%v sums to %d, want %d", got, sum, want)
	}
}

// TestBuildSeverities pins that every synthetic issue is MINOR except the
// last, which alone carries the severity that would reproduce the rating —
// including when count is 1, where that single issue must still get it.
func TestBuildSeverities(t *testing.T) {
	t.Run("no rating gives all MINOR", testBuildSeveritiesNoRating)
	t.Run("with a rating, only the last element carries it", testBuildSeveritiesWithRating)
	t.Run("count=1 with a rating puts it on the single element", testBuildSeveritiesSingleWithRating)
}

func testBuildSeveritiesNoRating(t *testing.T) {
	got := buildSeverities(4, 4.0, false)
	want := []string{"MINOR", "MINOR", "MINOR", "MINOR"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildSeverities(4, 4.0, false) = %v, want %v", got, want)
	}
}

func testBuildSeveritiesWithRating(t *testing.T) {
	got := buildSeverities(4, 4.0, true)
	want := []string{"MINOR", "MINOR", "MINOR", "CRITICAL"} // ratingToSeverity(4.0) == CRITICAL
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildSeverities(4, 4.0, true) = %v, want %v", got, want)
	}
}

func testBuildSeveritiesSingleWithRating(t *testing.T) {
	got := buildSeverities(1, 4.0, true)
	want := []string{"CRITICAL"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildSeverities(1, 4.0, true) = %v, want %v", got, want)
	}
}

// TestBuildEfforts pins that only CODE_SMELL issues ever carry a distributed
// sqale_index effort — bugs/vulnerabilities leave Effort unset regardless of
// sqaleIndex, and CODE_SMELL itself yields nil when there's no debt to spread.
func TestBuildEfforts(t *testing.T) {
	if got := buildEfforts("BUG", 3, 90); got != nil {
		t.Errorf(`buildEfforts("BUG", 3, 90) = %v, want nil (only CODE_SMELL carries effort)`, got)
	}
	if got := buildEfforts("VULNERABILITY", 3, 90); got != nil {
		t.Errorf(`buildEfforts("VULNERABILITY", 3, 90) = %v, want nil`, got)
	}
	if got := buildEfforts("CODE_SMELL", 3, 0); got != nil {
		t.Errorf(`buildEfforts("CODE_SMELL", 3, 0) = %v, want nil (sqaleIndex=0)`, got)
	}

	got := buildEfforts("CODE_SMELL", 3, 90)
	want := distributeInt(90, 3)
	if !reflect.DeepEqual(got, want) {
		t.Errorf(`buildEfforts("CODE_SMELL", 3, 90) = %v, want %v`, got, want)
	}
	assertInt64SliceSums(t, got, 90)
}

// TestBuildSyntheticIssues covers the top-level orchestrator: it must skip a
// count-0 type entirely (no issues, no ad-hoc rule) while still handling its
// siblings, and must wire buildSeverities/buildEfforts correctly per type.
// Each case is its own top-level function, called via t.Run — a previous
// nested-closure version of a test in this file was rejected by SonarCloud's
// cognitive-complexity gate, so this split style is mandatory here.
func TestBuildSyntheticIssues(t *testing.T) {
	t.Run("all counts zero yields no issues and no ad-hoc rules", testSyntheticIssuesAllZero)
	t.Run("bugs with a reliability rating puts it on the last issue only", testSyntheticIssuesBugsWithRating)
	t.Run("code smells distribute sqale_index effort across every issue", testSyntheticIssuesCodeSmellEffort)
	t.Run("a zero-count type contributes nothing while a sibling type still does", testSyntheticIssuesMixedZeroAndNonZero)
}

func testSyntheticIssuesAllZero(t *testing.T) {
	issues, rules := buildSyntheticIssues(nil, "comp_key")
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %d: %+v", len(issues), issues)
	}
	if len(rules) != 0 {
		t.Errorf("expected no ad-hoc rules, got %d: %+v", len(rules), rules)
	}
}

func testSyntheticIssuesBugsWithRating(t *testing.T) {
	measures := []scanreport.MeasureInput{
		measureIn("bugs", "3"), measureIn("reliability_rating", "4.0"),
	}
	issues, rules := buildSyntheticIssues(measures, "comp_key")
	if len(issues) != 3 {
		t.Fatalf("expected 3 issues, got %d: %+v", len(issues), issues)
	}
	for i, iss := range issues {
		if iss.Type != "BUG" {
			t.Errorf("issue %d: type = %q, want BUG", i, iss.Type)
		}
		if iss.Component != "comp_key" {
			t.Errorf("issue %d: component = %q, want comp_key", i, iss.Component)
		}
	}
	if issues[0].Severity != "MINOR" || issues[1].Severity != "MINOR" {
		t.Errorf("expected the first two issues MINOR, got %q, %q", issues[0].Severity, issues[1].Severity)
	}
	if want := ratingToSeverity(4.0); issues[2].Severity != want {
		t.Errorf("last issue severity = %q, want %q", issues[2].Severity, want)
	}
	if len(rules) != 1 || rules[0].Type != "BUG" {
		t.Fatalf("expected exactly 1 BUG ad-hoc rule, got %+v", rules)
	}
}

func testSyntheticIssuesCodeSmellEffort(t *testing.T) {
	measures := []scanreport.MeasureInput{
		measureIn("code_smells", "5"), measureIn("sqale_index", "50"),
	}
	issues, rules := buildSyntheticIssues(measures, "comp_key")
	if len(issues) != 5 {
		t.Fatalf("expected 5 issues, got %d: %+v", len(issues), issues)
	}
	for i, iss := range issues {
		if iss.Type != "CODE_SMELL" {
			t.Errorf("issue %d: type = %q, want CODE_SMELL", i, iss.Type)
		}
	}
	if sum := sumIssueEffortMinutes(t, issues); sum != 50 {
		t.Errorf("efforts sum to %d minutes, want 50", sum)
	}
	if len(rules) != 1 || rules[0].Type != "CODE_SMELL" {
		t.Fatalf("expected exactly 1 CODE_SMELL ad-hoc rule, got %+v", rules)
	}
}

func testSyntheticIssuesMixedZeroAndNonZero(t *testing.T) {
	measures := []scanreport.MeasureInput{
		measureIn("bugs", "0"), measureIn("vulnerabilities", "2"),
	}
	issues, rules := buildSyntheticIssues(measures, "comp_key")
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues (bugs=0 contributes none), got %d: %+v", len(issues), issues)
	}
	for i, iss := range issues {
		if iss.Type != "VULNERABILITY" {
			t.Errorf("issue %d: type = %q, want VULNERABILITY (no BUG issues expected)", i, iss.Type)
		}
	}
	if len(rules) != 1 || rules[0].Type != "VULNERABILITY" {
		t.Fatalf("expected exactly 1 VULNERABILITY ad-hoc rule and no BUG rule, got %+v", rules)
	}
}

// sumIssueEffortMinutes parses every issue's "<n>min" Effort string and sums
// the minutes, failing the test if any issue's Effort doesn't parse.
func sumIssueEffortMinutes(t *testing.T, issues []scanreport.ExternalIssueInput) int64 {
	t.Helper()
	var sum int64
	for _, iss := range issues {
		minutes, err := strconv.ParseInt(strings.TrimSuffix(iss.Effort, "min"), 10, 64)
		if err != nil {
			t.Fatalf("could not parse effort %q as \"<n>min\": %v", iss.Effort, err)
		}
		sum += minutes
	}
	return sum
}

// TestBuildSyntheticDuplication covers the same-file duplication synthesis:
// the <2/<2 guard, the duplicatedBlocks>duplicatedLines clamp, and the
// deliberate zero-value OtherFileRef that made the live SonarCloud experiment
// succeed. Each case is its own top-level function, called via t.Run, per the
// same mandatory split style as TestBuildSyntheticIssues above.
func TestBuildSyntheticDuplication(t *testing.T) {
	t.Run("100 duplicated lines across 2 blocks matches the live-verified case", testDuplicationLiveVerified)
	t.Run("fewer than 2 duplicated lines yields nil", testDuplicationTooFewLines)
	t.Run("fewer than 2 duplicated blocks yields nil", testDuplicationTooFewBlocks)
	t.Run("more blocks than lines clamps down to one line per block", testDuplicationClampedBlocks)
	t.Run("every duplicate entry leaves OtherFileRef at its zero value", testDuplicationOtherFileRefZero)
}

func testDuplicationLiveVerified(t *testing.T) {
	got := buildSyntheticDuplication([]scanreport.MeasureInput{
		measureIn("duplicated_lines", "100"), measureIn("duplicated_blocks", "2"),
	})
	dup := requireSingleDuplication(t, got)
	if len(dup.Duplicate) != 1 {
		t.Fatalf("expected exactly 1 Duplicate entry, got %d", len(dup.Duplicate))
	}
	origin := dup.OriginPosition
	other := dup.Duplicate[0].GetRange()
	// Pinned to the exact live-verified split (50+50), not just "non-nil":
	// this precise case produced duplicated_lines=100/duplicated_blocks=2 on
	// real SonarCloud.
	if origin.GetStartLine() != 1 || origin.GetEndLine() != 50 {
		t.Errorf("origin range = [%d,%d], want [1,50]", origin.GetStartLine(), origin.GetEndLine())
	}
	if other.GetStartLine() != 51 || other.GetEndLine() != 100 {
		t.Errorf("duplicate range = [%d,%d], want [51,100]", other.GetStartLine(), other.GetEndLine())
	}
	assertRangesSpan(t, []*pb.TextRange{origin, other}, 100)
}

func testDuplicationTooFewLines(t *testing.T) {
	if got := buildSyntheticDuplication([]scanreport.MeasureInput{
		measureIn("duplicated_lines", "1"), measureIn("duplicated_blocks", "5"),
	}); got != nil {
		t.Errorf("expected nil for duplicated_lines=1, got %+v", got)
	}
	if got := buildSyntheticDuplication([]scanreport.MeasureInput{
		measureIn("duplicated_lines", "0"), measureIn("duplicated_blocks", "5"),
	}); got != nil {
		t.Errorf("expected nil for duplicated_lines=0, got %+v", got)
	}
}

func testDuplicationTooFewBlocks(t *testing.T) {
	if got := buildSyntheticDuplication([]scanreport.MeasureInput{
		measureIn("duplicated_lines", "10"), measureIn("duplicated_blocks", "1"),
	}); got != nil {
		t.Errorf("expected nil for duplicated_blocks=1, got %+v", got)
	}
	if got := buildSyntheticDuplication([]scanreport.MeasureInput{
		measureIn("duplicated_lines", "10"), measureIn("duplicated_blocks", "0"),
	}); got != nil {
		t.Errorf("expected nil for duplicated_blocks=0, got %+v", got)
	}
}

func testDuplicationClampedBlocks(t *testing.T) {
	got := buildSyntheticDuplication([]scanreport.MeasureInput{
		measureIn("duplicated_lines", "3"), measureIn("duplicated_blocks", "10"),
	})
	dup := requireSingleDuplication(t, got)
	ranges := append([]*pb.TextRange{dup.OriginPosition}, duplicateRanges(dup)...)
	if len(ranges) != 3 {
		t.Fatalf("expected duplicated_blocks clamped down to duplicated_lines=3 occurrences, got %d", len(ranges))
	}
	for i, r := range ranges {
		if r.GetStartLine() > r.GetEndLine() {
			t.Errorf("range %d is malformed: start=%d > end=%d", i, r.GetStartLine(), r.GetEndLine())
		}
	}
	assertRangesSpan(t, ranges, 3)
}

func testDuplicationOtherFileRefZero(t *testing.T) {
	got := buildSyntheticDuplication([]scanreport.MeasureInput{
		measureIn("duplicated_lines", "100"), measureIn("duplicated_blocks", "2"),
	})
	dup := requireSingleDuplication(t, got)
	for i, d := range dup.Duplicate {
		// Deliberate: SonarCloud rejects a Duplicate whose OtherFileRef
		// explicitly names its own file, so it must be left at its zero value.
		if d.GetOtherFileRef() != 0 {
			t.Errorf("Duplicate entry %d: OtherFileRef = %d, want 0 (zero value)", i, d.GetOtherFileRef())
		}
	}
}

// requireSingleDuplication asserts got holds exactly one Duplication (the
// shape buildSyntheticDuplication always returns when non-nil) and returns it.
func requireSingleDuplication(t *testing.T, got []*pb.Duplication) *pb.Duplication {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 Duplication, got %d: %+v", len(got), got)
	}
	if got[0].OriginPosition == nil {
		t.Fatalf("expected an OriginPosition, got nil")
	}
	return got[0]
}

// duplicateRanges extracts the TextRange from each Duplicate entry.
func duplicateRanges(dup *pb.Duplication) []*pb.TextRange {
	out := make([]*pb.TextRange, len(dup.Duplicate))
	for i, d := range dup.Duplicate {
		out[i] = d.GetRange()
	}
	return out
}

// assertRangesSpan checks that ranges (each inclusive of StartLine..EndLine)
// together cover exactly want lines.
func assertRangesSpan(t *testing.T, ranges []*pb.TextRange, want int32) {
	t.Helper()
	var total int32
	for _, r := range ranges {
		total += r.GetEndLine() - r.GetStartLine() + 1
	}
	if total != want {
		t.Errorf("ranges span %d lines total, want %d", total, want)
	}
}
