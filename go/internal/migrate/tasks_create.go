// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/cloud"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// createTasks returns tasks that create entities in SonarQube Cloud.
func createTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "createProjects",
			Dependencies: []string{"generateProjectMappings"},
			Run:          runCreateProjects,
		},
		{
			Name:         "createProfiles",
			Dependencies: []string{"generateProfileMappings"},
			Run:          runCreateProfiles,
		},
		{
			Name:         "createGates",
			Dependencies: []string{"generateGateMappings"},
			Run:          runCreateGates,
		},
		{
			Name:         "createGroups",
			Dependencies: []string{"generateGroupMappings"},
			Run:          runCreateGroups,
		},
		{
			Name:         "createPermissionTemplates",
			Dependencies: []string{"generateTemplateMappings"},
			Run:          runCreatePermissionTemplates,
		},
		{
			Name:         "createPortfolios",
			Editions:     []common.Edition{common.EditionEnterprise, common.EditionDatacenter},
			Dependencies: []string{"generatePortfolioMappings", "getEnterprises"},
			Run:          runCreatePortfolios,
		},
	}
}

func runCreateProjects(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "createProjects", "generateProjectMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			key := extractField(item, "key")
			name := extractField(item, "name")
			ncdType := extractField(item, "new_code_definition_type")
			ncdValue := extractAnyStr(item, "new_code_definition_value")

			cloudKey := orgKey + "_" + key
			e.Logger.Debug("project api call: POST /api/projects/create",
				"source_key", key, "cloud_key", cloudKey, "name", name, "org", orgKey,
				"new_code_type", ncdType, "new_code_value", ncdValue)
			proj, err := e.Cloud.Projects.Create(ctx, cloud.CreateProjectParams{
				ProjectKey:             cloudKey,
				Name:                   name,
				Organization:           orgKey,
				Visibility:             "private",
				NewCodeDefinitionType:  ncdType,
				NewCodeDefinitionValue: ncdValue,
			})
			if err != nil {
				if !sqapi.IsAlreadyExists(err) {
					counter.Fail()
					logAPIWarn(e.Logger, "createProjects: create failed", err, "key", key)
					return nil
				}
				// SonarQube Cloud project keys are GLOBALLY unique, not
				// org-scoped, so "key already exists" doesn't guarantee
				// the existing project is in OUR target org. If it
				// isn't, downstream tasks (setProjectSettings,
				// setGlobalSettings fan-out, etc.) will issue PATCHes
				// against a phantom project and get 404s. Verify the
				// project is actually accessible in orgKey before
				// recording it (issue #193).
				exists, verifyErr := e.Cloud.Projects.ExistsInOrg(ctx, cloudKey, orgKey)
				if verifyErr != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "createProjects: could not verify already-existing project belongs to target org",
						verifyErr, "source_key", key, "cloud_key", cloudKey, "org", orgKey)
					return nil
				}
				if !exists {
					counter.Fail()
					e.Logger.Warn("createProjects: key already exists but project is not in target org, skipping",
						"source_key", key, "cloud_key", cloudKey, "org", orgKey)
					return nil
				}
				counter.Success()
				e.Logger.Info("createProjects: already exists", "source_key", key, "cloud_key", cloudKey, "org", orgKey)
			} else {
				counter.Success()
				cloudKey = proj.Key
				e.Logger.Debug("project operation: created new project",
					"source_key", key, "cloud_key", cloudKey, "name", name, "org", orgKey)
			}

			result := common.EnrichRaw(item, map[string]any{
				"cloud_project_key":  cloudKey,
				"sonarcloud_org_key": orgKey,
			})
			return w.WriteOne(result)
		})
	return err
}

func runCreateProfiles(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	// Serial creation: SonarCloud QP creation is async at the API
	// layer but enforces (org, name, language) uniqueness at the DB
	// layer, so two concurrent POSTs racing on the same name can both
	// succeed at the API and then crash the index. Profile counts are
	// small (typically <30, 99% of runs <100) so the wall-clock cost
	// of running serially is negligible. Issue #338.
	e.Logger.Info("createProfiles: provisioning quality profiles one at a time " +
		"(SonarCloud requires (org, name, language) uniqueness — see #338)")
	err := forEachMigrateItemSerial(ctx, e, "createProfiles", "generateProfileMappings",
		func(item json.RawMessage) bool {
			lang := extractField(item, "language")
			return !unsupportedLanguages[lang]
		},
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			name := extractField(item, "name")
			lang := extractField(item, "language")

			var profileKey string
			var reusedExisting bool
			prof, err := e.Cloud.QualityProfiles.Create(ctx, cloud.CreateProfileParams{
				Name: name, Language: lang, Organization: orgKey,
			})
			if err != nil {
				if !sqapi.IsAlreadyExists(err) {
					counter.Fail()
					logAPIWarn(e.Logger, "createProfiles: create failed", err, "name", name)
					return nil
				}
				e.Logger.Info("createProfiles: already exists, looking up", "name", name)
				profileKey, err = lookupExistingProfile(ctx, e.Raw, name, lang, orgKey)
				if err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "createProfiles: lookup failed", err, "name", name)
					return nil
				}
				counter.Success()
				reusedExisting = true
			} else {
				counter.Success()
				profileKey = prof.Key
			}

			// Per-profile completion line — one per provisioned QP
			// (#338). Helpful when the serial loop is slow enough to
			// make the overall task's progress logger feel sparse.
			e.Logger.Info("createProfiles: provisioned",
				"name", name, "language", lang, "org", orgKey,
				"cloud_profile_key", profileKey, "reused_existing", reusedExisting)

			result := common.EnrichRaw(item, map[string]any{
				"cloud_profile_key":  profileKey,
				"sonarcloud_org_key": orgKey,
			})
			return w.WriteOne(result)
		})
	return err
}

