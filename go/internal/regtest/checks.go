package regtest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// allChecks returns every registered check function.
func allChecks() []checkFn {
	return []checkFn{
		{"Projects", "Project existence and count", checkProjectCount},
		{"Projects", "Project identity (name, visibility)", checkProjectIdentity},
		{"Issues", "Total issue count (per project)", checkIssueTotals},
		{"Issues", "OPEN status count", checkIssueDistribution("statuses", "OPEN")},
		{"Issues", "CONFIRMED status count", checkIssueDistribution("statuses", "CONFIRMED")},
		{"Issues", "REOPENED status count", checkIssueDistribution("statuses", "REOPENED")},
		{"Issues", "RESOLVED status count", checkIssueDistribution("statuses", "RESOLVED")},
		{"Issues", "CLOSED status count", checkIssueDistribution("statuses", "CLOSED")},
		{"Issues", "BLOCKER severity count", checkIssueDistribution("severities", "BLOCKER")},
		{"Issues", "CRITICAL severity count", checkIssueDistribution("severities", "CRITICAL")},
		{"Issues", "MAJOR severity count", checkIssueDistribution("severities", "MAJOR")},
		{"Issues", "MINOR severity count", checkIssueDistribution("severities", "MINOR")},
		{"Issues", "INFO severity count", checkIssueDistribution("severities", "INFO")},
		{"Issues", "BUG type count", checkIssueDistribution("types", "BUG")},
		{"Issues", "VULNERABILITY type count", checkIssueDistribution("types", "VULNERABILITY")},
		{"Issues", "CODE_SMELL type count", checkIssueDistribution("types", "CODE_SMELL")},
		{"Issues", "FALSE-POSITIVE resolution", checkIssueDistribution("resolutions", "FALSE-POSITIVE")},
		{"Issues", "WONTFIX resolution", checkIssueDistribution("resolutions", "WONTFIX")},
		{"Issues", "FIXED resolution", checkIssueDistribution("resolutions", "FIXED")},
		{"Hotspots", "Total hotspot count (per project)", checkHotspotTotals},
		{"Hotspots", "TO_REVIEW status", checkHotspotByStatus("TO_REVIEW")},
		{"Hotspots", "REVIEWED status", checkHotspotByStatus("REVIEWED")},
		{"Quality Profiles", "Profile count", checkProfileCount},
		{"Quality Profiles", "Active rules per profile", checkProfileRules},
		{"Quality Profiles", "Default profile per language", checkProfileDefaults},
		{"Quality Profiles", "Inheritance chains", checkProfileInheritance},
		{"Quality Gates", "Gate count", checkGateCount},
		{"Quality Gates", "Conditions per gate", checkGateConditions},
		{"Quality Gates", "Default gate", checkGateDefault},
		{"Quality Gates", "Gate-project associations", checkGateAssociations},
		{"Groups", "Group count", checkGroupCount},
		{"Groups", "Group membership", checkGroupMembership},
		{"Permission Templates", "Template count", checkTemplateCount},
		{"Permission Templates", "Template group permissions", checkTemplatePermissions},
		{"Settings", "Global settings", checkGlobalSettings},
		{"Settings", "Per-project settings", checkProjectSettings},
		{"New Code Periods", "Per-project NCD", checkNewCodePeriods},
		{"Rules", "Custom rule count", checkCustomRules},
		{"Permissions", "Project group permissions", checkProjectPermissions},
		{"ALM Bindings", "Per-project ALM binding", checkALMBindings},
		{"Portfolios", "Portfolio count", checkPortfolios},
		{"Measures", "Key metrics per project", checkMeasures},
		{"Extract Files", "NDJSON file completeness", checkExtractFiles},
	}
}

// ── Projects ──────────────────────────────────────────────────────────

func checkProjectCount(ctx context.Context, s *Suite) []CheckResult {
	sqsCount, err := queryTotal(ctx, s.sqsRaw, "api/projects/search", nil)
	if err != nil {
		return []CheckResult{makeError("Projects", "Project count", err)}
	}
	scCount, err := queryCount(ctx, s.scRaw, "api/projects/search",
		urlParams("organization", s.cfg.SCOrg), "components")
	if err != nil {
		return []CheckResult{makeError("Projects", "Project count", err)}
	}
	return []CheckResult{makeResult("Projects", "Project count", sqsCount, scCount, "Exact")}
}

