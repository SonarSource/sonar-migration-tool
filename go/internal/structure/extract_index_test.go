// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

const (
	taskIssuesFull  = "getProjectIssuesFull" // project-scoped
	taskProjects    = "getProjects"          // project-scoped, read by #688 supersession
	taskActiveRules = "getActiveProfileRules"
	unknownProject  = "no-such-project"
)

// indexTasks are the tasks the equivalence tests compare: two project-scoped
// tasks and one server-wide one, which takes the newestFinished path.
var indexTasks = []string{taskIssuesFull, taskProjects, taskActiveRules}

// layoutTask is one task of one extract. With a .done directory, finished
// decides the marker; output decides whether the task directory exists, so a
// finished task without output and unfinished output are both reachable.
type layoutTask struct {
	name     string
	finished bool
	output   bool
	chunks   [][]string
}

// layoutExtract is one extract run of a generated export directory.
type layoutExtract struct {
	id      string
	url     string
	meta    map[string]any
	hasDone bool     // false is a legacy extract that predates .done
	failed  []string // project keys written to failed_projects.json
	tasks   []layoutTask
}

func (e layoutExtract) String() string {
	meta, _ := json.Marshal(e.meta)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s done=%v failed=%v\n", e.id, meta, e.hasDone, e.failed)
	for _, task := range e.tasks {
		fmt.Fprintf(&b, "  %s finished=%v output=%v chunks=%v\n", task.name, task.finished, task.output, task.chunks)
	}
	return b.String()
}

func describeLayout(extracts []layoutExtract) string {
	var b strings.Builder
	for _, e := range extracts {
		b.WriteString(e.String())
	}
	return b.String()
}

// writeLayout renders the extracts under root.
func writeLayout(tb testing.TB, root string, extracts []layoutExtract) {
	tb.Helper()
	for _, e := range extracts {
		dir := filepath.Join(root, e.id)
		writeFileTB(tb, filepath.Join(dir, "extract.json"), mustJSON(tb, e.meta))
		if e.hasDone {
			mkdirTB(tb, filepath.Join(dir, ".done"))
		}
		if e.failed != nil {
			list := make([]common.FailedProject, len(e.failed))
			for i, k := range e.failed {
				list[i] = common.FailedProject{Key: k, Task: taskIssuesFull, Error: "boom"}
			}
			writeFileTB(tb, filepath.Join(dir, common.FailedProjectsFileName), mustJSON(tb, list))
		}
		for _, task := range e.tasks {
			if e.hasDone && task.finished {
				writeFileTB(tb, filepath.Join(dir, ".done", task.name), nil)
			}
			if task.output {
				writeChunksTB(tb, filepath.Join(dir, task.name), task.chunks)
			}
		}
	}
}

// layoutGen draws export directories from a fixed-seed generator, so a
// failure names a seed that reproduces it.
type layoutGen struct {
	r    *rand.Rand
	keys []string
	// nonProjects lets meta draw non-projects extracts (#688).
	nonProjects bool
}

func newLayoutGen(seed uint64) *layoutGen {
	return &layoutGen{
		r:    rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		keys: []string{"p0", "p1", "p2", "p3", "p4", "p5"},
	}
}

func (g *layoutGen) key() string { return g.keys[g.r.IntN(len(g.keys))] }

func (g *layoutGen) otherKey(not string) string {
	for {
		if k := g.key(); k != not {
			return k
		}
	}
}

