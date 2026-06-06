// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/types"
	"golang.org/x/sync/errgroup"
)

// exclusionPair captures a "global vs local" SQS pair that has no
// direct SQC equivalent for the global half — the migrate task folds the
// global patterns into the local key before posting.
//
// Today this covers:
//
//   - sonar.global.exclusions      → sonar.exclusions
//   - sonar.global.test.exclusions → sonar.test.exclusions
//
// More pairs can be added without touching the merge or detail-render
// logic; just append to globalExclusionPairs.
type exclusionPair struct {
	// SQSGlobalKey is the SQS-side key with no SQC counterpart. Its
	// patterns are moved onto SQSLocalKey before migration.
	SQSGlobalKey string
	// SQSLocalKey is the SQS key that DOES have a SQC counterpart
	// (same name on both sides). After the merge it carries the
	// union of patterns from both sources.
	SQSLocalKey string
}

// sqsOnlyDecision tells partitionSQSOnlySettings what to do with one
// SQS setting that has no SonarQube Cloud equivalent.
//
//   - SkipSilently=true: drop the record entirely, no API call, no
//     report row.
//   - SkipSilently=false: drop from the API loop AND emit one
//     section-level note row at the bottom of the Global Settings
//     report section. Note carries the wording the report displays.
type sqsOnlyDecision struct {
	SkipSilently bool
	Note         string
}

// sqsOnlySettings names the SQS global settings that have no SQC
// counterpart at any scope. The migration loop short-circuits them
// before any API call so they never produce "not-on-sqc" Warns or
// settings.set failures, and the report flags non-default values as
// Skipped with a clear "setting does not exist on SonarQube Cloud"
// explanation (#240).
//
// Decision functions receive the raw getServerSettings record so they
// can inspect value / values / parentValue and decide between
// silent-skip (default value, nothing to say) and emit-a-note (the
// user has chosen this SQS-only feature; surface it in the report).
//
// The list is curated, not exhaustive. Settings outside this list that
// still happen to have no SQC counterpart go through the dynamic
// discovery path in applyOneGlobalSetting and surface as "not-on-sqc"
// at runtime.
var sqsOnlySettings = map[string]func(raw json.RawMessage) sqsOnlyDecision{
	// Silent-skip entries: keys that have no SQC counterpart AND have
	// no operator-meaningful value to report on (read-only server
	// metadata, bundled analyzer plugin manifest, announcement banners,
	// licensing thresholds, etc.). Real-migrate's partition strips
	// these before any API call; predict consults
	// IsSilentlySkippedGlobalSetting to mirror that and keep the two
	// reports identical.
	"sonar.core.id":                                           silentSkip, // internal server identity
	"sonar.core.serverBaseURL":                                silentSkip,
	"sonar.core.startTime":                                    silentSkip, // read-only server timestamp
	"sonar.builtInQualityProfiles.disableNotificationOnUpdate": silentSkip,
	"sonar.announcement.htmlMessage":                          silentSkip,
	"sonar.announcement.message":                              silentSkip,
	"sonar.cfamily.generateComputedConfig":                    silentSkip,
	"sonar.documentation.baseUrl":                             silentSkip,
	"sonar.login.displayMessage":                              silentSkip,
	"sonar.login.message":                                     silentSkip,
	"sonar.license.notifications.remainingLocThreshold":       silentSkip,
	"sonar.mcp.healthCheckInterval":                           silentSkip,
	"sonar.plugins.risk.consent":                              silentSkip,
	// Bundled analyzer plugin manifest fields — server-emitted, not
	// user-set, never portable. The sonar.cs.analyzer.* family is
	// covered by the prefix list below; remaining vbnet keys stay
	// explicit until the user requests a similar prefix.
	"sonar.vbnet.analyzer.dotnet.pluginKey":            silentSkip,
	"sonar.vbnet.analyzer.dotnet.pluginVersion":        silentSkip,
	"sonar.vbnet.analyzer.dotnet.staticResourceName":   silentSkip,
	"sonar.vbnet.analyzer.security.pluginKey":          silentSkip,
	"sonar.vbnet.analyzer.security.pluginVersion":      silentSkip,
	"sonar.vbnet.analyzer.security.staticResourceName": silentSkip,
	// Server-side JDBC driver class — internal SQS plumbing, not a
	// portable setting (no JDBC on SQC).
	"sonar.plsql.jdbc.driver.class": silentSkip,

	"sonar.qualityProfiles.allowDisableInheritedRules": func(raw json.RawMessage) sqsOnlyDecision {
		// Only worth mentioning when the SQS-side operator
		// explicitly enabled the feature; the default ("false") is
		// the same as "doesn't exist", so no note then.
		if extractField(raw, "value") == "true" {
			return sqsOnlyDecision{Note: sqsOnlyNoteText}
		}
		return sqsOnlyDecision{SkipSilently: true}
	},
	"sonar.technicalDebt.ratingGrid": func(raw json.RawMessage) sqsOnlyDecision {
		// SonarQube Cloud always uses the platform default for the
		// rating grid; the SQS-side value is dropped on migration.
		// Surface ONLY when the operator customised it away from the
		// SQS default, then emit the canonical "not on SQC" Skipped
		// note. The exact original/replacement values are kept in
		// the run log, not in the per-row Detail.
		const ratingGridDefault = "0.05,0.1,0.2,0.5"
		v := extractField(raw, "value")
		if v == "" || v == ratingGridDefault {
			return sqsOnlyDecision{SkipSilently: true}
		}
		return sqsOnlyDecision{Note: skipDetailNotOnSQC}
	},
	// Note: sonar.dbcleaner.branchesToKeepWhenInactive is intentionally
	// NOT in this curated map — #249 implements the actual migration
	// path (combine SQS values into a regex, POST to SQC org-scope
	// sonar.branch.longLivedBranches.regex), so the per-key handler
	// in runSetGlobalSettings owns it now.

	"sonar.allowPermissionManagementForProjectAdmins": func(raw json.RawMessage) sqsOnlyDecision {
		// SQC has no equivalent feature flag; the SQS default is
		// "false" (don't delegate permission management to project
		// admins). Only surface non-default values.
		if extractField(raw, "value") == "true" {
			return sqsOnlyDecision{Note: sqsOnlyNoteText}
		}
		return sqsOnlyDecision{SkipSilently: true}
	},
	"sonar.issues.sandbox.enabled":          sandboxBooleanDecision,
	"sonar.issues.sandbox.override.enabled": sandboxBooleanDecision,
	"sonar.issues.sandbox.default": func(raw json.RawMessage) sqsOnlyDecision {
		v := extractField(raw, "value")
		if v == "" || v == "false" {
			return sqsOnlyDecision{SkipSilently: true}
		}
		return sqsOnlyDecision{Note: sqsOnlyNoteText}
	},
	"sonar.issues.sandbox.software-qualities": func(raw json.RawMessage) sqsOnlyDecision {
		if extractField(raw, "value") == "" {
			return sqsOnlyDecision{SkipSilently: true}
		}
		return sqsOnlyDecision{Note: sqsOnlyNoteText}
	},

	// Features that are *always* enabled on SonarQube Cloud — surface a
	// FYI note whenever the SQS-side operator has touched the flag,
	// regardless of value. SQC controls the on/off itself.
	"sonar.architecture.visualization.enabled": alwaysEnabledOnSQC,
	"sonar.mcp.enabled":                        alwaysEnabledOnSQC,
	"sonar.misracompliance.enabled":            alwaysEnabledOnSQC,
	"sonar.sca.featureEnabled":                 alwaysEnabledOnSQC,
}

