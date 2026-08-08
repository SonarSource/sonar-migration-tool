// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

func TestIsAlreadyMigratedIssueComment(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		cloudComments []issueComment
		want          bool
	}{
		{
			name:          "no cloud comments",
			body:          "some comment text",
			cloudComments: nil,
			want:          false,
		},
		{
			name: "cloud comment contains prefix and body",
			body: "some comment text",
			cloudComments: []issueComment{
				{Markdown: "[Migrated from admin on 2024-01-01]\n\nsome comment text"},
			},
			want: true,
		},
		{
			name: "cloud comment has prefix but different body",
			body: "some comment text",
			cloudComments: []issueComment{
				{Markdown: "[Migrated from admin on 2024-01-01]\n\ndifferent comment"},
			},
			want: false,
		},
		{
			name: "cloud comment contains body but no prefix",
			body: "some comment text",
			cloudComments: []issueComment{
				{Markdown: "some comment text"},
			},
			want: false,
		},
		{
			name: "matches on HTMLText when Markdown is empty",
			body: "html comment",
			cloudComments: []issueComment{
				{HTMLText: "[Migrated from bob]\n\nhtml comment"},
			},
			want: true,
		},
		{
			name: "prefers Markdown over HTMLText",
			body: "real body",
			cloudComments: []issueComment{
				{Markdown: "[Migrated from bob]\n\nreal body", HTMLText: "ignored"},
			},
			want: true,
		},
		{
			name: "no match among multiple cloud comments",
			body: "target body",
			cloudComments: []issueComment{
				{Markdown: "[Migrated from alice]\n\nother body"},
				{Markdown: "plain comment without prefix"},
			},
			want: false,
		},
		{
			name: "match found in second of multiple cloud comments",
			body: "target body",
			cloudComments: []issueComment{
				{Markdown: "[Migrated from alice]\n\nother body"},
				{Markdown: "[Migrated from bob]\n\ntarget body"},
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isAlreadyMigratedIssueComment(tc.body, tc.cloudComments)
			if got != tc.want {
				t.Errorf("isAlreadyMigratedIssueComment(%q, ...) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// #350: narrowed source-side candidate set. Sync only issues whose
// triage state, tags or comments mark them as touched by a human;
// auto-assigned issues and CONFIRMED-no-extras issues are skipped.
func TestHasManualChanges(t *testing.T) {
	comment := issueComment{Login: "alice", Markdown: "looks fine"}

	tests := []struct {
		name string
		iss  matchableIssue
		want bool
	}{
		// Triage state — primary triggers.
		{name: "status ACCEPTED", iss: matchableIssue{Status: "ACCEPTED"}, want: true},
		{name: "status accepted lowercase", iss: matchableIssue{Status: "accepted"}, want: true},
		// Modern unified status enum — issueStatus folded into the
		// `status` field on post-10.4 servers.
		{name: "status FALSE_POSITIVE (modern)", iss: matchableIssue{Status: "FALSE_POSITIVE"}, want: true},
		{name: "status false_positive lowercase", iss: matchableIssue{Status: "false_positive"}, want: true},
		// Legacy resolution surface (pre-10.4 servers).
		{name: "resolution FALSE-POSITIVE (legacy)", iss: matchableIssue{Status: "RESOLVED", Resolution: "FALSE-POSITIVE"}, want: true},
		{name: "resolution false-positive lowercase", iss: matchableIssue{Status: "RESOLVED", Resolution: "false-positive"}, want: true},
		{name: "resolution WONTFIX (legacy)", iss: matchableIssue{Status: "RESOLVED", Resolution: "WONTFIX"}, want: true},
		// manualSeverity trigger.
		{name: "manualSeverity only", iss: matchableIssue{Status: "OPEN", ManualSeverity: true}, want: true},
		// User tags trigger (matchableIssue.Tags is already
		// user-only — rule defaults are subtracted at load time).
		{name: "user tags only", iss: matchableIssue{Status: "OPEN", Tags: []string{"flagged-by-team"}}, want: true},
		// Comments trigger.
		{name: "comments only", iss: matchableIssue{Status: "OPEN", Comments: []issueComment{comment}}, want: true},
		// Anti-triggers per #350 spec.
		{name: "OPEN, no triage signals — skip", iss: matchableIssue{Status: "OPEN"}, want: false},
		{name: "CONFIRMED with nothing else — skip (excluded per spec)", iss: matchableIssue{Status: "CONFIRMED"}, want: false},
		{name: "assignee only — skip (dropped per spec)", iss: matchableIssue{Status: "OPEN", Assignee: "bob"}, want: false},
		{name: "RESOLVED+FIXED with no other signal — skip", iss: matchableIssue{Status: "RESOLVED", Resolution: "FIXED"}, want: false},
		// Combinations — any one signal flips it.
		{name: "CONFIRMED + comment — sync", iss: matchableIssue{Status: "CONFIRMED", Comments: []issueComment{comment}}, want: true},
		{name: "assignee + tags — sync (via tags)", iss: matchableIssue{Status: "OPEN", Assignee: "bob", Tags: []string{"flagged"}}, want: true},
		{name: "manualSeverity + OPEN no other — sync", iss: matchableIssue{Status: "OPEN", ManualSeverity: true}, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hasManualChanges(tc.iss)
			if got != tc.want {
				t.Errorf("hasManualChanges(%+v) = %v, want %v", tc.iss, got, tc.want)
			}
		})
	}
}

// #352-followup: issue.tags from /api/issues/search is the union of
// the rule's tags+sysTags and any user-added tags. The "rule defaults"
// alone trigger `len(tags) > 0` on essentially every issue, so we
// subtract them at load time.
func TestRuleTagDefaultsUserTagsOnly(t *testing.T) {
	r := &ruleTagDefaults{
		bySrv: map[string]map[string]map[string]struct{}{
			"https://server1": {
				"java:S1481": setOf("unused", "convention"),
				"java:S2095": setOf("cwe", "denial-of-service", "security"),
			},
		},
	}

	tests := []struct {
		name      string
		serverURL string
		ruleKey   string
		allTags   []string
		want      []string
	}{
		{
			name:      "all tags are rule defaults — returns empty",
			serverURL: "https://server1", ruleKey: "java:S1481",
			allTags: []string{"unused", "convention"},
			want:    []string{},
		},
		{
			name:      "rule defaults stripped, user tags retained",
			serverURL: "https://server1", ruleKey: "java:S2095",
			allTags: []string{"cwe", "denial-of-service", "security", "flagged-by-team", "needs-investigation"},
			want:    []string{"flagged-by-team", "needs-investigation"},
		},
		{
			name:      "rule not indexed — fallback returns input unchanged",
			serverURL: "https://server1", ruleKey: "kotlin:S100",
			allTags: []string{"convention"},
			want:    []string{"convention"},
		},
		{
			name:      "server not indexed — fallback returns input unchanged",
			serverURL: "https://other-server", ruleKey: "java:S1481",
			allTags: []string{"unused"},
			want:    []string{"unused"},
		},
		{
			name:      "empty input — returns empty",
			serverURL: "https://server1", ruleKey: "java:S1481",
			allTags: nil,
			want:    nil,
		},
		{
			name:      "user tags only (no rule defaults present)",
			serverURL: "https://server1", ruleKey: "java:S1481",
			allTags: []string{"team-quarantine"},
			want:    []string{"team-quarantine"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := r.UserTagsOnly(tc.serverURL, tc.ruleKey, tc.allTags)
			if !equalStrings(got, tc.want) {
				t.Errorf("UserTagsOnly(%q, %q, %v) = %v, want %v", tc.serverURL, tc.ruleKey, tc.allTags, got, tc.want)
			}
		})
	}

	// Nil receiver — guards a caller that hasn't loaded the index.
	t.Run("nil receiver — passthrough", func(t *testing.T) {
		var nilR *ruleTagDefaults
		got := nilR.UserTagsOnly("https://server1", "java:S1481", []string{"cwe"})
		if !equalStrings(got, []string{"cwe"}) {
			t.Errorf("nil receiver should pass through, got %v", got)
		}
	})
}

func setOf(tags ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		out[t] = struct{}{}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// #356: stripProjectKeyPrefix isolates the file-path part of a
// SonarQube component identifier. The substitution is essential for
// the targeted cloud search: we send componentKeys=<cloudKey>:<file>
// derived from the source's <sourceKey>:<file> string.
//
// For multi-module (monorepo) projects the component key has TWO
// colon-separated prefixes: "projectKey:moduleKey:filePath". SonarCloud
// has no module layer, so both prefixes must be stripped to obtain the
// bare file path that matches the cloud component key.
func TestStripProjectKeyPrefix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "leading project key", in: "myproject:src/main/java/Foo.java", want: "src/main/java/Foo.java"},
		{name: "monorepo: project + module key both stripped", in: "myproject:mymodule:src/main/java/Foo.java", want: "src/main/java/Foo.java"},
		{name: "monorepo: real-world github-actions example", in: "github-actions-monorepo:github-actions-mono-gradle:src/main/java/com/acme/App.java", want: "src/main/java/com/acme/App.java"},
		{name: "no project prefix", in: "src/main/java/Foo.java", want: "src/main/java/Foo.java"},
		{name: "empty", in: "", want: ""},
		{name: "colon only", in: "myproject:", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stripProjectKeyPrefix(tc.in)
			if got != tc.want {
				t.Errorf("stripProjectKeyPrefix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Per-reason breakdown surfaced at the start of each per-project
// sync. An issue can trip multiple signals (e.g. ACCEPTED + tags); it
// must be counted once per signal so operators can see what's driving
// the migration cost. Branch count is a separate signal taken from
// distinct branch names on the source issues.
func TestClassifyActionableReasonsAndBranches(t *testing.T) {
	comment := issueComment{Login: "u", Markdown: "noted"}
	issues := []matchableIssue{
		{Status: "ACCEPTED", Branch: "main"},                                                      // accepted
		{Status: "RESOLVED", Resolution: "FALSE-POSITIVE", Branch: "main"},                        // accepted_or_fp (via resolution)
		{Status: "FALSE_POSITIVE", Branch: "develop"},                                             // accepted_or_fp (modern enum)
		{Status: "OPEN", Tags: []string{"flagged"}, Branch: "develop"},                            // custom_tags
		{Status: "OPEN", ManualSeverity: true, Branch: "feat-1"},                                  // manual_severity
		{Status: "OPEN", Comments: []issueComment{comment}, Branch: "feat-2"},                     // comments
		{Status: "ACCEPTED", Tags: []string{"audited"}, Branch: "main"},                           // accepted + tags (counted in both)
		{Status: "ACCEPTED", Comments: []issueComment{comment}, ManualSeverity: true, Branch: ""}, // accepted + manualSeverity + comments
	}
	b := classifyActionableReasons(issues)
	if want := 5; b.acceptedOrFP != want {
		t.Errorf("acceptedOrFP: want %d, got %d", want, b.acceptedOrFP)
	}
	if want := 2; b.customTags != want {
		t.Errorf("customTags: want %d, got %d", want, b.customTags)
	}
	if want := 2; b.manualSeverity != want {
		t.Errorf("manualSeverity: want %d, got %d", want, b.manualSeverity)
	}
	if want := 2; b.comments != want {
		t.Errorf("comments: want %d, got %d", want, b.comments)
	}
	// Distinct non-empty branches: main, develop, feat-1, feat-2 = 4.
	if want := 4; countDistinctBranches(issues) != want {
		t.Errorf("countDistinctBranches: want %d, got %d", want, countDistinctBranches(issues))
	}
}

// #412: issueMatchScore — rule mismatch is a hard reject; otherwise the
// score accumulates across message/file/line/type/severity/author.
func TestIssueMatchScore(t *testing.T) {
	base := matchableIssue{
		Rule: "java:S1", Component: "a.java", Line: 10,
		Message: "Fix this issue please", Type: "CODE_SMELL", Severity: "MAJOR", Author: "alice",
	}

	tests := []struct {
		name      string
		candidate matchableIssue
		want      int
	}{
		{"rule mismatch rejects", withRule(base, "java:S2"), -1},
		{"identical — max score", base, 7},
		{
			"similar message (edit distance <=5), all else equal",
			withMessage(base, "Fix this issue pls"),
			6,
		},
		{
			"very different message, all else equal",
			withMessage(base, "Completely unrelated wording here"),
			5,
		},
		{
			"only rule+line equal, everything else different",
			matchableIssue{Rule: "java:S1", Component: "b.java", Line: 10, Message: "zzz", Type: "BUG", Severity: "MINOR", Author: "bob"},
			1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := issueMatchScore(base, tc.candidate); got != tc.want {
				t.Errorf("issueMatchScore = %d, want %d", got, tc.want)
			}
		})
	}
}

func withRule(m matchableIssue, rule string) matchableIssue   { m.Rule = rule; return m }
func withMessage(m matchableIssue, msg string) matchableIssue { m.Message = msg; return m }

// #356: case a/b/c classification — exactly the semantics from the
// issue. 1 cloud counterpart on the source line → synced (a); 0
// counterparts on the line → not_found (c); 2+ on the same line →
// line_mismatch (b). Off-line candidates are ignored.
//
// classifyIssueCandidatesByLine itself is only used by hotspot-sync now
// (see its doc comment in tasks_issuesync.go) — resolveAndSyncIssue below
// uses the approximate scorer instead — but the line-based semantics still
// need direct coverage.
func TestClassifyIssueCandidatesByLine(t *testing.T) {
	cand := func(key string, line int) matchableIssue {
		return matchableIssue{Key: key, Line: line}
	}
	tests := []struct {
		name        string
		candidates  []matchableIssue
		sourceLine  int
		wantKey     string
		wantOutcome syncOutcome
	}{
		{
			name:        "exactly one match on line — synced (a)",
			candidates:  []matchableIssue{cand("cloud-1", 42)},
			sourceLine:  42,
			wantKey:     "cloud-1",
			wantOutcome: syncOutcomeSynced,
		},
		{
			name:        "one match among off-line candidates — synced (a)",
			candidates:  []matchableIssue{cand("cloud-a", 40), cand("cloud-b", 42), cand("cloud-c", 44)},
			sourceLine:  42,
			wantKey:     "cloud-b",
			wantOutcome: syncOutcomeSynced,
		},
		{
			name:        "two matches on same line — line_mismatch (b)",
			candidates:  []matchableIssue{cand("cloud-a", 42), cand("cloud-b", 42)},
			sourceLine:  42,
			wantOutcome: syncOutcomeLineMismatch,
		},
		{
			name:        "three matches on same line, one off-line — line_mismatch (b)",
			candidates:  []matchableIssue{cand("cloud-a", 42), cand("cloud-b", 42), cand("cloud-c", 42), cand("cloud-d", 99)},
			sourceLine:  42,
			wantOutcome: syncOutcomeLineMismatch,
		},
		{
			name:        "no matches on line, candidates elsewhere — not_found (c)",
			candidates:  []matchableIssue{cand("cloud-a", 40), cand("cloud-b", 44)},
			sourceLine:  42,
			wantOutcome: syncOutcomeNotFound,
		},
		{
			name:        "empty candidates — not_found (c)",
			candidates:  nil,
			sourceLine:  42,
			wantOutcome: syncOutcomeNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, outcome := classifyIssueCandidatesByLine(tc.candidates, tc.sourceLine)
			if outcome != tc.wantOutcome {
				t.Errorf("outcome = %v, want %v", outcome, tc.wantOutcome)
			}
			if tc.wantOutcome == syncOutcomeSynced && got.Key != tc.wantKey {
				t.Errorf("pick = %q, want %q", got.Key, tc.wantKey)
			}
		})
	}
}

// #412: classifyIssueCandidatesByScore — exact match (file+line+message)
// wins when unique; otherwise fall back to the approximate score, which
// must clear issueApproxMatchThreshold and be uniquely qualifying.
func TestClassifyIssueCandidatesByScore(t *testing.T) {
	source := matchableIssue{
		Rule: "java:S1", Component: "a.java", Line: 42,
		Message: "Fix this issue please", Type: "CODE_SMELL", Severity: "MAJOR", Author: "alice",
	}
	exactMatch := source
	exactMatch.Key = "cloud-exact"

	approxMatch := matchableIssue{
		Key: "cloud-approx", Rule: "java:S1", Component: "a.java", Line: 42,
		Message: "Fix this issue pls", Type: "CODE_SMELL", Severity: "MAJOR", Author: "alice",
	} // score 6: message similar(+1) + file+line+type+severity+author(+5)

	belowThreshold := matchableIssue{
		Key: "cloud-low", Rule: "java:S1", Component: "b.java", Line: 99,
		Message: "totally different text", Type: "BUG", Severity: "MINOR", Author: "bob",
	}

	tests := []struct {
		name        string
		candidates  []matchableIssue
		wantKey     string
		wantOutcome syncOutcome
	}{
		{
			name:        "unique exact match — synced",
			candidates:  []matchableIssue{belowThreshold, exactMatch},
			wantKey:     "cloud-exact",
			wantOutcome: syncOutcomeSynced,
		},
		{
			name:        "two exact matches — ambiguous",
			candidates:  []matchableIssue{exactMatch, {Key: "cloud-exact-2", Rule: "java:S1", Component: "a.java", Line: 42, Message: "Fix this issue please"}},
			wantOutcome: syncOutcomeLineMismatch,
		},
		{
			name:        "no exact, unique approximate match — synced",
			candidates:  []matchableIssue{belowThreshold, approxMatch},
			wantKey:     "cloud-approx",
			wantOutcome: syncOutcomeSynced,
		},
		{
			name:        "no exact, two qualifying approximate matches — ambiguous",
			candidates:  []matchableIssue{approxMatch, {Key: "cloud-approx-2", Rule: "java:S1", Component: "a.java", Line: 42, Message: "Fix this issue pls", Type: "CODE_SMELL", Severity: "MAJOR", Author: "alice"}},
			wantOutcome: syncOutcomeLineMismatch,
		},
		{
			name:        "no candidate clears the threshold — not_found",
			candidates:  []matchableIssue{belowThreshold},
			wantOutcome: syncOutcomeNotFound,
		},
		{
			name:        "empty candidates — not_found",
			candidates:  nil,
			wantOutcome: syncOutcomeNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, outcome := classifyIssueCandidatesByScore(tc.candidates, source)
			if outcome != tc.wantOutcome {
				t.Errorf("outcome = %v, want %v", outcome, tc.wantOutcome)
			}
			if tc.wantOutcome == syncOutcomeSynced && got.Key != tc.wantKey {
				t.Errorf("pick = %q, want %q", got.Key, tc.wantKey)
			}
		})
	}
}

// #322: getFallbackTransition must read the modern issueStatus enum
// FIRST. A real ACCEPTED issue from a 10.4+ server arrives as
// status=RESOLVED, resolution=WONTFIX, issueStatus=ACCEPTED — the old
// resolution-first logic caught the WONTFIX resolution and mapped it to
// the "wontfix" transition, landing the issue as Won't Fix on Cloud
// instead of Accepted. It must now map to the "accept" transition, while
// a GENUINE legacy WONTFIX (no issueStatus, pre-10.4) still maps to
// "wontfix" so those migrations are not regressed.
func TestGetFallbackTransition(t *testing.T) {
	tests := []struct {
		name        string
		issueStatus string
		resolution  string
		status      string
		want        string
	}{
		// --- Modern issueStatus enum (10.4+) takes priority ---
		{name: "#322 regression: ACCEPTED arrives as RESOLVED+WONTFIX -> accept", issueStatus: "ACCEPTED", resolution: "WONTFIX", status: "RESOLVED", want: "accept"},
		{name: "modern FALSE_POSITIVE -> falsepositive", issueStatus: "FALSE_POSITIVE", resolution: "FALSE-POSITIVE", status: "RESOLVED", want: "falsepositive"},
		{name: "modern CONFIRMED -> confirm", issueStatus: "CONFIRMED", status: "CONFIRMED", want: "confirm"},
		{name: "modern OPEN -> no transition", issueStatus: "OPEN", status: "OPEN", want: ""},
		{name: "modern FIXED -> no transition (excluded at load)", issueStatus: "FIXED", resolution: "FIXED", status: "RESOLVED", want: ""},
		{name: "issueStatus is case-insensitive", issueStatus: "accepted", resolution: "WONTFIX", status: "RESOLVED", want: "accept"},

		// --- Legacy resolution path (pre-10.4, no issueStatus) ---
		{name: "legacy WONTFIX (no issueStatus) -> wontfix, NOT regressed", issueStatus: "", resolution: "WONTFIX", status: "RESOLVED", want: "wontfix"},
		{name: "legacy FALSE-POSITIVE (no issueStatus) -> falsepositive", issueStatus: "", resolution: "FALSE-POSITIVE", status: "RESOLVED", want: "falsepositive"},

		// --- Legacy status fallback (no issueStatus, no resolution) ---
		{name: "legacy CONFIRMED status -> confirm", status: "CONFIRMED", want: "confirm"},
		{name: "legacy REOPENED status -> reopen", status: "REOPENED", want: "reopen"},
		{name: "legacy OPEN status -> no transition", status: "OPEN", want: ""},
		{name: "legacy RESOLVED status (no resolution) -> resolve", status: "RESOLVED", want: "resolve"},
		{name: "legacy ACCEPTED status (no issueStatus) -> accept", status: "ACCEPTED", want: "accept"},
		{name: "legacy FALSE_POSITIVE status -> falsepositive", status: "FALSE_POSITIVE", want: "falsepositive"},
		{name: "IN_SANDBOX -> no transition", status: "IN_SANDBOX", want: ""},
		{name: "all empty -> no transition", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getFallbackTransition(tc.issueStatus, tc.resolution, tc.status)
			if got != tc.want {
				t.Errorf("getFallbackTransition(%q, %q, %q) = %q, want %q", tc.issueStatus, tc.resolution, tc.status, got, tc.want)
			}
		})
	}
}

// #322: resolveTransition gates the "accept" transition on the matched
// Cloud issue's available-transition list. When "accept" is not offered
// it must leave the issue OPEN (return "") and report downgraded=true —
// it must NEVER silently downgrade to "wontfix" (that would mislabel the
// issue as Won't Fix). An empty/unknown list is treated optimistically.
// Transitions other than "accept" are not gated.
func TestResolveTransition(t *testing.T) {
	accepted := matchableIssue{IssueStatus: "ACCEPTED", Resolution: "WONTFIX", Status: "RESOLVED"}
	falsePos := matchableIssue{IssueStatus: "FALSE_POSITIVE", Resolution: "FALSE-POSITIVE", Status: "RESOLVED"}
	legacyWontfix := matchableIssue{Resolution: "WONTFIX", Status: "RESOLVED"}
	open := matchableIssue{IssueStatus: "OPEN", Status: "OPEN"}

	tests := []struct {
		name           string
		src            matchableIssue
		cloudTrans     []string
		wantTransition string
		wantDowngraded bool
	}{
		{name: "accept available -> accept", src: accepted, cloudTrans: []string{"confirm", "accept", "falsepositive"}, wantTransition: "accept", wantDowngraded: false},
		{name: "accept unavailable -> OPEN + downgraded (never wontfix)", src: accepted, cloudTrans: []string{"reopen", "unconfirm"}, wantTransition: "", wantDowngraded: true},
		{name: "unknown transitions (nil) -> optimistic accept", src: accepted, cloudTrans: nil, wantTransition: "accept", wantDowngraded: false},
		{name: "unknown transitions (empty slice) -> optimistic accept", src: accepted, cloudTrans: []string{}, wantTransition: "accept", wantDowngraded: false},
		{name: "false positive is not gated", src: falsePos, cloudTrans: []string{"confirm"}, wantTransition: "falsepositive", wantDowngraded: false},
		{name: "legacy wontfix is not gated", src: legacyWontfix, cloudTrans: []string{"reopen"}, wantTransition: "wontfix", wantDowngraded: false},
		{name: "open needs no transition", src: open, cloudTrans: []string{"accept"}, wantTransition: "", wantDowngraded: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotT, gotD := resolveTransition(tc.src, tc.cloudTrans)
			if gotT != tc.wantTransition || gotD != tc.wantDowngraded {
				t.Errorf("resolveTransition(%+v, %v) = (%q, %v), want (%q, %v)", tc.src, tc.cloudTrans, gotT, gotD, tc.wantTransition, tc.wantDowngraded)
			}
		})
	}
}

// #322: hasManualChanges must recognise the modern issueStatus enum so
// accepted / false-positive issues on 10.4+ servers (whose legacy status
// may be OPEN/RESOLVED with the triage state living only in issueStatus)
// are still flagged actionable — otherwise the transition fix never
// fires for them.
func TestHasManualChangesIssueStatus(t *testing.T) {
	tests := []struct {
		name string
		iss  matchableIssue
		want bool
	}{
		{name: "issueStatus ACCEPTED, legacy status OPEN -> actionable", iss: matchableIssue{IssueStatus: "ACCEPTED", Status: "OPEN"}, want: true},
		{name: "issueStatus FALSE_POSITIVE, legacy status OPEN -> actionable", iss: matchableIssue{IssueStatus: "FALSE_POSITIVE", Status: "OPEN"}, want: true},
		{name: "real-data shape: RESOLVED+WONTFIX+issueStatus ACCEPTED -> actionable", iss: matchableIssue{IssueStatus: "ACCEPTED", Resolution: "WONTFIX", Status: "RESOLVED"}, want: true},
		{name: "issueStatus OPEN, no other signal -> skip", iss: matchableIssue{IssueStatus: "OPEN", Status: "OPEN"}, want: false},
		{name: "issueStatus lowercase accepted -> actionable", iss: matchableIssue{IssueStatus: "accepted", Status: "OPEN"}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasManualChanges(tc.iss); got != tc.want {
				t.Errorf("hasManualChanges(%+v) = %v, want %v", tc.iss, got, tc.want)
			}
		})
	}
}

// #322: classifyActionableReasons counts issueStatus-driven accepted/FP
// even when the legacy status/resolution fields don't carry the state.
func TestClassifyActionableReasonsIssueStatus(t *testing.T) {
	issues := []matchableIssue{
		{IssueStatus: "ACCEPTED", Status: "OPEN"},                  // accepted via issueStatus only
		{IssueStatus: "FALSE_POSITIVE", Status: "OPEN"},            // fp via issueStatus only
		{IssueStatus: "OPEN", Status: "OPEN", Tags: []string{"x"}}, // tags only, not acceptedOrFP
	}
	b := classifyActionableReasons(issues)
	if b.acceptedOrFP != 2 {
		t.Errorf("acceptedOrFP via issueStatus: want 2, got %d", b.acceptedOrFP)
	}
	if b.customTags != 1 {
		t.Errorf("customTags: want 1, got %d", b.customTags)
	}
}

// --- #456: custom issue tags must survive the migration ---

// mergeIssueTags backs syncIssueTags. /api/issues/set_tags REPLACES an issue's
// tag list rather than merging into it, so the payload has to be the union of
// the source issue's tags and the tags the Cloud issue already carries.
// Previously only the user-added subset was sent, which stripped the
// rule-default tags (e.g. "cwe") the Cloud analysis had assigned.
func TestMergeIssueTags(t *testing.T) {
	tests := []struct {
		name       string
		sourceTags []string
		cloudTags  []string
		want       []string
	}{
		{
			name:       "source tags unioned with cloud rule-default tags",
			sourceTags: []string{"cwe", "action-plan"},
			cloudTags:  []string{"cwe", "secret"},
			want:       []string{"action-plan", "cwe", metadataSyncTag, "secret"},
		},
		{
			name:       "custom tag from source is added to an untagged cloud issue",
			sourceTags: []string{"action-plan"},
			cloudTags:  nil,
			want:       []string{"action-plan", metadataSyncTag},
		},
		{
			name:       "cloud-only tags are preserved, never clobbered",
			sourceTags: nil,
			cloudTags:  []string{"former-hotspot", "secret"},
			want:       []string{"former-hotspot", metadataSyncTag, "secret"},
		},
		{
			name:       "duplicates across both sides collapse",
			sourceTags: []string{"cwe", "cwe", "action-plan"},
			cloudTags:  []string{"cwe", "action-plan"},
			want:       []string{"action-plan", "cwe", metadataSyncTag},
		},
		{
			name:       "marker already present is not duplicated (idempotent re-run)",
			sourceTags: []string{"action-plan"},
			cloudTags:  []string{"action-plan", metadataSyncTag},
			want:       []string{"action-plan", metadataSyncTag},
		},
		{
			name:       "blank and whitespace-only tags are dropped",
			sourceTags: []string{"", "   ", "action-plan"},
			cloudTags:  []string{"\t"},
			want:       []string{"action-plan", metadataSyncTag},
		},
		{
			name:       "no tags anywhere still writes the marker",
			sourceTags: nil,
			cloudTags:  nil,
			want:       []string{metadataSyncTag},
		},
		{
			name:       "tags are trimmed before comparison",
			sourceTags: []string{" action-plan "},
			cloudTags:  []string{"action-plan"},
			want:       []string{"action-plan", metadataSyncTag},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeIssueTags(tc.sourceTags, tc.cloudTags)
			if !equalStrings(got, tc.want) {
				t.Errorf("mergeIssueTags(%v, %v) = %v, want %v",
					tc.sourceTags, tc.cloudTags, got, tc.want)
			}
		})
	}
}

