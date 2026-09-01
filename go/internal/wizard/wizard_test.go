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
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const (
	testOKTrue       = "expected ok=true"
	testSQCloudURL   = "https://sonarcloud.io/"
	errExpectExtract = "expected PhaseExtract, got %s"
)

// MockPrompter supplies pre-programmed responses for tests.
type MockPrompter struct {
	URLResponses         []string
	TextResponses        []string
	PasswordResponses    []string
	ConfirmResponses     []bool
	ReviewResponses      []bool
	ChoiceResponses      []int
	ExtractFormResponses []ExtractFormResult // optional; falls back to the caller's defaults when exhausted
	ExtractFormErr       error               // if set, PromptExtractForm returns this error once (e.g. simulating Cancel)
	MigrateFormResponses []MigrateFormResult // optional; falls back to the caller's defaults when exhausted
	MigrateFormErr       error               // if set, PromptMigrateForm returns this error once (e.g. simulating Cancel)

	Messages             []string // captures DisplayMessage, DisplayError, etc.
	OverallProgressCalls []OverallProgressCall

	urlIdx, textIdx, passIdx, confirmIdx, reviewIdx, choiceIdx, extractFormIdx, migrateFormIdx int
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

func (m *MockPrompter) PromptExtractForm(defaults ExtractFormDefaults) (ExtractFormResult, error) {
	if m.ExtractFormErr != nil {
		err := m.ExtractFormErr
		m.ExtractFormErr = nil
		return ExtractFormResult{}, err
	}

	result := ExtractFormResult{
		URL:                defaults.URL,
		PEMFilePath:        defaults.PEMFilePath,
		KeyFilePath:        defaults.KeyFilePath,
		ProjectKeyPattern:  defaults.ProjectKeyPattern,
		IncludeProjectData: defaults.IncludeProjectData,
		IncludeIssueSync:   defaults.IncludeIssueSync,
	}
	if m.extractFormIdx < len(m.ExtractFormResponses) {
		result = m.ExtractFormResponses[m.extractFormIdx]
		m.extractFormIdx++
	}
	return result, nil
}

func (m *MockPrompter) PromptMigrateForm(defaults MigrateFormDefaults) (MigrateFormResult, error) {
	if m.MigrateFormErr != nil {
		err := m.MigrateFormErr
		m.MigrateFormErr = nil
		return MigrateFormResult{}, err
	}

	result := MigrateFormResult{
		URL:                 defaults.URL,
		EnterpriseKey:       defaults.EnterpriseKey,
		DefaultOrganization: defaults.DefaultOrganization,
		IncludeProjectData:  defaults.IncludeProjectData,
		IncludeIssueSync:    defaults.IncludeIssueSync,
	}
	if m.migrateFormIdx < len(m.MigrateFormResponses) {
		result = m.MigrateFormResponses[m.migrateFormIdx]
		m.migrateFormIdx++
	}
	return result, nil
}

func (m *MockPrompter) PromptChoice(msg string, options []string) (int, error) {
	if m.choiceIdx >= len(m.ChoiceResponses) {
		return 0, nil
	}
	r := m.ChoiceResponses[m.choiceIdx]
	m.choiceIdx++
	return r, nil
}
func (m *MockPrompter) SetBackEnabled(bool)                    { /* no-op for tests */ }
func (m *MockPrompter) DisplayWelcome()                        { /* no-op for tests */ }
func (m *MockPrompter) DisplayPhaseProgress(phase WizardPhase) { /* no-op for tests */ }

// OverallProgressCall records one DisplayOverallProgress invocation (#519).
type OverallProgressCall struct {
	Percent float64
	Eta     time.Duration
	Known   bool
}

func (m *MockPrompter) DisplayOverallProgress(percent float64, eta time.Duration, known bool) {
	m.OverallProgressCalls = append(m.OverallProgressCalls, OverallProgressCall{percent, eta, known})
}
func (m *MockPrompter) DisplayMessage(msg string) { m.Messages = append(m.Messages, msg) }
func (m *MockPrompter) DisplayError(msg string)   { m.Messages = append(m.Messages, "ERR:"+msg) }
func (m *MockPrompter) DisplayWarning(msg string) { m.Messages = append(m.Messages, "WARN:"+msg) }
func (m *MockPrompter) DisplaySuccess(msg string) { m.Messages = append(m.Messages, "OK:"+msg) }
func (m *MockPrompter) DisplaySummary(title string, stats []KV) {
	/* no-op for tests — summary display not asserted */
}
func (m *MockPrompter) DisplayResumeInfo(state *WizardState) { /* no-op for tests */ }
func (m *MockPrompter) DisplayWizardComplete()               { /* no-op for tests */ }

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
	assertExtractPhase(t, phase, ok)
}

