package scanreport

import (
	"testing"
	"time"

	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

func TestHotspotIssueTagIsSQSHotspot(t *testing.T) {
	// Issue #423 requires this exact tag on every migrated hotspot.
	if HotspotIssueTag != "sqs-hotspot" {
		t.Fatalf("HotspotIssueTag = %q, want %q", HotspotIssueTag, "sqs-hotspot")
	}
}

func TestConvertHotspotToIssue(t *testing.T) {
	created := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)

	tests := []struct {
		name string
		in   HotspotInput
		want IssueInput
	}{
		{
			name: "full text range is preserved",
			in: HotspotInput{
				Key:                      "AXsomeKey",
				CreationDate:             created,
				RuleRepo:                 "java",
				RuleKey:                  "S2245",
				Message:                  "Make sure that using this pseudorandom number generator is safe here.",
				Component:                "proj:src/Main.java",
				VulnerabilityProbability: "MEDIUM",
				Status:                   HotspotStatusToReview,
				StartLine:                12,
				EndLine:                  12,
				StartOff:                 4,
				EndOff:                   22,
			},
			want: IssueInput{
				Key:          "AXsomeKey",
				CreationDate: created,
				RuleRepo:     "java",
				RuleKey:      "S2245",
				Message:      "Make sure that using this pseudorandom number generator is safe here.",
				Severity:     "",
				StartLine:    12,
				EndLine:      12,
				StartOff:     4,
				EndOff:       22,
				Component:    "proj:src/Main.java",
			},
		},
		{
			name: "HIGH probability still yields no severity override",
			in: HotspotInput{
				RuleRepo:                 "python",
				RuleKey:                  "S4502",
				Component:                "proj:app.py",
				VulnerabilityProbability: "HIGH",
				StartLine:                7,
				EndLine:                  7,
			},
			want: IssueInput{
				RuleRepo:  "python",
				RuleKey:   "S4502",
				Severity:  "",
				StartLine: 7,
				EndLine:   7,
				Component: "proj:app.py",
			},
		},
		{
			name: "missing line drops the text range entirely",
			in: HotspotInput{
				RuleRepo:  "java",
				RuleKey:   "S2077",
				Component: "proj:src/Db.java",
				StartOff:  5,
				EndOff:    9,
			},
			want: IssueInput{
				RuleRepo:  "java",
				RuleKey:   "S2077",
				Component: "proj:src/Db.java",
			},
		},
		{
			name: "end line below start line is clamped up",
			in: HotspotInput{
				RuleRepo:  "docker",
				RuleKey:   "S6505",
				Component: "proj:Dockerfile",
				StartLine: 30,
				EndLine:   0,
			},
			want: IssueInput{
				RuleRepo:  "docker",
				RuleKey:   "S6505",
				Component: "proj:Dockerfile",
				StartLine: 30,
				EndLine:   30,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ConvertHotspotToIssue(tc.in)
			if got != tc.want {
				t.Errorf("ConvertHotspotToIssue()\n got = %+v\nwant = %+v", got, tc.want)
			}
		})
	}
}

// The real scanner never tells the server what kind of finding a rule raises —
// the native Issue message has no type field. Converted hotspots must
// therefore carry no severity override, so Cloud applies the converted rule's
// own impact severity.
func TestConvertedHotspotEmitsNoSeverityOverride(t *testing.T) {
	hotspots := []HotspotInput{
		{RuleRepo: "python", RuleKey: "S4502", Component: "p:a.py", StartLine: 3, EndLine: 3, VulnerabilityProbability: "HIGH"},
		{RuleRepo: "python", RuleKey: "S1313", Component: "p:a.py", StartLine: 4, EndLine: 4, VulnerabilityProbability: "LOW"},
	}
	issues, skipped := ConvertHotspotsToIssues(hotspots)
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}

	cr := NewComponentRef()
	ref := cr.Get("p:a.py")

	byRef := BuildIssues(issues, cr)
	got := byRef[ref]
	if len(got) != 2 {
		t.Fatalf("built %d issues for ref 2, want 2", len(got))
	}
	for _, iss := range got {
		if iss.OverriddenSeverity != nil {
			t.Errorf("rule %s:%s got OverriddenSeverity=%v, want nil",
				iss.GetRuleRepository(), iss.GetRuleKey(), iss.GetOverriddenSeverity())
		}
		if len(iss.GetOverriddenImpacts()) != 0 {
			t.Errorf("rule %s:%s got OverriddenImpacts=%v, want none",
				iss.GetRuleRepository(), iss.GetRuleKey(), iss.GetOverriddenImpacts())
		}
	}
	if got[0].GetTextRange().GetStartLine() != 3 {
		t.Errorf("first issue start line = %d, want 3", got[0].GetTextRange().GetStartLine())
	}
}

