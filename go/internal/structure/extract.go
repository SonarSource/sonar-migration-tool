// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
)

// ExtractMapping maps each server URL to the ordered (oldest to newest)
// extract IDs that together form its merged view (#664): the latest full
// extract plus every partial extract newer than it. When every extract of a
// server is full, the set is exactly the latest one.
type ExtractMapping map[string][]string

// IDs returns the extract IDs of the merged set for serverURL, oldest first.
func (m ExtractMapping) IDs(serverURL string) []string { return m[serverURL] }

// AllIDs returns every extract ID of every server, ordered by server URL
// and then oldest first, so callers get a deterministic order.
func (m ExtractMapping) AllIDs() []string {
	urls := make([]string, 0, len(m))
	for url := range m {
		urls = append(urls, url)
	}
	sort.Strings(urls)
	var ids []string
	for _, url := range urls {
		ids = append(ids, m[url]...)
	}
	return ids
}

// extractMeta is the part of extract.json the structure package reads.
type extractMeta struct {
	URL         string   `json:"url"`
	Partial     bool     `json:"partial"`
	ProjectKeys []string `json:"project_keys"`
}

// GetUniqueExtracts scans the export directory for extract runs and returns
// a mapping of server URL to the extract IDs forming its merged view. A
// legacy extract.json without a "partial" field is a full extract.
func GetUniqueExtracts(directory string) (ExtractMapping, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}

	urlMappings := buildURLMappings(directory, entries)

	result := make(ExtractMapping, len(urlMappings))
	for url, extracts := range urlMappings {
		result[url] = mergedSet(extracts)
	}
	return result, nil
}

// buildURLMappings scans extract directories and groups extract IDs by
// server URL, remembering whether each one is partial.
func buildURLMappings(directory string, entries []os.DirEntry) map[string]map[string]bool {
	urlMappings := make(map[string]map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metaPath := filepath.Join(directory, entry.Name(), "extract.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta extractMeta
		if err := json.Unmarshal(data, &meta); err != nil || meta.URL == "" {
			continue
		}
		if !common.IsValidRunID(entry.Name()) {
			// A directory name that isn't a well-formed run ID must never
			// become a candidate: RunIDAfter falls back to a plain string
			// compare for anything that doesn't parse as "<prefix>-<digits>",
			// and since real run IDs always start with a digit, a rogue
			// name (e.g. planted by an attacker who can write to the
			// export directory) would beat every legitimate dated run in
			// that string compare (#550). Filtering here keeps
			// RunIDAfter's fallback intact for its other legitimate
			// callers/tests while closing this path off entirely.
			slog.Debug("skipping non-conforming extract directory name", "name", entry.Name())
			continue
		}
		if urlMappings[meta.URL] == nil {
			urlMappings[meta.URL] = make(map[string]bool)
		}
		urlMappings[meta.URL][entry.Name()] = meta.Partial
	}
	return urlMappings
}

// mergedSet picks, from a server's extracts (ID to partial flag), the latest
// full extract and every partial extract newer than it, oldest first. With
// no full extract, all partial extracts form the set.
//
// Ordering is numeric rather than lexicographic (#542) — see
// common.RunIDAfter for why a plain string compare misorders once a run's
// trailing counter grows past two digits.
func mergedSet(extracts map[string]bool) []string {
	ids := make([]string, 0, len(extracts))
	for id := range extracts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return common.RunIDAfter(ids[j], ids[i]) })

	start := 0
	for i := len(ids) - 1; i >= 0; i-- {
		if !extracts[ids[i]] {
			start = i
			break
		}
	}
	return ids[start:]
}

// MultiExtractReader reads JSONL objects from the named task across all extract
// runs in the mapping. It yields (serverURL, rawObject) pairs.
// Yields (serverURL, rawObject) pairs across all extract runs.
type ExtractItem struct {
	ServerURL string
	Data      json.RawMessage
}

