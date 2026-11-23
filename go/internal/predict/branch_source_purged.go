// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"encoding/json"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// branchSourcePurgedBranchKey identifies a single (server, project, branch)
// tuple, used throughout synthesizeBranchSourcePurged's per-branch counters.
type branchSourcePurgedBranchKey struct{ serverURL, projectKey, branch string }

// branchSourcePurgedProjectKey identifies a (server, project) tuple, used
// throughout synthesizeBranchSourcePurged's per-project lookups.
type branchSourcePurgedProjectKey struct{ serverURL, projectKey string }

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

	sourceBytes, sourceRecords := collectBranchSourceCounts(exportDir, extractMapping)
	componentRecords, componentsByProject := collectBranchComponentCounts(exportDir, extractMapping)
	branchesByProject, mainBranch := collectProjectBranches(exportDir, extractMapping)

	w, err := store.Writer("importProjectData")
	if err != nil {
		return err
	}

	return writeBranchSourcePurgedRows(w, projects, branchSourcePurgedLookups{
		branchesByProject:   branchesByProject,
		mainBranch:          mainBranch,
		componentsByProject: componentsByProject,
		componentRecords:    componentRecords,
		sourceBytes:         sourceBytes,
		sourceRecords:       sourceRecords,
	})
}

// collectBranchSourceCounts tallies source bytes and record presence per
// branch from the getProjectSourceCode extract. A purged branch still has
// (empty) source records — the extract writes one per file even when the
// source text is gone — so record presence distinguishes "purged" from
// "source never extracted".
//
// Streamed rather than slurped: this is a pure fold into two counters, but
// getProjectSourceCode is the largest thing the extract produces — full
// file text plus per-line highlighted HTML for every file on the instance —
// and none of it needs to be resident to count bytes.
func collectBranchSourceCounts(exportDir string, extractMapping structure.ExtractMapping) (sourceBytes, sourceRecords map[branchSourcePurgedBranchKey]int) {
	sourceBytes = make(map[branchSourcePurgedBranchKey]int)
	sourceRecords = make(map[branchSourcePurgedBranchKey]int)
	for item := range structure.ExtractItems(exportDir, extractMapping, "getProjectSourceCode") {
		bk := branchSourcePurgedBranchKey{item.ServerURL, jsonStringField(item.Data, "projectKey"), jsonStringField(item.Data, "branch")}
		sourceBytes[bk] += len(jsonStringField(item.Data, "source"))
		sourceRecords[bk]++
	}
	return sourceBytes, sourceRecords
}

// collectBranchComponentCounts tallies component records per branch, to
// predict the branches migrate will skip for having none (#565): a
// non-main branch with no extracted component lost its data to the
// source's housekeeping.
func collectBranchComponentCounts(exportDir string, extractMapping structure.ExtractMapping) (componentRecords map[branchSourcePurgedBranchKey]int, componentsByProject map[branchSourcePurgedProjectKey]int) {
	componentRecords = make(map[branchSourcePurgedBranchKey]int)
	componentsByProject = make(map[branchSourcePurgedProjectKey]int)
	for item := range structure.ExtractItems(exportDir, extractMapping, "getProjectComponentTree") {
		pk := branchSourcePurgedProjectKey{item.ServerURL, jsonStringField(item.Data, "projectKey")}
		componentRecords[branchSourcePurgedBranchKey{pk.serverURL, pk.projectKey, jsonStringField(item.Data, "branch")}]++
		componentsByProject[pk]++
	}
	return componentRecords, componentsByProject
}

// collectProjectBranches gathers LONG branches per project from the
// getBranches extract. Projects with no recorded branches fall back to a
// lone "main" branch, matching migrate.
func collectProjectBranches(exportDir string, extractMapping structure.ExtractMapping) (branchesByProject map[branchSourcePurgedProjectKey][]string, mainBranch map[branchSourcePurgedProjectKey]string) {
	branchesByProject = make(map[branchSourcePurgedProjectKey][]string)
	mainBranch = make(map[branchSourcePurgedProjectKey]string)
	for item := range structure.ExtractItems(exportDir, extractMapping, "getBranches") {
		if strings.ToUpper(jsonStringField(item.Data, "type")) == "SHORT" {
			continue
		}
		name := jsonStringField(item.Data, "name")
		if name == "" {
			continue
		}
		pk := branchSourcePurgedProjectKey{item.ServerURL, jsonStringField(item.Data, "projectKey")}
		branchesByProject[pk] = append(branchesByProject[pk], name)
		if jsonBoolField(item.Data, "isMain") {
			mainBranch[pk] = name
		}
	}
	return branchesByProject, mainBranch
}

// branchSourcePurgedLookups bundles the per-branch / per-project counters
// writeBranchSourcePurgedRows and writeBranchSourcePurgedRow need, so they
// don't each require a long, same-typed parameter list.
type branchSourcePurgedLookups struct {
	branchesByProject   map[branchSourcePurgedProjectKey][]string
	mainBranch          map[branchSourcePurgedProjectKey]string
	componentsByProject map[branchSourcePurgedProjectKey]int
	componentRecords    map[branchSourcePurgedBranchKey]int
	sourceBytes         map[branchSourcePurgedBranchKey]int
	sourceRecords       map[branchSourcePurgedBranchKey]int
}

// writeBranchSourcePurgedRows emits one importProjectData row per affected
// branch of every project in projects. See synthesizeBranchSourcePurged.
func writeBranchSourcePurgedRows(w *common.ChunkWriter, projects []json.RawMessage, lookups branchSourcePurgedLookups) error {
	for _, p := range projects {
		sourceKey := jsonStringField(p, "key")
		serverURL := jsonStringField(p, "server_url")
		cloudKey := jsonStringField(p, "cloud_project_key")
		if cloudKey == "" || sourceKey == "" {
			continue
		}
		pk := branchSourcePurgedProjectKey{serverURL, sourceKey}
		branches := lookups.branchesByProject[pk]
		if len(branches) == 0 {
			branches = []string{"main"}
		}
		for _, branch := range branches {
			if err := writeBranchSourcePurgedRow(w, pk, branch, cloudKey, lookups); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeBranchSourcePurgedRow writes, at most, one importProjectData row for
// a single (project, branch) pair: either a "no data" row (#565, a non-main
// branch with no extracted component) or a "source purged" row (source
// extraction ran but every record came back empty, matching
// migrate.buildBranchReport's sourcePurged test — findings are not
// required, since the CE rejects source-less files regardless of issues).
func writeBranchSourcePurgedRow(w *common.ChunkWriter, pk branchSourcePurgedProjectKey, branch, cloudKey string, lookups branchSourcePurgedLookups) error {
	bk := branchSourcePurgedBranchKey{pk.serverURL, pk.projectKey, branch}
	if branch != lookups.mainBranch[pk] && lookups.componentsByProject[pk] > 0 && lookups.componentRecords[bk] == 0 {
		return writeJSONRow(w, map[string]any{
			"cloud_project_key": cloudKey,
			"branch":            branch,
			"status":            migrate.BranchStatusNoData,
		})
	}
	if !(lookups.sourceBytes[bk] == 0 && lookups.sourceRecords[bk] > 0) {
		return nil
	}
	return writeJSONRow(w, map[string]any{
		"cloud_project_key": cloudKey,
		"branch":            branch,
		"status":            "success",
		"source_purged":     true,
	})
}

// writeJSONRow marshals rec and writes it as one JSONL record via w.
func writeJSONRow(w *common.ChunkWriter, rec map[string]any) error {
	b, _ := json.Marshal(rec)
	return w.WriteOne(b)
}
