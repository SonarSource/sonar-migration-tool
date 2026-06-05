// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"fmt"
	"os"
	"path/filepath"
)

// GenerateReports collects migration data from runDir once and renders BOTH a
// PDF and a Markdown summary report into outputDir. exportDir is the root
// export directory used to locate extract data.
//
// outputDir and exportDir each default to filepath.Dir(runDir) when empty.
//
// It returns the paths to the generated PDF and Markdown files.
func GenerateReports(runDir, outputDir, exportDir string) (pdfPath, mdPath string, err error) {
	if outputDir == "" {
		outputDir = filepath.Dir(runDir)
	}
	if exportDir == "" {
		exportDir = filepath.Dir(runDir)
	}

	summary, err := CollectSummary(runDir, exportDir)
	if err != nil {
		return "", "", fmt.Errorf("collecting summary: %w", err)
	}

	pdfBytes, err := RenderPDF(summary)
	if err != nil {
		return "", "", fmt.Errorf("rendering PDF: %w", err)
	}

	pdfPath = filepath.Join(outputDir, "migration_summary.pdf")
	if err := os.WriteFile(pdfPath, pdfBytes, 0o644); err != nil {
		return "", "", fmt.Errorf("writing PDF: %w", err)
	}

	mdBytes, err := RenderMarkdown(summary)
	if err != nil {
		return "", "", fmt.Errorf("rendering Markdown: %w", err)
	}

	mdPath = filepath.Join(outputDir, "migration_summary.md")
	if err := os.WriteFile(mdPath, mdBytes, 0o644); err != nil {
		return "", "", fmt.Errorf("writing Markdown: %w", err)
	}

	return pdfPath, mdPath, nil
}

// GeneratePDFReport is a back-compat wrapper around GenerateReports that
// returns only the generated PDF path. outputDir and exportDir each default to
// filepath.Dir(runDir) when empty.
func GeneratePDFReport(runDir, outputDir, exportDir string) (string, error) {
	pdf, _, err := GenerateReports(runDir, outputDir, exportDir)
	return pdf, err
}
