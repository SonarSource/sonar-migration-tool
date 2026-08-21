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
// real migrate (#323). It applies the same sync-eligibility rule real
// migrate uses (migrate.HotspotSyncEligibility, #527): a hotspot counts
// toward "actionable" only if it is not TO_REVIEW, has a user comment,
// or is ACKNOWLEDGED (inventoried but never state-synced). The predict
// pipeline can compute this exactly (it depends only on source-side
// status/resolution/comments); it cannot predict line_mismatch /
// not_found and assumes a 1:1 match for eligible, non-ACKNOWLEDGED
// hotspots.
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

	// Index (server_url, source key) → cloud_project_key.
	cloudByProject := buildCloudByProject(projects)

	// eligible/ack mirror the real-migrate hotspotSyncCategory split
	// (#527): eligible hotspots would get a full state/comment sync,
	// ACKNOWLEDGED ones are inventoried but never state-transitioned.
	// Excluded hotspots (TO_REVIEW, no user comment) are not counted.
	type counts struct {
		eligible int
		ack      int
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
		hasUserComment := jsonHasUserComment(item.Data)
		eligible, acknowledged := migrate.HotspotSyncEligibility(status, resolution, hasUserComment)
		if !eligible && !acknowledged {
			continue
		}
		c, ok := perProject[cloudKey]
		if !ok {
			c = &counts{}
			perProject[cloudKey] = c
		}
		if acknowledged {
			c.ack++
		} else {
			c.eligible++
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
			"synced":               c.eligible,
			"line_mismatch":        0,
			"not_found":            0,
			"acknowledged_demoted": c.ack,
			"actionable":           c.eligible + c.ack,
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

// jsonHasUserComment reports whether the hotspot's raw JSON "comment"
// array contains at least one entry with a non-empty "login" — the
// #527 "user comment" signal, mirroring migrate.hotspotHasUserComment
// for the raw-JSON extract shape.
func jsonHasUserComment(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	v, ok := obj["comment"]
	if !ok {
		return false
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(v, &arr); err != nil {
		return false
	}
	for _, c := range arr {
		if jsonStringField(c, "login") != "" {
			return true
		}
	}
	return false
}
