// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"log/slog"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagProjectKey and flagProjectKeyRegexp are shared by every command that
// takes this anchored-regexp parameter: transfer, extract, migrate, regtest.
// project_key is deprecated in favor of project_key_regexp (#592) — it was
// always a regexp, never a literal single key, and the old name misled
// users into thinking otherwise.
//
// sync-issues deliberately does NOT use this: its own --project_key is an
// unrelated, repeatable literal-key StringSlice (go/cmd/sync_issues.go),
// not a regexp, so renaming it to *_regexp would mislabel it.
const (
	flagProjectKey       = "project_key"
	flagProjectKeyRegexp = "project_key_regexp"
)

// registerProjectKeyFlags adds --project_key_regexp, plus the deprecated
// --project_key alias, to f. usage is the full help text shown for
// --project_key_regexp; the deprecated flag gets no usage text of its own
// (same convention as the --url/--token aliases, e.g. go/cmd/extract.go's
// init()) since MarkDeprecated hides it from --help anyway and its
// runtime warning message carries the useful text instead.
func registerProjectKeyFlags(f *pflag.FlagSet, usage string) {
	f.String(flagProjectKeyRegexp, "", usage)
	f.String(flagProjectKey, "", "")
	_ = f.MarkDeprecated(flagProjectKey, "use --"+flagProjectKeyRegexp+" instead")
}

// resolveProjectKeyFlags reads both flags off cmd (only when Changed, so
// an untouched flag never clobbers a config-file value) and returns the
// effective pattern, "" when neither was passed. --project_key_regexp
// wins when both are set. Cobra's own MarkDeprecated mechanism already
// warns when --project_key is used at all; this only adds the
// precedence warning for the "both set" case, which cobra doesn't know
// about (#592).
func resolveProjectKeyFlags(cmd *cobra.Command) string {
	legacy := ""
	if cmd.Flags().Changed(flagProjectKey) {
		legacy, _ = cmd.Flags().GetString(flagProjectKey)
	}
	newer := ""
	if cmd.Flags().Changed(flagProjectKeyRegexp) {
		newer, _ = cmd.Flags().GetString(flagProjectKeyRegexp)
	}
	if legacy != "" && newer != "" {
		slog.Default().Warn("both --" + flagProjectKey + " and --" + flagProjectKeyRegexp +
			" are set; --" + flagProjectKeyRegexp + " takes precedence")
	}
	if newer != "" {
		return newer
	}
	return legacy
}

// resolveProjectKeyFlagsInto applies resolveProjectKeyFlags onto *target,
// only touching it when at least one of the two flags was passed — the
// drop-in replacement for a single-flag overrideString(cmd, "project_key",
// target) call site.
func resolveProjectKeyFlagsInto(cmd *cobra.Command, target *string) {
	if v := resolveProjectKeyFlags(cmd); v != "" {
		*target = v
	}
}
