package migrate

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// deleteTasks returns tasks for deleting/resetting entities in Cloud.
func deleteTasks() []TaskDef {
	entEditions := []common.Edition{common.EditionEnterprise, common.EditionDatacenter}

	return []TaskDef{
		{
			Name:         "deleteProjects",
			Dependencies: []string{"getCreatedProjects"},
			Run:          runDeleteProjects,
		},
		{
			Name:         "deleteProfiles",
			Dependencies: []string{"createProfiles"},
			Run:          runDeleteProfiles,
		},
		{
			Name: "deleteGates",
			// resetDefaultGates restores the built-in Sonar way as the
			// org's default gate; without that, the custom default
			// can't be destroyed (SonarCloud rejects destroy on the
			// current default). Issue #213.
			Dependencies: []string{"createGates", "resetDefaultGates"},
			Run:          runDeleteGates,
		},
		{
			Name:         "deleteGroups",
			Dependencies: []string{"createGroups"},
			Run:          runDeleteGroups,
		},
		{
			Name:         "deleteTemplates",
			Dependencies: []string{"createPermissionTemplates"},
			Run:          runDeleteTemplates,
		},
		{
			Name:         "deletePortfolios",
			Editions:     entEditions,
			Dependencies: []string{"createPortfolios"},
			Run:          runDeletePortfolios,
		},
		{
			Name:         "resetDefaultProfiles",
			Dependencies: []string{"setDefaultProfiles"},
			Run:          runResetDefaultProfiles,
		},
		{
			// Restores the built-in "Sonar way" as the org's default
			// gate before deleteGates runs. SonarCloud rejects /api/
			// qualitygates/destroy on whichever gate is currently the
			// default, so without this step the gate that was set as
			// default during migration (and any gate the user later
			// promoted to default) survives reset. Issue #213.
			Name:         "resetDefaultGates",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runResetDefaultGates,
		},
		{
			Name:         "resetPermissionTemplates",
			Dependencies: []string{"setDefaultTemplates"},
			Run:          runResetPermissionTemplates,
		},
		{
			// Reverts every org-level setting that has been customized on
			// SonarQube Cloud back to its default. Setting reset is
			// scoped per organization; this task iterates the mapped orgs
			// and resets the union of customized keys in each.
			Name:         "resetGlobalSettings",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runResetGlobalSettings,
		},
	}
}

