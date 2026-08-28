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

	"github.com/sonar-solutions/sq-api-go/types"
)

// errSettingEmpty is the sentinel returned by applySettingByDef when the
// extract record had no value / values / fieldValues to send. Callers
// silently skip the record — it is not a real error.
var errSettingEmpty = errors.New("setting has no value")

// settingKeyTally counts occurrences per setting key so a task can log
// one line per key instead of one per (project, key) pair.
//
// setProjectSettings iterates the cross-product of projects and
// settings, so any per-record Warn is emitted O(projects) times for a
// fault that is really a property of the key alone. A single customer
// run produced 42,048 identical warnings for 49 keys across 858
// projects, which buried every other diagnostic in the log.
type settingKeyTally struct {
	mu     sync.Mutex
	counts map[string]int
}

func newSettingKeyTally() *settingKeyTally {
	return &settingKeyTally{counts: make(map[string]int)}
}

// mark records one occurrence of key and reports whether it was the
// first. Callers log only when first is true.
func (t *settingKeyTally) mark(key string) (first bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	first = t.counts[key] == 0
	t.counts[key]++
	return first
}

// has reports whether key has been marked at least once.
func (t *settingKeyTally) has(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[key] > 0
}

// logSummary emits one line per tallied key, in key order so the output
// is stable across runs. No-op when nothing was tallied.
func (t *settingKeyTally) logSummary(logger *slog.Logger, msg string) {
	t.mu.Lock()
	keys := make([]string, 0, len(t.counts))
	for k := range t.counts {
		keys = append(keys, k)
	}
	counts := make(map[string]int, len(t.counts))
	for k, v := range t.counts {
		counts[k] = v
	}
	t.mu.Unlock()

	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	for _, k := range keys {
		logger.Info(msg, "setting", k, "projects_affected", counts[k])
	}
}

// skipProjectSettingKey reports whether a getProjectSettings record
// should never be POSTed at project scope, and why.
//
// setGlobalSettings applies both of these filters before any API call
// (IsInternalSqsSetting at tasks_setglobalsettings.go and the curated
// sqsOnly* tables via partitionSQSOnlySettings). setProjectSettings
// historically applied neither, so a SonarQube Server global setting
// that reached the per-project extract was pushed to every project and
// rejected by SonarQube Cloud with HTTP 400.
//
// Both lists are curated and deliberately not exhaustive; the runtime
// IsProjectLevelRejection memoisation in runSetProjectSettings is what
// catches the remainder.
func skipProjectSettingKey(settingKey string, raw json.RawMessage) (reason string, skip bool) {
	if IsInternalSqsSetting(settingKey) {
		return "internal-sqs-setting", true
	}
	handler, ok := resolveSQSOnlyHandler(settingKey)
	if !ok {
		return "", false
	}
	if handler(raw).SkipSilently {
		return "sqs-only", true
	}
	// The key is SQS-only but carries an operator-meaningful value.
	// setGlobalSettings surfaces it as a report note rather than
	// migrating it; there is nothing to write at project scope either.
	return "sqs-only-note", true
}

// loadSettingDefinitionsForOrgs fetches /api/settings/list_definitions
// once for each SonarQube Cloud organization in the supplied set and
// returns a per-org lookup keyed by setting key. A failed fetch for one
// org is logged at Warn level and yields an empty (not nil) inner map —
// callers then transparently fall back to extract-shape dispatch for
// that org.
//
// taskName is included in the log message so the source of the warning is
// obvious (setProjectSettings vs setGlobalSettings).
func loadSettingDefinitionsForOrgs(ctx context.Context, e *Executor, orgs map[string]struct{}, taskName string) map[string]map[string]types.SettingDefinition {
	out := make(map[string]map[string]types.SettingDefinition, len(orgs))
	for org := range orgs {
		defs, err := e.Cloud.Settings.ListDefinitions(ctx, org, "")
		if err != nil {
			logAPIWarn(e.Logger, taskName+": list_definitions failed, falling back to extract-shape dispatch", err, "org", org)
			out[org] = map[string]types.SettingDefinition{}
			continue
		}
		byKey := make(map[string]types.SettingDefinition, len(defs))
		for _, d := range defs {
			byKey[d.Key] = d
		}
		out[org] = byKey
		e.Logger.Debug(taskName+": loaded definitions", "org", org, "count", len(defs))
	}
	return out
}

