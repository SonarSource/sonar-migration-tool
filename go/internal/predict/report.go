// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package predict

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sonar-solutions/sonar-migration-tool/internal/report/summary"
)

// predictivePDFFilename is the output filename for predictive reports.
// Deliberately distinct from the post-migrate "migration_summary.pdf"
// so an operator can't mistake a prediction for an actual run result.
const predictivePDFFilename = "predictive_migration_summary.pdf"

// GeneratePredictiveReport synthesizes the JSONL outputs a real migrate
// run would have produced under exportDir, then runs the standard
// summary pipeline to build a PDF. Returns the path to the generated
// PDF.
//
// Inputs: extract data + mapping CSVs (organizations.csv, gates.csv,
// projects.csv, ...) under exportDir.
//
// defaultOrg is optional and mirrors migrate's --default_organization /
// target.default_organization (#281): when organizations.csv carries no
// mapping at all, it is stamped onto every row first, so the prediction
// matches the migration it is predicting (#566). An already-mapped CSV
// wins and the value is ignored with a WARN, exactly as in migrate. No
// SonarQube Cloud call is made either way — see applyDefaultOrg.
//
// Output: <exportDir>/predictive_migration_summary.pdf. The Global
// Settings section is included now that #237 added a curated list of
// SQS-only setting keys — settings on that list are reported as
// Skipped (not-on-sqc); everything else is reported as Applied
// (predicted), with the caveat that real-migrate may still fall back
// to project scope or fail at runtime for the unpredictable cases.
func GeneratePredictiveReport(exportDir string, defaultOrg ...string) (string, error) {
	org := ""
	if len(defaultOrg) > 0 {
		org = defaultOrg[0]
	}
	if err := applyDefaultOrg(exportDir, org, slog.Default()); err != nil {
		return "", err
	}

	runDir, err := BuildPredictiveRun(exportDir)
	if err != nil {
		return "", fmt.Errorf("building predictive run: %w", err)
	}

	mig, err := summary.CollectSummary(runDir, exportDir)
	if err != nil {
		return "", fmt.Errorf("collecting summary: %w", err)
	}
	mig.Predictive = true

	pdfBytes, err := summary.RenderPDF(mig)
	if err != nil {
		return "", fmt.Errorf("rendering PDF: %w", err)
	}

	outPath := filepath.Join(exportDir, predictivePDFFilename)
	if err := os.WriteFile(outPath, pdfBytes, 0o644); err != nil {
		return "", fmt.Errorf("writing PDF: %w", err)
	}
	return outPath, nil
}
