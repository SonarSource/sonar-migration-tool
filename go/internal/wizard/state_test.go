// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package wizard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNewWizardState(t *testing.T) {
	s := NewWizardState()
	if s.Phase != PhaseInit {
		t.Errorf("expected phase %q, got %q", PhaseInit, s.Phase)
	}
	if s.ExtractID != nil || s.SourceURL != nil || s.TargetURL != nil ||
		s.EnterpriseKey != nil || s.MigrationRunID != nil {
		t.Error("expected all pointer fields to be nil")
	}
	if s.OrganizationsMapped || s.ValidationPassed {
		t.Error("expected bool fields to be false")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	original := &WizardState{
		Phase:              PhaseStructure,
		ExtractID:          strPtr("abc-123"),
		SourceURL:          strPtr("https://sonar.example.com"),
		TargetURL:          strPtr("https://sonarcloud.io"),
		EnterpriseKey:      strPtr("my-enterprise"),
		OrganizationsMapped: true,
		ValidationPassed:   false,
		MigrationRunID:     nil,
	}

	if err := original.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.Phase != original.Phase {
		t.Errorf("phase: got %q, want %q", loaded.Phase, original.Phase)
	}
	if *loaded.ExtractID != *original.ExtractID {
		t.Errorf("extract_id: got %q, want %q", *loaded.ExtractID, *original.ExtractID)
	}
	if *loaded.SourceURL != *original.SourceURL {
		t.Errorf("source_url: got %q, want %q", *loaded.SourceURL, *original.SourceURL)
	}
	if loaded.OrganizationsMapped != original.OrganizationsMapped {
		t.Errorf("organizations_mapped: got %v, want %v", loaded.OrganizationsMapped, original.OrganizationsMapped)
	}
	if loaded.MigrationRunID != nil {
		t.Error("expected migration_run_id to be nil")
	}
}

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	state, err := Load(dir)
	if err != nil {
		t.Fatalf("Load missing file: %v", err)
	}
	if state.Phase != PhaseInit {
		t.Errorf("expected phase %q for missing file, got %q", PhaseInit, state.Phase)
	}
}

func TestJSONFormat(t *testing.T) {
	state := &WizardState{
		Phase:              PhaseExtract,
		ExtractID:          strPtr("run-42"),
		SourceURL:          strPtr("https://sq.local"),
		TargetURL:          nil,
		EnterpriseKey:      nil,
		OrganizationsMapped: false,
		ValidationPassed:   false,
		MigrationRunID:     nil,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify the exact JSON format for state persistence.
	expected := `{
  "phase": "extract",
  "extract_id": "run-42",
  "source_url": "https://sq.local",
  "target_url": null,
  "enterprise_key": null,
  "organizations_mapped": false,
  "validation_passed": false,
  "migration_run_id": null
}`

	if string(data) != expected {
		t.Errorf("JSON mismatch.\nGot:\n%s\n\nWant:\n%s", string(data), expected)
	}
}

func TestSaveCreatesFile(t *testing.T) {
	dir := t.TempDir()
	state := NewWizardState()
	if err := state.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := filepath.Join(dir, stateFileName)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state file not created: %v", err)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte("{invalid json}"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestLoadReadError(t *testing.T) {
	dir := t.TempDir()
	// Create a directory where the state file should be, causing a read error.
	if err := os.Mkdir(filepath.Join(dir, stateFileName), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Error("expected error when state file is a directory")
	}
}

func TestResetPhaseStateExtract(t *testing.T) {
	s := fullyPopulatedState()
	resetPhaseState(s, PhaseExtract)

	if s.SourceURL != nil {
		t.Error("SourceURL should be nil after reset")
	}
	if s.ExtractID != nil {
		t.Error("ExtractID should be nil after reset")
	}
	if s.SkippedProjects != nil {
		t.Error("SkippedProjects should be nil after reset")
	}
	// Unrelated fields should remain.
	if s.TargetURL == nil {
		t.Error("TargetURL should be untouched")
	}
}

func TestResetPhaseStateOrgMapping(t *testing.T) {
	s := fullyPopulatedState()
	resetPhaseState(s, PhaseOrgMapping)

	if s.TargetURL != nil {
		t.Error("TargetURL should be nil after reset")
	}
	if s.EnterpriseKey != nil {
		t.Error("EnterpriseKey should be nil after reset")
	}
	if s.OrganizationsMapped {
		t.Error("OrganizationsMapped should be false after reset")
	}
	// Unrelated fields should remain.
	if s.SourceURL == nil {
		t.Error("SourceURL should be untouched")
	}
}

func TestResetPhaseStateValidate(t *testing.T) {
	s := fullyPopulatedState()
	resetPhaseState(s, PhaseValidate)

	if s.ValidationPassed {
		t.Error("ValidationPassed should be false after reset")
	}
	if s.SourceURL == nil {
		t.Error("SourceURL should be untouched")
	}
}

func TestResetPhaseStateMigrate(t *testing.T) {
	s := fullyPopulatedState()
	resetPhaseState(s, PhaseMigrate)

	if s.MigrationRunID != nil {
		t.Error("MigrationRunID should be nil after reset")
	}
	if s.SourceURL == nil {
		t.Error("SourceURL should be untouched")
	}
}

func TestResetPhaseStateNoOp(t *testing.T) {
	s := fullyPopulatedState()
	resetPhaseState(s, PhaseStructure)

	// PhaseStructure is not handled, nothing should change.
	if s.SourceURL == nil || s.TargetURL == nil {
		t.Error("unhandled phase should not clear fields")
	}
}

func fullyPopulatedState() *WizardState {
	return &WizardState{
		Phase:               PhaseComplete,
		SourceURL:           strPtr("https://source"),
		ExtractID:           strPtr("extract-1"),
		TargetURL:           strPtr("https://target"),
		EnterpriseKey:       strPtr("ent-1"),
		OrganizationsMapped: true,
		ValidationPassed:    true,
		MigrationRunID:      strPtr("run-1"),
		SkippedProjects:     []string{"proj-1"},
	}
}
