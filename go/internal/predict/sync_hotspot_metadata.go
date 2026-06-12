// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"encoding/json"
	"fmt"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// synthesizeSyncHotspotMetadata reads each project's hotspots from the
// extract's getProjectHotspotsFull task and emits one synthetic
// syncHotspotMetadata JSONL record per project so the predictive
// report can surface the same sync stats / NearPerfect routing as the
// real migrate (#323). The predict pipeline can compute the
// ACKNOWLEDGED demotion count exactly (it depends only on source-side
// resolution); it cannot predict line_mismatch / not_found and
// assumes a 1:1 match for non-ACKNOWLEDGED actionable hotspots.
//
// Schema matches what runSyncHotspotMetadata writes in real migrate
// so the existing collectSyncStats / collectSyncOutcome paths render
// the predictive section unchanged.
func synthesizeSyncHotspotMetadata(exportDir, runDir string, extractMapping structure.ExtractMapping) error {
	hotspotItems, err := structure.ReadExtractData(exportDir, extractMapping, "getProjectHotspotsFull")
	if err != nil {
		return fmt.Errorf("reading getProjectHotspotsFull extract: %w", err)
	}
	if len(hotspotItems) == 0 {
		return nil
	}

	store := common.NewDataStore(runDir)
	projects, err := store.ReadAll("createProjects")
	if err != nil || len(projects) == 0 {
		return nil
	}

	// Index (server_url, source key) → cloud_project_key (mirrors
	// new_code_periods.go).
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

	type counts struct {
		actionable int
		ack        int
	}
	perProject := make(map[string]*counts, len(cloudByProject))

	for _, item := range hotspotItems {
		sourceKey := jsonStringField(item.Data, "project")
		if sourceKey == "" {
			sourceKey = jsonStringField(item.Data, "projectKey")
		}
		cloudKey := cloudByProject[projID{item.ServerURL, sourceKey}]
		if cloudKey == "" {
			continue
		}
		status := jsonStringField(item.Data, "status")
		resolution := jsonStringField(item.Data, "resolution")
		hasComments := jsonHasNonEmptyArray(item.Data, "comment")
		if !migrate.HotspotHasManualChanges(status, resolution, hasComments) {
			continue
		}
		c, ok := perProject[cloudKey]
		if !ok {
			c = &counts{}
			perProject[cloudKey] = c
		}
		c.actionable++
		if migrate.IsAcknowledgedResolution(resolution) {
			c.ack++
		}
	}

	if len(perProject) == 0 {
		return nil
	}

	w, err := store.Writer("syncHotspotMetadata")
	if err != nil {
		return err
	}
	for cloudKey, c := range perProject {
		rec := map[string]any{
			"cloud_project_key":    cloudKey,
			"synced":               c.actionable - c.ack,
			"line_mismatch":        0,
			"not_found":            0,
			"acknowledged_demoted": c.ack,
			"actionable":           c.actionable,
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

// jsonHasNonEmptyArray reports whether the named top-level field is a
// JSON array with at least one element. Used to detect hotspot
// comment presence without parsing the full comment shape.
func jsonHasNonEmptyArray(raw json.RawMessage, key string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	v, ok := obj[key]
	if !ok {
		return false
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(v, &arr); err != nil {
		return false
	}
	return len(arr) > 0
}
