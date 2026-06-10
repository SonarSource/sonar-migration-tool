// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/types"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// sonarWayGateName is the canonical name of SonarCloud's built-in
// quality gate. Used as a fallback alongside the IsBuiltIn flag in
// case an API response omits the flag.
const sonarWayGateName = "Sonar way"

// isBuiltInGate reports whether a quality gate is the built-in
// SonarCloud "Sonar way". The IsBuiltIn flag is the source of truth
// when present; the name fallback handles SonarCloud responses that
// omit the flag and accepts the documented variants
// ("Sonar way", "Sonar Way", "Sonar way (built-in)").
func isBuiltInGate(g types.QualityGate) bool {
	if g.IsBuiltIn {
		return true
	}
	return matchesSonarWayName(g.Name)
}

// isBuiltInProfile is the profile analogue of isBuiltInGate. SonarCloud
// reports built-in language profiles with IsBuiltIn=true; the name
// fallback covers responses that omit the flag.
func isBuiltInProfile(p types.QualityProfile) bool {
	if p.IsBuiltIn {
		return true
	}
	return matchesSonarWayName(p.Name)
}

// matchesSonarWayName performs the case-insensitive, trimmed match
// against the canonical built-in name variants. Centralised so the
// gate and profile helpers stay in sync.
func matchesSonarWayName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "sonar way" || n == "sonar way (built-in)"
}

// defaultPermissionTemplateName is the canonical name of the
// permission template every new SonarCloud org ships with. Reset
// uses it to identify the built-in (case-insensitively) so it can
// promote it as default for every qualifier and skip it during the
// delete sweep.
const defaultPermissionTemplateName = "Default Template"

// resetPermissionTemplateQualifiers lists the object qualifiers reset
// promotes the built-in "Default Template" as default for. Mirrors
// the per-qualifier semantics of /api/permissions/set_default_template.
var resetPermissionTemplateQualifiers = []string{"TRK", "VW", "APP"}

// isBuiltInPermissionTemplate matches the built-in "Default Template"
// by name (case-insensitive, trimmed). The SQC search_templates
// response carries no isBuiltIn flag for permission templates, so the
// name is the only signal available.
func isBuiltInPermissionTemplate(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), defaultPermissionTemplateName)
}

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
			Name: "deleteProfiles",
			// deleteProfiles enumerates the org's profiles via the
			// SonarCloud API rather than reading createProfiles'
			// output — issue #214 requires deleting EVERY non-built-in
			// profile, not just those the migration created.
			// resetDefaultProfiles is pinned first so the built-in is
			// the per-language default before any delete call (SQC
			// refuses to delete a language's current default profile).
			Dependencies: []string{"generateOrganizationMappings", "resetDefaultProfiles"},
			Run:          runDeleteProfiles,
		},
		{
			Name: "deleteGates",
			// deleteGates enumerates the org's gates via the SonarCloud
			// API rather than reading createGates' output — issue #213
			// requires deleting EVERY non-built-in gate, not just the
			// ones the migration created. resetDefaultGates is pinned
			// first via the dependency so the built-in is the current
			// default before any destroy call (SonarCloud refuses to
			// destroy the current default).
			Dependencies: []string{"generateOrganizationMappings", "resetDefaultGates"},
			Run:          runDeleteGates,
		},
		{
			// Issue #210: reset doesn't share a run directory with the
			// migrate that produced createGroups, so iterating that
			// JSONL would always come up empty. Enumerate groups via
			// the SQC API per org (same pattern as deleteProfiles /
			// deleteGates) and delete every non-default group. The
			// "Members" default group is preserved.
			Name:         "deleteGroups",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runDeleteGroups,
		},
		{
			Name: "deleteTemplates",
			// deleteTemplates enumerates the org's permission templates
			// via the SonarCloud API rather than reading
			// createPermissionTemplates' output — issue #368 requires
			// deleting EVERY non-built-in template, not just the ones
			// the migration created (otherwise an org reset can leave
			// templates the migration set as default behind, since
			// SQC refuses to delete the current default). The
			// resetPermissionTemplates dependency runs first so the
			// built-in "Default Template" is the current default for
			// every qualifier before any delete call.
			Dependencies: []string{"generateOrganizationMappings", "resetPermissionTemplates"},
			Run:          runDeleteTemplates,
		},
		{
			Name:         "deletePortfolios",
			Editions:     entEditions,
			Dependencies: []string{"createPortfolios"},
			Run:          runDeletePortfolios,
		},
		{
			// Restores the built-in "Sonar way" as each language's
			// default profile before deleteProfiles runs. SonarCloud
			// rejects /api/qualityprofiles/delete on whichever profile
			// is the current per-language default, so without this
			// step the profile that migration (or an admin) promoted
			// to default survives reset. Issue #214.
			Name:         "resetDefaultProfiles",
			Dependencies: []string{"generateOrganizationMappings"},
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
			// Promotes the built-in "Default Template" as the org's
			// default permission template for every qualifier (TRK,
			// VW, APP) before deleteTemplates runs. SonarCloud rejects
			// /api/permissions/delete_template on whichever template
			// is the current default for any qualifier, so without
			// this step the migration-promoted custom default
			// template survives reset. Issue #368.
			Name:         "resetPermissionTemplates",
			Dependencies: []string{"generateOrganizationMappings"},
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
	counter := TaskCounterFromContext(ctx)
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
	return err
}

