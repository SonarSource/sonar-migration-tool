// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
)

// discardExecutor returns an Executor with a silent logger and the given
// unsupported-language handling mode.
func discardExecutor(mode string) *Executor {
	return &Executor{
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		UnsupportedLanguages: mode,
	}
}

// targetProfiles builds the SC-profile-by-language map the CE validates
// against, one profile per given language.
func targetProfiles(langs ...string) map[string]scanreport.QProfileInfo {
	m := make(map[string]scanreport.QProfileInfo, len(langs))
	for _, l := range langs {
		m[l] = scanreport.QProfileInfo{Key: "qp-" + l, Name: "Sonar way", Language: l}
	}
	return m
}

func TestParseUnsupportedLanguageMode(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: DefaultUnsupportedLanguages},
		{in: "exclude", want: UnsupportedLanguagesExclude},
		{in: "skip", want: UnsupportedLanguagesSkip},
		{in: "warn", want: UnsupportedLanguagesWarn},
		// Operators type flags by hand: tolerate case and stray whitespace.
		{in: "SKIP", want: UnsupportedLanguagesSkip},
		{in: "  Warn  ", want: UnsupportedLanguagesWarn},
		// An unknown mode must be rejected, never silently defaulted — a
		// typo'd --unsupported_languages=skipp would otherwise transfer
		// data the operator asked to skip.
		{in: "skipp", wantErr: true},
		{in: "none", wantErr: true},
	}
	for _, c := range cases {
		got, err := ParseUnsupportedLanguageMode(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseUnsupportedLanguageMode(%q): expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseUnsupportedLanguageMode(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseUnsupportedLanguageMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The default must be "exclude": with no flag, a project carrying a 3rd-party
// plugin language should still migrate everything it can.
func TestDefaultUnsupportedLanguagesIsExclude(t *testing.T) {
	if DefaultUnsupportedLanguages != UnsupportedLanguagesExclude {
		t.Errorf("default mode = %q, want %q", DefaultUnsupportedLanguages, UnsupportedLanguagesExclude)
	}
	cfg := MigrateConfig{}
	cfg.applyDefaults()
	if cfg.UnsupportedLanguages != UnsupportedLanguagesExclude {
		t.Errorf("applyDefaults left mode = %q, want %q", cfg.UnsupportedLanguages, UnsupportedLanguagesExclude)
	}
}

// applyDefaults must not preserve an invalid value — it is the last line of
// defence if a config file smuggled one past the CLI validation.
func TestApplyDefaultsNormalizesUnsupportedLanguages(t *testing.T) {
	cfg := MigrateConfig{UnsupportedLanguages: "SKIP"}
	cfg.applyDefaults()
	if cfg.UnsupportedLanguages != UnsupportedLanguagesSkip {
		t.Errorf("mode = %q, want %q", cfg.UnsupportedLanguages, UnsupportedLanguagesSkip)
	}
	bad := MigrateConfig{UnsupportedLanguages: "garbage"}
	bad.applyDefaults()
	if bad.UnsupportedLanguages != DefaultUnsupportedLanguages {
		t.Errorf("invalid mode = %q, want fallback %q", bad.UnsupportedLanguages, DefaultUnsupportedLanguages)
	}
}

func TestDetectUnsupportedLanguages(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "p:a.java", Path: "src/a.java", Language: "java"},
		{Key: "p:b.lua", Path: "src/b.lua", Language: "lua"},
		{Key: "p:c.lua", Path: "src/c.lua", Language: "lua"},
		// Language casing from the source API must not defeat the lookup.
		{Key: "p:d.pas", Path: "app/d.pas", Language: "Delphi"},
		// Files SonarQube assigns no language to are never matched against a
		// quality profile by the CE, so they must not be flagged.
		{Key: "p:LICENSE", Path: "LICENSE", Language: ""},
	}

	got := detectUnsupportedLanguages(components, targetProfiles("java", "xml"))
	if got == nil {
		t.Fatal("expected a finding, got nil")
	}
	if want := []string{"delphi", "lua"}; strings.Join(got.Languages, ",") != strings.Join(want, ",") {
		t.Errorf("Languages = %v, want %v (sorted)", got.Languages, want)
	}
	if got.FileCounts["lua"] != 2 {
		t.Errorf("lua file count = %d, want 2", got.FileCounts["lua"])
	}
	if got.FileCounts["delphi"] != 1 {
		t.Errorf("delphi file count = %d, want 1", got.FileCounts["delphi"])
	}
	if got.TotalFiles != 3 {
		t.Errorf("TotalFiles = %d, want 3 (the empty-language file must not count)", got.TotalFiles)
	}
	if got.Samples["lua"] != "src/b.lua" {
		t.Errorf("lua sample = %q, want the first matching path src/b.lua", got.Samples["lua"])
	}
}

func TestDetectUnsupportedLanguagesAllCovered(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "p:a.java", Path: "src/a.java", Language: "java"},
		{Key: "p:b.xml", Path: "src/b.xml", Language: "xml"},
		{Key: "p:LICENSE", Path: "LICENSE", Language: ""},
	}
	if got := detectUnsupportedLanguages(components, targetProfiles("java", "xml")); got != nil {
		t.Errorf("expected nil finding when every language has a target profile, got %+v", got)
	}
}

