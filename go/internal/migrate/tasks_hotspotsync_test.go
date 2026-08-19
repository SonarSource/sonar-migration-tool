// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// #356: filter now runs source-side directly on matchableHotspot —
// no longer pair-based, since we no longer pre-match against the
// full Cloud hotspot list before filtering.
func TestHotspotHasManualChanges(t *testing.T) {
	comment := hotspotComment{Login: "alice", Markdown: "please review"}

	tests := []struct {
		name string
		h    matchableHotspot
		want bool
	}{
		{name: "TO_REVIEW without comments — skip", h: matchableHotspot{Status: "TO_REVIEW"}, want: false},
		{name: "TO_REVIEW with comments — sync", h: matchableHotspot{Status: "TO_REVIEW", Comments: []hotspotComment{comment}}, want: true},
		// #350: REVIEWED without a resolution carries no payload.
		{name: "REVIEWED no resolution — skip", h: matchableHotspot{Status: "REVIEWED"}, want: false},
		{name: "REVIEWED + SAFE — sync", h: matchableHotspot{Status: "REVIEWED", Resolution: "SAFE"}, want: true},
		{name: "REVIEWED + ACKNOWLEDGED — sync", h: matchableHotspot{Status: "REVIEWED", Resolution: "ACKNOWLEDGED"}, want: true},
		{name: "REVIEWED + FIXED — sync", h: matchableHotspot{Status: "REVIEWED", Resolution: "FIXED"}, want: true},
		{name: "REVIEWED + unknown resolution — skip", h: matchableHotspot{Status: "REVIEWED", Resolution: "WHATEVER"}, want: false},
		{name: "REVIEWED + unknown resolution + comment — sync via comment", h: matchableHotspot{Status: "REVIEWED", Resolution: "WHATEVER", Comments: []hotspotComment{comment}}, want: true},
		{name: "case-insensitive status / resolution", h: matchableHotspot{Status: "reviewed", Resolution: "safe"}, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hotspotHasManualChanges(tc.h)
			if got != tc.want {
				t.Errorf("hotspotHasManualChanges(%+v) = %v, want %v", tc.h, got, tc.want)
			}
		})
	}
}

// #323 follow-up: cross-branch duplicate source hotspots — same
// (component, ruleKey, line) but different SQS keys — must collapse
// to a single representative before dispatch, with ACKNOWLEDGED
// winning over SAFE/FIXED so a cautious developer review on one
// branch is never silently overwritten by a SAFE sibling on another.
func TestDedupeActionableHotspotsAckWinsOverSafe(t *testing.T) {
	in := []matchableHotspot{
		{Key: "src-safe", Component: "p:f.py", RuleKey: "py:S1", Line: 42,
			Status: "REVIEWED", Resolution: "SAFE",
			Comments: []hotspotComment{{Login: "alice", Markdown: "safe note"}}},
		{Key: "src-ack", Component: "p:f.py", RuleKey: "py:S1", Line: 42,
			Status: "REVIEWED", Resolution: "ACKNOWLEDGED",
			Comments: []hotspotComment{{Login: "bob", Markdown: "ack note"}}},
		{Key: "src-fix", Component: "p:f.py", RuleKey: "py:S1", Line: 42,
			Status: "REVIEWED", Resolution: "FIXED"},
		// Unrelated location — must survive untouched.
		{Key: "src-other", Component: "p:g.py", RuleKey: "py:S2", Line: 7,
			Status: "REVIEWED", Resolution: "SAFE"},
	}
	out := dedupeActionableHotspots(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 groups (one per location), got %d: %+v", len(out), out)
	}

	var collapsed, unrelated *matchableHotspot
	for i := range out {
		switch out[i].Component {
		case "p:f.py":
			collapsed = &out[i]
		case "p:g.py":
			unrelated = &out[i]
		}
	}
	if collapsed == nil {
		t.Fatalf("missing collapsed group for p:f.py: %+v", out)
	}
	if unrelated == nil {
		t.Fatalf("missing unrelated group for p:g.py: %+v", out)
	}

	if strings.ToUpper(collapsed.Resolution) != "ACKNOWLEDGED" {
		t.Errorf("ACKNOWLEDGED must win the dedup, got resolution=%q (key=%q)", collapsed.Resolution, collapsed.Key)
	}
	if collapsed.Key != "src-ack" {
		t.Errorf("rep key must be the ACK source, got %q", collapsed.Key)
	}
	// Comments are the union — ACK rep keeps both its own and the
	// SAFE sibling's so notes aren't lost.
	if len(collapsed.Comments) != 2 {
		t.Errorf("expected 2 comments (union of ACK + SAFE), got %d: %+v", len(collapsed.Comments), collapsed.Comments)
	}

	if unrelated.Resolution != "SAFE" {
		t.Errorf("unrelated location must keep SAFE, got %q", unrelated.Resolution)
	}
}

