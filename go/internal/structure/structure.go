package structure

import "fmt"

// RunStructure groups projects into organizations based on DevOps Bindings
// and Server URLs. Outputs organizations.csv and projects.csv.
// When defaultOrgKey is non-empty and only one SonarCloud org is configured,
// it pre-populates the sonarcloud_org_key column in organizations.csv.
func RunStructure(exportDirectory string, defaultOrgKey ...string) error {
	mapping, err := GetUniqueExtracts(exportDirectory)
	if err != nil {
		return fmt.Errorf("scanning extracts: %w", err)
	}
	if len(mapping) == 0 {
		return fmt.Errorf("no extracts found in %s", exportDirectory)
	}

	bindings, projects := MapProjectStructure(exportDirectory, mapping)
	organizations := MapOrganizationStructure(bindings, defaultOrgKey...)

	if err := ExportCSV(exportDirectory, "organizations", organizations); err != nil {
		return fmt.Errorf("exporting organizations.csv: %w", err)
	}
	if err := ExportCSV(exportDirectory, "projects", projects); err != nil {
		return fmt.Errorf("exporting projects.csv: %w", err)
	}

	fmt.Printf("Structure complete: %d organizations, %d projects\n", len(organizations), len(projects))
	return nil
}

// RunMappings maps groups, permission templates, quality profiles, quality gates,
// and portfolios to relevant organizations. Outputs CSVs for each entity type.
func RunMappings(exportDirectory string) error {
	mapping, err := GetUniqueExtracts(exportDirectory)
	if err != nil {
		return fmt.Errorf("scanning extracts: %w", err)
	}
	if len(mapping) == 0 {
		return fmt.Errorf("no extracts found in %s", exportDirectory)
	}

	// Load projects from CSV (output of structure command).
	projectRows, err := LoadCSV(exportDirectory, "projects.csv")
	if err != nil {
		return fmt.Errorf("loading projects.csv: %w", err)
	}
	if len(projectRows) == 0 {
		return fmt.Errorf("no projects found — run 'structure' command first")
	}

	// Build project → org mapping.
	projectOrgMapping := make(map[string]string, len(projectRows))
	for _, p := range projectRows {
		serverURL, _ := p["server_url"].(string)
		key, _ := p["key"].(string)
		orgKey, _ := p["sonarqube_org_key"].(string)
		if serverURL != "" && key != "" && orgKey != "" {
			projectOrgMapping[serverURL+key] = orgKey
		}
	}

	templates := MapTemplates(projectOrgMapping, mapping, exportDirectory)
	profiles := MapProfiles(projectOrgMapping, mapping, exportDirectory)
	gates := MapGates(projectOrgMapping, mapping, exportDirectory)
	portfolios := MapPortfolios(exportDirectory, mapping)
	groups := MapGroups(projectOrgMapping, mapping, profiles, templates, exportDirectory)

	exports := map[string]any{
		"templates":  templates,
		"profiles":   profiles,
		"gates":      gates,
		"portfolios": portfolios,
		"groups":     groups,
	}
	for name, data := range exports {
		if err := ExportCSV(exportDirectory, name, data); err != nil {
			return fmt.Errorf("exporting %s.csv: %w", name, err)
		}
	}

	fmt.Printf("Mappings complete: %d templates, %d profiles, %d gates, %d portfolios, %d groups\n",
		len(templates), len(profiles), len(gates), len(portfolios), len(groups))
	return nil
}
