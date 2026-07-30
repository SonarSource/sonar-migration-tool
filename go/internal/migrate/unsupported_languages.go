// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
)

// Unsupported programming languages in the fabricated scanner report (#474).
//
// SonarQube Server can analyze languages that SonarQube Cloud cannot: a
// language contributed by a 3rd-party (non-SonarSource) plugin has no
// analyzer and therefore no quality profile on the Cloud side. The scanner
// report this tool fabricates stamps every FILE component with the language
// the source server reported for it, while metadata.qprofiles_per_language
// can only carry languages that DO have a target quality profile. When those
// two disagree the SonarQube Cloud Compute Engine rejects the ENTIRE report:
//
//	Report contains a file with language 'X' but no matching quality profile
//
// The project itself, its permissions and its quality gate are already on the
// target by then, so the operator is left with a project that looks migrated
// but carries no issues and no branches at all.
//
// This file provides the detection (which languages in the report have no
// target quality profile), the operator-facing explanation, and the three
// handling modes selected by --unsupported_languages.

const (
	// UnsupportedLanguagesExclude drops the offending files from the scanner
	// report so every other file — and therefore the project's issues,
	// measures and branches — still migrates. The default: it turns a total,
	// silent loss into a reported partial migration.
	UnsupportedLanguagesExclude = "exclude"

	// UnsupportedLanguagesSkip leaves the project's data un-migrated: no
	// scanner report is submitted for any of its branches. The project is
	// reported as skipped with the reason. This is the "option to not
	// transfer such projects" from #474.
	UnsupportedLanguagesSkip = "skip"

	// UnsupportedLanguagesWarn submits the report unchanged — the pre-#474
	// behaviour — but explains up front that the Compute Engine is expected
	// to reject it, and reports the rejection instead of swallowing it.
	UnsupportedLanguagesWarn = "warn"

	// DefaultUnsupportedLanguages is the mode used when none is configured.
	DefaultUnsupportedLanguages = UnsupportedLanguagesExclude
)

// UnsupportedLanguageModes lists the accepted --unsupported_languages values,
// in help-text order.
var UnsupportedLanguageModes = []string{
	UnsupportedLanguagesExclude, UnsupportedLanguagesSkip, UnsupportedLanguagesWarn,
}

// ParseUnsupportedLanguageMode normalises and validates a mode string. An
// empty value resolves to DefaultUnsupportedLanguages so an absent flag and
// an absent config field behave identically.
func ParseUnsupportedLanguageMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		return DefaultUnsupportedLanguages, nil
	}
	for _, valid := range UnsupportedLanguageModes {
		if mode == valid {
			return mode, nil
		}
	}
	return "", fmt.Errorf("invalid unsupported_languages value %q: expected one of %s",
		value, strings.Join(UnsupportedLanguageModes, ", "))
}

