// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A global setting that leaked into the per-project extract used to be
// POSTed once per project and rejected every time: 49 keys across 858
// projects produced 42,048 identical warnings and 42,048 wasted requests.
// SonarQube Cloud's verdict is a property of the KEY, so the first
// rejection must be remembered and the key abandoned for the rest of the
// run.
func TestRunSetProjectSettingsAbandonsKeyAfterProjectScopeRejection(t *testing.T) {
	const projects = 25
	rejectedKey := "sonar.dbcleaner.daysBeforeDeletingClosedIssues"

	var (
		mu       sync.Mutex
		attempts = map[string]int{}
	)
	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		key := r.FormValue("key")
		mu.Lock()
		attempts[key]++
		mu.Unlock()
		if key == rejectedKey {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"errors":[{"msg":"Setting '%s' cannot be set on a Project"}]}`, key)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mountSettingsDefinitions(cloudMux)
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	extractDir := filepath.Join(e.ExportDir, "extract-01", "getProjectSettings")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, _ := os.Create(filepath.Join(extractDir, "results.1.jsonl"))
	pw, _ := e.Store.Writer("createProjects")
	for i := 0; i < projects; i++ {
		proj := fmt.Sprintf("proj%02d", i)
		// The doomed global key plus one legitimate per-project override.
		for _, rec := range []map[string]any{
			{"project": proj, "key": rejectedKey, "value": "30"},
			{"project": proj, "key": "sonar.exclusions", "value": "**/gen/**"},
		} {
			b, _ := json.Marshal(rec)
			f.Write(b)
			f.Write([]byte("\n"))
		}
		pb, _ := json.Marshal(map[string]any{
			"key": proj, "server_url": testServerURL,
			"sonarcloud_org_key": "org1", "cloud_project_key": "org1_" + proj,
		})
		pw.WriteOne(pb)
	}
	f.Close()

	if err := runSetProjectSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetProjectSettings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// The rejected key must be attempted only by the workers already in
	// flight when the first 400 lands — bounded by the fan-out width, not
	// by the project count. Without memoisation this is exactly `projects`
	// (and was 858 per key in the field).
	got := attempts[rejectedKey]
	maxExpected := cap(e.Sem) + 2 // fan-out width plus scheduling slack
	if got > maxExpected {
		t.Errorf("rejected key attempted %d times for %d projects (fan-out %d) — the verdict was not memoised",
			got, projects, cap(e.Sem))
	}
	if got == 0 {
		t.Errorf("rejected key was never attempted; the runtime classifier cannot learn without one attempt")
	}
	t.Logf("rejected key attempted %d/%d times (fan-out %d)", got, projects, cap(e.Sem))

	// Legitimate per-project overrides must be entirely unaffected.
	if want := projects; attempts["sonar.exclusions"] != want {
		t.Errorf("sonar.exclusions applied %d times, want %d — a real override was dropped",
			attempts["sonar.exclusions"], want)
	}
}

// Curated pre-flight: server-internal and SQS-only keys have no
// project-scope counterpart on SonarQube Cloud, so they must never reach
// the API at all — no request, no 400, no retry. setGlobalSettings has
// applied these filters for some time; the project path had neither.
func TestRunSetProjectSettingsFiltersInternalAndSQSOnlyKeys(t *testing.T) {
	cloudMux := http.NewServeMux()
	mu, hitsPtr := mountSettingsSetCapture(cloudMux)
	mountSettingsDefinitions(cloudMux)
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	extractDir := filepath.Join(e.ExportDir, "extract-01", "getProjectSettings")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, _ := os.Create(filepath.Join(extractDir, "results.1.jsonl"))
	for _, rec := range []map[string]any{
		// internalSettingPrefixes: bundled .NET analyzer manifest fields.
		{"project": "proj1", "key": "sonaranalyzer-cs.pluginKey", "value": "csharpenterprise"},
		{"project": "proj1", "key": "sonaranalyzer-vbnet.pluginVersion", "value": "10.0"},
		// internalSettingPrefixes: server JDBC plumbing.
		{"project": "proj1", "key": "sonar.plsql.jdbc.driver.class", "value": "oracle.jdbc.Driver"},
		// sqsOnlyPrefixes: the newer .NET analyzer naming.
		{"project": "proj1", "key": "sonar.cs.analyzer.dotnet.pluginVersion", "value": "10.25"},
		// sqsOnlySettings exact match: instance-level server concern.
		{"project": "proj1", "key": "sonar.core.serverBaseURL", "value": "https://sq.internal"},
		// A genuine project-scoped setting — must still be applied.
		{"project": "proj1", "key": "sonar.exclusions", "value": "**/gen/**"},
	} {
		b, _ := json.Marshal(rec)
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()

	pw, _ := e.Store.Writer("createProjects")
	pb, _ := json.Marshal(map[string]any{
		"key": "proj1", "server_url": testServerURL,
		"sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj1",
	})
	pw.WriteOne(pb)

	if err := runSetProjectSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetProjectSettings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	hits := *hitsPtr
	if len(hits) != 1 {
		var keys []string
		for _, h := range hits {
			keys = append(keys, h.key)
		}
		t.Fatalf("expected only the genuine setting to reach SQC, got %d: %v", len(hits), keys)
	}
	if hits[0].key != "sonar.exclusions" {
		t.Errorf("wrong key reached SQC: %q", hits[0].key)
	}
}

// The leaked-globals signature is a high, near-uniform per-project record
// count. Detecting it turns a multi-thousand-line warning storm into one
// actionable line — and it is computed from counts tallied during the
// iteration, not a second full read of a quarter-million-record corpus.
func TestLooksLikeLeakedGlobals(t *testing.T) {
	build := func(projects, minPerProj, maxPerProj int) perProjectRecordCounts {
		c := perProjectRecordCounts{}
		for p := 0; p < projects; p++ {
			n := minPerProj
			if projects > 1 && maxPerProj > minPerProj {
				n = minPerProj + (maxPerProj-minPerProj)*p/(projects-1)
			}
			c[fmt.Sprintf("srv/proj%02d", p)] = n
		}
		return c
	}
	cases := []struct {
		name       string
		projects   int
		minPerProj int
		maxPerProj int
		want       bool
	}{
		// The customer shape: 214 settings on every project.
		{name: "uniform high count", projects: 10, minPerProj: 214, maxPerProj: 214, want: true},
		{name: "uniform high with jitter", projects: 12, minPerProj: 40, maxPerProj: 41, want: true},
		{name: "sparse overrides", projects: 10, minPerProj: 1, maxPerProj: 6, want: false},
		{name: "high but uneven", projects: 10, minPerProj: 5, maxPerProj: 90, want: false},
		{name: "too few projects", projects: 3, minPerProj: 214, maxPerProj: 214, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, got := build(c.projects, c.minPerProj, c.maxPerProj).looksLikeLeakedGlobals()
			if got != c.want {
				t.Errorf("looksLikeLeakedGlobals = %v, want %v", got, c.want)
			}
		})
	}
}