func checkProjectIdentity(ctx context.Context, s *Suite) []CheckResult {
	projects, err := s.getProjects(ctx)
	if err != nil {
		return []CheckResult{makeError("Projects", "Project identity", err)}
	}
	var results []CheckResult
	for _, proj := range projects {
		sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/components/show", urlParams("component", proj))
		if err != nil {
			results = append(results, makeError("Projects", fmt.Sprintf("Identity: %s", proj), err))
			continue
		}
		scBody, err := queryJSON(ctx, s.scRaw, "api/components/show", urlParams("component", s.scProjectKey(proj)))
		if err != nil {
			results = append(results, makeError("Projects", fmt.Sprintf("Identity: %s (not found on SC)", proj), err))
			continue
		}
		var sqsComp, scComp struct {
			Component struct {
				Name       string `json:"name"`
				Visibility string `json:"visibility"`
			} `json:"component"`
		}
		json.Unmarshal(sqsBody, &sqsComp)
		json.Unmarshal(scBody, &scComp)
		results = append(results, makeResultStr("Projects",
			fmt.Sprintf("Name: %s", proj), sqsComp.Component.Name, scComp.Component.Name, "Exact"))
		results = append(results, makeResultStr("Projects",
			fmt.Sprintf("Visibility: %s", proj), sqsComp.Component.Visibility, scComp.Component.Visibility, "Exact"))
	}
	return results
}

// ── Issues ────────────────────────────────────────────────────────────

func checkIssueTotals(ctx context.Context, s *Suite) []CheckResult {
	projects, err := s.getProjects(ctx)
	if err != nil {
		return []CheckResult{makeError("Issues", "Total count", err)}
	}
	var results []CheckResult
	for _, proj := range projects {
		sqsCount, err := queryTotal(ctx, s.sqsRaw, "api/issues/search", urlParams("projectKeys", proj))
		if err != nil {
			results = append(results, makeError("Issues", fmt.Sprintf("Total: %s", proj), err))
			continue
		}
		scCount, err := queryTotal(ctx, s.scRaw, "api/issues/search", urlParams("projects", s.scProjectKey(proj)))
		if err != nil {
			results = append(results, makeError("Issues", fmt.Sprintf("Total: %s", proj), err))
			continue
		}
		results = append(results, makeResult("Issues", fmt.Sprintf("Total: %s", proj), sqsCount, scCount, "Exact"))
	}
	return results
}

func checkIssueDistribution(filterKey, filterValue string) func(ctx context.Context, s *Suite) []CheckResult {
	return func(ctx context.Context, s *Suite) []CheckResult {
		projects, err := s.getProjects(ctx)
		if err != nil {
			return []CheckResult{makeError("Issues", filterValue, err)}
		}
		var results []CheckResult
		for _, proj := range projects {
			sqsCount, err := countWithFilter(ctx, s.sqsRaw, "api/issues/search",
				urlParams("projectKeys", proj), filterKey, filterValue)
			if err != nil {
				results = append(results, makeError("Issues", fmt.Sprintf("%s: %s", filterValue, proj), err))
				continue
			}
			scCount, err := countWithFilter(ctx, s.scRaw, "api/issues/search",
				urlParams("projects", s.scProjectKey(proj)), filterKey, filterValue)
			if err != nil {
				results = append(results, makeError("Issues", fmt.Sprintf("%s: %s", filterValue, proj), err))
				continue
			}
			results = append(results, makeResult("Issues",
				fmt.Sprintf("%s: %s", filterValue, proj), sqsCount, scCount, "Exact"))
		}
		return results
	}
}

// ── Hotspots ──────────────────────────────────────────────────────────

func checkHotspotTotals(ctx context.Context, s *Suite) []CheckResult {
	projects, err := s.getProjects(ctx)
	if err != nil {
		return []CheckResult{makeError("Hotspots", "Total count", err)}
	}
	var results []CheckResult
	for _, proj := range projects {
		sqsCount, err := queryTotal(ctx, s.sqsRaw, "api/hotspots/search", urlParams("projectKey", proj))
		if err != nil {
			results = append(results, makeError("Hotspots", fmt.Sprintf("Total: %s", proj), err))
			continue
		}
		scCount, err := queryTotal(ctx, s.scRaw, "api/hotspots/search", urlParams("projectKey", s.scProjectKey(proj)))
		if err != nil {
			results = append(results, makeError("Hotspots", fmt.Sprintf("Total: %s", proj), err))
			continue
		}
		results = append(results, makeResult("Hotspots", fmt.Sprintf("Total: %s", proj), sqsCount, scCount, "Exact"))
	}
	return results
}

func checkHotspotByStatus(status string) func(ctx context.Context, s *Suite) []CheckResult {
	return func(ctx context.Context, s *Suite) []CheckResult {
		projects, err := s.getProjects(ctx)
		if err != nil {
			return []CheckResult{makeError("Hotspots", status, err)}
		}
		var results []CheckResult
		for _, proj := range projects {
			sqsCount, err := countWithFilter(ctx, s.sqsRaw, "api/hotspots/search",
				urlParams("projectKey", proj), "status", status)
			if err != nil {
				results = append(results, makeError("Hotspots", fmt.Sprintf("%s: %s", status, proj), err))
				continue
			}
			scCount, err := countWithFilter(ctx, s.scRaw, "api/hotspots/search",
				urlParams("projectKey", s.scProjectKey(proj)), "status", status)
			if err != nil {
				results = append(results, makeError("Hotspots", fmt.Sprintf("%s: %s", status, proj), err))
				continue
			}
			results = append(results, makeResult("Hotspots",
				fmt.Sprintf("%s: %s", status, proj), sqsCount, scCount, "Exact"))
		}
		return results
	}
}