// tagsToApply picks what actually gets written to the Cloud issue: the source's
// COMPLETE tag list, not the user-added subset that drives actionability.
func TestMatchableIssueTagsToApply(t *testing.T) {
	tests := []struct {
		name string
		iss  matchableIssue
		want []string
	}{
		{
			name: "full source tag list preferred over the user-only subset",
			iss:  matchableIssue{Tags: []string{"action-plan"}, AllTags: []string{"cwe", "action-plan"}},
			want: []string{"cwe", "action-plan"},
		},
		{
			name: "falls back to the user-only subset when AllTags is unset",
			iss:  matchableIssue{Tags: []string{"action-plan"}},
			want: []string{"action-plan"},
		},
		{
			name: "no tags at all",
			iss:  matchableIssue{},
			want: nil,
		},
		{
			name: "rule-default-only tag list still applied",
			iss:  matchableIssue{Tags: nil, AllTags: []string{"cwe"}},
			want: []string{"cwe"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.iss.tagsToApply(); !equalStrings(got, tc.want) {
				t.Errorf("tagsToApply() = %v, want %v", got, tc.want)
			}
		})
	}
}

// An OPEN issue carrying a user-added tag must be actionable on its own: tag
// sync must NOT be gated behind needing a status/resolution transition. This is
// the property the #456 report suspected was broken; it holds, and the real
// fault was upstream (the issues were dropped from the scan report entirely) —
// so lock the property down against regression.
func TestOpenIssueWithCustomTagIsActionableAndNeedsNoTransition(t *testing.T) {
	iss := matchableIssue{
		Key:         "src-1",
		Rule:        "secrets:S7001",
		Component:   "demo-rules:secrets/az.xml",
		Line:        7,
		Status:      "OPEN",
		IssueStatus: "OPEN",
		Tags:        []string{"action-plan"},
		AllTags:     []string{"cwe", "action-plan"},
	}
	if !hasManualChanges(iss) {
		t.Fatal("an OPEN issue with a user-added tag must be actionable")
	}
	if trans, downgraded := resolveTransition(iss, nil); trans != "" || downgraded {
		t.Errorf("OPEN issue needs no transition: got %q downgraded=%v", trans, downgraded)
	}
	if b := classifyActionableReasons([]matchableIssue{iss}); b.customTags != 1 || b.acceptedOrFP != 0 {
		t.Errorf("want customTags=1 acceptedOrFP=0, got customTags=%d acceptedOrFP=%d",
			b.customTags, b.acceptedOrFP)
	}
	// The payload that would be written keeps both the custom and the
	// rule-default tag, plus the idempotency marker.
	want := []string{"action-plan", "cwe", metadataSyncTag}
	if got := mergeIssueTags(iss.tagsToApply(), nil); !equalStrings(got, want) {
		t.Errorf("tag payload = %v, want %v", got, want)
	}
}