func TestUnsupportedLangFindingSummary(t *testing.T) {
	one := detectUnsupportedLanguages(
		[]scanreport.ComponentInput{{Key: "p:b.lua", Path: "src/b.lua", Language: "lua"}},
		targetProfiles("java"))
	if got, want := one.summary(), "lua (1 file, e.g. src/b.lua)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}

	many := detectUnsupportedLanguages([]scanreport.ComponentInput{
		{Key: "p:b.lua", Path: "src/b.lua", Language: "lua"},
		{Key: "p:c.lua", Path: "src/c.lua", Language: "lua"},
	}, targetProfiles("java"))
	if got, want := many.summary(), "lua (2 files, e.g. src/b.lua)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// The explanation is the operator's only chance to understand a silent data
// loss, so assert it carries the four things it must: the languages, the
// reason, the consequence, and the remedy flag.
func TestUnsupportedLangFindingExplanation(t *testing.T) {
	f := detectUnsupportedLanguages(
		[]scanreport.ComponentInput{{Key: "p:b.lua", Path: "src/b.lua", Language: "lua"}},
		targetProfiles("java"))
	msg := f.explanation("org_proj", "main", UnsupportedLanguagesExclude)
	for _, want := range []string{
		"org_proj", "main", "lua", "quality profile",
		"3rd-party", "NO issues and NO branches", "--unsupported_languages=skip",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("explanation missing %q\ngot: %s", want, msg)
		}
	}
}

func TestExcludeComponentsByLanguage(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "p:a.java", Language: "java"},
		{Key: "p:b.lua", Language: "lua"},
		{Key: "p:c.java", Language: "java"},
		{Key: "p:d.lua", Language: "LUA"},
		{Key: "p:LICENSE", Language: ""},
	}
	kept, dropped := excludeComponentsByLanguage(components, map[string]bool{"lua": true})
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	if len(kept) != 3 {
		t.Fatalf("kept = %d components, want 3", len(kept))
	}
	for _, c := range kept {
		if strings.EqualFold(c.Language, "lua") {
			t.Errorf("kept a lua component: %+v", c)
		}
	}
}

func TestApplyUnsupportedLanguagePolicyExclude(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "p:a.java", Path: "src/a.java", Language: "java"},
		{Key: "p:b.lua", Path: "src/b.lua", Language: "lua"},
	}
	kept, langs, skip := applyUnsupportedLanguagePolicy(
		discardExecutor(UnsupportedLanguagesExclude), "org_proj", "main", components, targetProfiles("java"))

	if skip != nil {
		t.Fatalf("exclude mode must still submit the branch, got skip=%+v", skip)
	}
	if strings.Join(langs, ",") != "lua" {
		t.Errorf("languages = %v, want [lua]", langs)
	}
	if len(kept) != 1 || kept[0].Key != "p:a.java" {
		t.Errorf("kept = %+v, want only the java component", kept)
	}
}

