// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"log/slog"
	"os"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/redact"
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
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		debug, _ := cmd.Flags().GetBool("debug")
		configureDefaultLogger(debug)
		if err := applyRedactLogs(cmd); err != nil {
			return err
		}
		logStartupVersion()
		// Derive a soft heap ceiling from the cgroup / system memory so
		// large migrations collect early instead of being OOM-killed.
		// No-op when GOMEMLIMIT is already set.
		common.ApplyMemoryLimit(slog.Default())
		if debug {
			slog.Default().Debug("debug mode enabled", "command", cmd.Name())
		}
		return nil
	},
}

func init() {
	rootCmd.SetVersionTemplate(versionTemplate)
	rootCmd.SetHelpTemplate(helpTemplate)
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug-level logging (verbose request payloads on commands that hit the API)")
	rootCmd.PersistentFlags().String(flagRedactLogs, "true", redactLogsUsage)
	addCommands()
}

func addCommands() {
	rootCmd.AddCommand(
		transferCmd,
		wizardCmd,
		guiCmd,
		extractCmd,
		reportCmd,
		structureCmd,
		mappingsCmd,
		migrateCmd,
		syncIssuesCmd,
		predictiveReportCmd,
		resetCmd,
		analysisReportCmd,
		regtestCmd,
	)
}

// configureDefaultLogger installs a stderr text handler at the requested
// level as the slog default, so every subcommand picks up --debug without
// each one having to wire its own logger.
func configureDefaultLogger(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(redact.NewHandler(h)))
}

func logStartupVersion() {
	slog.Default().Info(version.ToolName+" starting", "version", version.Version)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
