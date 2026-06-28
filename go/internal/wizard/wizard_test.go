// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package wizard

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const (
	testOKTrue        = "expected ok=true"
	testSQCloudURL    = "https://sonarcloud.io/"
	errExpectExtract  = "expected PhaseExtract, got %s"
)

// MockPrompter supplies pre-programmed responses for tests.
type MockPrompter struct {
	URLResponses      []string
	TextResponses     []string
	PasswordResponses []string
	ConfirmResponses  []bool
	ReviewResponses   []bool
	ChoiceResponses   []int

	Messages []string // captures DisplayMessage, DisplayError, etc.

	urlIdx, textIdx, passIdx, confirmIdx, reviewIdx, choiceIdx int
}

func (m *MockPrompter) PromptURL(msg string, validate bool) (string, error) {
	if m.urlIdx >= len(m.URLResponses) {
		return "", fmt.Errorf("MockPrompter: no more URL responses")
	}
	r := m.URLResponses[m.urlIdx]
	m.urlIdx++
	return r, nil
}

func (m *MockPrompter) PromptText(msg, def string) (string, error) {
	if m.textIdx >= len(m.TextResponses) {
		return def, nil
	}
	r := m.TextResponses[m.textIdx]
	m.textIdx++
	return r, nil
}

func (m *MockPrompter) PromptPassword(msg string) (string, error) {
	if m.passIdx >= len(m.PasswordResponses) {
		return "", fmt.Errorf("MockPrompter: no more password responses")
	}
	r := m.PasswordResponses[m.passIdx]
	m.passIdx++
	return r, nil
}

func (m *MockPrompter) Confirm(msg string, def bool) (bool, error) {
	if m.confirmIdx >= len(m.ConfirmResponses) {
		return def, nil
	}
	r := m.ConfirmResponses[m.confirmIdx]
	m.confirmIdx++
	return r, nil
}

func (m *MockPrompter) ConfirmReview(title string, details []KV) (bool, error) {
	if m.reviewIdx >= len(m.ReviewResponses) {
		return false, nil
	}
	r := m.ReviewResponses[m.reviewIdx]
	m.reviewIdx++
	return r, nil
}

func (m *MockPrompter) PromptChoice(msg string, options []string) (int, error) {
	if m.choiceIdx >= len(m.ChoiceResponses) {
		return 0, nil
	}
	r := m.ChoiceResponses[m.choiceIdx]
	m.choiceIdx++
	return r, nil
}
func (m *MockPrompter) SetBackEnabled(bool)                     { /* no-op for tests */ }
func (m *MockPrompter) DisplayWelcome()                        { /* no-op for tests */ }
func (m *MockPrompter) DisplayPhaseProgress(phase WizardPhase) { /* no-op for tests */ }
func (m *MockPrompter) DisplayMessage(msg string)              { m.Messages = append(m.Messages, msg) }
func (m *MockPrompter) DisplayError(msg string)                { m.Messages = append(m.Messages, "ERR:"+msg) }
func (m *MockPrompter) DisplayWarning(msg string)              { m.Messages = append(m.Messages, "WARN:"+msg) }
func (m *MockPrompter) DisplaySuccess(msg string)              { m.Messages = append(m.Messages, "OK:"+msg) }
func (m *MockPrompter) DisplaySummary(title string, stats []KV) {
	/* no-op for tests — summary display not asserted */
}
func (m *MockPrompter) DisplayResumeInfo(state *WizardState) { /* no-op for tests */ }
func (m *MockPrompter) DisplayWizardComplete()                { /* no-op for tests */ }

// --- Resume Logic Tests ---

func TestHandleResumeInitPhase(t *testing.T) {
	state := NewWizardState()
	p := &MockPrompter{}
	dir := t.TempDir()

	result, shouldContinue := handleResume(p, state, dir)
	if !shouldContinue {
		t.Fatal("expected shouldContinue=true for INIT state")
	}
	if result.Phase != PhaseInit {
		t.Errorf("expected INIT phase, got %s", result.Phase)
	}
}