// ── Quality Profiles ──────────────────────────────────────────────────

func checkProfileCount(ctx context.Context, s *Suite) []CheckResult {
	sqsCount, err := queryCount(ctx, s.sqsRaw, "api/qualityprofiles/search", nil, "profiles")
	if err != nil {
		return []CheckResult{makeError("Quality Profiles", "Profile count", err)}
	}
	scCount, err := queryCount(ctx, s.scRaw, "api/qualityprofiles/search",
		urlParams("organization", s.cfg.SCOrg), "profiles")
	if err != nil {
		return []CheckResult{makeError("Quality Profiles", "Profile count", err)}
	}
	r := makeResult("Quality Profiles", "Profile count", sqsCount, scCount, "SC includes built-ins")
	if scCount >= sqsCount {
		r.Match = true
		r.Notes = fmt.Sprintf("SC=%d includes built-ins", scCount)
	}
	return []CheckResult{r}
}

func checkProfileRules(ctx context.Context, s *Suite) []CheckResult {
	sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/qualityprofiles/search", nil)
	if err != nil {
		return []CheckResult{makeError("Quality Profiles", "Active rules", err)}
	}
	scBody, err := queryJSON(ctx, s.scRaw, "api/qualityprofiles/search",
		urlParams("organization", s.cfg.SCOrg))
	if err != nil {
		return []CheckResult{makeError("Quality Profiles", "Active rules", err)}
	}
	type profile struct {
		Name            string `json:"name"`
		Language        string `json:"language"`
		ActiveRuleCount int    `json:"activeRuleCount"`
	}
	var sqsResp, scResp struct {
		Profiles []profile `json:"profiles"`
	}
	json.Unmarshal(sqsBody, &sqsResp)
	json.Unmarshal(scBody, &scResp)

	scMap := make(map[string]int)
	for _, p := range scResp.Profiles {
		scMap[p.Name+"|"+p.Language] = p.ActiveRuleCount
	}
	var results []CheckResult
	for _, p := range sqsResp.Profiles {
		scRules, found := scMap[p.Name+"|"+p.Language]
		if !found {
			results = append(results, CheckResult{
				Category: "Quality Profiles",
				Name:     fmt.Sprintf("Rules: %s (%s)", p.Name, p.Language),
				SQSValue: strconv.Itoa(p.ActiveRuleCount),
				SCValue:  "NOT FOUND",
				Match:    false,
				Notes:    "Profile missing on SC",
			})
			continue
		}
		results = append(results, makeResult("Quality Profiles",
			fmt.Sprintf("Rules: %s (%s)", p.Name, p.Language), p.ActiveRuleCount, scRules, "Exact"))
	}
	return results
}

func checkProfileDefaults(ctx context.Context, s *Suite) []CheckResult {
	sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/qualityprofiles/search", nil)
	if err != nil {
		return []CheckResult{makeError("Quality Profiles", "Defaults", err)}
	}
	scBody, err := queryJSON(ctx, s.scRaw, "api/qualityprofiles/search",
		urlParams("organization", s.cfg.SCOrg))
	if err != nil {
		return []CheckResult{makeError("Quality Profiles", "Defaults", err)}
	}
	type profile struct {
		Name      string `json:"name"`
		Language  string `json:"language"`
		IsDefault bool   `json:"isDefault"`
	}
	var sqsResp, scResp struct{ Profiles []profile `json:"profiles"` }
	json.Unmarshal(sqsBody, &sqsResp)
	json.Unmarshal(scBody, &scResp)

	sqsDef := make(map[string]string)
	for _, p := range sqsResp.Profiles {
		if p.IsDefault {
			sqsDef[p.Language] = p.Name
		}
	}
	scDef := make(map[string]string)
	for _, p := range scResp.Profiles {
		if p.IsDefault {
			scDef[p.Language] = p.Name
		}
	}
	var results []CheckResult
	for lang, sqsName := range sqsDef {
		results = append(results, makeResultStr("Quality Profiles",
			fmt.Sprintf("Default (%s)", lang), sqsName, scDef[lang], "Exact"))
	}
	return results
}

