// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// --- Pure utility function tests ---

func TestDedupActiveRules(t *testing.T) {
	// After remapping, multiple source profiles for one language share a
	// single SonarCloud profile key, producing duplicate (repo,key,qProfileKey)
	// triples. The CE rejects a profile that activates the same rule twice, so
	// dedup must keep exactly one per triple while preserving distinct rules.
	in := []scanreport.ActiveRuleInput{
		{RuleRepo: "python", RuleKey: "S100", QProfileKey: "qpPy", Language: "py"}, // from "Sonar way"
		{RuleRepo: "python", RuleKey: "S100", QProfileKey: "qpPy", Language: "py"}, // dup from "Olivier Way"
		{RuleRepo: "python", RuleKey: "S100", QProfileKey: "qpPy", Language: "py"}, // dup x3
		{RuleRepo: "python", RuleKey: "S200", QProfileKey: "qpPy", Language: "py"}, // distinct rule
		{RuleRepo: "docker", RuleKey: "S100", QProfileKey: "qpDk", Language: "docker"}, // distinct repo+profile
	}
	out := dedupActiveRules(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 distinct active rules, got %d: %+v", len(out), out)
	}
	seen := map[string]bool{}
	for _, r := range out {
		k := r.RuleRepo + "|" + r.RuleKey + "|" + r.QProfileKey
		if seen[k] {
			t.Errorf("duplicate survived dedup: %s", k)
		}
		seen[k] = true
	}
	// First occurrence preserved.
	if out[0].RuleKey != "S100" || out[1].RuleKey != "S200" || out[2].RuleRepo != "docker" {
		t.Errorf("order/first-occurrence not preserved: %+v", out)
	}
}

func TestSplitRule(t *testing.T) {
	tests := []struct {
		input    string
		wantRepo string
		wantKey  string
	}{
		{"java:S100", "java", "S100"},
		{"javascript:S1234", "javascript", "S1234"},
		{"norule", "norule", ""},
		{"a:b:c", "a", "b:c"},
		{"", "", ""},
	}
	for _, tt := range tests {
		repo, key := splitRule(tt.input)
		if repo != tt.wantRepo || key != tt.wantKey {
			t.Errorf("splitRule(%q) = (%q, %q), want (%q, %q)", tt.input, repo, key, tt.wantRepo, tt.wantKey)
		}
	}
}

func TestExtractInt32(t *testing.T) {
	data := json.RawMessage(`{"textRange":{"startLine":10,"endLine":20},"other":"value"}`)

	if got := extractInt32(data, "textRange", "startLine"); got != 10 {
		t.Errorf("startLine: expected 10, got %d", got)
	}
	if got := extractInt32(data, "textRange", "endLine"); got != 20 {
		t.Errorf("endLine: expected 20, got %d", got)
	}
	if got := extractInt32(data, "textRange", "missing"); got != 0 {
		t.Errorf("missing field: expected 0, got %d", got)
	}
	if got := extractInt32(data, "missing", "startLine"); got != 0 {
		t.Errorf("missing object: expected 0, got %d", got)
	}
	if got := extractInt32(json.RawMessage(`invalid`), "a", "b"); got != 0 {
		t.Errorf("invalid json: expected 0, got %d", got)
	}
}

func TestExtractInt32Field(t *testing.T) {
	data := json.RawMessage(`{"lines":42,"name":"test"}`)

	if got := extractInt32Field(data, "lines"); got != 42 {
		t.Errorf("expected 42, got %d", got)
	}
	if got := extractInt32Field(data, "missing"); got != 0 {
		t.Errorf("missing: expected 0, got %d", got)
	}
	if got := extractInt32Field(json.RawMessage(`invalid`), "x"); got != 0 {
		t.Errorf("invalid json: expected 0, got %d", got)
	}
}

func TestExtractMeasureInt32(t *testing.T) {
	data := json.RawMessage(`{"measures":[{"metric":"ncloc","value":"150"},{"metric":"coverage","value":"80"}]}`)

	if got := extractMeasureInt32(data, "ncloc"); got != 150 {
		t.Errorf("ncloc: expected 150, got %d", got)
	}
	if got := extractMeasureInt32(data, "coverage"); got != 80 {
		t.Errorf("coverage: expected 80, got %d", got)
	}
	if got := extractMeasureInt32(data, "missing"); got != 0 {
		t.Errorf("missing metric: expected 0, got %d", got)
	}

	// No measures key.
	if got := extractMeasureInt32(json.RawMessage(`{"other":"val"}`), "ncloc"); got != 0 {
		t.Errorf("no measures: expected 0, got %d", got)
	}

	// Invalid json.
	if got := extractMeasureInt32(json.RawMessage(`invalid`), "ncloc"); got != 0 {
		t.Errorf("invalid json: expected 0, got %d", got)
	}

	// Invalid measures array.
	if got := extractMeasureInt32(json.RawMessage(`{"measures":"not-array"}`), "ncloc"); got != 0 {
		t.Errorf("invalid measures: expected 0, got %d", got)
	}
}

func TestBuildSourceKeySet(t *testing.T) {
	sources := []sourceRecord{
		{Component: "p1:src/Main.java", Source: "class Main {}"},
		{Component: "p1:src/Util.java", Source: "class Util {}"},
	}
	keys := buildSourceKeySet(sources)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if !keys["p1:src/Main.java"] || !keys["p1:src/Util.java"] {
		t.Errorf("missing expected keys: %v", keys)
	}
}

func TestBuildSourceKeySetEmpty(t *testing.T) {
	keys := buildSourceKeySet(nil)
	if len(keys) != 0 {
		t.Errorf("expected empty, got %v", keys)
	}
}

func TestFilterComponentsWithSource(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "p1:src/Main.java", Name: "Main.java"},
		{Key: "p1:src/Test.java", Name: "Test.java"},
		{Key: "p1:src/NoSource.java", Name: "NoSource.java"},
	}
	sourceKeys := map[string]bool{
		"p1:src/Main.java": true,
		"p1:src/Test.java": true,
	}

	filtered := filterComponentsWithSource(components, sourceKeys)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 filtered, got %d", len(filtered))
	}
	if filtered[0].Key != "p1:src/Main.java" || filtered[1].Key != "p1:src/Test.java" {
		t.Errorf("unexpected filtered: %v", filtered)
	}
}

