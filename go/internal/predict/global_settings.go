// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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

	// #251: predict the AI Code Fix migration outcome up front, then
	// suppress the two driving keys from the rest of the loop so we
	// don't double-report them.
	if err := synthesizeAiCodeFixPredictions(exportDir, extractMapping, orgs, w); err != nil {
		return err
	}
	seenKey[migrate.AiCodeFixHiddenSetting] = true
	seenKey[migrate.AiCodeFixSuggestionsSetting] = true

	// #363: all sonar.auth.* keys collapse to a single Skipped row,
	// emitted once after the main loop.
	hasConsolidatedAuth := false

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

		// #363: defer all sonar.auth.* keys; the consolidated row is
		// written once after both loops. Honor the empty-value
		// silent-skip rule by consulting EvaluateSQSOnlyGlobalSetting —
		// when it returns isSQSOnly=false the key carries no value
		// worth surfacing, so it shouldn't trigger the consolidated row.
		if strings.HasPrefix(key, "sonar.auth.") {
			if _, isSQSOnly := migrate.EvaluateSQSOnlyGlobalSetting(key, it.Data); isSQSOnly {
				hasConsolidatedAuth = true
			}
			continue
		}

		// #249: sonar.dbcleaner.branchesToKeepWhenInactive migrates as
		// a regex into the SQC org-scope sonar.branch.longLivedBranches
		// .regex setting. Predict the same Applied outcome here so the
		// report shows it green.
		if key == "sonar.dbcleaner.branchesToKeepWhenInactive" {
			rec := buildPredictedDbCleanerRecord(key, it.Data)
			b, _ := json.Marshal(rec)
			if err := w.WriteOne(b); err != nil {
				return err
			}
			continue
		}

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

	// #363: single consolidated sonar.auth.* row, emitted once if any
	// auth setting carried a value worth surfacing.
	if hasConsolidatedAuth {
		rec := map[string]any{
			"key": migrate.SonarAuthConsolidatedKey,
			"outcomes": []map[string]string{{
				"org":    "",
				"status": "skipped",
				"reason": "sqs-only",
				"detail": migrate.SkipDetailSonarAuthConsolidated,
			}},
		}
		b, err := json.Marshal(rec)
		if err == nil {
			if err := w.WriteOne(b); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildPredictedDbCleanerRecord builds the predictive Applied outcome
// for #249: sonar.dbcleaner.branchesToKeepWhenInactive will be
// migrated as a regex into sonar.branch.longLivedBranches.regex at
// SQC org scope. Returns one section-level outcome (Org="") since
// the regex is the same across orgs.
func buildPredictedDbCleanerRecord(key string, raw json.RawMessage) map[string]any {
	values := extractStringValues(raw)
	regex := migrate.CombineBranchesAsRegex(values)
	detail := fmt.Sprintf("Will be migrated to %s on SonarQube Cloud", "sonar.branch.longLivedBranches.regex")
	if note := migrate.DbCleanerBranchesTransformNote(values, regex); note != "" {
		detail = note
	}
	return map[string]any{
		"key":    key,
		"values": values,
		"outcomes": []map[string]string{{
			"org":    "",
			"status": "applied",
			"detail": detail,
		}},
	}
}

// extractStringValues pulls a top-level "values" string array out of
// a JSON object blob, falling back to a single-element list if only
// the legacy "value" field is set.
func extractStringValues(raw json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	if v, ok := obj["values"]; ok {
		var out []string
		if err := json.Unmarshal(v, &out); err == nil {
			return out
		}
	}
	if v, ok := obj["value"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil && s != "" {
			return []string{s}
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

// synthesizeAiCodeFixPredictions mirrors applyAiCodeFixDecisions on
// the predict side (#251). For each source SQS state, evaluates the
// #251 strategy and writes one JSONL record per AI Code Fix setting
// (sonar.ai.codefix.hidden + sonar.ai.suggestions.enabled). Predict
// emits a single section-level outcome (Org="") per setting key — the
// real migrate task fans out per SQC org so individual PATCH failures
// surface, but predict has no per-org information to predict and
// would otherwise repeat the same row N times in the report.
func synthesizeAiCodeFixPredictions(exportDir string, mapping structure.ExtractMapping,
	orgs []string, w *common.ChunkWriter) error {

	if len(orgs) == 0 {
		return nil
	}
	_ = orgs // org count drives no per-row repetition for AI Code Fix.
	srvSettings, _ := structure.ReadExtractData(exportDir, mapping, "getServerSettings")
	aiConfigs, _ := structure.ReadExtractData(exportDir, mapping, "getAiCodeFixConfig")
	states := migrate.LoadAiCodeFixStates(srvSettings, aiConfigs)
	if len(states) == 0 {
		return nil
	}
	decisions := make([]migrate.AiCodeFixDecision, 0, len(states))
	for _, s := range states {
		decisions = append(decisions, migrate.EvaluateAiCodeFix(s))
	}
	primary := pickPredictPrimary(decisions)

	// Emit a SINGLE section-level outcome per setting key (Org="") —
	// matches the other predict rows, which deliberately collapse the
	// per-org repetition because the predictive report cannot know
	// per-org SQC differences anyway. The migrate task still emits
	// one row per org so failures on a single org surface in the
	// real report.
	if primary.Hidden.Status != "" {
		rec := map[string]any{
			"key":      migrate.AiCodeFixHiddenSetting,
			"outcomes": []map[string]string{predictedAiCodeFixOutcome("", primary.Hidden)},
		}
		if b, err := json.Marshal(rec); err == nil {
			if err := w.WriteOne(b); err != nil {
				return err
			}
		}
	}
	if primary.Suggestions.Status != "" {
		rec := map[string]any{
			"key":      migrate.AiCodeFixSuggestionsSetting,
			"outcomes": []map[string]string{predictedAiCodeFixOutcome("", primary.Suggestions)},
		}
		if b, err := json.Marshal(rec); err == nil {
			if err := w.WriteOne(b); err != nil {
				return err
			}
		}
	}
	return nil
}

func pickPredictPrimary(decisions []migrate.AiCodeFixDecision) migrate.AiCodeFixDecision {
	for _, d := range decisions {
		if d.PatchPayload != nil || d.Hidden.Status != "" || d.Suggestions.Status != "" {
			return d
		}
	}
	return decisions[0]
}

func predictedAiCodeFixOutcome(org string, row migrate.AiCodeFixRowOutcome) map[string]string {
	detail := row.Detail
	if row.NearPerfect {
		detail += migrate.AiCodeFixNearPerfectMarker
	}
	return map[string]string{
		"org":    org,
		"status": row.Status,
		"detail": detail,
		"reason": row.Reason,
	}
}

// jsonStringField extracts a top-level string field from a JSON object.
// Alias of common.ExtractField so the implementation lives in one place.
var jsonStringField = common.ExtractField

// jsonBoolField extracts a top-level bool field from a JSON object.
// Alias of common.ExtractBool so the implementation lives in one place.
var jsonBoolField = common.ExtractBool

// projID identifies a project by its originating server URL and source key.
// Used as a map key when joining extract items back to createProjects rows.
type projID struct{ serverURL, sourceKey string }

// buildCloudByProject indexes a slice of createProjects JSONL rows into a
// (serverURL, sourceKey) → cloud_project_key map for O(1) lookup.
func buildCloudByProject(projects []json.RawMessage) map[projID]string {
	out := make(map[projID]string, len(projects))
	for _, p := range projects {
		sourceKey := jsonStringField(p, "key")
		serverURL := jsonStringField(p, "server_url")
		cloudKey := jsonStringField(p, "cloud_project_key")
		if cloudKey == "" || sourceKey == "" {
			continue
		}
		out[projID{serverURL, sourceKey}] = cloudKey
	}
	return out
}