func TestDetermineStartingPhaseComplete(t *testing.T) {
	state := &WizardState{Phase: PhaseComplete}
	p := &MockPrompter{
		ConfirmResponses: []bool{true}, // start new=yes
	}
	dir := t.TempDir()
	phase, ok := determineStartingPhase(p, state, dir)
	assertExtractPhase(t, phase, ok)
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
	writeMinimalCSVs(t, dir)

	// A legitimate resume always has ExtractID set (phaseExtract sets it
	// before advancing past PhaseStructure) — verifyPhasePrerequisites
	// (#550) requires it for any phase after Extract.
	state := &WizardState{Phase: PhaseValidate, ExtractID: strPtr("test-extract-01")}
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
	writeMinimalCSVs(t, dir)

	// Real prerequisites present (ExtractID + the mapping CSVs) so this
	// exercises the decline-to-retry path inside phaseMigrate itself,
	// not verifyPhasePrerequisites (#550) rejecting the phase outright.
	state := &WizardState{Phase: PhaseMigrate, TargetURL: strPtr(testSQCloudURL), ExtractID: strPtr("test-extract-01")}
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

// --- verifyPhasePrerequisites (#550): a .wizard_state.json claiming a
// downstream phase — whether from tampering or ordinary corruption/a bad
// manual edit — must not let runPhaseHandler dispatch into that phase
// unless the artifacts earlier phases are supposed to have produced
// genuinely exist on disk. ---

func TestVerifyPhasePrerequisitesExtractIDMissing(t *testing.T) {
	dir := t.TempDir()
	writeMinimalCSVs(t, dir) // structure + mapping files present...

	for _, phase := range []WizardPhase{PhaseOrgMapping, PhaseMappings, PhaseValidate, PhaseMigrate} {
		state := &WizardState{Phase: phase} // ...but ExtractID is not set.
		if err := verifyPhasePrerequisites(state, dir, phase); err == nil {
			t.Errorf("phase %s: expected error when ExtractID is unset, got nil", phase)
		}
	}
}

func TestVerifyPhasePrerequisitesStructureOutputMissing(t *testing.T) {
	dir := t.TempDir() // no organizations.csv / projects.csv at all

	for _, phase := range []WizardPhase{PhaseOrgMapping, PhaseMappings, PhaseValidate, PhaseMigrate} {
		state := &WizardState{Phase: phase, ExtractID: strPtr("fake-id")}
		if err := verifyPhasePrerequisites(state, dir, phase); err == nil {
			t.Errorf("phase %s: expected error when structure output is missing, got nil", phase)
		}
	}
}

func TestVerifyPhasePrerequisitesMappingFilesMissing(t *testing.T) {
	dir := t.TempDir()
	// Structure output present, but none of the mapping CSVs
	// (templates/profiles/gates/groups) that phaseMappings produces.
	orgHeaders := []string{"sonarqube_org_key", "sonarcloud_org_key"}
	writeCSV(t, dir, fileOrganizations, orgHeaders, [][]string{{"org-1", "cloud-1"}})
	writeCSV(t, dir, fileProjects, []string{"key"}, [][]string{{"p1"}})

	for _, phase := range []WizardPhase{PhaseValidate, PhaseMigrate} {
		state := &WizardState{Phase: phase, ExtractID: strPtr("fake-id")}
		if err := verifyPhasePrerequisites(state, dir, phase); err == nil {
			t.Errorf("phase %s: expected error when mapping files are missing, got nil", phase)
		}
	}

	// OrgMapping only needs the structure output, not the mapping CSVs.
	if err := verifyPhasePrerequisites(&WizardState{Phase: PhaseOrgMapping, ExtractID: strPtr("fake-id")}, dir, PhaseOrgMapping); err != nil {
		t.Errorf("PhaseOrgMapping: unexpected error with structure output present: %v", err)
	}
}

func TestVerifyPhasePrerequisitesLegitimateResumePasses(t *testing.T) {
	dir := t.TempDir()
	writeMinimalCSVs(t, dir)

	for _, phase := range []WizardPhase{PhaseOrgMapping, PhaseMappings, PhaseValidate, PhaseMigrate} {
		state := &WizardState{Phase: phase, ExtractID: strPtr("real-extract-01")}
		if err := verifyPhasePrerequisites(state, dir, phase); err != nil {
			t.Errorf("phase %s: unexpected error for legitimate resume: %v", phase, err)
		}
	}
}

func TestVerifyPhasePrerequisitesExtractHasNone(t *testing.T) {
	// PhaseExtract is the entry point and is never passed to
	// verifyPhasePrerequisites by runPhaseHandler, but the function
	// itself must be a no-op for it regardless (defensive — no case
	// matches PhaseExtract in either switch).
	if err := verifyPhasePrerequisites(&WizardState{Phase: PhaseExtract}, t.TempDir(), PhaseExtract); err != nil {
		t.Errorf("PhaseExtract: expected no prerequisites, got %v", err)
	}
}

// TestRunRejectsFabricatedMigrateState reproduces the exact scenario
// from issue #550: a .wizard_state.json claiming phase "migrate" against
// an arbitrary target, with none of the real prerequisites (extraction,
// structure, org mapping, generated mapping CSVs) ever having run. This
// could arise from tampering or from ordinary file corruption / a bad
// manual edit — either way, runPhaseHandler must refuse to dispatch into
// phaseMigrate rather than proceeding to prompt for migrate credentials
// or invoke runMigrateFn against the fabricated target.
func TestRunRejectsFabricatedMigrateState(t *testing.T) {
	dir := t.TempDir() // fresh export dir: no CSVs, no extract output

	origMigrate := runMigrateFn
	migrateCalled := false
	runMigrateFn = func(_ context.Context, _ migrate.MigrateConfig) (string, error) {
		migrateCalled = true
		return "should-not-run", nil
	}
	defer func() { runMigrateFn = origMigrate }()

	// Defense in depth: even though verifyPhasePrerequisites should stop
	// the wizard before it ever reaches phaseExtract again, also stub
	// out real extraction so a bug in the restart-offer plumbing can
	// never make this test perform a live network call / retry loop.
	origExtract := runExtractFn
	extractCalled := false
	runExtractFn = func(_ context.Context, _ extract.ExtractConfig) ([]string, error) {
		extractCalled = true
		return nil, fmt.Errorf("extraction must not run in this test")
	}
	defer func() { runExtractFn = origExtract }()

	state := &WizardState{
		Phase:     PhaseMigrate,
		TargetURL: strPtr("https://evil.example.com"),
		ExtractID: strPtr("fake-id"),
	}
	if err := state.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	p := &MockPrompter{
		// resume=yes (so determineStartingPhase is reached at all);
		// restart-from-earlier-phase=no (so runPhaseLoop reports the
		// verifyPhasePrerequisites error instead of looping into
		// offerPhaseRestart's default-choice fallback).
		ConfirmResponses: []bool{true, false},
	}

	err := Run(context.Background(), p, dir)
	if err == nil {
		t.Fatal("expected Run to return an error instead of dispatching into phaseMigrate")
	}
	if migrateCalled {
		t.Error("runMigrateFn must never be called against a fabricated/corrupted state")
	}
	if extractCalled {
		t.Error("runExtractFn must never be called from this fabricated-migrate-state scenario")
	}

	// The persisted state's phase must not have been silently accepted
	// and advanced past — it stays at PhaseMigrate (or is untouched)
	// rather than reaching PhaseComplete.
	loaded, loadErr := Load(dir)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if loaded.Phase == PhaseComplete {
		t.Error("wizard must not reach PhaseComplete from a fabricated migrate state")
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
	assertExtractPhase(t, phase, ok)
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

// --- RunWithSeed final-state carry-forward (#515) ---

// TestRunWithSeedReturnsStateOnSuccess confirms RunWithSeed hands back
// the final in-memory state alongside a nil error on a normal
// completion, so a caller (cmd/gui.go) can use it as the next call's seed.
func TestRunWithSeedReturnsStateOnSuccess(t *testing.T) {
	restoreMig := mockMigrate(nil)
	defer restoreMig()

	dir := t.TempDir()
	writeMinimalCSVs(t, dir)

	state := &WizardState{
		Phase:         PhaseValidate,
		TargetURL:     strPtr(testSQCloudURL),
		EnterpriseKey: strPtr(testEntKey),
		ExtractID:     strPtr("test-extract-01"),
	}
	state.Save(dir)

	p := &MockPrompter{
		ConfirmResponses:  []bool{true, true},
		PasswordResponses: []string{"token"},
	}

	finalState, err := RunWithSeed(context.Background(), p, dir, nil)
	if err != nil {
		t.Fatalf("RunWithSeed: %v", err)
	}
	if finalState == nil {
		t.Fatal("expected non-nil final state")
	}
	if finalState.Phase != PhaseComplete {
		t.Errorf("expected PhaseComplete, got %s", finalState.Phase)
	}
}

// TestRunWithSeedReturnsStateOnCancel confirms RunWithSeed hands back
// whatever state was committed before an in-flight cancellation, even
// though it also returns a non-nil error.
func TestRunWithSeedReturnsStateOnCancel(t *testing.T) {
	dir := t.TempDir()
	state := &WizardState{Phase: PhaseExtract, SourceURL: strPtr(testSQServerURL)}
	state.Save(dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately, before any phase runs

	p := &MockPrompter{ConfirmResponses: []bool{true}} // resume=yes

	finalState, err := RunWithSeed(ctx, p, dir, nil)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if finalState == nil {
		t.Fatal("expected non-nil final state even on cancellation")
	}
	if finalState.Phase != PhaseExtract {
		t.Errorf("expected phase EXTRACT preserved, got %s", finalState.Phase)
	}
	if ptrStr(finalState.SourceURL) != testSQServerURL {
		t.Errorf("SourceURL: got %q, want %q", ptrStr(finalState.SourceURL), testSQServerURL)
	}
}

// TestRunWithSeedCarriesTokenAcrossCancelSimulation reproduces the
// exact GUI bug fixed by #515: SourceToken never touches disk
// (json:"-"), so a second RunWithSeed call against the same exportDir
// would normally lose any in-memory-only override unless the caller
// re-seeds it with the first call's returned state.
func TestRunWithSeedCarriesTokenAcrossCancelSimulation(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // simulate an immediate Cancel click

	seed := &WizardState{SourceToken: strPtr("overridden-token")}
	firstState, err := RunWithSeed(ctx, &MockPrompter{}, dir, seed)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if ptrStr(firstState.SourceToken) != "overridden-token" {
		t.Fatalf("first run: SourceToken = %q, want %q", ptrStr(firstState.SourceToken), "overridden-token")
	}

	// Disk never carried the token — confirm the bug precondition holds.
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.SourceToken != nil {
		t.Fatalf("disk state should never carry SourceToken, got %q", *reloaded.SourceToken)
	}

	// Re-run seeded with the first call's returned state (as
	// cmd/gui.go's OnStartWizard now does) — the override must survive.
	secondCtx, secondCancel := context.WithCancel(context.Background())
	secondCancel()
	secondState, err := RunWithSeed(secondCtx, &MockPrompter{}, dir, firstState)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if ptrStr(secondState.SourceToken) != "overridden-token" {
		t.Errorf("second run: SourceToken = %q, want %q (override lost)", ptrStr(secondState.SourceToken), "overridden-token")
	}
}

// --- Skipped projects display ---

func TestRunWithSkippedProjects(t *testing.T) {
	restoreMig := mockMigrate(nil)
	defer restoreMig()

	dir := t.TempDir()

	writeMinimalCSVs(t, dir)

	state := &WizardState{
		Phase:           PhaseValidate,
		TargetURL:       strPtr(testSQCloudURL),
		EnterpriseKey:   strPtr(testEntKey),
		SkippedProjects: []string{"proj-a", "proj-b"},
		ExtractID:       strPtr("test-extract-01"),
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

// --- Helpers ---

// writeMinimalCSVs writes the six minimal CSV stubs needed by Validate-phase
// tests: one org row plus single-row stubs for projects, templates, profiles,
// gates, and groups.
func writeMinimalCSVs(t *testing.T, dir string) {
	t.Helper()
	orgHeaders := []string{"sonarqube_org_key", "sonarcloud_org_key"}
	writeCSV(t, dir, fileOrganizations, orgHeaders, [][]string{{"org-1", "cloud-1"}})
	writeCSV(t, dir, fileProjects, []string{"key"}, [][]string{{"p1"}})
	writeCSV(t, dir, fileTemplates, []string{"name"}, [][]string{{"t1"}})
	writeCSV(t, dir, fileProfiles, []string{"name"}, [][]string{{"pr1"}})
	writeCSV(t, dir, fileGates, []string{"name"}, [][]string{{"g1"}})
	writeCSV(t, dir, fileGroups, []string{"name"}, [][]string{{"gr1"}})
}

// assertExtractPhase asserts that determineStartingPhase returned ok=true and
// phase==PhaseExtract; fatals with the pre-defined error constants otherwise.
func assertExtractPhase(t *testing.T, phase WizardPhase, ok bool) {
	t.Helper()
	if !ok {
		t.Fatal(testOKTrue)
	}
	if phase != PhaseExtract {
		t.Errorf(errExpectExtract, phase)
	}
}

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
				SourceURL:           strPtr("https://sq"),
				TargetURL:           strPtr("https://sc"),
				EnterpriseKey:       strPtr("ent"),
				SourceToken:         strPtr("sq-tok"),
				TargetToken:         strPtr("sc-tok"),
				PEMFilePath:         strPtr("/cert.pem"),
				KeyFilePath:         strPtr("/cert.key"),
				CertPassword:        strPtr("certsecret"),
				ProjectKeyPattern:   strPtr("BANKING_.+"),
				DefaultOrganization: strPtr("my-org"),
			},
			want: WizardState{
				SourceURL:           strPtr("https://sq"),
				TargetURL:           strPtr("https://sc"),
				EnterpriseKey:       strPtr("ent"),
				SourceToken:         strPtr("sq-tok"),
				TargetToken:         strPtr("sc-tok"),
				PEMFilePath:         strPtr("/cert.pem"),
				KeyFilePath:         strPtr("/cert.key"),
				CertPassword:        strPtr("certsecret"),
				ProjectKeyPattern:   strPtr("BANKING_.+"),
				DefaultOrganization: strPtr("my-org"),
			},
		},
		{
			name: "existing URL wins, seed token still applied",
			state: WizardState{
				SourceURL:     strPtr("disk-sq"),
				TargetURL:     strPtr("disk-sc"),
				EnterpriseKey: strPtr("disk-ent"),
				PEMFilePath:   strPtr("disk-cert.pem"),
			},
			seed: WizardState{
				SourceURL:           strPtr("seed-sq"),
				TargetURL:           strPtr("seed-sc"),
				EnterpriseKey:       strPtr("seed-ent"),
				SourceToken:         strPtr("sq-tok"),
				TargetToken:         strPtr("sc-tok"),
				PEMFilePath:         strPtr("seed-cert.pem"),
				CertPassword:        strPtr("certsecret"),
				DefaultOrganization: strPtr("seed-org"),
			},
			want: WizardState{
				SourceURL:           strPtr("disk-sq"),
				TargetURL:           strPtr("disk-sc"),
				EnterpriseKey:       strPtr("disk-ent"),
				SourceToken:         strPtr("sq-tok"),
				TargetToken:         strPtr("sc-tok"),
				PEMFilePath:         strPtr("disk-cert.pem"), // disk wins, non-secret field
				CertPassword:        strPtr("certsecret"),    // seed always wins, secret field
				DefaultOrganization: strPtr("seed-org"),      // disk was nil, seed fills it
			},
		},
		{
			name: "partial fill — only nil fields touched",
			state: WizardState{
				SourceURL: strPtr("disk-sq"),
			},
			seed: WizardState{
				SourceURL:         strPtr("seed-sq"),
				TargetURL:         strPtr("seed-sc"),
				EnterpriseKey:     strPtr("seed-ent"),
				KeyFilePath:       strPtr("seed-cert.key"),
				ProjectKeyPattern: strPtr("BANKING_.+"),
			},
			want: WizardState{
				SourceURL:         strPtr("disk-sq"),
				TargetURL:         strPtr("seed-sc"),
				EnterpriseKey:     strPtr("seed-ent"),
				KeyFilePath:       strPtr("seed-cert.key"),
				ProjectKeyPattern: strPtr("BANKING_.+"),
			},
		},
		{
			name:  "empty-string seed values are no-ops",
			state: WizardState{},
			seed: WizardState{
				SourceURL:           strPtr(""),
				TargetURL:           strPtr(""),
				EnterpriseKey:       strPtr(""),
				SourceToken:         strPtr(""),
				TargetToken:         strPtr(""),
				PEMFilePath:         strPtr(""),
				KeyFilePath:         strPtr(""),
				CertPassword:        strPtr(""),
				ProjectKeyPattern:   strPtr(""),
				DefaultOrganization: strPtr(""),
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
			assertPtrEqual(t, "PEMFilePath", state.PEMFilePath, c.want.PEMFilePath)
			assertPtrEqual(t, "KeyFilePath", state.KeyFilePath, c.want.KeyFilePath)
			assertPtrEqual(t, "CertPassword", state.CertPassword, c.want.CertPassword)
			assertPtrEqual(t, "ProjectKeyPattern", state.ProjectKeyPattern, c.want.ProjectKeyPattern)
			assertPtrEqual(t, "DefaultOrganization", state.DefaultOrganization, c.want.DefaultOrganization)
		})
	}
}

// #516: IncludeProjectData / IncludeIssueSync follow the same
// "state wins, seed fills nils" rule as the other seeded fields.
func TestMergeSeedIncludeFlags(t *testing.T) {
	trueVal, falseVal := true, false

	state := WizardState{}
	seed := WizardState{IncludeProjectData: &falseVal, IncludeIssueSync: &falseVal}
	mergeSeed(&state, &seed)
	if state.IncludeProjectData == nil || *state.IncludeProjectData {
		t.Errorf("IncludeProjectData: got %v, want false", state.IncludeProjectData)
	}
	if state.IncludeIssueSync == nil || *state.IncludeIssueSync {
		t.Errorf("IncludeIssueSync: got %v, want false", state.IncludeIssueSync)
	}

	stateWithDisk := WizardState{IncludeProjectData: &trueVal}
	seedFalse := WizardState{IncludeProjectData: &falseVal}
	mergeSeed(&stateWithDisk, &seedFalse)
	if stateWithDisk.IncludeProjectData == nil || !*stateWithDisk.IncludeProjectData {
		t.Errorf("IncludeProjectData: got %v, want true (disk wins over seed)", stateWithDisk.IncludeProjectData)
	}
}

// #388/#515: tokens and CertPassword carried in the wizard state must
// never reach .wizard_state.json — they're json:"-" so Save() drops
// them. Resume reloads the file and gets them nil.
func TestWizardState_TokensNotPersisted(t *testing.T) {
	dir := t.TempDir()
	state := &WizardState{
		SourceURL:    strPtr("https://sq"),
		SourceToken:  strPtr("secret-sq"),
		TargetToken:  strPtr("secret-sc"),
		CertPassword: strPtr("secret-cert"),
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
	if reloaded.CertPassword != nil {
		t.Errorf("CertPassword must not persist; got %q on disk", *reloaded.CertPassword)
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
