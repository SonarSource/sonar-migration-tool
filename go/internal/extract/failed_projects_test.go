// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// A server or network failure for one project skips that project and
// lets the run carry on. Anything that would fail for every project, an
// item that is not a project's, and cancellation still stop the run.
func TestSkipFailedProject(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	timeout := &url.Error{Op: "Get", URL: "https://sq.example.com/api/x", Err: os.ErrDeadlineExceeded}
	gateway := &common.HTTPError{StatusCode: http.StatusGatewayTimeout, Method: "GET", URL: "/api/x"}

	cases := []struct {
		name       string
		ctx        context.Context
		projectKey string
		err        error
		wantSkip   bool
	}{
		{"HTTP 504", context.Background(), "p1", gateway, true},
		{"HTTP 500", context.Background(), "p1", &common.HTTPError{StatusCode: 500}, true},
		{"client timeout", context.Background(), "p1", timeout, true},
		{"connection cut mid-body", context.Background(), "p1", io.ErrUnexpectedEOF, true},
		{"HTTP 400 fails for every project", context.Background(), "p1", &common.HTTPError{StatusCode: 400}, false},
		{"HTTP 401 fails for every project", context.Background(), "p1", &common.HTTPError{StatusCode: 401}, false},
		{"HTTP 429 is systemic", context.Background(), "p1", &common.HTTPError{StatusCode: 429}, false},
		{"not a project's item", context.Background(), "", gateway, false},
		{"cancelled run", cancelled, "p1", timeout, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestExecutor(t)
			got := skipFailedProject(tc.ctx, e, "getProjectMeasures", tc.projectKey, tc.err)
			if tc.wantSkip {
				if got != nil {
					t.Fatalf("expected the project to be skipped, got %v", got)
				}
				if !e.IsSkipped(tc.projectKey) {
					t.Error("expected the project to be skipped for the rest of the run")
				}
				list := e.FailedProjects()
				if len(list) != 1 || list[0].Key != tc.projectKey || list[0].Task != "getProjectMeasures" {
					t.Errorf("FailedProjects = %+v, want one entry for %s", list, tc.projectKey)
				}
				return
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("expected the error to stop the run, got %v", got)
			}
			if len(e.FailedProjects()) != 0 {
				t.Errorf("expected no failed project, got %+v", e.FailedProjects())
			}
		})
	}
}

// Many failures in a row mean the server is down, not that one project
// is failing: the run stops instead of skipping every project left.
func TestSkipFailedProject_StopsAfterConsecutiveFailures(t *testing.T) {
	prev := maxConsecutiveProjectFailures
	maxConsecutiveProjectFailures = 3
	t.Cleanup(func() { maxConsecutiveProjectFailures = prev })

	e := newTestExecutor(t)
	gateway := &common.HTTPError{StatusCode: http.StatusGatewayTimeout}
	fail := func(key string) error {
		return skipFailedProject(context.Background(), e, "getProjectMeasures", key, gateway)
	}

	if err := fail("p1"); err != nil {
		t.Fatalf("first failure: %v", err)
	}
	if err := fail("p2"); err != nil {
		t.Fatalf("second failure: %v", err)
	}
	e.recordProjectSuccess()
	if err := fail("p3"); err != nil {
		t.Fatalf("a success between failures must reset the count, got %v", err)
	}
	if err := fail("p4"); err != nil {
		t.Fatalf("fourth failure: %v", err)
	}
	err := fail("p5")
	if err == nil || !strings.Contains(err.Error(), "3 projects in a row failed") {
		t.Fatalf("expected the third failure in a row to stop the run, got %v", err)
	}
	if !errors.Is(err, gateway) {
		t.Errorf("expected the stop error to wrap the HTTP error, got %v", err)
	}
}

func TestProjectKeyOf(t *testing.T) {
	cases := []struct {
		depTask, item, want string
	}{
		{"getProjects", `{"key":"p1"}`, "p1"},
		{"getProjectComponentTree", `{"key":"p1:src/A.java","projectKey":"p1"}`, "p1"},
		{"getRules", `{"key":"java:S1"}`, ""},
		{"getProfiles", `{"key":"AYx"}`, ""},
	}
	for _, tc := range cases {
		if got := projectKeyOf(tc.depTask, json.RawMessage(tc.item)); got != tc.want {
			t.Errorf("projectKeyOf(%s, %s) = %q, want %q", tc.depTask, tc.item, got, tc.want)
		}
	}
}

