// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package wizard

import (
	"errors"
	"time"
)

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

	// PromptExtractForm asks for the source URL, admin token, and the
	// two dependent migration-scope choices (include project data,
	// include issue sync) together in a single screen. tokenOptional
	// means a token is already known (e.g. seeded via --config, #388),
	// so the token field may be submitted blank — the caller falls back
	// to the known token when the returned token is empty. When
	// includeProjectData is false, includeIssueSync is forced false
	// too, mirroring the migrate.MigrateConfig cascade. There is no
	// separate review step: submitting the form finalizes the values.
	PromptExtractForm(defaultURL string, tokenOptional bool, defaultIncludeProjectData, defaultIncludeIssueSync bool) (url, token string, includeProjectData, includeIssueSync bool, err error)

	// PromptMigrateForm asks for the target Cloud URL, admin token,
	// enterprise key, and the two migration-scope checkboxes together
	// in a single screen, right before migration executes. Same
	// tokenOptional/no-review-step semantics as PromptExtractForm.
	PromptMigrateForm(defaultURL string, tokenOptional bool, defaultEnterpriseKey string, defaultIncludeProjectData, defaultIncludeIssueSync bool) (url, token, enterpriseKey string, includeProjectData, includeIssueSync bool, err error)

	// PromptChoice presents a list of options and returns the 0-based index.
	PromptChoice(message string, options []string) (int, error)

	// SetBackEnabled controls whether the next prompt shows a Back button.
	SetBackEnabled(enabled bool)

	// Display methods (output only, no return).
	DisplayWelcome()
	DisplayPhaseProgress(phase WizardPhase)

	// DisplayOverallProgress reports the run-wide percent/ETA snapshot
	// computed by common.Tracker (#520) for the extract/migrate step
	// currently in flight. known is false until enough progress has
	// been made to extrapolate an ETA. Fired roughly every 10s plus
	// once more at completion (#519).
	DisplayOverallProgress(percent float64, eta time.Duration, known bool)
	DisplayMessage(msg string)
	DisplayError(msg string)
	DisplayWarning(msg string)
	DisplaySuccess(msg string)
	DisplaySummary(title string, stats []KV)
	DisplayResumeInfo(state *WizardState)
	DisplayWizardComplete()
}
