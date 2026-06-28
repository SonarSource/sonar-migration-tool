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