// loadProjectScopedSettingDefinitionsForOrgs is the project-scope
// counterpart to loadSettingDefinitionsForOrgs: it picks one
// representative project per org from projectKeyMap and calls
// /api/settings/list_definitions?organization=...&component=... so SQC
// returns the SUPERSET of definitions visible at that project
// (including language and external-analyzer keys that have no org-level
// counterpart). The diff project-scope − org-scope is what issue
// #189/#191 uses to decide which SQS global settings to propagate to
// every SQC project.
//
// If an org has no entry in projectKeyMap, it's skipped — there's no
// component to scope by. A failed fetch yields an empty (not nil)
// inner map so callers fall back to org-scope semantics.
func loadProjectScopedSettingDefinitionsForOrgs(ctx context.Context, e *Executor, projectKeyMap map[string]projectMapping, taskName string) map[string]map[string]types.SettingDefinition {
	// Pick one cloud_project_key per org. Stable choice (first one
	// encountered in iteration order) — SQC's project-scope defs are
	// the same across projects of the same org, so any project works.
	probeByOrg := make(map[string]string)
	for _, pm := range projectKeyMap {
		if pm.OrgKey == "" || pm.CloudKey == "" {
			continue
		}
		if _, seen := probeByOrg[pm.OrgKey]; !seen {
			probeByOrg[pm.OrgKey] = pm.CloudKey
		}
	}
	out := make(map[string]map[string]types.SettingDefinition, len(probeByOrg))
	for org, probe := range probeByOrg {
		// SQC's project-scope list_definitions can 404 with "Project
		// doesn't exist" for several seconds after createProjects
		// returns — independent indexing lag (#334). Retry through
		// that window so the probe succeeds on the freshly-created
		// project.
		var defs []types.SettingDefinition
		err := retryOnProjectNotFound(ctx, e.Logger, func() error {
			var inner error
			defs, inner = e.Cloud.Settings.ListDefinitions(ctx, org, probe)
			return inner
		}, "endpoint", "/api/settings/list_definitions", "org", org, "probe_project", probe)
		if err != nil {
			logAPIWarn(e.Logger, taskName+": project-scope list_definitions failed", err, "org", org, "probe_project", probe)
			out[org] = map[string]types.SettingDefinition{}
			continue
		}
		byKey := make(map[string]types.SettingDefinition, len(defs))
		for _, d := range defs {
			byKey[d.Key] = d
		}
		out[org] = byKey
		e.Logger.Debug(taskName+": loaded project-scope definitions", "org", org, "probe", probe, "count", len(defs))
	}
	return out
}

// sqsSettingDefaults is the getServerSettingsDefinitions baseline for
// the source SonarQube Server: the declared defaultValue per setting
// key, plus which keys the server declared at all. It is what lets a
// real operator override be told apart from a value that merely
// repeats what the scope above already supplies.
//
// Loaded once per setProjectSettings run and shared by both write
// paths, because the definitions extract is the only place the static
// defaults live and re-reading it per path was pure waste.
type sqsSettingDefaults struct {
	defaultValue map[string]string
	declared     map[string]bool
}

// readSQSSettingDefaults loads the definitions extract into a baseline.
// A missing or unreadable extract yields an empty (not nil) baseline:
// every lookup then reports "no baseline", and callers fall back to
// their fail-open behaviour rather than making a decision on no data.
func readSQSSettingDefaults(e *Executor) (*sqsSettingDefaults, error) {
	defItems, err := readExtractItems(e, "getServerSettingsDefinitions")
	if err != nil {
		return nil, fmt.Errorf("reading getServerSettingsDefinitions: %w", err)
	}
	d := &sqsSettingDefaults{
		defaultValue: make(map[string]string, len(defItems)),
		declared:     make(map[string]bool, len(defItems)),
	}
	for _, it := range defItems {
		k := extractField(it.Data, "key")
		if k == "" {
			continue
		}
		d.defaultValue[k] = extractField(it.Data, "defaultValue")
		d.declared[k] = true
	}
	return d, nil
}

// leftAtDefault reports whether a settings record carries a value
// indistinguishable from the one its scope would inherit anyway, so
// writing it to SonarQube Cloud would pin an override that reproduces
// nothing the operator actually chose.
//
// SonarQube Server does not answer "is this customized?" directly. It
// hands back two baselines, and both are needed:
//
//   - parentValue / parentValues on the record itself — what the scope
//     above supplies. Authoritative when present.
//   - defaultValue from list_definitions — the static declared default.
//     The only baseline for a key no higher scope has touched.
//
// When neither is available the answer is unknowable, and the two
// mistakes are not symmetric: wrongly applying a default-valued
// setting costs one redundant API call, while wrongly skipping one
// silently drops a value the operator set on purpose. So no baseline
// means "not at default" — apply it.
//
// A nil receiver behaves the same way, so a failed load degrades to
// the pre-existing "apply everything" behaviour instead of skipping
// wholesale.
func (d *sqsSettingDefaults) leftAtDefault(settingKey string, raw json.RawMessage) bool {
	if d == nil {
		return false
	}
	def := d.defaultValue[settingKey]
	if def == "" && !hasParentValue(raw) {
		return false
	}
	return !IsSettingCustomized(raw, def)
}

