// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeSyncTargetCSVs writes minimal projects.csv / organizations.csv
// fixtures into dir — only the columns resolveSyncTargets actually reads.
func writeSyncTargetCSVs(t *testing.T, dir, projectsCSV, orgsCSV string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "projects.csv"), []byte(projectsCSV), 0o644); err != nil {
		t.Fatalf("write projects.csv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "organizations.csv"), []byte(orgsCSV), 0o644); err != nil {
		t.Fatalf("write organizations.csv: %v", err)
	}
}

func TestResolveSyncTargets(t *testing.T) {
	orgsCSV := "sonarqube_org_key,sonarcloud_org_key\n" +
		"src-org,my-cloud-org\n" +
		"unmapped-org,\n"

	tests := []struct {
		name        string
		projectsCSV string
		pattern     string
		projectKeys []string
		want        []syncTarget
	}{
		{
			name: "renders cloud key from default pattern",
			projectsCSV: "key,server_url,sonarqube_org_key\n" +
				"proj-a,https://sqs.example.com,src-org\n",
			pattern: DefaultProjectKeyPattern,
			want: []syncTarget{
				{Key: "proj-a", ServerURL: "https://sqs.example.com", CloudProjectKey: "my-cloud-org_proj-a", OrgKey: "my-cloud-org"},
			},
		},
		{
			name: "custom pattern",
			projectsCSV: "key,server_url,sonarqube_org_key\n" +
				"proj-a,https://sqs.example.com,src-org\n",
			pattern: "acme_<ORIGINAL_PROJECT_KEY>",
			want: []syncTarget{
				{Key: "proj-a", ServerURL: "https://sqs.example.com", CloudProjectKey: "acme_proj-a", OrgKey: "my-cloud-org"},
			},
		},
		{
			name: "unmapped org is skipped",
			projectsCSV: "key,server_url,sonarqube_org_key\n" +
				"proj-a,https://sqs.example.com,src-org\n" +
				"proj-b,https://sqs.example.com,unmapped-org\n",
			pattern: DefaultProjectKeyPattern,
			want: []syncTarget{
				{Key: "proj-a", ServerURL: "https://sqs.example.com", CloudProjectKey: "my-cloud-org_proj-a", OrgKey: "my-cloud-org"},
			},
		},
		{
			name: "unknown org key (not in organizations.csv) is skipped",
			projectsCSV: "key,server_url,sonarqube_org_key\n" +
				"proj-a,https://sqs.example.com,src-org\n" +
				"proj-c,https://sqs.example.com,never-heard-of-it\n",
			pattern: DefaultProjectKeyPattern,
			want: []syncTarget{
				{Key: "proj-a", ServerURL: "https://sqs.example.com", CloudProjectKey: "my-cloud-org_proj-a", OrgKey: "my-cloud-org"},
			},
		},
		{
			name: "projectKeys filter narrows to the requested project",
			projectsCSV: "key,server_url,sonarqube_org_key\n" +
				"proj-a,https://sqs.example.com,src-org\n" +
				"proj-b,https://sqs.example.com,src-org\n",
			pattern:     DefaultProjectKeyPattern,
			projectKeys: []string{"proj-b"},
			want: []syncTarget{
				{Key: "proj-b", ServerURL: "https://sqs.example.com", CloudProjectKey: "my-cloud-org_proj-b", OrgKey: "my-cloud-org"},
			},
		},
		{
			name:        "empty projects.csv — no targets",
			projectsCSV: "key,server_url,sonarqube_org_key\n",
			pattern:     DefaultProjectKeyPattern,
			want:        nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSyncTargetCSVs(t, dir, tc.projectsCSV, orgsCSV)

			got, err := resolveSyncTargets(dir, tc.pattern, tc.projectKeys)
			if err != nil {
				t.Fatalf("resolveSyncTargets: %v", err)
			}
			sort.Slice(got, func(i, j int) bool { return got[i].Key < got[j].Key })
			if len(got) != len(tc.want) {
				t.Fatalf("got %d targets, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("target[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
