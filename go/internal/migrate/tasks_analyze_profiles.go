package migrate

import (
	"context"
	"encoding/json"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// runAnalyzeProfileRules walks every migrated quality profile and
// emits one JSONL ProfileFinding record per #226 yellow criterion
// detected in its rule set. The summary report reads this output to
// move QPs from Succeeded into NearPerfect with rule-key listings in
// the Issues column.
//
// No SonarQube Cloud API calls are made — this task is a pure
// re-read of extract data, so it's safe to re-run.
func runAnalyzeProfileRules(ctx context.Context, e *Executor) error {
	counter := NewTaskCounter("analyzeProfileRules")

	// Pre-load extract data once and index it.
	activeByProfile := indexExtractByServerAndField(e, "getActiveProfileRules", "profileKey")
	deactivatedByProfile := indexExtractByServerAndField(e, "getDeactivatedProfileRules", "profileKey")
	baseByServer := indexBaseRules(e)

	err := forEachMigrateItem(ctx, e, "analyzeProfileRules", "createProfiles",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			cloudKey := extractField(item, "cloud_profile_key")
			sourceKey := extractField(item, "source_profile_key")
			serverURL := extractField(item, "server_url")
			if cloudKey == "" || sourceKey == "" {
				return nil
			}
			input := ProfileAnalysisInput{
				CloudProfileKey:           cloudKey,
				ProfileName:               extractField(item, "name"),
				Language:                  extractField(item, "language"),
				ActiveRules:               activeByProfile[serverURL+"\x00"+sourceKey],
				DeactivatedInheritedRules: deactivatedByProfile[serverURL+"\x00"+sourceKey],
				BaseRulesByKey:            baseByServer[serverURL],
			}
			findings := AnalyzeProfile(input)
			for _, f := range findings {
				b, mErr := json.Marshal(f)
				if mErr != nil {
					continue
				}
				if err := w.WriteOne(b); err != nil {
					return err
				}
			}
			counter.Success()
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

// indexExtractByServerAndField groups extract items by
// (serverURL, value-of-field) into a single string key
// "<serverURL>\x00<value>". The \x00 separator avoids any collision
// with serverURL strings that happen to contain the source key.
func indexExtractByServerAndField(e *Executor, task, field string) map[string][]json.RawMessage {
	items, _ := readExtractItems(e, task)
	out := make(map[string][]json.RawMessage, len(items))
	for _, it := range items {
		val := extractField(it.Data, field)
		if val == "" {
			continue
		}
		key := it.ServerURL + "\x00" + val
		out[key] = append(out[key], it.Data)
	}
	return out
}

// indexBaseRules indexes getRules extract items by serverURL → rule
// key → raw record. Each migrated profile's analysis pulls the
// inner map for its server in O(1).
func indexBaseRules(e *Executor) map[string]map[string]json.RawMessage {
	items, _ := readExtractItems(e, "getRules")
	out := make(map[string]map[string]json.RawMessage)
	for _, it := range items {
		k := extractField(it.Data, "key")
		if k == "" {
			continue
		}
		inner := out[it.ServerURL]
		if inner == nil {
			inner = make(map[string]json.RawMessage)
			out[it.ServerURL] = inner
		}
		inner[k] = it.Data
	}
	return out
}