// --- #456: coverage for the tag write path and the not_found accounting ---

// newTagSyncServer returns a cloud stub that records every /api/issues/set_tags
// payload, plus the recorder itself.
func newTagSyncServer(t *testing.T, failSetTags bool) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/issues/set_tags", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		seen = append(seen, r.Form.Get("tags"))
		if failSetTags {
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("POST /api/issues/add_comment", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/issues/do_transition", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux), &seen
}

// syncIssueTags sends the union of the source and cloud tag lists. The cloud
// issue's own rule-default tags must survive, because set_tags REPLACES.
func TestSyncIssueTagsSendsUnion(t *testing.T) {
	cloudSrv, seen := newTagSyncServer(t, false)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	failed := syncIssueTags(context.Background(), e, "cloud-1",
		[]string{"cwe", "action-plan"}, []string{"secret", "cwe"})
	if failed {
		t.Fatal("syncIssueTags reported failure on a 200 response")
	}
	if len(*seen) != 1 {
		t.Fatalf("want exactly 1 set_tags call, got %d", len(*seen))
	}
	want := "action-plan,cwe,metadata-synchronized,secret"
	if (*seen)[0] != want {
		t.Errorf("set_tags payload = %q, want %q", (*seen)[0], want)
	}
}

// A non-2xx from set_tags must be reported so the pair counts as failed.
// Note: the shared HTTP client retries 5xx, so several attempts are expected —
// what matters is that the exhausted failure is surfaced, not swallowed.
func TestSyncIssueTagsReportsAPIFailure(t *testing.T) {
	cloudSrv, seen := newTagSyncServer(t, true)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	if !syncIssueTags(context.Background(), e, "cloud-1", []string{"action-plan"}, nil) {
		t.Error("syncIssueTags must report failure when the API rejects the call")
	}
	if len(*seen) == 0 {
		t.Fatal("expected at least one set_tags attempt")
	}
	// Every attempt must carry the same union payload.
	want := "action-plan," + metadataSyncTag
	for i, got := range *seen {
		if got != want {
			t.Errorf("attempt %d payload = %q, want %q", i, got, want)
		}
	}
}

// syncOnePair writes the source issue's COMPLETE tag list (AllTags), not the
// user-added subset that drives actionability.
func TestSyncOnePairWritesFullSourceTagList(t *testing.T) {
	cloudSrv, seen := newTagSyncServer(t, false)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	pair := issuePair{
		source: matchableIssue{
			Key: "src-1", Rule: "secrets:S7001", Status: "OPEN", IssueStatus: "OPEN",
			Tags: []string{"action-plan"}, AllTags: []string{"cwe", "action-plan"},
		},
		cloud: matchableIssue{Key: "cloud-1", Tags: []string{"secret"}},
	}
	counter := NewTaskCounter("test")
	syncOnePair(context.Background(), e, pair, "", "src-proj", counter)

	if len(*seen) != 1 {
		t.Fatalf("want 1 set_tags call, got %d", len(*seen))
	}
	want := "action-plan,cwe,metadata-synchronized,secret"
	if (*seen)[0] != want {
		t.Errorf("set_tags payload = %q, want %q", (*seen)[0], want)
	}
	if got := counter.succeeded.Load(); got != 1 {
		t.Errorf("want 1 success recorded, got %d", got)
	}
}

// A cloud issue already carrying the marker short-circuits: no tag rewrite.
func TestSyncOnePairSkipsAlreadySyncedIssue(t *testing.T) {
	cloudSrv, seen := newTagSyncServer(t, false)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	pair := issuePair{
		source: matchableIssue{Key: "src-1", AllTags: []string{"action-plan"}},
		cloud:  matchableIssue{Key: "cloud-1", Tags: []string{"action-plan", metadataSyncTag}},
	}
	syncOnePair(context.Background(), e, pair, "", "src-proj", NewTaskCounter("test"))

	if len(*seen) != 0 {
		t.Errorf("an already-synced issue must not be re-tagged, got %v", *seen)
	}
}

// setupIssueTagExtract writes a getProjectIssuesFull + getRuleDetails extract
// mirroring the #456 demo-rules shape: OPEN issues whose tags array folds the
// rule's sysTags ("cwe") together with the user-added tag ("action-plan").
func setupIssueTagExtract(t *testing.T, dir string) {
	t.Helper()
	extractDir := filepath.Join(dir, "extract-01")
	writeJSON(filepath.Join(extractDir, "extract.json"),
		map[string]any{"url": testServerURL, "edition": "enterprise"})

	writeJSONL(filepath.Join(extractDir, "getRuleDetails"), []map[string]any{
		{"key": "secrets:S7001", "tags": []string{}, "sysTags": []string{"cwe"}, "serverUrl": testServerURL},
	})

	writeJSONL(filepath.Join(extractDir, "getProjectIssuesFull"), []map[string]any{
		{
			"key": "src-tagged", "rule": "secrets:S7001", "component": "demo-rules:secrets/az.xml",
			"projectKey": "demo-rules", "branch": "main", "status": "OPEN", "issueStatus": "OPEN",
			"tags": []string{"cwe", "action-plan"}, "serverUrl": testServerURL,
			"textRange": map[string]any{"startLine": 7, "endLine": 7},
			"line":      7,
		},
		{
			"key": "src-plain", "rule": "secrets:S7001", "component": "demo-rules:secrets/tmp",
			"projectKey": "demo-rules", "branch": "main", "status": "OPEN", "issueStatus": "OPEN",
			"tags": []string{"cwe"}, "serverUrl": testServerURL,
			"line": 11,
		},
		{
			"key": "src-fixed", "rule": "secrets:S7001", "component": "demo-rules:secrets/tmp",
			"projectKey": "demo-rules", "branch": "main", "status": "RESOLVED", "resolution": "FIXED",
			"tags": []string{"cwe", "action-plan"}, "serverUrl": testServerURL, "line": 12,
		},
	})
}

// loadMatchableIssues must populate BOTH tag fields: AllTags with the complete
// source list (what gets written to Cloud) and Tags with the user-added subset
// (what decides actionability). FIXED issues are excluded.
func TestLoadMatchableIssuesPopulatesAllTagsAndUserTags(t *testing.T) {
	dir := t.TempDir()
	setupIssueTagExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	issues := loadMatchableIssues(e, testServerURL, "demo-rules", loadRuleTagDefaults(e))
	if len(issues) != 2 {
		t.Fatalf("want 2 issues (FIXED excluded), got %d: %+v", len(issues), issues)
	}

	byKey := make(map[string]matchableIssue, len(issues))
	for _, iss := range issues {
		byKey[iss.Key] = iss
	}

	tagged, ok := byKey["src-tagged"]
	if !ok {
		t.Fatalf("src-tagged missing from %v", byKey)
	}
	if !equalStrings(tagged.AllTags, []string{"cwe", "action-plan"}) {
		t.Errorf("AllTags = %v, want the full source list [cwe action-plan]", tagged.AllTags)
	}
	if !equalStrings(tagged.Tags, []string{"action-plan"}) {
		t.Errorf("Tags = %v, want the user-added subset [action-plan]", tagged.Tags)
	}
	if !hasManualChanges(tagged) {
		t.Error("an OPEN issue with a user-added tag must be actionable")
	}
	if !equalStrings(tagged.tagsToApply(), []string{"cwe", "action-plan"}) {
		t.Errorf("tagsToApply = %v, want the full source list", tagged.tagsToApply())
	}

	// The rule-default-only issue carries tags but no *user* tags, so it is not
	// actionable — and must not be, or every issue would be synced.
	plain := byKey["src-plain"]
	if !equalStrings(plain.AllTags, []string{"cwe"}) {
		t.Errorf("AllTags = %v, want [cwe]", plain.AllTags)
	}
	if len(plain.Tags) != 0 {
		t.Errorf("Tags = %v, want empty (cwe is a rule default)", plain.Tags)
	}
	if hasManualChanges(plain) {
		t.Error("a rule-default-only issue must NOT be actionable")
	}
}

// syncProjectIssues end-to-end over the mock cloud: the tagged issue that HAS a
// counterpart is tagged, and the tagged issue that has NONE is accounted as
// not_found rather than silently ignored (#456 — that silence hid the bug).
func TestSyncProjectIssuesTagsMatchedAndCountsNotFound(t *testing.T) {
	dir := t.TempDir()
	setupIssueTagExtract(t, dir)

	var setTagsPayloads []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		// The project-wide indexing probe must report a non-zero total so the
		// sync proceeds without waiting on the backoff.
		if r.URL.Query().Get("rules") == "" {
			json.NewEncoder(w).Encode(map[string]any{
				"issues": []map[string]any{},
				"paging": map[string]any{"pageIndex": 1, "pageSize": 1, "total": 2},
			})
			return
		}
		// Per-source targeted search: only az.xml has a counterpart. The
		// component-scoped search for secrets/tmp returns nothing, which is
		// exactly the "rule missing on the target" shape from #456.
		if strings.Contains(r.URL.Query().Get("componentKeys"), "secrets/az.xml") {
			json.NewEncoder(w).Encode(map[string]any{
				"issues": []map[string]any{{
					"key": "cloud-az", "rule": "secrets:S7001",
					"component": "cloud-proj:secrets/az.xml", "line": 7,
					"tags": []string{"secret", "cwe"},
				}},
				"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 0},
		})
	})
	mux.HandleFunc("POST /api/issues/set_tags", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		setTagsPayloads = append(setTagsPayloads, r.Form.Get("tags"))
	})
	mux.HandleFunc("POST /api/issues/add_comment", func(w http.ResponseWriter, _ *http.Request) {})
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	e := newTestExecutor(cloudSrv, apiSrv, dir)
	var logBuf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Make the second tagged issue actionable-but-unmatchable by giving the
	// extract a user tag on the secrets/tmp issue too.
	writeJSONL(filepath.Join(dir, "extract-01", "getProjectIssuesFull"), []map[string]any{
		{
			"key": "src-tagged", "rule": "secrets:S7001", "component": "demo-rules:secrets/az.xml",
			"projectKey": "demo-rules", "branch": "main", "status": "OPEN", "issueStatus": "OPEN",
			"tags": []string{"cwe", "action-plan"}, "serverUrl": testServerURL, "line": 7,
		},
		{
			"key": "src-orphan", "rule": "secrets:S7001", "component": "demo-rules:secrets/tmp",
			"projectKey": "demo-rules", "branch": "main", "status": "OPEN", "issueStatus": "OPEN",
			"tags": []string{"cwe", "action-plan"}, "serverUrl": testServerURL, "line": 11,
		},
	})

	stats := syncProjectIssues(context.Background(), e, "cloud-proj", "cloud-org",
		testServerURL, "demo-rules", NewTaskCounter("test"), loadRuleTagDefaults(e))

	if stats.Actionable != 2 {
		t.Errorf("actionable: want 2, got %d", stats.Actionable)
	}
	if stats.A != 1 {
		t.Errorf("synced: want 1, got %d", stats.A)
	}
	if stats.C != 1 {
		t.Errorf("not_found: want 1, got %d", stats.C)
	}
	if stats.B != 0 {
		t.Errorf("line_mismatch: want 0, got %d", stats.B)
	}

	// The matched issue keeps the cloud's rule-default tags AND gains the
	// source's custom tag.
	if len(setTagsPayloads) != 1 {
		t.Fatalf("want 1 set_tags call, got %d: %v", len(setTagsPayloads), setTagsPayloads)
	}
	want := "action-plan,cwe,metadata-synchronized,secret"
	if setTagsPayloads[0] != want {
		t.Errorf("set_tags payload = %q, want %q", setTagsPayloads[0], want)
	}

	// The unmatched triaged issue is surfaced at WARN with totals.
	out := logBuf.String()
	if !strings.Contains(out, "without a unique Cloud counterpart") {
		t.Errorf("expected the not_found WARN summary; got %q", out)
	}
	if !strings.Contains(out, "not_found=1") {
		t.Errorf("expected not_found=1 in the WARN summary; got %q", out)
	}
}