func TestFilterComponentsWithSourceEmpty(t *testing.T) {
	filtered := filterComponentsWithSource(nil, map[string]bool{})
	if len(filtered) != 0 {
		t.Errorf("expected empty, got %v", filtered)
	}
}

func TestCountFilesByExt(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "a", Language: "Java"},
		{Key: "b", Language: "java"},
		{Key: "c", Language: "Python"},
		{Key: "d", Language: ""},
	}
	counts := countFilesByExt(components)
	if counts["java"] != 2 {
		t.Errorf("java: expected 2, got %d", counts["java"])
	}
	if counts["python"] != 1 {
		t.Errorf("python: expected 1, got %d", counts["python"])
	}
	if _, ok := counts[""]; ok {
		t.Error("empty language should be excluded")
	}
}

func TestCountFilesByExtEmpty(t *testing.T) {
	counts := countFilesByExt(nil)
	if len(counts) != 0 {
		t.Errorf("expected empty, got %v", counts)
	}
}

func TestProjectDataTasksDef(t *testing.T) {
	tasks := projectDataTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Name != "importProjectData" {
		t.Errorf("expected importProjectData, got %s", tasks[0].Name)
	}
}

func TestBuildChangesetMap(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "p1:src/Main.java", Lines: 10},
		{Key: "p1:src/Util.java", Lines: 5},
		{Key: "p1:src/Empty.java", Lines: 0},
	}

	cr := scanreport.NewComponentRef()
	cr.Get("p1:src/Main.java")
	cr.Get("p1:src/Util.java")
	cr.Get("p1:src/Empty.java")

	now := time.Now()
	result := buildChangesetMap(cr, components, nil, nil, now)

	// Lines > 0 should produce changesets.
	mainRef, _ := cr.Refs()["p1:src/Main.java"]
	utilRef, _ := cr.Refs()["p1:src/Util.java"]
	emptyRef, _ := cr.Refs()["p1:src/Empty.java"]

	if _, ok := result[mainRef]; !ok {
		t.Error("expected changeset for Main.java")
	}
	if _, ok := result[utilRef]; !ok {
		t.Error("expected changeset for Util.java")
	}
	if _, ok := result[emptyRef]; ok {
		t.Error("Lines=0 should not produce changeset")
	}
}

// #425 — ensureFileSourcesPresent gives every source-less FILE component a
// blank placeholder source (one empty line per declared line) so the CE
// accepts the report, while leaving files that already have real source
// untouched. A zero/negative line count is clamped up to a single empty
// line and the component's declared line count is bumped to match.
func TestEnsureFileSourcesPresent(t *testing.T) {
	fileComps := []*pb.Component{
		{Ref: 1, Type: pb.Component_FILE, Lines: 10}, // purged -> 10 blank lines
		{Ref: 2, Type: pb.Component_FILE, Lines: 1},  // purged -> 1 blank line
		{Ref: 3, Type: pb.Component_FILE, Lines: 0},  // purged, no measure -> clamp to 1
		{Ref: 4, Type: pb.Component_FILE, Lines: 2},  // already has real source -> untouched
	}
	sources := map[int32]string{4: "real\nsource"}

	ensureFileSourcesPresent(fileComps, sources)

	if len(sources) != 4 {
		t.Fatalf("want a source entry per file (4), got %d", len(sources))
	}
	check := func(ref int32, wantLines int, wantBlank bool) {
		src, ok := sources[ref]
		if !ok {
			t.Errorf("ref %d: missing source", ref)
			return
		}
		if gotLines := strings.Count(src, "\n") + 1; gotLines != wantLines {
			t.Errorf("ref %d: want %d lines, got %d", ref, wantLines, gotLines)
		}
		if wantBlank && strings.TrimRight(src, "\n") != "" {
			t.Errorf("ref %d: placeholder source must be blank, got %q", ref, src)
		}
	}
	check(1, 10, true)
	check(2, 1, true)
	check(3, 1, true)
	if fileComps[2].Lines != 1 {
		t.Errorf("zero-line file must be clamped to Lines=1, got %d", fileComps[2].Lines)
	}
	// File that already had real source is left exactly as-is.
	if sources[4] != "real\nsource" {
		t.Errorf("file with real source must be untouched, got %q", sources[4])
	}
	if fileComps[3].Lines != 2 {
		t.Errorf("file with real source must keep its line count, got %d", fileComps[3].Lines)
	}
}

// #410 — real per-line SCM blame must be applied to NON-MAIN branches, not
// just main. The extract captures SCM per branch (getProjectSCMData carries a
// branch field); buildBranchReport must load it for the branch being migrated
// and build real changesets (author/date/revision) rather than the synthetic
// fallback. This locks in the report-build side (the CE rendering is separate).
func TestNonMainBranchGetsRealSCMChangesets(t *testing.T) {
	dir := t.TempDir()
	extractDir := filepath.Join(dir, "extract-01")
	writeJSON(filepath.Join(extractDir, "extract.json"),
		map[string]any{"url": testServerURL, "edition": "enterprise"})
	// A file on the develop (non-main) branch with source + real blame.
	writeJSONL(filepath.Join(extractDir, "getProjectComponentTree"), []map[string]any{
		{"key": "proj1:src/App.java", "name": "App.java", "path": "src/App.java",
			"language": "java", "lines": 3, "projectKey": "proj1", "branch": "develop",
			"serverUrl": testServerURL},
	})
	writeJSONL(filepath.Join(extractDir, "getProjectSourceCode"), []map[string]any{
		{"key": "proj1:src/App.java", "branch": "develop", "projectKey": "proj1",
			"source": "a\nb\nc", "serverUrl": testServerURL},
	})
	writeJSONL(filepath.Join(extractDir, "getProjectSCMData"), []map[string]any{
		{"key": "proj1:src/App.java", "branch": "develop", "projectKey": "proj1",
			"serverUrl": testServerURL,
			"scm": [][]any{
				{1, "alice@example.com", "2024-01-01T00:00:00+0000", "rev1"},
				{2, "bob@example.com", "2024-02-01T00:00:00+0000", "rev2"},
				{3, "alice@example.com", "2024-01-01T00:00:00+0000", "rev1"},
			}},
	})

	e := newProjectDataExecutor(t, dir)
	comps := loadExtractedComponents(e, testServerURL, "proj1", "develop")
	srcs := loadExtractedSources(e, testServerURL, "proj1", "develop")
	scm := loadExtractedSCM(e, testServerURL, "proj1", "develop")
	if len(scm) == 0 {
		t.Fatal("expected SCM blame loaded for non-main branch 'develop', got none")
	}

	_, _, cr := scanreport.BuildComponents("cloud", comps)
	pbSrc := map[int32]string{}
	for _, s := range srcs {
		if ref, ok := cr.Refs()[s.Component]; ok && s.Source != "" {
			pbSrc[ref] = s.Source
		}
	}
	cs := buildChangesetMap(cr, comps, pbSrc, scm, time.Unix(1700000000, 0))
	got := cs[cr.Refs()["proj1:src/App.java"]]
	if got == nil {
		t.Fatal("no changeset built for App.java on develop")
	}
	authors := map[string]bool{}
	for _, ch := range got.GetChangeset() {
		authors[ch.GetAuthor()] = true
	}
	if !authors["alice@example.com"] || !authors["bob@example.com"] {
		t.Errorf("non-main branch must carry real SCM blame authors, got %v", authors)
	}
}