// ExtractItems streams every JSONL record of the named task across all
// extract runs in the mapping, yielding (serverURL, rawObject) pairs one
// chunk file at a time. Peak memory is one chunk file rather than the whole
// task, which matters because getProjectSourceCode embeds full file text
// and per-line highlighted HTML for every file on the instance.
//
// Error policy is identical to ReadExtractData, which is now a thin wrapper
// over this:
//   - an extract whose task directory is missing is skipped entirely (the
//     extract may simply not have run that task);
//   - a single unreadable JSONL file inside a task directory is logged as a
//     warning and the records ReadJSONLFile parsed before the failure are
//     still yielded (#312, #314).
//
// No error is yielded because none can escape: every failure mode above is
// absorbed by design. Consumers stop early with break.
//
// A server whose merged set holds several extracts (#664) yields project-
// scoped records from every one, oldest first, dropping a record when a later
// extract of the set re-extracted its project. Portfolio-scoped records are
// merged the same way per portfolio (#666). Server-wide tasks are read from
// the newest extract that finished them (newestFinished). A single-extract
// set takes the fast path with no extra parsing.
//
// Ordering matches ReadExtractData exactly — mapping iteration is Go map
// order (nondeterministic across servers) and chunk files are visited in
// os.ReadDir order within an extract.
func ExtractItems(directory string, mapping ExtractMapping, key string) func(yield func(ExtractItem) bool) {
	return func(yield func(ExtractItem) bool) {
		for serverURL, ids := range mapping {
			ids, superseded := mergePlan(directory, ids, key)
			for i, extractID := range ids {
				var sup supersession
				if superseded != nil {
					sup = superseded[i]
				}
				completed, err := eachTaskDirRecord(filepath.Join(directory, extractID, key), recordEmitter(serverURL, sup, yield))
				if err != nil {
					continue // task may not exist for this extract
				}
				if !completed {
					return // consumer stopped early
				}
			}
		}
	}
}

// mergePlan decides how the extracts of one server's merged set are read for
// the task: project, portfolio and template-scoped tasks read every extract
// with the records a later extract re-extracted dropped (the returned
// supersessions, one per extract); any other, server-wide task is read from
// the newest extract that finished it. A single-extract set is returned as is.
func mergePlan(directory string, ids []string, key string) ([]string, []supersession) {
	if len(ids) < 2 {
		return ids, nil
	}
	switch {
	case extract.IsProjectScopedTask(key):
		return ids, supersessions(directory, ids, key)
	case extract.IsPortfolioScopedTask(key):
		return ids, portfolioSupersessions(directory, ids, key)
	case extract.IsTemplateScopedTask(key):
		return ids, templateSupersessions(directory, ids, key)
	}
	return newestFinished(directory, ids, key), nil
}

// recordEmitter yields each record of an extract as an ExtractItem, skipping
// those sup says a later extract re-extracted.
func recordEmitter(serverURL string, sup supersession, yield func(ExtractItem) bool) func(json.RawMessage) bool {
	covered := sup.any()
	return func(r json.RawMessage) bool {
		if covered && sup.covers(r) {
			return true
		}
		return yield(ExtractItem{ServerURL: serverURL, Data: r})
	}
}

// supersession describes what later extracts of the merged set re-extracted:
// every project (all) or a list of project keys.
type supersession struct {
	all  bool
	keys map[string]bool
	// portfolios is true when keys are portfolio keys (#666), matched against
	// the portfolio a record belongs to rather than its project.
	portfolios bool
	// templates is true when keys are permission template ids (#665),
	// matched against the template a record belongs to.
	templates bool
	// templateField is the record field holding the template id: "id" for
	// getTemplates itself, "templateId" for the permission tasks (their own
	// "id" would be a group or user id).
	templateField string
}

func (s supersession) any() bool { return s.all || len(s.keys) > 0 }

// covers reports whether record belongs to a project a later extract
// re-extracted, so the older copy must be dropped.
func (s supersession) covers(record json.RawMessage) bool {
	if s.all {
		return true
	}
	if s.portfolios {
		return s.keys[recordPortfolioKey(record)]
	}
	if s.templates {
		return s.keys[recordStringField(record, s.templateField)]
	}
	return s.keys[recordProjectKey(record)]
}

