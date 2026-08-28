// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package wizard

import (
	"context"
	"fmt"
	"os"
)

// Run is the main entry point for the wizard. It loads state, handles
// resume, and runs phases sequentially until complete or interrupted.
func Run(ctx context.Context, p Prompter, exportDir string) error {
	_, err := RunWithSeed(ctx, p, exportDir, nil)
	return err
}

// RunWithSeed is the same as Run but pre-fills the in-memory state
// with values from `seed` before prompting. Disk state wins over the
// seed — re-running `gui --config <file>` against a partially
// completed wizard never silently rewrites progress. Tokens carried
// in the seed (SourceToken / TargetToken / CertPassword) are merged
// into the in-memory state only; they are NEVER persisted to
// .wizard_state.json. The cmd/gui.go config-load path uses this to
// honour `--config` per issue #388.
//
// The final in-memory *WizardState is returned alongside the error —
// on success, early exit, or cancellation — so a caller that
// re-invokes RunWithSeed within the same process (cmd/gui.go's
// OnStartWizard, #515) can pass it back in as the next call's seed.
// That carries every override forward — including in-memory-only
// tokens/CertPassword that never touch disk — for the lifetime of the
// running process, fixing overrides otherwise being lost after Cancel.
func RunWithSeed(ctx context.Context, p Prompter, exportDir string, seed *WizardState) (*WizardState, error) {
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return seed, fmt.Errorf("creating export directory: %w", err)
	}

	p.DisplayWelcome()

	state, err := Load(exportDir)
	if err != nil {
		return seed, fmt.Errorf("loading wizard state: %w", err)
	}
	if seed != nil {
		mergeSeed(state, seed)
	}

	state, shouldContinue := handleResume(p, state, exportDir)
	if !shouldContinue {
		return state, nil
	}

	startPhase, ok := determineStartingPhase(p, state, exportDir)
	if !ok {
		return state, nil
	}

	err = runPhaseLoop(ctx, p, state, exportDir, startPhase)
	return state, err
}

// mergeDiskWins returns seed when dst is unset (nil) and seed carries a
// non-empty value, else it returns dst unchanged — the disk-wins rule
// used for every non-secret seeded field: a previously-completed phase
// keeps the value it recorded even if a new --config supplies a
// different one.
func mergeDiskWins(dst, seed *string) *string {
	if dst == nil && seed != nil && *seed != "" {
		return seed
	}
	return dst
}

// mergeSeedWins returns seed whenever it carries a non-empty value,
// else dst unchanged. Used for the in-memory-only secret fields
// (tokens, CertPassword) that never reach disk (json:"-"): disk can
// never have a competing value, so "disk wins" degenerates to "seed
// wins when present".
func mergeSeedWins(dst, seed *string) *string {
	if seed != nil && *seed != "" {
		return seed
	}
	return dst
}

// mergeSeed copies any field from seed into state when state's
// corresponding field is unset. See mergeDiskWins / mergeSeedWins for
// the two precedence rules applied.
func mergeSeed(state, seed *WizardState) {
	state.SourceURL = mergeDiskWins(state.SourceURL, seed.SourceURL)
	state.TargetURL = mergeDiskWins(state.TargetURL, seed.TargetURL)
	state.EnterpriseKey = mergeDiskWins(state.EnterpriseKey, seed.EnterpriseKey)
	state.PEMFilePath = mergeDiskWins(state.PEMFilePath, seed.PEMFilePath)
	state.KeyFilePath = mergeDiskWins(state.KeyFilePath, seed.KeyFilePath)
	state.ProjectKeyPattern = mergeDiskWins(state.ProjectKeyPattern, seed.ProjectKeyPattern)
	state.DefaultOrganization = mergeDiskWins(state.DefaultOrganization, seed.DefaultOrganization)

	if state.IncludeProjectData == nil && seed.IncludeProjectData != nil {
		state.IncludeProjectData = seed.IncludeProjectData
	}
	if state.IncludeIssueSync == nil && seed.IncludeIssueSync != nil {
		state.IncludeIssueSync = seed.IncludeIssueSync
	}

	state.SourceToken = mergeSeedWins(state.SourceToken, seed.SourceToken)
	state.TargetToken = mergeSeedWins(state.TargetToken, seed.TargetToken)
	state.CertPassword = mergeSeedWins(state.CertPassword, seed.CertPassword)
}

