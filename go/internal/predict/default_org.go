// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// orgCSVFileName is the organization mapping file every predict
// synthesizer joins against. Mirrors migrate's own constant.
const orgCSVFileName = "organizations.csv"

// applyDefaultOrg stamps defaultOrg onto every organizations.csv row
// whose sonarcloud_org_key is blank, so the predictive report predicts
// what a real `migrate` with the same config would actually do (#566).
//
// Before this existed, a unified config that named its target only via
// target.default_organization left the column empty, and both
// shouldSkipOrg here and collectSkipped in the summary package read an
// empty cell as a deliberate org skip: every project, group and
// permission template was reported as "Organization skipped" and the
// report claimed 0 entities would migrate, the exact opposite of what
// migrate would have done.
//
// The rules mirror migrate.applyOrgMapping (#281) with one deliberate
// difference: no SonarQube Cloud round-trip. migrate calls
// validateOrgExists before writing, to stop a typo reaching disk (#550);
// predictive-report must not, because it promises to make no Cloud API
// calls at all (#235). Only the write half, migrate.WriteOrgCSVWithDefault,
// is reused here, and it is pure local file I/O.
//
//   - Any row already mapped: the CSV is left untouched. A supplied
//     default is ignored with a WARN, same as migrate — except when
//     every row already carries exactly that value, which is the normal
//     outcome of `structure --config` with the same config file and
//     would otherwise print a WARN about ignoring a value that is
//     already in force.
//   - No row mapped, default supplied: every row is rewritten with it.
//   - No row mapped, no default: WARN, because the report is about to
//     say nothing will migrate, and a blank mapping is far more often a
//     setup mistake than a real intent to skip everything.
//
// A missing or empty organizations.csv is not this function's problem;
// BuildPredictiveRun reports that on its own.
func applyDefaultOrg(exportDir, defaultOrg string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	rows, err := structure.LoadCSV(exportDir, orgCSVFileName)
	if err != nil {
		return fmt.Errorf("loading %s: %w", orgCSVFileName, err)
	}
	if len(rows) == 0 {
		return nil
	}
	csvPath := filepath.Join(exportDir, orgCSVFileName)

	mapped := false
	// allMatchDefault stays true only when every row — blanks included —
	// already carries defaultOrg, i.e. applying it would change nothing.
	allMatchDefault := true
	for _, row := range rows {
		val, _ := row["sonarcloud_org_key"].(string)
		val = strings.TrimSpace(val)
		if val != "" {
			mapped = true
		}
		if val != defaultOrg {
			allMatchDefault = false
		}
	}

	if mapped {
		if defaultOrg != "" && !allMatchDefault {
			logger.Warn("Since organizations.csv mapping is defined, the provided default organization parameter is ignored",
				"default_organization", defaultOrg, "file", csvPath)
		}
		return nil
	}

	if defaultOrg == "" {
		logger.Warn("No organization mapping has been defined and no default organization was supplied — the predictive report will show every project, group and permission template as skipped",
			"file", csvPath,
			"hint", "map sonarcloud_org_key in organizations.csv, or set target.default_organization in the config file / pass --default_organization")
		return nil
	}

	if err := migrate.WriteOrgCSVWithDefault(csvPath, rows, defaultOrg); err != nil {
		return fmt.Errorf("applying default_organization to %s: %w", orgCSVFileName, err)
	}
	logger.Info("organizations.csv was empty — the predictive report assumes every project migrates to the provided default organization",
		"default_organization", defaultOrg, "rows", len(rows), "file", csvPath)
	return nil
}
