package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/sonar-solutions/sonar-migration-tool/internal/analysis"
	"github.com/spf13/cobra"
)

var analysisReportCmd = &cobra.Command{
	Use:   "analysis_report <run_id>",
	Short: "Generate a final analysis report",
	Long:  "Generate a final analysis report CSV from a migration run's requests.log.",
	Args:  cobra.ExactArgs(1),
	RunE:  runAnalysisReport,
}

func init() {
	analysisReportCmd.Flags().String("export_directory", DefaultExportDirectory, "Root directory containing all SonarQube exports")
}

func runAnalysisReport(cmd *cobra.Command, args []string) error {
	runID := args[0]
	exportDir, _ := cmd.Flags().GetString("export_directory")
	runDir := filepath.Join(exportDir, runID)

	rows, err := analysis.GenerateReport(runDir)
	if err != nil {
		return err
	}

	if len(rows) == 0 {
		fmt.Println("No POST requests found in requests.log")
		return nil
	}

	success, failure := countOutcomes(rows)
	fmt.Printf("Analysis Report: %d total, %d success, %d failure\n", len(rows), success, failure)
	fmt.Printf("CSV written to: %s/final_analysis_report.csv\n", runDir)
	printExportDirNotice(exportDir)
	return nil
}

func countOutcomes(rows []analysis.ReportRow) (success, failure int) {
	for _, r := range rows {
		if r.Outcome == "failure" {
			failure++
		} else {
			success++
		}
	}
	return
}