func checkProfileInheritance(ctx context.Context, s *Suite) []CheckResult {
	sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/qualityprofiles/search", nil)
	if err != nil {
		return []CheckResult{makeError("Quality Profiles", "Inheritance", err)}
	}
	scBody, err := queryJSON(ctx, s.scRaw, "api/qualityprofiles/search",
		urlParams("organization", s.cfg.SCOrg))
	if err != nil {
		return []CheckResult{makeError("Quality Profiles", "Inheritance", err)}
	}
	type profile struct {
		ParentKey string `json:"parentKey"`
	}
	var sqsResp, scResp struct{ Profiles []profile `json:"profiles"` }
	json.Unmarshal(sqsBody, &sqsResp)
	json.Unmarshal(scBody, &scResp)

	sqsCount, scCount := 0, 0
	for _, p := range sqsResp.Profiles {
		if p.ParentKey != "" {
			sqsCount++
		}
	}
	for _, p := range scResp.Profiles {
		if p.ParentKey != "" {
			scCount++
		}
	}
	return []CheckResult{makeResult("Quality Profiles", "Profiles with inheritance", sqsCount, scCount, "Exact")}
}

// ── Quality Gates ─────────────────────────────────────────────────────

func checkGateCount(ctx context.Context, s *Suite) []CheckResult {
	sqsCount, err := queryCount(ctx, s.sqsRaw, "api/qualitygates/list", nil, "qualitygates")
	if err != nil {
		return []CheckResult{makeError("Quality Gates", "Gate count", err)}
	}
	scCount, err := queryCount(ctx, s.scRaw, "api/qualitygates/list",
		urlParams("organization", s.cfg.SCOrg), "qualitygates")
	if err != nil {
		return []CheckResult{makeError("Quality Gates", "Gate count", err)}
	}
	r := makeResult("Quality Gates", "Gate count", sqsCount, scCount, "SC includes built-ins")
	if scCount >= sqsCount {
		r.Match = true
		r.Notes = fmt.Sprintf("SC=%d includes built-ins", scCount)
	}
	return []CheckResult{r}
}

func checkGateConditions(ctx context.Context, s *Suite) []CheckResult {
	sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/qualitygates/list", nil)
	if err != nil {
		return []CheckResult{makeError("Quality Gates", "Conditions", err)}
	}
	scBody, err := queryJSON(ctx, s.scRaw, "api/qualitygates/list",
		urlParams("organization", s.cfg.SCOrg))
	if err != nil {
		return []CheckResult{makeError("Quality Gates", "Conditions", err)}
	}
	type gate struct {
		ID   json.RawMessage `json:"id"`
		Name string          `json:"name"`
	}
	var sqsResp, scResp struct{ QualityGates []gate `json:"qualitygates"` }
	json.Unmarshal(sqsBody, &sqsResp)
	json.Unmarshal(scBody, &scResp)

	scIDs := make(map[string]string)
	for _, g := range scResp.QualityGates {
		scIDs[g.Name] = string(g.ID)
	}
	var results []CheckResult
	for _, sq := range sqsResp.QualityGates {
		sqDetail, err := queryJSON(ctx, s.sqsRaw, "api/qualitygates/show", urlParams("id", string(sq.ID)))
		if err != nil {
			results = append(results, makeError("Quality Gates", fmt.Sprintf("Conditions: %s", sq.Name), err))
			continue
		}
		var sqDet struct{ Conditions []json.RawMessage `json:"conditions"` }
		json.Unmarshal(sqDetail, &sqDet)

		scID, found := scIDs[sq.Name]
		if !found {
			results = append(results, CheckResult{
				Category: "Quality Gates", Name: fmt.Sprintf("Conditions: %s", sq.Name),
				SQSValue: strconv.Itoa(len(sqDet.Conditions)), SCValue: "NOT FOUND", Match: false,
			})
			continue
		}
		scDetail, err := queryJSON(ctx, s.scRaw, "api/qualitygates/show",
			urlParams("id", strings.Trim(scID, "\""), "organization", s.cfg.SCOrg))
		if err != nil {
			results = append(results, makeError("Quality Gates", fmt.Sprintf("Conditions: %s", sq.Name), err))
			continue
		}
		var scDet struct{ Conditions []json.RawMessage `json:"conditions"` }
		json.Unmarshal(scDetail, &scDet)
		results = append(results, makeResult("Quality Gates",
			fmt.Sprintf("Conditions: %s", sq.Name), len(sqDet.Conditions), len(scDet.Conditions), "Exact"))
	}
	return results
}

func checkGateDefault(ctx context.Context, s *Suite) []CheckResult {
	type gate struct {
		Name      string `json:"name"`
		IsDefault bool   `json:"isDefault"`
	}
	var sqsResp, scResp struct{ QualityGates []gate `json:"qualitygates"` }
	sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/qualitygates/list", nil)
	if err != nil {
		return []CheckResult{makeError("Quality Gates", "Default gate", err)}
	}
	scBody, err := queryJSON(ctx, s.scRaw, "api/qualitygates/list", urlParams("organization", s.cfg.SCOrg))
	if err != nil {
		return []CheckResult{makeError("Quality Gates", "Default gate", err)}
	}
	json.Unmarshal(sqsBody, &sqsResp)
	json.Unmarshal(scBody, &scResp)
	sqsDef, scDef := "none", "none"
	for _, g := range sqsResp.QualityGates {
		if g.IsDefault {
			sqsDef = g.Name
		}
	}
	for _, g := range scResp.QualityGates {
		if g.IsDefault {
			scDef = g.Name
		}
	}
	return []CheckResult{makeResultStr("Quality Gates", "Default gate", sqsDef, scDef, "Exact")}
}

