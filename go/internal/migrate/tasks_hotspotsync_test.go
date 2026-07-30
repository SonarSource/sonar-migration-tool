// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// mountHotspotsShowEmpty installs a GET /api/hotspots/show handler on mux that
// always responds with an empty comment list — the standard no-prior-comments
// fixture used by syncOneHotspot tests.
func mountHotspotsShowEmpty(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/hotspots/show", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"comment": []map[string]any{}})
	})
}

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

// #412: hotspotMatchScore — a non-empty rule mismatch is a hard reject;
// an empty ruleKey on either side is tolerated (preserving the pre-#412
// empty-rule fallback), and the score otherwise accumulates across
// message/file/line/offset/vulnerabilityProbability/author.
func TestHotspotMatchScore(t *testing.T) {
	base := matchableHotspot{
		RuleKey: "python:S4823", Component: "a.py", Line: 35, Offset: 17,
		Message: "Make sure this is safe", VulnerabilityProbability: "HIGH", Author: "alice",
	}

	tests := []struct {
		name      string
		candidate matchableHotspot
		want      int
	}{
		{"non-empty rule mismatch rejects", withHotspotRule(base, "python:S9999"), -1},
		{"identical — max score", base, 7},
		{"candidate empty ruleKey — tolerated, still scored", withHotspotRule(base, ""), 7},
		{
			"source has offset, candidate offset missing — no offset point",
			withHotspotOffset(base, 0),
			6,
		},
		{
			"very different message, else equal",
			withHotspotMessage(base, "An entirely unrelated finding description"),
			5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hotspotMatchScore(base, tc.candidate); got != tc.want {
				t.Errorf("hotspotMatchScore = %d, want %d", got, tc.want)
			}
		})
	}
}

func withHotspotRule(h matchableHotspot, rule string) matchableHotspot   { h.RuleKey = rule; return h }
func withHotspotOffset(h matchableHotspot, offset int) matchableHotspot  { h.Offset = offset; return h }
func withHotspotMessage(h matchableHotspot, msg string) matchableHotspot { h.Message = msg; return h }

// #412: classifyHotspotCandidatesByScore — exact match (rule-compatible +
// file + line + message) wins when unique; otherwise the approximate score
// must clear hotspotApproxMatchThreshold and uniquely qualify.
func TestClassifyHotspotCandidatesByScore(t *testing.T) {
	source := matchableHotspot{
		RuleKey: "python:S4823", Component: "a.py", Line: 35, Offset: 17,
		Message: "Make sure this is safe", VulnerabilityProbability: "HIGH", Author: "alice",
	}
	exactMatch := source
	exactMatch.Key = "h-exact"

	approxMatch := matchableHotspot{
		Key: "h-approx", RuleKey: "python:S4823", Component: "a.py", Line: 35, Offset: 17,
		Message: "Make sure this stays safe", VulnerabilityProbability: "HIGH", Author: "alice",
	} // message similar (+1), file+line+offset+vulnProb+author (+5) = 6

	belowThreshold := matchableHotspot{
		Key: "h-low", RuleKey: "python:S4823", Component: "b.py", Line: 99, Offset: 0,
		Message: "unrelated", VulnerabilityProbability: "LOW", Author: "bob",
	}

	// Preserves the pre-#412 empty-ruleKey fallback: some SQ
	// versions/endpoints omit ruleKey from hotspot search results.
	emptyRuleExact := source
	emptyRuleExact.Key = "h-empty-rule"
	emptyRuleExact.RuleKey = ""

	tests := []struct {
		name        string
		candidates  []matchableHotspot
		wantKey     string
		wantOutcome syncOutcome
	}{
		{
			name:        "unique exact match — synced",
			candidates:  []matchableHotspot{belowThreshold, exactMatch},
			wantKey:     "h-exact",
			wantOutcome: syncOutcomeSynced,
		},
		{
			name:        "two exact matches — ambiguous",
			candidates:  []matchableHotspot{exactMatch, {Key: "h-exact-2", RuleKey: "python:S4823", Component: "a.py", Line: 35, Message: "Make sure this is safe"}},
			wantOutcome: syncOutcomeLineMismatch,
		},
		{
			name:        "no exact, unique approximate match — synced",
			candidates:  []matchableHotspot{belowThreshold, approxMatch},
			wantKey:     "h-approx",
			wantOutcome: syncOutcomeSynced,
		},
		{
			name:        "no exact, two qualifying approximate matches — ambiguous",
			candidates:  []matchableHotspot{approxMatch, {Key: "h-approx-2", RuleKey: "python:S4823", Component: "a.py", Line: 35, Offset: 17, Message: "Make sure this stays safe", VulnerabilityProbability: "HIGH", Author: "alice"}},
			wantOutcome: syncOutcomeLineMismatch,
		},
		{
			name:        "no candidate clears the threshold — not_found",
			candidates:  []matchableHotspot{belowThreshold},
			wantOutcome: syncOutcomeNotFound,
		},
		{
			name:        "empty-ruleKey candidate still matches exactly (fallback preserved)",
			candidates:  []matchableHotspot{emptyRuleExact},
			wantKey:     "h-empty-rule",
			wantOutcome: syncOutcomeSynced,
		},
		{
			name:        "empty candidate set — not_found",
			candidates:  nil,
			wantOutcome: syncOutcomeNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, outcome := classifyHotspotCandidatesByScore(tc.candidates, source)
			if outcome != tc.wantOutcome {
				t.Errorf("outcome = %v, want %v", outcome, tc.wantOutcome)
			}
			if tc.wantOutcome == syncOutcomeSynced && got.Key != tc.wantKey {
				t.Errorf("pick = %q, want %q", got.Key, tc.wantKey)
			}
		})
	}
}

