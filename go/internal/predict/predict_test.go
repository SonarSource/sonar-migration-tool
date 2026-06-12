// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/report/summary"
)

const testServerURL = "https://sqs.example.com"

// writeFile is a small helper to put a string into a file under root.
func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeJSONL serialises objs as newline-separated JSON into path.
func writeJSONL(t *testing.T, path string, objs []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	for _, o := range objs {
		b, err := json.Marshal(o)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		f.Write(b)
		f.Write([]byte("\n"))
	}
}

// setupPredictiveFixture builds a small but realistic export directory:
//   - organizations.csv mapping one SQS org → one SQC org
//   - gates.csv with three gates (passthrough / dropped / remapped)
//   - projects.csv with two projects (one migrated, one skipped)
//   - an extract dir with getGateConditions matching the three gates
//
// Returns the export dir path.
func setupPredictiveFixture(t *testing.T) string {
	t.Helper()
	exportDir := t.TempDir()

	writeFile(t, exportDir, "organizations.csv",
		"sonarqube_org_key,sonarcloud_org_key,server_url\n"+
			"default,target-org,"+testServerURL+"\n")

	writeFile(t, exportDir, "gates.csv",
		"name,server_url,source_gate_key,is_default,sonarqube_org_key\n"+
			"Passthrough QG,"+testServerURL+",gate-1,false,default\n"+
			"Dropped QG,"+testServerURL+",gate-2,false,default\n"+
			"Remapped QG,"+testServerURL+",gate-3,false,default\n")

	writeFile(t, exportDir, "projects.csv",
		"name,key,server_url,sonarqube_org_key\n"+
			"App,com.example:app,"+testServerURL+",default\n"+
			"Skipped App,com.example:skip,"+testServerURL+",skipme\n")

	// organizations.csv only maps "default"; "skipme" is unmapped so its
	// projects.csv row gets no sonarcloud_org_key and is filtered out by
	// the synthesizer (mirrors migrate's shouldSkipOrg).

	extractID := "extract-0001"
	extractDir := filepath.Join(exportDir, extractID)
	writeFile(t, extractDir, "extract.json", `{"url":"`+testServerURL+`"}`)

	// getGateConditions: one record per source condition. Three gates:
	//   Passthrough QG → coverage LT 80    (unmapped → no note)
	//   Dropped QG    → contains_ai_code > 0 (mapped to empty → dropped note)
	//   Remapped QG   → software_quality_blocker_issues > 0
	//                       (composite expansion → remapped note,
	//                        not on the obvious-suppression list)
	writeJSONL(t, filepath.Join(extractDir, "getGateConditions", "conds.jsonl"),
		[]map[string]any{
			{"gateName": "Passthrough QG", "serverUrl": testServerURL,
				"metric": "coverage", "op": "LT", "error": "80"},
			{"gateName": "Dropped QG", "serverUrl": testServerURL,
				"metric": "contains_ai_code", "op": "GT", "error": "0"},
			{"gateName": "Remapped QG", "serverUrl": testServerURL,
				"metric": "software_quality_blocker_issues", "op": "GT", "error": "0"},
		})

	// getServerSettings + getServerSettingsDefinitions feed the Global
	// Settings section. Three settings:
	//   sonar.issues.sandbox.enabled=true → SQS-only (#240) → Skipped
	//                                       with the standard "does
	//                                       not exist" detail.
	//   sonar.issues.sandbox.enabled=false (hypothetical) → silent;
	//                                       to keep the fixture small
	//                                       we omit the default case.
	//   sonar.exclusions=**/vendor/**     → not on the SQS-only list →
	//                                       predicted Applied.
	writeJSONL(t, filepath.Join(extractDir, "getServerSettings", "settings.jsonl"),
		[]map[string]any{
			{"key": "sonar.issues.sandbox.enabled", "value": "true"},
			{"key": "sonar.exclusions", "value": "**/vendor/**"},
		})
	writeJSONL(t, filepath.Join(extractDir, "getServerSettingsDefinitions", "defs.jsonl"),
		[]map[string]any{
			{"key": "sonar.issues.sandbox.enabled", "defaultValue": ""},
			{"key": "sonar.exclusions", "defaultValue": ""},
		})

	return exportDir
}