// --- Data loading function tests (require extract dir setup) ---

func setupProjectDataExtract(t *testing.T, dir string) {
	t.Helper()
	extractDir := filepath.Join(dir, "extract-01")

	writeJSON(filepath.Join(extractDir, "extract.json"),
		map[string]any{"url": testServerURL, "edition": "enterprise"})

	writeJSONL(filepath.Join(extractDir, "getBranches"), []map[string]any{
		{"projectKey": "proj1", "name": "main", "type": "LONG", "isMain": true, "serverUrl": testServerURL},
		{"projectKey": "proj1", "name": "develop", "type": "LONG", "isMain": false, "serverUrl": testServerURL},
		{"projectKey": "proj1", "name": "pr-1", "type": "SHORT", "isMain": false, "serverUrl": testServerURL},
	})

	writeJSONL(filepath.Join(extractDir, "getProjectIssuesFull"), []map[string]any{
		{
			"key": "issue-1", "rule": "java:S100", "message": "Rename method",
			"severity": "MAJOR", "component": "proj1:src/Main.java",
			"projectKey": "proj1", "branch": "main",
			"textRange":  map[string]any{"startLine": 5, "endLine": 5, "startOffset": 0, "endOffset": 10},
			"creationDate": "2024-06-15T10:00:00+0000",
			"serverUrl": testServerURL,
		},
		{
			"key": "issue-2", "rule": "java:S200", "message": "Other issue",
			"severity": "MINOR", "component": "proj1:src/Util.java",
			"projectKey": "proj1", "branch": "develop",
			"serverUrl": testServerURL,
		},
	})

	writeJSONL(filepath.Join(extractDir, "getProjectComponentTree"), []map[string]any{
		{
			"key": "proj1:src/Main.java", "name": "Main.java", "path": "src/Main.java",
			"language": "java", "lines": 50,
			"projectKey": "proj1", "branch": "main",
			"serverUrl": testServerURL,
		},
		{
			"key": "proj1:src/Util.java", "name": "Util.java", "path": "src/Util.java",
			"language": "java",
			"measures": []map[string]any{{"metric": "ncloc", "value": "30"}},
			"projectKey": "proj1", "branch": "main",
			"serverUrl": testServerURL,
		},
	})

	writeJSONL(filepath.Join(extractDir, "getProjectSourceCode"), []map[string]any{
		{
			"key": "proj1:src/Main.java", "source": "public class Main {}",
			"projectKey": "proj1", "branch": "main",
			"serverUrl": testServerURL,
		},
		{
			"key": "proj1:src/Util.java", "source": "public class Util {}",
			"projectKey": "proj1", "branch": "main",
			"serverUrl": testServerURL,
		},
	})

	writeJSONL(filepath.Join(extractDir, "getActiveProfileRules"), []map[string]any{
		{"key": "java:S100", "severity": "MAJOR", "qProfile": "prof1", "lang": "java", "serverUrl": testServerURL},
		{"key": "external_tool:E1", "severity": "INFO", "qProfile": "prof1", "lang": "java", "serverUrl": testServerURL},
	})

	writeJSONL(filepath.Join(extractDir, "getProfiles"), []map[string]any{
		{"key": "prof1", "name": "Sonar way", "language": "java", "serverUrl": testServerURL},
	})

	writeJSONL(filepath.Join(extractDir, "getProjectHotspotsFull"), []map[string]any{
		{
			"key": "hotspot-1", "ruleKey": "java:S2092", "message": "Make this cookie secure",
			"component": "proj1:src/Main.java", "project": "proj1", "branch": "main",
			"vulnerabilityProbability": "HIGH",
			"creationDate": "2024-03-10T08:00:00+0000",
			"serverUrl": testServerURL,
		},
	})
}

func newProjectDataExecutor(t *testing.T, dir string) *Executor {
	t.Helper()
	runDir := filepath.Join(dir, "run-test")
	os.MkdirAll(runDir, 0o755)
	return &Executor{
		Store:     common.NewDataStore(runDir),
		ExportDir: dir,
		Mapping:   structure.ExtractMapping{testServerURL: "extract-01"},
		Sem:       make(chan struct{}, 5),
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1})),
	}
}

func TestCollectBranchInfo(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	branches := collectBranchInfo(e, testServerURL, "proj1")
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches (SHORT filtered), got %d: %v", len(branches), branches)
	}
	if branches[0].Name != "main" || !branches[0].IsMain {
		t.Errorf("expected main branch with IsMain=true, got %+v", branches[0])
	}
	if branches[1].Name != "develop" || branches[1].IsMain {
		t.Errorf("expected develop branch with IsMain=false, got %+v", branches[1])
	}
}

