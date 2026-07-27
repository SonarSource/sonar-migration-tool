// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
)

// hotspotIssueSyncRecorder captures the issue-API calls the hotspot sync makes.
type hotspotIssueSyncRecorder struct {
	mu          sync.Mutex
	tagCalls    []string // issue keys passed to set_tags
	tagsSet     []string // tags from the last set_tags call
	transitions []string
	comments    []string
	searchQuery []string // raw query strings seen on /api/issues/search
}

func (r *hotspotIssueSyncRecorder) mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/issues/set_tags", func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		r.mu.Lock()
		r.tagCalls = append(r.tagCalls, req.FormValue("issue"))
		r.tagsSet = strings.Split(req.FormValue("tags"), ",")
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/issues/do_transition", func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		r.mu.Lock()
		r.transitions = append(r.transitions, req.FormValue("transition"))
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/issues/add_comment", func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		r.mu.Lock()
		r.comments = append(r.comments, req.FormValue("text"))
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.searchQuery = append(r.searchQuery, req.URL.RawQuery)
		r.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{{
				"key": "cloud-issue-1", "rule": "java:S2245", "line": 12,
				"component": "proj:src/Main.java", "issueStatus": "OPEN",
				"transitions": []string{"accept", "falsepositive", "confirm"},
			}},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 100, "total": 1},
		})
	})
}

// Issue #423, checkbox 3: every migrated hotspot must carry the sqs-hotspot tag.
func TestSyncOneHotspotAsIssueAppliesSQSHotspotTag(t *testing.T) {
	rec := &hotspotIssueSyncRecorder{}
	mux := http.NewServeMux()
	rec.mount(mux)
	e := newCustomCloudTest(t, mux)

	src := matchableHotspot{Key: "hs-1", RuleKey: "java:S2245", Component: "proj:src/Main.java", Line: 12, Status: "TO_REVIEW"}
	target := matchableIssue{Key: "cloud-issue-1", Line: 12}

	if err := syncOneHotspotAsIssue(context.Background(), e, src, target, "", ""); err != nil {
		t.Fatalf("syncOneHotspotAsIssue: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.tagCalls) != 1 || rec.tagCalls[0] != "cloud-issue-1" {
		t.Fatalf("set_tags calls = %v, want exactly one for cloud-issue-1", rec.tagCalls)
	}
	if !slices.Contains(rec.tagsSet, scanreport.HotspotIssueTag) {
		t.Errorf("tags = %v, want it to contain %q", rec.tagsSet, scanreport.HotspotIssueTag)
	}
	if !slices.Contains(rec.tagsSet, metadataSyncTag) {
		t.Errorf("tags = %v, want it to contain %q", rec.tagsSet, metadataSyncTag)
	}
}

// A TO_REVIEW hotspot has no triage to migrate, but must still be tagged —
// this is the case the old actionable filter skipped entirely.
func TestSyncOneHotspotAsIssueTagsUntriagedHotspot(t *testing.T) {
	rec := &hotspotIssueSyncRecorder{}
	mux := http.NewServeMux()
	rec.mount(mux)
	e := newCustomCloudTest(t, mux)

	src := matchableHotspot{Key: "hs-untriaged", RuleKey: "java:S2077", Line: 4, Status: "TO_REVIEW"}
	target := matchableIssue{Key: "cloud-issue-1", Line: 4}

	if err := syncOneHotspotAsIssue(context.Background(), e, src, target, "", ""); err != nil {
		t.Fatalf("syncOneHotspotAsIssue: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !slices.Contains(rec.tagsSet, scanreport.HotspotIssueTag) {
		t.Errorf("an untriaged hotspot was not tagged; tags = %v", rec.tagsSet)
	}
	if len(rec.transitions) != 0 {
		t.Errorf("TO_REVIEW should need no transition, got %v", rec.transitions)
	}
}

func TestSyncOneHotspotAsIssueStatusMapping(t *testing.T) {
	tests := []struct {
		name           string
		status         string
		resolution     string
		cloudTransits  []string
		wantTransition string
	}{
		{"TO_REVIEW needs no transition", "TO_REVIEW", "", []string{"accept", "falsepositive"}, ""},
		{"REVIEWED+SAFE becomes a false positive", "REVIEWED", "SAFE", []string{"accept", "falsepositive"}, "falsepositive"},
		{"REVIEWED+FIXED is accepted", "REVIEWED", "FIXED", []string{"accept", "falsepositive"}, "accept"},
		{"REVIEWED+ACKNOWLEDGED is accepted, not downgraded to SAFE", "REVIEWED", "ACKNOWLEDGED", []string{"accept", "falsepositive"}, "accept"},
		{"accept is skipped when the target does not offer it", "REVIEWED", "ACKNOWLEDGED", []string{"confirm"}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &hotspotIssueSyncRecorder{}
			mux := http.NewServeMux()
			rec.mount(mux)
			e := newCustomCloudTest(t, mux)

			src := matchableHotspot{Key: "hs-1", RuleKey: "java:S2245", Line: 12, Status: tc.status, Resolution: tc.resolution}
			target := matchableIssue{Key: "cloud-issue-1", Line: 12, Transitions: tc.cloudTransits}

			if err := syncOneHotspotAsIssue(context.Background(), e, src, target, "", ""); err != nil {
				t.Fatalf("syncOneHotspotAsIssue: %v", err)
			}

			rec.mu.Lock()
			defer rec.mu.Unlock()
			if tc.wantTransition == "" {
				if len(rec.transitions) != 0 {
					t.Errorf("expected no transition, got %v", rec.transitions)
				}
				return
			}
			if len(rec.transitions) != 1 || rec.transitions[0] != tc.wantTransition {
				t.Errorf("transitions = %v, want [%s]", rec.transitions, tc.wantTransition)
			}
		})
	}
}

// set_tags replaces the whole tag set, so tags already on the Cloud issue must
// survive being marked as a former hotspot.
func TestSyncHotspotIssueTagsPreservesExistingTags(t *testing.T) {
	rec := &hotspotIssueSyncRecorder{}
	mux := http.NewServeMux()
	rec.mount(mux)
	e := newCustomCloudTest(t, mux)

	if failed := syncHotspotIssueTags(context.Background(), e, "cloud-issue-1", []string{"cwe", "former-hotspot"}); failed {
		t.Fatal("syncHotspotIssueTags reported failure")
	}

	rec.mu.Lock()
	got := append([]string(nil), rec.tagsSet...)
	rec.mu.Unlock()
	sort.Strings(got)
	want := []string{"cwe", "former-hotspot", metadataSyncTag, scanreport.HotspotIssueTag}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tags = %v, want %v", got, want)
	}
}

