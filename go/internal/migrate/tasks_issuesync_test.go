// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"testing"
)

func TestIsAlreadyMigratedIssueComment(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		cloudComments []issueComment
		want         bool
	}{
		{
			name: "no cloud comments",
			body: "some comment text",
			cloudComments: nil,
			want: false,
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
				"java:S2095":  setOf("cwe", "denial-of-service", "security"),
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
			name: "all tags are rule defaults — returns empty",
			serverURL: "https://server1", ruleKey: "java:S1481",
			allTags: []string{"unused", "convention"},
			want:    []string{},
		},
		{
			name: "rule defaults stripped, user tags retained",
			serverURL: "https://server1", ruleKey: "java:S2095",
			allTags: []string{"cwe", "denial-of-service", "security", "flagged-by-team", "needs-investigation"},
			want:    []string{"flagged-by-team", "needs-investigation"},
		},
		{
			name: "rule not indexed — fallback returns input unchanged",
			serverURL: "https://server1", ruleKey: "kotlin:S100",
			allTags: []string{"convention"},
			want:    []string{"convention"},
		},
		{
			name: "server not indexed — fallback returns input unchanged",
			serverURL: "https://other-server", ruleKey: "java:S1481",
			allTags: []string{"unused"},
			want:    []string{"unused"},
		},
		{
			name: "empty input — returns empty",
			serverURL: "https://server1", ruleKey: "java:S1481",
			allTags: nil,
			want:    nil,
		},
		{
			name: "user tags only (no rule defaults present)",
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
