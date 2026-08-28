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
	"strings"
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

// The rejection memo must be shared by BOTH paths in the task. It was wired
// into the per-record loop only, so the global-propagation post-pass re-ran
// every key SonarQube Cloud had already rejected against every remaining
// project — reproducing the exact warning storm the memo exists to stop.
//
// Here one key is rejected at project scope AND is a customized global that
// Cloud lists at project scope but not org scope, so both paths want to write
// it. Only a handful of attempts may reach the API, not one per project.
func TestRunSetProjectSettingsSharesRejectionMemoWithGlobalPropagation(t *testing.T) {
	const projects = 20
	rejected := "sonar.leaky.global"

	var mu sync.Mutex
	attempts := map[string]int{}

	cloudMux := http.NewServeMux()
	cloudMux.HandleFunc("POST /api/settings/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		key := r.FormValue("key")
		mu.Lock()
		attempts[key]++
		mu.Unlock()
		if key == rejected {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"errors":[{"msg":"Setting '%s' cannot be set on a Project"}]}`, key)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// Project scope defines the key; org scope does not — which is what makes
	// the post-pass want to propagate it to every project.
	mountSettingsDefinitionsScoped(cloudMux,
		[]map[string]any{{"key": "sonar.exclusions", "type": "STRING"}},
		[]map[string]any{
			{"key": "sonar.exclusions", "type": "STRING"},
			{"key": rejected, "type": "STRING"},
		})
	addDefaultCloudHandler(cloudMux)
	e := newCustomCloudTest(t, cloudMux)

	// The customized-globals corpus the post-pass reads.
	base := filepath.Join(e.ExportDir, "extract-01")
	for task, recs := range map[string][]map[string]any{
		"getServerSettingsDefinitions": {{"key": rejected, "defaultValue": "off"}},
		"getServerSettings":            {{"key": rejected, "value": "on", "parentValue": "off"}},
	} {
		dir := filepath.Join(base, task)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		f, _ := os.Create(filepath.Join(dir, "results.1.jsonl"))
		for _, r := range recs {
			b, _ := json.Marshal(r)
			f.Write(b)
			f.Write([]byte("\n"))
		}
		f.Close()
	}

	// One project carries the key as an explicit record so the per-record
	// loop hits the rejection first; the rest only get it via the post-pass.
	psDir := filepath.Join(base, "getProjectSettings")
	if err := os.MkdirAll(psDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pf, _ := os.Create(filepath.Join(psDir, "results.1.jsonl"))
	b, _ := json.Marshal(map[string]any{"project": "proj00", "key": rejected, "value": "on"})
	pf.Write(b)
	pf.Write([]byte("\n"))
	pf.Close()

	pw, _ := e.Store.Writer("createProjects")
	for i := 0; i < projects; i++ {
		proj := fmt.Sprintf("proj%02d", i)
		rec, _ := json.Marshal(map[string]any{
			"key": proj, "server_url": testServerURL,
			"sonarcloud_org_key": "org1", "cloud_project_key": "org1_" + proj,
		})
		pw.WriteOne(rec)
	}

	if err := runSetProjectSettings(context.Background(), e); err != nil {
		t.Fatalf("runSetProjectSettings: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	got := attempts[rejected]
	maxExpected := cap(e.Sem) + 2 // in-flight width plus scheduling slack
	if got > maxExpected {
		t.Errorf("rejected key attempted %d times across %d projects (fan-out %d) — the post-pass does not share the memo",
			got, projects, cap(e.Sem))
	}
	if got == 0 {
		t.Error("the key was never attempted, so the memo cannot have been learned")
	}
	t.Logf("rejected key attempted %d/%d times across both paths", got, projects)
}

// leftAtDefault is the "don't touch what the operator never chose" test.
// Its two failure modes are not symmetric — a wrong "customized" costs
// one redundant API call, a wrong "default" silently drops a real
// override — so the table pins the fail-open cases as hard as the skips.
func TestSQSSettingDefaultsLeftAtDefault(t *testing.T) {
	defs := &sqsSettingDefaults{
		defaultValue: map[string]string{
			"declared.string": "keep",
			"declared.csv":    "a,b",
			"declared.empty":  "",
		},
		declared: map[string]bool{
			"declared.string": true,
			"declared.csv":    true,
			"declared.empty":  true,
		},
	}

	cases := []struct {
		name string
		key  string
		raw  string
		want bool
	}{
		// parentValue is authoritative: same value as the scope above
		// means the project is inheriting, not overriding.
		{"value equals parentValue", "undeclared", `{"value":"true","parentValue":"true"}`, true},
		{"value differs from parentValue", "undeclared", `{"value":"false","parentValue":"true"}`, false},
		// Multi-value settings compare as sets — /api/settings/values
		// does not promise a stable element order.
		{"values set-equal to parentValues", "undeclared",
			`{"values":["b","a"],"parentValues":["a","b"]}`, true},
		{"values differ from parentValues", "undeclared",
			`{"values":["py","python"],"parentValues":["py"]}`, false},
		// No parent scope supplied a value, so the declared default is
		// the only baseline there is.
		{"value equals declared default", "declared.string", `{"value":"keep"}`, true},
		{"value differs from declared default", "declared.string", `{"value":"other"}`, false},
		{"values equal declared CSV default", "declared.csv", `{"values":["b","a"]}`, true},
		{"values differ from declared CSV default", "declared.csv", `{"values":["a"]}`, false},
		// Fail open. An undeclared key with no parentValue has no
		// baseline at all, and an empty declared default is
		// indistinguishable from a missing one.
		{"undeclared key with no parent value", "undeclared", `{"value":"anything"}`, false},
		{"declared with empty default and no parent value", "declared.empty", `{"value":"x"}`, false},
		// PROPERTY_SET: comparing arbitrary object arrays for "is this
		// the default" is not worth the risk of a wrong skip.
		{"fieldValues are never at default", "undeclared",
			`{"fieldValues":[{"fileRegexp":"Generated test"}],"parentFieldValues":[{"fileRegexp":"Generated test"}]}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := defs.leftAtDefault(c.key, json.RawMessage(c.raw)); got != c.want {
				t.Errorf("leftAtDefault(%q, %s) = %v, want %v", c.key, c.raw, got, c.want)
			}
		})
	}

	// A failed definitions load must degrade to the pre-existing
	// "apply everything" behaviour, never to skipping wholesale.
	var nilDefs *sqsSettingDefaults
	if nilDefs.leftAtDefault("any", json.RawMessage(`{"value":"v","parentValue":"v"}`)) {
		t.Error("nil baseline reported a setting as left-at-default; a failed load must fail open")
	}
}