func runCreateGates(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "createGates", "generateGateMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			name := extractField(item, "name")

			e.Logger.Debug("gate api call: POST /api/qualitygates/create",
				"name", name, "org", orgKey)
			var gateID string
			wasPreexisting := false
			gate, err := e.Cloud.QualityGates.Create(ctx, name, orgKey)
			if err != nil {
				if !sqapi.IsAlreadyExists(err) {
					counter.Fail()
					logAPIWarn(e.Logger, "createGates: create failed", err, "name", name)
					return nil
				}
				e.Logger.Info("createGates: already exists, will override conditions", "name", name)
				e.Logger.Debug("gate api call: GET /api/qualitygates/list (lookup)",
					"name", name, "org", orgKey)
				gateID, err = lookupExistingGate(ctx, e.Raw, name, orgKey)
				if err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "createGates: lookup failed", err, "name", name)
					return nil
				}
				wasPreexisting = true
				counter.Success()
				e.Logger.Debug("gate operation: reusing existing gate",
					"name", name, "gate_id", gateID, "org", orgKey)
			} else {
				counter.Success()
				gateID = strconv.Itoa(gate.ID)
				e.Logger.Debug("gate operation: created new gate",
					"name", name, "gate_id", gateID, "org", orgKey)
			}

			result := common.EnrichRaw(item, map[string]any{
				"cloud_gate_id":      gateID,
				"sonarcloud_org_key": orgKey,
				"was_preexisting":    wasPreexisting,
			})
			return w.WriteOne(result)
		})
	return err
}

func runCreateGroups(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "createGroups", "generateGroupMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			name := extractField(item, "name")
			// "sonar-users" is the SonarQube Server built-in default
			// group; the SonarQube Cloud equivalent is "Members" and
			// is managed by SQC. The report's collectSection injects a
			// Skipped row so operators see why it didn't migrate.
			if IsBuiltInGroup(name) {
				return nil
			}
			desc := extractField(item, "description")

			var groupID string
			group, err := e.Cloud.Groups.Create(ctx, cloud.CreateGroupParams{
				Name: name, Description: desc, Organization: orgKey,
			})
			if err != nil {
				if !sqapi.IsAlreadyExists(err) {
					counter.Fail()
					logAPIWarn(e.Logger, "createGroups: create failed", err, "name", name)
					return nil
				}
				e.Logger.Info("createGroups: already exists, looking up", "name", name)
				groupID, err = lookupExistingGroup(ctx, e.Raw, name, orgKey)
				if err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "createGroups: lookup failed", err, "name", name)
					return nil
				}
				counter.Success()
			} else {
				counter.Success()
				groupID = strconv.Itoa(group.ID)
			}

			result := common.EnrichRaw(item, map[string]any{
				"cloud_group_id":     groupID,
				"sonarcloud_org_key": orgKey,
			})
			return w.WriteOne(result)
		})
	return err
}

func runCreatePermissionTemplates(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "createPermissionTemplates", "generateTemplateMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			name := extractField(item, "name")
			desc := extractField(item, "description")
			pattern := extractField(item, "project_key_pattern")
			// Prepend org key to pattern if present.
			if pattern != "" {
				pattern = orgKey + "_" + pattern
			}

			var templateID string
			tpl, err := e.Cloud.Permissions.CreateTemplate(ctx, cloud.CreateTemplateParams{
				Name: name, Description: desc,
				Organization: orgKey, ProjectKeyPattern: pattern,
			})
			if err != nil {
				if !sqapi.IsAlreadyExists(err) {
					counter.Fail()
					logAPIWarn(e.Logger, "createPermissionTemplates: create failed", err, "name", name)
					return nil
				}
				e.Logger.Info("createPermissionTemplates: already exists, looking up", "name", name)
				templateID, err = lookupExistingTemplate(ctx, e.Raw, name, orgKey)
				if err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "createPermissionTemplates: lookup failed", err, "name", name)
					return nil
				}
				counter.Success()
			} else {
				counter.Success()
				templateID = tpl.ID
			}

			result := common.EnrichRaw(item, map[string]any{
				"cloud_template_id":  templateID,
				"sonarcloud_org_key": orgKey,
			})
			return w.WriteOne(result)
		})
	return err
}

