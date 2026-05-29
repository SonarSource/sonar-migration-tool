package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sonar-solutions/sonar-migration-tool/internal/report/maturity"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report/migration"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"github.com/spf13/cobra"
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Generate a migration or maturity report",
	Long:  "Generates a markdown report based on data extracted from one or more SonarQube Server instances.",
	RunE:  runReport,
}

func init() {
	f := reportCmd.Flags()
	f.String("export_directory", DefaultExportDirectory, "Root directory containing all SonarQube exports")
	f.String("report_type", "migration", "Type of report to generate")
	f.String("filename", "", "Filename for the report")
}

func runReport(cmd *cobra.Command, args []string) error {
	exportDir, _ := cmd.Flags().GetString("export_directory")
	reportType, _ := cmd.Flags().GetString("report_type")
	filename, _ := cmd.Flags().GetString("filename")

	mapping, err := structure.GetUniqueExtracts(exportDir)
	if err != nil {
		return fmt.Errorf("scanning extracts: %w", err)
	}
	if len(mapping) == 0 {
		return fmt.Errorf("no extracts found in %s", exportDir)
	}

	var md string
	switch reportType {
	case "migration":
		md = migration.GenerateMigrationReport(exportDir, mapping)
	case "maturity":
		md = maturity.GenerateMaturityReport(exportDir, mapping)
	default:
		return fmt.Errorf("unsupported report type: %s (available: migration, maturity)", reportType)
	}

	if filename == "" {
		filename = reportType
	}
	outPath := filepath.Join(exportDir, filename+".md")
	if err := os.WriteFile(outPath, []byte(md), 0o644); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}

	fmt.Printf("Report written to: %s\n", outPath)
	printExportDirNotice(exportDir)
	return nil
}