// A converted hotspot must be indistinguishable on the wire from the native
// issue the scanner would report for the same rule.
func TestConvertedHotspotMatchesNativeIssueOnTheWire(t *testing.T) {
	cr := NewComponentRef()
	ref := cr.Get("p:Main.java")

	hotspotIssues, _ := ConvertHotspotsToIssues([]HotspotInput{{
		RuleRepo: "java", RuleKey: "S2245", Message: "msg",
		Component: "p:Main.java", StartLine: 9, EndLine: 9, StartOff: 1, EndOff: 5,
		VulnerabilityProbability: "MEDIUM", Status: HotspotStatusReviewed, Resolution: HotspotResolutionSafe,
	}})
	native := []IssueInput{{
		RuleRepo: "java", RuleKey: "S2245", Message: "msg",
		Component: "p:Main.java", StartLine: 9, EndLine: 9, StartOff: 1, EndOff: 5,
	}}

	fromHotspot := BuildIssues(hotspotIssues, cr)[ref]
	fromNative := BuildIssues(native, cr)[ref]
	if len(fromHotspot) != 1 || len(fromNative) != 1 {
		t.Fatalf("expected 1 issue each, got %d and %d", len(fromHotspot), len(fromNative))
	}

	h, n := fromHotspot[0], fromNative[0]
	if h.GetRuleRepository() != n.GetRuleRepository() || h.GetRuleKey() != n.GetRuleKey() ||
		h.GetMsg() != n.GetMsg() || h.GetOverriddenSeverity() != n.GetOverriddenSeverity() {
		t.Errorf("converted hotspot differs from native issue:\nhotspot=%+v\nnative =%+v", h, n)
	}
	if h.GetTextRange().GetStartLine() != n.GetTextRange().GetStartLine() ||
		h.GetTextRange().GetStartOffset() != n.GetTextRange().GetStartOffset() {
		t.Errorf("text ranges differ: %+v vs %+v", h.GetTextRange(), n.GetTextRange())
	}
	// Guard the structural premise: the native Issue message carries no type.
	var _ *pb.Issue = h
}

func TestConvertHotspotsToIssuesSkipsRulelessHotspots(t *testing.T) {
	issues, skipped := ConvertHotspotsToIssues([]HotspotInput{
		{RuleRepo: "java", RuleKey: "S2245", Component: "p:A.java", StartLine: 1},
		{RuleRepo: "", RuleKey: "", Component: "p:B.java", StartLine: 2},
		{RuleRepo: "java", RuleKey: "", Component: "p:C.java", StartLine: 3},
		{RuleRepo: "", RuleKey: "S1313", Component: "p:D.java", StartLine: 4},
	})
	if len(issues) != 1 {
		t.Errorf("kept %d issues, want 1", len(issues))
	}
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}
}