// A project sitting on the SonarQube Server default has made no choice
// to carry over. Writing the value anyway converts an inherited value
// into a pinned project override on SonarQube Cloud, which then stops
// tracking the organization default and has to be unset by hand.
//
// The extract's inherited=true filter covers this for a clean corpus;
// this is the check that holds when the corpus is not clean.
func TestRunSetProjectSettingsLeavesServerDefaultsAlone(t *testing.T) {
	hits, logs := runProjectSettingsPropagationTest(t,
		[]map[string]any{
			{"key": "projA", "server_url": testServerURL,
				"sonarcloud_org_key": "org1", "cloud_project_key": "org1_projA"},
		},
		[]map[string]any{
			// Taken verbatim from the committed 2026-08-18-01 corpus:
			// value == parentValue, so the project is inheriting.
			{"project": "projA", "key": "sonar.autodetect.ai.code",
				"value": "true", "parentValue": "true"},
			// Same key family, genuinely overridden — must still land.
			{"project": "projA", "key": "sonar.cfamily.ignoreHeaderComments",
				"value": "false", "parentValue": "true"},
			// Set-equal to the parent, order differs.
			{"project": "projA", "key": "sonar.text.inclusions",
				"values": []string{"**/*.sh", "**/*.conf"}, "parentValues": []string{"**/*.conf", "**/*.sh"}},
			// A real multi-value override.
			{"project": "projA", "key": "sonar.python.file.suffixes",
				"values": []string{"py", "python"}, "parentValues": []string{"py"}},
			// Equals the declared default, no parent scope involved.
			{"project": "projA", "key": "sonar.coverage.exclusions",
				"value": "covexclude.*"},
			// No baseline of any kind — fail open and apply.
			{"project": "projA", "key": "sonar.exclusions",
				"values": []string{"**/excluded/*"}},
		},
		// No customized globals, so the propagation post-pass adds nothing.
		nil,
		[]map[string]any{
			{"key": "sonar.coverage.exclusions", "type": "STRING", "defaultValue": "covexclude.*"},
		},
		[]map[string]any{}, []map[string]any{},
	)

	applied := map[string]bool{}
	for _, h := range hits {
		applied[h.key] = true
	}
	for _, key := range []string{
		"sonar.cfamily.ignoreHeaderComments",
		"sonar.python.file.suffixes",
		"sonar.exclusions",
	} {
		if !applied[key] {
			t.Errorf("%s is a real project override and was not applied (hits: %+v)", key, hits)
		}
	}
	for _, key := range []string{
		"sonar.autodetect.ai.code",
		"sonar.text.inclusions",
		"sonar.coverage.exclusions",
	} {
		if applied[key] {
			t.Errorf("%s equals its SonarQube Server default and must not be written (hits: %+v)", key, hits)
		}
	}
	if len(hits) != 3 {
		t.Errorf("expected exactly 3 settings.set calls, got %d (%+v)", len(hits), hits)
	}
	if !strings.Contains(logs, "matches its SonarQube Server default") {
		t.Errorf("skipped defaults must be explained in the log, got:\n%s", logs)
	}
}

// The per-key tally means one log line per setting, not one per
// (project, setting) pair — the same shape as the rejection memo. A
// global settings table leaked into the per-project extract is the case
// that matters: 214 keys × 858 projects is 183,612 records, and a
// per-record log line for each is what buried the last customer run.
func TestRunSetProjectSettingsLogsLeftAtDefaultOncePerKey(t *testing.T) {
	const projects = 12
	var perProject []map[string]any
	var projectRows []map[string]any
	for i := 0; i < projects; i++ {
		proj := fmt.Sprintf("proj%02d", i)
		projectRows = append(projectRows, map[string]any{
			"key": proj, "server_url": testServerURL,
			"sonarcloud_org_key": "org1", "cloud_project_key": "org1_" + proj,
		})
		perProject = append(perProject, map[string]any{
			"project": proj, "key": "sonar.autodetect.ai.code",
			"value": "true", "parentValue": "true",
		})
	}

	hits, logs := runProjectSettingsPropagationTest(t,
		projectRows, perProject, nil, nil, []map[string]any{}, []map[string]any{})

	if len(hits) != 0 {
		t.Errorf("expected no settings.set calls for a key every project inherits, got %d (%+v)", len(hits), hits)
	}
	if n := strings.Count(logs, "matches its SonarQube Server default"); n != 1 {
		t.Errorf("expected 1 per-key log line for %d projects, got %d", projects, n)
	}
	if !strings.Contains(logs, "projects_affected=12") {
		t.Errorf("end-of-task summary must report how many projects were affected, got:\n%s", logs)
	}
}