// runDeleteProfiles enumerates every quality profile in each mapped
// org via /api/qualityprofiles/search and deletes the non-built-in
// ones. Issue #214 requires reset to delete EVERY non-built-in
// profile, not just those the migration created — including profiles
// an admin added manually. resetDefaultProfiles is a dependency, so
// by the time this runs the built-in is the per-language default and
// the previously-default custom profile is deletable.
func runDeleteProfiles(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "deleteProfiles", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			profiles, err := e.Cloud.QualityProfiles.Search(ctx, orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "deleteProfiles: listing profiles failed", err, "org", orgKey)
				return nil
			}
			e.Logger.Debug("deleteProfiles: listed profiles",
				"org", orgKey, "count", len(profiles), "summary", summariseProfiles(profiles))
			for _, p := range profiles {
				if isBuiltInProfile(p) {
					e.Logger.Debug("deleteProfiles: keeping built-in profile",
						"org", orgKey, "profile", p.Name, "language", p.Language)
					continue
				}
				e.Logger.Info("deleteProfiles: deleting non-built-in profile",
					"org", orgKey, "profile", p.Name, "language", p.Language, "isDefault", p.IsDefault)
				if err := e.Cloud.QualityProfiles.Delete(ctx, p.Language, p.Name, orgKey); err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "deleteProfiles failed", err,
						"profile", p.Name, "language", p.Language, "org", orgKey, "isDefault", p.IsDefault)
					continue
				}
				counter.Success()
			}
			return nil
		})
	return err
}

// runDeleteGates enumerates every quality gate in each mapped org via
// /api/qualitygates/list and destroys the non-built-in ones. Issue
// #213 requires reset to delete every non-built-in gate, not just
// those the migration created — including any gates an admin added
// manually. resetDefaultGates is a dependency, so by the time this
// runs the built-in Sonar way is the org's default and the
// previously-default custom gate is destroyable.
func runDeleteGates(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "deleteGates", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			gates, err := e.Cloud.QualityGates.List(ctx, orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "deleteGates: listing gates failed", err, "org", orgKey)
				return nil
			}
			e.Logger.Debug("deleteGates: listed gates",
				"org", orgKey, "count", len(gates), "summary", summariseGates(gates))
			for _, g := range gates {
				if isBuiltInGate(g) {
					e.Logger.Debug("deleteGates: keeping built-in gate",
						"org", orgKey, "gate", g.Name, "gate_id", g.ID)
					continue
				}
				e.Logger.Info("deleteGates: destroying non-built-in gate",
					"org", orgKey, "gate", g.Name, "gate_id", g.ID, "isDefault", g.IsDefault)
				if err := e.Cloud.QualityGates.Destroy(ctx, g.ID, orgKey); err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "deleteGates failed", err,
						"gate", g.Name, "gate_id", g.ID, "org", orgKey, "isDefault", g.IsDefault)
					continue
				}
				counter.Success()
			}
			return nil
		})
	return err
}

