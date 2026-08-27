// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSourceCorpus builds a getProjectSourceCode fixture with nProjects
// projects, one of which is "target". Each record carries realistically
// bulky source text and per-line highlighted HTML, because the whole point
// of the two-stage decode is not to materialize those for records that
// belong to another project.
func writeSourceCorpus(t *testing.T, dir string, nProjects, filesPerProject int) {
	t.Helper()
	taskDir := filepath.Join(dir, "extract-01", "getProjectSourceCode")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}

	body := strings.Repeat("// a line of source code that takes up some room\n", 40)
	highlighted := make([]string, 40)
	for i := range highlighted {
		highlighted[i] = "<span class=\"k\">// a line of source code that takes up some room</span>"
	}

	// One record per chunk file, which is what production does for this
	// task: extract writes each source record with ChunkWriter.WriteOne,
	// and WriteOne creates its own results.N.jsonl. That layout is why
	// chunk-granular streaming is record-granular here.
	chunk := 0
	for p := 0; p < nProjects; p++ {
		project := fmt.Sprintf("project-%03d", p)
		if p == 0 {
			project = "target"
		}
		for i := 0; i < filesPerProject; i++ {
			chunk++
			rec := map[string]any{
				"key":              fmt.Sprintf("%s:file%d.go", project, i),
				"projectKey":       project,
				"branch":           "main",
				"source":           body,
				"highlightedLines": highlighted,
			}
			b, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(taskDir, fmt.Sprintf("results.%d.jsonl", chunk))
			if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// BenchmarkLoadBranchSourceData is the repo's first benchmark. Run with
// -benchmem to get the per-load allocation figure quoted in the PR:
//
//	go test ./internal/migrate/ -run '^$' -bench LoadBranchSourceData -benchmem
func BenchmarkLoadBranchSourceData(b *testing.B) {
	t := &testing.T{}
	dir := b.TempDir()
	writeSourceCorpus(t, dir, 50, 20)
	e := newProjectDataExecutor(t, dir)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sources, highlights := loadBranchSourceData(e, testServerURL, "target", "main")
		if len(sources) == 0 || len(highlights) == 0 {
			b.Fatal("fixture produced no records")
		}
	}
}