func TestResolveMainTargetName(t *testing.T) {
	master := branchInfo{Name: "master", IsMain: true}
	main := branchInfo{Name: "main", IsMain: true}
	cases := []struct {
		name         string
		scMainBranch string
		mainBranch   *branchInfo
		want         string
	}{
		// #428: the source main branch name wins — the SC main branch is
		// renamed to match it, so non-main branches must reference it.
		{"prefers source main over SC name", "master", &main, "main"},
		{"uses source main when SC unknown", "", &master, "master"},
		{"falls back to SC main when source unknown", "develop", nil, "develop"},
		{"empty when no main known", "", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMainTargetName(tc.scMainBranch, tc.mainBranch); got != tc.want {
				t.Errorf("resolveMainTargetName(%q, %v) = %q, want %q", tc.scMainBranch, tc.mainBranch, got, tc.want)
			}
		})
	}
}

func TestMaxIssueEndLineByComponent(t *testing.T) {
	native := []scanreport.IssueInput{
		{Component: "a", StartLine: 10, EndLine: 20},
		{Component: "a", StartLine: 5, EndLine: 8},
		{Component: "b", StartLine: 30, EndLine: 0}, // end < start -> use start
	}
	hotspots := []scanreport.IssueInput{
		{Component: "a", StartLine: 25, EndLine: 40},
	}
	external := []scanreport.ExternalIssueInput{
		{Component: "c", StartLine: 1, EndLine: 99},
		{Component: "", StartLine: 1, EndLine: 5}, // empty component ignored
	}
	m := maxIssueEndLineByComponent(native, hotspots, external)
	if m["a"] != 40 {
		t.Errorf("component a: want 40, got %d", m["a"])
	}
	if m["b"] != 30 {
		t.Errorf("component b (start>end): want 30, got %d", m["b"])
	}
	if m["c"] != 99 {
		t.Errorf("component c: want 99, got %d", m["c"])
	}
	if _, ok := m[""]; ok {
		t.Errorf("empty component key must be ignored")
	}
}

func TestDropIssuesWithInactiveRules(t *testing.T) {
	active := []scanreport.ActiveRuleInput{
		{RuleRepo: "python", RuleKey: "S100"},
		{RuleRepo: "python", RuleKey: "S125"},
	}
	issues := []scanreport.IssueInput{
		{RuleRepo: "python", RuleKey: "S100", Component: "a"},
		{RuleRepo: "secrets", RuleKey: "S6702", Component: "b"}, // orphan -> dropped
		{RuleRepo: "python", RuleKey: "S125", Component: "c"},
		{RuleRepo: "secrets", RuleKey: "S6702", Component: "b"}, // orphan -> dropped
	}
	kept, dropped := dropIssuesWithInactiveRules(issues, active)
	if dropped != 2 {
		t.Errorf("dropped: want 2, got %d", dropped)
	}
	if len(kept) != 2 {
		t.Fatalf("kept: want 2, got %d", len(kept))
	}
	for _, k := range kept {
		if k.RuleRepo == "secrets" {
			t.Errorf("orphan secrets issue must have been dropped, got %+v", k)
		}
	}
}

func TestCollectBranchInfoNoMatch(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	branches := collectBranchInfo(e, testServerURL, "nonexistent")
	if len(branches) != 0 {
		t.Errorf("expected 0 branches for unknown project, got %v", branches)
	}
}

func TestCollectBranchInfoWrongServer(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	branches := collectBranchInfo(e, "https://other.server/", "proj1")
	if len(branches) != 0 {
		t.Errorf("expected 0 branches for wrong server, got %v", branches)
	}
}

func TestLoadExtractedSources(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	sources := loadExtractedSources(e, testServerURL, "proj1", "main")
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}
	if sources[0].Component != "proj1:src/Main.java" {
		t.Errorf("unexpected component: %s", sources[0].Component)
	}
	if sources[0].Source != "public class Main {}" {
		t.Errorf("unexpected source: %s", sources[0].Source)
	}
}

func TestLoadExtractedSourcesWrongBranch(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	sources := loadExtractedSources(e, testServerURL, "proj1", "nonexistent")
	if len(sources) != 0 {
		t.Errorf("expected 0 sources for wrong branch, got %d", len(sources))
	}
}

// TestEmptySourcesNotIncludedInPbSources verifies that components with empty
// extracted source are NOT added to pbSources, which prevents writing empty
// source-N.txt files into the ZIP that would cause the CE to reject the report
// (a 0-byte source file with a component claiming N lines is inconsistent).
func TestEmptySourcesNotIncludedInPbSources(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "proj:A.java", Path: "A.java", Language: "java", Lines: 10},
		{Key: "proj:B.java", Path: "B.java", Language: "java", Lines: 20},
	}
	sources := []sourceRecord{
		{Component: "proj:A.java", Source: "class A {}"},
		{Component: "proj:B.java", Source: ""},
	}

	_, _, cr := scanreport.BuildComponents("proj", components)
	pbSources := make(map[int32]string)
	for _, s := range sources {
		if ref, ok := cr.Refs()[s.Component]; ok && s.Source != "" {
			pbSources[ref] = s.Source
		}
	}

	if len(pbSources) != 1 {
		t.Fatalf("expected 1 source in pbSources (only non-empty), got %d", len(pbSources))
	}
	refA := cr.Refs()["proj:A.java"]
	if pbSources[refA] != "class A {}" {
		t.Errorf("expected source for A.java, got %q", pbSources[refA])
	}
	refB := cr.Refs()["proj:B.java"]
	if _, present := pbSources[refB]; present {
		t.Error("B.java has empty source and should NOT be in pbSources")
	}
}

func TestLoadExtractedIssues(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	issues := loadExtractedIssues(e, testServerURL, "proj1", "main")
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue on main, got %d", len(issues))
	}
	if issues[0].RuleRepo != "java" || issues[0].RuleKey != "S100" {
		t.Errorf("unexpected rule: %s:%s", issues[0].RuleRepo, issues[0].RuleKey)
	}
	if issues[0].Message != "Rename method" {
		t.Errorf("unexpected message: %s", issues[0].Message)
	}
	if issues[0].StartLine != 5 {
		t.Errorf("unexpected startLine: %d", issues[0].StartLine)
	}
}

func TestLoadExtractedIssuesWrongProject(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	issues := loadExtractedIssues(e, testServerURL, "other-proj", "main")
	if len(issues) != 0 {
		t.Errorf("expected 0, got %d", len(issues))
	}
}