func (g *layoutGen) keySubset() []string {
	n := 1 + g.r.IntN(3)
	var out []string
	for len(out) < n {
		if k := g.key(); !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// record returns one JSONL line for a record of the task. The spellings
// cover every field recordProjectKey and projectSpellings look at, records
// whose spellings disagree, and a line that is not JSON at all.
func (g *layoutGen) record(id string) string {
	p := g.key()
	var rec map[string]any
	switch g.r.IntN(8) {
	case 0, 1:
		rec = map[string]any{"projectKey": p}
	case 2:
		rec = map[string]any{"project": p}
	case 3:
		rec = map[string]any{"key": p}
	case 4:
		rec = map[string]any{"projectKey": p, "project": g.otherKey(p)}
	case 5:
		rec = map[string]any{"project": p, "key": g.otherKey(p)}
	case 6:
		rec = map[string]any{"projectKey": p, "project": p, "key": "issue-" + id}
	default:
		if g.r.IntN(3) == 0 {
			return "not json " + id
		}
		rec = map[string]any{"projectKey": p, "key": g.otherKey(p)}
	}
	rec["id"] = id
	b, _ := json.Marshal(rec)
	return string(b)
}

func (g *layoutGen) task(extractID, name string, hasDone bool) layoutTask {
	task := layoutTask{name: name, output: g.r.IntN(5) > 0, finished: hasDone && g.r.IntN(4) > 0}
	if !task.output {
		return task
	}
	files := g.r.IntN(4) // zero files is an empty task directory
	for c := range files {
		var lines []string
		for i := range 1 + g.r.IntN(4) {
			lines = append(lines, g.record(extractID+"/"+name+"/"+strconv.Itoa(c)+"/"+strconv.Itoa(i)))
		}
		task.chunks = append(task.chunks, lines)
	}
	return task
}

// meta draws an extract.json: legacy full, full, partial without project
// keys, partial sliced by project keys, or a non-projects extract (#688).
func (g *layoutGen) meta(url string) (map[string]any, bool) {
	meta := map[string]any{"url": url}
	nonProjects := false
	kinds := 8
	if g.nonProjects {
		kinds = 10
	}
	switch g.r.IntN(kinds) {
	case 0:
		// legacy full extract: no "partial" field
	case 1, 2:
		meta["partial"] = false
	case 3, 4:
		meta["partial"] = true
	case 5, 6, 7:
		meta["partial"] = true
		meta["project_keys"] = g.keySubset()
	default:
		meta["partial"] = true
		meta["objects"] = []string{"groups"}
		nonProjects = true
		if g.r.IntN(2) == 0 {
			meta["project_keys"] = g.keySubset()
		}
	}
	return meta, nonProjects
}

// layout draws one export directory of one or two servers with one to four
// extracts each.
func (g *layoutGen) layout() []layoutExtract {
	var extracts []layoutExtract
	servers := []string{streamTestURL}
	if g.r.IntN(3) == 0 {
		servers = append(servers, streamTestURL2)
	}
	for s, url := range servers {
		for n := range 1 + g.r.IntN(4) {
			e := layoutExtract{
				id:      fmt.Sprintf("2026-08-%02d-%04d", 20+s, n+1),
				url:     url,
				hasDone: g.r.IntN(3) > 0,
			}
			var nonProjects bool
			e.meta, nonProjects = g.meta(url)
			if nonProjects && g.r.IntN(2) == 0 {
				e.failed = []string{g.key()}
			}
			for _, name := range indexTasks {
				e.tasks = append(e.tasks, g.task(e.id, name, e.hasDone))
			}
			extracts = append(extracts, e)
		}
	}
	return extracts
}

// universe is every project key a layout can name, plus one it never does.
func (g *layoutGen) universe() []string {
	return append(slices.Clone(g.keys), unknownProject)
}

// wantProjectItems is the reference ProjectItems must reproduce: the full
// ExtractItems stream narrowed to the server and to the project's spellings.
func wantProjectItems(root string, mapping ExtractMapping, key, serverURL, projectKey string) []string {
	var out []string
	for it := range ExtractItems(root, mapping, key) {
		if it.ServerURL != serverURL {
			continue
		}
		if projectKey != "" && !decodeProjectSpellings(it.Data).has(projectKey) {
			continue
		}
		out = append(out, string(it.Data))
	}
	return out
}

// gotProjectItems collects ProjectItems, checking every record carries the
// requested server URL.
func gotProjectItems(t *testing.T, idx *ExtractIndex, key, serverURL, projectKey string) []string {
	t.Helper()
	var out []string
	for it := range idx.ProjectItems(key, serverURL, projectKey) {
		if it.ServerURL != serverURL {
			t.Errorf("ProjectItems(%s, %s, %s) yielded server %q", key, serverURL, projectKey, it.ServerURL)
		}
		out = append(out, string(it.Data))
	}
	return out
}

// assertIndexMatchesScan compares one shared index with the filtered scan for
// every server, task and project of the layout.
func assertIndexMatchesScan(t *testing.T, root string, mapping ExtractMapping, projects []string, layout string) {
	t.Helper()
	idx := NewExtractIndex(root, mapping)
	for serverURL := range mapping {
		for _, task := range indexTasks {
			for _, p := range projects {
				want := wantProjectItems(root, mapping, task, serverURL, p)
				got := gotProjectItems(t, idx, task, serverURL, p)
				if !slices.Equal(got, want) {
					t.Fatalf("ProjectItems(%s, %s, %s) =\n  %v\nwant\n  %v\nmapping %v\nlayout:\n%s",
						task, serverURL, p, got, want, mapping, layout)
				}
			}
		}
	}
}

// Hand-written layouts, one per case the index must agree with the scan on.
func TestExtractIndexMatchesExtractItemsNamedLayouts(t *testing.T) {
	rec := func(fields ...string) string {
		m := map[string]any{}
		for i := 0; i+1 < len(fields); i += 2 {
			m[fields[i]] = fields[i+1]
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	full := func(id string) layoutExtract {
		return layoutExtract{id: id, url: streamTestURL, meta: map[string]any{"url": streamTestURL, "partial": false}, hasDone: true}
	}
	partial := func(id string, keys ...string) layoutExtract {
		meta := map[string]any{"url": streamTestURL, "partial": true}
		if keys != nil {
			meta["project_keys"] = keys
		}
		return layoutExtract{id: id, url: streamTestURL, meta: meta, hasDone: true}
	}
	issues := func(finished bool, chunks ...[]string) layoutTask {
		return layoutTask{name: taskIssuesFull, finished: finished, output: chunks != nil, chunks: chunks}
	}
	rules := func(finished bool, v string) layoutTask {
		return layoutTask{name: taskActiveRules, finished: finished, output: true, chunks: [][]string{{rec("key", "rule", "v", v)}}}
	}
	with := func(e layoutExtract, tasks ...layoutTask) layoutExtract {
		e.tasks = tasks
		return e
	}
	mixed := [][]string{
		{rec("projectKey", "a", "v", "1"), rec("project", "b", "v", "1"), "not json"},
		{rec("key", "c", "v", "1"), rec("projectKey", "a", "project", "b", "v", "1")},
		{rec("project", "c", "key", "a", "v", "1"), rec("projectKey", "b", "v", "1")},
	}

	cases := map[string][]layoutExtract{
		"single full extract": {
			with(full(runA), issues(true, mixed...), rules(true, "1")),
		},
		"full plus partials with project keys": {
			with(full(runA), issues(true, mixed...), rules(true, "1")),
			with(partial(runB, "a"), issues(true, []string{rec("projectKey", "a", "v", "2"), rec("projectKey", "c", "v", "2")}), rules(true, "2")),
			with(partial("2026-08-20-0003", "b"), issues(true, []string{rec("project", "b", "v", "3")}, []string{rec("projectKey", "b", "project", "a", "v", "3")})),
		},
		"partials only": {
			with(partial(runA, "a", "b"), issues(true, mixed...)),
			with(partial(runB, "b"), issues(true, []string{rec("projectKey", "b", "v", "2")})),
		},
		"partial without project keys supersedes all": {
			with(full(runA), issues(true, mixed...)),
			with(partial(runB), issues(true, []string{rec("projectKey", "a", "v", "2")})),
		},
		"later extract did not finish the task": {
			with(full(runA), issues(true, mixed...), rules(true, "1")),
			with(partial(runB, "a"), issues(false, []string{rec("projectKey", "a", "v", "2")}), rules(false, "2")),
		},
		"later extract has no task dir but finished it": {
			with(full(runA), issues(true, mixed...), rules(true, "1")),
			with(partial(runB, "a"), issues(true)),
		},
		"legacy extracts without done markers": {
			func() layoutExtract {
				e := with(full(runA), issues(false, mixed...), rules(false, "1"))
				e.hasDone = false
				return e
			}(),
			func() layoutExtract {
				e := with(partial(runB, "b"), issues(false, []string{rec("project", "b", "v", "2")}), rules(false, "2"))
				e.hasDone = false
				return e
			}(),
		},
		"two servers": {
			with(full(runA), issues(true, mixed...), rules(true, "1")),
			func() layoutExtract {
				e := with(full("2026-08-21-0001"), issues(true, []string{rec("projectKey", "a", "v", "other")}), rules(true, "other"))
				e.url, e.meta["url"] = streamTestURL2, streamTestURL2
				return e
			}(),
		},
	}
	for name, extracts := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeLayout(t, root, extracts)
			mapping, err := GetUniqueExtracts(root)
			if err != nil {
				t.Fatal(err)
			}
			assertIndexMatchesScan(t, root, mapping, []string{"a", "b", "c", unknownProject}, describeLayout(extracts))
		})
	}
}

// Many generated layouts mixing every case above at random: extract kinds,
// .done states, servers, spellings and chunk layouts. Non-projects extracts
// (#688) supersede through getProjects and failed_projects.json, so they get
// their own run and a divergence there does not hide one elsewhere.
func TestExtractIndexMatchesExtractItemsRandomLayouts(t *testing.T) {
	t.Run("project extracts", func(t *testing.T) { checkRandomLayouts(t, false) })
	t.Run("with non-projects extracts", func(t *testing.T) { checkRandomLayouts(t, true) })
}

func checkRandomLayouts(t *testing.T, nonProjects bool) {
	const layouts = 250
	for seed := uint64(1); seed <= layouts; seed++ {
		g := newLayoutGen(seed)
		g.nonProjects = nonProjects
		extracts := g.layout()
		root := t.TempDir()
		writeLayout(t, root, extracts)
		mapping, err := GetUniqueExtracts(root)
		if err != nil {
			t.Fatal(err)
		}
		t.Run("seed "+strconv.FormatUint(seed, 10), func(t *testing.T) {
			assertIndexMatchesScan(t, root, mapping, g.universe(), describeLayout(extracts))
		})
	}
}

// One index serves every project read of a run, from several goroutines at
// once; its answers must not drift with reuse.
func TestExtractIndexReuseIsStableAndConcurrent(t *testing.T) {
	g := newLayoutGen(7)
	g.keys = []string{"p0", "p1", "p2"}
	root := t.TempDir()
	var extracts []layoutExtract
	for n := range 4 {
		e := layoutExtract{id: fmt.Sprintf("2026-08-20-%04d", n+1), url: streamTestURL, hasDone: true,
			meta: map[string]any{"url": streamTestURL, "partial": n > 0}}
		if n > 0 {
			e.meta["project_keys"] = []string{g.keys[n%len(g.keys)]}
		}
		for _, name := range indexTasks {
			task := g.task(e.id, name, true)
			task.finished, task.output = true, true
			task.chunks = append(task.chunks, []string{g.record(e.id + "/" + name + "/extra")})
			e.tasks = append(e.tasks, task)
		}
		extracts = append(extracts, e)
	}
	writeLayout(t, root, extracts)
	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}

	type query struct{ task, project string }
	want := map[query][]string{}
	for _, task := range indexTasks {
		for _, p := range g.universe() {
			want[query{task, p}] = wantProjectItems(root, mapping, task, streamTestURL, p)
		}
	}

	idx := NewExtractIndex(root, mapping)
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := range 5 {
				for q, expected := range want {
					var got []string
					for it := range idx.ProjectItems(q.task, streamTestURL, q.project) {
						got = append(got, string(it.Data))
					}
					if !slices.Equal(got, expected) {
						select {
						case errs <- fmt.Sprintf("worker %d round %d %v: got %v, want %v", w, round, q, got, expected):
						default:
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// Breaking out of ProjectItems stops it, and the index still answers in full
// afterwards.
func TestExtractIndexStopsEarly(t *testing.T) {
	root := t.TempDir()
	writeLayout(t, root, []layoutExtract{{
		id: runA, url: streamTestURL, meta: map[string]any{"url": streamTestURL},
		tasks: []layoutTask{{name: taskIssuesFull, output: true, chunks: [][]string{
			{`{"projectKey":"a","n":1}`, `{"projectKey":"a","n":2}`},
			{`{"projectKey":"a","n":3}`},
		}}},
	}})
	mapping, _ := GetUniqueExtracts(root)
	idx := NewExtractIndex(root, mapping)

	var seen []string
	for it := range idx.ProjectItems(taskIssuesFull, streamTestURL, "a") {
		seen = append(seen, string(it.Data))
		break
	}
	if !slices.Equal(seen, []string{`{"projectKey":"a","n":1}`}) {
		t.Fatalf("got %v, want exactly the first record", seen)
	}
	if got := gotProjectItems(t, idx, taskIssuesFull, streamTestURL, "a"); len(got) != 3 {
		t.Errorf("after an early stop the index yielded %d records, want 3", len(got))
	}
}

// Portfolio-scoped tasks and an empty project key take the unindexed path,
// which must still be the filtered scan.
func TestExtractIndexFallbacksMatchExtractItems(t *testing.T) {
	root := t.TempDir()
	writeTestJSON(t, filepath.Join(root, runA, "extract.json"), map[string]any{"url": streamTestURL, "partial": true})
	writeTestJSON(t, filepath.Join(root, runB, "extract.json"),
		map[string]any{"url": streamTestURL, "partial": true, "project_keys": []string{"a"}})
	portfolioRecords(root, runA, "getPortfolios", `{"key":"p1","v":1}`, `{"key":"p2","v":1}`)
	portfolioRecords(root, runB, "getPortfolios", `{"key":"p2","v":2}`, `{"key":"p3","v":2}`)
	portfolioRecords(root, runA, taskIssuesFull, `{"projectKey":"a","v":1}`, `{"project":"b","v":1}`)
	portfolioRecords(root, runB, taskIssuesFull, `{"projectKey":"a","v":2}`)
	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}
	idx := NewExtractIndex(root, mapping)

	for _, p := range []string{"p1", "p2", "p3", "", unknownProject} {
		want := wantProjectItems(root, mapping, "getPortfolios", streamTestURL, p)
		if got := gotProjectItems(t, idx, "getPortfolios", streamTestURL, p); !slices.Equal(got, want) {
			t.Errorf("getPortfolios %q: got %v, want %v", p, got, want)
		}
	}
	want := wantProjectItems(root, mapping, taskIssuesFull, streamTestURL, "")
	if len(want) != 2 {
		t.Fatalf("fixture: the full scan yielded %v, want b from the older extract and a from the newer", want)
	}
	if got := gotProjectItems(t, idx, taskIssuesFull, streamTestURL, ""); !slices.Equal(got, want) {
		t.Errorf("empty project key: got %v, want %v", got, want)
	}
}

// benchExport writes a merged set shaped like a sliced migration: one full
// extract of every project and six partial ones re-extracting ten projects
// each, with twenty chunk files of a few KB per project.
func benchExport(b *testing.B) (string, ExtractMapping, []string) {
	b.Helper()
	const (
		projects      = 60
		partials      = 6
		filesPerProj  = 20
		recordsPerFil = 2
	)
	payload := strings.Repeat("x", 1500)
	keys := make([]string, projects)
	for i := range keys {
		keys[i] = fmt.Sprintf("project-%02d", i)
	}
	root := b.TempDir()
	writeExtract := func(id string, meta map[string]any, scope []string) {
		writeFileTB(b, filepath.Join(root, id, "extract.json"), mustJSON(b, meta))
		writeFileTB(b, filepath.Join(root, id, ".done", taskIssuesFull), nil)
		var chunks [][]string
		for _, k := range scope {
			for f := range filesPerProj {
				var lines []string
				for r := range recordsPerFil {
					lines = append(lines, fmt.Sprintf(`{"projectKey":%q,"key":"%s-%d-%d","message":%q}`, k, id, f, r, payload))
				}
				chunks = append(chunks, lines)
			}
		}
		writeChunksTB(b, filepath.Join(root, id, taskIssuesFull), chunks)
	}
	writeExtract("2026-08-20-0001", map[string]any{"url": streamTestURL}, keys)
	for p := range partials {
		scope := keys[p*10 : p*10+10]
		writeExtract(fmt.Sprintf("2026-08-20-%04d", p+2),
			map[string]any{"url": streamTestURL, "partial": true, "project_keys": scope}, scope)
	}
	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		b.Fatal(err)
	}
	return root, mapping, keys
}

// Reading every project once, as the migrate project-data loaders do, by
// scanning the merged set per project versus through one shared index.
func BenchmarkProjectReads(b *testing.B) {
	root, mapping, keys := benchExport(b)
	b.Run("scan", func(b *testing.B) {
		for range b.N {
			n := 0
			for _, k := range keys {
				n += len(wantProjectItems(root, mapping, taskIssuesFull, streamTestURL, k))
			}
			if n == 0 {
				b.Fatal("no records read")
			}
		}
	})
	b.Run("index", func(b *testing.B) {
		for range b.N {
			idx := NewExtractIndex(root, mapping)
			n := 0
			for _, k := range keys {
				for range idx.ProjectItems(taskIssuesFull, streamTestURL, k) {
					n++
				}
			}
			if n == 0 {
				b.Fatal("no records read")
			}
		}
	})
}

// The helpers below mirror writeChunks and writeTestJSON for testing.TB, so
// the benchmark can build its fixture too.

func writeChunksTB(tb testing.TB, taskDir string, chunks [][]string) {
	tb.Helper()
	mkdirTB(tb, taskDir)
	for i, lines := range chunks {
		var buf []byte
		for _, l := range lines {
			buf = append(buf, l...)
			buf = append(buf, '\n')
		}
		writeFileTB(tb, filepath.Join(taskDir, "results."+strconv.Itoa(i+1)+".jsonl"), buf)
	}
}

func writeFileTB(tb testing.TB, path string, data []byte) {
	tb.Helper()
	mkdirTB(tb, filepath.Dir(path))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		tb.Fatal(err)
	}
}

func mkdirTB(tb testing.TB, dir string) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
}

func mustJSON(tb testing.TB, v any) []byte {
	tb.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// #688: a non-projects extract supersedes only the projects it read without
// error (getProjects or task records, minus failed_projects.json), never
// every project and never its project_keys alone.
func TestExtractIndexNonProjectsExtractSupersession(t *testing.T) {
	cases := map[string]layoutExtract{
		"no project keys, read nothing": {
			meta: map[string]any{"url": streamTestURL, "partial": true, "objects": []string{"groups"}},
		},
		"project keys of a failed project": {
			meta: map[string]any{"url": streamTestURL, "partial": true, "objects": []string{"groups"},
				"project_keys": []string{"a"}},
			failed: []string{"a"},
		},
	}
	for name, later := range cases {
		t.Run(name, func(t *testing.T) {
			later.id, later.url, later.hasDone = runB, streamTestURL, true
			later.tasks = []layoutTask{{name: taskIssuesFull, finished: true}}
			extracts := []layoutExtract{{
				id: runA, url: streamTestURL, meta: map[string]any{"url": streamTestURL},
				tasks: []layoutTask{{name: taskIssuesFull, output: true, chunks: [][]string{{`{"projectKey":"a","v":1}`}}}},
			}, later}
			root := t.TempDir()
			writeLayout(t, root, extracts)
			mapping, err := GetUniqueExtracts(root)
			if err != nil {
				t.Fatal(err)
			}
			assertIndexMatchesScan(t, root, mapping, []string{"a"}, describeLayout(extracts))
		})
	}
}

// A record whose routing fields only partly decode: recordProjectKey gives
// up on it, so the scan never supersedes it, and the index must not
// attribute it to the project it half-names either.
func TestExtractIndexPartlyDecodableRecord(t *testing.T) {
	root := t.TempDir()
	writeLayout(t, root, []layoutExtract{{
		id: runA, url: streamTestURL, meta: map[string]any{"url": streamTestURL},
		tasks: []layoutTask{{name: taskIssuesFull, output: true, chunks: [][]string{{`{"projectKey":"a","key":5}`}}}},
	}, {
		id: runB, url: streamTestURL, hasDone: true,
		meta:  map[string]any{"url": streamTestURL, "partial": true, "project_keys": []string{"a"}},
		tasks: []layoutTask{{name: taskIssuesFull, finished: true, output: true, chunks: [][]string{{`{"projectKey":"a","v":2}`}}}},
	}})
	mapping, err := GetUniqueExtracts(root)
	if err != nil {
		t.Fatal(err)
	}
	assertIndexMatchesScan(t, root, mapping, []string{"a"}, "")
}