// failingProjectServer wraps the mock server so every api/measures/search
// request for failKey answers 504.
func failingProjectServer(t *testing.T, failKey string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	mock := newMockServer()
	t.Cleanup(mock.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/measures/search" && r.URL.Query().Get("projectKeys") == failKey {
			hits.Add(1)
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		mock.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// One project failing no longer fails the extract: it is listed in
// failed_projects.json and the other projects are extracted as usual.
func TestRunExtractSkipsFailedProject(t *testing.T) {
	var hits atomic.Int32
	srv := failingProjectServer(t, "proj2", &hits)
	dir := t.TempDir()

	_, err := RunExtract(context.Background(), ExtractConfig{
		URL: srv.URL, Token: testToken, ExportDirectory: dir,
		TargetTask: "getProjectMeasures", Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("expected the extract to finish despite one failing project, got %v", err)
	}

	extractDir := firstExtractDir(t, dir)
	list, err := common.ReadFailedProjects(extractDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Key != "proj2" || list[0].Task != "getProjectMeasures" {
		t.Fatalf("failed_projects.json = %+v, want proj2 from getProjectMeasures", list)
	}
	items, err := NewDataStore(extractDir).ReadAll("getProjectMeasures")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if key := extractField(item, "projectKey"); key != "proj1" {
			t.Errorf("unexpected measures for %q; only proj1 should have been extracted", key)
		}
	}
	if len(items) == 0 {
		t.Error("expected proj1's measures to be extracted")
	}
}

// A resumed extract keeps skipping the projects an earlier attempt
// skipped: the tasks that finished before have none of their data.
func TestRunExtractResumeKeepsFailedProjectsSkipped(t *testing.T) {
	var hits atomic.Int32
	srv := failingProjectServer(t, "proj2", &hits)
	dir := t.TempDir()
	extractID := "04-16-2026-03"
	extractDir := filepath.Join(dir, extractID)
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := common.WriteFailedProjects(extractDir, []common.FailedProject{
		{Key: "proj2", Task: "getProjectDetails", Error: "HTTP 504"},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := RunExtract(context.Background(), ExtractConfig{
		URL: srv.URL, Token: testToken, ExportDirectory: dir, ExtractID: extractID,
		TargetTask: "getProjectMeasures", Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("RunExtract: %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("proj2 was requested %d times; a project an earlier attempt skipped must stay skipped", got)
	}
	list, _ := common.ReadFailedProjects(extractDir)
	if len(list) != 1 || list[0].Task != "getProjectDetails" {
		t.Errorf("failed_projects.json = %+v, want the earlier entry kept", list)
	}
}

func TestPrintFailedProjectsBlock(t *testing.T) {
	var none bytes.Buffer
	PrintFailedProjectsBlock(&none, nil)
	if none.Len() != 0 {
		t.Errorf("expected nothing printed with no failed project, got %q", none.String())
	}

	var out bytes.Buffer
	PrintFailedProjectsBlock(&out, []common.FailedProject{{Key: "p1", Task: "getProjectIssues", Error: "HTTP 504"}})
	for _, want := range []string{"1 project(s) were skipped", "left out of the migration", "p1 (getProjectIssues): HTTP 504", "--project_key_regexp"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected %q in:\n%s", want, out.String())
		}
	}
}

// A per-project success through runWithSem ends a run of failures, so
// failures spread across a long extract never add up to a stop.
func TestRunWithSemSuccessResetsFailureStreak(t *testing.T) {
	prev := maxConsecutiveProjectFailures
	maxConsecutiveProjectFailures = 2
	t.Cleanup(func() { maxConsecutiveProjectFailures = prev })

	e := newTestExecutor(t)
	w, err := e.Store.Writer("getProjectMeasures")
	if err != nil {
		t.Fatal(err)
	}
	gateway := &common.HTTPError{StatusCode: http.StatusGatewayTimeout}
	fail := func(context.Context, json.RawMessage, *ChunkWriter) error { return gateway }
	ok := func(context.Context, json.RawMessage, *ChunkWriter) error { return nil }
	run := func(key string, fn func(context.Context, json.RawMessage, *ChunkWriter) error) error {
		return runWithSem(context.Background(), e, "getProjectMeasures", key, json.RawMessage(`{"key":"`+key+`"}`), w, fn)
	}

	if err := run("p1", fail); err != nil {
		t.Fatalf("p1: %v", err)
	}
	if err := run("p2", ok); err != nil {
		t.Fatalf("p2: %v", err)
	}
	if err := run("p3", fail); err != nil {
		t.Fatalf("p3: the success of p2 should have reset the count, got %v", err)
	}
	if err := run("p4", fail); err == nil {
		t.Fatal("p4: expected two failures in a row to stop the run")
	}
}