// runDeleteGroups enumerates every group in each in-scope SQC org and
// deletes the non-default ones. SQC's per-org "Members" group is the
// only built-in (Default=true) and is preserved. Issue #210.
//
// Previous implementation iterated createGroups JSONL — that worked
// during a migrate run but came up empty during reset because reset
// creates a fresh run directory with no createGroups output of its
// own. Listing via /api/user_groups/search lets reset clean up
// everything the migration created, including the helper
// migration-scanners / migration-viewers groups.
func runDeleteGroups(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "deleteGroups", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			groups, err := e.Cloud.Groups.List(ctx, orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "deleteGroups: listing groups failed", err, "org", orgKey)
				return nil
			}
			e.Logger.Info("deleteGroups: listed groups", "org", orgKey, "count", len(groups))
			for _, g := range groups {
				if g.Default {
					e.Logger.Info("deleteGroups: keeping default group",
						"org", orgKey, "group", g.Name)
					continue
				}
				err := e.Cloud.Groups.DeleteByName(ctx, g.Name, orgKey)
				if err != nil {
					if sqapi.IsNotFound(err) {
						counter.Success()
						continue
					}
					counter.Fail()
					logAPIWarn(e.Logger, "deleteGroups failed", err, "group", g.Name, "org", orgKey)
					continue
				}
				counter.Success()
			}
			return nil
		})
	return err
}

// runDeleteTemplates enumerates every permission template in each
// mapped org via /api/permissions/search_templates and deletes the
// non-built-in ones. Issue #368 requires reset to delete every
// non-built-in template, not just those the migration created.
// resetPermissionTemplates is a dependency, so by the time this runs
// the built-in "Default Template" is the current default for every
// qualifier and any previously-default custom template is deletable.
func runDeleteTemplates(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "deleteTemplates", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			templates, _, err := searchPermissionTemplates(ctx, e, orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "deleteTemplates: listing templates failed", err, "org", orgKey)
				return nil
			}
			e.Logger.Debug("deleteTemplates: listed templates",
				"org", orgKey, "count", len(templates), "summary", summarisePermissionTemplates(templates))
			for _, tpl := range templates {
				if isBuiltInPermissionTemplate(tpl.Name) {
					e.Logger.Debug("deleteTemplates: keeping built-in template",
						"org", orgKey, "template", tpl.Name)
					continue
				}
				e.Logger.Info("deleteTemplates: deleting template",
					"org", orgKey, "template", tpl.Name, "template_id", tpl.ID)
				if err := e.Cloud.Permissions.DeleteTemplate(ctx, tpl.ID, orgKey); err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "deleteTemplates failed", err,
						"template", tpl.Name, "template_id", tpl.ID, "org", orgKey)
					continue
				}
				counter.Success()
			}
			return nil
		})
	return err
}

func runDeletePortfolios(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
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
	counter := TaskCounterFromContext(ctx)
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
	return err
}