// Re-running must not duplicate the markers.
func TestSyncHotspotIssueTagsIsIdempotent(t *testing.T) {
	rec := &hotspotIssueSyncRecorder{}
	mux := http.NewServeMux()
	rec.mount(mux)
	e := newCustomCloudTest(t, mux)

	existing := []string{"cwe", scanreport.HotspotIssueTag, metadataSyncTag}
	if failed := syncHotspotIssueTags(context.Background(), e, "cloud-issue-1", existing); failed {
		t.Fatal("syncHotspotIssueTags reported failure")
	}

	rec.mu.Lock()
	got := append([]string(nil), rec.tagsSet...)
	rec.mu.Unlock()
	counts := map[string]int{}
	for _, tag := range got {
		counts[tag]++
	}
	if counts[scanreport.HotspotIssueTag] != 1 {
		t.Errorf("%q appears %d times in %v, want once", scanreport.HotspotIssueTag, counts[scanreport.HotspotIssueTag], got)
	}
	if counts[metadataSyncTag] != 1 {
		t.Errorf("%q appears %d times in %v, want once", metadataSyncTag, counts[metadataSyncTag], got)
	}
}

// The target counterpart is an issue now. Looking it up through
// /api/hotspots/search could only ever return nothing.
func TestResolveAndSyncHotspotSearchesIssuesNotHotspots(t *testing.T) {
	rec := &hotspotIssueSyncRecorder{}
	mux := http.NewServeMux()
	rec.mount(mux)
	var hotspotSearchCalls int
	var mu sync.Mutex
	mux.HandleFunc("GET /api/hotspots/search", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hotspotSearchCalls++
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"hotspots": []map[string]any{}, "paging": map[string]any{"total": 0}})
	})
	e := newCustomCloudTest(t, mux)

	src := matchableHotspot{
		Key: "hs-1", RuleKey: "java:S2245",
		Component: "proj:src/Main.java", Line: 12, Status: "REVIEWED", Resolution: "SAFE",
	}
	counter := NewTaskCounter("test")
	outcome := resolveAndSyncHotspot(context.Background(), e, "proj", "org", "", "srcProj", src, counter)
	if outcome != syncOutcomeSynced {
		t.Fatalf("outcome = %v, want synced", outcome)
	}

	mu.Lock()
	defer mu.Unlock()
	if hotspotSearchCalls != 0 {
		t.Errorf("/api/hotspots/search was called %d times; the target has no hotspots", hotspotSearchCalls)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.searchQuery) == 0 {
		t.Fatal("/api/issues/search was never called")
	}
	// The issue endpoint accepts a server-side rule filter, unlike the hotspot one.
	if !strings.Contains(rec.searchQuery[0], "rules=java%3AS2245") {
		t.Errorf("issue search query %q does not scope by rule", rec.searchQuery[0])
	}
	if !slices.Contains(rec.tagsSet, scanreport.HotspotIssueTag) {
		t.Errorf("matched hotspot was not tagged; tags = %v", rec.tagsSet)
	}
}

