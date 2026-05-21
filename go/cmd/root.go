package cmd

import (
	"log/slog"

	"github.com/sonar-solutions/sonar-migration-tool/internal/version"
	"github.com/spf13/cobra"
)

const helpTemplate = `{{.Root.Name}} version {{.Root.Version}}

{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}

{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}`

const versionTemplate = `{{.Root.Name}} version {{.Root.Version}}
`

var rootCmd = &cobra.Command{
	Use:     version.ToolName,
	Version: version.Version,
	Short:   "Migrate SonarQube Server instances to SonarQube Cloud",
	Long: `CLI tool for migrating SonarQube Server instances to SonarQube Cloud.
Extracts configuration from a Server instance, transforms it, and applies
it to a Cloud organization. Also updates CI/CD pipelines post-migration.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		logStartupVersion()
	},
}

func init() {
	rootCmd.SetVersionTemplate(versionTemplate)
	rootCmd.SetHelpTemplate(helpTemplate)
	addCommands()
}

func addCommands() {
	rootCmd.AddCommand(
		wizardCmd,
		guiCmd,
		extractCmd,
		reportCmd,
		structureCmd,
		mappingsCmd,
		migrateCmd,
		resetCmd,
		analysisReportCmd,
	)
}

func logStartupVersion() {
	slog.Default().Info(version.ToolName+" starting", "version", version.Version)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