// supersessions computes, for each extract of ids, the projects that a later
// extract of the set re-extracted for the task. A later extract that has no
// output for the task superseded nothing. A later extract limited to
// specific projects lists them in extract.json; a partial one without a
// project list (--objects only) covers every project.
func supersessions(directory string, ids []string, key string) []supersession {
	result := make([]supersession, len(ids))
	var later supersession
	for i := len(ids) - 1; i >= 0; i-- {
		result[i] = supersession{all: later.all, keys: copyKeys(later.keys)}
		if _, err := os.Stat(filepath.Join(directory, ids[i], key)); err != nil {
			continue
		}
		meta := readExtractMeta(directory, ids[i])
		if !meta.Partial {
			continue // a full extract is always first in the set
		}
		if len(meta.ProjectKeys) == 0 {
			later.all = true
			continue
		}
		if later.keys == nil {
			later.keys = make(map[string]bool, len(meta.ProjectKeys))
		}
		for _, k := range meta.ProjectKeys {
			later.keys[k] = true
		}
	}
	return result
}

// newestFinished narrows the extracts to read a server-wide task from (gates,
// profiles, groups, users...) to the newest one that finished the task. Such a
// task fetches the whole server each time it runs, so a later run replaces an
// earlier one; reading both would repeat every record, which consumers that
// append (groups, users) would count twice. When no extract finished the task
// the set is returned unchanged, so nothing readable is dropped.
func newestFinished(directory string, ids []string, key string) []string {
	for i := len(ids) - 1; i >= 0; i-- {
		if taskFinished(directory, ids[i], key) {
			return ids[i : i+1]
		}
	}
	return ids
}

// taskFinished reports whether the extract has the task's output and, when it
// records finished tasks in .done, that it was marked finished. An extract
// made before .done existed has no such directory, so its task output is
// taken as finished.
func taskFinished(directory, id, key string) bool {
	if _, err := os.Stat(filepath.Join(directory, id, key)); err != nil {
		return false
	}
	doneDir := filepath.Join(directory, id, ".done")
	if _, err := os.Stat(doneDir); err != nil {
		return true
	}
	_, err := os.Stat(filepath.Join(doneDir, key))
	return err == nil
}

// portfolioSupersessions computes, for each extract of ids, the portfolios a
// later extract of the set re-extracted for the task (#666). Unlike projects,
// the portfolio keys are read from the later extract's own output for the
// task: a slice is defined by a regexp, so extract.json cannot list them. A
// later extract with no output for the task superseded nothing.
func portfolioSupersessions(directory string, ids []string, key string) []supersession {
	result := make([]supersession, len(ids))
	var later map[string]bool
	for i := len(ids) - 1; i >= 0; i-- {
		result[i] = supersession{keys: copyKeys(later), portfolios: true}
		_, _ = eachTaskDirRecord(filepath.Join(directory, ids[i], key), func(r json.RawMessage) bool {
			if k := recordPortfolioKey(r); k != "" {
				if later == nil {
					later = make(map[string]bool)
				}
				later[k] = true
			}
			return true
		})
	}
	return result
}

// templateSupersessions computes, for each extract of ids, the permission
// templates a later extract of the set re-extracted (#665). The ids come from
// the later extract's own getTemplates output, not from the task being read:
// a re-extracted template with no group or user permissions writes no record
// to getTemplateGroups* / getTemplateUsers*, yet must still replace the older,
// stale ones. A later extract that did not finish getTemplates (e.g. one
// limited by --objects) superseded nothing.
func templateSupersessions(directory string, ids []string, key string) []supersession {
	field := "templateId"
	if key == "getTemplates" {
		field = "id"
	}
	result := make([]supersession, len(ids))
	var later map[string]bool
	for i := len(ids) - 1; i >= 0; i-- {
		result[i] = supersession{keys: copyKeys(later), templates: true, templateField: field}
		if !taskFinished(directory, ids[i], "getTemplates") {
			continue
		}
		_, _ = eachTaskDirRecord(filepath.Join(directory, ids[i], "getTemplates"), func(r json.RawMessage) bool {
			if id := recordStringField(r, "id"); id != "" {
				if later == nil {
					later = make(map[string]bool)
				}
				later[id] = true
			}
			return true
		})
	}
	return result
}

