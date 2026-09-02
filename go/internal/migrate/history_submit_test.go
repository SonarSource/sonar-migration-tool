// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
	"google.golang.org/protobuf/proto"
)

// --- shared fixtures for the migrate side of #554 -------------------------
//
// Everything in this file drives history.go against local httptest servers
// only. The "target organization" profile key below is deliberately a value
// that appears nowhere in history.go: the PoC originally shipped a hardcoded
// profile key from the developer's own organization, so every assertion that
// this exact string reaches the submitted report is a regression guard for
// that bug.

const (
	histCloudKey   = "cloud-proj1"
	histOrgProfKey = "target-org-js-profile-key"
	histFileName   = "__history_snapshot__.js"
)

// histPlaceholder is the resolved placeholder a caller of
// submitHistoricalSnapshot would have obtained from the TARGET organization.
func histPlaceholder() historyPlaceholder {
	return historyPlaceholder{
		Language: "js",
		Ext:      "js",
		QProfile: scanreport.QProfileInfo{Key: histOrgProfKey, Name: "Sonar way", Language: "js"},
	}
}

func histBranchContext() branchImportContext {
	return branchImportContext{
		CloudKey:  histCloudKey,
		OrgKey:    testCloudOrg,
		ServerURL: testServerURL,
		ServerKey: projMain,
	}
}

// histRecord builds one getProjectAnalysisHistory extract record in exactly
// the shape internal/extract/tasks_history.go writes.
func histRecord(project, branch, date, version string, ncloc string) map[string]any {
	rec := map[string]any{
		"projectKey":     project,
		"branch":         branch,
		"date":           date,
		"projectVersion": version,
		"serverUrl":      testServerURL,
	}
	if ncloc != "" {
		rec["measures"] = []map[string]any{{"metric": "ncloc", "value": ncloc}}
	}
	return rec
}

// histSeed writes history records into the extract run the default test
// Mapping points at, so loadExtractedAnalysisHistory can find them.
func histSeed(e *Executor, records []map[string]any) {
	writeJSONL(filepath.Join(e.ExportDir, extractRun, "getProjectAnalysisHistory"), records)
}

// histLogBuf swaps in a buffer-backed logger at the given level and returns
// the buffer. Matches the log-capture convention used elsewhere in this
// package (gate_assignment_test.go, alm_preflight_test.go).
func histLogBuf(e *Executor, level slog.Level) *bytes.Buffer {
	var buf bytes.Buffer
	e.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level}))
	return &buf
}

// histRecorder counts the requests a mux receives, keyed by path.
type histRecorder struct {
	mu     sync.Mutex
	hits   map[string]int
	report []byte // last multipart "report" upload
	form   map[string][]string
}

func newHistRecorder() *histRecorder {
	return &histRecorder{hits: map[string]int{}}
}

func (r *histRecorder) note(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits[path]++
}

func (r *histRecorder) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[path]
}

func (r *histRecorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, v := range r.hits {
		n += v
	}
	return n
}

func (r *histRecorder) paths() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.hits))
	for k, v := range r.hits {
		out[k] = v
	}
	return out
}

// captureSubmit records the multipart fields and the uploaded report ZIP of a
// POST /api/ce/submit request.
func (r *histRecorder) captureSubmit(t *testing.T, req *http.Request) {
	t.Helper()
	if err := req.ParseMultipartForm(10 << 20); err != nil {
		t.Errorf("parsing ce/submit multipart form: %v", err)
		return
	}
	files := req.MultipartForm.File["report"]
	if len(files) != 1 {
		t.Errorf("expected exactly 1 uploaded report part, got %d", len(files))
		return
	}
	f, err := files[0].Open()
	if err != nil {
		t.Errorf("opening uploaded report: %v", err)
		return
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Errorf("reading uploaded report: %v", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.report = b
	r.form = map[string][]string{}
	for k, v := range req.MultipartForm.Value {
		r.form[k] = append([]string(nil), v...)
	}
}

func (r *histRecorder) reportBytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.report
}

func (r *histRecorder) formValues(key string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.form[key]
}

func (r *histRecorder) formValue(key string) string {
	v := r.formValues(key)
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// histProfileMux returns a mux whose target organization advertises exactly
// one quality profile: a JS one keyed histOrgProfKey.
func histProfileMux(rec *histRecorder) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		rec.note(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]any{
				{"key": histOrgProfKey, "name": "Sonar way", "language": "js", "isBuiltIn": true},
			},
		})
	})
	return mux
}

