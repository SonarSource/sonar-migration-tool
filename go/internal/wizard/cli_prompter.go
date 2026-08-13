// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package wizard

import (
	"fmt"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
)

// kvFormat is the format string for aligned key-value display.
const kvFormat = "    %-25s %s\n"

// CLIPrompter implements Prompter using survey/v2 for terminal interaction.
type CLIPrompter struct{}

// NewCLIPrompter returns a new CLIPrompter.
func NewCLIPrompter() *CLIPrompter {
	return &CLIPrompter{}
}

func (p *CLIPrompter) PromptURL(message string, validate bool) (string, error) {
	for {
		var result string
		prompt := &survey.Input{Message: message}
		if err := survey.AskOne(prompt, &result); err != nil {
			return "", err
		}

		result = strings.TrimSpace(result)
		result = normalizeTrailingSlash(result)

		if !validate {
			return result, nil
		}

		if err := validateServerURL(result); err != nil {
			displayColorLine(colorRed, "Error: "+err.Error())
			continue
		}

		if isLocalhostURL(result) {
			displayLocalhostNotice()
		}

		return result, nil
	}
}

func (p *CLIPrompter) PromptText(message, defaultVal string) (string, error) {
	var result string
	prompt := &survey.Input{Message: message, Default: defaultVal}
	if err := survey.AskOne(prompt, &result); err != nil {
		return "", err
	}
	return strings.TrimSpace(result), nil
}

func (p *CLIPrompter) PromptPassword(message string) (string, error) {
	var result string
	prompt := &survey.Password{Message: message}
	if err := survey.AskOne(prompt, &result); err != nil {
		return "", err
	}
	return result, nil
}

func (p *CLIPrompter) Confirm(message string, defaultVal bool) (bool, error) {
	var result bool
	prompt := &survey.Confirm{Message: message, Default: defaultVal}
	if err := survey.AskOne(prompt, &result); err != nil {
		return false, err
	}
	return result, nil
}

func (p *CLIPrompter) ConfirmReview(title string, details []KV) (bool, error) {
	fmt.Printf("\n  %s\n", title)
	for _, kv := range details {
		fmt.Printf(kvFormat, kv.Key+":", kv.Value)
	}
	fmt.Println()
	return p.Confirm("Are these values correct?", false)
}

func (p *CLIPrompter) PromptChoice(message string, options []string) (int, error) {
	var result string
	prompt := &survey.Select{Message: message, Options: options}
	if err := survey.AskOne(prompt, &result); err != nil {
		return 0, err
	}
	for i, opt := range options {
		if opt == result {
			return i, nil
		}
	}
	return 0, nil
}

func (p *CLIPrompter) SetBackEnabled(bool) { /* no-op for CLI */ }

// Display methods.

func (p *CLIPrompter) DisplayWelcome() {
	fmt.Println(welcomeBanner)
}

// DisplayOverallProgress is a no-op for the CLI: terminal users already
// see the #520 "-----> Overall progress" line on stderr from the
// extract/migrate logger itself, so a second rendering here would just
// be duplicate/competing output (#519 is scoped to the GUI).
func (p *CLIPrompter) DisplayOverallProgress(percent float64, eta time.Duration, known bool) {}

func (p *CLIPrompter) DisplayPhaseProgress(phase WizardPhase) {
	idx := PhaseIndex(phase)
	total := PhaseCount()
	name := PhaseDisplayName(phase)
	bar := buildProgressBar(idx, total)
	fmt.Printf("\n%s  [%d/%d] %s\n\n", bar, idx, total, name)
}

func (p *CLIPrompter) DisplayMessage(msg string) {
	fmt.Println(msg)
}

func (p *CLIPrompter) DisplayError(msg string) {
	displayColorLine(colorRed, "Error: "+msg)
}

func (p *CLIPrompter) DisplayWarning(msg string) {
	displayColorLine(colorYellow, "Warning: "+msg)
}

func (p *CLIPrompter) DisplaySuccess(msg string) {
	displayColorLine(colorGreen, msg)
}

func (p *CLIPrompter) DisplaySummary(title string, stats []KV) {
	fmt.Printf("\n  %s\n", title)
	for _, kv := range stats {
		fmt.Printf(kvFormat, kv.Key+":", kv.Value)
	}
	fmt.Println()
}

func (p *CLIPrompter) DisplayResumeInfo(state *WizardState) {
	fmt.Println("\n  Previous wizard session found:")
	fmt.Printf(kvFormat, "Phase:", PhaseDisplayName(state.Phase))
	if state.SourceURL != nil {
		fmt.Printf(kvFormat, "Source URL:", *state.SourceURL)
	}
	if state.TargetURL != nil {
		fmt.Printf(kvFormat, "Target URL:", *state.TargetURL)
	}
	if state.ExtractID != nil {
		fmt.Printf(kvFormat, "Extract ID:", *state.ExtractID)
	}
	fmt.Println()
}

func (p *CLIPrompter) DisplayWizardComplete() {
	displayColorLine(colorGreen, "\nWizard complete! Your migration is finished.")
	fmt.Println("Review the output files in your export directory for details.")
}

// ANSI color codes.
const (
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorReset  = "\033[0m"
)

func displayColorLine(color, msg string) {
	fmt.Printf("%s%s%s\n", color, msg, colorReset)
}

func displayLocalhostNotice() {
	displayColorLine(colorYellow, `
  Note: You entered a localhost URL. Make sure SonarQube Server
  is running on this machine and accessible at the specified port.
`)
}

func buildProgressBar(current, total int) string {
	filled := current
	empty := total - current
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", empty) + "]"
}

const welcomeBanner = `
======================================================
  SonarQube Migration Wizard
======================================================

  This wizard will guide you through migrating your
  SonarQube Server instance to SonarQube Cloud.

  Steps:
    1. Extract   - Export data from SonarQube Server
    2. Structure  - Analyze project organization
    3. Org Map    - Map organizations to Cloud
    4. Mappings   - Generate entity mappings
    5. Validate   - Pre-flight checks
    6. Migrate    - Execute migration

======================================================`
