// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// Issue #612: reset has to offer the organizations a per-project override
// dispatched projects into, or a migration that used the override cannot
// be undone — reset would list only the organizations.csv ones and leave
// the dispatched projects behind.
func TestLoadResetTargetOrgsIncludesProjectOverrides(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "organizations.csv",
		"sonarqube_org_key,sonarcloud_org_key\n"+
			"https://sq.example.invalid,default-org\n"+
			"gitlab.com/acme,gitlab-org\n"+
			"unmapped,\n"+
			"excluded,SKIPPED\n")
	writeFile(t, dir, structure.ProjectsCSVFileName,
		"key,sonarcloud_org_key,server_url,sonarqube_org_key,alm,is_cloud_binding\n"+
			"p1,extra-org,https://sq.example.invalid,https://sq.example.invalid,,false\n"+
			"p2,extra-org,https://sq.example.invalid,https://sq.example.invalid,,false\n"+
			"p3,,https://sq.example.invalid,https://sq.example.invalid,,false\n"+
			"p4,refused-org,https://sq.example.invalid,gitlab.com/acme,gitlab,true\n")

	got, err := loadResetTargetOrgs(dir)
	if err != nil {
		t.Fatalf("loadResetTargetOrgs: %v", err)
	}
	// Sorted, no empty, no SKIPPED, and no organization that only ever
	// appeared as a refused override.
	want := "default-org,extra-org,gitlab-org"
	if strings.Join(got, ",") != want {
		t.Errorf("loadResetTargetOrgs = %v, want [%s]", got, want)
	}
}

// A missing projects.csv must not break reset: a user can reset from an
// export directory that only ever had organizations.csv.
func TestLoadResetTargetOrgsWithoutProjectsCSV(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "organizations.csv",
		"sonarqube_org_key,sonarcloud_org_key\nhttps://sq.example.invalid,default-org\n")

	got, err := loadResetTargetOrgs(dir)
	if err != nil {
		t.Fatalf("loadResetTargetOrgs: %v", err)
	}
	if strings.Join(got, ",") != "default-org" {
		t.Errorf("loadResetTargetOrgs = %v, want [default-org]", got)
	}
}