// When every actionable issue matches, the not_found WARN must NOT fire.
func TestSyncProjectIssuesNoWarnWhenAllMatched(t *testing.T) {
	dir := t.TempDir()
	setupIssueTagExtract(t, dir)
	writeJSONL(filepath.Join(dir, "extract-01", "getProjectIssuesFull"), []map[string]any{
		{
			"key": "src-tagged", "rule": "secrets:S7001", "component": "demo-rules:secrets/az.xml",
			"projectKey": "demo-rules", "branch": "main", "status": "OPEN", "issueStatus": "OPEN",
			"tags": []string{"cwe", "action-plan"}, "serverUrl": testServerURL, "line": 7,
		},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("rules") == "" {
			json.NewEncoder(w).Encode(map[string]any{
				"issues": []map[string]any{},
				"paging": map[string]any{"pageIndex": 1, "pageSize": 1, "total": 1},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{{
				"key": "cloud-az", "rule": "secrets:S7001",
				"component": "cloud-proj:secrets/az.xml", "line": 7,
			}},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
		})
	})
	mux.HandleFunc("POST /api/issues/set_tags", func(w http.ResponseWriter, _ *http.Request) {})
	mux.HandleFunc("POST /api/issues/add_comment", func(w http.ResponseWriter, _ *http.Request) {})
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	e := newTestExecutor(cloudSrv, apiSrv, dir)
	var logBuf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	stats := syncProjectIssues(context.Background(), e, "cloud-proj", "cloud-org",
		testServerURL, "demo-rules", NewTaskCounter("test"), loadRuleTagDefaults(e))

	if stats.A != 1 || stats.B != 0 || stats.C != 0 {
		t.Errorf("want synced=1 ambiguous=0 not_found=0, got a=%d b=%d c=%d", stats.A, stats.B, stats.C)
	}
	if strings.Contains(logBuf.String(), "without a unique Cloud counterpart") {
		t.Errorf("the not_found WARN must not fire when everything matched; got %q", logBuf.String())
	}
}

// No source issues at all: the sync returns zeroed stats without touching the
// network (guards the early return).
func TestSyncProjectIssuesNoSourceIssues(t *testing.T) {
	dir := t.TempDir()
	writeJSON(filepath.Join(dir, "extract-01", "extract.json"),
		map[string]any{"url": testServerURL, "edition": "enterprise"})
	writeJSONL(filepath.Join(dir, "extract-01", "getProjectIssuesFull"), nil)

	cloudSrv, _ := newTagSyncServer(t, false)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	stats := syncProjectIssues(context.Background(), e, "cloud-proj", "cloud-org",
		testServerURL, "demo-rules", NewTaskCounter("test"), loadRuleTagDefaults(e))
	if stats.Actionable != 0 || stats.A != 0 || stats.C != 0 {
		t.Errorf("want fully zeroed stats, got %+v", stats)
	}
}

// Source issues exist but none is actionable: no cloud calls, zero stats
// (guards the "no actionable" early return distinct from "no issues").
func TestSyncProjectIssuesNoActionableIssues(t *testing.T) {
	dir := t.TempDir()
	setupIssueTagExtract(t, dir)
	writeJSONL(filepath.Join(dir, "extract-01", "getProjectIssuesFull"), []map[string]any{
		{
			"key": "src-plain", "rule": "secrets:S7001", "component": "demo-rules:secrets/tmp",
			"projectKey": "demo-rules", "branch": "main", "status": "OPEN", "issueStatus": "OPEN",
			"tags": []string{"cwe"}, "serverUrl": testServerURL, "line": 11,
		},
	})

	cloudSrv, seen := newTagSyncServer(t, false)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	stats := syncProjectIssues(context.Background(), e, "cloud-proj", "cloud-org",
		testServerURL, "demo-rules", NewTaskCounter("test"), loadRuleTagDefaults(e))
	if stats.Actionable != 0 {
		t.Errorf("a rule-default-only issue must not be actionable, got %d", stats.Actionable)
	}
	if len(*seen) != 0 {
		t.Errorf("no set_tags calls expected, got %v", *seen)
	}
}

// #412: loadMatchableIssues carries message/type/severity/author through
// from the extract into matchableIssue — the approximate-match scorer
// (matchscore.go) needs them.
func TestLoadMatchableIssuesCarriesScorerFields(t *testing.T) {
	dir := t.TempDir()
	extractDir := filepath.Join(dir, "extract-01")
	writeJSONL(filepath.Join(extractDir, "getProjectIssuesFull"), []map[string]any{
		{
			"key": "iss-1", "rule": "java:S100", "component": "proj-a:src/app.go", "line": 10,
			"status": "OPEN", "projectKey": "proj-a",
			"message": "Do not do this", "type": "BUG", "severity": "MAJOR", "author": "dev@example.com",
		},
	})

	e := &Executor{ExportDir: dir, Mapping: structure.ExtractMapping{testServerURL: "extract-01"}}
	got := loadMatchableIssues(e, testServerURL, "proj-a", loadRuleTagDefaults(e))
	if len(got) != 1 {
		t.Fatalf("loadMatchableIssues: got %d issues, want 1", len(got))
	}
	iss := got[0]
	if iss.Message != "Do not do this" || iss.Type != "BUG" || iss.Severity != "MAJOR" || iss.Author != "dev@example.com" {
		t.Errorf("loadMatchableIssues: scorer fields not carried through, got %+v", iss)
	}
}