// --- zip inspection helpers ----------------------------------------------

func histZipEntry(t *testing.T, zipBytes []byte, name string) ([]byte, bool) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("reading submitted report zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening zip entry %s: %v", name, err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading zip entry %s: %v", name, err)
		}
		return b, true
	}
	return nil, false
}

func histZipNames(t *testing.T, zipBytes []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("reading submitted report zip: %v", err)
	}
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func histMetadata(t *testing.T, zipBytes []byte) *pb.Metadata {
	t.Helper()
	raw, ok := histZipEntry(t, zipBytes, "metadata.pb")
	if !ok {
		t.Fatalf("submitted report has no metadata.pb, entries: %v", histZipNames(t, zipBytes))
	}
	var md pb.Metadata
	if err := proto.Unmarshal(raw, &md); err != nil {
		t.Fatalf("unmarshalling metadata.pb: %v", err)
	}
	return &md
}

// histFileComponent finds the single FILE component in the submitted report.
func histFileComponent(t *testing.T, zipBytes []byte) *pb.Component {
	t.Helper()
	var found []*pb.Component
	for _, name := range histZipNames(t, zipBytes) {
		if !strings.HasPrefix(name, "component-") {
			continue
		}
		raw, _ := histZipEntry(t, zipBytes, name)
		var c pb.Component
		if err := proto.Unmarshal(raw, &c); err != nil {
			t.Fatalf("unmarshalling %s: %v", name, err)
		}
		if c.GetType() == pb.Component_FILE {
			found = append(found, &c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly 1 FILE component in the submitted report, got %d", len(found))
	}
	return found[0]
}

// histDecodeMeasures reads a measures-<ref>.pb entry, which the packager
// writes as a varint-length-delimited stream of pb.Measure messages.
func histDecodeMeasures(t *testing.T, raw []byte) []*pb.Measure {
	t.Helper()
	var out []*pb.Measure
	for len(raw) > 0 {
		n, used := binary.Uvarint(raw)
		if used <= 0 {
			t.Fatalf("corrupt varint length prefix in measures stream")
		}
		raw = raw[used:]
		if uint64(len(raw)) < n {
			t.Fatalf("measures stream truncated: want %d bytes, have %d", n, len(raw))
		}
		var m pb.Measure
		if err := proto.Unmarshal(raw[:n], &m); err != nil {
			t.Fatalf("unmarshalling measure: %v", err)
		}
		out = append(out, &m)
		raw = raw[n:]
	}
	return out
}

// --- loadExtractedAnalysisHistory ----------------------------------------

// TestLoadExtractedAnalysisHistoryOrdersAndFilters pins the three things the
// loader is responsible for: chronological order (the CE rejects an analysis
// dated before the branch's most recent one, so submitting newest-first would
// break every point after the first), scope filtering, and dropping records
// whose date can't be parsed rather than submitting them with a zero date
// (epoch 1970).
func TestLoadExtractedAnalysisHistoryOrdersAndFilters(t *testing.T) {
	e := newProjectDataExecutor(t, t.TempDir())
	// Deliberately written newest-first and interleaved with out-of-scope and
	// undated records.
	histSeed(e, []map[string]any{
		histRecord(projMain, branchMain, "2023-06-01T00:00:00Z", "3.0", "300"),
		histRecord(projMain, branchMain, "2022-01-15T00:00:00Z", "1.0", "100"),
		// The non-RFC3339 spelling parseISODate also accepts.
		histRecord(projMain, branchMain, "2023-03-05T12:00:00+0000", "2.0", "200"),
		histRecord(projMain, branchMain, "", "no-date", "1"),
		histRecord(projMain, branchMain, "not-a-date", "bad-date", "1"),
		histRecord(projMain, branchDev, "2021-01-01T00:00:00Z", "dev", "1"),
		histRecord("proj2", branchMain, "2021-02-01T00:00:00Z", "other-project", "1"),
	})

	got := loadExtractedAnalysisHistory(e, testServerURL, projMain, branchMain)

	if len(got) != 3 {
		t.Fatalf("expected 3 usable history points (undated + out-of-scope records dropped), got %d: %+v", len(got), got)
	}

	wantDates := []time.Time{
		time.Date(2022, 1, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2023, 3, 5, 12, 0, 0, 0, time.UTC),
		time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	wantVersions := []string{"1.0", "2.0", "3.0"}
	wantNcloc := []string{"100", "200", "300"}

	for i, snap := range got {
		if !snap.Date.Equal(wantDates[i]) {
			t.Errorf("point %d: date = %s, want %s (history must be sorted oldest to newest)",
				i, snap.Date.Format(time.RFC3339), wantDates[i].Format(time.RFC3339))
		}
		if snap.ProjectVersion != wantVersions[i] {
			t.Errorf("point %d: projectVersion = %q, want %q", i, snap.ProjectVersion, wantVersions[i])
		}
		if len(snap.Measures) != 1 {
			t.Fatalf("point %d: expected 1 measure, got %d", i, len(snap.Measures))
		}
		m := snap.Measures[0]
		if m.MetricKey != "ncloc" || m.Value != wantNcloc[i] {
			t.Errorf("point %d: measure = %s=%s, want ncloc=%s", i, m.MetricKey, m.Value, wantNcloc[i])
		}
		// The loader attributes measures to the SOURCE project key;
		// submitHistoricalSnapshot retargets them onto its placeholder file.
		if m.Component != projMain {
			t.Errorf("point %d: measure component = %q, want the source project key %q", i, m.Component, projMain)
		}
	}
}

// TestLoadExtractedAnalysisHistoryNoHistory covers the case that keeps #554 a
// true no-op for everyone who didn't run extract with --migrate_history: the
// task directory simply isn't there.
func TestLoadExtractedAnalysisHistoryNoHistory(t *testing.T) {
	t.Run("task directory absent", func(t *testing.T) {
		e := newProjectDataExecutor(t, t.TempDir())
		if got := loadExtractedAnalysisHistory(e, testServerURL, projMain, branchMain); got != nil {
			t.Errorf("expected nil when no history was extracted, got %+v", got)
		}
	})

	t.Run("history exists only for another server", func(t *testing.T) {
		e := newProjectDataExecutor(t, t.TempDir())
		histSeed(e, []map[string]any{
			histRecord(projMain, branchMain, "2023-06-01T00:00:00Z", "3.0", "300"),
		})
		if got := loadExtractedAnalysisHistory(e, "https://other.test/", projMain, branchMain); got != nil {
			t.Errorf("expected nil for a server URL outside the extract mapping, got %+v", got)
		}
	})

	t.Run("every record is undated", func(t *testing.T) {
		e := newProjectDataExecutor(t, t.TempDir())
		histSeed(e, []map[string]any{
			histRecord(projMain, branchMain, "", "x", "1"),
			histRecord(projMain, branchMain, "17 March 2023", "y", "1"),
		})
		if got := loadExtractedAnalysisHistory(e, testServerURL, projMain, branchMain); got != nil {
			t.Errorf("expected nil when no record carries a parseable date, got %+v", got)
		}
	})
}

// TestExtractHistoryMeasuresCorruptRecord pins that a corrupt extract record
// degrades to "this point has no measures" rather than panicking or
// half-parsing.
func TestExtractHistoryMeasuresCorruptRecord(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"record is not JSON at all", `not json at all`},
		{"record is a JSON array, not an object", `[{"metric":"ncloc","value":"1"}]`},
		{"measures is an object, not an array", `{"measures":{"ncloc":"1"}}`},
		{"measures is a string", `{"measures":"oops"}`},
		{"measures entries are not objects", `{"measures":[1,2]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractHistoryMeasures(json.RawMessage(tc.data), "k"); got != nil {
				t.Errorf("expected nil measures for %s, got %+v", tc.name, got)
			}
		})
	}

	// Guard the one shape that is NOT an error: a null measures array
	// unmarshals fine and must yield an empty (non-nil) slice.
	if got := extractHistoryMeasures(json.RawMessage(`{"measures":null}`), "k"); len(got) != 0 {
		t.Errorf("expected no measures for a null measures field, got %+v", got)
	}
}

// --- resolveHistoryPlaceholderProfile ------------------------------------

// TestResolveHistoryPlaceholderProfileFromTargetOrg pins that the placeholder
// profile key comes from the TARGET organization's live
// /api/qualityprofiles/search rather than from any constant in the code —
// the bug this PoC first shipped with ("Quality profiles with following keys
// don't exist in organization").
func TestResolveHistoryPlaceholderProfileFromTargetOrg(t *testing.T) {
	rec := newHistRecorder()
	mux := histProfileMux(rec)
	addDefaultCloudHandler(mux)
	e := newCustomCloudTest(t, mux)

	got, ok := resolveHistoryPlaceholderProfile(context.Background(), e, testCloudOrg)
	if !ok {
		t.Fatal("expected a usable placeholder when the org advertises a JS profile")
	}
	if rec.count("/api/qualityprofiles/search") != 1 {
		t.Errorf("expected the target org's profile search to be queried once, got %d", rec.count("/api/qualityprofiles/search"))
	}
	if got.Language != "js" || got.Ext != "js" {
		t.Errorf("placeholder language/ext = %q/%q, want js/js", got.Language, got.Ext)
	}
	if got.QProfile.Key != histOrgProfKey {
		t.Errorf("placeholder profile key = %q, want the key the target org returned (%q)", got.QProfile.Key, histOrgProfKey)
	}
}

// TestResolveHistoryPlaceholderProfileDegradesToSkip pins that a target org
// with no usable profiles — or one whose profile API fails — reports "not ok"
// so the caller skips history, rather than inventing a profile key the CE
// would reject.
func TestResolveHistoryPlaceholderProfileDegradesToSkip(t *testing.T) {
	t.Run("org returns no profiles", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"profiles": []map[string]any{}})
		})
		addDefaultCloudHandler(mux)
		e := newCustomCloudTest(t, mux)

		got, ok := resolveHistoryPlaceholderProfile(context.Background(), e, testCloudOrg)
		if ok {
			t.Fatalf("expected not-ok when the org has no profiles, got %+v", got)
		}
		if got != (historyPlaceholder{}) {
			t.Errorf("expected a zero placeholder, got %+v", got)
		}
	})

	t.Run("profile API fails", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"errors":[{"msg":"boom"}]}`, http.StatusInternalServerError)
		})
		addDefaultCloudHandler(mux)
		e := newCustomCloudTest(t, mux)
		histLogBuf(e, slog.LevelWarn)

		if _, ok := resolveHistoryPlaceholderProfile(context.Background(), e, testCloudOrg); ok {
			t.Error("expected not-ok when the target org's profile API fails")
		}
	})

	t.Run("no cloud client configured", func(t *testing.T) {
		e := newProjectDataExecutor(t, t.TempDir())
		if _, ok := resolveHistoryPlaceholderProfile(context.Background(), e, testCloudOrg); ok {
			t.Error("expected not-ok when no Cloud client is wired")
		}
	})
}

