// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// indexedTwin returns a copy of e that reads through an ExtractIndex, so a
// loader's indexed and unindexed results can be compared (#690).
func indexedTwin(e *Executor) *Executor {
	twin := *e
	twin.ExtractIndex = structure.NewExtractIndex(e.ExportDir, e.Mapping)
	return &twin
}

// Every per-branch loader must return the same thing with and without the
// index, over a merged set where a later tranche re-extracted one project.
func TestExtractIndexLoadersMatchFullScan(t *testing.T) {
	dir := t.TempDir()
	const slice = "extract-02"
	writeJSON(filepath.Join(dir, extractRun, "extract.json"), map[string]any{"url": testServerURL})
	writeJSON(filepath.Join(dir, slice, "extract.json"),
		map[string]any{"url": testServerURL, "partial": true, "project_keys": []string{projMain}})
	sourceCorpus(t, dir)
	// The tranche re-extracted proj1 only: its main-branch source must replace
	// the full extract's, while proj2 keeps coming from the full extract.
	writeJSONL(filepath.Join(dir, slice, taskSourceCode), []map[string]any{
		{fieldKey: fileA, fieldProjectKey: projMain, fieldBranch: branchMain, fieldSource: "package a2\n"},
	})
	writeJSONL(filepath.Join(dir, extractRun, taskHotspots), []map[string]any{
		{fieldKey: hsNoBranch, fieldProject: projMain},
		{fieldKey: hsOtherProject, fieldProject: "proj2", fieldBranch: branchMain},
	})
	writeJSONL(filepath.Join(dir, extractRun, "getProjectIssuesFull"), []map[string]any{
		{fieldKey: "i1", fieldProjectKey: projMain, fieldBranch: branchMain, "rule": "java:S1", "status": "OPEN", "component": fileA},
		{fieldKey: "i2", fieldProjectKey: "proj2", fieldBranch: branchMain, "rule": "java:S1", "status": "OPEN"},
	})

	e := newProjectDataExecutor(t, dir)
	e.Mapping = structure.ExtractMapping{testServerURL: {extractRun, slice}}
	ix := indexedTwin(e)

	for _, project := range []string{projMain, "proj2", "unknown"} {
		for _, branch := range []string{branchMain, branchDev} {
			wantSrc, wantHl := loadBranchSourceData(e, testServerURL, project, branch)
			gotSrc, gotHl := loadBranchSourceData(ix, testServerURL, project, branch)
			if !reflect.DeepEqual(gotSrc, wantSrc) || !reflect.DeepEqual(gotHl, wantHl) {
				t.Errorf("%s/%s sources: indexed %+v %+v, full scan %+v %+v", project, branch, gotSrc, gotHl, wantSrc, wantHl)
			}
			if got, want := loadExtractedHotspots(ix, testServerURL, project, branch), loadExtractedHotspots(e, testServerURL, project, branch); !reflect.DeepEqual(got, want) {
				t.Errorf("%s/%s hotspots: indexed %+v, full scan %+v", project, branch, got, want)
			}
			if got, want := loadExtractedIssues(ix, testServerURL, project, branch), loadExtractedIssues(e, testServerURL, project, branch); !reflect.DeepEqual(got, want) {
				t.Errorf("%s/%s issues: indexed %+v, full scan %+v", project, branch, got, want)
			}
		}
	}

	src, _ := loadBranchSourceData(ix, testServerURL, projMain, branchMain)
	if len(src) != 1 || src[0].Source != "package a2\n" {
		t.Errorf("proj1/main should come only from the later tranche, got %+v", src)
	}
}

// The cached active rules are handed out as copies: buildBranchReport
// filters and remaps them in place, which must not leak into the next branch.
func TestLoadExtractedActiveRulesCacheReturnsCopies(t *testing.T) {
	dir := t.TempDir()
	writeJSON(filepath.Join(dir, extractRun, "extract.json"), map[string]any{"url": testServerURL})
	writeJSONL(filepath.Join(dir, extractRun, "getActiveProfileRules"), []map[string]any{
		{fieldKey: "java:S1", "severity": "MAJOR", "qProfile": "qp1", "lang": "java"},
	})
	e := newProjectDataExecutor(t, dir)
	e.activeRules = newActiveRulesCache()

	first := loadExtractedActiveRules(e, testServerURL, "")
	if len(first) != 1 {
		t.Fatalf("got %d rules, want 1", len(first))
	}
	first[0].QProfileKey = "remapped"

	second := loadExtractedActiveRules(e, testServerURL, "")
	if len(second) != 1 || second[0].QProfileKey != "qp1" {
		t.Errorf("cached rules were mutated through a previous result: %+v", second)
	}
}
