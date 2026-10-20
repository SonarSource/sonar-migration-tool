// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFailedProjectsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if list, err := ReadFailedProjects(dir); err != nil || list != nil {
		t.Fatalf("missing file: got %v, %v; want nil, nil", list, err)
	}

	in := []FailedProject{{Key: "b", Task: "t1", Error: "e1"}, {Key: "a", Task: "t2", Error: "e2"}}
	if err := WriteFailedProjects(dir, in); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFailedProjects(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []FailedProject{{Key: "a", Task: "t2", Error: "e2"}, {Key: "b", Task: "t1", Error: "e1"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back %+v, want %+v sorted by key", got, want)
	}

	// An empty list removes the file rather than leaving a stale one.
	if err := WriteFailedProjects(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, FailedProjectsFileName)); !os.IsNotExist(err) {
		t.Errorf("expected the file removed, stat err = %v", err)
	}
}

func TestDataStoreDoneMarkers(t *testing.T) {
	dir := t.TempDir()
	ds := NewDataStore(dir)

	// Finished: kept by DiscardUnfinished, its directory untouched.
	w, err := ds.Writer("done")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteOne([]byte(`{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := ds.MarkDone("done"); err != nil {
		t.Fatal(err)
	}
	if !ds.IsDone("done") || !ds.IsComplete("done") {
		t.Error("expected the task done on disk and in memory")
	}
	if discarded, err := ds.DiscardUnfinished("done"); err != nil || discarded {
		t.Errorf("finished task: discarded=%v err=%v, want false, nil", discarded, err)
	}

	// Started but not finished: its partial output is deleted.
	w, err = ds.Writer("partial")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteOne([]byte(`{"k":2}`)); err != nil {
		t.Fatal(err)
	}
	if discarded, err := ds.DiscardUnfinished("partial"); err != nil || !discarded {
		t.Errorf("unfinished task: discarded=%v err=%v, want true, nil", discarded, err)
	}
	if ds.TaskDirExists("partial") {
		t.Error("expected the unfinished task's directory deleted")
	}

	// A task that wrote nothing gets a marker but no task directory.
	if err := ds.MarkDone("empty"); err != nil {
		t.Fatal(err)
	}
	if !ds.IsDone("empty") || ds.TaskDirExists("empty") {
		t.Error("expected a done marker and no task directory for a task that wrote nothing")
	}

	// Never started: nothing to discard.
	if discarded, err := ds.DiscardUnfinished("never"); err != nil || discarded {
		t.Errorf("never-run task: discarded=%v err=%v, want false, nil", discarded, err)
	}

	// The marker is not read as task data.
	items, err := ds.ReadAll("done")
	if err != nil || len(items) != 1 {
		t.Errorf("ReadAll(done) = %d items, err %v; want 1", len(items), err)
	}
}

func TestDataStoreMarkDoneNilSafe(t *testing.T) {
	var ds *DataStore
	if err := ds.MarkDone("x"); err != nil {
		t.Errorf("nil store: %v", err)
	}
}