// sqsOnlyPrefixes is the prefix-based fallback when the exact-match
// sqsOnlySettings map doesn't classify a key. Used for whole families:
//
//   - sonaranalyzer-cs.*, sonaranalyzer-vbnet.*, sonar.updatecenter.*
//     → silently dropped (internal SQS metadata, never user-set).
//   - sonar.auth.*
//     → surfaced in the report as Skipped with an auth-specific
//     note. Customers must re-configure authentication on SQC; it's
//     not portable, but worth calling out so they don't miss it.
var sqsOnlyPrefixes = []struct {
	Prefix  string
	Handler func(raw json.RawMessage) sqsOnlyDecision
}{
	{"sonar.cs.analyzer.", silentSkip},
	{"sonaranalyzer-cs.", silentSkip},
	{"sonaranalyzer-vbnet.", silentSkip},
	{"sonaranalyzer.security.cs.", silentSkip},
	{"sonar.updatecenter.", silentSkip},
	{"sonar.auth.", authReconfigureNote},
}

// authReconfigureNote surfaces a sonar.auth.* setting in the report
// as Skipped/not-on-SQC when it carries any value. Empty / unset auth
// keys stay silent.
func authReconfigureNote(raw json.RawMessage) sqsOnlyDecision {
	v := extractField(raw, "value")
	vs := extractStringArray(raw, "values")
	if v == "" && len(vs) == 0 {
		return sqsOnlyDecision{SkipSilently: true}
	}
	return sqsOnlyDecision{Note: skipDetailNotOnSQC}
}

// Two canonical Detail strings for every Skipped global setting row
// in the migration report — one per skip reason. Per #244 follow-up,
// every per-key handler must surface ONE of these two so operators
// see consistent wording across the section. Per-setting nuance
// (auth-reconfiguration, ratingGrid value substitution, the dbcleaner
// regex transformation) is captured in the run log instead.
const (
	skipDetailDefaultValue = "Setting is left to default on SQS, no migration needed"
	skipDetailNotOnSQC     = "Setting does not exist (feature does not exist or setting irrelevant) on SQC, no migration possible"

	// Exported aliases — the predict pipeline consumes these so the
	// real and predictive reports share the exact same wording.
	SkipDetailDefaultValue = skipDetailDefaultValue
	SkipDetailNotOnSQC     = skipDetailNotOnSQC
)

// sqsOnlyNoteText kept for backward compatibility with existing
// callers; it now resolves to the canonical "not on SQC" Detail.
const sqsOnlyNoteText = skipDetailNotOnSQC

// sandboxBooleanDecision is shared between sonar.issues.sandbox.enabled
// and sonar.issues.sandbox.override.enabled, which both default to
// "false" on SQS. Anything other than "false" / "" surfaces in the
// report as SQS-only.
func sandboxBooleanDecision(raw json.RawMessage) sqsOnlyDecision {
	v := extractField(raw, "value")
	if v == "" || v == "false" {
		return sqsOnlyDecision{SkipSilently: true}
	}
	return sqsOnlyDecision{Note: sqsOnlyNoteText}
}

// silentSkip is the trivial decision-function for keys that are always
// silently dropped — read-only server metadata, bundled plugin
// manifest fields, announcement / login banner text, licensing
// thresholds, etc. Shared so the curated map stays a one-line lookup.
func silentSkip(_ json.RawMessage) sqsOnlyDecision {
	return sqsOnlyDecision{SkipSilently: true}
}

// alwaysEnabledOnSQC emits the canonical "not on SQC" Skipped note in
// the report for SQS feature flags that SonarQube Cloud has
// permanently turned on. SQC controls the on/off itself, so there's
// nothing to migrate — the operator just sees "setting irrelevant on
// SQC" via the unified wording.
func alwaysEnabledOnSQC(_ json.RawMessage) sqsOnlyDecision {
	return sqsOnlyDecision{Note: skipDetailNotOnSQC}
}

// resolveSQSOnlyHandler looks up the per-key decision function for a
// SonarQube Server global setting, consulting both the exact-match
// sqsOnlySettings map and the prefix-based sqsOnlyPrefixes fallback.
// Returns (nil, false) when the key is not classified by the curated
// rules — in which case real-migrate falls back to dynamic discovery
// and predict treats the key as Applied (predicted).
func resolveSQSOnlyHandler(key string) (func(raw json.RawMessage) sqsOnlyDecision, bool) {
	if h, ok := sqsOnlySettings[key]; ok {
		return h, true
	}
	for _, p := range sqsOnlyPrefixes {
		if strings.HasPrefix(key, p.Prefix) {
			return p.Handler, true
		}
	}
	return nil, false
}

