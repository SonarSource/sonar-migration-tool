package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sonar-solutions/sq-api-go/types"
)

// errSettingEmpty is the sentinel returned by applySettingByDef when the
// extract record had no value / values / fieldValues to send. Callers
// silently skip the record — it is not a real error.
var errSettingEmpty = errors.New("setting has no value")

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
		defs, err := e.Cloud.Settings.ListDefinitions(ctx, org)
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