// findSection returns the named section from a summary, or nil.
func findSection(s *summary.MigrationSummary, name string) *summary.Section {
	for i := range s.Sections {
		if s.Sections[i].Name == name {
			return &s.Sections[i]
		}
	}
	return nil
}

func TestGeneratePredictiveReport_HappyPath(t *testing.T) {
	exportDir := setupPredictiveFixture(t)

	pdfPath, err := GeneratePredictiveReport(exportDir)
	if err != nil {
		t.Fatalf("GeneratePredictiveReport: %v", err)
	}
	if !strings.HasSuffix(pdfPath, "predictive_migration_summary.pdf") {
		t.Errorf("unexpected PDF path: %q", pdfPath)
	}
	if _, err := os.Stat(pdfPath); err != nil {
		t.Fatalf("PDF not written: %v", err)
	}

	// Walk the predictive run dir so we can re-collect the summary and
	// inspect the section contents directly (the PDF itself is binary
	// and not worth parsing in a unit test).
	entries, err := os.ReadDir(exportDir)
	if err != nil {
		t.Fatalf("readdir exportDir: %v", err)
	}
	var runDir string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "predictive-") {
			runDir = filepath.Join(exportDir, e.Name())
			break
		}
	}
	if runDir == "" {
		t.Fatal("no predictive run directory found under exportDir")
	}

	mig, err := summary.CollectSummary(runDir, exportDir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}

	gates := findSection(mig, "Quality Gates")
	if gates == nil {
		t.Fatal("Quality Gates section missing")
	}

	// Passthrough QG → no note → stays in Succeeded.
	// Dropped QG    → drop note → moves to Partial.
	// Remapped QG   → remap note (composite, non-obvious) → moves to NearPerfect.
	bucketByName := func(items []summary.EntityItem, bucket string) map[string]string {
		out := map[string]string{}
		for _, it := range items {
			out[it.Name] = bucket
		}
		return out
	}
	placement := map[string]string{}
	for k, v := range bucketByName(gates.Succeeded, "Succeeded") {
		placement[k] = v
	}
	for k, v := range bucketByName(gates.NearPerfect, "NearPerfect") {
		placement[k] = v
	}
	for k, v := range bucketByName(gates.Partial, "Partial") {
		placement[k] = v
	}

	if got, want := placement["Passthrough QG"], "Succeeded"; got != want {
		t.Errorf("Passthrough QG: got %s, want %s", got, want)
	}
	if got, want := placement["Dropped QG"], "Partial"; got != want {
		t.Errorf("Dropped QG: got %s, want %s", got, want)
	}
	if got, want := placement["Remapped QG"], "NearPerfect"; got != want {
		t.Errorf("Remapped QG: got %s, want %s", got, want)
	}

	// Projects section: App goes through; Skipped App is filtered by
	// shouldSkipOrg (no sonarcloud_org_key) and won't appear in
	// Succeeded — but the summary collector also treats unmapped-org
	// rows as Skipped via its own logic, so just assert App is present.
	projects := findSection(mig, "Projects")
	if projects == nil {
		t.Fatal("Projects section missing")
	}
	var sawApp bool
	for _, it := range projects.Succeeded {
		if it.Name == "App" {
			sawApp = true
		}
	}
	if !sawApp {
		t.Errorf("expected App in Projects.Succeeded, got %+v", projects.Succeeded)
	}

	// Global Settings (#237 + #240): the SQS-only key (sandbox enabled
	// = true) should land in Skipped with the standard "does not exist
	// on SonarQube Cloud" detail; the regular customised setting should
	// land in Succeeded (predicted applied).
	settings := findSection(mig, "Global Settings")
	if settings == nil {
		t.Fatal("Global Settings section missing")
	}
	var sawSQSOnlySkipped, sawAppliedPredict bool
	for _, it := range settings.Skipped {
		if it.Name == "sonar.issues.sandbox.enabled" &&
			strings.Contains(it.Detail, "does not exist") {
			sawSQSOnlySkipped = true
		}
	}
	for _, it := range settings.Succeeded {
		if it.Name == "sonar.exclusions" {
			sawAppliedPredict = true
		}
	}
	if !sawSQSOnlySkipped {
		t.Errorf("expected sonar.issues.sandbox.enabled in Global Settings Skipped with explanatory detail; got %+v", settings.Skipped)
	}
	if !sawAppliedPredict {
		t.Errorf("expected sonar.exclusions in Global Settings Succeeded; got %+v", settings.Succeeded)
	}
}

