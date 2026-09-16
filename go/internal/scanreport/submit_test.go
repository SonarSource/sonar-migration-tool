// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package scanreport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain drives CEPollInterval down to near-zero for the whole package so
// PollCETask tests don't pay its real wall-clock delay on every poll (#571).
func TestMain(m *testing.M) {
	CEPollInterval = time.Millisecond
	os.Exit(m.Run())
}

func TestPreCreateAnalysis(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "uuid-123", "branchId": "b-1", "branchType": "long", "referenceBranchName": "main",
		})
	}))
	defer srv.Close()

	res, err := PreCreateAnalysis(context.Background(), srv.Client(), AnalysisConfig{
		APIURL: srv.URL, OrgKey: "org", ProjectKey: "proj",
		ProjectVersion: "1.0", BranchName: "release-x", TargetBranch: "main",
	})
	if err != nil {
		t.Fatalf("PreCreateAnalysis: %v", err)
	}
	if gotPath != "/analysis/analyses" {
		t.Errorf("path: want /analysis/analyses, got %s", gotPath)
	}
	if res.AnalysisUUID != "uuid-123" {
		t.Errorf("analysis uuid: want uuid-123, got %q", res.AnalysisUUID)
	}
	if gotBody["organizationKey"] != "org" || gotBody["projectKey"] != "proj" ||
		gotBody["branchName"] != "release-x" || gotBody["targetBranchName"] != "main" {
		t.Errorf("request body wrong: %v", gotBody)
	}
}

func TestPreCreateAnalysisErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Forbidden"}`))
	}))
	defer srv.Close()
	if _, err := PreCreateAnalysis(context.Background(), srv.Client(), AnalysisConfig{
		APIURL: srv.URL, OrgKey: "o", ProjectKey: "p", BranchName: "b",
	}); err == nil {
		t.Error("expected error on 403, got nil")
	}
}

type submitCapture struct {
	contentType string
	fields      map[string]string
	fileSize    int
}

func newSubmitServer(t *testing.T) (*httptest.Server, *submitCapture) {
	t.Helper()
	cap := &submitCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ce/submit" {
			w.WriteHeader(404)
			return
		}
		cap.contentType = r.Header.Get("Content-Type")
		cap.fields, cap.fileSize = parseMultipart(t, cap.contentType, r.Body)
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]string{"taskId": "AX-123"})
	}))
	return srv, cap
}

func parseMultipart(t *testing.T, ct string, body io.Reader) (map[string]string, int) {
	t.Helper()
	mediaType, params, _ := mime.ParseMediaType(ct)
	if mediaType != "multipart/form-data" {
		t.Errorf("expected multipart/form-data, got %s", mediaType)
	}
	fields := make(map[string]string)
	var fileSize int
	mr := multipart.NewReader(body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading part: %v", err)
		}
		data, _ := io.ReadAll(part)
		if part.FormName() == "report" {
			fileSize = len(data)
		} else {
			fields[part.FormName()] = string(data)
		}
		part.Close()
	}
	return fields, fileSize
}

func TestSubmitReport(t *testing.T) {
	srv, cap := newSubmitServer(t)
	defer srv.Close()

	cfg := SubmitConfig{
		CloudURL:   srv.URL + "/",
		ProjectKey: "my-proj",
		OrgKey:     "my-org",
		BranchName: "main",
	}

	result, err := SubmitReport(context.Background(), srv.Client(), cfg, []byte("fake-zip-data"))
	if err != nil {
		t.Fatalf("SubmitReport: %v", err)
	}
	if result.TaskID != "AX-123" {
		t.Errorf("expected taskId AX-123, got %s", result.TaskID)
	}
	if cap.fileSize != len("fake-zip-data") {
		t.Errorf("expected file size %d, got %d", len("fake-zip-data"), cap.fileSize)
	}
	if cap.fields["projectKey"] != "my-proj" {
		t.Errorf("expected projectKey my-proj, got %s", cap.fields["projectKey"])
	}
	if cap.fields["organization"] != "my-org" {
		t.Errorf("expected organization my-org, got %s", cap.fields["organization"])
	}
	if !strings.Contains(cap.fields["properties"], "sonar.projectKey=my-proj") {
		t.Errorf("expected properties to contain projectKey, got %s", cap.fields["properties"])
	}
}

func TestSubmitReportHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"errors":[{"msg":"bad request"}]}`)
	}))
	defer srv.Close()

	cfg := SubmitConfig{CloudURL: srv.URL + "/", ProjectKey: "p", OrgKey: "o"}
	_, err := SubmitReport(context.Background(), srv.Client(), cfg, []byte("zip"))
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected error to contain 400, got: %v", err)
	}
}

func TestPollCETaskSuccess(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var status string
		if callCount < 3 {
			status = "PENDING"
		} else {
			status = "SUCCESS"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"task": map[string]string{"id": "AX-123", "status": status},
		})
	}))
	defer srv.Close()

	logger := slog.Default()
	err := PollCETask(context.Background(), srv.Client(), srv.URL+"/", "AX-123", logger)
	if err != nil {
		t.Fatalf("PollCETask: %v", err)
	}
}

func TestPollCETaskFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"task": map[string]string{"id": "AX-123", "status": "FAILED", "errorMessage": "analysis error"},
		})
	}))
	defer srv.Close()

	logger := slog.Default()
	err := PollCETask(context.Background(), srv.Client(), srv.URL+"/", "AX-123", logger)
	if err == nil {
		t.Fatal("expected error for failed task")
	}
	if !strings.Contains(err.Error(), "analysis error") {
		t.Errorf("expected error message, got: %v", err)
	}
}

func TestParseTaskID(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		want  string
	}{
		{"simple", `{"taskId":"AX-1"}`, "AX-1"},
		{"nested", `{"task":{"id":"AX-2","status":"PENDING"}}`, "AX-2"},
		{"empty", `{}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTaskID([]byte(tc.body))
			if got != tc.want {
				t.Errorf("parseTaskID(%s): got %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestExtractJSONField(t *testing.T) {
	body := `{"task":{"id":"AX-1","status":"SUCCESS","errorMessage":"oops"}}`
	if got := extractJSONField([]byte(body), "task", "status"); got != "SUCCESS" {
		t.Errorf("expected SUCCESS, got %q", got)
	}
	if got := extractJSONField([]byte(body), "task", "errorMessage"); got != "oops" {
		t.Errorf("expected oops, got %q", got)
	}
	if got := extractJSONField([]byte(body), "task", "missing"); got != "" {
		t.Errorf("expected empty for missing field, got %q", got)
	}
}
