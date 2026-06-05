// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package wizard

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const stateFileName = ".wizard_state.json"

// WizardPhase represents a phase in the migration wizard.
type WizardPhase string

const (
	PhaseInit       WizardPhase = "init"
	PhaseExtract    WizardPhase = "extract"
	PhaseStructure  WizardPhase = "structure"
	PhaseOrgMapping WizardPhase = "org_mapping"
	PhaseMappings   WizardPhase = "mappings"
	PhaseValidate   WizardPhase = "validate"
	PhaseMigrate  WizardPhase = "migrate"
	PhaseComplete WizardPhase = "complete"
)

// WizardState holds the persistent state of a migration wizard session.
// JSON serialization persists to .wizard_state.json for resume support.
type WizardState struct {
	Phase              WizardPhase `json:"phase"`
	ExtractID          *string     `json:"extract_id"`
	SourceURL          *string     `json:"source_url"`
	TargetURL          *string     `json:"target_url"`
	EnterpriseKey      *string     `json:"enterprise_key"`
	OrganizationsMapped bool       `json:"organizations_mapped"`
	ValidationPassed   bool        `json:"validation_passed"`
	MigrationRunID     *string     `json:"migration_run_id"`
	SkippedProjects    []string    `json:"skipped_projects,omitempty"`
}

// NewWizardState returns a WizardState initialized to the INIT phase.
func NewWizardState() *WizardState {
	return &WizardState{Phase: PhaseInit}
}

// Save persists the wizard state to .wizard_state.json in the given directory.
func (s *WizardState) Save(directory string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, stateFileName), data, 0644)
}

// resetPhaseState clears state fields written by the given phase so the
// phase handler will re-prompt the user for that information.
func resetPhaseState(state *WizardState, phase WizardPhase) {
	switch phase {
	case PhaseExtract:
		state.SourceURL = nil
		state.ExtractID = nil
		state.SkippedProjects = nil
	case PhaseOrgMapping:
		state.TargetURL = nil
		state.EnterpriseKey = nil
		state.OrganizationsMapped = false
	case PhaseValidate:
		state.ValidationPassed = false
	case PhaseMigrate:
		state.MigrationRunID = nil
	}
}

// Load reads a WizardState from .wizard_state.json in the given directory.
// If the file does not exist, it returns a new state at the INIT phase.
func Load(directory string) (*WizardState, error) {
	data, err := os.ReadFile(filepath.Join(directory, stateFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewWizardState(), nil
		}
		return nil, err
	}
	var state WizardState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}
