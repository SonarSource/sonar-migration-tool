// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestRunTimings_ConcurrentAdds drives addPhase/addTask from many goroutines
// and asserts that phasesSnapshot/tasksSnapshot return every entry with no
// drops or torn data. Run under `go test -race` to catch races on the
// RunTimings mutex.
func TestRunTimings_ConcurrentAdds(t *testing.T) {
	const (
		phaseGoroutines = 50
		taskGoroutines  = 50
		perGoroutine    = 20
	)

	tm := &RunTimings{StartedAt: time.Now()}

	var wg sync.WaitGroup
	wg.Add(phaseGoroutines + taskGoroutines)

	for g := 0; g < phaseGoroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				tm.addPhase(PhaseTiming{Index: g*perGoroutine + i, Tasks: 1, Duration: 0.5})
			}
		}(g)
	}
	for g := 0; g < taskGoroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				tm.addTask(TaskTiming{
					Phase:    g,
					Name:     fmt.Sprintf("task-%d-%d", g, i),
					Duration: 0.25,
					OK:       true,
				})
			}
		}(g)
	}
	wg.Wait()

	wantPhases := phaseGoroutines * perGoroutine
	wantTasks := taskGoroutines * perGoroutine
	if got := tm.phasesSnapshot(); len(got) != wantPhases {
		t.Errorf("phasesSnapshot count: got %d, want %d", len(got), wantPhases)
	}
	if got := tm.tasksSnapshot(); len(got) != wantTasks {
		t.Errorf("tasksSnapshot count: got %d, want %d", len(got), wantTasks)
	}

	// Verify the full set of task names is present (no torn / dropped rows).
	tasks := tm.tasksSnapshot()
	seen := make(map[string]bool, len(tasks))
	for _, tk := range tasks {
		seen[tk.Name] = true
	}
	for g := 0; g < taskGoroutines; g++ {
		for i := 0; i < perGoroutine; i++ {
			name := fmt.Sprintf("task-%d-%d", g, i)
			if !seen[name] {
				t.Fatalf("missing task timing %q", name)
			}
		}
	}
}

// TestRunTimings_FailedTaskRecorded asserts that a failed task (OK=false,
// Err!="") is captured with a non-zero-ish measured duration.
func TestRunTimings_FailedTaskRecorded(t *testing.T) {
	tm := &RunTimings{StartedAt: time.Now()}

	// Measure a real (tiny) interval so the recorded duration is non-zero,
	// mirroring how runPhase derives Duration from time.Since(taskStart).
	start := time.Now()
	for time.Since(start) == 0 {
		// Spin until the monotonic clock advances at least one tick so the
		// duration is provably > 0 on every platform.
	}
	tm.addTask(TaskTiming{
		Phase:    1,
		Name:     "boom",
		Duration: time.Since(start).Seconds(),
		OK:       false,
		Err:      "exploded",
	})
	tm.addTask(TaskTiming{Phase: 1, Name: "fine", Duration: 0.1, OK: true})

	var failed *TaskTiming
	for i, tk := range tm.tasksSnapshot() {
		if tk.Name == "boom" {
			ts := tm.tasksSnapshot()[i]
			failed = &ts
			break
		}
	}
	if failed == nil {
		t.Fatal("failed task 'boom' not found in snapshot")
	}
	if failed.OK {
		t.Error("failed task should have OK=false")
	}
	if failed.Err == "" {
		t.Error("failed task should have a non-empty Err")
	}
	if failed.Duration <= 0 {
		t.Errorf("failed task duration should be > 0, got %v", failed.Duration)
	}
}

// TestComputeStatus covers the three terminal states the run-meta writer
// derives from the final error and recorded task outcomes:
//   - nil error                       => "success"
//   - error + at least one OK task    => "partial"
//   - error + only failed tasks       => "failed"
func TestComputeStatus(t *testing.T) {
	someErr := errors.New("phase 2: task x: boom")

	cases := []struct {
		name  string
		err   error
		tasks []TaskTiming
		want  string
	}{
		{
			name:  "nil error is success regardless of tasks",
			err:   nil,
			tasks: []TaskTiming{{Name: "a", OK: false, Err: "ignored"}},
			want:  "success",
		},
		{
			name: "error with a successful task is partial",
			err:  someErr,
			tasks: []TaskTiming{
				{Name: "ok", OK: true},
				{Name: "bad", OK: false, Err: "boom"},
			},
			want: "partial",
		},
		{
			name: "error with only failed tasks is failed",
			err:  someErr,
			tasks: []TaskTiming{
				{Name: "bad1", OK: false, Err: "boom"},
				{Name: "bad2", OK: false, Err: "boom"},
			},
			want: "failed",
		},
		{
			name:  "error with no tasks at all is failed",
			err:   someErr,
			tasks: nil,
			want:  "failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm := &RunTimings{}
			for _, tk := range tc.tasks {
				tm.addTask(tk)
			}
			if got := computeStatus(tc.err, tm); got != tc.want {
				t.Errorf("computeStatus: got %q, want %q", got, tc.want)
			}
		})
	}
}