func TestGeneratePredictiveReport_NoCSVs(t *testing.T) {
	emptyDir := t.TempDir()
	_, err := GeneratePredictiveReport(emptyDir)
	if err == nil {
		t.Error("expected error on exportDir without any mapping CSVs")
	}
}

// #363: the predictive Global Settings section must consolidate every
// sonar.auth.* key carrying a value into a single Skipped row keyed
// "sonar.auth.*" with the dedicated "must be redefined" wording.
func TestGeneratePredictiveReport_ConsolidatesSonarAuth(t *testing.T) {
	exportDir := t.TempDir()

	writeFile(t, exportDir, "organizations.csv",
		"sonarqube_org_key,sonarcloud_org_key,server_url\n"+
			"default,target-org,"+testServerURL+"\n")
	writeFile(t, exportDir, "projects.csv",
		"name,key,server_url,sonarqube_org_key\n"+
			"App,com.example:app,"+testServerURL+",default\n")
	writeFile(t, exportDir, "gates.csv",
		"name,server_url,source_gate_key,is_default,sonarqube_org_key\n")

	extractID := "extract-0001"
	extractDir := filepath.Join(exportDir, extractID)
	writeFile(t, extractDir, "extract.json", `{"url":"`+testServerURL+`"}`)

	// Three sonar.auth.* keys with values plus one with an empty value
	// (must stay silent). The non-auth control should remain in Succeeded.
	writeJSONL(t, filepath.Join(extractDir, "getServerSettings", "settings.jsonl"),
		[]map[string]any{
			{"key": "sonar.auth.saml.enabled", "value": "true"},
			{"key": "sonar.auth.github.enabled", "value": "true"},
			{"key": "sonar.auth.gitlab.clientId", "value": "abc"},
			{"key": "sonar.auth.bitbucket.enabled", "value": ""},
			{"key": "sonar.exclusions", "value": "**/vendor/**"},
		})
	writeJSONL(t, filepath.Join(extractDir, "getServerSettingsDefinitions", "defs.jsonl"),
		[]map[string]any{
			{"key": "sonar.auth.saml.enabled", "defaultValue": ""},
			{"key": "sonar.auth.github.enabled", "defaultValue": ""},
			{"key": "sonar.auth.gitlab.clientId", "defaultValue": ""},
			{"key": "sonar.auth.bitbucket.enabled", "defaultValue": ""},
			{"key": "sonar.exclusions", "defaultValue": ""},
		})

	if _, err := GeneratePredictiveReport(exportDir); err != nil {
		t.Fatalf("GeneratePredictiveReport: %v", err)
	}

	entries, err := os.ReadDir(exportDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var runDir string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "predictive-") {
			runDir = filepath.Join(exportDir, e.Name())
			break
		}
	}
	if runDir == "" {
		t.Fatal("no predictive run directory found")
	}

	mig, err := summary.CollectSummary(runDir, exportDir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	settings := findSection(mig, "Global Settings")
	if settings == nil {
		t.Fatal("Global Settings section missing")
	}

	// No per-key auth row may appear anywhere in the section.
	allBuckets := append([]summary.EntityItem{}, settings.Succeeded...)
	allBuckets = append(allBuckets, settings.NearPerfect...)
	allBuckets = append(allBuckets, settings.Partial...)
	allBuckets = append(allBuckets, settings.Skipped...)
	allBuckets = append(allBuckets, settings.Failed...)
	for _, it := range allBuckets {
		if strings.HasPrefix(it.Name, "sonar.auth.") && it.Name != "sonar.auth.*" {
			t.Errorf("per-key sonar.auth.* row %q must not appear; expected only the consolidated row", it.Name)
		}
	}

	// Exactly one consolidated row in Skipped with the dedicated wording.
	var seen int
	for _, it := range settings.Skipped {
		if it.Name == "sonar.auth.*" {
			seen++
			if it.Detail != "Settings not migrated. Authentication must be redefined from scratch on SonarQube Cloud" {
				t.Errorf("consolidated row Detail = %q; want the #363 wording", it.Detail)
			}
		}
	}
	if seen != 1 {
		t.Errorf("expected exactly 1 consolidated sonar.auth.* row in Skipped, got %d (skipped=%+v)", seen, settings.Skipped)
	}
}

