// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package gui

import "github.com/sonar-solutions/sonar-migration-tool/internal/wizard"

// Server-to-browser message types (prompts require a response).
const (
	TypePromptURL           = "prompt_url"
	TypePromptText          = "prompt_text"
	TypePromptPassword      = "prompt_password"
	TypePromptConfirm       = "prompt_confirm"
	TypePromptConfirmReview = "prompt_confirm_review"
	TypePromptExtractForm   = "prompt_extract_form"
	TypePromptMigrateForm   = "prompt_migrate_form"
	TypePromptChoice        = "prompt_choice"

	TypeDisplayWelcome        = "display_welcome"
	TypeDisplayPhaseProgress  = "display_phase_progress"
	TypeDisplayMessage        = "display_message"
	TypeDisplayError          = "display_error"
	TypeDisplayWarning        = "display_warning"
	TypeDisplaySuccess        = "display_success"
	TypeDisplaySummary        = "display_summary"
	TypeDisplayResumeInfo     = "display_resume_info"
	TypeDisplayWizardComplete = "display_wizard_complete"

	TypeWizardStarted  = "wizard_started"
	TypeWizardFinished = "wizard_finished"
)

// Browser-to-server message types.
const (
	TypePromptResponse = "prompt_response"
	TypeStartWizard    = "start_wizard"
	TypeCancelWizard   = "cancel_wizard"
)

// KVPair is a key-value pair for JSON transport (mirrors wizard.KV).
type KVPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ServerMessage is sent from the Go server to the browser.
type ServerMessage struct {
	Type        string   `json:"type"`
	ID          string   `json:"id,omitempty"`
	Message     string   `json:"message,omitempty"`
	Validate    bool     `json:"validate,omitempty"`
	Default     any      `json:"default,omitempty"`
	Title       string   `json:"title,omitempty"`
	Details     []KVPair `json:"details,omitempty"`
	Phase       string   `json:"phase,omitempty"`
	Index       int      `json:"index,omitempty"`
	Total       int      `json:"total,omitempty"`
	Name        string   `json:"name,omitempty"`
	Stats       []KVPair `json:"stats,omitempty"`
	SourceURL   string   `json:"source_url,omitempty"`
	TargetURL   string   `json:"target_url,omitempty"`
	ExtractID   string   `json:"extract_id,omitempty"`
	Error       *string  `json:"error"`
	BackEnabled bool     `json:"back_enabled,omitempty"`
	Options     []string `json:"options,omitempty"`

	// DefaultURL / TokenOptional / DefaultEnterpriseKey /
	// DefaultIncludeProjectData / DefaultIncludeIssueSync carry the
	// field defaults for prompt_extract_form and prompt_migrate_form.
	// DefaultEnterpriseKey is only used by the latter.
	DefaultURL                string `json:"default_url,omitempty"`
	TokenOptional             bool   `json:"token_optional,omitempty"`
	DefaultEnterpriseKey      string `json:"default_enterprise_key,omitempty"`
	DefaultIncludeProjectData bool   `json:"default_include_project_data"`
	DefaultIncludeIssueSync   bool   `json:"default_include_issue_sync"`
}

// ClientMessage is sent from the browser to the Go server.
type ClientMessage struct {
	Type  string `json:"type"`
	ID    string `json:"id,omitempty"`
	Value any    `json:"value,omitempty"`
}

// ToKVPairs converts wizard.KV slices to the transport type.
func ToKVPairs(kvs []wizard.KV) []KVPair {
	out := make([]KVPair, len(kvs))
	for i, kv := range kvs {
		out[i] = KVPair{Key: kv.Key, Value: kv.Value}
	}
	return out
}