// #323 follow-up: when all duplicates share the same resolution, dedup
// still collapses to a single representative — first wins by sorted
// source key.
func TestDedupeActionableHotspotsAllSafe(t *testing.T) {
	in := []matchableHotspot{
		{Key: "src-b", Component: "p:f.py", RuleKey: "py:S1", Line: 42,
			Status: "REVIEWED", Resolution: "SAFE"},
		{Key: "src-a", Component: "p:f.py", RuleKey: "py:S1", Line: 42,
			Status: "REVIEWED", Resolution: "SAFE"},
	}
	out := dedupeActionableHotspots(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 group, got %d", len(out))
	}
	if out[0].Key != "src-a" {
		t.Errorf("expected src-a (alphabetically first) as rep, got %q", out[0].Key)
	}
}

// #323 follow-up: when source hotspots target distinct cloud locations,
// dedup must be a no-op.
func TestDedupeActionableHotspotsNoCollisions(t *testing.T) {
	in := []matchableHotspot{
		{Key: "a", Component: "p:f.py", RuleKey: "py:S1", Line: 10, Status: "REVIEWED", Resolution: "SAFE"},
		{Key: "b", Component: "p:f.py", RuleKey: "py:S1", Line: 20, Status: "REVIEWED", Resolution: "SAFE"},
		{Key: "c", Component: "p:f.py", RuleKey: "py:S2", Line: 10, Status: "REVIEWED", Resolution: "SAFE"},
		{Key: "d", Component: "p:g.py", RuleKey: "py:S1", Line: 10, Status: "REVIEWED", Resolution: "SAFE"},
	}
	out := dedupeActionableHotspots(in)
	if len(out) != 4 {
		t.Errorf("expected 4 distinct groups (no dedup), got %d", len(out))
	}
}

// #392 follow-up: two hotspots of the same rule firing on different
// columns of the same line (e.g. sys.argv[1] and sys.argv[2]) must
// stay as TWO distinct source reps post-dedup. Cross-branch copies
// of the SAME (component, rule, line, offset) still collapse.
func TestDedupeActionableHotspotsOffsetDistinguishesCoLocated(t *testing.T) {
	in := []matchableHotspot{
		// Branch main: two hotspots at line 35, columns 17 and 35.
		{Key: "main-a", Component: "p:f.py", RuleKey: "py:S4823", Line: 35, Offset: 17, Status: "REVIEWED", Resolution: "SAFE"},
		{Key: "main-b", Component: "p:f.py", RuleKey: "py:S4823", Line: 35, Offset: 35, Status: "REVIEWED", Resolution: "SAFE"},
		// Branch develop: same two hotspots — should collapse with main's siblings.
		{Key: "dev-a", Component: "p:f.py", RuleKey: "py:S4823", Line: 35, Offset: 17, Status: "REVIEWED", Resolution: "SAFE"},
		{Key: "dev-b", Component: "p:f.py", RuleKey: "py:S4823", Line: 35, Offset: 35, Status: "REVIEWED", Resolution: "SAFE"},
	}
	out := dedupeActionableHotspots(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 distinct (line, offset) groups, got %d: %+v", len(out), out)
	}
	offsets := map[int]bool{}
	for _, h := range out {
		offsets[h.Offset] = true
	}
	if !offsets[17] || !offsets[35] {
		t.Errorf("expected both column offsets (17, 35) preserved as distinct reps, got %v", offsets)
	}
}