// recordStringField returns the string value of one top-level field of a
// JSONL record, or "" when it is absent or the record does not parse.
func recordStringField(record json.RawMessage, field string) string {
	var obj map[string]any
	if err := json.Unmarshal(record, &obj); err != nil {
		return ""
	}
	s, _ := obj[field].(string)
	return s
}

func copyKeys(m map[string]bool) map[string]bool {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func readExtractMeta(directory, id string) extractMeta {
	var meta extractMeta
	data, err := os.ReadFile(filepath.Join(directory, id, "extract.json"))
	if err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	return meta
}

// recordProjectKey returns the project a project-scoped record belongs to:
// the first non-empty of projectKey, project and key (hotspot records use
// "project"; getProjects and getProjectDetails use "key").
func recordProjectKey(record json.RawMessage) string {
	var obj struct {
		ProjectKey string `json:"projectKey"`
		Project    string `json:"project"`
		Key        string `json:"key"`
	}
	if err := json.Unmarshal(record, &obj); err != nil {
		return ""
	}
	for _, k := range []string{obj.ProjectKey, obj.Project, obj.Key} {
		if k != "" {
			return k
		}
	}
	return ""
}

// recordPortfolioKey returns the portfolio a portfolio-scoped record belongs
// to: portfolioKey (getPortfolioProjects) or key (getPortfolios,
// getPortfolioDetails).
func recordPortfolioKey(record json.RawMessage) string {
	var obj struct {
		PortfolioKey string `json:"portfolioKey"`
		Key          string `json:"key"`
	}
	if err := json.Unmarshal(record, &obj); err != nil {
		return ""
	}
	if obj.PortfolioKey != "" {
		return obj.PortfolioKey
	}
	return obj.Key
}

// ReadExtractData reads all JSONL items for a given task key across all extracts.
//
// Prefer ExtractItems for anything whose size scales with the instance —
// this materializes every record of every project at once.
func ReadExtractData(directory string, mapping ExtractMapping, key string) ([]ExtractItem, error) {
	var items []ExtractItem
	for item := range ExtractItems(directory, mapping, key) {
		items = append(items, item)
	}
	return items, nil
}

// readTaskDir reads all JSONL files from a task directory. A failure
// on a single file is logged as a warning and the file is skipped —
// the remaining files still contribute their records (#314). Aborting
// on the first per-file error used to silently throw away the entire
// task's data, which caused #312 (a single oversize source-code
// record disabling project-data migration across the whole run).
//
// Returns an error only when the task directory itself can't be
// listed; per-file failures are visible via slog warnings.
func readTaskDir(dir string) ([]json.RawMessage, error) {
	var all []json.RawMessage
	_, err := eachTaskDirRecord(dir, func(r json.RawMessage) bool {
		all = append(all, r)
		return true
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// eachTaskDirRecord yields every JSONL record under dir, one chunk file at
// a time. It is the shared body of readTaskDir and ExtractItems, so both
// observe exactly the same error policy (see readTaskDir's doc).
//
// Returns an error only when the directory itself can't be listed.
// completed reports whether iteration ran to the end; false means the
// consumer stopped early, so no further files were opened.
func eachTaskDirRecord(dir string, yield func(json.RawMessage) bool) (completed bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		items, err := common.ReadJSONLFile(path)
		if err != nil {
			slog.Warn("readTaskDir: skipping unreadable JSONL file (records from other files in this task are still loaded)",
				"file", path, "err", err)
			// Keep whatever the partial read returned before the
			// error — ReadJSONLFile returns the records it parsed
			// up to the failure point, which is better than nothing
			// for callers that downstream process per-record.
		}
		for _, r := range items {
			if !yield(r) {
				return false, nil
			}
		}
	}
	return true, nil
}