func TestApplyUnsupportedLanguagePolicySkip(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "p:a.java", Path: "src/a.java", Language: "java"},
		{Key: "p:b.lua", Path: "src/b.lua", Language: "lua"},
	}
	kept, langs, skip := applyUnsupportedLanguagePolicy(
		discardExecutor(UnsupportedLanguagesSkip), "org_proj", "main", components, targetProfiles("java"))

	if skip == nil {
		t.Fatal("skip mode must return a skip result so no report is submitted")
	}
	if skip.Status != "skipped" {
		t.Errorf("skip.Status = %q, want \"skipped\"", skip.Status)
	}
	if !strings.Contains(skip.Error, "lua") {
		t.Errorf("skip reason must name the language, got %q", skip.Error)
	}
	if strings.Join(skip.UnsupportedLanguages, ",") != "lua" {
		t.Errorf("skip.UnsupportedLanguages = %v, want [lua]", skip.UnsupportedLanguages)
	}
	// Skip must not silently mutate the component set.
	if len(kept) != 2 {
		t.Errorf("kept = %d components, want 2 (unchanged)", len(kept))
	}
	if strings.Join(langs, ",") != "lua" {
		t.Errorf("languages = %v, want [lua] alongside the skip result", langs)
	}
}

func TestApplyUnsupportedLanguagePolicyWarn(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "p:a.java", Path: "src/a.java", Language: "java"},
		{Key: "p:b.lua", Path: "src/b.lua", Language: "lua"},
	}
	kept, langs, skip := applyUnsupportedLanguagePolicy(
		discardExecutor(UnsupportedLanguagesWarn), "org_proj", "main", components, targetProfiles("java"))

	if skip != nil {
		t.Fatalf("warn mode must submit the report unchanged, got skip=%+v", skip)
	}
	if len(kept) != 2 {
		t.Errorf("warn mode changed the component set: kept %d, want 2", len(kept))
	}
	if strings.Join(langs, ",") != "lua" {
		t.Errorf("warn mode must still report the languages, got %v", langs)
	}
}

// Every file being in an unsupported language leaves nothing to submit; the
// branch must be skipped with a reason rather than sending an empty report.
func TestApplyUnsupportedLanguagePolicyExcludeEverything(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "p:a.lua", Path: "src/a.lua", Language: "lua"},
		{Key: "p:b.lua", Path: "src/b.lua", Language: "lua"},
	}
	_, _, skip := applyUnsupportedLanguagePolicy(
		discardExecutor(UnsupportedLanguagesExclude), "org_proj", "main", components, targetProfiles("java"))

	if skip == nil {
		t.Fatal("expected a skip result when every file is excluded")
	}
	if skip.Status != "skipped" {
		t.Errorf("Status = %q, want \"skipped\"", skip.Status)
	}
	if skip.ExcludedFiles != 2 {
		t.Errorf("ExcludedFiles = %d, want 2", skip.ExcludedFiles)
	}
	if !strings.Contains(skip.Error, "every file") {
		t.Errorf("reason should say every file was excluded, got %q", skip.Error)
	}
}

// Safety guard: buildSCProfileMap returns an EMPTY map when the target-side
// quality-profile lookup fails. Detection must then be a no-op — otherwise a
// transient API error would make every language look unsupported and drop the
// project's entire file set.
func TestApplyUnsupportedLanguagePolicyEmptyProfileMapIsNoop(t *testing.T) {
	components := []scanreport.ComponentInput{
		{Key: "p:a.java", Path: "src/a.java", Language: "java"},
		{Key: "p:b.lua", Path: "src/b.lua", Language: "lua"},
	}
	kept, langs, skip := applyUnsupportedLanguagePolicy(
		discardExecutor(UnsupportedLanguagesExclude), "org_proj", "main",
		components, map[string]scanreport.QProfileInfo{})

	if skip != nil {
		t.Fatalf("must not skip on an empty profile map, got %+v", skip)
	}
	if langs != nil {
		t.Errorf("must not report languages on an empty profile map, got %v", langs)
	}
	if len(kept) != 2 {
		t.Errorf("must not drop components on an empty profile map: kept %d, want 2", len(kept))
	}
}