func checkGateAssociations(ctx context.Context, s *Suite) []CheckResult {
	projects, err := s.getProjects(ctx)
	if err != nil {
		return []CheckResult{makeError("Quality Gates", "Associations", err)}
	}
	var results []CheckResult
	for _, proj := range projects {
		sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/qualitygates/get_by_project", urlParams("project", proj))
		if err != nil {
			results = append(results, makeError("Quality Gates", fmt.Sprintf("Assoc: %s", proj), err))
			continue
		}
		scBody, err := queryJSON(ctx, s.scRaw, "api/qualitygates/get_by_project",
			urlParams("project", s.scProjectKey(proj), "organization", s.cfg.SCOrg))
		if err != nil {
			results = append(results, makeError("Quality Gates", fmt.Sprintf("Assoc: %s", proj), err))
			continue
		}
		var sqsG, scG struct {
			QualityGate struct{ Name string `json:"name"` } `json:"qualityGate"`
		}
		json.Unmarshal(sqsBody, &sqsG)
		json.Unmarshal(scBody, &scG)
		results = append(results, makeResultStr("Quality Gates",
			fmt.Sprintf("Gate: %s", proj), sqsG.QualityGate.Name, scG.QualityGate.Name, "Exact"))
	}
	return results
}

// ── Groups ────────────────────────────────────────────────────────────

func checkGroupCount(ctx context.Context, s *Suite) []CheckResult {
	sqsCount, err := queryCount(ctx, s.sqsRaw, "api/user_groups/search", urlParams("ps", "500"), "groups")
	if err != nil {
		return []CheckResult{makeError("Groups", "Group count", err)}
	}
	scCount, err := queryCount(ctx, s.scRaw, "api/user_groups/search",
		urlParams("organization", s.cfg.SCOrg, "ps", "500"), "groups")
	if err != nil {
		return []CheckResult{makeError("Groups", "Group count", err)}
	}
	return []CheckResult{makeResult("Groups", "Group count", sqsCount, scCount, "Built-in handling may differ")}
}

func checkGroupMembership(ctx context.Context, s *Suite) []CheckResult {
	sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/user_groups/search", urlParams("ps", "500"))
	if err != nil {
		return []CheckResult{makeError("Groups", "Membership", err)}
	}
	type group struct {
		Name         string `json:"name"`
		MembersCount int    `json:"membersCount"`
	}
	var resp struct{ Groups []group `json:"groups"` }
	json.Unmarshal(sqsBody, &resp)

	var results []CheckResult
	for _, g := range resp.Groups {
		if g.Name == "sonar-users" {
			results = append(results, makeSkipped("Groups", fmt.Sprintf("Members: %s", g.Name), "maps to SC Members"))
			continue
		}
		scBody, err := queryJSON(ctx, s.scRaw, "api/user_groups/search",
			urlParams("organization", s.cfg.SCOrg, "q", g.Name, "ps", "500"))
		if err != nil {
			results = append(results, makeError("Groups", fmt.Sprintf("Members: %s", g.Name), err))
			continue
		}
		var scResp struct{ Groups []group `json:"groups"` }
		json.Unmarshal(scBody, &scResp)
		scMembers := 0
		for _, sg := range scResp.Groups {
			if sg.Name == g.Name {
				scMembers = sg.MembersCount
				break
			}
		}
		results = append(results, makeResult("Groups",
			fmt.Sprintf("Members: %s", g.Name), g.MembersCount, scMembers, "Where users exist in SC"))
	}
	return results
}

// ── Permission Templates ──────────────────────────────────────────────

func checkTemplateCount(ctx context.Context, s *Suite) []CheckResult {
	sqsCount, err := queryCount(ctx, s.sqsRaw, "api/permissions/search_templates", nil, "permissionTemplates")
	if err != nil {
		return []CheckResult{makeError("Permission Templates", "Template count", err)}
	}
	scCount, err := queryCount(ctx, s.scRaw, "api/permissions/search_templates",
		urlParams("organization", s.cfg.SCOrg), "permissionTemplates")
	if err != nil {
		return []CheckResult{makeError("Permission Templates", "Template count", err)}
	}
	return []CheckResult{makeResult("Permission Templates", "Template count", sqsCount, scCount, "Exact")}
}

