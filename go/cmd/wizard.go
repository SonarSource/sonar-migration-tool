package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sonar-solutions/sonar-migration-tool/internal/wizard"
	"github.com/spf13/cobra"
)

var wizardCmd = &cobra.Command{
	Use:   "wizard",
	Short: "Interactive guided migration wizard",
	Long:  "Walks through all migration phases interactively: extract, structure, mappings, validate, migrate, and pipelines.",
	RunE:  runWizard,
}

func init() {
	wizardCmd.Flags().String("export_directory", DefaultExportDirectory, "Root directory to output the export")
}

func runWizard(cmd *cobra.Command, args []string) error {
	exportDir, _ := cmd.Flags().GetString("export_directory")

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	p := wizard.NewCLIPrompter()
	err := wizard.Run(ctx, p, exportDir)

	if err != nil && ctx.Err() != nil {
		fmt.Println("\nWizard interrupted. Your progress has been saved.")
		fmt.Println("Run the wizard again to resume from where you left off.")
		return nil
	}
	if err == nil {
		printExportDirNotice(exportDir)
	}
	return err
}
