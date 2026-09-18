// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const testServerURL = "https://sq.test/"

// setupTestMigrateDir creates a test directory with CSV mappings for testing
// setup tasks (generateMappings).
func setupTestMigrateDir(t *testing.T) (string, *Executor) {
	t.Helper()
	dir := t.TempDir()

	// Write test CSVs.
	orgs := []structure.Organization{
		{SonarQubeOrgKey: "org1", SonarCloudOrgKey: "cloud-org1", ServerURL: testServerURL,
			ALM: "github", URL: "https://api.github.com", IsCloud: true, ProjectCount: 1},
	}
	structure.ExportCSV(dir, "organizations", orgs)

	projects := []structure.Project{
		{Key: "proj1", Name: "Project 1", ServerURL: testServerURL, SonarQubeOrgKey: "org1",
			MainBranch: "main", NewCodeDefinitionType: "days", NewCodeDefinitionValue: 30},
	}
	structure.ExportCSV(dir, "projects", projects)

	profiles := []structure.Profile{
		{UniqueKey: "org1prof1", Name: "Custom", Language: "java", ServerURL: testServerURL,
			SourceProfileKey: "prof1", SonarQubeOrgKey: "org1"},
	}
	structure.ExportCSV(dir, "profiles", profiles)

	gates := []structure.Gate{
		{Name: "Custom Gate", ServerURL: testServerURL, SourceGateKey: "Custom Gate",
			IsDefault: true, SonarQubeOrgKey: "org1"},
	}
	structure.ExportCSV(dir, "gates", gates)

	groups := []structure.Group{
		{Name: "sonar-users", ServerURL: testServerURL, SonarQubeOrgKey: "org1"},
	}
	structure.ExportCSV(dir, "groups", groups)

	templates := []structure.Template{
		{UniqueKey: "org1tpl1", SourceTemplateKey: "tpl1", Name: "Default",
			ServerURL: testServerURL, IsDefault: true, SonarQubeOrgKey: "org1"},
	}
	structure.ExportCSV(dir, "templates", templates)

	structure.ExportCSV(dir, "portfolios", []structure.Portfolio{})

	// Create run directory.
	runDir := filepath.Join(dir, "run-01")
	os.MkdirAll(runDir, 0o755)

	store := common.NewDataStore(runDir)
	e := &Executor{
		Store:              store,
		ExportDir:          dir,
		ConcurrencyLimiter: NewFixedConcurrencyLimiter(5),
	}
	return dir, e
}

func TestSetupTasks(t *testing.T) {
	_, e := setupTestMigrateDir(t)

	tasks := setupTasks()
	for _, task := range tasks {
		t.Run(task.Name, func(t *testing.T) {
			err := task.Run(context.Background(), e)
			if err != nil {
				t.Fatalf("%s failed: %v", task.Name, err)
			}

			// Verify output was written.
			items, err := e.Store.ReadAll(task.Name)
			if err != nil {
				t.Fatal(err)
			}
			// portfolios may be empty.
			if task.Name == "generatePortfolioMappings" {
				return
			}
			if len(items) == 0 {
				t.Errorf("%s produced no output", task.Name)
			}
		})
	}
}

func TestSetupTaskProjectMappingsContent(t *testing.T) {
	_, e := setupTestMigrateDir(t)

	task := setupTasks()[0] // generateProjectMappings
	if err := task.Run(context.Background(), e); err != nil {
		t.Fatal(err)
	}

	items, _ := e.Store.ReadAll("generateProjectMappings")
	if len(items) != 1 {
		t.Fatalf("expected 1 project, got %d", len(items))
	}

	var proj map[string]any
	json.Unmarshal(items[0], &proj)
	if proj["key"] != "proj1" {
		t.Errorf("expected key=proj1, got %v", proj["key"])
	}
}

func TestSetupTaskOrgMappingsContent(t *testing.T) {
	_, e := setupTestMigrateDir(t)

	// Find generateOrganizationMappings.
	for _, task := range setupTasks() {
		if task.Name != "generateOrganizationMappings" {
			continue
		}
		if err := task.Run(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		items, _ := e.Store.ReadAll("generateOrganizationMappings")
		if len(items) != 1 {
			t.Fatalf("expected 1 org, got %d", len(items))
		}
		var org map[string]any
		json.Unmarshal(items[0], &org)
		if org["sonarcloud_org_key"] != "cloud-org1" {
			t.Errorf("expected cloud-org1, got %v", org["sonarcloud_org_key"])
		}
	}
}
