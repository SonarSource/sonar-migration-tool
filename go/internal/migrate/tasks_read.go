package migrate

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// readTasks returns tasks that fetch existing state from SonarQube Cloud.
func readTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "getProjectIds",
			Dependencies: []string{"createProjects"},
			Run:          runGetProjectIds,
		},
		{
			Name:         "getOrgRepos",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runGetOrgRepos,
		},
		{
			Name:         "getGateConditions",
			Dependencies: []string{"createGates"},
			Run:          runGetGateConditions,
		},
		{
			Name:         "getProfileBackups",
			Dependencies: []string{"createProfiles"},
			Run:          runGetProfileBackups,
		},
		{
			Name:         "getMigrationUser",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runGetMigrationUser,
		},
		{
			Name:         "getCreatedProjects",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runGetCreatedProjects,
		},
		{
			Name:         "getEnterprises",
			Editions:     []common.Edition{common.EditionEnterprise, common.EditionDatacenter},
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runGetEnterprises,
		},
	}
}

func runGetProjectIds(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "getProjectIds", "createProjects",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			projectKey := extractField(item, "cloud_project_key")
			if shouldSkipOrg(orgKey) || projectKey == "" {
				return nil
			}
			e.Logger.Debug("project api call: GET /api/projects/search (lookup by key)",
				"project", projectKey, "org", orgKey)
			raw, err := e.Raw.GetPaginated(ctx, common.PaginatedOpts{
				Path: "api/projects/search", ResultKey: "components",
				Params: url.Values{
					"organization": {orgKey},
					"projects":     {projectKey},
				},
			})
			if err != nil {
				return err
			}
			enriched := common.EnrichAll(raw, map[string]any{
				"sonarcloud_org_key": orgKey,
			})
			return w.WriteChunk(enriched)
		})
}

func runGetOrgRepos(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "getOrgRepos", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			raw, err := e.Raw.GetArray(ctx,
				"api/alm_integration/list_repositories", "repositories",
				url.Values{"organization": {orgKey}})
			if err != nil {
				e.Logger.Warn("getOrgRepos skipped", "org", orgKey, "err", err)
				return nil
			}
			enriched := common.EnrichAll(raw, map[string]any{
				"sonarcloud_org_key": orgKey,
			})
			return w.WriteChunk(enriched)
		})
}

func runGetGateConditions(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "getGateConditions", "createGates",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			gateName := extractField(item, "source_gate_key")
			orgKey := extractField(item, "sonarcloud_org_key")
			serverURL := extractField(item, "server_url")
			cloudGateID := extractField(item, "cloud_gate_id")
			wasPreexisting := extractBool(item, "was_preexisting")
			if cloudGateID == "" {
				return nil
			}

			// The extract writes one record per source condition, each
			// enriched with the parent gateName and serverUrl. Group every
			// matching condition into a single per-gate record so the
			// downstream addGateConditions task can decide once per gate
			// whether to clear the target's pre-existing conditions first.
			extractItems, _ := readExtractItems(e, "getGateConditions")
			var conditions []map[string]any
			for _, ei := range extractItems {
				if extractField(ei.Data, "gateName") != gateName || ei.ServerURL != serverURL {
					continue
				}
				var cond map[string]any
				if err := json.Unmarshal(ei.Data, &cond); err != nil {
					continue
				}
				// Drop the bookkeeping fields the extract added — only the
				// condition payload itself is useful downstream.
				delete(cond, "gateName")
				delete(cond, "serverUrl")
				conditions = append(conditions, cond)
			}
			if len(conditions) == 0 && !wasPreexisting {
				return nil
			}

			out, err := json.Marshal(map[string]any{
				"gate_name":          gateName,
				"sonarcloud_org_key": orgKey,
				"cloud_gate_id":      cloudGateID,
				"was_preexisting":    wasPreexisting,
				"conditions":         conditions,
			})
			if err != nil {
				return err
			}
			return w.WriteOne(out)
		})
}

func runGetProfileBackups(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "getProfileBackups", "createProfiles",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			profileKey := extractField(item, "source_profile_key")
			orgKey := extractField(item, "sonarcloud_org_key")
			// Read backup from extract data.
			items, _ := readExtractItems(e, "getProfileBackups")
			for _, ei := range items {
				eiKey := extractField(ei.Data, "profileKey")
				if eiKey == profileKey {
					enriched := common.EnrichRaw(ei.Data, map[string]any{
						"sonarcloud_org_key": orgKey,
					})
					if err := w.WriteOne(enriched); err != nil {
						return err
					}
				}
			}
			return nil
		})
}

func runGetMigrationUser(ctx context.Context, e *Executor) error {
	w, err := e.Store.Writer("getMigrationUser")
	if err != nil {
		return err
	}
	raw, err := e.Raw.Get(ctx, "api/users/current", nil)
	if err != nil {
		return err
	}
	return w.WriteOne(raw)
}

// runGetCreatedProjects lists every project in every mapped SonarCloud
// organization. It iterates generateOrganizationMappings (one record per
// org) rather than createProjects (one record per project) so that the
// /api/projects/search call fires exactly once per org. Reset feeds the
// output to deleteProjects; iterating per-project caused N×N duplicates
// where reset would try to delete the same key once per createProjects
// record in that org.
func runGetCreatedProjects(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "getCreatedProjects", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			e.Logger.Debug("project api call: GET /api/projects/search (list org projects)",
				"org", orgKey)
			raw, err := e.Raw.GetPaginated(ctx, common.PaginatedOpts{
				Path: "api/projects/search", ResultKey: "components",
				Params: url.Values{"organization": {orgKey}},
			})
			if err != nil {
				return err
			}
			enriched := common.EnrichAll(raw, map[string]any{
				"sonarcloud_org_key": orgKey,
			})
			return w.WriteChunk(enriched)
		})
}

func runGetEnterprises(ctx context.Context, e *Executor) error {
	w, err := e.Store.Writer("getEnterprises")
	if err != nil {
		return err
	}
	raw, err := e.RawAPI.Get(ctx, "enterprises/enterprises", nil)
	if err != nil {
		return err
	}
	return w.WriteOne(raw)
}
