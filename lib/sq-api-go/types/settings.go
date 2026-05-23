package types

// Setting represents a single configuration setting returned by
// /api/settings/values.
type Setting struct {
	Key         string                   `json:"key"`
	Value       string                   `json:"value"`
	Values      []string                 `json:"values"`
	FieldValues []map[string]interface{} `json:"fieldValues"`
	Inherited   bool                     `json:"inherited"`
}

// SettingsValuesResponse is the response envelope for /api/settings/values.
type SettingsValuesResponse struct {
	Settings []Setting `json:"settings"`
}

// SettingDefinition describes a setting's property type and whether it
// accepts multiple values, as returned by /api/settings/list_definitions.
// Only the fields actually consumed during migration are decoded — the
// API also returns name, description, category, defaultValue, options, and
// fields, all of which we don't need.
type SettingDefinition struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	MultiValues bool   `json:"multiValues"`
}

// SettingsListDefinitionsResponse is the response envelope for
// /api/settings/list_definitions.
type SettingsListDefinitionsResponse struct {
	Definitions []SettingDefinition `json:"definitions"`
}