func TestHandleResumeResumeExisting(t *testing.T) {
	state := &WizardState{
		Phase:     PhaseStructure,
		SourceURL: strPtr(testServerURLSlash),
	}
	p := &MockPrompter{
		ConfirmResponses: []bool{true}, // resume=yes
	}
	dir := t.TempDir()

	result, shouldContinue := handleResume(p, state, dir)
	if !shouldContinue {
		t.Fatal("expected shouldContinue=true when resuming")
	}
	if result.Phase != PhaseStructure {
		t.Errorf("expected STRUCTURE phase, got %s", result.Phase)
	}
}

func TestHandleResumeStartFresh(t *testing.T) {
	state := &WizardState{Phase: PhaseStructure}
	p := &MockPrompter{
		ConfirmResponses: []bool{false, true}, // resume=no, start new=yes
	}
	dir := t.TempDir()

	result, shouldContinue := handleResume(p, state, dir)
	if !shouldContinue {
		t.Fatal("expected shouldContinue=true when starting fresh")
	}
	if result.Phase != PhaseInit {
		t.Errorf("expected INIT phase after fresh start, got %s", result.Phase)
	}
}

func TestHandleResumeCancel(t *testing.T) {
	state := &WizardState{Phase: PhaseStructure}
	p := &MockPrompter{
		ConfirmResponses: []bool{false, false}, // resume=no, start new=no
	}
	dir := t.TempDir()

	_, shouldContinue := handleResume(p, state, dir)
	if shouldContinue {
		t.Fatal("expected shouldContinue=false when both declined")
	}
}

func TestDetermineStartingPhaseInit(t *testing.T) {
	state := NewWizardState()
	p := &MockPrompter{}
	phase, ok := determineStartingPhase(p, state, t.TempDir())
	if !ok {
		t.Fatal(testOKTrue)
	}
	if phase != PhaseExtract {
		t.Errorf(errExpectExtract, phase)
	}
}

func TestDetermineStartingPhaseComplete(t *testing.T) {
	state := &WizardState{Phase: PhaseComplete}
	p := &MockPrompter{
		ConfirmResponses: []bool{true}, // start new=yes
	}
	dir := t.TempDir()
	phase, ok := determineStartingPhase(p, state, dir)
	if !ok {
		t.Fatal(testOKTrue)
	}
	if phase != PhaseExtract {
		t.Errorf(errExpectExtract, phase)
	}
}

func TestDetermineStartingPhaseCompleteDecline(t *testing.T) {
	state := &WizardState{Phase: PhaseComplete}
	p := &MockPrompter{
		ConfirmResponses: []bool{false}, // start new=no
	}
	_, ok := determineStartingPhase(p, state, t.TempDir())
	if ok {
		t.Fatal("expected ok=false when declining new migration")
	}
}

func TestDetermineStartingPhaseResume(t *testing.T) {
	state := &WizardState{Phase: PhaseMappings}
	p := &MockPrompter{}
	phase, ok := determineStartingPhase(p, state, t.TempDir())
	if !ok {
		t.Fatal(testOKTrue)
	}
	if phase != PhaseMappings {
		t.Errorf("expected PhaseMappings, got %s", phase)
	}
}

// --- Run with Context Cancellation ---

