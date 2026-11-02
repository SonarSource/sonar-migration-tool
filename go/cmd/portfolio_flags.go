// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"github.com/spf13/cobra"
)

// flagPortfolioKeyRegexp and flagPortfolioNameRegexp slice the portfolios
// extract and migrate process (#666). They are mutually exclusive; the
// exclusion is enforced by common.NewPortfolioFilter wherever the config is
// resolved, so it also covers values that came from a config file.
const (
	flagPortfolioKeyRegexp  = "portfolio_key_regexp"
	flagPortfolioNameRegexp = "portfolio_name_regexp"
)

// registerPortfolioFlags adds --portfolio_key_regexp and
// --portfolio_name_regexp to a command. what is the verb phrase used in the
// usage text, e.g. "extract" or "migrate".
func registerPortfolioFlags(cmd *cobra.Command, what string) {
	f := cmd.Flags()
	f.String(flagPortfolioKeyRegexp, "", "Regexp pattern of the portfolio keys to "+what+", implicitly anchored (^...$) — a plain key matches only itself. Lets a large instance's portfolios be handled in slices. Mutually exclusive with --"+flagPortfolioNameRegexp)
	f.String(flagPortfolioNameRegexp, "", "Regexp pattern of the portfolio names to "+what+", implicitly anchored (^...$). For when portfolio keys are not well normalized. Mutually exclusive with --"+flagPortfolioKeyRegexp)
}
