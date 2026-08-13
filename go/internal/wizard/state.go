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
	PhaseMigrate    WizardPhase = "migrate"
	PhaseComplete   WizardPhase = "complete"
)

// WizardState holds the persistent state of a migration wizard session.
// JSON serialization persists to .wizard_state.json for resume support.
//
// SourceToken / TargetToken are deliberately tagged json:"-" so they
// never reach disk. They exist only so `gui --config <file>` (#388)
// can pre-fill the corresponding password prompts in memory via
// RunWithSeed — the wizard reads them when present and prompts
// otherwise. Resume reloads the state from disk, where these fields
// will be empty, and the user is asked to retype the tokens.
type WizardState struct {
	Phase               WizardPhase `json:"phase"`
	ExtractID           *string     `json:"extract_id"`
	SourceURL           *string     `json:"source_url"`
	TargetURL           *string     `json:"target_url"`
	EnterpriseKey       *string     `json:"enterprise_key"`
	OrganizationsMapped bool        `json:"organizations_mapped"`
	ValidationPassed    bool        `json:"validation_passed"`
	MigrationRunID      *string     `json:"migration_run_id"`
	SkippedProjects     []string    `json:"skipped_projects,omitempty"`

	// Tokens — in-memory only. See type-level comment for rationale.
	SourceToken *string `json:"-"`
	TargetToken *string `json:"-"`
}

// NewWizardState returns a WizardState initialized to the INIT phase.
func NewWizardState() *WizardState {
	return &WizardState{Phase: PhaseInit}
}

// Save persists the wizard state to .wizard_state.json in the given
// directory. Written atomically (temp file + rename) so a process killed
// mid-save (Ctrl-C, crash, OOM) can never leave a truncated/empty state
// file behind — os.WriteFile alone truncates-then-writes as separate
// syscalls, and a kill between them corrupts the file for every future
// Load until it's manually deleted.
func (s *WizardState) Save(directory string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(directory, stateFileName+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once Rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(directory, stateFileName))
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
// A zero-byte file — the signature left by a pre-atomic-write Save that
// was killed mid-write (Ctrl-C, crash) before this fix — is treated the
// same as a missing file: WizardState always serializes to non-empty
// JSON, so an empty file can never have been a legitimately saved state,
// and there's nothing to resume from it anyway.
func Load(directory string) (*WizardState, error) {
	data, err := os.ReadFile(filepath.Join(directory, stateFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewWizardState(), nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return NewWizardState(), nil
	}
	var state WizardState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}
