// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// --- fixtures -------------------------------------------------------------

// multiBranchNonMain are the non-main branches the fixtures below give real
// components and source to, so each one actually reaches SubmitReport
// instead of short-circuiting as "skipped: no components".
var multiBranchNonMain = []string{"develop", "release-1", "release-2", "release-3"}

// setupMultiBranchExtract writes an extract in which the main branch AND
// every branch in multiBranchNonMain has a component, source and an active
// rule — the minimum for importBranch to build and submit a report.
// setupProjectDataExtract deliberately gives components to "main" only, so
// its non-main branches never submit anything and cannot exercise a
// parallel fan-out.
func setupMultiBranchExtract(t *testing.T, dir string) {
	t.Helper()
	extractDir := filepath.Join(dir, "extract-01")

	writeJSON(filepath.Join(extractDir, "extract.json"),
		map[string]any{"url": testServerURL, "edition": "enterprise"})

	branches := []map[string]any{
		{"projectKey": "proj1", "name": "main", "type": "LONG", "isMain": true, "serverUrl": testServerURL},
	}
	var components, sources []map[string]any
	for _, b := range append([]string{"main"}, multiBranchNonMain...) {
		if b != "main" {
			branches = append(branches, map[string]any{
				"projectKey": "proj1", "name": b, "type": "LONG", "isMain": false, "serverUrl": testServerURL,
			})
		}
		components = append(components, map[string]any{
			"key": "proj1:src/Main.java", "name": "Main.java", "path": "src/Main.java",
			"language": "java", "lines": 50,
			"projectKey": "proj1", "branch": b,
			"serverUrl": testServerURL,
		})
		sources = append(sources, map[string]any{
			"key": "proj1:src/Main.java", "source": "public class Main {}",
			"projectKey": "proj1", "branch": b,
			"serverUrl": testServerURL,
		})
	}

	writeJSONL(filepath.Join(extractDir, "getBranches"), branches)
	writeJSONL(filepath.Join(extractDir, "getProjectComponentTree"), components)
	writeJSONL(filepath.Join(extractDir, "getProjectSourceCode"), sources)
	writeJSONL(filepath.Join(extractDir, "getProjectIssuesFull"), []map[string]any{})
	writeJSONL(filepath.Join(extractDir, "getProjectHotspotsFull"), []map[string]any{})
	writeJSONL(filepath.Join(extractDir, "getActiveProfileRules"), []map[string]any{
		{"key": "java:S100", "severity": "MAJOR", "qProfile": "prof1", "lang": "java", "serverUrl": testServerURL},
	})
	writeJSONL(filepath.Join(extractDir, "getProfiles"), []map[string]any{
		{"key": "prof1", "name": "Sonar way", "language": "java", "serverUrl": testServerURL},
	})
}

// ceRecorder is a mutex-guarded CE stand-in that tracks how many ce/submit
// calls are in flight simultaneously, and in what order they arrived.
//
// The mutex is not optional: the whole point of these tests is that
// multiple httptest handler goroutines hit this at once, and an
// unsynchronized recorder would trip -race (the pre-existing recorder in
// TestImportProjectBranchesMainFirst is unguarded but never sees real
// concurrency).
type ceRecorder struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	order    []string
	done     []string
	hold     time.Duration
}

func (c *ceRecorder) enter(branch string) {
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.maxSeen {
		c.maxSeen = c.inFlight
	}
	c.order = append(c.order, branch)
	c.mu.Unlock()
}

func (c *ceRecorder) leave(branch string) {
	c.mu.Lock()
	c.inFlight--
	c.done = append(c.done, branch)
	c.mu.Unlock()
}

func (c *ceRecorder) snapshot() (maxSeen int, order, done []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxSeen, append([]string(nil), c.order...), append([]string(nil), c.done...)
}

