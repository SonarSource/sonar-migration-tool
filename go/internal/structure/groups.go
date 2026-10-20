// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"encoding/json"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// MapGroups aggregates groups from 4 sources: default groups, project groups,
// template groups, and profile groups.
func MapGroups(projectOrgMapping map[string]string, mapping ExtractMapping, profiles []Profile, templates []Template, directory string) []Group {
	results := make(map[string]Group)
	addDefaultGroups(results, projectOrgMapping, mapping, directory)
	addProjectGroups(results, projectOrgMapping, mapping, directory)
	addTemplateGroups(results, templates, mapping, directory)
	addProfileGroups(results, profiles, mapping, directory)

	list := make([]Group, 0, len(results))
	for _, g := range results {
		list = append(list, g)
	}
	return list
}

func addGroupResult(results map[string]Group, orgKey, name, serverURL, description string) {
	uniqueKey := orgKey + name
	results[uniqueKey] = Group{
		Name:            name,
		ServerURL:       serverURL,
		Description:     description,
		SonarQubeOrgKey: orgKey,
	}
}

// addDefaultGroups adds groups with permissions to all orgs (excluding "Anyone").
func addDefaultGroups(results map[string]Group, projectOrgMapping map[string]string, mapping ExtractMapping, directory string) {
	orgKeys := uniqueOrgKeys(projectOrgMapping)
	items, _ := ReadExtractData(directory, mapping, "getGroups")

	for _, item := range items {
		var g map[string]any
		json.Unmarshal(item.Data, &g)
		groupID := getString(g, "id")
		name := getString(g, "name")
		description := getString(g, "description")

		// Skip "Anyone" group and groups without permissions.
		if groupID == "Anyone" || name == "Anyone" {
			continue
		}
		permissions := g["permissions"]
		if permissions == nil {
			continue
		}
		// Check if permissions is non-empty.
		if arr, ok := permissions.([]any); ok && len(arr) == 0 {
			continue
		}

		for _, orgKey := range orgKeys {
			addGroupResult(results, orgKey, name, item.ServerURL, description)
		}
	}
}

// addProjectGroups adds groups from project-level group permissions.
func addProjectGroups(results map[string]Group, projectOrgMapping map[string]string, mapping ExtractMapping, directory string) {
	items, _ := ReadExtractData(directory, mapping, "getProjectGroupsPermissions")
	for _, item := range items {
		project := common.ExtractField(item.Data, "project")
		orgKey, ok := projectOrgMapping[item.ServerURL+project]
		if !ok {
			continue
		}
		name := common.ExtractField(item.Data, "name")
		var g map[string]any
		json.Unmarshal(item.Data, &g)
		description := getString(g, "description")
		addGroupResult(results, orgKey, name, item.ServerURL, description)
	}
}

// addTemplateGroups adds groups from permission template assignments.
// A template can be mapped to several organizations, so each group is added
// to every organization its template maps to (issue #648).
func addTemplateGroups(results map[string]Group, templates []Template, mapping ExtractMapping, directory string) {
	// Build template → set of org keys mapping.
	templateOrgs := make(map[string]map[string]bool)
	for _, t := range templates {
		k := t.ServerURL + t.SourceTemplateKey
		if templateOrgs[k] == nil {
			templateOrgs[k] = make(map[string]bool)
		}
		templateOrgs[k][t.SonarQubeOrgKey] = true
	}

	for _, key := range []string{"getTemplateGroupsScanners", "getTemplateGroupsViewers"} {
		items, _ := ReadExtractData(directory, mapping, key)
		for _, item := range items {
			templateID := common.ExtractField(item.Data, "templateId")
			orgs, ok := templateOrgs[item.ServerURL+templateID]
			if !ok {
				continue
			}
			name := common.ExtractField(item.Data, "name")
			var g map[string]any
			json.Unmarshal(item.Data, &g)
			description := getString(g, "description")
			for orgKey := range orgs {
				addGroupResult(results, orgKey, name, item.ServerURL, description)
			}
		}
	}
}

// addProfileGroups adds groups from quality profile group assignments.
func addProfileGroups(results map[string]Group, profiles []Profile, mapping ExtractMapping, directory string) {
	// Build profile → set of org keys mapping.
	profileOrgs := make(map[string]map[string]bool)
	for _, p := range profiles {
		if profileOrgs[p.SourceProfileKey] == nil {
			profileOrgs[p.SourceProfileKey] = make(map[string]bool)
		}
		profileOrgs[p.SourceProfileKey][p.SonarQubeOrgKey] = true
	}

	items, _ := ReadExtractData(directory, mapping, "getProfileGroups")
	for _, item := range items {
		profileKey := common.ExtractField(item.Data, "profileKey")
		orgs, ok := profileOrgs[profileKey]
		if !ok {
			continue
		}
		name := common.ExtractField(item.Data, "name")
		var g map[string]any
		json.Unmarshal(item.Data, &g)
		description := getString(g, "description")
		for orgKey := range orgs {
			addGroupResult(results, orgKey, name, item.ServerURL, description)
		}
	}
}