// EvaluateSQSOnlyGlobalSetting consults the curated sqsOnlySettings list
// for the given raw getServerSettings record and reports whether the
// key (i) is in the list AND (ii) is currently at a non-default value
// worth surfacing in the migration report. The returned note is the
// user-facing wording the report should display in the Detail column.
//
// Exported so the predictive-report pipeline (internal/predict) can
// apply the same SQS-only classification without duplicating the per-
// key value-vs-default rules.
//
// Returns isSQSOnly=false for keys that are silently skipped (e.g.
// read-only server timestamps); callers should additionally consult
// IsSilentlySkippedGlobalSetting to suppress those from the report.
func EvaluateSQSOnlyGlobalSetting(key string, raw json.RawMessage) (note string, isSQSOnly bool) {
	handler, ok := resolveSQSOnlyHandler(key)
	if !ok {
		return "", false
	}
	decision := handler(raw)
	if decision.SkipSilently {
		return "", false
	}
	return decision.Note, true
}

// IsSilentlySkippedGlobalSetting reports whether the curated list
// classifies this (key, value) as "drop from the report entirely" —
// e.g. read-only server metadata like sonar.core.startTime, or
// SQS-only feature flags whose value matches SQS's default.
//
// Real-migrate's partitionSQSOnlySettings already strips these out
// before any API call. The predictive-report pipeline consults this
// function to keep its output consistent with the real-migrate report.
func IsSilentlySkippedGlobalSetting(key string, raw json.RawMessage) bool {
	handler, ok := resolveSQSOnlyHandler(key)
	if !ok {
		return false
	}
	return handler(raw).SkipSilently
}

// partitionSQSOnlySettings splits the raw SQS getServerSettings list,
// removing any key that's known to have no SQC counterpart (issue
// #200). The function is called BEFORE the "customized vs default"
// filter so the per-key handlers can decide based on the raw value
// even when that value happens to match SQS's default (e.g.
// sonar.qualityProfiles.allowDisableInheritedRules=true, which is
// SQS's default but still wants a report note).
//
// Keys returned in `notes` carry one synthetic outcome with Org=""
// so the report renders them once at the bottom of the section, NOT
// per-org.
func partitionSQSOnlySettings(values []json.RawMessage) (remaining []json.RawMessage, notes []globalSettingResult) {
	remaining = values[:0]
	for _, raw := range values {
		key := extractField(raw, "key")
		handler, isSQSOnly := resolveSQSOnlyHandler(key)
		if !isSQSOnly {
			remaining = append(remaining, raw)
			continue
		}
		decision := handler(raw)
		if decision.SkipSilently {
			continue
		}
		rec := globalSettingResult{Key: key}
		rec.Value, rec.Values, rec.FieldValues = readSettingPayload(raw)
		rec.Outcomes = []orgOutcome{{
			Org:    "",
			Status: outcomeSkipped,
			Reason: skipReasonSQSOnlyValue,
			Detail: decision.Note,
		}}
		notes = append(notes, rec)
	}
	return remaining, notes
}

// skipReasonSQSOnlyValue is the reason marker placed on per-section
// note rows so the report can both group them under the right label
// and order them last in the Skipped bucket. Mirrors the constant
// exported from the report package — kept here too to avoid a
// migrate -> report dependency direction reversal.
const skipReasonSQSOnlyValue = "sqs-only"

var globalExclusionPairs = []exclusionPair{
	{SQSGlobalKey: "sonar.global.exclusions", SQSLocalKey: "sonar.exclusions"},
	{SQSGlobalKey: "sonar.global.test.exclusions", SQSLocalKey: "sonar.test.exclusions"},
}

