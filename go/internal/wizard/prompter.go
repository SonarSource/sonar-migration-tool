// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package wizard

import "errors"

// ErrBack is returned by prompt methods when the user clicks the Back button.
var ErrBack = errors.New("back")

// KV is an ordered key-value pair for display (Go maps are unordered).
type KV struct {
	Key   string
	Value string
}

// Prompter abstracts all user-facing I/O so phase handlers can be driven
// by CLI (survey), GUI (Wails), or tests (mock).
type Prompter interface {
	// PromptURL asks for a URL. When validate is true, it checks scheme,
	// hostname, and normalizes trailing slashes.
	PromptURL(message string, validate bool) (string, error)

	// PromptText asks for free-form text with an optional default.
	PromptText(message, defaultVal string) (string, error)

	// PromptPassword asks for a secret (masked input).
	PromptPassword(message string) (string, error)

	// Confirm asks a yes/no question with the given default.
	Confirm(message string, defaultVal bool) (bool, error)

	// ConfirmReview displays key-value details and asks the user to accept.
	// Returns true if the user confirms, false to re-enter.
	ConfirmReview(title string, details []KV) (bool, error)

	// ConfirmExtractScope displays the source credentials for review
	// together with two dependent migration-scope choices (include
	// project data, include issue sync) and asks the user to accept.
	// When includeProjectData is false, includeIssueSync is forced
	// false too, mirroring the migrate.MigrateConfig cascade (#516).
	ConfirmExtractScope(title string, details []KV, defaultIncludeProjectData, defaultIncludeIssueSync bool) (confirmed, includeProjectData, includeIssueSync bool, err error)

	// PromptChoice presents a list of options and returns the 0-based index.
	PromptChoice(message string, options []string) (int, error)

	// SetBackEnabled controls whether the next prompt shows a Back button.
	SetBackEnabled(enabled bool)

	// Display methods (output only, no return).
	DisplayWelcome()
	DisplayPhaseProgress(phase WizardPhase)
	DisplayMessage(msg string)
	DisplayError(msg string)
	DisplayWarning(msg string)
	DisplaySuccess(msg string)
	DisplaySummary(title string, stats []KV)
	DisplayResumeInfo(state *WizardState)
	DisplayWizardComplete()
}
