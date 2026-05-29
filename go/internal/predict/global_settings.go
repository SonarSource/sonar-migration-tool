package predict

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// synthesizeSetGlobalSettings produces a setGlobalSettings JSONL output
// in the predictive run directory, mirroring what the real migrate task
// would write — minus the SQC API calls.
//
// For each customised SQS global setting × each target SQC org, the
// outcome is one of:
//
//   - skipped/not-on-sqc when the setting key is in the curated
//     migrate.IsSQSOnlyGlobalSetting list (#237 — these have no SQC
//     equivalent at any scope, real-migrate would skip them too).
//   - applied (predicted) otherwise. Real-migrate may still surface a
//     project-scope-only or org-level rejection for these at runtime,
//     but the predict pipeline can't know that without hitting SQC.
//
// The output schema matches summary.parseOutcomes so the existing
// Global Settings section renders without any changes.
func synthesizeSetGlobalSettings(exportDir, runDir string, extractMapping structure.ExtractMapping, orgLookup map[string]string) error {
	// SQS-side raw settings + their defaults from the extract.
	rawItems, err := structure.ReadExtractData(exportDir, extractMapping, "getServerSettings")
	if err != nil {
		return fmt.Errorf("reading getServerSettings extract: %w", err)
	}
	if len(rawItems) == 0 {
		return nil
	}
	defItems, err := structure.ReadExtractData(exportDir, extractMapping, "getServerSettingsDefinitions")
	if err != nil {
		return fmt.Errorf("reading getServerSettingsDefinitions extract: %w", err)
	}
	defaults := make(map[string]string, len(defItems))
	for _, di := range defItems {
		key := jsonStringField(di.Data, "key")
		if key == "" {
			continue
		}
		defaults[key] = jsonStringField(di.Data, "defaultValue")
	}

	// Target SQC orgs — same source the real-migrate setup uses.
	orgs := collectTargetOrgs(orgLookup)
	if len(orgs) == 0 {
		return nil
	}

	store := common.NewDataStore(runDir)
	w, err := store.Writer("setGlobalSettings")
	if err != nil {
		return err
	}

	// One JSONL record per (customised) setting, holding one outcome
	// per target org. Dedup customised records by key so a setting
	// pulled from multiple source extracts (one per server) still ends
	// up as a single report row.
	seenKey := make(map[string]bool, len(rawItems))
	for _, it := range rawItems {
		key := jsonStringField(it.Data, "key")
		if key == "" || seenKey[key] {
			continue
		}
		// Drop internal settings (#244) — sonar-tools _SQ_INTERNAL_SETTINGS.
		if migrate.IsInternalSqsSetting(key) {
			continue
		}
		if !migrate.IsSettingCustomized(it.Data, defaults[key]) {
			continue
		}
		seenKey[key] = true

		// Honor the curated silent-skip list (read-only keys like
		// sonar.core.startTime, plus SQS-only feature flags whose
		// value matches SQS's default). Real-migrate drops these in
		// partitionSQSOnlySettings; predict mirrors that so the two
		// reports stay consistent.
		if migrate.IsSilentlySkippedGlobalSetting(key, it.Data) {
			continue
		}

		rec, ok := buildPredictedOutcomeRecord(key, it.Data, orgs)
		if !ok {
			continue
		}
		b, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		if err := w.WriteOne(b); err != nil {
			return err
		}
	}

	// Default-value sweep (#244). For every non-internal key in the
	// getServerSettingsDefinitions catalog that wasn't surfaced by
	// the loop above (because it was either uncustomised or absent
	// from getServerSettings entirely), emit a section-level Skipped
	// outcome so the predictive report inventories the full settings
	// catalog.
	defItems, _ = structure.ReadExtractData(exportDir, extractMapping, "getServerSettingsDefinitions")
	for _, di := range defItems {
		key := jsonStringField(di.Data, "key")
		if key == "" || seenKey[key] {
			continue
		}
		if migrate.IsInternalSqsSetting(key) {
			continue
		}
		// Honor the curated SQS-only list — its per-key handlers
		// decide silent-skip vs surface-as-Skipped based on value.
		// For a setting at the SQS default, the handler returns
		// SkipSilently and we should NOT emit a default-value row
		// here either.
		if _, isSQSOnly := migrate.EvaluateSQSOnlyGlobalSetting(key, di.Data); isSQSOnly {
			continue
		}
		if migrate.IsSilentlySkippedGlobalSetting(key, di.Data) {
			continue
		}
		seenKey[key] = true
		rec := map[string]any{
			"key": key,
			"outcomes": []map[string]string{{
				"org":    "",
				"status": "skipped",
				"reason": "default-value",
				"detail": migrate.SkipDetailDefaultValue,
			}},
		}
		b, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		if err := w.WriteOne(b); err != nil {
			return err
		}
	}
	return nil
}

// buildPredictedOutcomeRecord constructs the JSONL record for a single
// setting key. Both branches now emit a single section-level outcome
// (Org="") so the predictive report shows one row per setting,
// regardless of how many SQC orgs the customer is migrating to (#240).
//
//   - SQS-only with a non-default value → Skipped + standard
//     "cannot be migrated" detail.
//   - Anything else → Applied (predicted) + "applied for each
//     project instead" detail.
//
// Returns (nil, false) when the key is SQS-only but at default — that
// row should not appear in the report.
func buildPredictedOutcomeRecord(key string, raw json.RawMessage, orgs []string) (map[string]any, bool) {
	value := jsonStringField(raw, "value")
	_ = orgs // org count is no longer relevant for the report row count

	if note, isSQSOnly := migrate.EvaluateSQSOnlyGlobalSetting(key, raw); isSQSOnly {
		return map[string]any{
			"key":   key,
			"value": value,
			"outcomes": []map[string]string{{
				"org":    "",
				"status": "skipped",
				"reason": "sqs-only",
				"detail": note,
			}},
		}, true
	}
	return map[string]any{
		"key":   key,
		"value": value,
		"outcomes": []map[string]string{{
			"org":    "",
			"status": "applied",
			"reason": "",
			"detail": "Setting does not exist at global org level in SQC, will be applied for each project instead",
		}},
	}, true
}

// collectTargetOrgs returns the sorted list of distinct, non-skipped
// SonarCloud org keys discovered in organizations.csv.
func collectTargetOrgs(orgLookup map[string]string) []string {
	seen := make(map[string]bool, len(orgLookup))
	for _, sc := range orgLookup {
		if shouldSkipOrg(sc) {
			continue
		}
		seen[sc] = true
	}
	out := make([]string, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

// jsonStringField pulls a top-level string field out of a JSON object
// blob without allocating a full map per call. Returns "" when missing
// or non-string.
func jsonStringField(raw json.RawMessage, key string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	v, ok := obj[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}
