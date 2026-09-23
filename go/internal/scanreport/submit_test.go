// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package scanreport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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

// withFastCEPoll collapses the whole backoff ladder for the duration of a
// test (the production ladder runs 1s -> 10s, which would otherwise make
// these tests take tens of seconds for no benefit). Setting only the max is
// enough: initialPollInterval clamps the initial wait down to it.
func withFastCEPoll(t *testing.T) {
	t.Helper()
	origMax := CEPollMaxInterval
	CEPollMaxInterval = time.Millisecond
	t.Cleanup(func() { CEPollMaxInterval = origMax })
}

func TestPollCETaskSuccess(t *testing.T) {
	withFastCEPoll(t)
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
	withFastCEPoll(t)
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

// withPollLadder pins the backoff ladder's three tunables for one test.
func withPollLadder(t *testing.T, initial, maxInterval time.Duration, factor float64) {
	t.Helper()
	oi, om, of := CEPollInitialInterval, CEPollMaxInterval, CEPollBackoffFactor
	CEPollInitialInterval, CEPollMaxInterval, CEPollBackoffFactor = initial, maxInterval, factor
	t.Cleanup(func() { CEPollInitialInterval, CEPollMaxInterval, CEPollBackoffFactor = oi, om, of })
}

// TestPollLadderRungs walks the ladder the production defaults produce and
// asserts it saturates at the cap rather than growing without bound.
func TestPollLadderRungs(t *testing.T) {
	withPollLadder(t, 1*time.Second, 10*time.Second, 2.0)

	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		10 * time.Second, // 16s would overshoot: saturates at the cap
		10 * time.Second,
	}
	got := make([]time.Duration, 0, len(want))
	d := initialPollInterval()
	for range want {
		got = append(got, d)
		d = nextPollInterval(d)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rung %d = %v, want %v (full ladder: %v)", i, got[i], want[i], got)
		}
	}
}

// TestPollLadderDegenerateTunables pins the guards that keep a
// mis-configured ladder from busy-looping: a zero/negative interval is
// floored, and a factor of <= 1 holds the wait flat instead of shrinking
// it toward zero.
func TestPollLadderDegenerateTunables(t *testing.T) {
	t.Run("zero initial is floored", func(t *testing.T) {
		withPollLadder(t, 0, 10*time.Second, 2.0)
		if got := initialPollInterval(); got != minPollInterval {
			t.Errorf("initialPollInterval() = %v, want %v", got, minPollInterval)
		}
	})
	t.Run("negative max floors the whole ladder", func(t *testing.T) {
		withPollLadder(t, 1*time.Second, -5*time.Second, 2.0)
		d := initialPollInterval()
		if d != minPollInterval {
			t.Errorf("initialPollInterval() = %v, want %v", d, minPollInterval)
		}
		if got := nextPollInterval(d); got != minPollInterval {
			t.Errorf("nextPollInterval(%v) = %v, want %v", d, got, minPollInterval)
		}
	})
	t.Run("factor of 1 holds the interval flat", func(t *testing.T) {
		withPollLadder(t, 2*time.Second, 10*time.Second, 1.0)
		if got := nextPollInterval(2 * time.Second); got != 2*time.Second {
			t.Errorf("nextPollInterval = %v, want it held flat at 2s", got)
		}
	})
	t.Run("initial above max clamps down to max", func(t *testing.T) {
		withPollLadder(t, 30*time.Second, 10*time.Second, 2.0)
		if got := initialPollInterval(); got != 10*time.Second {
			t.Errorf("initialPollInterval() = %v, want 10s", got)
		}
	})
}

// TestAveragePollInterval covers the figure internal/migrate's
// pollBoundConcurrency sizes the CE gate from. It is elapsed-at-discovery
// divided by polls spent, NOT the mean of the ladder's rungs.
func TestAveragePollInterval(t *testing.T) {
	withPollLadder(t, 1*time.Second, 10*time.Second, 2.0)

	tests := []struct {
		name string
		task time.Duration
		want time.Duration
	}{
		// Never sleeps: the immediate first poll already finds it done.
		{"zero duration returns the first rung", 0, 1 * time.Second},
		{"negative duration returns the first rung", -1 * time.Second, 1 * time.Second},
		// Discovered at t=1s on poll 2 (immediate, +1s).
		{"sub-second task", 500 * time.Millisecond, 500 * time.Millisecond},
		// Discovered at t=3s on poll 3 (immediate, +1s, +2s).
		{"measured median CE duration", 1570 * time.Millisecond, 1 * time.Second},
		// Same three polls cover anything up to 3s.
		{"measured p90 CE duration", 2950 * time.Millisecond, 1 * time.Second},
		// t=0,1,3,7,15,25,35,45,55,65,75 -> discovered at 75s over 11 polls.
		{"slow outlier settles near the cap", 73 * time.Second, 75 * time.Second / 11},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AveragePollInterval(tt.task); got != tt.want {
				t.Errorf("AveragePollInterval(%v) = %v, want %v", tt.task, got, tt.want)
			}
		})
	}
}

// TestPollCETaskBacksOff proves PollCETask actually sleeps the growing
// ladder between polls rather than a flat interval. With a 10ms initial
// and a 40ms cap, reaching the 4th poll must take at least 10+20+40=70ms.
func TestPollCETaskBacksOff(t *testing.T) {
	withPollLadder(t, 10*time.Millisecond, 40*time.Millisecond, 2.0)

	const terminalOnPoll = 4
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		status := "PENDING"
		if polls >= terminalOnPoll {
			status = "SUCCESS"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"task": map[string]string{"id": "AX-123", "status": status},
		})
	}))
	defer srv.Close()

	start := time.Now()
	if err := PollCETask(context.Background(), srv.Client(), srv.URL+"/", "AX-123", slog.Default()); err != nil {
		t.Fatalf("PollCETask: %v", err)
	}
	elapsed := time.Since(start)

	if polls != terminalOnPoll {
		t.Errorf("polled %d times, want %d", polls, terminalOnPoll)
	}
	// Lower bound only: a flat 10ms cadence would finish in ~30ms.
	if want := 70 * time.Millisecond; elapsed < want {
		t.Errorf("elapsed %v, want at least %v — the ladder does not appear to be backing off", elapsed, want)
	}
}

// TestPollCETaskHonorsCanceledContext guards the ctx check on the very
// first iteration, which runs before any sleep.
func TestPollCETaskHonorsCanceledContext(t *testing.T) {
	withFastCEPoll(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("PollCETask issued a request with an already-canceled context")
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := PollCETask(ctx, srv.Client(), srv.URL+"/", "AX-123", slog.Default()); !errors.Is(err, context.Canceled) {
		t.Errorf("PollCETask = %v, want context.Canceled", err)
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
