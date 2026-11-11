// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
)

// ExtractIndex serves one project's records of an extract task without
// decoding every record of every extract in the merged set (#690).
//
// ExtractItems streams a task across the whole merged set on every call, and
// the migrate project-data loaders call it about ten times per branch. Import
// cost therefore grew with branches × total extract size, which is quadratic
// in instance size, and every partial extract left over from an earlier
// tranche (#664) was re-read by every later one.
//
// The index is built lazily, once per (extract, task): one pass that decodes
// only the routing fields of each record and remembers which chunk files hold
// each project. Supersession is computed once per (server, task) from that
// same pass. A project read then opens only that project's chunk files.
//
// It assumes the extracts it reads do not change while it is in use, which
// holds for the length of a migrate run. Safe for concurrent use.
type ExtractIndex struct {
	directory string
	mapping   ExtractMapping

	mu    sync.Mutex
	tasks map[taskRef]*taskIndex
	plans map[planRef]*readPlan
}

type taskRef struct{ extractID, key string }

type planRef struct{ serverURL, key string }

// taskIndex locates the records of one task in one extract.
type taskIndex struct {
	once  sync.Once
	files []string // chunk file paths, in eachTaskDirRecord order
	// owned maps a record's recordProjectKey to the files (indices into
	// files, ascending) holding such records. Its keys are exactly the
	// project keys scannedTaskKeys collects from the task output.
	owned map[string][]int
	// alias maps the other spellings a record carries (project, projectKey)
	// when they differ from its recordProjectKey, so readers that match on
	// a different field (hotspotHeader) still find the record.
	alias map[string][]int
}

// readPlan is planServerReads for one (server, task), computed once.
type readPlan struct {
	once       sync.Once
	ids        []string
	superseded []supersession
}

// NewExtractIndex returns an empty index over the merged set of mapping.
func NewExtractIndex(directory string, mapping ExtractMapping) *ExtractIndex {
	return &ExtractIndex{
		directory: directory,
		mapping:   mapping,
		tasks:     make(map[taskRef]*taskIndex),
		plans:     make(map[planRef]*readPlan),
	}
}

// ProjectItems streams the records of task key from serverURL's extracts
// that ExtractItems would yield and that belong to projectKey: the record's
// projectKey, project or (failing both) key field equals projectKey. Records
// come in the same relative order as from ExtractItems.
//
// Portfolio- and group-scoped tasks, and an empty projectKey, fall back to
// filtering a full ExtractItems stream.
func (x *ExtractIndex) ProjectItems(key, serverURL, projectKey string) func(yield func(ExtractItem) bool) {
	if projectKey == "" || extract.IsPortfolioScopedTask(key) || extract.IsGroupScopedTask(key) {
		return x.filteredItems(key, serverURL, projectKey)
	}
	return func(yield func(ExtractItem) bool) {
		plan := x.plan(serverURL, key)
		for i, id := range plan.ids {
			var sup supersession
			if plan.superseded != nil {
				sup = plan.superseded[i]
			}
			if sup.all {
				continue // a later extract re-extracted every project
			}
			idx := x.task(id, key)
			for _, f := range idx.filesFor(projectKey, sup.keys[projectKey]) {
				for _, r := range readChunkFile(f) {
					h := decodeProjectSpellings(r)
					if !h.has(projectKey) || sup.keys[h.primary()] {
						continue
					}
					if !yield(ExtractItem{ServerURL: serverURL, Data: r}) {
						return
					}
				}
			}
		}
	}
}

// filteredItems is the unindexed equivalent of ProjectItems.
func (x *ExtractIndex) filteredItems(key, serverURL, projectKey string) func(yield func(ExtractItem) bool) {
	return func(yield func(ExtractItem) bool) {
		for item := range ExtractItems(x.directory, ExtractMapping{serverURL: x.mapping[serverURL]}, key) {
			if projectKey != "" && !decodeProjectSpellings(item.Data).has(projectKey) {
				continue
			}
			if !yield(item) {
				return
			}
		}
	}
}

