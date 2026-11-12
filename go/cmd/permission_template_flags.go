// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"log/slog"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagPermissionTemplateRegexp and its alias flagPTRegexp select which
// permission templates extract / migrate process, so a large instance can
// migrate them in slices (#665).
const (
	flagPermissionTemplateRegexp = "permission_template_regexp"
	flagPTRegexp                 = "pt_regexp"
)

// registerPermissionTemplateFlags adds --permission_template_regexp and its
// --pt_regexp alias to f. usage is the help text of the main flag; the alias
// points back at it.
func registerPermissionTemplateFlags(f *pflag.FlagSet, usage string) {
	f.String(flagPermissionTemplateRegexp, "", usage)
	f.String(flagPTRegexp, "", "Alias of --"+flagPermissionTemplateRegexp+".")
}

// resolvePermissionTemplateFlagsInto applies whichever of the two flags was
// passed onto *target (only when Changed, so an untouched flag never clobbers
// a config-file value). --permission_template_regexp wins when both are set.
func resolvePermissionTemplateFlagsInto(cmd *cobra.Command, target *string) {
	long, short := "", ""
	if cmd.Flags().Changed(flagPermissionTemplateRegexp) {
		long, _ = cmd.Flags().GetString(flagPermissionTemplateRegexp)
	}
	if cmd.Flags().Changed(flagPTRegexp) {
		short, _ = cmd.Flags().GetString(flagPTRegexp)
	}
	if long != "" && short != "" && long != short {
		slog.Default().Warn("both --" + flagPermissionTemplateRegexp + " and --" + flagPTRegexp +
			" are set; --" + flagPermissionTemplateRegexp + " takes precedence")
	}
	switch {
	case long != "":
		*target = long
	case short != "":
		*target = short
	}
}
