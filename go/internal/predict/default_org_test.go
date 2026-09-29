// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/report/summary"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// setupUnmappedFixture is setupPredictiveFixture with the one thing #566
// is about: organizations.csv lists the source org but leaves
// sonarcloud_org_key blank, which is exactly what `structure` produced
// for a unified config before this fix. Both projects hang off that one
// org, so nothing at all should migrate unless a default is applied.
func setupUnmappedFixture(t *testing.T) string {
	t.Helper()
	exportDir := setupPredictiveFixture(t)

	writeFile(t, exportDir, "organizations.csv",
		"sonarqube_org_key,sonarcloud_org_key,server_url\n"+
			"default,,"+testServerURL+"\n")

	writeFile(t, exportDir, "projects.csv",
		"name,key,server_url,sonarqube_org_key\n"+
			"App,com.example:app,"+testServerURL+",default\n"+
			"Other App,com.example:other,"+testServerURL+",default\n")

	return exportDir
}

// testLogger returns a logger writing into buf, so the WARN/INFO lines
// can be asserted without touching the process-wide default logger.
func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func readOrgKeys(t *testing.T, exportDir string) []string {
	t.Helper()
	rows, err := structure.LoadCSV(exportDir, orgCSVFileName)
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		v, _ := row["sonarcloud_org_key"].(string)
		out = append(out, v)
	}
	return out
}

// #566: an unmapped organizations.csv plus a default organization must
// be filled in, the same way migrate does it, instead of reporting
// every entity as "Organization skipped".
func TestApplyDefaultOrg_UnmappedRowsGetTheDefault(t *testing.T) {
	exportDir := setupUnmappedFixture(t)
	var buf bytes.Buffer

	if err := applyDefaultOrg(exportDir, "my-org", testLogger(&buf)); err != nil {
		t.Fatalf("applyDefaultOrg: %v", err)
	}

	for _, got := range readOrgKeys(t, exportDir) {
		if got != "my-org" {
			t.Errorf("sonarcloud_org_key: got %q, want my-org", got)
		}
	}
	if !strings.Contains(buf.String(), "default organization") {
		t.Errorf("expected a log line naming the default organization, got:\n%s", buf.String())
	}
}