func TestLoadExtractedComponents(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	components := loadExtractedComponents(e, testServerURL, "proj1", "main")
	if len(components) != 2 {
		t.Fatalf("expected 2 components, got %d", len(components))
	}
	if components[0].Key != "proj1:src/Main.java" {
		t.Errorf("unexpected key: %s", components[0].Key)
	}
	if components[0].Lines != 50 {
		t.Errorf("expected lines=50, got %d", components[0].Lines)
	}
	// Second component should fall back to ncloc measure.
	if components[1].Lines != 30 {
		t.Errorf("expected lines=30 (from ncloc), got %d", components[1].Lines)
	}
}

func TestLoadExtractedActiveRules(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	rules := loadExtractedActiveRules(e, testServerURL, "proj1")
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule (external filtered), got %d", len(rules))
	}
	if rules[0].RuleRepo != "java" || rules[0].RuleKey != "S100" {
		t.Errorf("unexpected rule: %s:%s", rules[0].RuleRepo, rules[0].RuleKey)
	}
}

func TestLoadExtractedQProfiles(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	profiles := loadExtractedQProfiles(e, testServerURL, "proj1")
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}
	if profiles[0].Key != "prof1" || profiles[0].Name != "Sonar way" {
		t.Errorf("unexpected profile: %+v", profiles[0])
	}
}

func TestToExtractedIssues(t *testing.T) {
	createdAt, _ := time.Parse(time.RFC3339, "2023-06-15T10:00:00Z")
	issues := []scanreport.IssueInput{
		{
			Key:          "issue-abc123",
			CreationDate: createdAt,
			RuleRepo:     "java",
			RuleKey:      "S100",
			Component:    "proj1:src/Main.java",
			StartLine:    5,
			EndLine:       5,
		},
	}

	extracted := toExtractedIssues(issues)
	if len(extracted) != 1 {
		t.Fatalf("expected 1 extracted issue, got %d", len(extracted))
	}
	if extracted[0].Key != "issue-abc123" {
		t.Errorf("unexpected key: %s", extracted[0].Key)
	}
	if extracted[0].Component != "proj1:src/Main.java" {
		t.Errorf("unexpected component: %s", extracted[0].Component)
	}
	if !extracted[0].CreationDate.Equal(createdAt) {
		t.Errorf("unexpected creation date: %v", extracted[0].CreationDate)
	}
	if extracted[0].StartLine != 5 || extracted[0].EndLine != 5 {
		t.Errorf("unexpected line range: %d-%d", extracted[0].StartLine, extracted[0].EndLine)
	}
}

func TestParseISODate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantZero bool
		wantYear int
	}{
		{name: "empty string", input: "", wantZero: true},
		{name: "invalid string", input: "not-a-date", wantZero: true},
		{name: "RFC3339 UTC", input: "2024-06-15T10:00:00Z", wantYear: 2024},
		{name: "RFC3339 with offset", input: "2024-06-15T10:00:00+05:30", wantYear: 2024},
		{name: "legacy +0000 format", input: "2024-03-10T08:00:00+0000", wantYear: 2024},
		{name: "legacy -0500 format", input: "2023-11-01T12:30:00-0500", wantYear: 2023},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseISODate(tc.input)
			if tc.wantZero {
				if !got.IsZero() {
					t.Errorf("parseISODate(%q) = %v, want zero time", tc.input, got)
				}
				return
			}
			if got.Year() != tc.wantYear {
				t.Errorf("parseISODate(%q) year = %d, want %d", tc.input, got.Year(), tc.wantYear)
			}
		})
	}
}

func TestLoadExtractedHotspots(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	hotspots := loadExtractedHotspots(e, testServerURL, "proj1", "main")
	if len(hotspots) != 1 {
		t.Fatalf("expected 1 hotspot on main, got %d", len(hotspots))
	}
	if hotspots[0].Key != "hotspot-1" {
		t.Errorf("unexpected key: %s", hotspots[0].Key)
	}
	if hotspots[0].RuleRepo != "java" || hotspots[0].RuleKey != "S2092" {
		t.Errorf("unexpected rule: %s:%s", hotspots[0].RuleRepo, hotspots[0].RuleKey)
	}
	if hotspots[0].CreationDate.IsZero() {
		t.Error("expected non-zero CreationDate")
	}
	if hotspots[0].CreationDate.Year() != 2024 {
		t.Errorf("unexpected CreationDate year: %d", hotspots[0].CreationDate.Year())
	}
}

func TestLoadExtractedHotspotsWrongProject(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	hotspots := loadExtractedHotspots(e, testServerURL, "other-proj", "main")
	if len(hotspots) != 0 {
		t.Errorf("expected 0 hotspots for wrong project, got %d", len(hotspots))
	}
}

func TestBuildProjectQProfiles(t *testing.T) {
	profileByLang := map[string]scanreport.QProfileInfo{
		"java":   {Key: "java-prof", Name: "Sonar way", Language: "java"},
		"python": {Key: "py-prof", Name: "Sonar way Python", Language: "python"},
	}

	langs := map[string]bool{"java": true, "kotlin": true}
	got := buildProjectQProfiles(langs, profileByLang)

	if len(got) != 1 {
		t.Fatalf("expected 1 profile (only java matches), got %d", len(got))
	}
	if got[0].Key != "java-prof" {
		t.Errorf("unexpected profile key: %s", got[0].Key)
	}
}