// runSetGlobalSettings migrates customized SQS-side global settings to
// every SonarQube Cloud organization in scope (issue #186).
//
// Pipeline:
//
//  1. Read getServerSettingsDefinitions to learn each SQS setting's
//     defaultValue (and shape) — that's how we detect "customized".
//  2. Read getServerSettings (raw values) and filter out any setting
//     whose value equals the SQS default — uncustomized settings are
//     skipped entirely.
//  3. Read generateOrganizationMappings and collect every target
//     sonarcloud_org_key that isn't empty / SKIPPED.
//  4. For each org, fetch SQC's list_definitions once (cached) so we know
//     which keys actually exist on the target and what shape (single /
//     multi / property-set) they expect.
//  5. For each (customized SQS setting × target SQC org):
//     – not in SQC's defs → log Warn, record skipped(reason=not-on-sqc).
//     – in SQC's defs → dispatch via applySettingByDef (the same helper
//     that drives setProjectSettings, but with empty projectKey so the
//     SDK scopes the request to the organization).
//  6. Emit one JSONL record per setting key, with applied / failed /
//     skipped org lists plus a pre-built "detail" string that the
//     summary report renders verbatim.
func runSetGlobalSettings(ctx context.Context, e *Executor) error {
	// getServerSettings and getServerSettingsDefinitions are EXTRACT
	// tasks — their output lives in the per-server extract directories
	// (one level above the migrate run dir) and is reached via
	// readExtractItems / e.Mapping. Using e.Store.ReadAll here would
	// silently return zero records and the task would no-op.
	sqsDefItems, err := readExtractItems(e, "getServerSettingsDefinitions")
	if err != nil {
		return fmt.Errorf("setGlobalSettings: reading getServerSettingsDefinitions: %w", err)
	}
	sqsDefaultByKey := make(map[string]string, len(sqsDefItems))
	for _, d := range sqsDefItems {
		k := extractField(d.Data, "key")
		if k == "" {
			continue
		}
		sqsDefaultByKey[k] = extractField(d.Data, "defaultValue")
	}

	// Raw SQS global settings — kept only when customized.
	sqsItems, err := readExtractItems(e, "getServerSettings")
	if err != nil {
		return fmt.Errorf("setGlobalSettings: reading getServerSettings: %w", err)
	}
	sqsValues := make([]json.RawMessage, 0, len(sqsItems))
	for _, it := range sqsItems {
		sqsValues = append(sqsValues, it.Data)
	}

	// Issue #200 — partition off the SQS-only settings BEFORE the
	// customized filter. For these keys the per-handler logic decides
	// what to do based on the raw value, not on whether the value
	// differs from SQS's default. The previous order (customized
	// filter first, then partition) hid keys whose value happened to
	// equal SQS's default — e.g. sonar.qualityProfiles.allowDisable
	// InheritedRules=true where SQS's parentValue is also true: the
	// customized filter dropped it, and the section-level note for
	// "exists only on SQS" never reached the report.
	// Drop internal settings (#244) before anything else so they never
	// reach the partition / customized-filter / per-org loops.
	filtered := sqsValues[:0]
	for _, raw := range sqsValues {
		if IsInternalSqsSetting(extractField(raw, "key")) {
			continue
		}
		filtered = append(filtered, raw)
	}
	sqsValues = filtered

	sqsValues, sqsOnlyNotes := partitionSQSOnlySettings(sqsValues)

	customized := make([]json.RawMessage, 0, len(sqsValues))
	for _, raw := range sqsValues {
		key := extractField(raw, "key")
		if key == "" {
			continue
		}
		if !IsSettingCustomized(raw, sqsDefaultByKey[key]) {
			continue
		}
		customized = append(customized, raw)
	}

	// Peel off sonar.dbcleaner.branchesToKeepWhenInactive — #249
	// migrates it as a regex into the SQC org-scope setting
	// sonar.branch.longLivedBranches.regex, not via the standard
	// per-org applyOneGlobalSetting path. Handled below after the
	// parallel loop.
	var dbCleanerRaw json.RawMessage
	dbCleanerRaw, customized = extractDbCleanerBranches(customized)

	// Peel off the AI Code Fix settings — issue #251. They drive a
	// dedicated SQC endpoint (PATCH /fix-suggestions/organization-configs)
	// with strategy-dependent enablement and provider/model values; the
	// standard /api/settings/set path on SQC has no equivalent key.
	_, customized = extractAiCodeFixHidden(customized)
	customized = extractAiCodeFixSuggestions(customized)

	// SQS exposes platform-enforced exclusion patterns via the
	// sonar.global.* keys (today: sonar.global.exclusions and
	// sonar.global.test.exclusions). SQC has no global counterparts,
	// so the migrate task folds each global key's patterns into its
	// non-global sibling (sonar.exclusions / sonar.test.exclusions)
	// and drops the global side from the migration list — otherwise
	// a "not-on-sqc" Warn would fire and the platform-enforced
	// patterns would be silently lost.
	for _, pair := range globalExclusionPairs {
		customized = mergeGlobalIntoLocal(pair, sqsValues, customized, e.Logger)
	}

	// Target SQC orgs.
	orgItems, _ := e.Store.ReadAll("generateOrganizationMappings")
	orgs := make(map[string]struct{})
	orgList := make([]string, 0, len(orgItems))
	for _, o := range orgItems {
		orgKey := extractField(o, "sonarcloud_org_key")
		if shouldSkipOrg(orgKey) {
			continue
		}
		if _, dup := orgs[orgKey]; dup {
			continue
		}
		orgs[orgKey] = struct{}{}
		orgList = append(orgList, orgKey)
	}
	sort.Strings(orgList)

	e.Logger.Info("starting task", "task", "setGlobalSettings",
		"customized_settings", len(customized), "target_orgs", len(orgList))

	// One list_definitions fetch per target org (org scope).
	defsByOrg := loadSettingDefinitionsForOrgs(ctx, e, orgs, "setGlobalSettings")

	// Project-scope defs cover the superset of keys visible to a
	// project (language settings, external-analyzer settings, etc.).
	// Used below to distinguish "truly not on SQC" (warn) from "exists
	// at project scope only — handled by setProjectSettings" (info).
	// Issues #189 / #191.
	projects, _ := e.Store.ReadAll("createProjects")
	projectKeyMap := make(map[string]projectMapping, len(projects))
	for _, p := range projects {
		serverURL := extractField(p, "server_url")
		key := extractField(p, "key")
		projectKeyMap[serverURL+key] = projectMapping{
			CloudKey: extractField(p, "cloud_project_key"),
			OrgKey:   extractField(p, "sonarcloud_org_key"),
		}
	}
	projectDefsByOrg := loadProjectScopedSettingDefinitionsForOrgs(ctx, e, projectKeyMap, "setGlobalSettings")

	// Read getProjectSettings extract so the fan-out (below) knows
	// which (project, key) pairs already have a per-project SQS
	// override. setProjectSettings's per-record loop applies those in
	// parallel with this task; without the coverage map the fan-out
	// would race against — and potentially overwrite — those values.
	overrideCovered := buildPerProjectOverrideCoverage(e)

	counter := TaskCounterFromContext(ctx)
	w, err := e.Store.Writer("setGlobalSettings")
	if err != nil {
		return err
	}

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))
	for _, raw := range customized {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			rec := applyOneGlobalSetting(gctx, e, raw, orgList, defsByOrg, projectDefsByOrg, projectKeyMap, overrideCovered, counter)
			b, _ := json.Marshal(rec)
			mu.Lock()
			defer mu.Unlock()
			return w.WriteOne(b)
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	// #249: migrate sonar.dbcleaner.branchesToKeepWhenInactive as a
	// regex to the SQC org-scope sonar.branch.longLivedBranches.regex
	// setting. Done outside the parallel loop because the source key
	// itself does not exist on SQC — the apply-via-list_definitions
	// path would just mark it Skipped/not-on-sqc.
	applyDbCleanerBranchesGlobal(ctx, e, dbCleanerRaw, orgList, w, &mu, counter)

	// #251: migrate AI Code Fix configuration to SQC via the dedicated
	// fix-suggestions org-config PATCH endpoint. Reads two extracts
	// (getServerSettings for the hidden flag, getAiCodeFixConfig for
	// enablement + provider + model + projects), evaluates the per-
	// source strategy, and PATCHes each target SQC org.
	if sourceSettings, err := readExtractItems(e, "getServerSettings"); err == nil {
		aiConfigs, _ := readExtractItems(e, "getAiCodeFixConfig")
		states := LoadAiCodeFixStates(sourceSettings, aiConfigs)
		if len(states) > 0 {
			decisions := make([]AiCodeFixDecision, 0, len(states))
			for _, s := range states {
				decisions = append(decisions, EvaluateAiCodeFix(s))
			}
			applyAiCodeFixDecisions(ctx, e, decisions, orgList, projectKeyMap, w, &mu, counter)
		}
	}

	// Section-level notes for SQS-only settings (issue #200).
	// Written AFTER the parallel loop so they always trail the
	// regular per-org rows in the report's Skipped bucket — and
	// because the writer is shared, the mutex still applies.
	for _, rec := range sqsOnlyNotes {
		b, _ := json.Marshal(rec)
		mu.Lock()
		_ = w.WriteOne(b)
		mu.Unlock()
	}

	// Default-value sweep (#244). Every setting in
	// getServerSettingsDefinitions that didn't reach the main loop —
	// because it was at the SQS default value or never customised —
	// gets a single section-level Skipped row so the operator sees
	// the inventory of "untouched" settings. Internal keys (sonar-
	// tools _SQ_INTERNAL_SETTINGS port) and keys already classified
	// by partitionSQSOnlySettings are excluded.
	customizedKeys := make(map[string]bool, len(customized))
	for _, raw := range customized {
		if k := extractField(raw, "key"); k != "" {
			customizedKeys[k] = true
		}
	}
	partitionHandled := make(map[string]bool, len(sqsOnlyNotes))
	for _, note := range sqsOnlyNotes {
		partitionHandled[note.Key] = true
	}
	for _, def := range sqsDefItems {
		key := extractField(def.Data, "key")
		if key == "" {
			continue
		}
		if IsInternalSqsSetting(key) {
			continue
		}
		if customizedKeys[key] || partitionHandled[key] {
			continue
		}
		// AI Code Fix keys are owned by applyAiCodeFixDecisions
		// above — skip the default-value sweep so they don't
		// double-report (issue #251).
		if key == AiCodeFixHiddenSetting || key == AiCodeFixSuggestionsSetting {
			continue
		}
		// Already-curated SQS-only keys (sqsOnlySettings map +
		// sqsOnlyPrefixes) are handled by partitionSQSOnlySettings
		// when their value is non-default. When their value is at
		// default, the partition's per-key handler emits SkipSilently
		// — honour that here too so we don't double-report them.
		if _, found := resolveSQSOnlyHandler(key); found {
			continue
		}
		rec := globalSettingResult{Key: key}
		rec.Outcomes = []orgOutcome{{
			Org:    "",
			Status: outcomeSkipped,
			Reason: "default-value",
			Detail: skipDetailDefaultValue,
		}}
		b, _ := json.Marshal(rec)
		mu.Lock()
		_ = w.WriteOne(b)
		mu.Unlock()
	}

	return nil
}