// newCEServer serves the submit/poll pair importBranch drives. Each submit
// is held open for rec.hold so overlapping imports actually overlap in wall
// clock, making concurrency observable.
func newCEServer(t *testing.T, rec *ceRecorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/analysis/analyses":
			// The non-main "Create analysis" handshake; must return an id or
			// preCreateBranchAnalysis fails before the report is ever built.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "an-1", "branchId": "br-1", "branchType": "LONG",
			})
		case "/api/ce/submit":
			branch := submittedBranch(r)
			rec.enter(branch)
			time.Sleep(rec.hold)
			rec.leave(branch)
			_ = json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-1"})
		case "/api/ce/task":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"task": map[string]any{"status": "SUCCESS"},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// submittedBranch names the branch a ce/submit request carries. Main sends
// no branch characteristic at all (buildMultipartForm omits them so the CE
// registers the project's main branch), so its absence identifies main.
func submittedBranch(r *http.Request) string {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		return "main"
	}
	for _, ch := range r.MultipartForm.Value["characteristic"] {
		if name, ok := strings.CutPrefix(ch, "branch="); ok {
			return name
		}
	}
	return "main"
}

func multiBranchProject(t *testing.T) json.RawMessage {
	t.Helper()
	proj, err := json.Marshal(map[string]any{
		"key":                "proj1",
		"cloud_project_key":  "cloud-proj1",
		"sonarcloud_org_key": "cloud-org1",
		"server_url":         testServerURL,
	})
	if err != nil {
		t.Fatalf("marshal project: %v", err)
	}
	return proj
}

// newMultiBranchExecutor points every Cloud-side client at srv. Both Raw
// and RawAPI are needed: the report upload goes through CloudURL, while the
// non-main "Create analysis" handshake goes through RawAPI/APIURL.
func newMultiBranchExecutor(t *testing.T, dir string, srv *httptest.Server) *Executor {
	t.Helper()
	e := newProjectDataExecutor(t, dir)
	e.CloudURL = srv.URL + "/"
	e.APIURL = srv.URL + "/"
	e.Raw = common.NewRawClient(srv.Client(), srv.URL+"/")
	e.RawAPI = common.NewRawClient(srv.Client(), srv.URL+"/")
	return e
}

func multiBranchList() []branchInfo {
	branches := []branchInfo{{Name: "main", IsMain: true}}
	for _, b := range multiBranchNonMain {
		branches = append(branches, branchInfo{Name: b, IsMain: false})
	}
	return branches
}

// runMultiBranchImport wires an Executor against a CE recorder, runs
// importProjectBranches for one project with one main plus
// multiBranchNonMain, and returns the recorder.
func runMultiBranchImport(t *testing.T, gate *DynamicGate, hold time.Duration) *ceRecorder {
	t.Helper()
	dir := t.TempDir()
	setupMultiBranchExtract(t, dir)

	rec := &ceRecorder{hold: hold}
	srv := newCEServer(t, rec)

	e := newMultiBranchExecutor(t, dir, srv)
	e.BranchGate = gate

	w, err := e.Store.Writer("importProjectData")
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if err := importProjectBranches(context.Background(), e, multiBranchProject(t), multiBranchList(), nil, nil, w); err != nil {
		t.Fatalf("importProjectBranches: %v", err)
	}
	return rec
}

// --- tests ----------------------------------------------------------------

// TestImportProjectBranchesRunsNonMainInParallel is the core assertion for
// the branch fan-out: with an unconstrained gate, the non-main branches
// must overlap rather than run one after another.
func TestImportProjectBranchesRunsNonMainInParallel(t *testing.T) {
	rec := runMultiBranchImport(t, NewDynamicGate(NewFixedConcurrencyLimiter(len(multiBranchNonMain))), 60*time.Millisecond)

	maxSeen, order, _ := rec.snapshot()
	if want := len(multiBranchNonMain) + 1; len(order) != want {
		t.Fatalf("saw %d ce/submit calls %v, want %d (main + %d non-main)", len(order), order, want, len(multiBranchNonMain))
	}
	if maxSeen < 2 {
		t.Errorf("max concurrent ce/submit = %d, want >= 2 — non-main branches still appear to be sequential (order: %v)", maxSeen, order)
	}
}