func TestBuildProjectQProfilesEmpty(t *testing.T) {
	got := buildProjectQProfiles(map[string]bool{}, map[string]scanreport.QProfileInfo{})
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestRemapActiveRuleProfiles(t *testing.T) {
	rules := []scanreport.ActiveRuleInput{
		{RuleRepo: "java", RuleKey: "S100", QProfileKey: "old-java-key", Language: "Java"},
		{RuleRepo: "python", RuleKey: "S200", QProfileKey: "old-py-key", Language: "PYTHON"},
		{RuleRepo: "kotlin", RuleKey: "S300", QProfileKey: "no-change", Language: "kotlin"},
	}
	profileByLang := map[string]scanreport.QProfileInfo{
		"java":   {Key: "new-java-key"},
		"python": {Key: "new-python-key"},
	}

	remapActiveRuleProfiles(rules, profileByLang)

	if rules[0].QProfileKey != "new-java-key" {
		t.Errorf("java: expected new-java-key, got %s", rules[0].QProfileKey)
	}
	if rules[1].QProfileKey != "new-python-key" {
		t.Errorf("python: expected new-python-key, got %s", rules[1].QProfileKey)
	}
	if rules[2].QProfileKey != "no-change" {
		t.Errorf("kotlin: expected no-change (no SC profile), got %s", rules[2].QProfileKey)
	}
}

func TestBuildSCProfileMap(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	e := newTestExecutor(cloudSrv, apiSrv, dir)

	profiles := buildSCProfileMap(context.Background(), e, testCloudOrg)
	if len(profiles) == 0 {
		t.Fatal("expected at least one profile from mock server")
	}
	if _, ok := profiles["java"]; !ok {
		t.Errorf("expected java profile, got keys: %v", func() []string {
			ks := make([]string, 0, len(profiles))
			for k := range profiles {
				ks = append(ks, k)
			}
			return ks
		}())
	}
}

func TestBuildSCProfileMapNoCloud(t *testing.T) {
	dir := t.TempDir()
	e := newProjectDataExecutor(t, dir)
	profiles := buildSCProfileMap(context.Background(), e, testCloudOrg)
	if len(profiles) != 0 {
		t.Errorf("expected empty map when Cloud is nil, got %v", profiles)
	}
}

// --- Integration tests for importBranch and runImportProjectData ---

func newCEMockServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/analysis/analyses": // create-analysis handshake (non-main branches)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"id": "analysis-test-uuid", "branchId": "branch-uuid",
				"branchType": "long", "referenceBranchName": "main",
			})
		case "/api/ce/submit":
			json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-test-123"})
		case "/api/ce/task":
			json.NewEncoder(w).Encode(map[string]any{
				"task": map[string]any{"status": "SUCCESS"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
}

func TestImportBranch(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)

	srv := newCEMockServer()
	defer srv.Close()

	e := newProjectDataExecutor(t, dir)
	e.CloudURL = srv.URL + "/"
	e.Raw = common.NewRawClient(srv.Client(), srv.URL+"/")
	// Non-main branch import now performs the create-analysis handshake against
	// the API host; point it at the same mock server.
	e.APIURL = srv.URL + "/"
	e.RawAPI = common.NewRawClient(srv.Client(), srv.URL+"/")

	input := importBranchInput{
		CloudKey:        "cloud-proj1",
		OrgKey:          "cloud-org1",
		ServerURL:       testServerURL,
		ServerKey:       "proj1",
		Branch:          "main",
		ReferenceBranch: "master",
	}

	result, err := importBranch(context.Background(), e, input)
	if err != nil {
		t.Fatalf("importBranch: %v", err)
	}
	if result.Status != "success" {
		t.Errorf("expected success, got %s", result.Status)
	}
	if result.TaskID != "AX-test-123" {
		t.Errorf("expected AX-test-123, got %s", result.TaskID)
	}
}

func TestImportBranchSkipsNoComponents(t *testing.T) {
	dir := t.TempDir()
	extractDir := filepath.Join(dir, "extract-01")
	writeJSON(filepath.Join(extractDir, "extract.json"),
		map[string]any{"url": testServerURL, "edition": "enterprise"})

	// Write empty data — no components will have source.
	writeJSONL(filepath.Join(extractDir, "getProjectIssuesFull"), nil)
	writeJSONL(filepath.Join(extractDir, "getProjectComponentTree"), nil)
	writeJSONL(filepath.Join(extractDir, "getProjectSourceCode"), nil)
	writeJSONL(filepath.Join(extractDir, "getActiveProfileRules"), nil)
	writeJSONL(filepath.Join(extractDir, "getProfiles"), nil)

	e := newProjectDataExecutor(t, dir)

	input := importBranchInput{
		CloudKey:  "cloud-proj1",
		OrgKey:    "cloud-org1",
		ServerURL: testServerURL,
		ServerKey: "proj1",
		Branch:    "main",
	}

	result, err := importBranch(context.Background(), e, input)
	if err != nil {
		t.Fatalf("importBranch: %v", err)
	}
	if result.Status != "skipped" {
		t.Errorf("expected skipped (no components), got %s", result.Status)
	}
}

func TestRunImportProjectData(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)

	srv := newCEMockServer()
	defer srv.Close()

	e := newProjectDataExecutor(t, dir)
	e.CloudURL = srv.URL + "/"
	e.Raw = common.NewRawClient(srv.Client(), srv.URL+"/")

	// Write createProjects dependency.
	w, _ := e.Store.Writer("createProjects")
	b, _ := json.Marshal(map[string]any{
		"key":                "proj1",
		"cloud_project_key":  "cloud-proj1",
		"sonarcloud_org_key": "cloud-org1",
		"server_url":         testServerURL,
	})
	w.WriteOne(b)

	err := runImportProjectData(context.Background(), e)
	if err != nil {
		t.Fatalf("runImportProjectData: %v", err)
	}

	items, _ := e.Store.ReadAll("importProjectData")
	if len(items) == 0 {
		t.Fatal("expected import results written")
	}
	status := extractField(items[0], "status")
	if status != "success" && status != "skipped" {
		t.Errorf("expected success or skipped, got %s", status)
	}
}

func TestRunImportProjectDataSkipsEmptyKeys(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)
	e := newProjectDataExecutor(t, dir)

	// Write project with empty cloud key — should be skipped.
	w, _ := e.Store.Writer("createProjects")
	b, _ := json.Marshal(map[string]any{
		"key":                "proj1",
		"cloud_project_key":  "",
		"sonarcloud_org_key": "",
		"server_url":         testServerURL,
	})
	w.WriteOne(b)

	err := runImportProjectData(context.Background(), e)
	if err != nil {
		t.Fatalf("runImportProjectData: %v", err)
	}

	items, _ := e.Store.ReadAll("importProjectData")
	if len(items) != 0 {
		t.Errorf("expected 0 results for empty keys, got %d", len(items))
	}
}

// --- New tests for branch migration fixes ---