// plan returns the memoized read plan for (serverURL, key). It matches
// planServerReads, except that project supersessions take the task-output
// keys from the task indexes instead of re-reading the output.
func (x *ExtractIndex) plan(serverURL, key string) *readPlan {
	x.mu.Lock()
	p, ok := x.plans[planRef{serverURL, key}]
	if !ok {
		p = &readPlan{}
		x.plans[planRef{serverURL, key}] = p
	}
	x.mu.Unlock()
	p.once.Do(func() {
		ids := x.mapping[serverURL]
		if len(ids) >= 2 && extract.IsProjectScopedTask(key) {
			p.ids, p.superseded = ids, supersessionsWith(x.directory, ids, key, x.taskKeys)
			return
		}
		p.ids, p.superseded = planServerReads(x.directory, ids, key)
	})
	return p
}

// taskKeys answers supersessionsWith from the task indexes, so supersession
// costs no pass over the task output beyond the one building the index.
func (x *ExtractIndex) taskKeys(id, key string) map[string]bool {
	owned := x.task(id, key).owned
	keys := make(map[string]bool, len(owned))
	for k := range owned {
		keys[k] = true
	}
	return keys
}

// task returns the built index of one task in one extract.
func (x *ExtractIndex) task(extractID, key string) *taskIndex {
	x.mu.Lock()
	t, ok := x.tasks[taskRef{extractID, key}]
	if !ok {
		t = &taskIndex{}
		x.tasks[taskRef{extractID, key}] = t
	}
	x.mu.Unlock()
	t.once.Do(func() { t.build(filepath.Join(x.directory, extractID, key)) })
	return t
}

// build scans the task directory once. A missing directory leaves the index
// empty, as ExtractItems skips an extract that did not run the task.
func (t *taskIndex) build(dir string) {
	t.owned = make(map[string][]int)
	t.alias = make(map[string][]int)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		fi := len(t.files)
		t.files = append(t.files, filepath.Join(dir, entry.Name()))
		for _, r := range readChunkFile(t.files[fi]) {
			h := decodeProjectSpellings(r)
			primary := h.primary()
			if primary != "" {
				t.owned[primary] = appendFile(t.owned[primary], fi)
			}
			for _, k := range []string{h.ProjectKey, h.Project} {
				if k != "" && k != primary {
					t.alias[k] = appendFile(t.alias[k], fi)
				}
			}
		}
	}
}

// filesFor returns, in ascending order, the chunk files that may hold
// records of projectKey. When the project's own records are all superseded
// only the files reaching it through an alias spelling are left to check.
func (t *taskIndex) filesFor(projectKey string, superseded bool) []string {
	var picked []int
	if !superseded {
		picked = append(picked, t.owned[projectKey]...)
	}
	picked = append(picked, t.alias[projectKey]...)
	slices.Sort(picked)
	picked = slices.Compact(picked)
	files := make([]string, len(picked))
	for i, fi := range picked {
		files[i] = t.files[fi]
	}
	return files
}

// appendFile appends fi unless it is already the last entry, which keeps
// each list ascending and duplicate-free because files are scanned in order.
func appendFile(list []int, fi int) []int {
	if n := len(list); n > 0 && list[n-1] == fi {
		return list
	}
	return append(list, fi)
}

// projectSpellings holds the fields a project-scoped record may name its
// project by. Only these are decoded, so the rest of the record (source text,
// highlighted HTML) is scanned without being copied.
type projectSpellings struct {
	ProjectKey string `json:"projectKey"`
	Project    string `json:"project"`
	Key        string `json:"key"`
}

// decodeProjectSpellings returns no spelling for a record that does not
// decode cleanly, as recordProjectKey does, so the index and supersession
// agree on which project such a record belongs to: none.
func decodeProjectSpellings(record json.RawMessage) projectSpellings {
	var h projectSpellings
	if err := json.Unmarshal(record, &h); err != nil {
		return projectSpellings{}
	}
	return h
}

// primary is recordProjectKey's answer: the first non-empty of projectKey,
// project and key.
func (h projectSpellings) primary() string {
	for _, k := range []string{h.ProjectKey, h.Project, h.Key} {
		if k != "" {
			return k
		}
	}
	return ""
}

// has reports whether the record belongs to projectKey under any spelling
// the index records.
func (h projectSpellings) has(projectKey string) bool {
	return h.primary() == projectKey || h.ProjectKey == projectKey || h.Project == projectKey
}