func checkTemplatePermissions(ctx context.Context, s *Suite) []CheckResult {
	sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/permissions/search_templates", nil)
	if err != nil {
		return []CheckResult{makeError("Permission Templates", "Permissions", err)}
	}
	type tmpl struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var resp struct{ PermissionTemplates []tmpl `json:"permissionTemplates"` }
	json.Unmarshal(sqsBody, &resp)

	perms := []string{"admin", "codeviewer", "issueadmin", "securityhotspotadmin", "scan", "user"}
	var results []CheckResult
	for _, t := range resp.PermissionTemplates {
		for _, perm := range perms {
			// SQS records the baseline group count for manual review. The
			// corresponding SC permission-template group endpoint does not
			// expose a directly comparable structure, so this is reported
			// as SKIPPED rather than unconditionally PASS.
			sqsCount, err := queryCount(ctx, s.sqsRaw, "api/permissions/template_groups",
				urlParams("templateId", t.ID, "permission", perm, "ps", "500"), "groups")
			if err != nil {
				results = append(results, makeError("Permission Templates",
					fmt.Sprintf("%s/%s", t.Name, perm), err))
				continue
			}
			r := CheckResult{
				Category: "Permission Templates",
				Name:     fmt.Sprintf("%s/%s groups", t.Name, perm),
				SQSValue: strconv.Itoa(sqsCount),
				SCValue:  "N/A",
				Match:    false,
				Notes:    "SKIPPED",
			}
			results = append(results, r)
		}
	}
	return results
}

// ── Settings ──────────────────────────────────────────────────────────

// isSubsetMatch reports whether scCount represents a valid SC-side subset of
// the SQS baseline: non-zero (proves something actually migrated) and no
// larger than the SQS baseline (a count larger than SQS would indicate a
// misconfiguration worth surfacing as a real failure). Shared by the
// global-settings and per-project-settings checks so the acceptance rule
// stays in sync if it ever changes.
func isSubsetMatch(sqsCount, scCount int) bool {
	return scCount > 0 && scCount <= sqsCount
}

func checkGlobalSettings(ctx context.Context, s *Suite) []CheckResult {
	sqsCount, err := queryCount(ctx, s.sqsRaw, "api/settings/values", nil, "settings")
	if err != nil {
		return []CheckResult{makeError("Settings", "Global settings", err)}
	}
	scCount, err := queryCount(ctx, s.scRaw, "api/settings/values",
		urlParams("component", s.cfg.SCOrg), "settings")
	if err != nil {
		return []CheckResult{makeError("Settings", "Global settings", err)}
	}
	r := makeResult("Settings", "Global settings", sqsCount, scCount, "SC-compatible subset")
	r.Notes = "SC supports subset of SQS settings"
	// SC's supported setting set is intentionally a subset of SQS, so a
	// direct equality is not expected. The check passes when SC has at
	// least one migrated setting AND that count is no larger than SQS.
	r.Match = isSubsetMatch(sqsCount, scCount)
	return []CheckResult{r}
}

func checkProjectSettings(ctx context.Context, s *Suite) []CheckResult {
	projects, err := s.getProjects(ctx)
	if err != nil {
		return []CheckResult{makeError("Settings", "Project settings", err)}
	}
	var results []CheckResult
	for _, proj := range projects {
		sqsCount, err := queryCount(ctx, s.sqsRaw, "api/settings/values", urlParams("component", proj), "settings")
		if err != nil {
			results = append(results, makeError("Settings", fmt.Sprintf("Settings: %s", proj), err))
			continue
		}
		scCount, err := queryCount(ctx, s.scRaw, "api/settings/values",
			urlParams("component", s.scProjectKey(proj)), "settings")
		if err != nil {
			results = append(results, makeError("Settings", fmt.Sprintf("Settings: %s", proj), err))
			continue
		}
		r := makeResult("Settings", fmt.Sprintf("Settings: %s", proj), sqsCount, scCount, "SC-compatible subset")
		r.Notes = "SC-compatible subset"
		// Same subset-relationship rule as checkGlobalSettings.
		r.Match = isSubsetMatch(sqsCount, scCount)
		results = append(results, r)
	}
	return results
}

// ── New Code Periods ──────────────────────────────────────────────────

func checkNewCodePeriods(ctx context.Context, s *Suite) []CheckResult {
	projects, err := s.getProjects(ctx)
	if err != nil {
		return []CheckResult{makeError("New Code Periods", "NCD", err)}
	}
	var results []CheckResult
	for _, proj := range projects {
		sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/new_code_periods/show", urlParams("project", proj))
		if err != nil {
			results = append(results, makeError("New Code Periods", fmt.Sprintf("NCD: %s", proj), err))
			continue
		}
		scBody, err := queryJSON(ctx, s.scRaw, "api/new_code_periods/show",
			urlParams("project", s.scProjectKey(proj)))
		if err != nil {
			results = append(results, makeSkipped("New Code Periods",
				fmt.Sprintf("NCD: %s", proj), "SC API may not support read-back"))
			continue
		}
		var sqsNCD, scNCD struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
		json.Unmarshal(sqsBody, &sqsNCD)
		json.Unmarshal(scBody, &scNCD)
		results = append(results, makeResultStr("New Code Periods",
			fmt.Sprintf("NCD type: %s", proj), sqsNCD.Type, scNCD.Type, "Exact"))
	}
	return results
}

