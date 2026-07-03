// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"encoding/json"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// synthesizeBranchSourcePurged predicts, per project, which branches will
// be migrated WITHOUT their source code because SonarQube housekeeping has
// purged the source text (issue #425). It mirrors the actual-migrate
// decision in migrate.buildBranchReport: a branch is migrated source-less
// when it has findings (issues/hotspots) but zero source bytes were
// extracted for it.
//
// For each such branch it writes an importProjectData JSONL row with
// status="success" and source_purged=true. That feeds the same summary
// collector (collectBranchSourcePurged → attachBranchSourcePurged) the
// actual report uses, so the predictive report shows the identical
// "Source code of branch(es) X is missing (likely purged in SQS)" note in
// the affected project's Details column. Rows are emitted ONLY for purged
// branches, so the predictive report's project outcomes are unchanged.
//
// The synthesizer joins extract items back to the synthesized
// createProjects rows by (server_url, projectKey) so the cloud_project_key
// matches what the rest of the predict pipeline uses.
func synthesizeBranchSourcePurged(exportDir, runDir string, extractMapping structure.ExtractMapping) error {
	store := common.NewDataStore(runDir)
	projects, err := store.ReadAll("createProjects")
	if err != nil || len(projects) == 0 {
		return nil
	}

	// branchKey identifies a single (server, project, branch) tuple.
	type branchKey struct{ serverURL, projectKey, branch string }
	// projectKey identifies a (server, project) tuple.
	type projectKeyT struct{ serverURL, projectKey string }

	// Source bytes and record presence per branch. A purged branch still
	// has (empty) source records — the extract writes one per file even
	// when the source text is gone — so record presence distinguishes
	// "purged" from "source never extracted".
	sourceBytes := make(map[branchKey]int)
	sourceRecords := make(map[branchKey]int)
	if items, err := structure.ReadExtractData(exportDir, extractMapping, "getProjectSourceCode"); err == nil {
		for _, item := range items {
			bk := branchKey{item.ServerURL, jsonStringField(item.Data, "projectKey"), jsonStringField(item.Data, "branch")}
			sourceBytes[bk] += len(jsonStringField(item.Data, "source"))
			sourceRecords[bk]++
		}
	}

	// Branches per project (LONG branches only). Projects with no recorded
	// branches fall back to a lone "main" branch, matching migrate.
	branchesByProject := make(map[projectKeyT][]string)
	if items, err := structure.ReadExtractData(exportDir, extractMapping, "getBranches"); err == nil {
		for _, item := range items {
			if strings.ToUpper(jsonStringField(item.Data, "type")) == "SHORT" {
				continue
			}
			name := jsonStringField(item.Data, "name")
			if name == "" {
				continue
			}
			pk := projectKeyT{item.ServerURL, jsonStringField(item.Data, "projectKey")}
			branchesByProject[pk] = append(branchesByProject[pk], name)
		}
	}

	w, err := store.Writer("importProjectData")
	if err != nil {
		return err
	}

	for _, p := range projects {
		sourceKey := jsonStringField(p, "key")
		serverURL := jsonStringField(p, "server_url")
		cloudKey := jsonStringField(p, "cloud_project_key")
		if cloudKey == "" || sourceKey == "" {
			continue
		}
		pk := projectKeyT{serverURL, sourceKey}
		branches := branchesByProject[pk]
		if len(branches) == 0 {
			branches = []string{"main"}
		}
		for _, branch := range branches {
			bk := branchKey{serverURL, sourceKey, branch}
			// A branch is migrated source-less when source extraction ran
			// (records exist) but every record came back empty — matching
			// migrate.buildBranchReport's sourcePurged test. Findings are not
			// required: a branch with purged source and no issues is still
			// migrated without source (the CE rejects source-less files
			// regardless of issues).
			if !(sourceBytes[bk] == 0 && sourceRecords[bk] > 0) {
				continue
			}
			rec := map[string]any{
				"cloud_project_key": cloudKey,
				"branch":            branch,
				"status":            "success",
				"source_purged":     true,
			}
			b, _ := json.Marshal(rec)
			if err := w.WriteOne(b); err != nil {
				return err
			}
		}
	}
	return nil
}