// applyOneGlobalSetting applies a single customized SQS global setting to
// every target SQC org and returns a result record describing the per-org
// outcomes plus a pre-built detail string for the report.
func applyOneGlobalSetting(ctx context.Context, e *Executor, raw json.RawMessage, orgs []string,
	defsByOrg, projectDefsByOrg map[string]map[string]types.SettingDefinition,
	projectKeyMap map[string]projectMapping,
	overrideCovered map[string]map[string]bool,
	counter *TaskCounter) globalSettingResult {

	key := extractField(raw, "key")
	rec := globalSettingResult{Key: key}
	rec.Value, rec.Values, rec.FieldValues = readSettingPayload(raw)
	// Carry the merge provenance through to the output record so the
	// report can call out which SQS-global key contributed patterns to
	// this SQC-side key (e.g. sonar.global.test.exclusions for the
	// sonar.test.exclusions record).
	rec.MergedFromGlobal = extractField(raw, "_merged_from")

	// Memoize the "SQC rejects this key at org level" verdict across
	// the orgs we iterate below. Without this, every org would try
	// the same failing org-scope POST before falling back; with it,
	// we try at most ONCE and reuse the result for the remaining
	// orgs. The verdict is per-key but cached across orgs because
	// SQC's list_definitions inconsistency is a property of the
	// setting key, not of the org.
	orgRejected := false

	valueSummary := renderValueSummary(rec)
	mergeSuffix := ""
	if rec.MergedFromGlobal != "" {
		mergeSuffix = fmt.Sprintf(" (merged from %s + %s)", rec.MergedFromGlobal, key)
	}

	for _, org := range orgs {
		def, hasDef := defsByOrg[org][key]
		if !hasDef {
			// Distinguish two cases here (issues #189 / #191):
			//   - key NOT in org-scope and NOT in project-scope → truly
			//     unknown to SQC; keep the existing Warn.
			//   - key NOT in org-scope BUT IS in project-scope →
			//     handled by setProjectSettings's propagation pass;
			//     downgrade to Info and use a non-alarming reason so
			//     the report doesn't flag it red.
			if _, atProject := projectDefsByOrg[org][key]; atProject {
				e.Logger.Info("setGlobalSettings: key exists only at SQC project scope, will be propagated by setProjectSettings",
					"key", key, "org", org)
				rec.Outcomes = append(rec.Outcomes, orgOutcome{
					Org:    org,
					Status: outcomeSkipped,
					Reason: "project-scope-only",
					Detail: "Setting does not exist at global org level on SonarQube Cloud; was applied for each project instead." + mergeSuffix,
				})
				continue
			}
			e.Logger.Warn("setGlobalSettings: setting key not available on SQC, skipping",
				"key", key, "org", org)
			rec.Outcomes = append(rec.Outcomes, orgOutcome{
				Org:    org,
				Status: outcomeSkipped,
				Reason: "not-on-sqc",
				Detail: "Skipped (not on SQC)" + mergeSuffix,
			})
			continue
		}
		// If a previous org for THIS key already rejected the
		// org-scope POST, skip the org attempt entirely and go
		// straight to the project fan-out. Saves one wasted 400 per
		// extra org.
		if orgRejected {
			rec.Outcomes = append(rec.Outcomes,
				fanOutOutcome(ctx, e, raw, key, org, valueSummary, mergeSuffix, projectDefsByOrg, projectKeyMap, overrideCovered, counter, /*alreadyKnown=*/ true))
			continue
		}

		err := applySettingByDef(ctx, e, "", org, raw, key, def, true)
		switch {
		case errors.Is(err, errSettingEmpty):
			rec.Outcomes = append(rec.Outcomes, orgOutcome{
				Org: org, Status: outcomeSkipped, Reason: "empty",
				Detail: "Skipped (empty payload)" + mergeSuffix,
			})
		case sqapi.IsOrgLevelRejection(err):
			// SQC's list_definitions falsely reported this key as
			// settable at org scope (e.g. sonar.coverage.jacoco.xmlReportPaths,
			// sonar.androidLint.reportPaths). Fall back to setting the
			// value on each project in that org. Mark orgRejected so
			// the remaining orgs in the loop skip the failing org-
			// scope POST altogether.
			orgRejected = true
			rec.Outcomes = append(rec.Outcomes,
				fanOutOutcome(ctx, e, raw, key, org, valueSummary, mergeSuffix, projectDefsByOrg, projectKeyMap, overrideCovered, counter, /*alreadyKnown=*/ false))
		case err != nil:
			counter.Fail()
			logAPIWarn(e.Logger, "setGlobalSettings failed", err, "key", key, "org", org)
			rec.Outcomes = append(rec.Outcomes, orgOutcome{
				Org: org, Status: outcomeFailed, Reason: err.Error(),
				Detail: "Failed: " + apiErrMessage(err) + mergeSuffix,
			})
		default:
			counter.Success()
			rec.Outcomes = append(rec.Outcomes, orgOutcome{
				Org: org, Status: outcomeApplied,
				Detail: "Applied " + valueSummary + mergeSuffix,
			})
		}
	}
	return rec
}