// runResetDefaultProfiles restores the built-in "Sonar way" profile
// as each language's default in every mapped org, so deleteProfiles
// can subsequently delete the previously-default custom profile.
// SonarCloud rejects /api/qualityprofiles/delete on whichever profile
// is the current default for a language; without this step the
// migration-promoted custom default profile survives reset.
// Issue #214.
//
// Defaults are per-language. The task groups the org's profiles by
// language, finds the built-in for each language that has a
// non-built-in default, and posts /api/qualityprofiles/set_default
// for that language + built-in profile name.
func runResetDefaultProfiles(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "resetDefaultProfiles", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			profiles, err := e.Cloud.QualityProfiles.Search(ctx, orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "resetDefaultProfiles: listing profiles failed", err, "org", orgKey)
				return nil
			}
			e.Logger.Debug("resetDefaultProfiles: listed profiles",
				"org", orgKey, "count", len(profiles), "summary", summariseProfiles(profiles))

			// Languages whose current default is non-built-in.
			needsRestore := make(map[string]bool)
			// Built-in profile per language (first one wins).
			builtInByLang := make(map[string]types.QualityProfile)
			for _, p := range profiles {
				if p.Language == "" {
					continue
				}
				if isBuiltInProfile(p) {
					if _, seen := builtInByLang[p.Language]; !seen {
						builtInByLang[p.Language] = p
					}
				}
				if p.IsDefault && !isBuiltInProfile(p) {
					needsRestore[p.Language] = true
				}
			}

			for lang := range needsRestore {
				bi, ok := builtInByLang[lang]
				if !ok {
					e.Logger.Warn("resetDefaultProfiles: no built-in profile found for language; deleteProfiles may fail to delete the current default",
						"org", orgKey, "language", lang)
					counter.Fail()
					continue
				}
				e.Logger.Info("resetDefaultProfiles: promoting built-in to default",
					"org", orgKey, "language", lang, "profile", bi.Name)
				if err := e.Cloud.QualityProfiles.SetDefault(ctx, lang, bi.Name, orgKey); err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "resetDefaultProfiles: set_default failed", err,
						"org", orgKey, "language", lang, "profile", bi.Name)
					continue
				}
				counter.Success()
			}
			return nil
		})
	return err
}

// summariseProfiles renders a compact, log-friendly summary of an
// org's profiles. Mirrors summariseGates so operators get the same
// shape across the gate and profile reset paths.
func summariseProfiles(profiles []types.QualityProfile) string {
	parts := make([]string, 0, len(profiles))
	for _, p := range profiles {
		parts = append(parts, fmt.Sprintf("%q [%s] (builtIn=%t, default=%t)",
			p.Name, p.Language, p.IsBuiltIn, p.IsDefault))
	}
	return strings.Join(parts, ", ")
}

// runResetDefaultGates restores the built-in "Sonar way" as each
// mapped org's default quality gate, so deleteGates can subsequently
// destroy whichever custom gate the migration (or the user) had
// promoted to default. SonarCloud's /api/qualitygates/destroy rejects
// the current default; without this step the custom default gate
// survives reset. Issue #213.
func runResetDefaultGates(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
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
			e.Logger.Debug("resetDefaultGates: listed gates",
				"org", orgKey, "count", len(gates), "summary", summariseGates(gates))

			var builtIn *int
			var builtInName string
			for i := range gates {
				if isBuiltInGate(gates[i]) {
					builtIn = &gates[i].ID
					builtInName = gates[i].Name
					if gates[i].IsDefault {
						// Already default — nothing to do.
						e.Logger.Info("resetDefaultGates: built-in is already default",
							"org", orgKey, "gate", builtInName, "gate_id", *builtIn)
						counter.Success()
						return nil
					}
					break
				}
			}
			if builtIn == nil {
				e.Logger.Warn("resetDefaultGates: no built-in gate found in list response; deleteGates may fail to destroy the current default",
					"org", orgKey, "gates_returned", summariseGates(gates))
				counter.Fail()
				return nil
			}
			e.Logger.Info("resetDefaultGates: promoting built-in to default",
				"org", orgKey, "gate", builtInName, "gate_id", *builtIn)
			if err := e.Cloud.QualityGates.SetDefault(ctx, *builtIn, orgKey); err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "resetDefaultGates: set_as_default failed", err, "org", orgKey, "gate_id", *builtIn)
				return nil
			}
			counter.Success()
			return nil
		})
	return err
}

// summariseGates renders a compact, log-friendly summary of an org's
// gates: "<name> (id=N, builtIn=B, default=D)" joined by ", ".
// Used by reset's task logging so an operator can see exactly what
// SonarCloud returned when something goes wrong.
func summariseGates(gates []types.QualityGate) string {
	parts := make([]string, 0, len(gates))
	for _, g := range gates {
		parts = append(parts, fmt.Sprintf("%q (id=%d, builtIn=%t, default=%t)",
			g.Name, g.ID, g.IsBuiltIn, g.IsDefault))
	}
	return strings.Join(parts, ", ")
}