func TestRunContextCancellation(t *testing.T) {
	dir := t.TempDir()
	state := &WizardState{Phase: PhaseExtract}
	state.Save(dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	p := &MockPrompter{
		ConfirmResponses: []bool{true}, // resume=yes
	}

	err := Run(ctx, p, dir)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	loaded, loadErr := Load(dir)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if loaded.Phase != PhaseExtract {
		t.Errorf("expected phase EXTRACT preserved, got %s", loaded.Phase)
	}
}

// --- runPhaseLoop tests ---

// --- runPhaseHandler dispatch for all testable phases ---

func TestRunPhaseHandlerValidate(t *testing.T) {
	dir := t.TempDir()
	orgHeaders := []string{"sonarqube_org_key", "sonarcloud_org_key"}
	writeCSV(t, dir, fileOrganizations, orgHeaders, [][]string{{"org-1", "cloud-1"}})
	writeCSV(t, dir, fileProjects, []string{"key"}, [][]string{{"p1"}})
	writeCSV(t, dir, fileTemplates, []string{"name"}, [][]string{{"t1"}})
	writeCSV(t, dir, fileProfiles, []string{"name"}, [][]string{{"pr1"}})
	writeCSV(t, dir, fileGates, []string{"name"}, [][]string{{"g1"}})
	writeCSV(t, dir, fileGroups, []string{"name"}, [][]string{{"gr1"}})

	state := &WizardState{Phase: PhaseValidate}
	p := &MockPrompter{}

	err := runPhaseHandler(context.Background(), p, state, dir, PhaseValidate)
	if err != nil {
		t.Fatalf("runPhaseHandler validate: %v", err)
	}
	if state.Phase != PhaseMigrate {
		t.Errorf("expected MIGRATE, got %s", state.Phase)
	}
}

func TestRunPhaseHandlerMigrateCancel(t *testing.T) {
	dir := t.TempDir()
	state := &WizardState{Phase: PhaseMigrate, TargetURL: strPtr(testSQCloudURL)}
	p := &MockPrompter{ConfirmResponses: []bool{false}}

	err := runPhaseHandler(context.Background(), p, state, dir, PhaseMigrate)
	if err == nil {
		t.Fatal("expected error when user declines migration")
	}
}

func TestRunPhaseHandlerUnknownPhase(t *testing.T) {
	state := NewWizardState()
	p := &MockPrompter{}
	err := runPhaseHandler(context.Background(), p, state, t.TempDir(), WizardPhase("bogus"))
	if err == nil {
		t.Fatal("expected error for unknown phase")
	}
}

// --- Run with resume paths ---

func TestRunResumeFromOrgMapping(t *testing.T) {
	restoreM := mockMappings(nil)
	defer restoreM()
	restoreMig := mockMigrate(nil)
	defer restoreMig()

	dir := t.TempDir()

	// Pre-existing state at org mapping phase
	state := &WizardState{
		Phase:     PhaseOrgMapping,
		SourceURL: strPtr(testSQServerURL),
		ExtractID: strPtr("test-01"),
	}
	state.Save(dir)

	// Create CSVs needed for org mapping onward
	orgs := []structure.Organization{
		{SonarQubeOrgKey: "org-1", ServerURL: testSQServerURL, ProjectCount: 1},
	}
	structure.ExportCSV(dir, "organizations", orgs)
	structure.ExportCSV(dir, "projects", []structure.Project{{Key: "p1"}})
	for _, name := range []string{"templates", "profiles", "gates", "groups", "portfolios"} {
		writeCSV(t, dir, name+".csv", []string{"name"}, [][]string{{"x"}})
	}

	p := &MockPrompter{
		ConfirmResponses: []bool{
			true, // resume=yes
			true, // migrate org-1
			true, // proceed with migration
		},
		URLResponses:      []string{testSQCloudURL},
		TextResponses:     []string{testEntKey, "cloud-org"},
		PasswordResponses: []string{"cloud-token"},
		ReviewResponses:   []bool{true},
	}

	err := Run(context.Background(), p, dir)
	if err != nil {
		t.Fatalf("Run resume: %v", err)
	}

	loaded, _ := Load(dir)
	if loaded.Phase != PhaseComplete {
		t.Errorf(errExpectComplete, loaded.Phase)
	}
}

// --- Run fresh start path ---

func TestRunFreshStartFailsOnExtract(t *testing.T) {
	dir := t.TempDir()
	p := &MockPrompter{
		URLResponses:      []string{testServerURLSlash},
		PasswordResponses: []string{"token123"},
		ReviewResponses:   []bool{true},
		ConfirmResponses:  []bool{false, false, false}, // project data: no, retry: no, restart: no
	}

	err := Run(context.Background(), p, dir)
	if err == nil {
		t.Fatal("expected error (no server to extract from)")
	}

	loaded, loadErr := Load(dir)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if loaded.Phase != PhaseInit {
		t.Errorf("expected INIT (extract failed before advancing), got %s", loaded.Phase)
	}
}

// --- offerPhaseRestart tests ---

func TestOfferPhaseRestartAccepted(t *testing.T) {
	p := &MockPrompter{
		ConfirmResponses: []bool{true},
		ChoiceResponses:  []int{0},
	}
	phase, ok := offerPhaseRestart(p, PhaseOrgMapping)
	if !ok {
		t.Fatal(testOKTrue)
	}
	if phase != PhaseExtract {
		t.Errorf(errExpectExtract, phase)
	}
}

func TestOfferPhaseRestartPickLater(t *testing.T) {
	p := &MockPrompter{
		ConfirmResponses: []bool{true},
		ChoiceResponses:  []int{2},
	}
	phase, ok := offerPhaseRestart(p, PhaseMigrate)
	if !ok {
		t.Fatal(testOKTrue)
	}
	if phase != PhaseOrgMapping {
		t.Errorf("expected PhaseOrgMapping, got %s", phase)
	}
}

func TestOfferPhaseRestartDeclined(t *testing.T) {
	p := &MockPrompter{
		ConfirmResponses: []bool{false},
	}
	_, ok := offerPhaseRestart(p, PhaseMappings)
	if ok {
		t.Fatal("expected ok=false when user declines restart")
	}
}

// --- Run cancel paths ---

func TestRunCancelledAtResume(t *testing.T) {
	dir := t.TempDir()
	state := &WizardState{Phase: PhaseStructure}
	state.Save(dir)

	p := &MockPrompter{
		ConfirmResponses: []bool{false, false},
	}

	err := Run(context.Background(), p, dir)
	if err != nil {
		t.Fatalf("expected nil (user cancelled at resume), got %v", err)
	}
}

func TestRunCancelledAtDeterminePhase(t *testing.T) {
	dir := t.TempDir()
	state := &WizardState{Phase: PhaseComplete}
	state.Save(dir)

	p := &MockPrompter{
		ConfirmResponses: []bool{true, false},
	}

	err := Run(context.Background(), p, dir)
	if err != nil {
		t.Fatalf("expected nil (user cancelled at determine phase), got %v", err)
	}
}

// --- Skipped projects display ---

func TestRunWithSkippedProjects(t *testing.T) {
	restoreMig := mockMigrate(nil)
	defer restoreMig()

	dir := t.TempDir()

	orgHeaders := []string{"sonarqube_org_key", "sonarcloud_org_key"}
	writeCSV(t, dir, fileOrganizations, orgHeaders, [][]string{{"org-1", "cloud-1"}})
	writeCSV(t, dir, fileProjects, []string{"key"}, [][]string{{"p1"}})
	writeCSV(t, dir, fileTemplates, []string{"name"}, [][]string{{"t1"}})
	writeCSV(t, dir, fileProfiles, []string{"name"}, [][]string{{"pr1"}})
	writeCSV(t, dir, fileGates, []string{"name"}, [][]string{{"g1"}})
	writeCSV(t, dir, fileGroups, []string{"name"}, [][]string{{"gr1"}})

	state := &WizardState{
		Phase:           PhaseValidate,
		TargetURL:       strPtr(testSQCloudURL),
		EnterpriseKey:   strPtr(testEntKey),
		SkippedProjects: []string{"proj-a", "proj-b"},
	}
	state.Save(dir)

	p := &MockPrompter{
		ConfirmResponses:  []bool{true, true},
		PasswordResponses: []string{"token"},
	}

	err := Run(context.Background(), p, dir)
	if err != nil {
		t.Fatalf("Run with skipped projects: %v", err)
	}

	hasSkipWarning := false
	for _, msg := range p.Messages {
		if len(msg) > 4 && msg[:5] == "WARN:" {
			hasSkipWarning = true
			break
		}
	}
	if !hasSkipWarning {
		t.Error("expected warning about skipped projects")
	}
}

// --- Helper to create CSV fixture ---

func writeCSV(t *testing.T, dir, name string, headers []string, rows [][]string) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write(headers)
	for _, row := range rows {
		w.Write(row)
	}
	w.Flush()
}

