// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"sort"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/report"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const pathProjectKey = "$.projectKey"

// LOC tier thresholds.
var tiers = []struct {
	name  string
	minLOC int
}{
	{"xl", 500000}, {"l", 100000}, {"m", 10000}, {"s", 1000}, {"xs", 0},
}

// ProcessProjectDetails extracts project metadata from getProjectDetails, and
// each project's LOC from getUsage or, failing that, getProjectMeasures.
func ProcessProjectDetails(dir string, mapping structure.ExtractMapping, idMap ServerIDMapping) Projects {
	projects := make(Projects)
	for _, item := range readData(dir, mapping, "getProjectDetails") {
		sid := serverID(idMap, item.ServerURL)
		projectKey := report.ExtractString(item.Data, "$.key")
		if projectKey == "" {
			continue
		}
		ensureProject(projects, sid, projectKey)
		profiles := extractActiveProfiles(item.Data)
		projects[sid][projectKey] = map[string]any{
			"server_id":      sid,
			"name":           report.ExtractString(item.Data, "$.name"),
			"key":            projectKey,
			"main_branch":    report.ExtractString(item.Data, "$.branch"),
			"profiles":       extractProfileKeys(profiles),
			"languages":      extractLanguages(profiles),
			"quality_gate":   report.ExtractString(item.Data, "$.qualityGate.name"),
			"binding":        report.ExtractString(item.Data, "$.binding.key"),
			"loc":            0,
			"tier":           "unknown",
			"rules":          0,
			"template_rules": 0,
			"plugin_rules":   0,
		}
	}
	processProjectUsage(dir, mapping, idMap, projects)
	return projects
}

func extractActiveProfiles(data map[string]any) []any {
	raw := report.ExtractPathValue(data, "qualityProfiles", nil)
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var active []any
	for _, p := range arr {
		if m, ok := p.(map[string]any); ok {
			if !report.ExtractBool(m, "$.deleted") {
				active = append(active, m)
			}
		}
	}
	return active
}

func extractProfileKeys(profiles []any) []string {
	var keys []string
	for _, p := range profiles {
		if m, ok := p.(map[string]any); ok {
			if k := report.ExtractString(m, "key"); k != "" {
				keys = append(keys, k)
			}
		}
	}
	return keys
}

func extractLanguages(profiles []any) []string {
	langs := make(map[string]bool)
	for _, p := range profiles {
		if m, ok := p.(map[string]any); ok {
			if lang := report.ExtractString(m, "$.language"); lang != "" {
				langs[lang] = true
			}
		}
	}
	var result []string
	for lang := range langs {
		result = append(result, lang)
	}
	sort.Strings(result)
	return result
}

func ensureProject(projects Projects, sid, key string) {
	if projects[sid] == nil {
		projects[sid] = make(map[string]map[string]any)
	}
}

// processProjectUsage enriches projects with LOC data and tier assignment.
// LOC comes from getUsage (api/projects/license_usage). A project it does
// not cover, which is every project when extract skipped getUsage, falls
// back to its main branch's ncloc measure from getProjectMeasures. That
// can read lower than license usage, which counts a project's largest
// branch. A project with neither keeps loc 0 and tier "unknown".
func processProjectUsage(dir string, mapping structure.ExtractMapping, idMap ServerIDMapping, projects Projects) {
	sized := make(map[string]map[string]bool)
	for _, item := range readData(dir, mapping, "getUsage") {
		sid := serverID(idMap, item.ServerURL)
		projectKey := report.ExtractString(item.Data, pathProjectKey)
		if projects[sid] == nil || projects[sid][projectKey] == nil {
			continue
		}
		setProjectLOC(projects[sid][projectKey], report.ExtractInt(item.Data, "$.linesOfCode", 0))
		if sized[sid] == nil {
			sized[sid] = make(map[string]bool)
		}
		sized[sid][projectKey] = true
	}
	for _, item := range readData(dir, mapping, "getProjectMeasures") {
		if report.ExtractString(item.Data, "$.metric") != "ncloc" {
			continue
		}
		sid := serverID(idMap, item.ServerURL)
		projectKey := report.ExtractString(item.Data, pathProjectKey)
		if projects[sid] == nil || projects[sid][projectKey] == nil || sized[sid][projectKey] {
			continue
		}
		setProjectLOC(projects[sid][projectKey], report.ExtractInt(item.Data, "$.value", 0))
	}
}

