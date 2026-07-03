// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cloud

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/sonar-solutions/sq-api-go/types"
)

const apiSettingsSet = "api/settings/set"

// SettingsClient provides write-path methods for SonarQube Cloud project settings.
type SettingsClient struct{ baseClient }

// ListDefinitions returns the setting definitions registered on SonarQube
// Cloud — used by migration to decide whether each setting key should be
// posted via "value" (single, possibly CSV-joined), "values" (multi-value
// list), or "fieldValues" (property-set). The shape returned by SQS's
// /api/settings/values for a given key is NOT always the same as what SQC
// expects when writing: e.g. sonar.java.file.suffixes comes back from SQS
// as a values=[...] array but on SQC is defined as a single STRING property
// with multiValues=false, so posting it as values= silently no-ops (returns
// 204 without persisting). Reading the target's definitions removes that
// guesswork.
//
// organization scopes the call to a single SQC org (required for
// project-level callers). Passing an empty organization returns the
// global definitions.
//
// component, when non-empty, switches the call to project scope —
// SQC returns the superset of definitions visible at that project,
// including project-only keys (sonar.<lang>.* language settings,
// external-analyzer settings, etc.) that are NOT visible at org
// scope. Issue #189/#191 migration uses the difference between
// project-scope and org-scope sets to detect which SQS global
// settings need to be propagated to every SQC project.
func (s *SettingsClient) ListDefinitions(ctx context.Context, organization, component string) ([]types.SettingDefinition, error) {
	q := url.Values{}
	if organization != "" {
		q.Set("organization", organization)
	}
	if component != "" {
		q.Set("component", component)
	}
	path := "api/settings/list_definitions"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp types.SettingsListDefinitionsResponse
	if err := s.getJSON(ctx, path, &resp); err != nil {
		return nil, err
	}
	return resp.Definitions, nil
}

// Set sets a single-value project setting via /api/settings/set.
//
// The organization argument is accepted for API symmetry but only included
// in the request when projectKey is empty. SonarQube Cloud rejects requests
// that include both component and organization with HTTP 400
// "Only component or organization can be set, not both"; for project-level
// settings the cloud project key already namespaces by organization.
func (s *SettingsClient) Set(ctx context.Context, projectKey, settingKey, value, organization string) error {
	form := url.Values{}
	form.Set("key", settingKey)
	form.Set("value", value)
	addSettingsScope(form, projectKey, organization)
	return s.postForm(ctx, apiSettingsSet, form, nil)
}

// SetValues sets a multi-value project setting via /api/settings/set.
// Each entry is sent as a separate "values" form parameter (the encoding
// the SonarQube/SonarQube Cloud Web API expects for multi-value settings
// such as sonar.exclusions, sonar.coverage.exclusions, etc.).
func (s *SettingsClient) SetValues(ctx context.Context, projectKey, settingKey string, values []string, organization string) error {
	form := url.Values{}
	form.Set("key", settingKey)
	for _, v := range values {
		form.Add("values", v)
	}
	addSettingsScope(form, projectKey, organization)
	return s.postForm(ctx, apiSettingsSet, form, nil)
}

// SetFieldValues sets a property-set (multi-field) project setting via
// /api/settings/set. Each entry is JSON-encoded and sent as a separate
// "fieldValues" form parameter — the encoding used by settings such as
// sonar.issue.ignore.allfile, sonar.issue.ignore.multicriteria, etc.
func (s *SettingsClient) SetFieldValues(ctx context.Context, projectKey, settingKey string, fieldValues []map[string]any, organization string) error {
	form := url.Values{}
	form.Set("key", settingKey)
	for _, fv := range fieldValues {
		encoded, err := json.Marshal(fv)
		if err != nil {
			continue
		}
		form.Add("fieldValues", string(encoded))
	}
	addSettingsScope(form, projectKey, organization)
	return s.postForm(ctx, apiSettingsSet, form, nil)
}

// Values returns settings explicitly set at the given scope. Settings
// still at their default are omitted by SonarQube Cloud's response.
// Used by reset to enumerate which org-level settings need reverting.
func (s *SettingsClient) Values(ctx context.Context, projectKey, organization string) ([]types.Setting, error) {
	q := url.Values{}
	if projectKey != "" {
		q.Set("component", projectKey)
	} else if organization != "" {
		q.Set("organization", organization)
	}
	path := "api/settings/values"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp types.SettingsValuesResponse
	if err := s.getJSON(ctx, path, &resp); err != nil {
		return nil, err
	}
	return resp.Settings, nil
}

// Reset reverts the named settings to their default value at the given
// scope (POST /api/settings/reset). Multiple keys are joined with
// commas — the SonarQube Web API expects a single "keys" parameter,
// not repeated keys=K1&keys=K2 form values. Passing an empty keys list
// is a no-op (the API would reject it with HTTP 400).
func (s *SettingsClient) Reset(ctx context.Context, projectKey string, keys []string, organization string) error {
	if len(keys) == 0 {
		return nil
	}
	form := url.Values{}
	form.Set("keys", strings.Join(keys, ","))
	addSettingsScope(form, projectKey, organization)
	return s.postForm(ctx, "api/settings/reset", form, nil)
}

// addSettingsScope adds exactly one of "component" or "organization" to
// the request form. SonarQube Cloud rejects calls that include both, so a
// non-empty projectKey wins (project-level settings inherit organization
// scope from the cloud project key prefix).
func addSettingsScope(form url.Values, projectKey, organization string) {
	if projectKey != "" {
		form.Set("component", projectKey)
		return
	}
	if organization != "" {
		form.Set("organization", organization)
	}
}
