// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/cloud"
)

// renameRecorder is a mock SonarCloud server that reports a configurable main
// branch name and records any rename request it receives. #428.
func newBranchRenameServer(t *testing.T, currentMain string) (*httptest.Server, *string) {
	t.Helper()
	var renamedTo string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/project_branches/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"branches": []map[string]any{
				{"name": currentMain, "isMain": true, "type": "LONG"},
				{"name": "develop", "isMain": false, "type": "LONG"},
			},
		})
	})
	mux.HandleFunc("POST /api/project_branches/rename", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		renamedTo = r.FormValue("name")
		w.WriteHeader(http.StatusNoContent)
	})
	return httptest.NewServer(mux), &renamedTo
}

func newCloudOnlyExecutor(srv *httptest.Server) *Executor {
	return &Executor{
		Cloud:  cloud.New(sqapi.NewCloudClient(srv.URL+"/", "test-token")),
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1})),
	}
}

// #428 — the SonarCloud main branch (created as "master") is renamed to the
// source main branch name.
func TestRenameSCMainBranchToSource_Renames(t *testing.T) {
	srv, renamedTo := newBranchRenameServer(t, "master")
	defer srv.Close()
	e := newCloudOnlyExecutor(srv)

	renameSCMainBranchToSource(context.Background(), e, "cloud-proj", "main")

	if *renamedTo != "main" {
		t.Errorf("expected rename to %q, got %q", "main", *renamedTo)
	}
}

// No rename when the SonarCloud main branch already carries the source name
// (e.g. source main is "master" and SonarCloud's default is also "master").
func TestRenameSCMainBranchToSource_NoOpWhenSame(t *testing.T) {
	srv, renamedTo := newBranchRenameServer(t, "master")
	defer srv.Close()
	e := newCloudOnlyExecutor(srv)

	renameSCMainBranchToSource(context.Background(), e, "cloud-proj", "master")

	if *renamedTo != "" {
		t.Errorf("expected no rename, but renamed to %q", *renamedTo)
	}
}

// No panic / no call when no Cloud client is configured (e.g. dry contexts).
func TestRenameSCMainBranchToSource_NilCloud(t *testing.T) {
	e := &Executor{Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))}
	// Must not panic.
	renameSCMainBranchToSource(context.Background(), e, "cloud-proj", "main")
}