// readCustomizedSQSGlobals reads getServerSettings from the extract and
// returns the SQS global settings whose value differs from the declared
// defaultValue in `defaults` — the same filter used by setGlobalSettings
// (issue #186) and now reused by setProjectSettings (issue #189/#191) to
// feed the project-scope propagation pass.
//
// Errors reading the extract are surfaced; callers downstream treat them
// as fatal because they signal an incomplete extract.
func readCustomizedSQSGlobals(e *Executor, defaults *sqsSettingDefaults) ([]json.RawMessage, error) {
	if defaults == nil {
		defaults = &sqsSettingDefaults{}
	}
	valueItems, err := readExtractItems(e, "getServerSettings")
	if err != nil {
		return nil, fmt.Errorf("reading getServerSettings: %w", err)
	}
	customized := make([]json.RawMessage, 0, len(valueItems))
	for _, it := range valueItems {
		key := extractField(it.Data, "key")
		if key == "" {
			continue
		}
		// At GLOBAL scope inherited=true means "no row at this level,
		// the value shown is the definition default", so the setting is
		// by definition not customized. This is authoritative and needs
		// no defaultValue comparison — measured on SonarQube Server
		// 2026.3, 81 of 255 global records carry the flag.
		if extractBool(it.Data, "inherited") {
			continue
		}
		// Never propagate server-internal or curated SQS-only keys.
		// setGlobalSettings drops these before any API call; this
		// function feeds the project-scope propagation pass, which had
		// no equivalent filter.
		if _, skip := skipProjectSettingKey(key, it.Data); skip {
			continue
		}
		// A key absent from getServerSettingsDefinitions has no known
		// defaultValue. Treating the zero value as "the default is
		// empty" makes every such key look customized — on a 2026.3
		// instance 55 of 246 keys returned by /api/settings/values are
		// absent from list_definitions (hidden, secured, or permission-
		// filtered), which is how the bundled .NET analyzer manifest
		// keys entered the customized set. Fall back to parentValue
		// when the definition is missing, and skip when there is no
		// baseline to compare against at all.
		if !defaults.declared[key] && !hasParentValue(it.Data) {
			continue
		}
		if !IsSettingCustomized(it.Data, defaults.defaultValue[key]) {
			continue
		}
		customized = append(customized, it.Data)
	}
	return customized, nil
}

// hasParentValue reports whether a settings record carries the parent
// value that /api/settings/values populates when more than one scope
// supplied a value. Presence is NOT a reliable "this is an override"
// test on its own — inherited records carry it too — but it does mean a
// comparison baseline exists.
func hasParentValue(raw json.RawMessage) bool {
	if extractField(raw, "parentValue") != "" {
		return true
	}
	if len(extractStringArray(raw, "parentValues")) > 0 {
		return true
	}
	return len(extractObjectArray(raw, "parentFieldValues")) > 0
}

