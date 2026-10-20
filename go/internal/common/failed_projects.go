// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// FailedProjectsFileName is the file, in an extract's directory, that
// lists the projects extract skipped because a request for them failed
// after every retry. structure leaves these projects out of projects.csv,
// so migrate never migrates a project whose extracted data is incomplete.
const FailedProjectsFileName = "failed_projects.json"

// FailedProject is one project extract skipped after a failed request.
type FailedProject struct {
	Key   string `json:"key"`
	Task  string `json:"task"`  // the task whose request failed
	Error string `json:"error"` // the error that request returned
}

// WriteFailedProjects writes the list to dir, sorted by key, replacing
// any earlier list. An empty list removes the file, so a directory never
// holds a stale one.
func WriteFailedProjects(dir string, list []FailedProject) error {
	path := filepath.Join(dir, FailedProjectsFileName)
	if len(list) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", path, err)
		}
		return nil
	}
	sorted := append([]FailedProject(nil), list...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	data, err := json.MarshalIndent(sorted, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadFailedProjects reads the list from dir. A missing file is an empty
// list.
func ReadFailedProjects(dir string) ([]FailedProject, error) {
	data, err := os.ReadFile(filepath.Join(dir, FailedProjectsFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []FailedProject
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("reading %s: %w", FailedProjectsFileName, err)
	}
	return list, nil
}