// --- migrateBranchHistory ------------------------------------------------

// TestMigrateBranchHistoryNoOps pins the guards that keep #554 invisible to
// everyone who didn't opt in: with the feature off, on a non-main branch, or
// with no extracted history, migrateBranchHistory must not touch the network
// at all — not even to look up quality profiles.
func TestMigrateBranchHistoryNoOps(t *testing.T) {
	tests := []struct {
		name           string
		migrateHistory bool
		branch         branchInfo
		seed           bool
	}{
		{
			name:           "feature not enabled",
			migrateHistory: false,
			branch:         branchInfo{Name: branchMain, IsMain: true},
			seed:           true,
		},
		{
			name:           "non-main branch is out of scope for the PoC",
			migrateHistory: true,
			branch:         branchInfo{Name: branchDev, IsMain: false},
			seed:           true,
		},
		{
			name:           "no history was extracted for this branch",
			migrateHistory: true,
			branch:         branchInfo{Name: branchMain, IsMain: true},
			seed:           false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := newHistRecorder()
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				rec.note(r.URL.Path)
				_ = json.NewEncoder(w).Encode(map[string]any{})
			})
			e := newCustomCloudTest(t, mux)
			e.MigrateHistory = tc.migrateHistory
			if tc.seed {
				// History exists on disk for main AND develop, so the only
				// thing that can stop a request is the guard under test.
				histSeed(e, []map[string]any{
					histRecord(projMain, branchMain, "2022-01-15T00:00:00Z", "1.0", "100"),
					histRecord(projMain, branchDev, "2022-01-15T00:00:00Z", "1.0", "100"),
				})
			}

			migrateBranchHistory(context.Background(), e, histBranchContext(), tc.branch, branchMain)

			if n := rec.total(); n != 0 {
				t.Errorf("expected zero HTTP requests, got %d: %v", n, rec.paths())
			}
		})
	}
}