func runDeleteProjects(ctx context.Context, e *Executor) error {
	counter := NewTaskCounter("deleteProjects")
	err := forEachMigrateItem(ctx, e, "deleteProjects", "getCreatedProjects",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			key := extractField(item, "key")
			if key == "" {
				return nil
			}
			e.Logger.Debug("project api call: POST /api/projects/delete",
				"project", key)
			err := e.Cloud.Projects.Delete(ctx, key)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "deleteProjects failed", err, "key", key)
			} else {
				counter.Success()
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runDeleteProfiles(ctx context.Context, e *Executor) error {
	counter := NewTaskCounter("deleteProfiles")
	err := forEachMigrateItemFiltered(ctx, e, "deleteProfiles", "createProfiles",
		func(item json.RawMessage) bool {
			return !extractBool(item, "is_default")
		},
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			name := extractField(item, "name")
			lang := extractField(item, "language")

			err := e.Cloud.QualityProfiles.Delete(ctx, lang, name, orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "deleteProfiles failed", err, "name", name)
			} else {
				counter.Success()
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runDeleteGates(ctx context.Context, e *Executor) error {
	counter := NewTaskCounter("deleteGates")
	err := forEachMigrateItem(ctx, e, "deleteGates", "createGates",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			gateIDStr := extractField(item, "cloud_gate_id")
			gateName := extractField(item, "name")
			gateID, _ := strconv.Atoi(gateIDStr)

			e.Logger.Debug("gate api call: POST /api/qualitygates/destroy",
				"name", gateName, "gate_id", gateIDStr, "org", orgKey)
			err := e.Cloud.QualityGates.Destroy(ctx, gateID, orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "deleteGates failed", err, "gate", gateIDStr)
			} else {
				counter.Success()
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runDeleteGroups(ctx context.Context, e *Executor) error {
	counter := NewTaskCounter("deleteGroups")
	err := forEachMigrateItem(ctx, e, "deleteGroups", "createGroups",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			groupIDStr := extractField(item, "cloud_group_id")
			groupID, _ := strconv.Atoi(groupIDStr)
			if groupID == 0 {
				return nil
			}
			orgKey := extractField(item, "sonarcloud_org_key")
			err := e.Cloud.Groups.Delete(ctx, groupID, orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "deleteGroups failed", err, "group", groupIDStr)
			} else {
				counter.Success()
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runDeleteTemplates(ctx context.Context, e *Executor) error {
	counter := NewTaskCounter("deleteTemplates")
	err := forEachMigrateItem(ctx, e, "deleteTemplates", "createPermissionTemplates",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			templateID := extractField(item, "cloud_template_id")
			if templateID == "" {
				return nil
			}
			orgKey := extractField(item, "sonarcloud_org_key")
			err := e.Cloud.Permissions.DeleteTemplate(ctx, templateID, orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "deleteTemplates failed", err, "template", templateID)
			} else {
				counter.Success()
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runDeletePortfolios(ctx context.Context, e *Executor) error {
	counter := NewTaskCounter("deletePortfolios")
	err := forEachMigrateItem(ctx, e, "deletePortfolios", "createPortfolios",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			portfolioID := extractField(item, "cloud_portfolio_id")
			if portfolioID == "" {
				return nil
			}
			err := e.CloudAPI.Enterprises.DeletePortfolio(ctx, portfolioID)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "deletePortfolios failed", err, "portfolio", portfolioID)
			} else {
				counter.Success()
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

// runResetGlobalSettings reverts every customized org-level setting on
// SonarQube Cloud back to its default. SQC's /api/settings/values only
// returns keys that have been explicitly customized, so the reset key
// list is naturally bounded — no enumeration of all definitions is
// required. Iteration is per-org from generateOrganizationMappings so
// no upstream create*/generate* dependency is pulled into reset's
// plan.
func runResetGlobalSettings(ctx context.Context, e *Executor) error {
	counter := NewTaskCounter("resetGlobalSettings")
	err := forEachMigrateItem(ctx, e, "resetGlobalSettings", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}

			values, err := e.Cloud.Settings.Values(ctx, "", orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "resetGlobalSettings: listing org settings failed", err, "org", orgKey)
				return nil
			}

			var keys []string
			for _, s := range values {
				// Skip settings that are still at their inherited default
				// — only revert what's been explicitly set at org scope.
				if s.Inherited || s.Key == "" {
					continue
				}
				keys = append(keys, s.Key)
			}
			if len(keys) == 0 {
				counter.Success()
				return nil
			}

			e.Logger.Debug("settings api call: POST /api/settings/reset",
				"org", orgKey, "keys", keys)
			if err := e.Cloud.Settings.Reset(ctx, "", keys, orgKey); err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "resetGlobalSettings: reset failed", err, "org", orgKey, "keys", keys)
				return nil
			}
			counter.Success()
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runResetDefaultProfiles(_ context.Context, e *Executor) error {
	// No-op: Cloud resets defaults when profiles are deleted.
	w, _ := e.Store.Writer("resetDefaultProfiles")
	return w.WriteChunk(nil)
}

// runResetDefaultGates restores the built-in "Sonar way" as each
// mapped org's default quality gate, so deleteGates can subsequently
// destroy whichever custom gate the migration (or the user) had
// promoted to default. SonarCloud's /api/qualitygates/destroy rejects
// the current default; without this step the custom default gate
// survives reset. Issue #213.
func runResetDefaultGates(ctx context.Context, e *Executor) error {
	counter := NewTaskCounter("resetDefaultGates")
	err := forEachMigrateItem(ctx, e, "resetDefaultGates", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			gates, err := e.Cloud.QualityGates.List(ctx, orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "resetDefaultGates: listing gates failed", err, "org", orgKey)
				return nil
			}
			var builtIn *int
			for i := range gates {
				if gates[i].IsBuiltIn {
					builtIn = &gates[i].ID
					if gates[i].IsDefault {
						// Already default — nothing to do.
						counter.Success()
						return nil
					}
					break
				}
			}
			if builtIn == nil {
				e.Logger.Warn("resetDefaultGates: no built-in gate found, custom default may block deleteGates",
					"org", orgKey)
				counter.Fail()
				return nil
			}
			e.Logger.Debug("gate api call: POST /api/qualitygates/set_as_default",
				"org", orgKey, "gate_id", *builtIn)
			if err := e.Cloud.QualityGates.SetDefault(ctx, *builtIn, orgKey); err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "resetDefaultGates: set_as_default failed", err, "org", orgKey)
				return nil
			}
			counter.Success()
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runResetPermissionTemplates(_ context.Context, e *Executor) error {
	// No-op: Cloud resets defaults when templates are deleted.
	w, _ := e.Store.Writer("resetPermissionTemplates")
	return w.WriteChunk(nil)
}