func setProjectLOC(project map[string]any, loc int) {
	project["loc"] = loc
	project["tier"] = assignTier(loc)
}

func assignTier(loc int) string {
	for _, t := range tiers {
		if loc >= t.minLOC {
			return t.name
		}
	}
	return "unknown"
}

// ProcessProjectBranches returns a set of project keys with multiple branches per server.
func ProcessProjectBranches(dir string, mapping structure.ExtractMapping, idMap ServerIDMapping) map[string]map[string]bool {
	branches := make(map[string]map[string]map[string]bool) // sid → projectKey → branch names
	for _, item := range readData(dir, mapping, "getBranches") {
		sid := serverID(idMap, item.ServerURL)
		excluded := report.ExtractBool(item.Data, "$.excludedFromPurge")
		projectKey := report.ExtractString(item.Data, pathProjectKey)
		if !excluded || projectKey == "" {
			continue
		}
		if branches[sid] == nil {
			branches[sid] = make(map[string]map[string]bool)
		}
		if branches[sid][projectKey] == nil {
			branches[sid][projectKey] = make(map[string]bool)
		}
		name := report.ExtractString(item.Data, "$.name")
		branches[sid][projectKey][name] = true
	}
	// Only return projects with >1 branch.
	result := make(map[string]map[string]bool)
	for sid, serverBranches := range branches {
		result[sid] = make(map[string]bool)
		for projectKey, branchSet := range serverBranches {
			if len(branchSet) > 1 {
				result[sid][projectKey] = true
			}
		}
	}
	return result
}

// ProcessProjectPullRequests returns a set of project keys with PR analysis per server.
func ProcessProjectPullRequests(dir string, mapping structure.ExtractMapping, idMap ServerIDMapping) map[string]map[string]bool {
	prProjects := make(map[string]map[string]bool)
	for _, item := range readData(dir, mapping, "getProjectPullRequests") {
		sid := serverID(idMap, item.ServerURL)
		projectKey := report.ExtractString(item.Data, pathProjectKey)
		analysisDate := report.ExtractString(item.Data, "$.analysisDate")
		if projectKey == "" || analysisDate == "" {
			continue
		}
		if _, ok := parseSQDate(analysisDate); !ok {
			continue
		}
		if prProjects[sid] == nil {
			prProjects[sid] = make(map[string]bool)
		}
		prProjects[sid][projectKey] = true
	}
	return prProjects
}

// GenerateProjectMetricsMarkdown generates the Project Metrics markdown table.
func GenerateProjectMetricsMarkdown(projects Projects, projectScans map[string]map[string]map[string]map[string]any) string {
	var rows []map[string]any
	for sid, serverProjects := range projects {
		for _, project := range serverProjects {
			row := buildProjectRow(project, sid, projectScans)
			rows = append(rows, row)
		}
	}
	return report.GenerateSection(
		[]report.Column{
			{Header: "Server ID", Key: "server_id"}, {Header: "Project Name", Key: "name"}, {Header: "Total Rules", Key: "rules"},
			{Header: "Template Rules", Key: "template_rules"}, {Header: "Plugin Rules", Key: "plugin_rules"},
			{Header: "Most Recent Scan", Key: "most_recent_scan"},
		},
		rows,
		report.WithTitle("Project Metrics", 2),
	)
}

func buildProjectRow(project map[string]any, sid string, projectScans map[string]map[string]map[string]map[string]any) map[string]any {
	row := make(map[string]any, len(project)+1)
	for k, v := range project {
		row[k] = v
	}
	projectKey, _ := project["key"].(string)
	scanDate := findMostRecentScan(projectKey, sid, projectScans)
	if scanDate != nil {
		row["most_recent_scan"] = scanDate.Format("2006-01-02")
	} else {
		row["most_recent_scan"] = ""
	}
	return row
}

func findMostRecentScan(projectKey, sid string, projectScans map[string]map[string]map[string]map[string]any) *time.Time {
	if projectScans == nil || projectScans[sid] == nil {
		return nil
	}
	var most *time.Time
	for _, ciProjects := range projectScans[sid] {
		scan, ok := ciProjects[projectKey]
		if !ok {
			continue
		}
		if lastScan, ok := scan["last_scan"].(time.Time); ok {
			if most == nil || lastScan.After(*most) {
				most = &lastScan
			}
		}
	}
	return most
}