// TestMigrateBranchHistorySkipsWhenOrgHasNoProfile pins the "skip, don't
// submit" behaviour when the target organization has no quality profile the
// placeholder file could legally declare: submitting anyway would have the CE
// reject the whole report.
func TestMigrateBranchHistorySkipsWhenOrgHasNoProfile(t *testing.T) {
	rec := newHistRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/qualityprofiles/search", func(w http.ResponseWriter, r *http.Request) {
		rec.note(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"profiles": []map[string]any{}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rec.note(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	e := newCustomCloudTest(t, mux)
	e.MigrateHistory = true
	buf := histLogBuf(e, slog.LevelWarn)
	histSeed(e, []map[string]any{
		histRecord(projMain, branchMain, "2022-01-15T00:00:00Z", "1.0", "100"),
	})

	migrateBranchHistory(context.Background(), e,
		histBranchContext(), branchInfo{Name: branchMain, IsMain: true}, branchMain)

	if rec.count("/api/qualityprofiles/search") != 1 {
		t.Errorf("expected the target org's profiles to be looked up once, got %d", rec.count("/api/qualityprofiles/search"))
	}
	if n := rec.count("/api/ce/submit"); n != 0 {
		t.Errorf("expected no report submission when no profile is usable, got %d", n)
	}
	if !strings.Contains(buf.String(), "no usable quality profile") {
		t.Errorf("expected a warning explaining the skip, got: %s", buf.String())
	}
}

// TestMigrateBranchHistoryStopsAtFirstFailure pins two things at once:
//
//   - history is replayed OLDEST first (the CE refuses an analysis dated
//     before the branch's most recent one, so any other order breaks after
//     the first point), and
//   - a failing point stops the branch rather than skipping ahead to the next
//     one, which would just hammer the CE with N more doomed submissions.
//
// It also asserts, end to end and for free, that the qprofile key inside the
// submitted report is the one the TARGET organization returned — the #554
// regression — since the failing handler still receives the full upload.
func TestMigrateBranchHistoryStopsAtFirstFailure(t *testing.T) {
	rec := newHistRecorder()
	mux := histProfileMux(rec)
	mux.HandleFunc("POST /api/ce/submit", func(w http.ResponseWriter, r *http.Request) {
		rec.note(r.URL.Path)
		rec.captureSubmit(t, r)
		http.Error(w, `{"errors":[{"msg":"nope"}]}`, http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /api/ce/task", func(w http.ResponseWriter, r *http.Request) {
		rec.note(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"status": "SUCCESS"}})
	})
	addDefaultCloudHandler(mux)

	e := newCustomCloudTest(t, mux)
	e.MigrateHistory = true
	buf := histLogBuf(e, slog.LevelInfo)
	// Written newest-first on disk on purpose.
	histSeed(e, []map[string]any{
		histRecord(projMain, branchMain, "2023-06-01T00:00:00Z", "3.0", "300"),
		histRecord(projMain, branchMain, "2022-01-15T00:00:00Z", "1.0", "100"),
	})

	migrateBranchHistory(context.Background(), e,
		histBranchContext(), branchInfo{Name: branchMain, IsMain: true}, branchMain)

	if n := rec.count("/api/ce/submit"); n != 1 {
		t.Fatalf("expected exactly 1 submission before giving up on the branch, got %d", n)
	}
	if n := rec.count("/api/ce/task"); n != 0 {
		t.Errorf("expected no CE polling after a failed submission, got %d", n)
	}

	md := histMetadata(t, rec.reportBytes())
	oldest := time.Date(2022, 1, 15, 0, 0, 0, 0, time.UTC)
	if md.GetAnalysisDate() != oldest.UnixMilli() {
		t.Errorf("first submitted point analysisDate = %d, want the OLDEST point %d",
			md.GetAnalysisDate(), oldest.UnixMilli())
	}
	if md.GetProjectVersion() != "1.0" {
		t.Errorf("first submitted point projectVersion = %q, want the oldest point's %q", md.GetProjectVersion(), "1.0")
	}
	qp, ok := md.GetQprofilesPerLanguage()["js"]
	if !ok {
		t.Fatalf("expected a js qprofile in the submitted metadata, got %v", md.GetQprofilesPerLanguage())
	}
	if qp.GetKey() != histOrgProfKey {
		t.Errorf("submitted qprofile key = %q, want the key resolved from the target org (%q)", qp.GetKey(), histOrgProfKey)
	}

	logged := buf.String()
	if !strings.Contains(logged, "stopped early") {
		t.Errorf("expected a 'stopped early' warning, got: %s", logged)
	}
	if !strings.Contains(logged, "2022-01-15T00:00:00Z") {
		t.Errorf("expected the failing point's date in the warning, got: %s", logged)
	}
	if !strings.Contains(logged, "points=2") {
		t.Errorf("expected the info log to report 2 history points, got: %s", logged)
	}
}

// --- submitHistoricalSnapshot --------------------------------------------

// TestSubmitHistoricalSnapshotBackdatedReport is the end-to-end assertion for
// the whole point of #554: the report that reaches the CE must be stamped
// with the SOURCE analysis date, not "now", and must declare the target
// organization's own quality profile key.
//
// This test costs ~5s: scanreport.PollCETask sleeps 5 real seconds before its
// first poll and has no injectable clock. It is deliberately the only
// successful submission in this file.
func TestSubmitHistoricalSnapshotBackdatedReport(t *testing.T) {
	rec := newHistRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/ce/submit", func(w http.ResponseWriter, r *http.Request) {
		rec.note(r.URL.Path)
		rec.captureSubmit(t, r)
		_ = json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-hist-1"})
	})
	mux.HandleFunc("GET /api/ce/task", func(w http.ResponseWriter, r *http.Request) {
		rec.note(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"status": "SUCCESS"}})
	})
	addDefaultCloudHandler(mux)

	e := newCustomCloudTest(t, mux)
	buf := histLogBuf(e, slog.LevelInfo)

	snapDate := time.Date(2022, 3, 1, 9, 30, 0, 0, time.UTC)
	snap := historySnapshot{
		Date:           snapDate,
		ProjectVersion: "2.5",
		// Component is the SOURCE project key, as the loader produces it:
		// submitHistoricalSnapshot has to retarget it onto the placeholder
		// file or BuildMeasures silently drops the measure.
		Measures: []scanreport.MeasureInput{
			{Component: projMain, MetricKey: "ncloc", Value: "500"},
		},
	}

	err := submitHistoricalSnapshot(context.Background(), e, histBranchContext(), branchMain, snap, histPlaceholder())
	if err != nil {
		t.Fatalf("submitHistoricalSnapshot: %v", err)
	}
	if n := rec.count("/api/ce/submit"); n != 1 {
		t.Fatalf("expected exactly 1 submission, got %d", n)
	}
	if n := rec.count("/api/ce/task"); n < 1 {
		t.Errorf("expected the CE task to be polled, got %d polls", n)
	}

	// Main-branch-only by design: declaring the point as a named LONG branch
	// makes the CE reject the report.
	if chars := rec.formValues("characteristic"); len(chars) != 0 {
		t.Errorf("expected no branch characteristic on a main-branch history point, got %v", chars)
	}
	if got := rec.formValue("projectKey"); got != histCloudKey {
		t.Errorf("submitted projectKey = %q, want %q", got, histCloudKey)
	}
	if got := rec.formValue("organization"); got != testCloudOrg {
		t.Errorf("submitted organization = %q, want %q", got, testCloudOrg)
	}

	zipBytes := rec.reportBytes()
	md := histMetadata(t, zipBytes)

	if md.GetAnalysisDate() != snapDate.UnixMilli() {
		t.Errorf("analysisDate = %d, want the backdated snapshot date %d (a report stamped with 'now' defeats the whole feature)",
			md.GetAnalysisDate(), snapDate.UnixMilli())
	}
	if md.GetProjectVersion() != "2.5" {
		t.Errorf("projectVersion = %q, want %q", md.GetProjectVersion(), "2.5")
	}
	if md.GetBranchName() != branchMain {
		t.Errorf("branchName = %q, want %q", md.GetBranchName(), branchMain)
	}
	if md.GetBranchType() != pb.Metadata_BRANCH {
		t.Errorf("branchType = %v, want BRANCH", md.GetBranchType())
	}
	if md.GetProjectKey() != histCloudKey {
		t.Errorf("metadata projectKey = %q, want %q", md.GetProjectKey(), histCloudKey)
	}
	if md.GetOrganizationKey() != testCloudOrg {
		t.Errorf("metadata organizationKey = %q, want %q", md.GetOrganizationKey(), testCloudOrg)
	}
	qp, ok := md.GetQprofilesPerLanguage()["js"]
	if !ok {
		t.Fatalf("expected a js qprofile in metadata, got %v", md.GetQprofilesPerLanguage())
	}
	if qp.GetKey() != histOrgProfKey {
		t.Errorf("metadata qprofile key = %q, want the target organization's key %q", qp.GetKey(), histOrgProfKey)
	}
	if got := md.GetAnalyzedIndexedFileCountPerType()["js"]; got != 1 {
		t.Errorf("expected 1 indexed file for language js, got %d", got)
	}

	// The placeholder file must be named and typed from the resolved
	// language: SonarQube Cloud derives a file's language from its extension,
	// and a mismatch against the declared profile is a hard CE rejection.
	fc := histFileComponent(t, zipBytes)
	if fc.GetProjectRelativePath() != histFileName {
		t.Errorf("placeholder path = %q, want %q", fc.GetProjectRelativePath(), histFileName)
	}
	if fc.GetLanguage() != "js" {
		t.Errorf("placeholder language = %q, want %q", fc.GetLanguage(), "js")
	}
	if fc.GetLines() != 1 {
		t.Errorf("placeholder lines = %d, want 1", fc.GetLines())
	}

	// The measures must land on the placeholder FILE ref. Attaching them to
	// the PROJECT ref instead is the shape the CE rejected during the PoC's
	// live verification.
	raw, found := histZipEntry(t, zipBytes, "measures-"+strconv.Itoa(int(fc.GetRef()))+".pb")
	if !found {
		t.Fatalf("expected measures on the placeholder file ref %d, zip entries: %v", fc.GetRef(), histZipNames(t, zipBytes))
	}
	measures := histDecodeMeasures(t, raw)
	if len(measures) != 1 {
		t.Fatalf("expected 1 measure on the placeholder file, got %d", len(measures))
	}
	if measures[0].GetMetricKey() != "ncloc" {
		t.Errorf("measure metric = %q, want ncloc", measures[0].GetMetricKey())
	}
	if v := measures[0].GetIntValue().GetValue(); v != 500 {
		t.Errorf("measure value = %d, want 500", v)
	}

	// Changesets are backdated too, so the CE dates the placeholder's single
	// line at the snapshot instead of the migration run.
	if _, found := histZipEntry(t, zipBytes, "changesets-"+strconv.Itoa(int(fc.GetRef()))+".pb"); !found {
		t.Errorf("expected changesets for the placeholder file ref %d, zip entries: %v", fc.GetRef(), histZipNames(t, zipBytes))
	}
	// No issues means no rules need activating in the target org.
	for _, name := range histZipNames(t, zipBytes) {
		if name == "activerules.pb" {
			t.Error("a history point carries no issues, so it must not declare active rules")
		}
	}

	logged := buf.String()
	if !strings.Contains(logged, "historical analysis migrated") {
		t.Errorf("expected a success log line, got: %s", logged)
	}
	if !strings.Contains(logged, "AX-hist-1") {
		t.Errorf("expected the CE task id in the success log, got: %s", logged)
	}
}