// ── Rules ─────────────────────────────────────────────────────────────

func checkCustomRules(ctx context.Context, s *Suite) []CheckResult {
	sqsTotal, err := queryTotal(ctx, s.sqsRaw, "api/rules/search",
		urlParams("is_template", "false", "include_external", "false"))
	if err != nil {
		return []CheckResult{makeError("Rules", "Rule count", err)}
	}
	scTotal, err := queryTotal(ctx, s.scRaw, "api/rules/search",
		urlParams("organization", s.cfg.SCOrg, "is_template", "false"))
	if err != nil {
		return []CheckResult{makeError("Rules", "Rule count", err)}
	}
	r := makeResult("Rules", "Rule count", sqsTotal, scTotal, "Rule sets may differ")
	r.Notes = "Rule sets may differ between SQS and SC"
	// Match is left to makeResult's sqsCount == scCount comparison: SC's
	// rule catalogue is a subset of SQS so equality is rare and a mismatch
	// is informational, not a failure. The Notes field explains the gap
	// for reviewers.
	return []CheckResult{r}
}

// ── Permissions ───────────────────────────────────────────────────────

func checkProjectPermissions(ctx context.Context, s *Suite) []CheckResult {
	projects, err := s.getProjects(ctx)
	if err != nil {
		return []CheckResult{makeError("Permissions", "Project permissions", err)}
	}
	perms := []string{"admin", "codeviewer", "issueadmin", "securityhotspotadmin", "scan", "user"}
	var results []CheckResult
	for _, proj := range projects {
		for _, perm := range perms {
			sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/permissions/groups",
				urlParams("projectKey", proj, "permission", perm, "ps", "500"))
			if err != nil {
				results = append(results, makeError("Permissions", fmt.Sprintf("%s/%s", proj, perm), err))
				continue
			}
			scBody, err := queryJSON(ctx, s.scRaw, "api/permissions/groups",
				urlParams("projectKey", s.scProjectKey(proj), "permission", perm,
					"organization", s.cfg.SCOrg, "ps", "500"))
			if err != nil {
				results = append(results, makeError("Permissions", fmt.Sprintf("%s/%s", proj, perm), err))
				continue
			}
			type permResp struct {
				Groups []struct{ Name string `json:"name"` } `json:"groups"`
			}
			var sqsP, scP permResp
			json.Unmarshal(sqsBody, &sqsP)
			json.Unmarshal(scBody, &scP)
			results = append(results, makeResult("Permissions",
				fmt.Sprintf("%s/%s groups", proj, perm), len(sqsP.Groups), len(scP.Groups), "Except built-in remapping"))
		}
	}
	return results
}

// ── ALM Bindings ──────────────────────────────────────────────────────

func checkALMBindings(ctx context.Context, s *Suite) []CheckResult {
	projects, err := s.getProjects(ctx)
	if err != nil {
		return []CheckResult{makeError("ALM Bindings", "Bindings", err)}
	}
	var results []CheckResult
	for _, proj := range projects {
		sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/alm_settings/get_binding", urlParams("project", proj))
		if err != nil {
			results = append(results, makeSkipped("ALM Bindings", fmt.Sprintf("Binding: %s", proj), "No SQS binding"))
			continue
		}
		scBody, err := queryJSON(ctx, s.scRaw, "api/alm_settings/get_binding",
			urlParams("project", s.scProjectKey(proj)))
		if err != nil {
			var sqsB struct{ ALM string `json:"alm"` }
			json.Unmarshal(sqsBody, &sqsB)
			results = append(results, CheckResult{
				Category: "ALM Bindings", Name: fmt.Sprintf("Binding: %s", proj),
				SQSValue: sqsB.ALM, SCValue: "NOT FOUND", Match: false,
			})
			continue
		}
		var sqsB, scB struct{ ALM string `json:"alm"` }
		json.Unmarshal(sqsBody, &sqsB)
		json.Unmarshal(scBody, &scB)
		results = append(results, makeResultStr("ALM Bindings",
			fmt.Sprintf("Binding: %s", proj), sqsB.ALM, scB.ALM, "Exact"))
	}
	return results
}

// ── Portfolios ────────────────────────────────────────────────────────

func checkPortfolios(ctx context.Context, s *Suite) []CheckResult {
	sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/views/search", urlParams("ps", "500"))
	if err != nil {
		return []CheckResult{makeSkipped("Portfolios", "Portfolio count", "Enterprise API unavailable")}
	}
	var sqsResp struct{ Views []json.RawMessage `json:"views"` }
	if err := json.Unmarshal(sqsBody, &sqsResp); err != nil || len(sqsResp.Views) == 0 {
		return []CheckResult{makeSkipped("Portfolios", "Portfolio count", "0 portfolios on SQS")}
	}
	scBody, err := queryJSON(ctx, s.scRaw, "api/views/search",
		urlParams("organization", s.cfg.SCOrg, "ps", "500"))
	if err != nil {
		r := makeResult("Portfolios", "Portfolio count", len(sqsResp.Views), 0, "Enterprise only")
		r.Notes = "SC enterprise API may require elevated token"
		return []CheckResult{r}
	}
	var scResp struct{ Views []json.RawMessage `json:"views"` }
	json.Unmarshal(scBody, &scResp)
	return []CheckResult{makeResult("Portfolios", "Portfolio count",
		len(sqsResp.Views), len(scResp.Views), "Enterprise only")}
}