// #323: ACKNOWLEDGED has no SonarQube Cloud counterpart; the mapper
// must return "" so the caller can skip the change_status API call
// and record the demotion instead. SAFE and FIXED still pass through.
func TestMapHotspotResolution(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"SAFE", "SAFE"},
		{"FIXED", "FIXED"},
		{"safe", "SAFE"},   // case-insensitive
		{"fixed", "FIXED"}, // case-insensitive
		{"ACKNOWLEDGED", ""},
		{"acknowledged", ""},
		{"", ""},
		{"GIBBERISH", ""},
	}
	for _, tc := range tests {
		if got := mapHotspotResolution(tc.in); got != tc.want {
			t.Errorf("mapHotspotResolution(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// #321: syncOneHotspot appends a back-link comment to the original
// SonarQube Server hotspot, using the provided base URL. The link is added
// even when the hotspot has no source comments, and is idempotent.
func TestSyncOneHotspotAddsSourceLink(t *testing.T) {
	var (
		mu       sync.Mutex
		comments []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/hotspots/change_status", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mountHotspotsShowEmpty(mux)
	mux.HandleFunc("POST /api/hotspots/add_comment", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		// api/hotspots/add_comment names the body parameter "comment".
		comments = append(comments, r.FormValue("comment"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newCustomCloudTest(t, mux)

	// A REVIEWED/SAFE hotspot on a feature branch with NO source comments —
	// the link must still be added and carry the branch.
	pair := hotspotPair{
		source: matchableHotspot{Key: "HS-7", Status: "REVIEWED", Resolution: "SAFE", Branch: "feature/x"},
		cloud:  matchableHotspot{Key: "cloud-1"},
	}
	if err := syncOneHotspot(context.Background(), e, pair, "https://sqs.example.com", "my-proj"); err != nil {
		t.Fatalf("syncOneHotspot: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := "Link to [Original hotspot](https://sqs.example.com/security_hotspots?id=my-proj&hotspots=HS-7&branch=feature%2Fx)"
	found := false
	for _, c := range comments {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected source-link comment %q, got %v", want, comments)
	}
}

// #323: when the source hotspot is REVIEWED/ACKNOWLEDGED, syncOneHotspot
// must call /api/hotspots/change_status with status=TO_REVIEW and no
// resolution — this resets a cloud hotspot left in SAFE by a previous
// (buggy) migration AND is a safe no-op for never-touched hotspots
// (TO_REVIEW is also the cloud default). Comments still propagate.
func TestSyncOneHotspotAcknowledgedResetsToToReview(t *testing.T) {
	var (
		mu            sync.Mutex
		changeStatus  string
		changeResol   string
		changeHotspot string
		changeCalls   int
		commentCalls  int
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/hotspots/change_status", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		changeCalls++
		changeHotspot = r.FormValue("hotspot")
		changeStatus = r.FormValue("status")
		changeResol = r.FormValue("resolution")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mountHotspotsShowEmpty(mux)
	mux.HandleFunc("POST /api/hotspots/add_comment", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		commentCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	e := newCustomCloudTest(t, mux)

	pair := hotspotPair{
		source: matchableHotspot{Key: "src-1", Status: "REVIEWED", Resolution: "ACKNOWLEDGED",
			Comments: []hotspotComment{{Login: "alice", Markdown: "needs review"}}},
		cloud: matchableHotspot{Key: "cloud-1"},
	}
	if err := syncOneHotspot(context.Background(), e, pair, "", ""); err != nil {
		t.Fatalf("syncOneHotspot: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if changeCalls != 1 {
		t.Fatalf("expected 1 change_status call (status=TO_REVIEW reset), got %d", changeCalls)
	}
	if changeHotspot != "cloud-1" {
		t.Errorf("hotspot = %q, want \"cloud-1\"", changeHotspot)
	}
	if changeStatus != "TO_REVIEW" {
		t.Errorf("status = %q, want \"TO_REVIEW\" (ACKNOWLEDGED has no SQC equivalent)", changeStatus)
	}
	if changeResol != "" {
		t.Errorf("resolution must be empty for TO_REVIEW reset, got %q", changeResol)
	}
	if commentCalls != 1 {
		t.Errorf("expected 1 add_comment call (comment sync should still run), got %d", commentCalls)
	}
}

// #323: re-running migration after a prior (buggy) run that left a
// hotspot in REVIEWED/SAFE must still find that cloud hotspot. SQC's
// /api/hotspots/search defaults to status=TO_REVIEW when no status is
// supplied, so without explicit per-status queries an already-REVIEWED
// hotspot is invisible and the SAFE state survives reset. This test
// pins that findCloudHotspotCandidates calls the endpoint once per
// status and merges the results.
func TestFindCloudHotspotCandidatesQueriesBothStatuses(t *testing.T) {
	var (
		mu         sync.Mutex
		seenStatus []string
		seenBranch []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hotspots/search", func(w http.ResponseWriter, r *http.Request) {
		status := r.URL.Query().Get("status")
		mu.Lock()
		seenStatus = append(seenStatus, status)
		seenBranch = append(seenBranch, r.URL.Query().Get("branch"))
		mu.Unlock()
		switch status {
		case "TO_REVIEW":
			json.NewEncoder(w).Encode(map[string]any{
				"hotspots": []map[string]any{
					{"key": "to-rev-1", "ruleKey": "rk1", "line": 10, "status": "TO_REVIEW"},
				},
				"paging": map[string]any{"pageIndex": 1, "pageSize": 100, "total": 1},
			})
		case "REVIEWED":
			// This is the previously-migrated SAFE hotspot that would
			// otherwise be invisible.
			json.NewEncoder(w).Encode(map[string]any{
				"hotspots": []map[string]any{
					{"key": "rev-safe-1", "ruleKey": "rk1", "line": 20, "status": "REVIEWED", "resolution": "SAFE"},
				},
				"paging": map[string]any{"pageIndex": 1, "pageSize": 100, "total": 1},
			})
		default:
			t.Errorf("unexpected status param: %q", status)
		}
	})
	e := newCustomCloudTest(t, mux)

	got, err := findCloudHotspotCandidates(context.Background(), e, "cloud-proj", "cloud-org", "src/app.go", "develop")
	if err != nil {
		t.Fatalf("findCloudHotspotCandidates: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenStatus) != 2 || seenStatus[0] != "TO_REVIEW" || seenStatus[1] != "REVIEWED" {
		t.Errorf("expected status queries [TO_REVIEW, REVIEWED], got %v", seenStatus)
	}
	// The branch must be forwarded to the cloud search so non-main-branch
	// hotspots are matched (otherwise the search resolves to main only).
	for _, b := range seenBranch {
		if b != "develop" {
			t.Errorf("expected branch=develop on every hotspot search, got %q in %v", b, seenBranch)
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates (one per status), got %d: %+v", len(got), got)
	}
	keys := map[string]bool{}
	for _, h := range got {
		keys[h.Key] = true
	}
	if !keys["to-rev-1"] || !keys["rev-safe-1"] {
		t.Errorf("expected candidates to include both to-rev-1 and rev-safe-1, got %v", keys)
	}
}

// #323: dedup by hotspot key — if the same key appears in both
// status responses (defensive: SQC could conceivably return overlap),
// the merged candidate list must list it once.
func TestFindCloudHotspotCandidatesDedupsByKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hotspots/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"hotspots": []map[string]any{
				{"key": "dup-1", "ruleKey": "rk1", "line": 5},
			},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 100, "total": 1},
		})
	})
	e := newCustomCloudTest(t, mux)

	got, err := findCloudHotspotCandidates(context.Background(), e, "cloud-proj", "cloud-org", "src/app.go", "")
	if err != nil {
		t.Fatalf("findCloudHotspotCandidates: %v", err)
	}
	if len(got) != 1 || got[0].Key != "dup-1" {
		t.Errorf("expected single deduplicated candidate dup-1, got %+v", got)
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

// #323: SAFE / FIXED resolutions still trigger change_status with the
// mapped resolution — guards against the new branching code accidentally
// short-circuiting the happy path.
func TestSyncOneHotspotSafeCallsChangeStatus(t *testing.T) {
	var (
		mu          sync.Mutex
		status      string
		resolution  string
		hotspotKey  string
		changeCalls int
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/hotspots/change_status", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		changeCalls++
		hotspotKey = r.FormValue("hotspot")
		status = r.FormValue("status")
		resolution = r.FormValue("resolution")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mountHotspotsShowEmpty(mux)
	e := newCustomCloudTest(t, mux)

	pair := hotspotPair{
		source: matchableHotspot{Key: "src-1", Status: "REVIEWED", Resolution: "SAFE"},
		cloud:  matchableHotspot{Key: "cloud-1"},
	}
	if err := syncOneHotspot(context.Background(), e, pair, "", ""); err != nil {
		t.Fatalf("syncOneHotspot: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if changeCalls != 1 {
		t.Fatalf("expected 1 change_status call, got %d", changeCalls)
	}
	if hotspotKey != "cloud-1" {
		t.Errorf("hotspot = %q, want \"cloud-1\"", hotspotKey)
	}
	if status != "REVIEWED" {
		t.Errorf("status = %q, want \"REVIEWED\"", status)
	}
	if resolution != "SAFE" {
		t.Errorf("resolution = %q, want \"SAFE\"", resolution)
	}
}
