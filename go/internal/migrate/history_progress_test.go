// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// TestProjectHistoryPointTotal covers #564's history-migration ETA
// integration: the upfront point count RunMigrate uses to give
// migrateProjectHistory its own tracked unit of work must sum every
// extracted project's main-branch history points, purely from
// already-extracted data (getProjects/getBranches/getProjectAnalysisHistory)
// — no API calls, safe to call before a single migrate task has run.
func TestProjectHistoryPointTotal(t *testing.T) {
	dir := t.TempDir()
	writeExtractMetaJSON(t, dir, extractRun, testServerURL)
	writeJSONL(filepath.Join(dir, extractRun, "getProjects"), []map[string]any{
		{"key": projMain},
		{"key": "proj2"},
	})
	writeJSONL(filepath.Join(dir, extractRun, "getBranches"), []map[string]any{
		{"projectKey": projMain, "name": "main", "isMain": true, "type": "BRANCH"},
		{"projectKey": "proj2", "name": "main", "isMain": true, "type": "BRANCH"},
		// A non-main branch's history must not be counted — migrateBranchHistory
		// only ever replays the main branch (see its own doc comment).
		{"projectKey": "proj2", "name": "feature", "isMain": false, "type": "BRANCH"},
	})
	writeJSONL(filepath.Join(dir, extractRun, "getProjectAnalysisHistory"), []map[string]any{
		histRecord(projMain, "main", "2025-01-01T00:00:00+0000", "1.0", "100"),
		histRecord(projMain, "main", "2025-02-01T00:00:00+0000", "1.1", "110"),
		histRecord("proj2", "main", "2025-01-01T00:00:00+0000", "2.0", "50"),
		histRecord("proj2", "feature", "2025-01-15T00:00:00+0000", "2.0-dev", "55"),
	})

	cloudSrv := newMockCloudServer()
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	e.MigrateHistory = false
	if got := projectHistoryPointTotal(e); got != 0 {
		t.Errorf("with MigrateHistory off: got %d, want 0", got)
	}

	e.MigrateHistory = true
	if got := projectHistoryPointTotal(e); got != 3 {
		t.Errorf("total = %d, want 3 (2 for %s/main + 1 for proj2/main; proj2/feature excluded, not main)", got, projMain)
	}
}

// TestProjectHistoryPointTotalNoHistoryExtracted: when extract never ran
// with --migrate_history, getProjectAnalysisHistory has no records at
// all — the total must be 0, keeping the feature a true no-op.
func TestProjectHistoryPointTotalNoHistoryExtracted(t *testing.T) {
	dir := t.TempDir()
	writeExtractMetaJSON(t, dir, extractRun, testServerURL)
	writeJSONL(filepath.Join(dir, extractRun, "getProjects"), []map[string]any{
		{"key": projMain},
	})
	writeJSONL(filepath.Join(dir, extractRun, "getBranches"), []map[string]any{
		{"projectKey": projMain, "name": "main", "isMain": true, "type": "BRANCH"},
	})

	cloudSrv := newMockCloudServer()
	t.Cleanup(cloudSrv.Close)
	apiSrv := newMockAPIServer()
	t.Cleanup(apiSrv.Close)
	e := newTestExecutor(cloudSrv, apiSrv, dir)
	e.MigrateHistory = true

	if got := projectHistoryPointTotal(e); got != 0 {
		t.Errorf("total = %d, want 0 (no getProjectAnalysisHistory extract data)", got)
	}
}

// TestMigrateBranchHistoryIncrementsHistoryProgress covers #564's other
// half of the history-ETA wiring: every point migrateBranchHistory
// attempts — success or failure — must advance e.HistoryProgress, since
// either way real wall-clock time was spent on it.
func TestMigrateBranchHistoryIncrementsHistoryProgress(t *testing.T) {
	t.Run("all points succeed", func(t *testing.T) {
		rec := newHistRecorder()
		mux := histProfileMux(rec)
		mux.HandleFunc("POST /api/ce/submit", func(w http.ResponseWriter, r *http.Request) {
			rec.note(r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-hist"})
		})
		mux.HandleFunc("GET /api/ce/task", func(w http.ResponseWriter, r *http.Request) {
			rec.note(r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"status": "SUCCESS"}})
		})
		addDefaultCloudHandler(mux)

		e := newCustomCloudTest(t, mux)
		e.MigrateHistory = true
		e.HistoryProgress = common.NewProgressLogger(e.Logger, "migrateProjectHistory", 2)
		histSeed(e, []map[string]any{
			histRecord(projMain, branchMain, "2022-01-15T00:00:00Z", "1.0", "100"),
			histRecord(projMain, branchMain, "2023-06-01T00:00:00Z", "2.0", "200"),
		})

		migrateBranchHistory(context.Background(), e,
			histBranchContext(), branchInfo{Name: branchMain, IsMain: true}, branchMain)

		if got := e.HistoryProgress.Fraction(); got != 1 {
			t.Errorf("HistoryProgress.Fraction() = %v, want 1 (both points incremented)", got)
		}
	})

	t.Run("stops after first failure, only the attempted point counts", func(t *testing.T) {
		rec := newHistRecorder()
		mux := histProfileMux(rec)
		mux.HandleFunc("POST /api/ce/submit", func(w http.ResponseWriter, r *http.Request) {
			rec.note(r.URL.Path)
			http.Error(w, `{"errors":[{"msg":"nope"}]}`, http.StatusInternalServerError)
		})
		addDefaultCloudHandler(mux)

		e := newCustomCloudTest(t, mux)
		e.MigrateHistory = true
		e.HistoryProgress = common.NewProgressLogger(e.Logger, "migrateProjectHistory", 2)
		histSeed(e, []map[string]any{
			histRecord(projMain, branchMain, "2022-01-15T00:00:00Z", "1.0", "100"),
			histRecord(projMain, branchMain, "2023-06-01T00:00:00Z", "2.0", "200"),
		})

		migrateBranchHistory(context.Background(), e,
			histBranchContext(), branchInfo{Name: branchMain, IsMain: true}, branchMain)

		if got := e.HistoryProgress.Fraction(); got != 0.5 {
			t.Errorf("HistoryProgress.Fraction() = %v, want 0.5 (1 of 2 points attempted before giving up)", got)
		}
	})

	t.Run("nil HistoryProgress is a no-op, not a panic", func(t *testing.T) {
		rec := newHistRecorder()
		mux := histProfileMux(rec)
		mux.HandleFunc("POST /api/ce/submit", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-hist"})
		})
		mux.HandleFunc("GET /api/ce/task", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"status": "SUCCESS"}})
		})
		addDefaultCloudHandler(mux)

		e := newCustomCloudTest(t, mux)
		e.MigrateHistory = true
		// e.HistoryProgress deliberately left nil, mirroring a real run with
		// no history to replay for OTHER projects.
		histSeed(e, []map[string]any{
			histRecord(projMain, branchMain, "2022-01-15T00:00:00Z", "1.0", "100"),
		})

		migrateBranchHistory(context.Background(), e,
			histBranchContext(), branchInfo{Name: branchMain, IsMain: true}, branchMain) // must not panic
	})
}