// resolveUnsupportedLanguages returns the first non-empty value, letting the
// config-file loader express "section value wins, else top-level value" as a
// plain assignment.
func resolveUnsupportedLanguages(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// unsupportedLanguageMode resolves the executor's configured mode, falling
// back to the default for an unparseable value (RunMigrate validates the
// value up front, so this only guards direct Executor construction).
func (e *Executor) unsupportedLanguageMode() string {
	mode, err := ParseUnsupportedLanguageMode(e.UnsupportedLanguages)
	if err != nil {
		return DefaultUnsupportedLanguages
	}
	return mode
}

// unsupportedLangFinding describes the languages present in one branch's
// components that have no quality profile on the target organization.
type unsupportedLangFinding struct {
	// Languages are the offending language keys, sorted for stable output.
	Languages []string
	// FileCounts maps each offending language to its file count.
	FileCounts map[string]int
	// Samples maps each offending language to one example file path, so the
	// operator can see which part of the project is affected.
	Samples map[string]string
	// TotalFiles is the number of file components across all offending
	// languages — i.e. how many files mode "exclude" would drop.
	TotalFiles int
}

// detectUnsupportedLanguages returns the languages carried by the report's
// file components that are absent from the target organization's quality
// profiles, or nil when every language is covered.
//
// This mirrors exactly what the Compute Engine validates: it compares each
// component's language against metadata.qprofiles_per_language, which
// buildProjectQProfiles derives from targetProfileByLang.
func detectUnsupportedLanguages(components []scanreport.ComponentInput,
	targetProfileByLang map[string]scanreport.QProfileInfo) *unsupportedLangFinding {

	finding := &unsupportedLangFinding{
		FileCounts: make(map[string]int),
		Samples:    make(map[string]string),
	}
	for _, c := range components {
		lang := strings.ToLower(strings.TrimSpace(c.Language))
		if lang == "" {
			// A component with no language is not attributed to any quality
			// profile, so the CE never looks one up for it.
			continue
		}
		if _, ok := targetProfileByLang[lang]; ok {
			continue
		}
		finding.FileCounts[lang]++
		finding.TotalFiles++
		if _, seen := finding.Samples[lang]; !seen {
			sample := c.Path
			if sample == "" {
				sample = c.Key
			}
			finding.Samples[lang] = sample
		}
	}
	if finding.TotalFiles == 0 {
		return nil
	}
	for lang := range finding.FileCounts {
		finding.Languages = append(finding.Languages, lang)
	}
	sort.Strings(finding.Languages)
	return finding
}

// languageSet returns the offending languages as a lookup set.
func (f *unsupportedLangFinding) languageSet() map[string]bool {
	set := make(map[string]bool, len(f.Languages))
	for _, l := range f.Languages {
		set[l] = true
	}
	return set
}

// summary renders the offending languages as a compact, human-readable list:
//
//	lua (1 file, e.g. src/main/Constants.lua); delphi (12 files, e.g. app/Main.pas)
func (f *unsupportedLangFinding) summary() string {
	parts := make([]string, 0, len(f.Languages))
	for _, lang := range f.Languages {
		n := f.FileCounts[lang]
		unit := "files"
		if n == 1 {
			unit = "file"
		}
		part := fmt.Sprintf("%s (%d %s", lang, n, unit)
		if sample := f.Samples[lang]; sample != "" {
			part += ", e.g. " + sample
		}
		parts = append(parts, part+")")
	}
	return strings.Join(parts, "; ")
}

// explanation is the operator-facing "what happened and why" (#474). It is
// deliberately self-contained: it names the languages, says why the target has
// no profile for them, states the consequence, and names the remedy for the
// mode in force.
func (f *unsupportedLangFinding) explanation(projectKey, branch, mode string) string {
	return fmt.Sprintf(
		"project %q branch %q contains %d file(s) in language(s) that have no quality profile "+
			"on the target SonarQube Cloud organization: %s. "+
			"SonarQube Cloud only provides quality profiles for languages its own analyzers support, "+
			"so a language contributed by a 3rd-party SonarQube Server plugin has none. "+
			"The Compute Engine rejects an entire scanner report that contains a file whose language "+
			"has no matching quality profile, which would leave this project on SonarQube Cloud with "+
			"its settings, permissions and quality gate but with NO issues and NO branches. %s",
		projectKey, branch, f.TotalFiles, f.summary(), unsupportedLanguageAction(mode))
}

// unsupportedLanguageAction describes what the tool is doing about the finding
// and how to choose differently.
func unsupportedLanguageAction(mode string) string {
	switch mode {
	case UnsupportedLanguagesSkip:
		return "Skipping this project's data as requested by --unsupported_languages=skip: " +
			"no scanner report is submitted for any of its branches. " +
			"Use --unsupported_languages=exclude to migrate everything except the unsupported files."
	case UnsupportedLanguagesWarn:
		return "Submitting the report unchanged as requested by --unsupported_languages=warn; " +
			"expect the Compute Engine to reject it. " +
			"Use --unsupported_languages=exclude to migrate everything except the unsupported files, " +
			"or --unsupported_languages=skip to skip this project's data entirely."
	default:
		return "Excluding those files from the scanner report (--unsupported_languages=exclude, the default) " +
			"so the rest of the project — its other files, issues, measures and branches — still migrates. " +
			"Use --unsupported_languages=skip to skip this project's data entirely, " +
			"or --unsupported_languages=warn to submit the report unchanged."
	}
}

// skipReason is the short, report-facing sentence recorded on every branch
// when mode "skip" declines to submit a report.
func (f *unsupportedLangFinding) skipReason() string {
	return "Project data not migrated: the project contains files in language(s) with no quality profile " +
		"on the target organization — " + f.summary() +
		" (typically a language provided by a 3rd-party SonarQube Server plugin). " +
		"Skipped by --unsupported_languages=skip."
}

// allFilesExcludedReason is recorded when mode "exclude" removed every file in
// the branch, leaving nothing to submit.
func (f *unsupportedLangFinding) allFilesExcludedReason() string {
	return "Project data not migrated: every file in the branch is in a language with no quality profile " +
		"on the target organization — " + f.summary() +
		" (typically a language provided by a 3rd-party SonarQube Server plugin)."
}

// excludeComponentsByLanguage returns the components whose language is not in
// langs, plus the number dropped. Issues, measures, source text, SCM blame and
// syntax highlighting all resolve through the component-ref map built from the
// returned slice, so dropping a component drops everything attached to it.
func excludeComponentsByLanguage(components []scanreport.ComponentInput,
	langs map[string]bool) ([]scanreport.ComponentInput, int) {

	kept := make([]scanreport.ComponentInput, 0, len(components))
	dropped := 0
	for _, c := range components {
		if langs[strings.ToLower(strings.TrimSpace(c.Language))] {
			dropped++
			continue
		}
		kept = append(kept, c)
	}
	return kept, dropped
}

// applyUnsupportedLanguagePolicy detects unsupported languages in one branch's
// components, explains the situation to the operator, and applies the mode
// selected by --unsupported_languages.
//
// It returns the components that should go into the scanner report (unchanged
// except in "exclude" mode), the offending language keys (nil when every
// language is covered), and a non-nil importResult when the branch must NOT be
// submitted at all.
func applyUnsupportedLanguagePolicy(e *Executor, projectKey, branch string,
	components []scanreport.ComponentInput,
	targetProfileByLang map[string]scanreport.QProfileInfo,
) ([]scanreport.ComponentInput, []string, *importResult) {

	// An empty profile map means the target-side quality-profile lookup failed
	// (buildSCProfileMap logs the error and returns an empty map) or no Cloud
	// client is configured. Every language would then look unsupported, so
	// detection must not run: a transient API error must never drop a whole
	// project's files.
	if len(targetProfileByLang) == 0 {
		return components, nil, nil
	}

	finding := detectUnsupportedLanguages(components, targetProfileByLang)
	if finding == nil {
		return components, nil, nil
	}

	mode := e.unsupportedLanguageMode()
	// Logged at WARN so it appears without --debug: this is the "meaningful
	// explanation of what happened and why" #474 asks for.
	e.Logger.Warn("unsupported programming language: "+finding.explanation(projectKey, branch, mode),
		"project", projectKey, "branch", branch,
		"languages", strings.Join(finding.Languages, ","),
		"files", finding.TotalFiles, "mode", mode)

	switch mode {
	case UnsupportedLanguagesSkip:
		return components, finding.Languages, &importResult{
			Status:               "skipped",
			Error:                finding.skipReason(),
			UnsupportedLanguages: finding.Languages,
		}
	case UnsupportedLanguagesWarn:
		return components, finding.Languages, nil
	default:
		kept, dropped := excludeComponentsByLanguage(components, finding.languageSet())
		if len(kept) == 0 {
			return kept, finding.Languages, &importResult{
				Status:               "skipped",
				Error:                finding.allFilesExcludedReason(),
				UnsupportedLanguages: finding.Languages,
				ExcludedFiles:        dropped,
			}
		}
		e.Logger.Warn("excluded unsupported-language files from the scanner report so the rest of the branch migrates",
			"project", projectKey, "branch", branch,
			"excludedFiles", dropped, "remainingFiles", len(kept),
			"languages", strings.Join(finding.Languages, ","))
		return kept, finding.Languages, nil
	}
}

// ceUnsupportedLanguagePattern matches the Compute Engine's rejection message
// for a file whose language has no quality profile, e.g.
//
//	Report contains a file with language 'lua' but no matching quality profile
var ceUnsupportedLanguagePattern = regexp.MustCompile(`language '([^']+)'`)

// UnsupportedLanguageFromCEError extracts the language key from a Compute
// Engine "no matching quality profile" rejection, or returns "" when the
// message is a different failure. Used by the migration report to explain a
// CE rejection instead of framing it as a generic API error (#474).
func UnsupportedLanguageFromCEError(errMsg string) string {
	lower := strings.ToLower(errMsg)
	if !strings.Contains(lower, "quality profile") {
		return ""
	}
	if !strings.Contains(lower, "no matching") && !strings.Contains(lower, "not found") {
		return ""
	}
	if m := ceUnsupportedLanguagePattern.FindStringSubmatch(errMsg); len(m) == 2 {
		return m[1]
	}
	return ""
}

// IsUnsupportedLanguageCEFailure reports whether a failure came from the
// Compute Engine rejecting a report because a file's language has no
// matching quality profile on the target.
func IsUnsupportedLanguageCEFailure(errMsg string) bool {
	return UnsupportedLanguageFromCEError(errMsg) != ""
}

// recordImportOutcome logs and counts one project's data-import outcome, so the
// end-of-task summary reports `failed=N` instead of staying silent (#474).
func recordImportOutcome(e *Executor, counter *TaskCounter, cloudKey string, err error) {
	if err == nil {
		counter.Success()
		return
	}
	logImportProjectDataFailure(e, cloudKey, err)
	counter.Fail()
}

// logImportProjectDataFailure reports a project whose data import failed.
//
// Before #474 this was a single Warn line that the operator could easily miss:
// the project had already been created on the target with its permissions and
// quality gate, the transfer exited 0, and nothing said that every issue and
// every branch had been dropped. It is now an ERROR, and a Compute Engine
// rejection caused by a language with no target quality profile is named as
// such together with the remedy.
func logImportProjectDataFailure(e *Executor, cloudKey string, err error) {
	if lang := UnsupportedLanguageFromCEError(err.Error()); lang != "" {
		e.Logger.Error("project data NOT migrated: SonarQube Cloud rejected the analysis report because the "+
			"project contains files in language '"+lang+"', which has no quality profile in the target "+
			"organization (typically a language contributed by a 3rd-party SonarQube Server plugin). "+
			"NO issues and NO branches were migrated for this project, even though the project itself, its "+
			"permissions and its quality gate were. Re-run with --unsupported_languages=exclude to migrate "+
			"everything except those files, or --unsupported_languages=skip to skip the project's data.",
			"project", cloudKey, "language", lang, "err", err)
		return
	}
	e.Logger.Error("project data NOT migrated: no issues and no branches were migrated for this project",
		"project", cloudKey, "err", err)
}