// renderValueSummary returns the "value=<bold>X</bold>" fragment used
// inside per-row Detail strings ("Applied value=X", "Applied value=X
// to all projects", …). The value portion is wrapped in
// InlineBoldStart / InlineBoldEnd so the PDF renderer can stress it.
// Multi-value lists are joined by comma without brackets, mirroring
// the CSV form the SQC API actually expects. Property-set settings
// stay in their JSON-encoded form since collapsing them into a
// comma list would lose structure.
func renderValueSummary(r globalSettingResult) string {
	var value string
	switch {
	case len(r.FieldValues) > 0:
		b, _ := json.Marshal(r.FieldValues)
		value = string(b)
	case len(r.Values) > 0:
		value = strings.Join(r.Values, ",")
	default:
		value = r.Value
	}
	return "value=" + InlineBoldStart + value + InlineBoldEnd
}

// apiErrMessage extracts the API error message when err is an
// sqapi.APIError, falling back to err.Error() otherwise. Keeps the
// report's Detail text compact (no Method/URL noise).
func apiErrMessage(err error) string {
	var apiErr *sqapi.APIError
	if errors.As(err, &apiErr) {
		if msg := apiErr.Message(); msg != "" {
			return msg
		}
	}
	return err.Error()
}

// fanOutOutcome handles the per-org fan-out when SQC rejected the
// org-scope POST for a key (or when we've already seen that rejection
// on a previous org during this run). Per-project SQS overrides are
// skipped so we don't race against the parallel setProjectSettings
// per-record loop. Returns a single orgOutcome with a Detail string
// that summarises the apply ("Applied to all projects (value=…)") and
// enumerates any project-level exceptions.
func fanOutOutcome(ctx context.Context, e *Executor, raw json.RawMessage,
	key, org, valueSummary, mergeSuffix string,
	projectDefsByOrg map[string]map[string]types.SettingDefinition,
	projectKeyMap map[string]projectMapping,
	overrideCovered map[string]map[string]bool,
	counter *TaskCounter, alreadyKnown bool) orgOutcome {

	projectDef, hasProjDef := projectDefsByOrg[org][key]
	if !hasProjDef {
		// Pathological — org said yes, project says no.
		counter.Fail()
		return orgOutcome{
			Org: org, Status: outcomeFailed,
			Reason: "rejected at org scope, key absent from project scope",
			Detail: "Failed: rejected at org scope, key absent from project scope" + mergeSuffix,
		}
	}
	if alreadyKnown {
		e.Logger.Debug("setGlobalSettings: org-level write already rejected for this key, propagating to projects without retrying",
			"key", key, "org", org)
	} else {
		e.Logger.Debug("setGlobalSettings: key not settable at org level despite list_definitions claim, propagating to projects",
			"key", key, "org", org)
	}
	applied, failed, skipped := fanOutGlobalToProjects(ctx, e, raw, key, org, projectDef, projectKeyMap, overrideCovered)
	for range applied {
		counter.Success()
	}
	for range failed {
		counter.Fail()
	}

	// Branch the Detail wording on actual per-project counts so the
	// row doesn't say "Applied to all projects" while also listing
	// failures — that's the contradictory text the report showed
	// before. Three cases:
	//
	//   - applied=N, failed=0 → "Applied value=… to all projects"
	//   - applied=N, failed=M → "Applied value=… to N of (N+M) projects
	//                            (failed: …; skipped: …)"
	//   - applied=0, failed=M → "Failed: N projects (e.g. <error>)"
	//
	// Override-skipped projects (per-project SQS override wins) are
	// listed alongside failures but are not failures themselves.
	a, f, s := len(applied), len(failed), len(skipped)
	total := a + f
	var detail string
	var status string
	switch {
	case a > 0 && f == 0:
		detail = "Applied " + valueSummary + " to all projects"
		status = outcomeAppliedToProjects
	case a > 0 && f > 0:
		detail = fmt.Sprintf("Applied %s to %d of %d projects", valueSummary, a, total)
		status = outcomePartial
	default: // a == 0 — every fan-out project failed (or no targets).
		if f > 0 {
			detail = fmt.Sprintf("Failed: %d project(s), e.g. %s", f, failed[0].reason)
		} else {
			detail = "Failed: no eligible projects in org"
		}
		status = outcomeFailed
	}
	var notes []string
	if f > 0 {
		names := make([]string, 0, f)
		for _, p := range failed {
			names = append(names, p.project)
		}
		notes = append(notes, "failed: "+strings.Join(names, ", "))
	}
	if s > 0 {
		names := make([]string, 0, s)
		for _, p := range skipped {
			names = append(names, p+" (override)")
		}
		notes = append(notes, "skipped: "+strings.Join(names, ", "))
	}
	if len(notes) > 0 {
		detail += " (" + strings.Join(notes, "; ") + ")"
	}
	detail += mergeSuffix
	out := orgOutcome{Org: org, Status: status, Detail: detail}
	if status == outcomeFailed && f > 0 {
		// Surface a concrete API error message in the Reason so the
		// report's Failed bucket can populate EntityItem.ErrorMessage.
		out.Reason = failed[0].reason
	}
	return out
}

