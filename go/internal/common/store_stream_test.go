// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeStoreChunks writes one results.N.jsonl per chunk directly into the
// task directory, bypassing ChunkWriter so a test can control the exact
// file layout. Multi-chunk tasks are what production actually produces.
func writeStoreChunks(t *testing.T, taskDir string, chunks [][]string) {
	t.Helper()
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, lines := range chunks {
		var buf []byte
		for _, l := range lines {
			buf = append(buf, l...)
			buf = append(buf, '\n')
		}
		name := filepath.Join(taskDir, "results."+strconv.Itoa(i+1)+".jsonl")
		if err := os.WriteFile(name, buf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// collectRecords drains the iterator, returning records and the first error.
func collectRecords(seq func(func(json.RawMessage, error) bool)) ([]json.RawMessage, error) {
	var out []json.RawMessage
	var firstErr error
	for item, err := range seq {
		if err != nil {
			firstErr = err
			break
		}
		out = append(out, item)
	}
	return out, firstErr
}

// The wrapper must agree with the iterator exactly — that equivalence is
// what lets the existing ReadAll call sites stay untouched.
func TestRecordsMatchesReadAllAcrossChunks(t *testing.T) {
	dir := t.TempDir()
	ds := NewDataStore(dir)
	writeStoreChunks(t, filepath.Join(dir, "someTask"), [][]string{
		{`{"n":1}`, `{"n":2}`},
		{`{"n":3}`},
	})

	streamed, err := collectRecords(ds.Records("someTask"))
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	slurped, err := ds.ReadAll("someTask")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if len(streamed) != 3 {
		t.Fatalf("streamed %d records, want 3", len(streamed))
	}
	if len(slurped) != len(streamed) {
		t.Fatalf("ReadAll %d vs Records %d", len(slurped), len(streamed))
	}
	for i := range streamed {
		if string(streamed[i]) != string(slurped[i]) {
			t.Errorf("record %d: %s vs %s", i, streamed[i], slurped[i])
		}
	}
}

// ReadAll returns (nil, nil) for a task that never ran; Records must yield
// nothing at all rather than surfacing an error.
func TestRecordsMissingDirYieldsNothing(t *testing.T) {
	ds := NewDataStore(t.TempDir())

	got, err := collectRecords(ds.Records("neverRan"))
	if err != nil {
		t.Fatalf("missing dir should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d records, want 0", len(got))
	}

	items, err := ds.ReadAll("neverRan")
	if err != nil || items != nil {
		t.Errorf("ReadAll = (%v, %v), want (nil, nil)", items, err)
	}
}

// Unlike the extract reader, the store is all-or-nothing: a per-file read
// error aborts and no record from the failing file is yielded. Callers of
// ReadAll rely on never seeing a partial task.
func TestRecordsAbortsOnFileError(t *testing.T) {
	dir := t.TempDir()
	ds := NewDataStore(dir)
	taskDir := filepath.Join(dir, "someTask")
	writeStoreChunks(t, taskDir, [][]string{{`{"n":1}`}})

	bad := filepath.Join(taskDir, "results.2.jsonl")
	if err := os.WriteFile(bad, []byte(`{"n":2}`+"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o600) })

	_, err := collectRecords(ds.Records("someTask"))
	if err == nil {
		t.Error("Records should surface the per-file error")
	}

	if _, err := ds.ReadAll("someTask"); err == nil {
		t.Error("ReadAll should still return the error")
	}
}

// Task names embedding ':' are sanitized for Windows (#486). Records
// resolves through the same taskDir, so Writer / ReadAll / Records must all
// agree on where the data lives.
func TestRecordsSanitizesTaskName(t *testing.T) {
	dir := t.TempDir()
	ds := NewDataStore(dir)

	w, err := ds.Writer("getTemplateGroupsScanners:apply")
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	if err := w.WriteOne(json.RawMessage(`{"k":"v"}`)); err != nil {
		t.Fatalf("WriteOne: %v", err)
	}

	got, err := collectRecords(ds.Records("getTemplateGroupsScanners:apply"))
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(got) != 1 || string(got[0]) != `{"k":"v"}` {
		t.Errorf("got %v, want the single written record", got)
	}
}

// Breaking out must stop before the next chunk file is opened.
func TestRecordsStopsEarly(t *testing.T) {
	dir := t.TempDir()
	ds := NewDataStore(dir)
	taskDir := filepath.Join(dir, "someTask")
	writeStoreChunks(t, taskDir, [][]string{{`{"n":1}`}})

	bad := filepath.Join(taskDir, "results.2.jsonl")
	if err := os.WriteFile(bad, []byte(`{"n":2}`+"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o600) })

	count := 0
	for _, err := range ds.Records("someTask") {
		if err != nil {
			t.Fatalf("unexpected error before break: %v", err)
		}
		count++
		break
	}
	if count != 1 {
		t.Errorf("yielded %d records after break, want 1", count)
	}
}

// Non-.jsonl files in a task directory are ignored by both paths.
func TestRecordsIgnoresNonJSONLFiles(t *testing.T) {
	dir := t.TempDir()
	ds := NewDataStore(dir)
	taskDir := filepath.Join(dir, "someTask")
	writeStoreChunks(t, taskDir, [][]string{{`{"n":1}`}})

	if err := os.WriteFile(filepath.Join(taskDir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := collectRecords(ds.Records("someTask"))
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d records, want 1", len(got))
	}
}