// runResetPermissionTemplates promotes the built-in "Default Template"
// as each mapped org's default permission template for every
// qualifier (TRK, VW, APP), so deleteTemplates can subsequently
// destroy whichever custom template the migration (or the user) had
// promoted to default. SonarCloud's /api/permissions/delete_template
// rejects whichever template is the current default for any
// qualifier; without this step the custom default survives reset.
// Issue #368.
func runResetPermissionTemplates(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "resetPermissionTemplates", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			templates, defaults, err := searchPermissionTemplates(ctx, e, orgKey)
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "resetPermissionTemplates: listing templates failed", err, "org", orgKey)
				return nil
			}
			e.Logger.Debug("resetPermissionTemplates: listed templates",
				"org", orgKey, "count", len(templates), "summary", summarisePermissionTemplates(templates))

			var builtIn *permissionTemplateInfo
			for i := range templates {
				if isBuiltInPermissionTemplate(templates[i].Name) {
					builtIn = &templates[i]
					break
				}
			}
			if builtIn == nil {
				e.Logger.Warn("resetPermissionTemplates: no built-in \"Default Template\" found; deleteTemplates may fail to delete the current default",
					"org", orgKey, "templates_returned", summarisePermissionTemplates(templates))
				counter.Fail()
				return nil
			}

			for _, q := range resetPermissionTemplateQualifiers {
				if defaults[q] == builtIn.ID {
					e.Logger.Info("resetPermissionTemplates: built-in is already default",
						"org", orgKey, "qualifier", q, "template", builtIn.Name, "template_id", builtIn.ID)
					counter.Success()
					continue
				}
				e.Logger.Info("resetPermissionTemplates: promoting built-in to default",
					"org", orgKey, "qualifier", q, "template", builtIn.Name, "template_id", builtIn.ID)
				if err := e.Cloud.Permissions.SetDefaultTemplate(ctx, builtIn.ID, q, orgKey); err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "resetPermissionTemplates: set_default_template failed", err,
						"org", orgKey, "qualifier", q, "template_id", builtIn.ID)
					continue
				}
				counter.Success()
			}
			return nil
		})
	return err
}

// permissionTemplateInfo is the slim subset of /api/permissions/
// search_templates each reset/delete task needs.
type permissionTemplateInfo struct {
	ID   string
	Name string
}

// searchPermissionTemplates fetches and parses both the
// permissionTemplates[] list and the defaultTemplates[] map (keyed
// by qualifier -> templateId) for one org via
// /api/permissions/search_templates.
func searchPermissionTemplates(ctx context.Context, e *Executor, orgKey string) ([]permissionTemplateInfo, map[string]string, error) {
	body, err := e.Raw.Get(ctx, "api/permissions/search_templates", url.Values{"organization": {orgKey}})
	if err != nil {
		return nil, nil, fmt.Errorf("search_templates org=%s: %w", orgKey, err)
	}
	var parsed struct {
		PermissionTemplates []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"permissionTemplates"`
		DefaultTemplates []struct {
			TemplateID string `json:"templateId"`
			Qualifier  string `json:"qualifier"`
		} `json:"defaultTemplates"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("decoding search_templates response for org=%s: %w", orgKey, err)
	}
	templates := make([]permissionTemplateInfo, 0, len(parsed.PermissionTemplates))
	for _, t := range parsed.PermissionTemplates {
		templates = append(templates, permissionTemplateInfo{ID: t.ID, Name: t.Name})
	}
	defaults := make(map[string]string, len(parsed.DefaultTemplates))
	for _, d := range parsed.DefaultTemplates {
		defaults[d.Qualifier] = d.TemplateID
	}
	return templates, defaults, nil
}

// summarisePermissionTemplates renders a compact, log-friendly summary
// of an org's permission templates: "<name> (id=X)" joined by ", ".
func summarisePermissionTemplates(templates []permissionTemplateInfo) string {
	parts := make([]string, 0, len(templates))
	for _, t := range templates {
		parts = append(parts, fmt.Sprintf("%q (id=%s)", t.Name, t.ID))
	}
	return strings.Join(parts, ", ")
}