// The back-link must be posted on the issue, and must still point at the
// source server's security_hotspots view.
func TestSyncOneHotspotAsIssueAddsSourceLinkComment(t *testing.T) {
	rec := &hotspotIssueSyncRecorder{}
	mux := http.NewServeMux()
	rec.mount(mux)
	e := newCustomCloudTest(t, mux)

	src := matchableHotspot{Key: "hs-42", RuleKey: "java:S2245", Line: 12, Status: "TO_REVIEW", Branch: "main"}
	target := matchableIssue{Key: "cloud-issue-1", Line: 12}

	if err := syncOneHotspotAsIssue(context.Background(), e, src, target, "https://sq.example.com", "srcProj"); err != nil {
		t.Fatalf("syncOneHotspotAsIssue: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var found string
	for _, c := range rec.comments {
		if strings.Contains(c, hotspotSourceLinkMarker) {
			found = c
		}
	}
	if found == "" {
		t.Fatalf("no source-link comment posted; comments = %v", rec.comments)
	}
	for _, want := range []string{"https://sq.example.com/security_hotspots", "id=srcProj", "hotspots=hs-42", "branch=main"} {
		if !strings.Contains(found, want) {
			t.Errorf("source link %q missing %q", found, want)
		}
	}
}

// An already-linked issue must not accumulate duplicate back-links on re-run.
func TestSyncOneHotspotAsIssueSourceLinkIsIdempotent(t *testing.T) {
	rec := &hotspotIssueSyncRecorder{}
	mux := http.NewServeMux()
	rec.mount(mux)
	e := newCustomCloudTest(t, mux)

	src := matchableHotspot{Key: "hs-42", RuleKey: "java:S2245", Line: 12, Status: "TO_REVIEW"}
	target := matchableIssue{
		Key: "cloud-issue-1", Line: 12,
		Comments: []issueComment{{Markdown: hotspotSourceLinkMarker + "(https://sq.example.com/security_hotspots?id=srcProj&hotspots=hs-42)"}},
	}

	if err := syncOneHotspotAsIssue(context.Background(), e, src, target, "https://sq.example.com", "srcProj"); err != nil {
		t.Fatalf("syncOneHotspotAsIssue: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, c := range rec.comments {
		if strings.Contains(c, hotspotSourceLinkMarker) {
			t.Errorf("posted a duplicate source-link comment: %q", c)
		}
	}
}

// Hotspot review comments must reach the migrated issue.
func TestSyncOneHotspotAsIssueMigratesReviewComments(t *testing.T) {
	rec := &hotspotIssueSyncRecorder{}
	mux := http.NewServeMux()
	rec.mount(mux)
	e := newCustomCloudTest(t, mux)

	src := matchableHotspot{
		Key: "hs-1", RuleKey: "java:S2245", Line: 12, Status: "REVIEWED", Resolution: "SAFE",
		Comments: []hotspotComment{{Login: "alice", Markdown: "reviewed, key is a test fixture", CreatedAt: "2021-05-05T00:00:00+0000"}},
	}
	target := matchableIssue{Key: "cloud-issue-1", Line: 12, Transitions: []string{"falsepositive"}}

	if err := syncOneHotspotAsIssue(context.Background(), e, src, target, "", ""); err != nil {
		t.Fatalf("syncOneHotspotAsIssue: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var got bool
	for _, c := range rec.comments {
		if strings.Contains(c, "reviewed, key is a test fixture") && strings.Contains(c, "alice") {
			got = true
		}
	}
	if !got {
		t.Errorf("review comment not migrated; comments = %v", rec.comments)
	}
}

func TestHotspotCommentsAsIssueComments(t *testing.T) {
	in := []hotspotComment{{Login: "bob", HTMLText: "<p>hi</p>", Markdown: "hi", CreatedAt: "2020-01-01"}}
	got := hotspotCommentsAsIssueComments(in)
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	want := issueComment{Login: "bob", HTMLText: "<p>hi</p>", Markdown: "hi", CreatedAt: "2020-01-01"}
	if got[0] != want {
		t.Errorf("got %+v, want %+v", got[0], want)
	}
}