func writeExtractMeta(t *testing.T, dir string) {
	t.Helper()
	extractDir := filepath.Join(dir, "test-extract-01")
	os.MkdirAll(extractDir, 0o755)
	meta := map[string]any{
		"url":     testServerURLSlash,
		"version": 10.7,
		"edition": "enterprise",
		"run_id":  "test-extract-01",
	}
	data, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(extractDir, "extract.json"), data, 0o644)
}

// #388: mergeSeed pre-fills wizard state fields from a config-driven
// seed without clobbering values the wizard has already recorded
// (disk-wins semantics for the URL / enterprise-key triple). Tokens
// always come from the seed because the on-disk state never carries
// them (json:"-").
func TestMergeSeed(t *testing.T) {
	cases := []struct {
		name  string
		state WizardState
		seed  WizardState
		want  WizardState
	}{
		{
			name:  "empty state — every seed field applied",
			state: WizardState{},
			seed: WizardState{
				SourceURL:     strPtr("https://sq"),
				TargetURL:     strPtr("https://sc"),
				EnterpriseKey: strPtr("ent"),
				SourceToken:   strPtr("sq-tok"),
				TargetToken:   strPtr("sc-tok"),
			},
			want: WizardState{
				SourceURL:     strPtr("https://sq"),
				TargetURL:     strPtr("https://sc"),
				EnterpriseKey: strPtr("ent"),
				SourceToken:   strPtr("sq-tok"),
				TargetToken:   strPtr("sc-tok"),
			},
		},
		{
			name: "existing URL wins, seed token still applied",
			state: WizardState{
				SourceURL:     strPtr("disk-sq"),
				TargetURL:     strPtr("disk-sc"),
				EnterpriseKey: strPtr("disk-ent"),
			},
			seed: WizardState{
				SourceURL:     strPtr("seed-sq"),
				TargetURL:     strPtr("seed-sc"),
				EnterpriseKey: strPtr("seed-ent"),
				SourceToken:   strPtr("sq-tok"),
				TargetToken:   strPtr("sc-tok"),
			},
			want: WizardState{
				SourceURL:     strPtr("disk-sq"),
				TargetURL:     strPtr("disk-sc"),
				EnterpriseKey: strPtr("disk-ent"),
				SourceToken:   strPtr("sq-tok"),
				TargetToken:   strPtr("sc-tok"),
			},
		},
		{
			name: "partial fill — only nil fields touched",
			state: WizardState{
				SourceURL: strPtr("disk-sq"),
			},
			seed: WizardState{
				SourceURL:     strPtr("seed-sq"),
				TargetURL:     strPtr("seed-sc"),
				EnterpriseKey: strPtr("seed-ent"),
			},
			want: WizardState{
				SourceURL:     strPtr("disk-sq"),
				TargetURL:     strPtr("seed-sc"),
				EnterpriseKey: strPtr("seed-ent"),
			},
		},
		{
			name:  "empty-string seed values are no-ops",
			state: WizardState{},
			seed: WizardState{
				SourceURL:     strPtr(""),
				TargetURL:     strPtr(""),
				EnterpriseKey: strPtr(""),
				SourceToken:   strPtr(""),
				TargetToken:   strPtr(""),
			},
			want: WizardState{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state := c.state
			mergeSeed(&state, &c.seed)
			assertPtrEqual(t, "SourceURL", state.SourceURL, c.want.SourceURL)
			assertPtrEqual(t, "TargetURL", state.TargetURL, c.want.TargetURL)
			assertPtrEqual(t, "EnterpriseKey", state.EnterpriseKey, c.want.EnterpriseKey)
			assertPtrEqual(t, "SourceToken", state.SourceToken, c.want.SourceToken)
			assertPtrEqual(t, "TargetToken", state.TargetToken, c.want.TargetToken)
		})
	}
}