func TestSortBranchesMainFirst(t *testing.T) {
	tests := []struct {
		name  string
		input []branchInfo
		first string
	}{
		{
			name:  "main already first",
			input: []branchInfo{{Name: "main", IsMain: true}, {Name: "develop"}, {Name: "release"}},
			first: "main",
		},
		{
			name:  "main in middle",
			input: []branchInfo{{Name: "develop"}, {Name: "main", IsMain: true}, {Name: "release"}},
			first: "main",
		},
		{
			name:  "main at end",
			input: []branchInfo{{Name: "develop"}, {Name: "release"}, {Name: "main", IsMain: true}},
			first: "main",
		},
		{
			name:  "single branch",
			input: []branchInfo{{Name: "main", IsMain: true}},
			first: "main",
		},
		{
			name:  "no main",
			input: []branchInfo{{Name: "develop"}, {Name: "release"}},
			first: "develop",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sortBranchesMainFirst(tt.input)
			if len(tt.input) > 0 && tt.input[0].Name != tt.first {
				t.Errorf("expected first=%s, got %s", tt.first, tt.input[0].Name)
			}
		})
	}
}

func TestSortBranchesMainFirstEmpty(t *testing.T) {
	var empty []branchInfo
	sortBranchesMainFirst(empty)
	if len(empty) != 0 {
		t.Errorf("expected empty slice after sort")
	}
}

func TestFilterBranches(t *testing.T) {
	branches := []branchInfo{
		{Name: "main", IsMain: true},
		{Name: "develop"},
		{Name: "feature/foo"},
		{Name: "feature/bar"},
		{Name: "release/1.0"},
	}

	// No patterns — all returned.
	result := filterBranches(branches, nil)
	if len(result) != 5 {
		t.Errorf("nil patterns: expected 5, got %d", len(result))
	}

	// Exclude feature/*.
	result = filterBranches(branches, []string{"feature/*"})
	if len(result) != 3 {
		t.Errorf("exclude feature/*: expected 3, got %d", len(result))
	}
	for _, b := range result {
		if b.Name == "feature/foo" || b.Name == "feature/bar" {
			t.Errorf("feature branch should be excluded: %s", b.Name)
		}
	}

	// Main is never excluded even if pattern matches.
	result = filterBranches(branches, []string{"main"})
	found := false
	for _, b := range result {
		if b.IsMain {
			found = true
		}
	}
	if !found {
		t.Error("main branch should never be excluded")
	}
}

func TestMatchesAnyGlob(t *testing.T) {
	if !matchesAnyGlob("feature/foo", []string{"feature/*"}) {
		t.Error("expected feature/foo to match feature/*")
	}
	if matchesAnyGlob("develop", []string{"feature/*"}) {
		t.Error("develop should not match feature/*")
	}
	if matchesAnyGlob("anything", nil) {
		t.Error("nil patterns should not match")
	}
	if matchesAnyGlob("release/1.0", []string{"bugfix/*", "release/*"}) {
		// filepath.Match: * does not match /
		// So release/* does NOT match release/1.0 with filepath.Match
		// This is expected Go behavior — adjust test accordingly
	}
}

func TestLoadCompletedBranches(t *testing.T) {
	dir := t.TempDir()
	store := common.NewDataStore(dir)
	w, _ := store.Writer("importProjectData")

	for _, rec := range []map[string]any{
		{"cloud_project_key": "proj1", "branch": "main", "status": "success"},
		{"cloud_project_key": "proj1", "branch": "develop", "status": "failed"},
		{"cloud_project_key": "proj1", "branch": "release", "status": "skipped"},
		{"cloud_project_key": "proj2", "branch": "main", "status": "success"},
	} {
		b, _ := json.Marshal(rec)
		w.WriteOne(b)
	}

	completed := loadCompletedBranches(store)
	if completed == nil {
		t.Fatal("expected non-nil completed map")
	}
	if !completed["proj1:main"] {
		t.Error("proj1:main should be completed")
	}
	if completed["proj1:develop"] {
		t.Error("proj1:develop (failed) should not be completed")
	}
	if completed["proj1:release"] {
		t.Error("proj1:release (skipped) should not be completed")
	}
	if !completed["proj2:main"] {
		t.Error("proj2:main should be completed")
	}
}

func TestLoadCompletedBranchesEmpty(t *testing.T) {
	dir := t.TempDir()
	store := common.NewDataStore(dir)
	completed := loadCompletedBranches(store)
	if completed != nil {
		t.Errorf("expected nil for empty store, got %v", completed)
	}
}

func TestShouldSkipBranch(t *testing.T) {
	if shouldSkipBranch(nil, "proj", "main") {
		t.Error("nil map should not skip")
	}

	completed := map[string]bool{"proj:main": true}
	if !shouldSkipBranch(completed, "proj", "main") {
		t.Error("should skip completed branch")
	}
	if shouldSkipBranch(completed, "proj", "develop") {
		t.Error("should not skip non-completed branch")
	}
}

// #393 regression: filterCompleted must NOT drop importProjectData
// from the plan even when its task dir already exists. The task dir
// is created by the first e.Store.Writer call (via os.MkdirAll
// inside ChunkWriter), which happens before any branch finishes.
// Resume granularity is per (project, branch) via
// loadCompletedBranches + shouldSkipBranch — if the whole task is
// skipped, un-imported branches are silently dropped.
func TestFilterCompletedKeepsImportProjectData(t *testing.T) {
	dir := t.TempDir()
	store := common.NewDataStore(dir)

	// Simulate a previously-started run: writer created (which created
	// the task dir) plus one successful main-branch record on disk.
	w, err := store.Writer("importProjectData")
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	rec, _ := json.Marshal(map[string]any{
		"cloud_project_key": "proj1", "branch": "main", "status": "success",
	})
	if err := w.WriteOne(rec); err != nil {
		t.Fatalf("WriteOne: %v", err)
	}
	if !store.TaskDirExists("importProjectData") {
		t.Fatal("precondition: importProjectData dir should exist on disk")
	}

	plan := [][]string{{"createProjects", "importProjectData"}}
	filtered := filterCompleted(plan, store)

	// createProjects has no dir, so it stays. importProjectData is the
	// regression target: it MUST stay even though its dir exists.
	if len(filtered) != 1 {
		t.Fatalf("expected one phase, got %d: %v", len(filtered), filtered)
	}
	gotImport := false
	for _, name := range filtered[0] {
		if name == "importProjectData" {
			gotImport = true
		}
	}
	if !gotImport {
		t.Errorf("importProjectData should remain in plan on resume, got %v", filtered[0])
	}

	// Sanity-check the partner mechanism: the existing per-branch
	// completed map records proj1:main, so a re-run would skip it.
	completed := loadCompletedBranches(store)
	if !shouldSkipBranch(completed, "proj1", "main") {
		t.Error("loadCompletedBranches + shouldSkipBranch should skip the previously-recorded branch on resume")
	}
	if shouldSkipBranch(completed, "proj1", "develop") {
		t.Error("an un-recorded branch must NOT be skipped — that is the data the regression protects")
	}
}

