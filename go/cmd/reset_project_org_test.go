// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"io"
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

// #612: organizations now come from two files, so both "nothing matched"
// errors have to name both. Pointing an operator at organizations.csv
// alone sends them to a file that may legitimately have no mapping while
// projects.csv carries every override.
func TestConfirmResetOrgsEmptyErrorsNameBothFiles(t *testing.T) {
	cases := []struct {
		name       string
		orgCSV     string
		projectCSV string
		orgPattern string
	}{
		{
			name:       "no organization matches the narrowing pattern",
			orgCSV:     "sonarqube_org_key,sonarcloud_org_key\nsrv,default-org\n",
			projectCSV: "key,sonarcloud_org_key,sonarqube_org_key,alm,is_cloud_binding\np1,extra-org,srv,,false\n",
			orgPattern: "nothing-matches-this",
		},
		{
			name:       "no organization mapped anywhere",
			orgCSV:     "sonarqube_org_key,sonarcloud_org_key\nsrv,\n",
			projectCSV: "key,sonarcloud_org_key,sonarqube_org_key,alm,is_cloud_binding\np1,,srv,,false\n",
			orgPattern: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "organizations.csv", tc.orgCSV)
			writeFile(t, dir, structure.ProjectsCSVFileName, tc.projectCSV)

			_, err := confirmResetOrgs(dir, false, tc.orgPattern, nil, strings.NewReader(""), io.Discard)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			for _, want := range []string{"organizations.csv", structure.ProjectsCSVFileName} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not name %q: %q", want, err.Error())
				}
			}
		})
	}
}

// The narrowing flag is --organization. A message naming a flag that does
// not exist sends the operator looking for it in --help.
func TestConfirmResetOrgsNamesTheRealNarrowingFlag(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "organizations.csv", "sonarqube_org_key,sonarcloud_org_key\nsrv,default-org\n")

	_, err := confirmResetOrgs(dir, false, "nothing-matches-this", nil, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "--organization ") {
		t.Errorf("error does not name --organization: %q", err.Error())
	}
	if strings.Contains(err.Error(), "--reset_organization") {
		t.Errorf("error names the non-existent --reset_organization flag: %q", err.Error())
	}
}