// projectFanOutFailure is returned from fanOutGlobalToProjects for each
// project where the per-project apply failed, so the caller can roll
// the error into its result record.
type projectFanOutFailure struct {
	project string
	reason  string
}

// fanOutGlobalToProjects applies a global setting record to every
// project in the given org using the same definition-aware dispatcher
// that setProjectSettings uses. This is the runtime fallback when
// SQC's list_definitions claims a key is settable at org-scope but
// /api/settings/set?organization=... returns 400 — the key is in
// reality project-scope-only.
//
// Projects whose source SQS has a per-project override for this key
// (recorded in overrideCovered, keyed by serverURL+sourceKey) are
// skipped here — setProjectSettings applies their specific value via
// its per-record loop; the fan-out would race against it and could
// clobber the override. Such projects are returned as `skipped` so
// the caller can include them in the result record.
func fanOutGlobalToProjects(ctx context.Context, e *Executor, raw json.RawMessage,
	key, org string, def types.SettingDefinition,
	projectKeyMap map[string]projectMapping,
	overrideCovered map[string]map[string]bool,
) (applied []string, failed []projectFanOutFailure, skipped []string) {

	for projLookupKey, pm := range projectKeyMap {
		if pm.OrgKey != org {
			continue
		}
		if overrideCovered[projLookupKey][key] {
			e.Logger.Debug("setGlobalSettings: per-project override wins, skipping fan-out",
				"project", pm.CloudKey, "key", key, "org", org)
			skipped = append(skipped, pm.CloudKey)
			continue
		}
		// Retry the per-project apply when SQC reports the project
		// as not-yet-indexed (#334). The indexing-lag window is
		// independent of /api/projects/search readiness so we can
		// only handle it at the call site.
		err := retryOnProjectNotFound(ctx, e.Logger, func() error {
			return applySettingByDef(ctx, e, pm.CloudKey, pm.OrgKey, raw, key, def, true)
		}, "endpoint", "/api/settings/set", "project", pm.CloudKey, "org", org, "setting_key", key)
		switch {
		case errors.Is(err, errSettingEmpty):
			// nothing to do
		case err != nil:
			logAPIWarn(e.Logger, "setGlobalSettings: project fan-out failed", err,
				"key", key, "project", pm.CloudKey, "org", org)
			failed = append(failed, projectFanOutFailure{project: pm.CloudKey, reason: err.Error()})
		default:
			applied = append(applied, pm.CloudKey)
		}
	}
	return applied, failed, skipped
}

// buildPerProjectOverrideCoverage scans the getProjectSettings extract
// and returns a {serverURL+projectKey -> {settingKey -> true}} set of
// per-project SQS overrides. setProjectSettings's per-record loop
// applies these in parallel; the fan-out in fanOutGlobalToProjects
// uses this map to avoid clobbering them. Errors are swallowed: an
// empty map degrades gracefully to the previous behaviour (fan-out
// over-writes overrides) rather than failing the migration.
func buildPerProjectOverrideCoverage(e *Executor) map[string]map[string]bool {
	items, err := readExtractItems(e, "getProjectSettings")
	if err != nil {
		e.Logger.Debug("setGlobalSettings: could not read getProjectSettings extract for override coverage",
			"err", err)
		return map[string]map[string]bool{}
	}
	out := make(map[string]map[string]bool)
	for _, it := range items {
		projectKey := extractField(it.Data, "project")
		if projectKey == "" {
			projectKey = extractField(it.Data, "projectKey")
		}
		settingKey := extractField(it.Data, "key")
		if projectKey == "" || settingKey == "" {
			continue
		}
		lookup := it.ServerURL + projectKey
		if out[lookup] == nil {
			out[lookup] = make(map[string]bool)
		}
		out[lookup][settingKey] = true
	}
	return out
}

// mergeGlobalIntoLocal folds the SQS-side patterns from a global-only
// key (pair.SQSGlobalKey) into the related key that exists on SQC
// (pair.SQSLocalKey), so platform-enforced patterns aren't lost during
// migration. The synthesized record carries a _merged_from marker
// holding the global key name; renderGlobalSettingDetail uses it to
// produce a "merged from <global> + <local>" note in the report.
//
// Behaviour rules (same shape for every pair):
//   - If pair.SQSGlobalKey has no values (or value=) on SQS, this is a
//     no-op — the original customized list passes through.
//   - If pair.SQSGlobalKey IS set, the synthesized pair.SQSLocalKey
//     record carries the union (order-preserving, deduped) of the
//     global patterns and the local patterns.
//   - pair.SQSGlobalKey is removed from the customized list — its
//     patterns have moved to pair.SQSLocalKey, so there's nothing to
//     migrate under the original key.
//   - If pair.SQSLocalKey wasn't customized on its own (only the
//     global side was) we still emit the synthesized record so the
//     global patterns make it to SQC.
func mergeGlobalIntoLocal(pair exclusionPair, sqsValues []json.RawMessage,
	customized []json.RawMessage, logger *slog.Logger) []json.RawMessage {

	// Look up both sides in the full extract — we need the global patterns
	// even if the local key wasn't filtered into `customized`.
	var globalRec, localRec json.RawMessage
	for _, raw := range sqsValues {
		switch extractField(raw, "key") {
		case pair.SQSGlobalKey:
			globalRec = raw
		case pair.SQSLocalKey:
			localRec = raw
		}
	}
	globalVals := readPatterns(globalRec)
	if len(globalVals) == 0 {
		return customized
	}
	localVals := readPatterns(localRec)
	merged := unionPreservingOrder(globalVals, localVals)

	synth := map[string]any{
		"key":          pair.SQSLocalKey,
		"values":       merged,
		"_merged_from": pair.SQSGlobalKey,
	}
	synthRaw, _ := json.Marshal(synth)

	out := make([]json.RawMessage, 0, len(customized)+1)
	replaced := false
	for _, raw := range customized {
		switch extractField(raw, "key") {
		case pair.SQSGlobalKey:
			// Drop — its patterns have moved into the local key.
			continue
		case pair.SQSLocalKey:
			out = append(out, synthRaw)
			replaced = true
		default:
			out = append(out, raw)
		}
	}
	if !replaced {
		// Local key wasn't in the customized list (it was at SQS
		// default), but the global side was set — synthesize a
		// record so the global patterns make it across.
		out = append(out, synthRaw)
	}
	logger.Info("setGlobalSettings: merged global key into local key",
		"global_key", pair.SQSGlobalKey,
		"local_key", pair.SQSLocalKey,
		"global_patterns", len(globalVals),
		"local_patterns", len(localVals),
		"merged_patterns", len(merged))
	return out
}