// Every other column and the header order survive the rewrite.
func TestApplyDefaultOrg_PreservesOtherColumns(t *testing.T) {
	exportDir := setupUnmappedFixture(t)
	writeFile(t, exportDir, "organizations.csv",
		"sonarqube_org_key,sonarcloud_org_key,binding_key,server_url,alm,project_count\n"+
			"default,,repo/one,"+testServerURL+",github,7\n")

	if err := applyDefaultOrg(exportDir, "my-org", testLogger(&bytes.Buffer{})); err != nil {
		t.Fatalf("applyDefaultOrg: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(exportDir, orgCSVFileName))
	if err != nil {
		t.Fatalf("reading organizations.csv: %v", err)
	}
	want := "sonarqube_org_key,sonarcloud_org_key,binding_key,server_url,alm,project_count\n" +
		"default,my-org,repo/one," + testServerURL + ",github,7\n"
	if string(raw) != want {
		t.Errorf("organizations.csv:\ngot:\n%s\nwant:\n%s", raw, want)
	}
}

// A hand-mapped CSV always wins, and the ignored default is called out
// — same rule migrate applies (#281).
func TestApplyDefaultOrg_MappedRowsWinAndWarn(t *testing.T) {
	exportDir := setupPredictiveFixture(t) // already maps default → target-org
	var buf bytes.Buffer

	if err := applyDefaultOrg(exportDir, "my-org", testLogger(&buf)); err != nil {
		t.Fatalf("applyDefaultOrg: %v", err)
	}

	for _, got := range readOrgKeys(t, exportDir) {
		if got != "target-org" {
			t.Errorf("existing mapping was overwritten: got %q, want target-org", got)
		}
	}
	if !strings.Contains(buf.String(), "ignored") {
		t.Errorf("expected a WARN that the default organization is ignored, got:\n%s", buf.String())
	}
}

// #566: no mapping and no default used to produce a silent "0 migrated"
// report. It now warns.
func TestApplyDefaultOrg_NoMappingNoDefaultWarns(t *testing.T) {
	exportDir := setupUnmappedFixture(t)
	var buf bytes.Buffer

	if err := applyDefaultOrg(exportDir, "", testLogger(&buf)); err != nil {
		t.Fatalf("applyDefaultOrg: %v", err)
	}

	for _, got := range readOrgKeys(t, exportDir) {
		if got != "" {
			t.Errorf("CSV should be untouched, got %q", got)
		}
	}
	logged := buf.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("expected a WARN when nothing is mapped and no default exists, got:\n%s", logged)
	}
	if !strings.Contains(logged, "No organization mapping has been defined") {
		t.Errorf("expected the missing-mapping wording, got:\n%s", logged)
	}
}

// A mapped CSV with no default supplied is the ordinary case: silence.
func TestApplyDefaultOrg_MappedNoDefaultIsSilent(t *testing.T) {
	exportDir := setupPredictiveFixture(t)
	var buf bytes.Buffer

	if err := applyDefaultOrg(exportDir, "", testLogger(&buf)); err != nil {
		t.Fatalf("applyDefaultOrg: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log output for an already-mapped CSV, got:\n%s", buf.String())
	}
}

// A missing organizations.csv is BuildPredictiveRun's problem to report,
// not this function's — it must not error or create the file.
func TestApplyDefaultOrg_MissingCSVIsNotAnError(t *testing.T) {
	exportDir := t.TempDir()

	if err := applyDefaultOrg(exportDir, "my-org", testLogger(&bytes.Buffer{})); err != nil {
		t.Fatalf("applyDefaultOrg: %v", err)
	}
	if _, err := os.Stat(filepath.Join(exportDir, orgCSVFileName)); !os.IsNotExist(err) {
		t.Errorf("organizations.csv should not have been created, stat err = %v", err)
	}
}

// End-to-end regression for #566: the same export directory produces a
// report claiming nothing migrates without a default, and one where
// every project migrates with it.
func TestGeneratePredictiveReport_DefaultOrgRescuesSkippedEntities(t *testing.T) {
	projectNames := func(t *testing.T, exportDir string) (succeeded, skipped []string) {
		t.Helper()
		runDir := findPredictiveRunDir(t, exportDir)
		mig, err := summary.CollectSummary(runDir, exportDir)
		if err != nil {
			t.Fatalf("CollectSummary: %v", err)
		}
		projects := findSection(mig, "Projects")
		if projects == nil {
			t.Fatal("Projects section missing")
		}
		for _, it := range projects.Succeeded {
			succeeded = append(succeeded, it.Name)
		}
		for _, it := range projects.Skipped {
			skipped = append(skipped, it.Name+": "+it.Detail)
		}
		return succeeded, skipped
	}

	t.Run("without_default_everything_is_skipped", func(t *testing.T) {
		exportDir := setupUnmappedFixture(t)
		if _, err := GeneratePredictiveReport(exportDir); err != nil {
			t.Fatalf("GeneratePredictiveReport: %v", err)
		}
		succeeded, skipped := projectNames(t, exportDir)
		if len(succeeded) != 0 {
			t.Errorf("expected no migrated projects without a default org, got %v", succeeded)
		}
		if len(skipped) == 0 {
			t.Error("expected the projects to be reported as skipped")
		}
	})

	t.Run("with_default_everything_migrates", func(t *testing.T) {
		exportDir := setupUnmappedFixture(t)
		if _, err := GeneratePredictiveReport(exportDir, "my-org"); err != nil {
			t.Fatalf("GeneratePredictiveReport: %v", err)
		}
		succeeded, skipped := projectNames(t, exportDir)
		for _, want := range []string{"App", "Other App"} {
			found := false
			for _, got := range succeeded {
				if got == want {
					found = true
				}
			}
			if !found {
				t.Errorf("expected %q in Projects.Succeeded, got %v (skipped: %v)", want, succeeded, skipped)
			}
		}
		for _, s := range skipped {
			if strings.Contains(s, "Organization skipped") {
				t.Errorf("no project should be org-skipped once a default org is applied, got %q", s)
			}
		}
	})
}

// The default is optional: omitting it entirely must behave exactly as
// before, so every existing caller and the mapped-CSV path are safe.
func TestGeneratePredictiveReport_OmittedDefaultOrgUnchanged(t *testing.T) {
	exportDir := setupPredictiveFixture(t)

	if _, err := GeneratePredictiveReport(exportDir); err != nil {
		t.Fatalf("GeneratePredictiveReport: %v", err)
	}
	for _, got := range readOrgKeys(t, exportDir) {
		if got != "target-org" {
			t.Errorf("sonarcloud_org_key: got %q, want target-org", got)
		}
	}
}

// The normal unified-config flow: `structure --config` already stamped
// the very same organization on every row, so predictive-report has
// nothing to do and must not warn about "ignoring" a value that is
// already in force.
func TestApplyDefaultOrg_MappingAlreadyEqualsDefaultIsSilent(t *testing.T) {
	exportDir := setupPredictiveFixture(t) // maps default → target-org
	var buf bytes.Buffer

	if err := applyDefaultOrg(exportDir, "target-org", testLogger(&buf)); err != nil {
		t.Fatalf("applyDefaultOrg: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log output when the mapping already equals the default, got:\n%s", buf.String())
	}
}

// A partially mapped CSV still wins — migrate leaves the blank rows
// blank too — but the operator is warned, because those rows really
// will be skipped while the default would have covered them.
func TestApplyDefaultOrg_PartiallyMappedWarnsAndIsLeftAlone(t *testing.T) {
	exportDir := setupUnmappedFixture(t)
	writeFile(t, exportDir, "organizations.csv",
		"sonarqube_org_key,sonarcloud_org_key,server_url\n"+
			"default,target-org,"+testServerURL+"\n"+
			"other,,"+testServerURL+"\n")
	var buf bytes.Buffer

	if err := applyDefaultOrg(exportDir, "target-org", testLogger(&buf)); err != nil {
		t.Fatalf("applyDefaultOrg: %v", err)
	}
	if got := readOrgKeys(t, exportDir); got[0] != "target-org" || got[1] != "" {
		t.Errorf("CSV should be untouched, got %v", got)
	}
	if !strings.Contains(buf.String(), "ignored") {
		t.Errorf("expected a WARN that the default organization is ignored, got:\n%s", buf.String())
	}
}
