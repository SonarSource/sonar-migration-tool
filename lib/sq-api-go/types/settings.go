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

// SettingDefinition describes a setting's property type, whether it accepts
// multiple values, and its default value, as returned by
// /api/settings/list_definitions. The defaultValue is the source-of-truth
// for "is this setting customized?" — global-settings migration (issue
// #186) compares it against the value returned by /api/settings/values to
// decide whether to forward the setting to SQC.
type SettingDefinition struct {
	Key          string `json:"key"`
	Type         string `json:"type"`
	MultiValues  bool   `json:"multiValues"`
	DefaultValue string `json:"defaultValue"`
}

// SettingsListDefinitionsResponse is the response envelope for
// /api/settings/list_definitions.
type SettingsListDefinitionsResponse struct {
	Definitions []SettingDefinition `json:"definitions"`
}
