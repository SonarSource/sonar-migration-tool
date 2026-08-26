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

// ReadExtractData reads all JSONL items for a given task key across all extracts.
func ReadExtractData(directory string, mapping ExtractMapping, key string) ([]ExtractItem, error) {
	var items []ExtractItem
	for serverURL, extractID := range mapping {
		taskDir := filepath.Join(directory, extractID, key)
		raw, err := readTaskDir(taskDir)
		if err != nil {
			continue // task may not exist for this extract
		}
		for _, r := range raw {
			items = append(items, ExtractItem{ServerURL: serverURL, Data: r})
		}
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
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var all []json.RawMessage
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
			all = append(all, items...)
			continue
		}
		all = append(all, items...)
	}
	return all, nil
}