// TestImportProjectBranchesMainIsABarrier guards the invariant that makes
// the fan-out legal at all. A non-main report carries branch
// characteristics the CE rejects until a main analysis anchors the project,
// and PreCreateAnalysis targets bctx.MainTargetName, which only resolves
// after renameSCMainBranchToSource. So main must not merely be submitted
// first — it must have FINISHED before any non-main branch is submitted.
func TestImportProjectBranchesMainIsABarrier(t *testing.T) {
	rec := runMultiBranchImport(t, NewDynamicGate(NewFixedConcurrencyLimiter(len(multiBranchNonMain))), 40*time.Millisecond)

	_, order, done := rec.snapshot()
	if len(order) == 0 {
		t.Fatal("no ce/submit calls recorded")
	}
	if order[0] != "main" {
		t.Errorf("first ce/submit was %q, want \"main\" (order: %v)", order[0], order)
	}
	// Main must also be the first to COMPLETE — proving it was a barrier
	// and not just the first of several overlapping submissions.
	if len(done) == 0 || done[0] != "main" {
		t.Errorf("first ce/submit to complete was %v, want \"main\" — main is not acting as a barrier", done)
	}
}

// TestImportProjectBranchesRespectsBranchGate proves the fan-out is bounded
// by the run-wide gate rather than launching every branch at once. Without
// this bound, in-flight CE tasks would be projects x branches — roughly ten
// times the rate budget the project gate was sized to fit (see
// Executor.BranchGate).
func TestImportProjectBranchesRespectsBranchGate(t *testing.T) {
	const limit = 2
	rec := runMultiBranchImport(t, NewDynamicGate(NewFixedConcurrencyLimiter(limit)), 60*time.Millisecond)

	maxSeen, order, _ := rec.snapshot()
	if maxSeen > limit {
		t.Errorf("max concurrent ce/submit = %d, want <= %d (order: %v)", maxSeen, limit, order)
	}
	if want := len(multiBranchNonMain) + 1; len(order) != want {
		t.Errorf("saw %d ce/submit calls, want %d — gating must throttle, not drop branches", len(order), want)
	}
}

// TestImportProjectBranchesNilGateStillImports covers the fixture and
// reset.go / sync_issues_standalone.go path, where BranchGate is nil.
// DynamicGate's methods are nil-safe, so every branch should still import.
func TestImportProjectBranchesNilGateStillImports(t *testing.T) {
	rec := runMultiBranchImport(t, nil, 0)

	_, order, _ := rec.snapshot()
	if want := len(multiBranchNonMain) + 1; len(order) != want {
		t.Errorf("saw %d ce/submit calls %v with a nil BranchGate, want %d", len(order), order, want)
	}
}