// handleResume prompts the user when a previous session exists.
// Returns the (possibly reset) state and whether to continue.
func handleResume(p Prompter, state *WizardState, exportDir string) (*WizardState, bool) {
	if state.Phase == PhaseInit {
		return state, true
	}

	p.DisplayResumeInfo(state)

	resume, err := p.Confirm("Resume from previous session?", true)
	if err != nil {
		return state, false
	}
	if resume {
		return state, true
	}

	startNew, err := p.Confirm("Start a new wizard session? (This will reset progress.)", false)
	if err != nil {
		return state, false
	}
	if startNew {
		fresh := NewWizardState()
		fresh.Save(exportDir)
		return fresh, true
	}

	return state, false
}

// determineStartingPhase figures out which phase to begin with.
func determineStartingPhase(p Prompter, state *WizardState, exportDir string) (WizardPhase, bool) {
	if state.Phase == PhaseInit {
		return PhaseExtract, true
	}

	if state.Phase == PhaseComplete {
		p.DisplaySuccess("Previous migration completed successfully.")
		startNew, err := p.Confirm("Start a new migration?", false)
		if err != nil || !startNew {
			return "", false
		}
		fresh := NewWizardState()
		fresh.Save(exportDir)
		return PhaseExtract, true
	}

	return state.Phase, true
}

// runPhaseLoop executes phases sequentially from startPhase to completion.
func runPhaseLoop(ctx context.Context, p Prompter, state *WizardState, exportDir string, startPhase WizardPhase) error {
	currentPhase := startPhase

	for currentPhase != PhaseComplete {
		if err := ctx.Err(); err != nil {
			state.Save(exportDir)
			return err
		}

		p.DisplayPhaseProgress(currentPhase)

		if err := runPhaseHandler(ctx, p, state, exportDir, currentPhase); err != nil {
			state.Save(exportDir)
			restartPhase, ok := offerPhaseRestart(p, currentPhase)
			if ok {
				resetPhaseState(state, restartPhase)
				state.Phase = restartPhase
				currentPhase = restartPhase
				continue
			}
			return fmt.Errorf("phase %s: %w", PhaseDisplayName(currentPhase), err)
		}

		currentPhase = state.Phase
	}

	state.Phase = PhaseComplete
	state.Save(exportDir)
	if len(state.SkippedProjects) > 0 {
		p.DisplayWarning(fmt.Sprintf("%d project(s) were skipped during extraction (insufficient privileges):", len(state.SkippedProjects)))
		for _, key := range state.SkippedProjects {
			p.DisplayMessage("  - " + key)
		}
	}
	p.DisplayWizardComplete()
	return nil
}

// runPhaseHandler dispatches to the correct phase function.
func runPhaseHandler(ctx context.Context, p Prompter, state *WizardState, exportDir string, phase WizardPhase) error {
	switch phase {
	case PhaseExtract:
		return phaseExtract(ctx, p, state, exportDir)
	case PhaseStructure:
		return phaseStructure(ctx, p, state, exportDir)
	case PhaseOrgMapping:
		return phaseOrgMapping(ctx, p, state, exportDir)
	case PhaseMappings:
		return phaseMappings(ctx, p, state, exportDir)
	case PhaseValidate:
		return phaseValidate(ctx, p, state, exportDir)
	case PhaseMigrate:
		return phaseMigrate(ctx, p, state, exportDir)
	default:
		return fmt.Errorf("unknown phase: %s", phase)
	}
}

// offerPhaseRestart asks the user if they want to restart from a previous phase.
// Returns the selected phase and true, or zero-value and false if declined.
func offerPhaseRestart(p Prompter, failedPhase WizardPhase) (WizardPhase, bool) {
	restart, err := p.Confirm("Restart from a previous phase?", true)
	if err != nil || !restart {
		return "", false
	}

	options := phasesUpTo(failedPhase)
	if len(options) == 0 {
		return "", false
	}

	idx, err := p.PromptChoice("Which phase?", options)
	if err != nil {
		return "", false
	}

	return phaseByIndex(idx), true
}
