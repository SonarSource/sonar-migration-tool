package predict

import (
	"encoding/json"
	"fmt"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// ncdTypesNotOnSQCProjectScope lists the SonarQube Server new-code-
// definition types that exist on SQC at org level but NOT at project
// level — the real migrate task falls back to the org default for
// these. Mirrored from migrate.sqcProjectNewCodeType (the inverse set).
var ncdTypesNotOnSQCProjectScope = map[string]bool{
	"REFERENCE_BRANCH":  true,
	"SPECIFIC_ANALYSIS": true,
}

// synthesizeSetNewCodePeriods reads each project's NCD type from the
// extract's getNewCodePeriods task and, for every project whose
// project-level NCD is one of the SQC-unsupported types (reference
// branch or specific analysis), writes a setNewCodePeriods JSONL row
// with ncd_fallback=true. The summary collector's
// applyNCDFallbackPartials picks those rows up and moves the project
// from Succeeded → Partial with an explanatory Issue (#240
// follow-up).
//
// The synthesizer joins extract items back to the synthesized
// createProjects rows by (server_url, projectKey) so the
// cloud_project_key matches what the rest of the predict pipeline
// uses.
func synthesizeSetNewCodePeriods(exportDir, runDir string, extractMapping structure.ExtractMapping) error {
	ncdItems, err := structure.ReadExtractData(exportDir, extractMapping, "getNewCodePeriods")
	if err != nil {
		return fmt.Errorf("reading getNewCodePeriods extract: %w", err)
	}
	if len(ncdItems) == 0 {
		return nil
	}

	store := common.NewDataStore(runDir)
	projects, err := store.ReadAll("createProjects")
	if err != nil || len(projects) == 0 {
		return nil
	}

	// Index (server_url, source key) → cloud_project_key.
	type projID struct{ serverURL, sourceKey string }
	cloudByProject := make(map[projID]string, len(projects))
	for _, p := range projects {
		sourceKey := jsonStringField(p, "key")
		serverURL := jsonStringField(p, "server_url")
		cloudKey := jsonStringField(p, "cloud_project_key")
		if cloudKey == "" || sourceKey == "" {
			continue
		}
		cloudByProject[projID{serverURL, sourceKey}] = cloudKey
	}

	w, err := store.Writer("setNewCodePeriods")
	if err != nil {
		return err
	}

	seenFallback := make(map[string]bool, len(ncdItems))
	seenBranchOverride := make(map[string]bool, len(ncdItems))

	for _, item := range ncdItems {
		sourceKey := jsonStringField(item.Data, "projectKey")
		cloudKey := cloudByProject[projID{item.ServerURL, sourceKey}]
		if cloudKey == "" {
			continue
		}
		branchKey := jsonStringField(item.Data, "branchKey")
		ncdType := jsonStringField(item.Data, "type")
		inherited := jsonBoolField(item.Data, "inherited")

		switch {
		case branchKey == "":
			// Project-level record. Only flag fallback for SQC-
			// unsupported NCD types.
			if !ncdTypesNotOnSQCProjectScope[ncdType] {
				continue
			}
			if seenFallback[cloudKey] {
				continue
			}
			seenFallback[cloudKey] = true
			rec := map[string]any{
				"cloud_project_key": cloudKey,
				"source_ncd_type":   ncdType,
				"ncd_fallback":      true,
			}
			b, _ := json.Marshal(rec)
			if err := w.WriteOne(b); err != nil {
				return err
			}

		case !inherited:
			// Per-branch override that doesn't simply inherit the
			// project-level NCD. SonarQube Cloud has no per-branch NCD
			// concept, so the branch will silently inherit the
			// project-level value. Flag the project as Partial once.
			if seenBranchOverride[cloudKey] {
				continue
			}
			seenBranchOverride[cloudKey] = true
			rec := map[string]any{
				"cloud_project_key":   cloudKey,
				"ncd_branch_override": true,
				"branch":              branchKey,
			}
			b, _ := json.Marshal(rec)
			if err := w.WriteOne(b); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonBoolField extracts a top-level bool field from a raw JSON
// message. Returns false on missing/unparseable values.
func jsonBoolField(raw json.RawMessage, key string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	v, ok := obj[key]
	if !ok {
		return false
	}
	var b bool
	_ = json.Unmarshal(v, &b)
	return b
}