func runCreatePortfolios(ctx context.Context, e *Executor) error {
	entID, err := resolveEnterpriseID(e)
	if err != nil {
		return err
	}

	// Pre-fetch every portfolio that already exists in the enterprise so we
	// can resolve duplicates without depending on a specific error code from
	// CreatePortfolio. This is what makes `reset` work on a re-run: the
	// existing-portfolio IDs land in the createPortfolios JSONL and
	// deletePortfolios can read them.
	existingByName, err := loadExistingPortfolioIDs(ctx, e, entID)
	if err != nil {
		e.Logger.Warn("createPortfolios: could not list existing portfolios; duplicate-name re-runs will fail", "err", err)
		existingByName = map[string]string{}
	}

	// Empty portfolios (no resolved projects on the source) are not
	// migrated — there is nothing to populate them with on SQC. The
	// summary report surfaces them in the Skipped bucket via the
	// generatePortfolioMappings + getPortfolioProjects join.
	emptySourceKeys := buildEmptyPortfolioSet(e)

	counter := TaskCounterFromContext(ctx)
	err = forEachMigrateItem(ctx, e, "createPortfolios", "generatePortfolioMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			name := extractField(item, "name")
			desc := extractField(item, "description")
			serverURL := extractField(item, "server_url")
			sourceKey := extractField(item, "source_portfolio_key")
			if emptySourceKeys[serverURL+"|"+sourceKey] {
				e.Logger.Info("createPortfolios: skipping empty source portfolio", "name", name, "source_key", sourceKey)
				return nil
			}

			if existingID, ok := existingByName[name]; ok {
				e.Logger.Info("createPortfolios: already exists, reusing", "name", name, "id", existingID)
				counter.Success()
				result := common.EnrichRaw(item, map[string]any{
					"cloud_portfolio_id": existingID,
				})
				return w.WriteOne(result)
			}

			portfolio, err := e.CloudAPI.Enterprises.CreatePortfolio(ctx, cloud.CreatePortfolioParams{
				EnterpriseID: entID,
				Name:         name,
				Description:  desc,
				Selection:    "projects",
			})
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "createPortfolios: create failed", err, "name", name)
				return nil
			}

			counter.Success()
			result := common.EnrichRaw(item, map[string]any{
				"cloud_portfolio_id": portfolio.ID,
			})
			return w.WriteOne(result)
		})
	return err
}

// loadExistingPortfolioIDs lists every portfolio in the given enterprise and
// returns a name → ID map. The enterprise API has no "create-or-get"
// semantics, so we need this snapshot to recover IDs of portfolios that
// already exist (e.g. during `reset` or a resumed run).
func loadExistingPortfolioIDs(ctx context.Context, e *Executor, entID string) (map[string]string, error) {
	portfolios, err := e.CloudAPI.Enterprises.ListPortfolios(ctx, cloud.ListPortfoliosParams{
		EnterpriseID: entID,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(portfolios))
	for _, p := range portfolios {
		if p.Name == "" || p.ID == "" {
			continue
		}
		out[p.Name] = p.ID
	}
	return out, nil
}

// resolveEnterpriseID reads the getEnterprises task output and returns the UUID
// for the enterprise matching e.EntKey. The API expects the UUID, not the key.
func resolveEnterpriseID(e *Executor) (string, error) {
	items, err := e.Store.ReadAll("getEnterprises")
	if err != nil {
		return "", fmt.Errorf("resolveEnterpriseID: reading getEnterprises: %w", err)
	}
	for _, item := range items {
		// getEnterprises stores the raw API response which is an array of enterprises.
		var enterprises []json.RawMessage
		if json.Unmarshal(item, &enterprises) == nil {
			for _, ent := range enterprises {
				if extractField(ent, "key") == e.EntKey {
					return extractField(ent, "id"), nil
				}
			}
		}
		// Also try as a flat enterprise object.
		if extractField(item, "key") == e.EntKey {
			return extractField(item, "id"), nil
		}
	}
	return "", fmt.Errorf("resolveEnterpriseID: no enterprise found with key %q", e.EntKey)
}

// extractAnyStr extracts a value as string, handling numeric types.
func extractAnyStr(raw json.RawMessage, key string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	val, ok := obj[key]
	if !ok {
		return ""
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(val, &s); err == nil {
		return s
	}
	// Try number.
	var n float64
	if err := json.Unmarshal(val, &n); err == nil {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return ""
}
