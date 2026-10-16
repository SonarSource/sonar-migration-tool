// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import "log/slog"

// deprecatedByExtractShortPrefix marks a command as deprecated in the
// root command's -h listing. cobra's own Command.Deprecated is not used
// because it hides the command from -h, and #503 wants the listing to
// say the command is deprecated instead.
const deprecatedByExtractShortPrefix = "[DEPRECATED, extract now does this] "

// warnDeprecatedByExtract logs that the structure or mappings command is
// deprecated. Each command calls it when it starts and again when it
// ends (#503), so the notice is seen whichever end of a long output the
// operator reads.
func warnDeprecatedByExtract(command string) {
	slog.Default().Warn("this command is deprecated: extract now runs structure and mappings automatically, so a migration only needs extract, then migrate",
		"command", command)
}
