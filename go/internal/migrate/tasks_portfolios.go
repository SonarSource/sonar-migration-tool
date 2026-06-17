// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"

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

// runSetPortfolioProjects is intentionally a no-op.
//
// The SonarQube Cloud portfolios PATCH endpoint requires `projects` to be an
// array of `{branchId: <UUID>}` objects — there is no way to assign projects
// by key. Looking up branch UUIDs per project would require an extra round
// trip per project, so we instead translate every selection mode (including
// MANUAL) into a `selection: "regex"` PATCH inside configurePortfolios:
// the regex matches the resolved cloud keys exactly. The task survives only
// as a dependency anchor for configurePortfolios.
func runSetPortfolioProjects(_ context.Context, e *Executor) error {
	w, err := e.Store.Writer("setPortfolioProjects")
	if err != nil {
		return err
	}
	return w.WriteChunk(nil)
}

// runConfigurePortfolios sets the SQC selection mode for every source
// portfolio that has a definition we can translate. Three selection modes
// are migrated:
//
//   - REGEXP: rewrite the SQS regex so it matches under the SQC project-key
//     renaming scheme (orgKey_<sqsKey>).
//   - TAGS:   forward the source tag list verbatim.
//   - MANUAL: turn the resolved cloud project keys into a regex that matches
//     exactly those keys (SQC's PATCH endpoint rejects raw project keys —
//     it requires per-project branch UUIDs we do not hold). This keeps the
//     portfolio populated without an extra branches lookup; the trade-off
//     is that new projects with matching keys will be auto-included, which
//     is acceptable because cloud keys are namespaced by org.
//
// Selection modes NONE and REST are left alone — they map to "empty" on
// SQC, which is the default state after createPortfolios.
//
// When a portfolio's resolved projects span no migrated org (typically for
// REGEXP/TAGS portfolios on an SQS instance that has not yet computed the
// match), the task falls back to every migrated org so the portfolio is
// still scoped and the PATCH still goes out.
func runConfigurePortfolios(ctx context.Context, e *Executor) error {
	projectIndex := buildProjectCloudKeyIndex(e)
	portfolioProjects := buildPortfolioProjectList(e, projectIndex)
	portfolioOrgs := buildPortfolioOrgIndex(e, projectIndex)
	allMigratedOrgs := collectAllMigratedOrgs(projectIndex)
	portfolioCompositions := indexPortfolioCompositions(e)

	// Side-channel writer that records per-portfolio task-level failures
	// (skipped because of missing data, etc.) so the summary report can mark
	// them as Failed even when no HTTP request was issued.
	failW, _ := e.Store.Writer("configurePortfolios.failures")
	// Cache of cloud_project_key → main branch UUID, populated lazily via
	// /api/project_branches/list. Required by SQC's PATCH endpoint when
	// selection is "projects". forEachMigrateItem fans the closure out
	// concurrently across portfolios, so the cache is shared mutable
	// state — guard reads and writes with branchIDMu (#308).
	branchIDByCloudKey := map[string]string{}
	var branchIDMu sync.Mutex
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "configurePortfolios", "createPortfolios",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			selectionMode := strings.ToUpper(extractField(item, "selection_mode"))
			portfolioID := extractField(item, "cloud_portfolio_id")
			sourceKey := extractField(item, "source_portfolio_key")
			serverURL := extractField(item, "server_url")
			name := extractField(item, "name")
			if portfolioID == "" || sourceKey == "" {
				return nil
			}

			orgs := portfolioOrgs[sourceKey]
			if len(orgs) == 0 && len(allMigratedOrgs) > 0 {
				e.Logger.Info("configurePortfolios: no resolved projects on source, falling back to all migrated orgs",
					"portfolio", sourceKey, "orgs", allMigratedOrgs)
				orgs = allMigratedOrgs
			}

			composition := portfolioCompositions[serverURL+"|"+sourceKey]
			// Effective selection mode + criteria after #229: a parent
			// portfolio whose own selection_mode is empty/NONE/REST is
			// configured from the composition of its direct subviews —
			// apps (substituted by their enclosed projects), or
			// subportfolios (combined when uniform / flattened
			// otherwise). Single-mode portfolios are unaffected.
			effMode, effRegexp, effTags := resolveEffectivePortfolioConfig(selectionMode,
				extractField(item, "regexp"), extractField(item, "tags"),
				composition, orgs, e.ProjectKeyPattern)
			if effMode == "" {
				return nil
			}

			params := cloud.UpdatePortfolioParams{
				PortfolioID: portfolioID,
			}
			switch effMode {
			case "REGEXP":
				params.Selection = "regex"
				params.RegularExpression = effRegexp
			case "TAGS":
				params.Selection = "tags"
				params.Tags = effTags
			case "MANUAL":
				cloudKeys := portfolioProjects[sourceKey]
				if len(cloudKeys) == 0 {
					msg := "MANUAL portfolio has no resolved cloud projects to migrate"
					e.Logger.Warn("configurePortfolios: "+msg, "portfolio", sourceKey)
					counter.Fail()
					recordPortfolioFailure(failW, portfolioID, name, msg)
					return nil
				}
				refs, lookupErr := resolveProjectBranchRefs(ctx, e, branchIDByCloudKey, &branchIDMu, cloudKeys)
				if lookupErr != nil || len(refs) == 0 {
					msg := "could not resolve main branch UUID for any MANUAL portfolio project"
					if lookupErr != nil {
						msg = msg + ": " + lookupErr.Error()
					}
					e.Logger.Warn("configurePortfolios: "+msg, "portfolio", sourceKey)
					counter.Fail()
					recordPortfolioFailure(failW, portfolioID, name, msg)
					return nil
				}
				params.Selection = "projects"
				params.Projects = refs
			}

			e.Logger.Debug("configurePortfolios: sending PATCH",
				"portfolio", portfolioID,
				"name", name,
				"selection", params.Selection,
				"regex", params.RegularExpression,
				"tags", params.Tags,
				"projects", len(params.Projects),
			)
			if err := e.CloudAPI.Enterprises.UpdatePortfolio(ctx, params); err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "configurePortfolios failed", err,
					"portfolio", portfolioID, "selection", selectionMode)
				recordPortfolioFailure(failW, portfolioID, name, "PATCH /enterprises/portfolios/<id>: "+err.Error())
			} else {
				counter.Success()
				e.Logger.Info("configurePortfolios: PATCH succeeded",
					"portfolio", portfolioID, "selection", params.Selection)
			}
			return nil
		})
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
	seen := make(map[string]map[string]bool)
	for _, item := range items {
		portfolioKey := extractField(item.Data, "portfolioKey")
		refKey := extractField(item.Data, "refKey")
		info, ok := projectIndex[item.ServerURL+refKey]
		if !ok || info.CloudKey == "" {
			continue
		}
		if seen[portfolioKey] == nil {
			seen[portfolioKey] = map[string]bool{}
		}
		if seen[portfolioKey][info.CloudKey] {
			continue
		}
		seen[portfolioKey][info.CloudKey] = true
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

// collectAllMigratedOrgs returns the unique list of SonarQube Cloud
// organization keys that received at least one project during the run.
// Used as the fallback target set for portfolios whose resolved-projects
// list is empty on the source side.
func collectAllMigratedOrgs(projectIndex map[string]cloudProjectInfo) []string {
	seen := map[string]bool{}
	var out []string
	for _, info := range projectIndex {
		if info.OrgKey == "" || seen[info.OrgKey] {
			continue
		}
		seen[info.OrgKey] = true
		out = append(out, info.OrgKey)
	}
	return out
}

// recordPortfolioFailure appends a sidecar JSONL entry describing a
// per-portfolio failure inside configurePortfolios. The summary report
// reads this file to surface task-level failures (e.g. "no resolved
// projects on source") that never produced an HTTP request and therefore
// don't appear in requests.log.
func recordPortfolioFailure(w *common.ChunkWriter, portfolioID, name, reason string) {
	if w == nil {
		return
	}
	rec, _ := json.Marshal(map[string]any{
		"cloud_portfolio_id": portfolioID,
		"name":               name,
		"reason":             reason,
	})
	_ = w.WriteOne(rec)
}

// resolveProjectBranchRefs maps each cloud project key to its main branch
// UUID via /api/project_branches/list and returns the list of
// PortfolioProjectRef objects required by the SQC enterprise portfolios
// PATCH endpoint when selection is "projects". The cache is populated
// lazily so repeated portfolios with overlapping projects share lookups.
//
// The caller is responsible for serialising access to the shared cache via
// mu — runConfigurePortfolios fans this function out concurrently across
// portfolios, so without the lock the lazy write produced a "concurrent
// map writes" panic in production (#308). Two concurrent goroutines may
// still both miss in cache for the same key and each issue an HTTP
// lookup; the second's write simply overwrites the first with an
// identical value, so the redundant call is wasted work, not corruption.
//
// If any individual lookup fails, it is logged at Warn level and the
// project is skipped — the rest still get migrated. Returns an error only
// when no UUIDs at all could be resolved (e.g. the endpoint is unreachable).
func resolveProjectBranchRefs(ctx context.Context, e *Executor, cache map[string]string, mu *sync.Mutex, cloudKeys []string) ([]cloud.PortfolioProjectRef, error) {
	refs := make([]cloud.PortfolioProjectRef, 0, len(cloudKeys))
	var firstErr error
	for _, key := range cloudKeys {
		if key == "" {
			continue
		}
		mu.Lock()
		uuid, ok := cache[key]
		mu.Unlock()
		if !ok {
			id, err := e.Cloud.Branches.MainBranchID(ctx, key)
			if err != nil {
				e.Logger.Warn("configurePortfolios: main branch UUID lookup failed, project will be omitted",
					"project", key, "err", err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			mu.Lock()
			cache[key] = id
			mu.Unlock()
			uuid = id
		}
		refs = append(refs, cloud.PortfolioProjectRef{BranchID: uuid})
	}
	if len(refs) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, nil
	}
	return refs, nil
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
//
// The prefix each org contributes is derived from the configured
// project-key pattern (issue #138) via ProjectKeyAffixes, so the regex
// adapts to whatever renaming strategy is in effect. For the default
// <ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY> pattern the prefix is "<org>_", matching
// the historical behaviour. A non-empty affix suffix (rare — only when the
// pattern places literal text after <ORIGINAL_PROJECT_KEY>) is mirrored onto
// the regex tail; it is assumed org-independent.
func transformPortfolioRegex(regex string, orgKeys []string, pattern string) string {
	if regex == "" {
		return ""
	}
	seen := map[string]bool{}
	prefixes := make([]string, 0, len(orgKeys))
	suffix := ""
	for _, o := range orgKeys {
		if o == "" {
			continue
		}
		p, s := ProjectKeyAffixes(pattern, o)
		quoted := regexp.QuoteMeta(p)
		// Dedupe on the rendered prefix, not the org key: a pattern with a
		// static prefix (no <ORGANIZATION_KEY>) produces the same prefix for every
		// org, so the alternation must collapse to a single branch.
		if seen[quoted] {
			continue
		}
		seen[quoted] = true
		prefixes = append(prefixes, quoted)
		if s != "" {
			suffix = s
		}
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
	out := regex
	if strings.HasPrefix(out, "^") {
		out = "^" + prefix + out[1:]
	} else {
		out = prefix + out
	}
	if suffix != "" {
		q := regexp.QuoteMeta(suffix)
		if strings.HasSuffix(out, "$") {
			out = out[:len(out)-1] + q + "$"
		} else {
			out += q
		}
	}
	return out
}

// buildEmptyPortfolioSet returns composite serverURL|portfolioKey strings
// for every source portfolio that has zero resolved projects in the
// getPortfolioProjects extract. Used by createPortfolios to skip
// portfolios that would land empty on SonarQube Cloud anyway.
func buildEmptyPortfolioSet(e *Executor) map[string]bool {
	mappings, _ := e.Store.ReadAll("generatePortfolioMappings")
	if len(mappings) == 0 {
		return nil
	}
	nonEmpty := make(map[string]bool)
	items, _ := readExtractItems(e, "getPortfolioProjects")
	for _, it := range items {
		pk := extractField(it.Data, "portfolioKey")
		rk := extractField(it.Data, "refKey")
		if pk == "" || rk == "" {
			continue
		}
		nonEmpty[it.ServerURL+"|"+pk] = true
	}
	out := make(map[string]bool)
	for _, m := range mappings {
		serverURL := extractField(m, "server_url")
		sourceKey := extractField(m, "source_portfolio_key")
		if serverURL == "" || sourceKey == "" {
			continue
		}
		composite := serverURL + "|" + sourceKey
		if !nonEmpty[composite] {
			out[composite] = true
		}
	}
	return out
}

// indexPortfolioCompositions reads getPortfolioDetails JSONL and returns
// a map of composite serverURL|portfolioKey → PortfolioComposition for
// every parent portfolio. Leaf portfolios (no subviews) are absent —
// the caller treats a zero-value composition as "no special handling".
func indexPortfolioCompositions(e *Executor) map[string]PortfolioComposition {
	items, _ := readExtractItems(e, "getPortfolioDetails")
	out := make(map[string]PortfolioComposition, len(items))
	for _, item := range items {
		var d map[string]any
		if err := json.Unmarshal(item.Data, &d); err != nil {
			continue
		}
		k, _ := d["key"].(string)
		if k == "" {
			continue
		}
		pc := AnalyzePortfolio(d)
		if !pc.HasApps && !pc.HasSubportfolios {
			continue
		}
		out[item.ServerURL+"|"+k] = pc
	}
	return out
}

// resolveEffectivePortfolioConfig produces the (mode, regex, tags) that
// configurePortfolios should apply to a single SQC portfolio.
//
//  1. When the source portfolio's own selection_mode is one of
//     REGEXP/TAGS/MANUAL, that takes precedence — single-portfolio
//     migration is unchanged.
//  2. When the source portfolio has only applications and no own mode,
//     the SQC portfolio is configured as MANUAL with the flat resolved
//     project list (apps are substituted by their enclosed projects via
//     api/views/projects_status).
//  3. When the source portfolio has direct subportfolios whose modes
//     all agree (REGEXP / TAGS / MANUAL) AND none of them has further
//     nested subportfolios, the criteria are combined — regex
//     alternation, tag union, or the same flat project list for MANUAL.
//  4. Anything else (mixed modes, nested depth ≥ 2) falls back to
//     MANUAL with the flat resolved project list — the only safe
//     translation of a non-uniform hierarchy on SQC.
//
// An empty effMode return means "no configurePortfolios call" (e.g. a
// truly empty parent with no children).
func resolveEffectivePortfolioConfig(parentMode, parentRegexp, parentTags string,
	composition PortfolioComposition, orgs []string, pattern string) (effMode, effRegexp string, effTags []string) {

	if parentMode == "REGEXP" {
		return "REGEXP", transformPortfolioRegex(parentRegexp, orgs, pattern), nil
	}
	if parentMode == "TAGS" {
		if parentTags == "" {
			return "TAGS", "", nil
		}
		return "TAGS", "", splitTrim(parentTags)
	}
	if parentMode == "MANUAL" {
		return "MANUAL", "", nil
	}
	// REST mode (catch-all for "rest of projects") has no SQC
	// equivalent — flatten to the resolved project list captured at
	// extract time. #229.
	if parentMode == "REST" {
		return "MANUAL", "", nil
	}

	// Composition-driven configuration (#229).
	switch {
	case composition.HasSubportfolios && !composition.DepthGT1 && composition.CommonSelectionMode != "":
		switch composition.CommonSelectionMode {
		case "REGEXP":
			parts := make([]string, 0, len(composition.Subportfolios))
			for _, sp := range composition.Subportfolios {
				if sp.Regexp == "" {
					continue
				}
				parts = append(parts, transformPortfolioRegex(sp.Regexp, orgs, pattern))
			}
			if len(parts) == 0 {
				return "", "", nil
			}
			if len(parts) == 1 {
				return "REGEXP", parts[0], nil
			}
			return "REGEXP", "(" + strings.Join(parts, "|") + ")", nil
		case "TAGS":
			seen := map[string]bool{}
			var union []string
			for _, sp := range composition.Subportfolios {
				for _, t := range sp.Tags {
					t = strings.TrimSpace(t)
					if t == "" || seen[t] {
						continue
					}
					seen[t] = true
					union = append(union, t)
				}
			}
			if len(union) == 0 {
				return "", "", nil
			}
			return "TAGS", "", union
		case "MANUAL":
			return "MANUAL", "", nil
		}
	case composition.HasSubportfolios:
		// Depth ≥ 2 or mixed modes → flat project list.
		return "MANUAL", "", nil
	case composition.HasApps:
		// Apps only → flat project list (apps substituted by enclosed
		// projects via the resolved getPortfolioProjects data).
		return "MANUAL", "", nil
	}
	return "", "", nil
}

func splitTrim(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