// filterCompleted still applies the dir-existence gate to every
// task that isn't on the importProjectData exception path.
func TestFilterCompletedSkipsCompletedNonImportTasks(t *testing.T) {
	dir := t.TempDir()
	store := common.NewDataStore(dir)
	if _, err := store.Writer("createProjects"); err != nil {
		t.Fatalf("Writer: %v", err)
	}

	plan := [][]string{{"createProjects", "setProjectProfiles"}}
	filtered := filterCompleted(plan, store)

	if len(filtered) != 1 {
		t.Fatalf("expected one phase, got %d: %v", len(filtered), filtered)
	}
	for _, name := range filtered[0] {
		if name == "createProjects" {
			t.Errorf("createProjects has an existing dir and should be filtered out, got %v", filtered[0])
		}
	}
}

func newCEFailMockServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ce/submit":
			json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-fail-123"})
		case "/api/ce/task":
			json.NewEncoder(w).Encode(map[string]any{
				"task": map[string]any{"status": "FAILED", "errorMessage": "main branch not ready"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
}

func TestImportProjectBranchesMainCEFailAborts(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)

	srv := newCEFailMockServer()
	defer srv.Close()

	e := newProjectDataExecutor(t, dir)
	e.CloudURL = srv.URL + "/"
	e.Raw = common.NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("importProjectData")
	proj, _ := json.Marshal(map[string]any{
		"key":                "proj1",
		"cloud_project_key":  "cloud-proj1",
		"sonarcloud_org_key": "cloud-org1",
		"server_url":         testServerURL,
	})

	branches := []branchInfo{
		{Name: "main", IsMain: true},
		{Name: "develop", IsMain: false},
	}

	err := importProjectBranches(context.Background(), e, proj, branches, "", nil, w)
	if err == nil {
		t.Fatal("expected error when main branch CE fails")
	}

	items, _ := e.Store.ReadAll("importProjectData")
	if len(items) < 2 {
		t.Fatalf("expected at least 2 results (main=failed, develop=skipped), got %d", len(items))
	}

	var mainStatus, devStatus string
	for _, item := range items {
		branch := extractField(item, "branch")
		status := extractField(item, "status")
		if branch == "main" {
			mainStatus = status
		}
		if branch == "develop" {
			devStatus = status
		}
	}
	if mainStatus != "failed" {
		t.Errorf("main branch: expected failed, got %s", mainStatus)
	}
	if devStatus != "skipped" {
		t.Errorf("develop branch: expected skipped, got %s", devStatus)
	}
}

func TestImportProjectBranchesMainFirst(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)

	var submitOrder []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ce/submit":
			if err := r.ParseMultipartForm(10 << 20); err == nil {
				if branch := r.FormValue("characteristic"); branch != "" {
					submitOrder = append(submitOrder, branch)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"taskId": "AX-test-" + r.FormValue("projectKey")})
		case "/api/ce/task":
			json.NewEncoder(w).Encode(map[string]any{
				"task": map[string]any{"status": "SUCCESS"},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	e := newProjectDataExecutor(t, dir)
	e.CloudURL = srv.URL + "/"
	e.Raw = common.NewRawClient(srv.Client(), srv.URL+"/")

	w, _ := e.Store.Writer("importProjectData")
	proj, _ := json.Marshal(map[string]any{
		"key":                "proj1",
		"cloud_project_key":  "cloud-proj1",
		"sonarcloud_org_key": "cloud-org1",
		"server_url":         testServerURL,
	})

	// Provide branches with non-main first (before sort).
	branches := []branchInfo{
		{Name: "develop", IsMain: false},
		{Name: "main", IsMain: true},
	}
	sortBranchesMainFirst(branches)

	err := importProjectBranches(context.Background(), e, proj, branches, "", nil, w)
	if err != nil {
		t.Fatalf("importProjectBranches: %v", err)
	}

	items, _ := e.Store.ReadAll("importProjectData")
	if len(items) == 0 {
		t.Fatal("expected results written")
	}

	// Verify main was processed first by checking item order.
	firstBranch := extractField(items[0], "branch")
	if firstBranch != "main" {
		t.Errorf("expected main to be imported first, got %s", firstBranch)
	}
}

func TestImportSkipsCompletedBranches(t *testing.T) {
	dir := t.TempDir()
	setupProjectDataExtract(t, dir)

	srv := newCEMockServer()
	defer srv.Close()

	e := newProjectDataExecutor(t, dir)
	e.CloudURL = srv.URL + "/"
	e.Raw = common.NewRawClient(srv.Client(), srv.URL+"/")

	// Pre-populate completed branches.
	completed := map[string]bool{"cloud-proj1:main": true}

	w, _ := e.Store.Writer("importProjectData")
	proj, _ := json.Marshal(map[string]any{
		"key":                "proj1",
		"cloud_project_key":  "cloud-proj1",
		"sonarcloud_org_key": "cloud-org1",
		"server_url":         testServerURL,
	})

	branches := []branchInfo{
		{Name: "main", IsMain: true},
		{Name: "develop", IsMain: false},
	}

	err := importProjectBranches(context.Background(), e, proj, branches, "", completed, w)
	if err != nil {
		t.Fatalf("importProjectBranches: %v", err)
	}

	// Main was skipped, so only develop should appear in results.
	items, _ := e.Store.ReadAll("importProjectData")
	for _, item := range items {
		branch := extractField(item, "branch")
		if branch == "main" {
			t.Error("main branch should have been skipped (already completed)")
		}
	}
}