// readPatterns reads exclusion-style patterns from a setting record,
// handling both shapes that /api/settings/values may return (values=[...]
// for a multi-value field, value="csv,joined" for a single field).
func readPatterns(raw json.RawMessage) []string {
	if raw == nil {
		return nil
	}
	if vals := extractStringArray(raw, "values"); len(vals) > 0 {
		return vals
	}
	if v := extractField(raw, "value"); v != "" {
		return strings.Split(v, ",")
	}
	return nil
}

// unionPreservingOrder returns the deduplicated concatenation a ++ b,
// preserving first-seen order. Used to merge two exclusion lists into a
// stable, predictable single list.
func unionPreservingOrder(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range b {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// IsSettingCustomized reports whether the SQS-side value for a setting
// actually differs from its default. SQS reveals the default in two
// places, in priority order:
//
//  1. parentValue / parentValues on the value record itself — SQS's
//     own "this is the default the user is inheriting from" hint.
//     Present whenever a global setting has been explicitly set,
//     even if to the same value as the default.
//  2. defaultValue on the list_definitions record — the static
//     declared default. Some setting keys don't expose this field,
//     which is why parentValue is the more reliable signal.
//
// Issue #196: after several SQS upgrades, operators sometimes end up
// with global settings explicitly set to values identical to the
// default. Without comparing against parentValue/parentValues those
// settings would be migrated unnecessarily, inflating the SQC API
// call count and noising up the migration report.
func IsSettingCustomized(raw json.RawMessage, defaultValue string) bool {
	if fvs := extractObjectArray(raw, "fieldValues"); len(fvs) > 0 {
		// PROPERTY_SET — comparing two arbitrary JSON object arrays
		// for "is this the default" is non-trivial, and these
		// settings are rarely set to default by accident. Treat any
		// populated fieldValues as customized.
		return true
	}
	if vals := extractStringArray(raw, "values"); len(vals) > 0 {
		// Prefer parentValues (SQS-side default for this very key)
		// over defaultValue from list_definitions: parentValues is
		// always populated when the setting is set; defaultValue is
		// sometimes missing from list_definitions.
		if pvals := extractStringArray(raw, "parentValues"); len(pvals) > 0 {
			return !equalSortedStrings(vals, pvals)
		}
		return !equalSortedStrings(vals, splitDefaultCSV(defaultValue))
	}
	v := extractField(raw, "value")
	if pv := extractField(raw, "parentValue"); pv != "" {
		return v != pv
	}
	return v != defaultValue
}

// equalSortedStrings reports whether a and b contain the same set of
// elements, ignoring order. Both slices are sorted on a copy so the
// callers' originals are untouched.
func equalSortedStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa := append([]string(nil), a...)
	sb := append([]string(nil), b...)
	sort.Strings(sa)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// splitDefaultCSV turns SQS's comma-joined default representation
// ("a,b,c") into a string slice, treating an empty defaultValue as the
// empty slice (NOT a slice with one empty element, which is what
// strings.Split returns by default).
func splitDefaultCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// readSettingPayload extracts the three possible value shapes from a
// settings record so the result can be serialized back into the output
// JSONL record exactly as it came from SQS.
func readSettingPayload(raw json.RawMessage) (value string, values []string, fieldValues []map[string]any) {
	return extractField(raw, "value"),
		extractStringArray(raw, "values"),
		extractObjectArray(raw, "fieldValues")
}

// globalSettingResult is the per-setting record written to the
// setGlobalSettings task output (one JSONL line per setting key) and
// read back by the summary report to populate the Global Settings
// section. Outcomes carry the per-org result, so the report can
// render a row per (setting × org) with a Detail string specific to
// what happened on THAT org.
type globalSettingResult struct {
	Key              string           `json:"key"`
	Value            string           `json:"value,omitempty"`
	Values           []string         `json:"values,omitempty"`
	FieldValues      []map[string]any `json:"fieldValues,omitempty"`
	Outcomes         []orgOutcome     `json:"outcomes"`
	MergedFromGlobal string           `json:"merged_from_global,omitempty"`
}

// orgOutcome is the per-(setting, org) result. Status drives the
// report bucket (applied / applied-to-projects → Succeeded, failed →
// Failed, skipped → Skipped); Detail is the pre-rendered row text the
// report displays verbatim; Reason is forwarded to EntityItem's
// ErrorMessage (failed) or SkipReason (skipped) fields.
type orgOutcome struct {
	Org    string `json:"org"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Reason string `json:"reason,omitempty"`
}

// outcomeStatus* constants name the values orgOutcome.Status can take.
// Kept here (rather than as an exported enum on the report side) so
// migrate and report agree on the contract.
const (
	outcomeApplied           = "applied"
	outcomeAppliedToProjects = "applied-to-projects"
	outcomePartial           = "partial"
	outcomeFailed            = "failed"
	outcomeSkipped           = "skipped"
)
