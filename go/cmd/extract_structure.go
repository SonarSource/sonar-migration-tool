// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// runExtractStructure runs what the standalone structure and mappings
// commands do, once extract has succeeded, so a migration takes two
// commands (extract, then migrate) instead of four (#503).
//
// Every extract in exportDir is aggregated, exactly as structure does, so
// extracting a second server regenerates the CSVs for both. Before
// organizations.csv and projects.csv are regenerated, the existing copies
// are backed up, because those are the files the operator edits by hand,
// and the sonarcloud_org_key values the operator entered are carried over
// into the regenerated files (#664).
//
// Both steps run whatever --objects selected: every extract extracts the
// DevOps platform data organizations.csv is built from (#688), and mappings
// keeps a CSV whose entities this extract left out.
// orgKey pre-populates sonarcloud_org_key, as structure --config does.
func runExtractStructure(exportDir, orgKey string, now time.Time) error {
	logger := slog.Default()

	// Read the operator's sonarcloud_org_key values before anything is
	// regenerated, so they can be written back into the new files (#664).
	carry, err := structure.ReadCarriedKeys(exportDir)
	if err != nil {
		return extractStepError("structure", exportDir, err)
	}
	backups, err := structure.BackupEditableCSVs(exportDir, now)
	if err != nil {
		return extractStepError("structure", exportDir, err)
	}
	for _, path := range backups {
		logger.Info("regenerating a CSV that already existed: the previous copy is saved", "backup", path)
	}

	stats, err := structure.RunStructureCarryOver(exportDir, carry, orgKey)
	if err != nil {
		return extractStepError("structure", exportDir, err)
	}
	if len(backups) > 0 {
		logger.Info("carried over sonarcloud_org_key values from the previous CSVs", "carried", stats.Carried)
	}
	if len(backups) > 0 && stats.NeedsReview() {
		logger.Warn("some sonarcloud_org_key values could not be carried over: review organizations.csv and projects.csv (the previous copies are saved as .bak files)",
			"not_carried", stats.Dropped, "organizations_without_key", stats.OrgsWithoutKey)
	}
	// Mappings assigns entities to the organizations of their projects, so
	// it has nothing to do when no project was found. That is not an extract
	// failure: an extract of groups alone on a server without readable
	// projects must still succeed (#688).
	if rows, err := structure.LoadCSV(exportDir, "projects.csv"); err == nil && len(rows) == 0 {
		logger.Warn("skipping mappings: no project was extracted, so no entity can be assigned to an organization (check the token can browse the projects)",
			"export_directory", exportDir)
		return nil
	}
	if err := structure.RunMappings(exportDir); err != nil {
		return extractStepError("mappings", exportDir, err)
	}
	return nil
}

// extractStepError says which post-extract step failed and that the
// extracted data itself is complete, so the operator does not re-extract
// for nothing.
func extractStepError(step, exportDir string, err error) error {
	return fmt.Errorf("extract completed, but its %s step failed (the extract data in %s is intact): %w",
		step, exportDir, err)
}
