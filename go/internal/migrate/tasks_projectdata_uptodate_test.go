// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sq-api-go/types"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return parsed
}

func TestTargetBranchUpToDate(t *testing.T) {
	sourceDate := mustTime(t, "2026-09-10T08:48:18Z")

	tests := []struct {
		name         string
		dates        map[string]time.Time
		targetBranch string
		lastAnalysis time.Time
		want         bool
	}{
		{
			name:         "target holds the same date - nothing to submit",
			dates:        map[string]time.Time{"main": sourceDate},
			targetBranch: "main",
			lastAnalysis: sourceDate,
			want:         true,
		},
		{
			name:         "target is newer - the CE would reject an older report",
			dates:        map[string]time.Time{"main": sourceDate.Add(time.Hour)},
			targetBranch: "main",
			lastAnalysis: sourceDate,
			want:         true,
		},
		{
			name:         "source was re-analyzed since - import again",
			dates:        map[string]time.Time{"main": sourceDate},
			targetBranch: "main",
			lastAnalysis: sourceDate.Add(time.Hour),
			want:         false,
		},
		{
			name:         "branch absent from the target",
			dates:        map[string]time.Time{"main": sourceDate},
			targetBranch: "develop",
			lastAnalysis: sourceDate,
			want:         false,
		},
		{
			name:         "target project is new - no branch list at all",
			dates:        nil,
			targetBranch: "main",
			lastAnalysis: sourceDate,
			want:         false,
		},
		{
			name:         "source branch never analyzed - report is stamped now",
			dates:        map[string]time.Time{"main": sourceDate},
			targetBranch: "main",
			lastAnalysis: time.Time{},
			want:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			existing, got := targetBranchUpToDate(tc.dates, tc.targetBranch, tc.lastAnalysis)
			if got != tc.want {
				t.Errorf("targetBranchUpToDate = %v, want %v", got, tc.want)
			}
			if got && existing.IsZero() {
				t.Error("an up-to-date branch must report the date the target holds")
			}
		})
	}
}

func TestTargetAnalysisDates(t *testing.T) {
	dates := targetAnalysisDates([]types.Branch{
		{Name: "main", IsMain: true, AnalysisDate: "2026-09-10T08:48:18+0000"},
		{Name: "rfc3339", AnalysisDate: "2026-09-11T08:48:18Z"},
		{Name: "never-analyzed", AnalysisDate: ""},
		{Name: "unparseable", AnalysisDate: "last tuesday"},
	})

	if len(dates) != 2 {
		t.Fatalf("expected 2 parsed dates, got %d: %v", len(dates), dates)
	}
	if got := dates["main"]; !got.Equal(mustTime(t, "2026-09-10T08:48:18Z")) {
		t.Errorf("main: got %v", got)
	}
	if got := dates["rfc3339"]; !got.Equal(mustTime(t, "2026-09-11T08:48:18Z")) {
		t.Errorf("rfc3339: got %v", got)
	}
	for _, absent := range []string{"never-analyzed", "unparseable"} {
		if _, ok := dates[absent]; ok {
			t.Errorf("%s should not have an entry", absent)
		}
	}

	if got := targetAnalysisDates(nil); got != nil {
		t.Errorf("empty branch list should yield a nil map, got %v", got)
	}
}

func TestMainBranchName(t *testing.T) {
	branches := []types.Branch{{Name: "develop"}, {Name: "gitlab", IsMain: true}}
	if got := mainBranchName(branches); got != "gitlab" {
		t.Errorf("mainBranchName = %q, want %q", got, "gitlab")
	}
	if got := mainBranchName([]types.Branch{{Name: "develop"}}); got != "" {
		t.Errorf("no main branch should yield %q, got %q", "", got)
	}
}

// newCountingCEServer returns a CE mock that succeeds and counts how many
// reports were submitted to it.
func newCountingCEServer(submits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ce/submit":
			submits.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-uptodate"}) //nolint:errcheck
		case "/api/ce/task":
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"task": map[string]any{"status": "SUCCESS"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
}

func upToDateTestProject(t *testing.T) json.RawMessage {
	t.Helper()
	proj, err := json.Marshal(map[string]any{
		"key":                "proj1",
		"cloud_project_key":  "cloud-proj1",
		"sonarcloud_org_key": "cloud-org1",
		"server_url":         testServerURL,
	})
	if err != nil {
		t.Fatalf("marshalling project: %v", err)
	}
	return proj
}

// #588 — a second transfer against an unchanged source must not resubmit the
// branch. The Compute Engine rejects a report dated at or before the one it
// already processed, which used to fail the whole project.
func TestImportProjectBranchesSkipsUpToDateBranch(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)

	var submits atomic.Int32
	srv := newCountingCEServer(&submits)
	defer srv.Close()

	e := newProjectDataExecutor(t, dir)
	e.CloudURL = srv.URL + "/"
	e.Raw = common.NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("importProjectData")
	lastAnalysis := mustTime(t, "2026-09-10T08:48:18Z")
	branches := []branchInfo{{Name: "main", IsMain: true, LastAnalysisDate: lastAnalysis}}
	scBranches := []types.Branch{{Name: "main", IsMain: true, AnalysisDate: "2026-09-10T08:48:18+0000"}}

	if err := importProjectBranches(context.Background(), e, upToDateTestProject(t), branches, scBranches, nil, w); err != nil {
		t.Fatalf("importProjectBranches: %v", err)
	}

	if got := submits.Load(); got != 0 {
		t.Errorf("expected no report submission for an up-to-date branch, got %d", got)
	}

	items, _ := e.Store.ReadAll("importProjectData")
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 branch record, got %d", len(items))
	}
	if got := extractField(items[0], "status"); got != branchStatusUpToDate {
		t.Errorf("status = %q, want %q", got, branchStatusUpToDate)
	}
}

// The mirror case: the source was analyzed again after the target's last
// import, so the report carries a newer date and must still be submitted.
func TestImportProjectBranchesImportsWhenSourceIsNewer(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)

	var submits atomic.Int32
	srv := newCountingCEServer(&submits)
	defer srv.Close()

	e := newProjectDataExecutor(t, dir)
	e.CloudURL = srv.URL + "/"
	e.Raw = common.NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("importProjectData")
	branches := []branchInfo{{Name: "main", IsMain: true, LastAnalysisDate: mustTime(t, "2026-09-20T08:48:18Z")}}
	scBranches := []types.Branch{{Name: "main", IsMain: true, AnalysisDate: "2026-09-10T08:48:18+0000"}}

	if err := importProjectBranches(context.Background(), e, upToDateTestProject(t), branches, scBranches, nil, w); err != nil {
		t.Fatalf("importProjectBranches: %v", err)
	}

	if got := submits.Load(); got != 1 {
		t.Errorf("expected the branch to be submitted once, got %d", got)
	}
	items, _ := e.Store.ReadAll("importProjectData")
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 branch record, got %d", len(items))
	}
	if got := extractField(items[0], "status"); got != "success" {
		t.Errorf("status = %q, want %q", got, "success")
	}
}
