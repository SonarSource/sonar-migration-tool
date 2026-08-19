// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The cloud issue search must be scoped to the source issue's branch;
// without it /api/issues/search resolves to the project's main branch only
// and non-main-branch issues never find their counterpart (so they go
// unsynced). Source and target branch names match 1:1 after import (#428).
func TestFindCloudIssueCandidatesPassesBranch(t *testing.T) {
	var seenBranch string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		seenBranch = r.URL.Query().Get("branch")
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{
				{"key": "iss-1", "rule": "java:S100", "component": "cloud-proj:src/app.go", "line": 10},
			},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 1},
		})
	})
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()

	apiSrv := newMockAPIServer()
	defer apiSrv.Close()

	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	got, err := findCloudIssueCandidates(context.Background(), e, "cloud-proj", "cloud-org", "src/app.go", "java:S100", "release-3.x")
	if err != nil {
		t.Fatalf("findCloudIssueCandidates: %v", err)
	}
	if seenBranch != "release-3.x" {
		t.Errorf("expected branch=release-3.x forwarded to cloud search, got %q", seenBranch)
	}
	if len(got) != 1 || got[0].Key != "iss-1" {
		t.Errorf("expected single candidate iss-1, got %+v", got)
	}
}

// A cloud-search failure during issue resolution is reported as a lookup
// error (and not a silent skip), exercising the branch-aware call site.
func TestResolveAndSyncIssueLookupError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	src := matchableIssue{Key: "s1", Rule: "java:S100", Component: "src-proj:src/app.go", Line: 10, Branch: "develop"}
	got := resolveAndSyncIssue(context.Background(), e, "cloud-proj", "cloud-org", "", "src-proj", src, nil)
	if got != syncOutcomeLookupError {
		t.Fatalf("want syncOutcomeLookupError, got %v", got)
	}
}

// A cloud-search failure during the bulk index build (#527) is surfaced as an
// error rather than silently producing an empty index — the caller
// (syncProjectHotspots) treats this as a whole-branch failure and skips
// resolving that branch's hotspots this run, rather than misreporting them
// as not_found. The lookup goes through /api/issues/search: since
// 2026-07-01 the migrated hotspot is an issue on the target, so
// /api/hotspots/search is never consulted (#423).
func TestBuildCloudIssueIndexLookupError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	_, err := buildCloudIssueIndex(context.Background(), e, "cloud-proj", "cloud-org", "develop", []string{"rk1"})
	if err == nil {
		t.Fatal("want an error from buildCloudIssueIndex on a cloud search failure")
	}
}

// No cloud candidates on the source's file means resolveAndSyncIssue
// reports not_found rather than synced or an error.
func TestResolveAndSyncIssueNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 0},
		})
	})
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	src := matchableIssue{Key: "s1", Rule: "java:S100", Component: "src-proj:src/app.go", Line: 10, Message: "Do not do this"}
	got := resolveAndSyncIssue(context.Background(), e, "cloud-proj", "cloud-org", "", "src-proj", src, nil)
	if got != syncOutcomeNotFound {
		t.Fatalf("want syncOutcomeNotFound, got %v", got)
	}
}

// Two cloud candidates that both exactly match the source's file, line, and
// message are ambiguous — resolveAndSyncIssue must skip (line_mismatch)
// rather than guess.
func TestResolveAndSyncIssueLineMismatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{
				{"key": "iss-1", "rule": "java:S100", "component": "cloud-proj:src/app.go", "line": 10, "message": "Do not do this"},
				{"key": "iss-2", "rule": "java:S100", "component": "cloud-proj:src/app.go", "line": 10, "message": "Do not do this"},
			},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 2},
		})
	})
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	src := matchableIssue{Key: "s1", Rule: "java:S100", Component: "src-proj:src/app.go", Line: 10, Message: "Do not do this"}
	got := resolveAndSyncIssue(context.Background(), e, "cloud-proj", "cloud-org", "", "src-proj", src, nil)
	if got != syncOutcomeLineMismatch {
		t.Fatalf("want syncOutcomeLineMismatch, got %v", got)
	}
}

// No cloud candidates on the source's file means resolveAndSyncHotspot
// reports not_found rather than synced or an error. The lookup goes through
// /api/issues/search (#423) — see TestResolveAndSyncHotspotLookupError.
func TestResolveAndSyncHotspotNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 0},
		})
	})
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	src := matchableHotspot{Key: "h1", RuleKey: "rk1", Component: "src-proj:src/app.go", Line: 10, Message: "Review this"}
	idx, err := buildCloudIssueIndex(context.Background(), e, "cloud-proj", "cloud-org", "", []string{src.RuleKey})
	if err != nil {
		t.Fatalf("buildCloudIssueIndex: %v", err)
	}
	got := resolveAndSyncHotspot(context.Background(), e, "cloud-proj", "", "src-proj", src, classifyHotspotForSync(src), idx, nil)
	if got != syncOutcomeNotFound {
		t.Fatalf("want syncOutcomeNotFound, got %v", got)
	}
}

// Two cloud candidates that both exactly match the source's file, line, and
// message are ambiguous — resolveAndSyncHotspot must skip (line_mismatch)
// rather than guess. The lookup goes through /api/issues/search (#423).
func TestResolveAndSyncHotspotLineMismatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issues": []map[string]any{
				{"key": "hs-1", "rule": "rk1", "component": "cloud-proj:src/app.go", "line": 10, "message": "Review this"},
				{"key": "hs-2", "rule": "rk1", "component": "cloud-proj:src/app.go", "line": 10, "message": "Review this"},
			},
			"paging": map[string]any{"pageIndex": 1, "pageSize": 500, "total": 2},
		})
	})
	cloudSrv := httptest.NewServer(mux)
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	e := newTestExecutor(cloudSrv, apiSrv, t.TempDir())

	src := matchableHotspot{Key: "h1", RuleKey: "rk1", Component: "src-proj:src/app.go", Line: 10, Message: "Review this"}
	idx, err := buildCloudIssueIndex(context.Background(), e, "cloud-proj", "cloud-org", "", []string{src.RuleKey})
	if err != nil {
		t.Fatalf("buildCloudIssueIndex: %v", err)
	}
	got := resolveAndSyncHotspot(context.Background(), e, "cloud-proj", "", "src-proj", src, classifyHotspotForSync(src), idx, nil)
	if got != syncOutcomeLineMismatch {
		t.Fatalf("want syncOutcomeLineMismatch, got %v", got)
	}
}