// TestImportProjectBranchesRecordsEveryBranch pins the behavior that
// survived the sequential-to-parallel switch: each branch's outcome is
// recorded independently, and a non-main branch never fails the project.
func TestImportProjectBranchesRecordsEveryBranch(t *testing.T) {
	dir := t.TempDir()
	setupMultiBranchExtract(t, dir)

	rec := &ceRecorder{}
	srv := newCEServer(t, rec)

	e := newMultiBranchExecutor(t, dir, srv)
	e.BranchGate = NewDynamicGate(NewFixedConcurrencyLimiter(4))

	w, err := e.Store.Writer("importProjectData")
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if err := importProjectBranches(context.Background(), e, multiBranchProject(t), multiBranchList(), nil, nil, w); err != nil {
		t.Fatalf("importProjectBranches: %v", err)
	}

	items, err := e.Store.ReadAll("importProjectData")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	seen := map[string]bool{}
	for _, it := range items {
		seen[extractField(it, "branch")] = true
	}
	for _, b := range append([]string{"main"}, multiBranchNonMain...) {
		if !seen[b] {
			t.Errorf("no result recorded for branch %q (recorded: %v)", b, keysOf(seen))
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestImportProjectBranchesMainFailureSkipsNonMain re-pins the Phase 1
// barrier's failure path now that Phase 2 is a fan-out: when main's CE
// fails, no non-main branch may be submitted at all.
func TestImportProjectBranchesMainFailureSkipsNonMain(t *testing.T) {
	dir := t.TempDir()
	setupMultiBranchExtract(t, dir)

	var mu sync.Mutex
	var submitted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/analysis/analyses":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "an-1"})
		case "/api/ce/submit":
			mu.Lock()
			submitted = append(submitted, submittedBranch(r))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-1"})
		case "/api/ce/task":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"task": map[string]any{"status": "FAILED", "errorMessage": "boom"},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer srv.Close()

	e := newMultiBranchExecutor(t, dir, srv)
	e.BranchGate = NewDynamicGate(NewFixedConcurrencyLimiter(4))

	w, _ := e.Store.Writer("importProjectData")
	if err := importProjectBranches(context.Background(), e, multiBranchProject(t), multiBranchList(), nil, nil, w); err == nil {
		t.Fatal("importProjectBranches returned nil, want the main-branch CE failure")
	}

	mu.Lock()
	n := len(submitted)
	mu.Unlock()
	if n != 1 {
		t.Errorf("%d ce/submit calls after main failed, want exactly 1 (main only): %v", n, submitted)
	}

	items, _ := e.Store.ReadAll("importProjectData")
	for _, it := range items {
		branch := extractField(it, "branch")
		if branch == "main" {
			continue
		}
		if status := extractField(it, "status"); status != "skipped" {
			t.Errorf("branch %q status = %q, want \"skipped\"", branch, status)
		}
	}
}

// TestImportProjectBranchesRecordsBranchesSkippedByCancellation pins the
// report accounting for a cancelled run. The migration report is assembled
// by enumerating the importProjectData store, with no pass that reconciles
// it against the branch list, so a branch the run never attempted has to
// leave a row behind or it disappears from the report entirely — and a
// project whose rows all disappear renders as Succeeded.
func TestImportProjectBranchesRecordsBranchesSkippedByCancellation(t *testing.T) {
	dir := t.TempDir()
	setupMultiBranchExtract(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel on the first non-main "Create analysis" handshake. Main never
	// calls that endpoint, so by the time it fires main is fully imported
	// and recorded — which is what forces Phase 2 to be the code under test.
	// Cancelling any earlier fails MAIN instead, and Phase 1's own "main
	// branch CE failed" path would write the skip rows while Phase 2 never
	// runs at all.
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/analysis/analyses":
			once.Do(cancel)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "an-1", "branchId": "br-1", "branchType": "LONG",
			})
		case "/api/ce/submit":
			_ = json.NewEncoder(w).Encode(map[string]any{"taskId": "task-" + submittedBranch(r)})
		case "/api/ce/task":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"task": map[string]any{"status": "SUCCESS"},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)

	e := newMultiBranchExecutor(t, dir, srv)
	// One slot, so only the first non-main branch is ever admitted. The rest
	// are still blocked on Acquire when the cancel lands, which is the path
	// that used to drop them.
	e.BranchGate = NewDynamicGate(NewFixedConcurrencyLimiter(1))

	w, err := e.Store.Writer("importProjectData")
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if err := importProjectBranches(ctx, e, multiBranchProject(t), multiBranchList(), nil, nil, w); err != nil {
		t.Fatalf("importProjectBranches: %v", err)
	}

	items, err := e.Store.ReadAll("importProjectData")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	status := map[string]string{}
	reason := map[string]string{}
	for _, it := range items {
		branch := extractField(it, "branch")
		status[branch] = extractField(it, "status")
		reason[branch] = extractField(it, "error")
	}

	// Guard against this test going vacuous: if main did not succeed, Phase 1
	// wrote the rows below and the Phase 2 path was never exercised.
	if status["main"] != "success" {
		t.Fatalf("main must succeed for Phase 2 to be reached: main status = %q, error = %q",
			status["main"], reason["main"])
	}

	// The accounting assertion: nothing vanishes.
	for _, name := range multiBranchNonMain {
		if _, ok := status[name]; !ok {
			t.Errorf("branch %q left no row; a cancelled run must still account for it (rows: %v)", name, status)
		}
	}

	cancelled := 0
	for _, name := range multiBranchNonMain {
		if reason[name] == "skipped: migration cancelled" {
			if status[name] != "skipped" {
				t.Errorf("branch %q: status = %q, want \"skipped\"", name, status[name])
			}
			cancelled++
		}
	}
	if cancelled == 0 {
		t.Errorf("no branch was recorded as cancelled; reasons: %v", reason)
	}
}
