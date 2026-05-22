package migrate

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/sonar-solutions/sq-api-go/cloud"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// portfolioTasks returns tasks for Enterprise portfolio management.
func portfolioTasks() []TaskDef {
	entEditions := []common.Edition{common.EditionEnterprise, common.EditionDatacenter}

	return []TaskDef{
		{
			Name:         "setPortfolioProjects",
			Editions:     entEditions,
			Dependencies: []string{"createPortfolios", "createProjects"},
			Run:          runSetPortfolioProjects,
		},
		{
			Name:         "configurePortfolios",
			Editions:     entEditions,
			Dependencies: []string{"createPortfolios", "createProjects", "setPortfolioProjects"},
			Run:          runConfigurePortfolios,
		},
	}
}

// runSetPortfolioProjects assigns the resolved project list to MANUAL source
// portfolios. REGEXP- and TAGS-based source portfolios are skipped here and
// handled by runConfigurePortfolios instead, so we don't overwrite their
// selection mode with a project list.
func runSetPortfolioProjects(ctx context.Context, e *Executor) error {
	projectIndex := buildProjectCloudKeyIndex(e)
	portfolioProjects := buildPortfolioProjectList(e, projectIndex)

	counter := NewTaskCounter("setPortfolioProjects")
	err := forEachMigrateItem(ctx, e, "setPortfolioProjects", "createPortfolios",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			selectionMode := strings.ToUpper(extractField(item, "selection_mode"))
			if selectionMode == "REGEXP" || selectionMode == "TAGS" {
				return nil
			}
			portfolioID := extractField(item, "cloud_portfolio_id")
			sourceKey := extractField(item, "source_portfolio_key")
			projects, ok := portfolioProjects[sourceKey]
			if !ok || len(projects) == 0 {
				return nil
			}

			err := e.CloudAPI.Enterprises.UpdatePortfolio(ctx, cloud.UpdatePortfolioParams{
				PortfolioID: portfolioID,
				ProjectKeys: projects,
			})
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "setPortfolioProjects failed", err,
					"portfolio", portfolioID)
			} else {
				counter.Success()
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

// runConfigurePortfolios sets the SQC selection mode for REGEXP- and
// TAGS-based source portfolios. The regex is rewritten so that it matches
// projects under their new SonarQube Cloud key prefix (orgKey_<...>); for
// portfolios whose projects span multiple SQC orgs, the prefix becomes a
// non-capturing alternation of org keys.
func runConfigurePortfolios(ctx context.Context, e *Executor) error {
	projectIndex := buildProjectCloudKeyIndex(e)
	portfolioOrgs := buildPortfolioOrgIndex(e, projectIndex)

	orgUUIDs := map[string]string{}
	counter := NewTaskCounter("configurePortfolios")
	err := forEachMigrateItem(ctx, e, "configurePortfolios", "createPortfolios",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			selectionMode := strings.ToUpper(extractField(item, "selection_mode"))
			if selectionMode != "REGEXP" && selectionMode != "TAGS" {
				return nil
			}
			portfolioID := extractField(item, "cloud_portfolio_id")
			sourceKey := extractField(item, "source_portfolio_key")
			if portfolioID == "" || sourceKey == "" {
				return nil
			}
			orgs := portfolioOrgs[sourceKey]
			if len(orgs) == 0 {
				e.Logger.Warn("configurePortfolios: no target organization found, skipping",
					"portfolio", sourceKey)
				return nil
			}
			orgIDs, err := resolveOrgUUIDs(ctx, e, orgUUIDs, orgs)
			if err != nil || len(orgIDs) == 0 {
				counter.Fail()
				return nil
			}

			params := cloud.UpdatePortfolioParams{
				PortfolioID:     portfolioID,
				OrganizationIDs: orgIDs,
			}
			switch selectionMode {
			case "REGEXP":
				params.Selection = "regexp"
				params.RegularExpression = transformPortfolioRegex(
					extractField(item, "regexp"), orgs)
			case "TAGS":
				params.Selection = "tags"
				if tagsStr := extractField(item, "tags"); tagsStr != "" {
					params.Tags = strings.Split(tagsStr, ",")
				}
			}

			if err := e.CloudAPI.Enterprises.UpdatePortfolio(ctx, params); err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "configurePortfolios failed", err,
					"portfolio", portfolioID, "selection", selectionMode)
			} else {
				counter.Success()
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

// cloudProjectInfo records the per-project data we need when assembling
// portfolio configurations: the cloud key and the SQC organization it belongs to.
type cloudProjectInfo struct {
	CloudKey string
	OrgKey   string
}

// buildProjectCloudKeyIndex returns a map keyed by serverURL+SQS_project_key →
// {cloud key, sonarcloud org key} built from createProjects JSONL.
func buildProjectCloudKeyIndex(e *Executor) map[string]cloudProjectInfo {
	projects, _ := e.Store.ReadAll("createProjects")
	out := make(map[string]cloudProjectInfo, len(projects))
	for _, p := range projects {
		serverURL := extractField(p, "server_url")
		key := extractField(p, "key")
		if serverURL == "" || key == "" {
			continue
		}
		out[serverURL+key] = cloudProjectInfo{
			CloudKey: extractField(p, "cloud_project_key"),
			OrgKey:   extractField(p, "sonarcloud_org_key"),
		}
	}
	return out
}

// buildPortfolioProjectList resolves each portfolio's project membership to
// the cloud project keys we created. Returns source_portfolio_key →
// []cloud_project_key.
func buildPortfolioProjectList(e *Executor, projectIndex map[string]cloudProjectInfo) map[string][]string {
	items, _ := readExtractItems(e, "getPortfolioProjects")
	out := make(map[string][]string)
	for _, item := range items {
		portfolioKey := extractField(item.Data, "portfolioKey")
		refKey := extractField(item.Data, "refKey")
		info, ok := projectIndex[item.ServerURL+refKey]
		if !ok || info.CloudKey == "" {
			continue
		}
		out[portfolioKey] = append(out[portfolioKey], info.CloudKey)
	}
	return out
}

// buildPortfolioOrgIndex returns source_portfolio_key → unique list of
// sonarcloud_org_key values across the portfolio's resolved projects.
func buildPortfolioOrgIndex(e *Executor, projectIndex map[string]cloudProjectInfo) map[string][]string {
	items, _ := readExtractItems(e, "getPortfolioProjects")
	tmp := make(map[string]map[string]bool)
	for _, item := range items {
		portfolioKey := extractField(item.Data, "portfolioKey")
		refKey := extractField(item.Data, "refKey")
		info, ok := projectIndex[item.ServerURL+refKey]
		if !ok || info.OrgKey == "" {
			continue
		}
		if tmp[portfolioKey] == nil {
			tmp[portfolioKey] = map[string]bool{}
		}
		tmp[portfolioKey][info.OrgKey] = true
	}
	out := make(map[string][]string, len(tmp))
	for portfolioKey, set := range tmp {
		for orgKey := range set {
			out[portfolioKey] = append(out[portfolioKey], orgKey)
		}
	}
	return out
}

// resolveOrgUUIDs converts each org key to its UUID via the SonarQube Cloud
// organizations API, caching results across calls.
func resolveOrgUUIDs(ctx context.Context, e *Executor, cache map[string]string, orgKeys []string) ([]string, error) {
	out := make([]string, 0, len(orgKeys))
	for _, orgKey := range orgKeys {
		uuid, ok := cache[orgKey]
		if !ok {
			id, err := e.Cloud.Organizations.LookupID(ctx, orgKey)
			if err != nil {
				e.Logger.Warn("configurePortfolios: organization UUID lookup failed",
					"org", orgKey, "err", err)
				continue
			}
			cache[orgKey] = id
			uuid = id
		}
		out = append(out, uuid)
	}
	return out, nil
}

// transformPortfolioRegex rewrites a source-side regex so that it matches
// projects under the SQC project-key renaming scheme (cloud key =
// <orgKey>_<sqsKey>). When the portfolio's projects span multiple orgs, the
// generated prefix is an alternation of org keys.
//
// Examples (orgKeys = ["org1"]):
//
//	"^backend-"    → "^org1_backend-"
//	"^[A-Z].*"     → "^org1_[A-Z].*"
//	"backend"      → "org1_backend"
//
// With orgKeys = ["org1","org2"]:
//
//	"^foo" → "^(?:org1_|org2_)foo"
func transformPortfolioRegex(regex string, orgKeys []string) string {
	if regex == "" {
		return ""
	}
	seen := map[string]bool{}
	prefixes := make([]string, 0, len(orgKeys))
	for _, o := range orgKeys {
		if o == "" || seen[o] {
			continue
		}
		seen[o] = true
		prefixes = append(prefixes, regexp.QuoteMeta(o+"_"))
	}
	if len(prefixes) == 0 {
		return regex
	}
	var prefix string
	if len(prefixes) == 1 {
		prefix = prefixes[0]
	} else {
		prefix = "(?:" + strings.Join(prefixes, "|") + ")"
	}
	if strings.HasPrefix(regex, "^") {
		return "^" + prefix + regex[1:]
	}
	return prefix + regex
}