func TestUnsupportedLanguageFromCEError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "verbatim CE rejection",
			in:   "CE task failed: Report contains a file with language 'lua' but no matching quality profile",
			want: "lua",
		},
		{
			name: "wrapped with trailing detail",
			in: "main branch CE failed for org_proj: CE task failed: Report contains a file with " +
				"language 'delphi' but no matching quality profile. Please check the analysis logs.",
			want: "delphi",
		},
		{
			name: "unrelated CE failure",
			in:   "CE task failed: There was an issue whilst processing the report",
			want: "",
		},
		{
			name: "profile mentioned but not a language mismatch",
			in:   "quality profile could not be restored",
			want: "",
		},
		{name: "empty", in: "", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnsupportedLanguageFromCEError(c.in); got != c.want {
				t.Errorf("UnsupportedLanguageFromCEError(%q) = %q, want %q", c.in, got, c.want)
			}
			if got, want := IsUnsupportedLanguageCEFailure(c.in), c.want != ""; got != want {
				t.Errorf("IsUnsupportedLanguageCEFailure(%q) = %v, want %v", c.in, got, want)
			}
		})
	}
}

// A rejected report used to be a Warn that left the transfer looking clean.
// It must now be an ERROR, and an unsupported-language rejection must name the
// language and the remedy.
func TestLogImportProjectDataFailure(t *testing.T) {
	capture := func(err error) string {
		var buf bytes.Buffer
		e := &Executor{Logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
		logImportProjectDataFailure(e, "org_proj", err)
		return buf.String()
	}

	got := capture(errors.New(
		"main branch CE failed for org_proj: CE task failed: Report contains a file with " +
			"language 'lua' but no matching quality profile"))
	if !strings.Contains(got, "level=ERROR") {
		t.Errorf("must log at ERROR, got: %s", got)
	}
	for _, want := range []string{
		"project data NOT migrated", "'lua'", "3rd-party",
		"--unsupported_languages=exclude", "--unsupported_languages=skip", "language=lua",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q\ngot: %s", want, got)
		}
	}

	generic := capture(errors.New("CE task failed: HTTP 500"))
	if !strings.Contains(generic, "level=ERROR") {
		t.Errorf("generic failure must also log at ERROR, got: %s", generic)
	}
	if !strings.Contains(generic, "no issues and no branches were migrated") {
		t.Errorf("generic failure must still state the impact, got: %s", generic)
	}
	if strings.Contains(generic, "unsupported_languages") {
		t.Errorf("generic failure must not suggest the language remedy, got: %s", generic)
	}
}

// The task summary is the operator's at-a-glance signal. Before #474 a project
// whose report was rejected incremented nothing, so importProjectData reported
// no outcome at all and a transfer that migrated zero issues looked clean.
func TestRecordImportOutcome(t *testing.T) {
	newExec := func(buf *bytes.Buffer) *Executor {
		return &Executor{Logger: slog.New(slog.NewTextHandler(buf, nil))}
	}

	var okBuf bytes.Buffer
	okCounter := NewTaskCounter("importProjectData")
	recordImportOutcome(newExec(&okBuf), okCounter, "org_proj", nil)
	if s, f := okCounter.succeeded.Load(), okCounter.failed.Load(); s != 1 || f != 0 {
		t.Errorf("success: succeeded=%d failed=%d, want 1/0", s, f)
	}
	if okBuf.Len() != 0 {
		t.Errorf("success must not log: %s", okBuf.String())
	}

	var failBuf bytes.Buffer
	failCounter := NewTaskCounter("importProjectData")
	recordImportOutcome(newExec(&failBuf), failCounter, "org_proj",
		errors.New("CE task failed: Report contains a file with language 'lua' but no matching quality profile"))
	if s, f := failCounter.succeeded.Load(), failCounter.failed.Load(); s != 0 || f != 1 {
		t.Errorf("failure: succeeded=%d failed=%d, want 0/1", s, f)
	}
	if !strings.Contains(failBuf.String(), "level=ERROR") {
		t.Errorf("failure must be logged at ERROR: %s", failBuf.String())
	}
}

func TestResolveUnsupportedLanguages(t *testing.T) {
	if got := resolveUnsupportedLanguages("", "skip"); got != "skip" {
		t.Errorf("got %q, want the first non-empty value \"skip\"", got)
	}
	if got := resolveUnsupportedLanguages("warn", "skip"); got != "warn" {
		t.Errorf("got %q, want the section value \"warn\" to win", got)
	}
	if got := resolveUnsupportedLanguages("  ", ""); got != "" {
		t.Errorf("got %q, want \"\" when nothing is set", got)
	}
}
