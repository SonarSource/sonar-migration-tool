// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	streamTestURL  = "https://sq.test/"
	streamTestURL2 = "https://other.test/"

	// Extract run directory names used by the fixtures.
	extract01 = "extract-01"
	extract02 = "extract-02"

	taskSourceCode = "getProjectSourceCode"
)

// writeChunks creates a task directory containing one results.N.jsonl file
// per chunk. Production writes one chunk file per source-code record, so
// multi-chunk tasks are the norm rather than the exception — every other
// fixture in the repo hardcodes results.1.jsonl and so never exercised it.
func writeChunks(t *testing.T, taskDir string, chunks [][]string) {
	t.Helper()
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, lines := range chunks {
		name := filepath.Join(taskDir, "results."+strconv.Itoa(i+1)+".jsonl")
		var buf []byte
		for _, l := range lines {
			buf = append(buf, l...)
			buf = append(buf, '\n')
		}
		if err := os.WriteFile(name, buf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// oneExtract builds <root>/extract-01/<task>/ with the given chunks and
// returns the root plus a mapping pointing at it.
func oneExtract(t *testing.T, task string, chunks [][]string) (string, ExtractMapping) {
	t.Helper()
	root := t.TempDir()
	writeChunks(t, filepath.Join(root, extract01, task), chunks)
	return root, ExtractMapping{streamTestURL: {extract01}}
}

func collect(seq func(func(ExtractItem) bool)) []ExtractItem {
	var out []ExtractItem
	for it := range seq {
		out = append(out, it)
	}
	return out
}

// The wrapper must return exactly what the iterator yields, in the same
// order — that equivalence is what lets ~126 cold call sites keep using
// ReadExtractData unchanged.
func TestExtractItemsMatchesReadExtractDataAcrossChunks(t *testing.T) {
	root, mapping := oneExtract(t, taskSourceCode, [][]string{
		{`{"k":"a1"}`, `{"k":"a2"}`},
		{`{"k":"b1"}`},
		{`{"k":"c1"}`, `{"k":"c2"}`, `{"k":"c3"}`},
	})

	streamed := collect(ExtractItems(root, mapping, taskSourceCode))
	slurped, err := ReadExtractData(root, mapping, taskSourceCode)
	if err != nil {
		t.Fatalf("ReadExtractData: %v", err)
	}

	if len(streamed) != 6 {
		t.Fatalf("streamed %d records, want 6", len(streamed))
	}
	if len(slurped) != len(streamed) {
		t.Fatalf("ReadExtractData returned %d, ExtractItems yielded %d", len(slurped), len(streamed))
	}
	for i := range streamed {
		if string(streamed[i].Data) != string(slurped[i].Data) {
			t.Errorf("record %d: streamed %s, slurped %s", i, streamed[i].Data, slurped[i].Data)
		}
		if streamed[i].ServerURL != slurped[i].ServerURL {
			t.Errorf("record %d: serverURL %q vs %q", i, streamed[i].ServerURL, slurped[i].ServerURL)
		}
	}
}

// results.10.jsonl sorts lexically before results.2.jsonl. That ordering is
// irrelevant for tasks that get sorted downstream, but not for the callers
// that index element [0], so pin that streaming preserves it rather than
// leaving the equivalence assumed.
func TestExtractItemsPreservesReadDirOrdering(t *testing.T) {
	chunks := make([][]string, 10)
	for i := range chunks {
		chunks[i] = []string{`{"chunk":` + strconv.Itoa(i+1) + `}`}
	}
	root, mapping := oneExtract(t, "task", chunks)

	streamed := collect(ExtractItems(root, mapping, "task"))
	slurped, err := ReadExtractData(root, mapping, "task")
	if err != nil {
		t.Fatalf("ReadExtractData: %v", err)
	}
	for i := range streamed {
		if string(streamed[i].Data) != string(slurped[i].Data) {
			t.Fatalf("order diverged at %d: %s vs %s", i, streamed[i].Data, slurped[i].Data)
		}
	}
	// Sanity: confirm this fixture really does exercise the lexical quirk.
	if string(streamed[1].Data) != `{"chunk":10}` {
		t.Errorf("expected results.10 to sort second, got %s", streamed[1].Data)
	}
}

// #314, at the iterator level: a single unreadable file must not abort the
// task, and records from the readable files must still be yielded.
func TestExtractItemsSkipsUnreadableFile(t *testing.T) {
	root := t.TempDir()
	taskDir := filepath.Join(root, extract01, "task")
	writeChunks(t, taskDir, [][]string{{`{"k":"v1"}`, `{"k":"v2"}`}})

	bad := filepath.Join(taskDir, "results.2.jsonl")
	if err := os.WriteFile(bad, []byte(`{"k":"v3"}`+"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o600) })

	got := collect(ExtractItems(root, ExtractMapping{streamTestURL: {extract01}}, "task"))
	if len(got) != 2 {
		t.Errorf("got %d records, want 2 from the readable file", len(got))
	}
}

// An extract that never ran the task is skipped, and the other extract in
// the mapping still contributes — this is why the missing-dir case must
// stay an error internally rather than being silently treated as empty.
func TestExtractItemsSkipsExtractMissingTaskDir(t *testing.T) {
	root := t.TempDir()
	writeChunks(t, filepath.Join(root, extract01, "task"), [][]string{{`{"k":"present"}`}})
	// extract-02 exists but has no "task" directory.
	if err := os.MkdirAll(filepath.Join(root, extract02, "other"), 0o755); err != nil {
		t.Fatal(err)
	}

	mapping := ExtractMapping{
		streamTestURL:  {extract01},
		streamTestURL2: {extract02},
	}
	got := collect(ExtractItems(root, mapping, "task"))
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].ServerURL != streamTestURL {
		t.Errorf("serverURL = %q, want %q", got[0].ServerURL, streamTestURL)
	}
}

// Breaking out must stop before the next chunk file is opened. The second
// chunk is made unreadable: if iteration continued it would warn and the
// record count would be wrong, so a clean early exit proves laziness — the
// whole point of streaming.
func TestExtractItemsStopsEarlyWithoutOpeningNextFile(t *testing.T) {
	root := t.TempDir()
	taskDir := filepath.Join(root, extract01, "task")
	writeChunks(t, taskDir, [][]string{{`{"k":"first"}`}})

	bad := filepath.Join(taskDir, "results.2.jsonl")
	if err := os.WriteFile(bad, []byte(`{"k":"never-read"}`+"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o600) })

	var seen []string
	for it := range ExtractItems(root, ExtractMapping{streamTestURL: {extract01}}, "task") {
		seen = append(seen, string(it.Data))
		break
	}

	if len(seen) != 1 || seen[0] != `{"k":"first"}` {
		t.Fatalf("got %v, want exactly the first record", seen)
	}
}

// Stopping early must also abandon remaining extracts, not just remaining
// files within one extract.
func TestExtractItemsStopsEarlyAcrossExtracts(t *testing.T) {
	root := t.TempDir()
	writeChunks(t, filepath.Join(root, extract01, "task"), [][]string{{`{"n":1}`, `{"n":2}`}})
	writeChunks(t, filepath.Join(root, extract02, "task"), [][]string{{`{"n":3}`, `{"n":4}`}})

	mapping := ExtractMapping{
		streamTestURL:  {extract01},
		streamTestURL2: {extract02},
	}

	count := 0
	for range ExtractItems(root, mapping, "task") {
		count++
		break
	}
	if count != 1 {
		t.Errorf("yielded %d records after break, want 1", count)
	}
}

// An empty mapping yields nothing rather than panicking.
func TestExtractItemsEmptyMapping(t *testing.T) {
	if got := collect(ExtractItems(t.TempDir(), ExtractMapping{}, "task")); len(got) != 0 {
		t.Errorf("got %d records, want 0", len(got))
	}
}

// The yielded Data must be usable as JSON, i.e. the raw message is not
// aliased or truncated by chunk boundaries.
func TestExtractItemsYieldsParseableRecords(t *testing.T) {
	root, mapping := oneExtract(t, "task", [][]string{
		{`{"key":"a","branch":"main"}`},
		{`{"key":"b","branch":"dev"}`},
	})

	var keys []string
	for it := range ExtractItems(root, mapping, "task") {
		var rec struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(it.Data, &rec); err != nil {
			t.Fatalf("unmarshal %s: %v", it.Data, err)
		}
		keys = append(keys, rec.Key)
	}
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Errorf("keys = %v, want [a b]", keys)
	}
}

const (
	runA = "2026-08-20-0001"
	runB = "2026-08-20-0002"
)

func projectRecords(root, id string, lines ...string) {
	dir := filepath.Join(root, id, "getProjectDetails")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "results.1.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// #664: a project re-extracted by a later extract replaces its older record;
// a server-wide task is read from the newest extract that ran it.
func TestExtractItemsSupersedesReExtractedProjects(t *testing.T) {
	root := t.TempDir()
	writeTestJSON(t, filepath.Join(root, runA, "extract.json"), map[string]any{"url": streamTestURL})
	writeTestJSON(t, filepath.Join(root, runB, "extract.json"),
		map[string]any{"url": streamTestURL, "partial": true, "project_keys": []string{"b"}})
	projectRecords(root, runA, `{"key":"a","v":1}`, `{"key":"b","v":1}`)
	projectRecords(root, runB, `{"key":"b","v":2}`)
	writeChunks(t, filepath.Join(root, runA, "getGates"), [][]string{{`{"name":"g1"}`}})
	writeChunks(t, filepath.Join(root, runB, "getGates"), [][]string{{`{"name":"g2"}`}})

	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for it := range ExtractItems(root, mapping, "getProjectDetails") {
		got = append(got, string(it.Data))
	}
	want := []string{`{"key":"a","v":1}`, `{"key":"b","v":2}`}
	if !slices.Equal(got, want) {
		t.Errorf("projects = %v, want %v", got, want)
	}
	gates := collect(ExtractItems(root, mapping, "getGates"))
	if len(gates) != 1 || string(gates[0].Data) != `{"name":"g2"}` {
		t.Errorf("gates = %v, want only the newest extract's record", gates)
	}
}

// On resume, completed output from the original project scope remains beside
// output for the replacement scope. Both sets of records must supersede older
// copies even though extract.json records only the replacement project keys.
func TestExtractItemsResumeScopeChangeDoesNotDuplicateRetainedProjects(t *testing.T) {
	root := t.TempDir()
	writeTestJSON(t, filepath.Join(root, runA, "extract.json"), map[string]any{"url": streamTestURL})
	writeTestJSON(t, filepath.Join(root, runB, "extract.json"),
		map[string]any{"url": streamTestURL, "partial": true, "project_keys": []string{"b"}})
	projectRecords(root, runA, `{"key":"a","v":1}`, `{"key":"b","v":1}`, `{"key":"c","v":1}`)
	projectRecords(root, runB, `{"key":"a","v":2}`, `{"key":"b","v":2}`)
	markDone(t, filepath.Join(root, runB, ".done", "getProjectDetails"), "")

	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ExtractItems(root, mapping, "getProjectDetails"))
	want := []string{`{"key":"c","v":1}`, `{"key":"a","v":2}`, `{"key":"b","v":2}`}
	var records []string
	for _, item := range got {
		records = append(records, string(item.Data))
	}
	if !slices.Equal(records, want) {
		t.Errorf("projects = %v, want %v", records, want)
	}
}

// A partial extract without a project list re-extracted every project.
func TestExtractItemsPartialWithoutKeysSupersedesAll(t *testing.T) {
	root := t.TempDir()
	writeTestJSON(t, filepath.Join(root, runA, "extract.json"), map[string]any{"url": streamTestURL})
	writeTestJSON(t, filepath.Join(root, runB, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	projectRecords(root, runA, `{"key":"a","v":1}`)
	projectRecords(root, runB, `{"key":"a","v":2}`)
	mapping, _ := GetUniqueExtracts(root)
	got := collect(ExtractItems(root, mapping, "getProjectDetails"))
	if len(got) != 1 || string(got[0].Data) != `{"key":"a","v":2}` {
		t.Errorf("got %v, want only the newer record", got)
	}
}

// A later partial that did not run the task supersedes nothing.
func TestExtractItemsLaterExtractWithoutTaskSupersedesNothing(t *testing.T) {
	root := t.TempDir()
	writeTestJSON(t, filepath.Join(root, runA, "extract.json"), map[string]any{"url": streamTestURL})
	writeTestJSON(t, filepath.Join(root, runB, "extract.json"),
		map[string]any{"url": streamTestURL, "partial": true, "project_keys": []string{"a"}})
	projectRecords(root, runA, `{"key":"a","v":1}`)
	mapping, _ := GetUniqueExtracts(root)
	if got := collect(ExtractItems(root, mapping, "getProjectDetails")); len(got) != 1 {
		t.Errorf("got %d records, want 1", len(got))
	}
}

func TestRecordProjectKeyPrecedence(t *testing.T) {
	for raw, want := range map[string]string{
		`{"projectKey":"p","project":"q","key":"r"}`: "p",
		`{"project":"q","key":"r"}`:                  "q",
		`{"key":"r"}`:                                "r",
		`{"x":1}`:                                    "",
	} {
		if got := recordProjectKey([]byte(raw)); got != want {
			t.Errorf("recordProjectKey(%s) = %q, want %q", raw, got, want)
		}
	}
}

func portfolioRecords(root, id, task string, lines ...string) {
	dir := filepath.Join(root, id, task)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "results.1.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// #666: portfolio slices accumulate, and a portfolio re-extracted by a later
// slice replaces its older records (getPortfolioProjects is keyed by
// portfolioKey, the others by key).
func TestExtractItemsMergesPortfolioSlices(t *testing.T) {
	root := t.TempDir()
	writeTestJSON(t, filepath.Join(root, runA, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	writeTestJSON(t, filepath.Join(root, runB, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	portfolioRecords(root, runA, "getPortfolios", `{"key":"p1","v":1}`, `{"key":"p2","v":1}`)
	portfolioRecords(root, runB, "getPortfolios", `{"key":"p2","v":2}`, `{"key":"p3","v":2}`)
	portfolioRecords(root, runA, "getPortfolioProjects", `{"portfolioKey":"p1","k":"x"}`, `{"portfolioKey":"p2","k":"old"}`)
	portfolioRecords(root, runB, "getPortfolioProjects", `{"portfolioKey":"p2","k":"new"}`)

	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for it := range ExtractItems(root, mapping, "getPortfolios") {
		got = append(got, string(it.Data))
	}
	want := []string{`{"key":"p1","v":1}`, `{"key":"p2","v":2}`, `{"key":"p3","v":2}`}
	if !slices.Equal(got, want) {
		t.Errorf("portfolios = %v, want %v", got, want)
	}
	got = nil
	for it := range ExtractItems(root, mapping, "getPortfolioProjects") {
		got = append(got, string(it.Data))
	}
	want = []string{`{"portfolioKey":"p1","k":"x"}`, `{"portfolioKey":"p2","k":"new"}`}
	if !slices.Equal(got, want) {
		t.Errorf("portfolio projects = %v, want %v", got, want)
	}
}

// A later slice supersedes a portfolio even when it has no projects, so the
// older project list cannot reappear in cumulative extracts.
func TestExtractItemsEmptyPortfolioSliceRemovesOldProjects(t *testing.T) {
	root := t.TempDir()
	writeTestJSON(t, filepath.Join(root, runA, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	writeTestJSON(t, filepath.Join(root, runB, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	portfolioRecords(root, runA, "getPortfolios", `{"key":"p1"}`, `{"key":"p2"}`)
	portfolioRecords(root, runB, "getPortfolios", `{"key":"p2"}`)
	portfolioRecords(root, runA, "getPortfolioProjects", `{"portfolioKey":"p1","k":"keep"}`, `{"portfolioKey":"p2","k":"stale"}`)
	markDone(t, filepath.Join(root, runB, ".done", "getPortfolioProjects"), "")

	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ExtractItems(root, mapping, "getPortfolioProjects"))
	if len(got) != 1 || string(got[0].Data) != `{"portfolioKey":"p1","k":"keep"}` {
		t.Errorf("portfolio projects = %v, want only p1's older project list", got)
	}
}

// Merely finishing getPortfolios does not supersede records from a different
// portfolio-scoped task that the later extract never ran.
func TestExtractItemsPortfolioTaskSupersessionRequiresThatTaskToFinish(t *testing.T) {
	root := t.TempDir()
	writeTestJSON(t, filepath.Join(root, runA, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	writeTestJSON(t, filepath.Join(root, runB, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	portfolioRecords(root, runA, "getPortfolios", `{"key":"p1"}`)
	portfolioRecords(root, runB, "getPortfolios", `{"key":"p1"}`)
	portfolioRecords(root, runA, "getPortfolioProjects", `{"portfolioKey":"p1","k":"from-older-task"}`)

	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}
	got := collect(ExtractItems(root, mapping, "getPortfolioProjects"))
	if len(got) != 1 || string(got[0].Data) != `{"portfolioKey":"p1","k":"from-older-task"}` {
		t.Errorf("portfolio projects = %v, want the older task's record", got)
	}
}

// A later extract that ran no portfolio task (for example --objects projects)
// leaves the earlier portfolio records alone.
func TestExtractItemsPortfoliosSurviveLaterExtractWithoutThem(t *testing.T) {
	root := t.TempDir()
	writeTestJSON(t, filepath.Join(root, runA, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	writeTestJSON(t, filepath.Join(root, runB, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	portfolioRecords(root, runA, "getPortfolios", `{"key":"p1"}`)
	projectRecords(root, runB, `{"key":"a"}`)
	mapping, _ := GetUniqueExtracts(root)
	if got := collect(ExtractItems(root, mapping, "getPortfolios")); len(got) != 1 {
		t.Errorf("got %d portfolio records, want 1", len(got))
	}
}

func TestRecordPortfolioKeyPrecedence(t *testing.T) {
	for raw, want := range map[string]string{
		`{"portfolioKey":"p","key":"k"}`: "p",
		`{"key":"k"}`:                    "k",
		`{"x":1}`:                        "",
	} {
		if got := recordPortfolioKey(json.RawMessage(raw)); got != want {
			t.Errorf("recordPortfolioKey(%s) = %q, want %q", raw, got, want)
		}
	}
}

func markDone(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, contents)
}

// #666: a server-wide task re-run by a later extract must not repeat its
// records, and an unfinished newer run must not hide a finished older one.
func TestExtractItemsServerWideTaskNewestFinishedWins(t *testing.T) {
	root := t.TempDir()
	writeTestJSON(t, filepath.Join(root, runA, "extract.json"), map[string]any{"url": streamTestURL})
	writeTestJSON(t, filepath.Join(root, runB, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	for _, id := range []string{runA, runB} {
		writeChunks(t, filepath.Join(root, id, "getGroups"), [][]string{{`{"name":"` + id + `"}`}})
		markDone(t, filepath.Join(root, id, ".done", "getProjects"), "")
	}
	// Both extracts record finished tasks; only the older one finished getGroups.
	markDone(t, filepath.Join(root, runA, ".done", "getGroups"), "")
	mapping, _ := GetUniqueExtracts(root)
	got := collect(ExtractItems(root, mapping, "getGroups"))
	if len(got) != 1 || string(got[0].Data) != `{"name":"`+runA+`"}` {
		t.Errorf("got %v, want only the finished older extract's record", got)
	}
	// Once the newer run finishes it, it wins.
	markDone(t, filepath.Join(root, runB, ".done", "getGroups"), "")
	got = collect(ExtractItems(root, mapping, "getGroups"))
	if len(got) != 1 || string(got[0].Data) != `{"name":"`+runB+`"}` {
		t.Errorf("got %v, want only the newer extract's record", got)
	}
}
