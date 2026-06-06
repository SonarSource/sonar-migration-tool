// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
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

func runDeleteTemplates(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
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

func runResetPermissionTemplates(_ context.Context, e *Executor) error {
	// No-op: Cloud resets defaults when templates are deleted.
	w, _ := e.Store.Writer("resetPermissionTemplates")
	return w.WriteChunk(nil)
}
