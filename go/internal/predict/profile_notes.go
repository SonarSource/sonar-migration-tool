package predict

import (
	"encoding/json"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// synthesizeAnalyzeProfileRulesNotes mirrors the real-migrate
// analyzeProfileRules task (#226) so the predictive report can
// surface the same NearPerfect classification for quality profiles
// hit by any of the six yellow criteria. It re-uses the analysis
// helpers in internal/migrate and runs them across the extract data,
// joining results back to the synthesized createProfiles rows.
func synthesizeAnalyzeProfileRulesNotes(exportDir, runDir string, extractMapping structure.ExtractMapping) error {
	store := common.NewDataStore(runDir)
	profiles, err := store.ReadAll("createProfiles")
	if err != nil || len(profiles) == 0 {
		return nil
	}

	activeByProfile := indexExtractRecords(exportDir, extractMapping, "getActiveProfileRules", "profileKey")
	deactivatedByProfile := indexExtractRecords(exportDir, extractMapping, "getDeactivatedProfileRules", "profileKey")
	baseByServer := indexBaseRulesByServer(exportDir, extractMapping)

	w, err := store.Writer("analyzeProfileRules")
	if err != nil {
		return err
	}

	for _, raw := range profiles {
		cloudKey := jsonStringField(raw, "cloud_profile_key")
		sourceKey := jsonStringField(raw, "source_profile_key")
		serverURL := jsonStringField(raw, "server_url")
		if cloudKey == "" || sourceKey == "" {
			continue
		}
		in := migrate.ProfileAnalysisInput{
			CloudProfileKey:           cloudKey,
			ProfileName:               jsonStringField(raw, "name"),
			Language:                  jsonStringField(raw, "language"),
			ActiveRules:               activeByProfile[serverURL+"\x00"+sourceKey],
			DeactivatedInheritedRules: deactivatedByProfile[serverURL+"\x00"+sourceKey],
			BaseRulesByKey:            baseByServer[serverURL],
		}
		for _, f := range migrate.AnalyzeProfile(in) {
			b, err := json.Marshal(f)
			if err != nil {
				continue
			}
			if err := w.WriteOne(b); err != nil {
				return err
			}
		}
	}
	return nil
}

// indexExtractRecords groups records of an extract task by
// (serverURL, field-value) — the same composite-key trick the
// real-migrate analyzer uses for fast per-profile lookups.
func indexExtractRecords(exportDir string, mapping structure.ExtractMapping, task, field string) map[string][]json.RawMessage {
	items, _ := structure.ReadExtractData(exportDir, mapping, task)
	out := make(map[string][]json.RawMessage, len(items))
	for _, it := range items {
		val := jsonStringField(it.Data, field)
		if val == "" {
			continue
		}
		key := it.ServerURL + "\x00" + val
		out[key] = append(out[key], it.Data)
	}
	return out
}

// indexBaseRulesByServer builds serverURL → ruleKey → raw rule. Used
// by the analyzer to look up a rule's default severity / params /
// templateKey when scoring custom-severity / custom-params /
// template-instance findings.
func indexBaseRulesByServer(exportDir string, mapping structure.ExtractMapping) map[string]map[string]json.RawMessage {
	items, _ := structure.ReadExtractData(exportDir, mapping, "getRules")
	out := make(map[string]map[string]json.RawMessage)
	for _, it := range items {
		k := jsonStringField(it.Data, "key")
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
