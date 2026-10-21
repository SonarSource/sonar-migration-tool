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

// writeTask writes one record for task, as a task's run would.
func writeTask(t *testing.T, ds *DataStore, task string) {
	t.Helper()
	w, err := ds.Writer(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteOne([]byte(`{"k":1}`)); err != nil {
		t.Fatal(err)
	}
}

// A finished task is done on disk and in memory, DiscardUnfinished leaves
// it alone, and its marker is not read as task data.
func TestDataStoreDoneMarkers_FinishedTaskIsKept(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	writeTask(t, ds, "done")
	if err := ds.MarkDone("done"); err != nil {
		t.Fatal(err)
	}
	if !ds.IsDone("done") || !ds.IsComplete("done") {
		t.Error("expected the task done on disk and in memory")
	}
	if discarded, err := ds.DiscardUnfinished("done"); err != nil || discarded {
		t.Errorf("discarded=%v err=%v, want false, nil", discarded, err)
	}
	if items, err := ds.ReadAll("done"); err != nil || len(items) != 1 {
		t.Errorf("ReadAll = %d items, err %v; want 1", len(items), err)
	}
}

// A task that started but did not finish has its partial output deleted.
func TestDataStoreDoneMarkers_UnfinishedTaskIsDiscarded(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	writeTask(t, ds, "partial")
	if discarded, err := ds.DiscardUnfinished("partial"); err != nil || !discarded {
		t.Errorf("discarded=%v err=%v, want true, nil", discarded, err)
	}
	if ds.TaskDirExists("partial") {
		t.Error("expected the unfinished task's directory deleted")
	}
}

// A task that wrote nothing gets a marker but still no task directory,
// and one that never ran has nothing to discard.
func TestDataStoreDoneMarkers_NoOutput(t *testing.T) {
	ds := NewDataStore(t.TempDir())
	if err := ds.MarkDone("empty"); err != nil {
		t.Fatal(err)
	}
	if !ds.IsDone("empty") || ds.TaskDirExists("empty") {
		t.Error("expected a done marker and no task directory for a task that wrote nothing")
	}
	if discarded, err := ds.DiscardUnfinished("never"); err != nil || discarded {
		t.Errorf("never-run task: discarded=%v err=%v, want false, nil", discarded, err)
	}
}

func TestDataStoreMarkDoneNilSafe(t *testing.T) {
	var ds *DataStore
	if err := ds.MarkDone("x"); err != nil {
		t.Errorf("nil store: %v", err)
	}
}