// #388: tokens carried in the wizard state must never reach
// .wizard_state.json — they're json:"-" so Save() drops them. Resume
// reloads the file and gets nil tokens.
func TestWizardState_TokensNotPersisted(t *testing.T) {
	dir := t.TempDir()
	state := &WizardState{
		SourceURL:   strPtr("https://sq"),
		SourceToken: strPtr("secret-sq"),
		TargetToken: strPtr("secret-sc"),
	}
	if err := state.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.SourceToken != nil {
		t.Errorf("SourceToken must not persist; got %q on disk", *reloaded.SourceToken)
	}
	if reloaded.TargetToken != nil {
		t.Errorf("TargetToken must not persist; got %q on disk", *reloaded.TargetToken)
	}
	if reloaded.SourceURL == nil || *reloaded.SourceURL != "https://sq" {
		t.Errorf("SourceURL did not round-trip; got %v", reloaded.SourceURL)
	}
}

func assertPtrEqual(t *testing.T, field string, got, want *string) {
	t.Helper()
	switch {
	case got == nil && want == nil:
		return
	case got == nil:
		t.Errorf("%s: got nil, want %q", field, *want)
	case want == nil:
		t.Errorf("%s: got %q, want nil", field, *got)
	case *got != *want:
		t.Errorf("%s: got %q, want %q", field, *got, *want)
	}
}
