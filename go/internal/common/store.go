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

// BaseDir returns the root directory.
func (ds *DataStore) BaseDir() string {
	return ds.baseDir
}

// Writer returns a ChunkWriter for the named task.
func (ds *DataStore) Writer(taskName string) (*ChunkWriter, error) {
	return NewChunkWriter(filepath.Join(ds.baseDir, taskName))
}

// ReadAll returns every JSONL object for a completed task as raw JSON.
func (ds *DataStore) ReadAll(taskName string) ([]json.RawMessage, error) {
	dir := filepath.Join(ds.baseDir, taskName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading task dir %s: %w", dir, err)
	}
	var all []json.RawMessage
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		items, err := ReadJSONLFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
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
	info, err := os.Stat(filepath.Join(ds.baseDir, taskName))
	return err == nil && info.IsDir()
}
