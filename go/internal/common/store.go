// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DataStore manages on-disk JSONL task output and tracks completion.
type DataStore struct {
	baseDir   string
	mu        sync.RWMutex
	completed map[string]bool
}

// NewDataStore creates a DataStore rooted at the given directory.
func NewDataStore(baseDir string) *DataStore {
	return &DataStore{
		baseDir:   baseDir,
		completed: make(map[string]bool),
	}
}

// taskDirSanitizer maps characters that are legal in task names but
// illegal in a Windows path component to "_". Some task names embed a
// ":" (e.g. "getTemplateGroupsScanners:apply"), which is fine on
// Linux/macOS but rejected by NTFS — Windows forbids \ / : * ? " < > |
// in file and directory names. Sanitizing here, at the single point
// where a task name becomes a directory path, keeps the on-disk layout
// unchanged on POSIX (these characters are otherwise unused in task
// names) while making the migrate command runnable on Windows.
// See issue #486.
var taskDirSanitizer = strings.NewReplacer(
	"\\", "_", "/", "_", ":", "_", "*", "_",
	"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
)

// taskDir returns the filesystem-safe directory path for a task.
func (ds *DataStore) taskDir(taskName string) string {
	return filepath.Join(ds.baseDir, taskDirSanitizer.Replace(taskName))
}

// BaseDir returns the root directory.
func (ds *DataStore) BaseDir() string {
	return ds.baseDir
}

// Writer returns a ChunkWriter for the named task.
func (ds *DataStore) Writer(taskName string) (*ChunkWriter, error) {
	return NewChunkWriter(ds.taskDir(taskName))
}

// Records streams every JSONL object of a completed task, one chunk file at
// a time. Peak memory is one chunk file rather than the whole task.
//
// Error policy is identical to ReadAll, which is now a thin wrapper:
//   - a missing task directory yields nothing (ReadAll's (nil, nil));
//   - any other ReadDir failure, or the first per-file read error, yields
//     (nil, err) and ends iteration without yielding any record from the
//     failing file, preserving ReadAll's all-or-nothing contract.
//
// Note this differs deliberately from structure.ExtractItems, which warns
// and keeps partial records (#312/#314). The two stores have different
// contracts and both are relied upon; the error in this signature is what
// makes the difference visible rather than buried in a comment.
//
// taskName is resolved through taskDir, so Windows sanitization (#486)
// applies here too.
func (ds *DataStore) Records(taskName string) func(yield func(json.RawMessage, error) bool) {
	return func(yield func(json.RawMessage, error) bool) {
		dir := ds.taskDir(taskName)
		names, err := jsonlFileNames(dir)
		if err != nil {
			yield(nil, err)
			return
		}
		for _, name := range names {
			items, err := ReadJSONLFile(filepath.Join(dir, name))
			if err != nil {
				yield(nil, err)
				return
			}
			if !yieldRecords(items, yield) {
				return
			}
		}
	}
}

// jsonlFileNames lists the .jsonl file names directly under dir. A missing
// directory is not an error — the task simply never ran — and returns no
// names, which is what gives ReadAll its (nil, nil).
func jsonlFileNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading task dir %s: %w", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

// yieldRecords passes each record to yield, reporting false as soon as the
// consumer stops so the caller can abandon the remaining files unopened.
func yieldRecords(items []json.RawMessage, yield func(json.RawMessage, error) bool) bool {
	for _, item := range items {
		if !yield(item, nil) {
			return false
		}
	}
	return true
}

// ReadAll returns every JSONL object for a completed task as raw JSON.
//
// Prefer Records for anything whose size scales with the instance — this
// materializes every record of the task at once.
func (ds *DataStore) ReadAll(taskName string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	for item, err := range ds.Records(taskName) {
		if err != nil {
			return nil, err
		}
		all = append(all, item)
	}
	return all, nil
}

// ReadJSONLFile reads a single JSONL file into a slice of raw JSON
// messages. Lines are unbounded in length: project-data extract
// records embed full source files as a JSON string, and large
// generated / minified sources routinely exceed the bufio.Scanner
// default ceiling (and even the bumped 10 MB ceiling we used to
// carry). We use bufio.Reader.ReadBytes('\n') so the only effective
// limit is available memory. A single oversize line previously
// caused readTaskDir to abort the entire task — which silently
// dropped every source record across the migration.
func ReadJSONLFile(path string) ([]json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var items []json.RawMessage
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(string(line))
			if trimmed != "" {
				items = append(items, json.RawMessage(trimmed))
			}
		}
		if err != nil {
			if err == io.EOF {
				return items, nil
			}
			return items, err
		}
	}
}

// MarkComplete marks a task as finished.
func (ds *DataStore) MarkComplete(taskName string) {
	ds.mu.Lock()
	ds.completed[taskName] = true
	ds.mu.Unlock()
}

// IsComplete reports whether a task has been marked complete.
func (ds *DataStore) IsComplete(taskName string) bool {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	return ds.completed[taskName]
}

// TaskDirExists checks if a task's output directory exists on disk
// (for resumability — skip tasks that already ran).
func (ds *DataStore) TaskDirExists(taskName string) bool {
	info, err := os.Stat(ds.taskDir(taskName))
	return err == nil && info.IsDir()
}
