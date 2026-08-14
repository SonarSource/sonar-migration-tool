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
	return RunWithSeed(ctx, p, exportDir, nil)
}

// RunWithSeed is the same as Run but pre-fills the in-memory state
// with values from `seed` before prompting. Disk state wins over the
// seed — re-running `gui --config <file>` against a partially
// completed wizard never silently rewrites progress. Tokens carried
// in the seed (SourceToken / TargetToken) are merged into the
// in-memory state only; they are NEVER persisted to
// .wizard_state.json. The cmd/gui.go config-load path uses this to
// honour `--config` per issue #388.
func RunWithSeed(ctx context.Context, p Prompter, exportDir string, seed *WizardState) error {
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return fmt.Errorf("creating export directory: %w", err)
	}

	p.DisplayWelcome()

	state, err := Load(exportDir)
	if err != nil {
		return fmt.Errorf("loading wizard state: %w", err)
	}
	if seed != nil {
		mergeSeed(state, seed)
	}

	state, shouldContinue := handleResume(p, state, exportDir)
	if !shouldContinue {
		return nil
	}

	startPhase, ok := determineStartingPhase(p, state, exportDir)
	if !ok {
		return nil
	}

	return runPhaseLoop(ctx, p, state, exportDir, startPhase)
}

// mergeSeed copies any field from seed into state when state's
// corresponding field is unset. The disk-wins rule preserves resume
// semantics: a previously-completed phase keeps the values it
// recorded even if a new --config supplies different ones.
func mergeSeed(state, seed *WizardState) {
	if state.SourceURL == nil && seed.SourceURL != nil && *seed.SourceURL != "" {
		state.SourceURL = seed.SourceURL
	}
	if state.TargetURL == nil && seed.TargetURL != nil && *seed.TargetURL != "" {
		state.TargetURL = seed.TargetURL
	}
	if state.EnterpriseKey == nil && seed.EnterpriseKey != nil && *seed.EnterpriseKey != "" {
		state.EnterpriseKey = seed.EnterpriseKey
	}
	if state.IncludeProjectData == nil && seed.IncludeProjectData != nil {
		state.IncludeProjectData = seed.IncludeProjectData
	}
	if state.IncludeIssueSync == nil && seed.IncludeIssueSync != nil {
		state.IncludeIssueSync = seed.IncludeIssueSync
	}
	// Tokens are always seeded when supplied — disk never has them
	// (json:"-"), so the "disk wins" rule degenerates to "seed wins
	// when present" for these two fields.
	if seed.SourceToken != nil && *seed.SourceToken != "" {
		state.SourceToken = seed.SourceToken
	}
	if seed.TargetToken != nil && *seed.TargetToken != "" {
		state.TargetToken = seed.TargetToken
	}
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
