// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import "strings"

const (
	// ProjectsCSVFileName is the mapping file that carries one row per
	// source project.
	ProjectsCSVFileName = "projects.csv"

	// ProjectOrgColumn is the projects.csv column holding a per-project
	// SonarQube Cloud organization override (issue #612). Empty by
	// default; `structure` never fills it in.
	ProjectOrgColumn = "sonarcloud_org_key"
)

// ResolveProjectOrg picks the SonarQube Cloud organization one projects.csv
// row migrates into, given the organization organizations.csv maps its
// source org to.
//
// A filled sonarcloud_org_key cell on the row wins. organizations.csv can
// only express one organization per DevOps binding, which funnels every
// project that was never bound on SonarQube Server into a single "unbound"
// organization; the per-project column lets an operator dispatch those
// projects across as many organizations as they like (issue #612). An empty
// cell — the default `structure` writes — falls back to the organizations.csv
// mapping, so the column stays inert until somebody edits it.
//
// The override is refused for a project bound to a DevOps platform that
// SonarQube Cloud can bind to as well. Such a project has to land in the
// organization carrying the matching platform binding or its own binding and
// pull-request decoration cannot be recreated, so the organizations.csv row
// derived from that binding is the only correct answer. A project with no
// binding at all, or one bound to an on-premise platform (GitHub Enterprise
// Server, self-managed GitLab, Bitbucket Server) that has no SonarQube Cloud
// counterpart, carries no such constraint and stays overridable.
//
// refusedOverride returns the override that was turned down, so the caller
// can log it. It is empty whenever nothing was refused.
func ResolveProjectOrg(row map[string]any, mappedOrg string) (org string, refusedOverride string) {
	override := strings.TrimSpace(csvCellString(row[ProjectOrgColumn]))
	if override == "" {
		return mappedOrg, ""
	}
	if HasCloudBinding(row) {
		return mappedOrg, override
	}
	return override, ""
}

// HasCloudBinding reports whether a projects.csv row describes a project
// bound to a DevOps platform SonarQube Cloud can bind to as well.
//
// It reads the is_cloud_binding column MapProjectStructure wrote, which is
// the same signal the project-binding migration itself keys on — so the
// override guard and the binding migration can never disagree about which
// projects are bound.
func HasCloudBinding(row map[string]any) bool {
	switch v := row["is_cloud_binding"].(type) {
	case bool:
		return v
	case string:
		// LoadCSV coerces "true"/"false" to bool, but a hand-edited
		// file can carry anything.
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

// csvCellString reads a CSV cell as a string, tolerating the typed values
// LoadCSV coerces non-identifier columns into.
func csvCellString(v any) string {
	s, _ := v.(string)
	return s
}
