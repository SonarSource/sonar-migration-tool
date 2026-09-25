// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

// ChunkWriter writes JSONL output for a single task.
// Thread-safe: concurrent goroutines can call WriteChunk / WriteOne
// and each gets a unique results.N.jsonl file via atomic indexing.
type ChunkWriter struct {
	dir      string
	chunkIdx int32 // atomic, 0-based; file names are 1-indexed
}

// NewChunkWriter creates a writer for the given task directory,
// creating it if necessary.
//
// The index is seeded past the highest results.N.jsonl already in the
// directory, so a writer opened on a directory a previous run wrote to
// appends instead of overwriting (#604). It used to start at 0
// unconditionally, which made a resumed run re-use results.1 onward: a
// resume that writes FEWER rows than the first attempt truncated the rows
// it re-wrote and left the first attempt's tail behind, so the run
// directory ended up holding a mixture of both attempts with the stale
// rows winning. importProjectData is the one task that re-enters an
// existing directory — filterCompleted gates every other task on
// TaskDirExists — and its rows drive the whole per-project migration
// report, so the corruption surfaced as a fully-migrated project being
// reported Skipped or Failed.
//
// Readers resolve the resulting duplicates by taking the last record for
// a given key; DataStore.Records reads chunks in numeric index order so
// "last" means "written by the most recent attempt".
func NewChunkWriter(taskDir string) (*ChunkWriter, error) {
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating task dir %s: %w", taskDir, err)
	}
	start, err := highestChunkIndex(taskDir)
	if err != nil {
		return nil, err
	}
	return &ChunkWriter{dir: taskDir, chunkIdx: start}, nil
}

// highestChunkIndex returns the largest N of the results.N.jsonl files
// directly under dir, or 0 when there are none. Files that do not match
// the chunk naming scheme are ignored rather than treated as an error:
// task directories are also where ad-hoc artifacts land, and an
// unreadable neighbour must not stop the task from writing.
func highestChunkIndex(dir string) (int32, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading task dir %s: %w", dir, err)
	}
	var highest int32
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		idx, ok := chunkIndex(entry.Name())
		if ok && idx > highest {
			highest = idx
		}
	}
	return highest, nil
}

// Chunk files are named "<chunkFilePrefix><N><chunkFileSuffix>". The name
// is written in one place and parsed in another (chunkIndex), so both
// share these.
const (
	chunkFilePrefix = "results."
	chunkFileSuffix = ".jsonl"
)

// chunkIndex parses N out of a "results.N.jsonl" file name. The second
// return is false for any other name, including "results.jsonl",
// "results.-1.jsonl" and "results.0.jsonl" — indices are 1-based, so 0 is
// not a name this package ever writes.
func chunkIndex(name string) (int32, bool) {
	digits, ok := strings.CutPrefix(name, chunkFilePrefix)
	if !ok {
		return 0, false
	}
	digits, ok = strings.CutSuffix(digits, chunkFileSuffix)
	if !ok || digits == "" {
		return 0, false
	}
	// ParseInt accepts a leading sign and "+1"/"-1" would collide with
	// "1" once parsed; reject anything that is not plain digits.
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	idx, err := strconv.ParseInt(digits, 10, 32)
	if err != nil || idx <= 0 {
		return 0, false
	}
	return int32(idx), true
}

// WriteChunk writes a slice of raw JSON objects as JSONL to results.N.jsonl.
// No-op for empty slices.
func (w *ChunkWriter) WriteChunk(objects []json.RawMessage) error {
	if len(objects) == 0 {
		return nil
	}
	idx := atomic.AddInt32(&w.chunkIdx, 1) // 1-indexed
	path := filepath.Join(w.dir, fmt.Sprintf("%s%d%s", chunkFilePrefix, idx, chunkFileSuffix))
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	defer f.Close()
	for _, obj := range objects {
		if _, err := f.Write(obj); err != nil {
			return err
		}
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	return nil
}

// WriteOne writes a single JSON object as a one-line chunk file.
func (w *ChunkWriter) WriteOne(obj json.RawMessage) error {
	return w.WriteChunk([]json.RawMessage{obj})
}