// #527: a comment counts as user-created only when its Login is
// non-empty — blank/whitespace-only logins are treated as
// SonarQube-technical.
func TestHotspotHasUserComment(t *testing.T) {
	tests := []struct {
		name string
		in   []hotspotComment
		want bool
	}{
		{"no comments", nil, false},
		{"blank login", []hotspotComment{{Login: "", Markdown: "auto note"}}, false},
		{"whitespace-only login", []hotspotComment{{Login: "   ", Markdown: "auto note"}}, false},
		{"real login", []hotspotComment{{Login: "alice", Markdown: "reviewed"}}, true},
		{"mixed — one real among technical", []hotspotComment{{Login: ""}, {Login: "bob"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hotspotHasUserComment(tc.in); got != tc.want {
				t.Errorf("hotspotHasUserComment(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// #527: eligibility rule — not TO_REVIEW, or a user comment, makes a
// hotspot eligible for full sync; REVIEWED+ACKNOWLEDGED is always
// flagged as acknowledged regardless of the eligible verdict.
func TestHotspotSyncEligibility(t *testing.T) {
	tests := []struct {
		name           string
		status         string
		resolution     string
		hasUserComment bool
		wantEligible   bool
		wantAck        bool
	}{
		{"TO_REVIEW, no comment — excluded", "TO_REVIEW", "", false, false, false},
		{"TO_REVIEW, user comment — eligible", "TO_REVIEW", "", true, true, false},
		{"REVIEWED+SAFE — eligible", "REVIEWED", "SAFE", false, true, false},
		{"REVIEWED+FIXED — eligible", "REVIEWED", "FIXED", false, true, false},
		{"REVIEWED+ACKNOWLEDGED, no comment — eligible+ack", "REVIEWED", "ACKNOWLEDGED", false, true, true},
		{"REVIEWED+ACKNOWLEDGED, user comment — eligible+ack", "REVIEWED", "ACKNOWLEDGED", true, true, true},
		{"case-insensitive / whitespace", "  reviewed  ", " acknowledged ", false, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotEligible, gotAck := HotspotSyncEligibility(tc.status, tc.resolution, tc.hasUserComment)
			if gotEligible != tc.wantEligible || gotAck != tc.wantAck {
				t.Errorf("HotspotSyncEligibility(%q, %q, %v) = (%v, %v), want (%v, %v)",
					tc.status, tc.resolution, tc.hasUserComment, gotEligible, gotAck, tc.wantEligible, tc.wantAck)
			}
		})
	}
}

// #527: classifyHotspotForSync wraps HotspotSyncEligibility into the
// three dispatch buckets, deriving hasUserComment from Comments.
func TestClassifyHotspotForSync(t *testing.T) {
	tests := []struct {
		name string
		h    matchableHotspot
		want hotspotSyncCategory
	}{
		{"TO_REVIEW, no comment — excluded", matchableHotspot{Status: "TO_REVIEW"}, hotspotCategoryExcluded},
		{
			"TO_REVIEW, technical comment only — excluded",
			matchableHotspot{Status: "TO_REVIEW", Comments: []hotspotComment{{Login: ""}}},
			hotspotCategoryExcluded,
		},
		{
			"TO_REVIEW, user comment — eligible",
			matchableHotspot{Status: "TO_REVIEW", Comments: []hotspotComment{{Login: "alice"}}},
			hotspotCategoryEligible,
		},
		{"REVIEWED+SAFE — eligible", matchableHotspot{Status: "REVIEWED", Resolution: "SAFE"}, hotspotCategoryEligible},
		{"REVIEWED+ACKNOWLEDGED — acknowledged", matchableHotspot{Status: "REVIEWED", Resolution: "ACKNOWLEDGED"}, hotspotCategoryAcknowledged},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyHotspotForSync(tc.h); got != tc.want {
				t.Errorf("classifyHotspotForSync(%+v) = %v, want %v", tc.h, got, tc.want)
			}
		})
	}
}

func TestChunkStrings(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		n    int
		want [][]string
	}{
		{"empty", nil, 3, nil},
		{"n<=0", []string{"a"}, 0, nil},
		{"exact multiple", []string{"a", "b", "c", "d"}, 2, [][]string{{"a", "b"}, {"c", "d"}}},
		{"remainder", []string{"a", "b", "c"}, 2, [][]string{{"a", "b"}, {"c"}}},
		{"n larger than input", []string{"a", "b"}, 60, [][]string{{"a", "b"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := chunkStrings(tc.in, tc.n)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("chunkStrings(%v, %d) = %v, want %v", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestGroupHotspotsByBranch(t *testing.T) {
	in := []matchableHotspot{
		{Key: "a", Branch: "main"},
		{Key: "b", Branch: "develop"},
		{Key: "c", Branch: "main"},
		{Key: "d"}, // no branch — empty-string bucket
	}
	got := groupHotspotsByBranch(in)
	if len(got["main"]) != 2 || len(got["develop"]) != 1 || len(got[""]) != 1 {
		t.Fatalf("unexpected grouping: %+v", got)
	}
}

func TestDistinctRuleKeys(t *testing.T) {
	in := []matchableHotspot{
		{RuleKey: "java:S2245"},
		{RuleKey: "java:S2077"},
		{RuleKey: "java:S2245"},
		{RuleKey: ""},
	}
	got := distinctRuleKeys(in)
	want := []string{"java:S2077", "java:S2245"} // sorted
	if !reflect.DeepEqual(got, want) {
		t.Errorf("distinctRuleKeys(...) = %v, want %v", got, want)
	}
}

// #527: buildCloudIssueIndex must chunk rule keys to stay under the
// per-request cap, issue one search per chunk, and index results by
// (bare file path, rule key) — the same scope the per-hotspot search
// used, just fetched once for the whole branch.
func TestBuildCloudIssueIndexChunksAndIndexes(t *testing.T) {
	var searchCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		searchCalls++
		rules := r.URL.Query().Get("rules")
		var issues []map[string]any
		for i, rk := range strings.Split(rules, ",") {
			issues = append(issues, map[string]any{
				"key": rk + "-issue", "rule": rk, "component": "proj:src/Main.java", "line": 10 + i,
			})
		}
		json.NewEncoder(w).Encode(map[string]any{
			"issues": issues,
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": len(issues)},
		})
	})
	e := newCustomCloudTest(t, mux)

	ruleKeys := make([]string, 0, 125)
	for i := 0; i < 125; i++ {
		ruleKeys = append(ruleKeys, "java:S"+strconv.Itoa(1000+i))
	}

	idx, err := buildCloudIssueIndex(context.Background(), e, "proj", "org", "main", ruleKeys)
	if err != nil {
		t.Fatalf("buildCloudIssueIndex: %v", err)
	}
	wantChunks := (len(ruleKeys) + cloudIssueSearchRuleChunkSize - 1) / cloudIssueSearchRuleChunkSize
	if searchCalls != wantChunks {
		t.Errorf("expected %d chunked search calls for %d rule keys, got %d", wantChunks, len(ruleKeys), searchCalls)
	}
	if len(idx) != len(ruleKeys) {
		t.Errorf("expected %d indexed (file, rule) entries, got %d", len(ruleKeys), len(idx))
	}
	sample := ruleKeys[0]
	candidates := idx[cloudIssueIndexKey{File: "src/Main.java", RuleKey: sample}]
	if len(candidates) != 1 || candidates[0].Key != sample+"-issue" {
		t.Errorf("expected indexed candidate for %q, got %+v", sample, candidates)
	}
}
