// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"log/slog"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// validateBranchAnalyzedAfter parses/validates raw (empty = unset, no
// filter) and logs a one-time advisory when the resulting cutoff is more
// than common.BranchAnalyzedAfterWarnAgeDays days in the past, since the
// filter is then unlikely to exclude many branches (#583). Shared by
// extract, migrate, and transfer's cmd-layer config builders so the
// warning is only ever logged from one place.
func validateBranchAnalyzedAfter(raw string) error {
	cutoff, err := common.ParseBranchAnalyzedAfter(raw)
	if err != nil {
		return err
	}
	if cutoff != nil && common.IsBranchAnalyzedAfterStale(*cutoff, time.Now()) {
		slog.Default().Warn("--branch_analyzed_after is more than 2 years in the past; it may not filter out many branches",
			"branch_analyzed_after", raw)
	}
	return nil
}