func TestHotspotIssueStatus(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		resolution string
		want       string
		needsSync  bool
	}{
		{"to review stays open", "TO_REVIEW", "", IssueStatusOpen, false},
		{"to review ignores stray resolution", "TO_REVIEW", "SAFE", IssueStatusOpen, false},
		{"reviewed safe is a false positive", "REVIEWED", "SAFE", IssueStatusFalsePositive, true},
		{"reviewed fixed is accepted", "REVIEWED", "FIXED", IssueStatusAccepted, true},
		{"reviewed acknowledged is accepted", "REVIEWED", "ACKNOWLEDGED", IssueStatusAccepted, true},
		{"reviewed with unknown resolution is accepted", "REVIEWED", "WHATEVER", IssueStatusAccepted, true},
		{"reviewed with no resolution is accepted", "REVIEWED", "", IssueStatusAccepted, true},
		{"lowercase input is handled", "reviewed", "safe", IssueStatusFalsePositive, true},
		{"padded input is handled", " REVIEWED ", " ACKNOWLEDGED ", IssueStatusAccepted, true},
		{"unknown status stays open", "SOMETHING_ELSE", "SAFE", IssueStatusOpen, false},
		{"empty status stays open", "", "", IssueStatusOpen, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HotspotIssueStatus(tc.status, tc.resolution); got != tc.want {
				t.Errorf("HotspotIssueStatus(%q, %q) = %q, want %q", tc.status, tc.resolution, got, tc.want)
			}
			if got := HotspotNeedsTriage(tc.status, tc.resolution); got != tc.needsSync {
				t.Errorf("HotspotNeedsTriage(%q, %q) = %v, want %v", tc.status, tc.resolution, got, tc.needsSync)
			}
		})
	}
}

// ACKNOWLEDGED must no longer collapse onto the same target state as SAFE —
// that conflation was forced by Cloud's hotspot API and the issue model can
// represent the difference.
func TestAcknowledgedIsDistinctFromSafe(t *testing.T) {
	safe := HotspotIssueStatus(HotspotStatusReviewed, HotspotResolutionSafe)
	ack := HotspotIssueStatus(HotspotStatusReviewed, HotspotResolutionAcknowledged)
	if safe == ack {
		t.Errorf("SAFE and ACKNOWLEDGED both map to %q; they must differ", safe)
	}
}

// Creation dates ride the existing changeset-backdating mechanism, which keys
// off IssueInput.Key and CreationDate. Both must survive conversion or every
// migrated hotspot would be dated to the migration run.
func TestConvertedHotspotsCarryKeyAndCreationDateForBackdating(t *testing.T) {
	created := time.Date(2019, 11, 2, 8, 30, 0, 0, time.UTC)
	issues, _ := ConvertHotspotsToIssues([]HotspotInput{{
		Key: "AY-hotspot-1", CreationDate: created,
		RuleRepo: "java", RuleKey: "S2245", Component: "p:Main.java", StartLine: 5, EndLine: 5,
	}})
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}
	if issues[0].Key != "AY-hotspot-1" {
		t.Errorf("Key = %q, want %q — backdating safety-split keys off it", issues[0].Key, "AY-hotspot-1")
	}
	if !issues[0].CreationDate.Equal(created) {
		t.Errorf("CreationDate = %v, want %v", issues[0].CreationDate, created)
	}

	// End to end through the mechanism that actually applies the date.
	cs := map[string]*pb.Changesets{
		"p:Main.java": {
			Changeset:            []*pb.Changesets_Changeset{{Revision: "r1", Author: "a@b.c", Date: time.Now().UnixMilli()}},
			ChangesetIndexByLine: []int32{0, 0, 0, 0, 0, 0},
		},
	}
	BackdateChangesets([]ExtractedIssue{{
		Key: issues[0].Key, Component: "p:Main.java",
		StartLine: 5, EndLine: 5, CreationDate: created,
	}}, cs, time.Now())

	// ChangesetIndexByLine is 0-based, so line 5 lives at index 4.
	idx := cs["p:Main.java"].GetChangesetIndexByLine()[4]
	gotMs := cs["p:Main.java"].GetChangeset()[idx].GetDate()
	if gotMs != created.UnixMilli() {
		t.Errorf("line 5 changeset date = %d, want %d (original hotspot creation date)", gotMs, created.UnixMilli())
	}
}