// ── Measures ──────────────────────────────────────────────────────────

func checkMeasures(ctx context.Context, s *Suite) []CheckResult {
	projects, err := s.getProjects(ctx)
	if err != nil {
		return []CheckResult{makeError("Measures", "Metrics", err)}
	}
	metrics := []string{"ncloc", "coverage", "bugs", "vulnerabilities", "code_smells",
		"sqale_rating", "reliability_rating", "security_rating",
		"duplicated_lines_density", "sqale_index", "complexity", "cognitive_complexity"}
	metricKeys := strings.Join(metrics, ",")

	var results []CheckResult
	for _, proj := range projects {
		sqsBody, err := queryJSON(ctx, s.sqsRaw, "api/measures/component",
			urlParams("component", proj, "metricKeys", metricKeys))
		if err != nil {
			results = append(results, makeError("Measures", fmt.Sprintf("Metrics: %s", proj), err))
			continue
		}
		scBody, err := queryJSON(ctx, s.scRaw, "api/measures/component",
			urlParams("component", s.scProjectKey(proj), "metricKeys", metricKeys))
		if err != nil {
			results = append(results, makeError("Measures", fmt.Sprintf("Metrics: %s", proj), err))
			continue
		}
		type measure struct {
			Metric string `json:"metric"`
			Value  string `json:"value"`
		}
		type resp struct {
			Component struct{ Measures []measure `json:"measures"` } `json:"component"`
		}
		var sqsM, scM resp
		json.Unmarshal(sqsBody, &sqsM)
		json.Unmarshal(scBody, &scM)

		scMap := make(map[string]string)
		for _, m := range scM.Component.Measures {
			scMap[m.Metric] = m.Value
		}
		for _, m := range sqsM.Component.Measures {
			tolerance := "Exact"
			if m.Metric == "coverage" || m.Metric == "duplicated_lines_density" {
				tolerance = "±0.1%"
			}
			results = append(results, makeResultStr("Measures",
				fmt.Sprintf("%s: %s", m.Metric, proj), m.Value, scMap[m.Metric], tolerance))
		}
	}
	return results
}

// ── Extract Files ─────────────────────────────────────────────────────

func checkExtractFiles(ctx context.Context, s *Suite) []CheckResult {
	expected := []string{
		"projects", "quality_profiles", "quality_gates", "groups",
		"permission_templates", "settings", "issues", "hotspots",
		"rules", "users", "project_branches",
	}
	dirs, err := findExtractDirs(s.cfg.ExportDir)
	if err != nil || len(dirs) == 0 {
		return []CheckResult{makeError("Extract Files", "Find extract dirs",
			fmt.Errorf("no extract directories found in %s", s.cfg.ExportDir))}
	}
	var results []CheckResult
	for _, dir := range dirs {
		for _, name := range expected {
			matches, _ := filepath.Glob(filepath.Join(dir, "*", name+".ndjson"))
			if len(matches) == 0 {
				matches, _ = filepath.Glob(filepath.Join(dir, name+".ndjson"))
			}
			if len(matches) == 0 {
				results = append(results, CheckResult{
					Category: "Extract Files", Name: fmt.Sprintf("%s.ndjson", name),
					SQSValue: "expected", SCValue: "MISSING", Match: false,
				})
				continue
			}
			for _, f := range matches {
				info, err := os.Stat(f)
				if err != nil {
					results = append(results, makeError("Extract Files", name+".ndjson", err))
					continue
				}
				if info.Size() == 0 {
					results = append(results, CheckResult{
						Category: "Extract Files", Name: fmt.Sprintf("%s.ndjson", name),
						SQSValue: "expected non-empty", SCValue: "EMPTY", Match: false,
					})
				} else {
					results = append(results, CheckResult{
						Category: "Extract Files", Name: fmt.Sprintf("%s.ndjson", name),
						SQSValue: fmt.Sprintf("%d bytes", info.Size()), SCValue: "present", Match: true,
					})
				}
			}
		}
	}
	return results
}

func findExtractDirs(exportDir string) ([]string, error) {
	entries, err := os.ReadDir(exportDir)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			metaPath := filepath.Join(exportDir, e.Name(), "extract.json")
			if _, err := os.Stat(metaPath); err == nil {
				dirs = append(dirs, filepath.Join(exportDir, e.Name()))
			}
		}
	}
	return dirs, nil
}