// applySettingByDef is the shared definition-aware dispatcher used by both
// setProjectSettings (projectKey non-empty) and setGlobalSettings
// (projectKey empty, orgKey non-empty). When a SQC definition is supplied
// for the setting key, the post shape is chosen from the target's
// definition (PROPERTY_SET → fieldValues, multiValues=true → values,
// otherwise → single CSV-joined value). Without a definition we fall back
// to the extract record's shape.
//
// The definition path matters because SQS and SQC disagree on a handful of
// settings — notably sonar.java.file.suffixes — where SQS returns
// values=[...] but SQC's definition is a single STRING with
// multiValues=false. POSTing values= to such a setting on SQC returns 204
// but silently fails to persist; joining with comma and POSTing as value=
// is what actually lands. See issue #120 for the regression that motivated
// this dispatcher and issue #186 for the global-scope reuse.
func applySettingByDef(ctx context.Context, e *Executor, projectKey, orgKey string,
	raw json.RawMessage, settingKey string, def types.SettingDefinition, hasDef bool) error {

	scope := "project"
	if projectKey == "" {
		scope = "global"
	}

	if hasDef {
		switch {
		case def.Type == "PROPERTY_SET":
			fvs := extractObjectArray(raw, "fieldValues")
			if len(fvs) == 0 {
				return errSettingEmpty
			}
			e.Logger.Debug(scope+" api call: POST /api/settings/set (property-set)",
				"project", projectKey, "key", settingKey, "field_values_count", len(fvs), "org", orgKey)
			return e.Cloud.Settings.SetFieldValues(ctx, projectKey, settingKey, fvs, orgKey)
		case def.MultiValues:
			vals := extractStringArray(raw, "values")
			if len(vals) == 0 {
				if v := extractField(raw, "value"); v != "" {
					vals = strings.Split(v, ",")
				}
			}
			if len(vals) == 0 {
				return errSettingEmpty
			}
			e.Logger.Debug(scope+" api call: POST /api/settings/set (multi-value)",
				"project", projectKey, "key", settingKey, "values", vals, "org", orgKey)
			return e.Cloud.Settings.SetValues(ctx, projectKey, settingKey, vals, orgKey)
		default:
			// Single-value (STRING/BOOLEAN/INTEGER/FLOAT/SINGLE_SELECT_LIST,
			// etc.). If SQS returned a list (values=), CSV-join it so SQC
			// stores it as one string.
			value := extractField(raw, "value")
			if value == "" {
				if vals := extractStringArray(raw, "values"); len(vals) > 0 {
					value = strings.Join(vals, ",")
				}
			}
			if value == "" {
				return errSettingEmpty
			}
			e.Logger.Debug(scope+" api call: POST /api/settings/set",
				"project", projectKey, "key", settingKey, "value", value, "org", orgKey)
			return e.Cloud.Settings.Set(ctx, projectKey, settingKey, value, orgKey)
		}
	}

	// No SQC definition for this key — fall back to dispatching by the
	// shape of the extract record. This preserves behaviour for custom or
	// plugin-defined settings that aren't in list_definitions.
	if vals := extractStringArray(raw, "values"); len(vals) > 0 {
		e.Logger.Debug(scope+" api call: POST /api/settings/set (multi-value, no SQC def)",
			"project", projectKey, "key", settingKey, "values", vals, "org", orgKey)
		return e.Cloud.Settings.SetValues(ctx, projectKey, settingKey, vals, orgKey)
	}
	if fvs := extractObjectArray(raw, "fieldValues"); len(fvs) > 0 {
		e.Logger.Debug(scope+" api call: POST /api/settings/set (property-set, no SQC def)",
			"project", projectKey, "key", settingKey, "field_values_count", len(fvs), "org", orgKey)
		return e.Cloud.Settings.SetFieldValues(ctx, projectKey, settingKey, fvs, orgKey)
	}
	value := extractField(raw, "value")
	if value == "" {
		return errSettingEmpty
	}
	e.Logger.Debug(scope+" api call: POST /api/settings/set (no SQC def)",
		"project", projectKey, "key", settingKey, "value", value, "org", orgKey)
	return e.Cloud.Settings.Set(ctx, projectKey, settingKey, value, orgKey)
}

// extractObjectArray reads a []map[string]any from a JSON field. Returns
// nil for missing fields, non-array shapes, or empty arrays.
func extractObjectArray(raw json.RawMessage, key string) []map[string]any {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	arrRaw, ok := obj[key]
	if !ok {
		return nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(arrRaw, &arr); err != nil {
		return nil
	}
	return arr
}

// perProjectRecordCounts is the record count per project observed while
// iterating the extract.
type perProjectRecordCounts map[string]int

// looksLikeLeakedGlobals reports the leaked-global-table signature: a high
// record count per project that is near-identical for every project.
//
// /api/settings/values?component=X returns the project's EFFECTIVE
// configuration — the whole instance-scope set plus any project rows — and
// the extract is supposed to keep only records the server marked
// non-inherited. When that filter does not take effect (an extract produced
// by an older build, or a source whose responses omit the flag), every
// project ends up carrying an identical copy of the global table. One
// customer run reached 244,399 records for 1,142 projects: 214.01 each, i.e.
// ~11 genuine overrides in the whole estate and 42,048 HTTP 400s from the
// ~49 instance-scope-only keys.
//
// Real overrides are sparse and uneven, so uniformity is the tell.
func (c perProjectRecordCounts) looksLikeLeakedGlobals() (mean float64, min, max int, ok bool) {
	const (
		minProjects       = 5  // too few to reason about a distribution
		suspiciousPerProj = 25 // real per-project override sets are small
	)
	if len(c) < minProjects {
		return 0, 0, 0, false
	}
	total := 0
	min, max = -1, 0
	for _, n := range c {
		total += n
		if min < 0 || n < min {
			min = n
		}
		if n > max {
			max = n
		}
	}
	mean = float64(total) / float64(len(c))
	if mean < suspiciousPerProj {
		return mean, min, max, false
	}
	// A leaked global table gives every project the same count, so the
	// spread is a tiny fraction of the mean.
	if float64(max-min) > 0.25*mean {
		return mean, min, max, false
	}
	return mean, min, max, true
}
