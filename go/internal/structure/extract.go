// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// ExtractMapping maps server URLs to their latest extract IDs.
type ExtractMapping map[string]string

// GetUniqueExtracts scans the export directory for extract runs and returns
// a mapping of server URL → latest extract ID.
func GetUniqueExtracts(directory string) (ExtractMapping, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}

	urlMappings := buildURLMappings(directory, entries)

	result := make(ExtractMapping, len(urlMappings))
	for url, ids := range urlMappings {
		result[url] = latestID(ids)
	}
	return result, nil
}

// buildURLMappings scans extract directories and groups extract IDs by server URL.
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
		var meta struct {
			URL string `json:"url"`
		}
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
		urlMappings[meta.URL][entry.Name()] = true
	}
	return urlMappings
}

// latestID returns the most recent run ID from a set, ordering
// numerically rather than lexicographically (#542) — see
// common.RunIDAfter for why a plain string compare misorders once a
// run's trailing counter grows past two digits.
func latestID(ids map[string]bool) string {
	var latest string
	for id := range ids {
		if common.RunIDAfter(id, latest) {
			latest = id
		}
	}
	return latest
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
// Ordering matches ReadExtractData exactly — mapping iteration is Go map
// order (nondeterministic across servers) and chunk files are visited in
// os.ReadDir order within an extract.
func ExtractItems(directory string, mapping ExtractMapping, key string) func(yield func(ExtractItem) bool) {
	return func(yield func(ExtractItem) bool) {
		for serverURL, extractID := range mapping {
			taskDir := filepath.Join(directory, extractID, key)
			completed, err := eachTaskDirRecord(taskDir, func(r json.RawMessage) bool {
				return yield(ExtractItem{ServerURL: serverURL, Data: r})
			})
			if err != nil {
				continue // task may not exist for this extract
			}
			if !completed {
				return // consumer stopped early
			}
		}
	}
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