// TestSubmitHistoricalSnapshotSubmitFailure pins that a rejected upload
// surfaces as an error naming the submit step — and that no CE polling is
// attempted for a task that was never created.
func TestSubmitHistoricalSnapshotSubmitFailure(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "CE rejects the upload",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `{"errors":[{"msg":"Insufficient privileges"}]}`, http.StatusForbidden)
			},
		},
		{
			name: "CE accepts but returns no task id",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{}`))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := newHistRecorder()
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/ce/submit", func(w http.ResponseWriter, r *http.Request) {
				rec.note(r.URL.Path)
				tc.handler(w, r)
			})
			mux.HandleFunc("GET /api/ce/task", func(w http.ResponseWriter, r *http.Request) {
				rec.note(r.URL.Path)
				_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"status": "SUCCESS"}})
			})
			addDefaultCloudHandler(mux)

			e := newCustomCloudTest(t, mux)
			snap := historySnapshot{Date: time.Date(2022, 3, 1, 0, 0, 0, 0, time.UTC), ProjectVersion: "2.5"}

			err := submitHistoricalSnapshot(context.Background(), e, histBranchContext(), branchMain, snap, histPlaceholder())
			if err == nil {
				t.Fatal("expected an error when the CE refuses the report")
			}
			if !strings.Contains(err.Error(), "submitting historical report") {
				t.Errorf("expected the error to name the submit step, got: %v", err)
			}
			if n := rec.count("/api/ce/task"); n != 0 {
				t.Errorf("expected no CE polling when submission failed, got %d polls", n)
			}
		})
	}
}

// TestSubmitHistoricalSnapshotCETaskFailure pins that a report the CE accepts
// but then fails to process is reported as an error rather than silently
// counting as a migrated point. Costs ~5s (PollCETask's fixed first-poll
// delay).
func TestSubmitHistoricalSnapshotCETaskFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/ce/submit", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-hist-fail"})
	})
	mux.HandleFunc("GET /api/ce/task", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task": map[string]any{
				"status":       "FAILED",
				"errorType":    "INVALID_REPORT",
				"errorMessage": "Quality profiles with following keys don't exist in organization",
			},
		})
	})
	addDefaultCloudHandler(mux)

	e := newCustomCloudTest(t, mux)
	histLogBuf(e, slog.LevelWarn)
	snap := historySnapshot{Date: time.Date(2022, 3, 1, 0, 0, 0, 0, time.UTC), ProjectVersion: "2.5"}

	err := submitHistoricalSnapshot(context.Background(), e, histBranchContext(), branchMain, snap, histPlaceholder())
	if err == nil {
		t.Fatal("expected an error when the CE task fails")
	}
	if !strings.Contains(err.Error(), "CE task failed") {
		t.Errorf("expected the error to name the CE task failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "AX-hist-fail") {
		t.Errorf("expected the failing task id in the error, got: %v", err)
	}
}