// #323: predictive report must surface per-project hotspot sync stats
// and route projects with ACKNOWLEDGED hotspots to NearPerfect with
// the "N ACKNOWLEDGED hotspot(s) left as TO_REVIEW" callout in the
// Details column.
func TestGeneratePredictiveReport_HotspotAcknowledgedDemotion(t *testing.T) {
	exportDir := t.TempDir()

	writeFile(t, exportDir, "organizations.csv",
		"sonarqube_org_key,sonarcloud_org_key,server_url\n"+
			"default,target-org,"+testServerURL+"\n")
	writeFile(t, exportDir, "projects.csv",
		"name,key,server_url,sonarqube_org_key\n"+
			"App,com.example:app,"+testServerURL+",default\n")
	writeFile(t, exportDir, "gates.csv",
		"name,server_url,source_gate_key,is_default,sonarqube_org_key\n")

	extractID := "extract-0001"
	extractDir := filepath.Join(exportDir, extractID)
	writeFile(t, extractDir, "extract.json", `{"url":"`+testServerURL+`"}`)

	// Three actionable hotspots: 2 SAFE (cleanly synced), 1 ACKNOWLEDGED
	// (demoted to TO_REVIEW). Plus one TO_REVIEW with no comments — not
	// actionable, must NOT inflate the actionable count.
	writeJSONL(t, filepath.Join(extractDir, "getProjectHotspotsFull", "hotspots.jsonl"),
		[]map[string]any{
			{"key": "h1", "project": "com.example:app", "status": "REVIEWED", "resolution": "SAFE"},
			{"key": "h2", "project": "com.example:app", "status": "REVIEWED", "resolution": "SAFE"},
			{"key": "h3", "project": "com.example:app", "status": "REVIEWED", "resolution": "ACKNOWLEDGED"},
			{"key": "h4", "project": "com.example:app", "status": "TO_REVIEW"},
		})

	if _, err := GeneratePredictiveReport(exportDir); err != nil {
		t.Fatalf("GeneratePredictiveReport: %v", err)
	}

	entries, err := os.ReadDir(exportDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var runDir string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "predictive-") {
			runDir = filepath.Join(exportDir, e.Name())
			break
		}
	}
	if runDir == "" {
		t.Fatal("no predictive run directory found")
	}

	mig, err := summary.CollectSummary(runDir, exportDir)
	if err != nil {
		t.Fatalf("CollectSummary: %v", err)
	}
	projects := findSection(mig, "Projects")
	if projects == nil {
		t.Fatal("Projects section missing")
	}

	// The project must land in NearPerfect (not Succeeded) because
	// one ACKNOWLEDGED hotspot was demoted.
	if len(projects.Succeeded) != 0 {
		var names []string
		for _, it := range projects.Succeeded {
			names = append(names, it.Name)
		}
		t.Errorf("project must NOT remain in Succeeded; found %v", names)
	}
	var app *summary.EntityItem
	for i := range projects.NearPerfect {
		if projects.NearPerfect[i].Name == "App" {
			app = &projects.NearPerfect[i]
			break
		}
	}
	if app == nil {
		t.Fatalf("expected App in Projects.NearPerfect; got %+v", projects.NearPerfect)
	}

	// The Detail must carry both the hotspot sync fraction and the
	// ack= demotion segment so the renderer emits the #323 callout.
	if !strings.Contains(app.Detail, "h=2/3") {
		t.Errorf("Detail must include h=2/3 hotspot sync fraction; got %q", app.Detail)
	}
	if !strings.Contains(app.Detail, "ack=1") {
		t.Errorf("Detail must include ack=1 ACKNOWLEDGED-demoted segment; got %q", app.Detail)
	}
}
