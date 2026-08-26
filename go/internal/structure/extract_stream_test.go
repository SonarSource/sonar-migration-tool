// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const streamTestURL = "https://sq.test/"

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
	writeChunks(t, filepath.Join(root, "extract-01", task), chunks)
	return root, ExtractMapping{streamTestURL: "extract-01"}
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
	root, mapping := oneExtract(t, "getProjectSourceCode", [][]string{
		{`{"k":"a1"}`, `{"k":"a2"}`},
		{`{"k":"b1"}`},
		{`{"k":"c1"}`, `{"k":"c2"}`, `{"k":"c3"}`},
	})

	streamed := collect(ExtractItems(root, mapping, "getProjectSourceCode"))
	slurped, err := ReadExtractData(root, mapping, "getProjectSourceCode")
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
	taskDir := filepath.Join(root, "extract-01", "task")
	writeChunks(t, taskDir, [][]string{{`{"k":"v1"}`, `{"k":"v2"}`}})

	bad := filepath.Join(taskDir, "results.2.jsonl")
	if err := os.WriteFile(bad, []byte(`{"k":"v3"}`+"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o600) })

	got := collect(ExtractItems(root, ExtractMapping{streamTestURL: "extract-01"}, "task"))
	if len(got) != 2 {
		t.Errorf("got %d records, want 2 from the readable file", len(got))
	}
}

// An extract that never ran the task is skipped, and the other extract in
// the mapping still contributes — this is why the missing-dir case must
// stay an error internally rather than being silently treated as empty.
func TestExtractItemsSkipsExtractMissingTaskDir(t *testing.T) {
	root := t.TempDir()
	writeChunks(t, filepath.Join(root, "extract-01", "task"), [][]string{{`{"k":"present"}`}})
	// extract-02 exists but has no "task" directory.
	if err := os.MkdirAll(filepath.Join(root, "extract-02", "other"), 0o755); err != nil {
		t.Fatal(err)
	}

	mapping := ExtractMapping{
		streamTestURL:         "extract-01",
		"https://other.test/": "extract-02",
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
	taskDir := filepath.Join(root, "extract-01", "task")
	writeChunks(t, taskDir, [][]string{{`{"k":"first"}`}})

	bad := filepath.Join(taskDir, "results.2.jsonl")
	if err := os.WriteFile(bad, []byte(`{"k":"never-read"}`+"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o600) })

	var seen []string
	for it := range ExtractItems(root, ExtractMapping{streamTestURL: "extract-01"}, "task") {
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
	writeChunks(t, filepath.Join(root, "extract-01", "task"), [][]string{{`{"n":1}`, `{"n":2}`}})
	writeChunks(t, filepath.Join(root, "extract-02", "task"), [][]string{{`{"n":3}`, `{"n":4}`}})

	mapping := ExtractMapping{
		streamTestURL:         "extract-01",
		"https://other.test/": "extract-02",
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
