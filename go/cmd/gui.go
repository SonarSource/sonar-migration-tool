// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/sonar-solutions/sonar-migration-tool/internal/gui"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/wizard"
	"github.com/spf13/cobra"
)

var guiCmd = &cobra.Command{
	Use:   "gui",
	Short: "Launch browser-based GUI for the migration wizard",
	Long:  "Starts a local HTTP server and opens the migration wizard in your default browser.",
	RunE:  runGUI,
}

func init() {
	guiCmd.Flags().String("export_directory", DefaultExportDirectory, "Root directory to output the export")
	guiCmd.Flags().String("addr", "localhost:0", "Address to bind the HTTP server (default: random port)")
	guiCmd.Flags().Bool("no-browser", false, "Do not open the browser automatically")
	guiCmd.Flags().StringP("config", "c", "", "Path to JSON configuration file (same shape as extract / migrate / transfer). Pre-fills the wizard form with the URLs, enterprise key, and tokens it carries.")
}

func runGUI(cmd *cobra.Command, args []string) error {
	exportDir, seed, err := resolveGUIDefaults(cmd)
	if err != nil {
		return err
	}
	addr, _ := cmd.Flags().GetString("addr")
	noBrowser, _ := cmd.Flags().GetBool("no-browser")

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tmpl, err := gui.ParseTemplates()
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}

	hub := gui.NewHub(nil)
	srv := gui.NewServer(addr, exportDir, hub, tmpl)

	var (
		wizMu     sync.Mutex
		wizCancel context.CancelFunc
		wizActive bool
	)

	hub.OnStartWizard = func() {
		wizMu.Lock()
		defer wizMu.Unlock()
		if wizActive {
			hub.Send(gui.ServerMessage{
				Type:    gui.TypeDisplayWarning,
				Message: "Wizard is already running.",
			})
			return
		}
		wizActive = true

		wizCtx, wCancel := context.WithCancel(ctx)
		wizCancel = wCancel

		prompter := gui.NewWebPrompter(wizCtx, hub.Send)
		hub.SetPrompter(prompter)
		hub.Send(gui.ServerMessage{Type: gui.TypeWizardStarted})

		// currentSeed snapshots the shared `seed` variable under wizMu;
		// runWizardAsync writes the run's final state back into the same
		// variable (via &seed) once it finishes, so the next "Start
		// Wizard" click carries forward every override made this run —
		// including in-memory-only tokens/cert password — instead of
		// reverting to the original --config values after Cancel (#515).
		currentSeed := seed
		go runWizardAsync(wizCtx, prompter, hub, exportDir, currentSeed, &seed, &wizMu, &wizActive)
	}

	hub.OnCancelWizard = func() {
		wizMu.Lock()
		defer wizMu.Unlock()
		if wizCancel != nil {
			wizCancel()
		}
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	if !noBrowser {
		go openBrowserWhenReady(ctx, srv)
	}

	return <-errCh
}

func runWizardAsync(ctx context.Context, prompter *gui.WebPrompter, hub *gui.Hub, exportDir string, seed *wizard.WizardState, seedOut **wizard.WizardState, mu *sync.Mutex, active *bool) {
	finalState, err := wizard.RunWithSeed(ctx, prompter, exportDir, seed)

	mu.Lock()
	*active = false
	*seedOut = finalState
	mu.Unlock()

	msg := gui.ServerMessage{Type: gui.TypeWizardFinished}
	if err != nil && ctx.Err() == nil {
		errStr := err.Error()
		msg.Error = &errStr
	} else if err == nil {
		if state, stateErr := wizard.Load(exportDir); stateErr == nil && state.TargetURL != nil {
			msg.TargetURL = *state.TargetURL
		}
	}
	hub.Send(msg)
}

// resolveGUIDefaults reads --config (if any) plus --export_directory
// and returns the export dir to use plus a wizard seed for every
// wizard-relevant setting the config carries: URLs, tokens, enterprise
// key, cert settings, default organization, project key pattern, and
// the migration-scope checkboxes (#388, #515). Returns (exportDir,
// nil, nil) when --config is absent — the wizard then starts with a
// blank form, preserving the pre-#388 behaviour.
//
// Precedence:
//   - --export_directory wins over config's export_directory when
//     explicitly set on the CLI (matches the transfer command).
//   - The seed is never persisted to disk in full — tokens travel
//     in-memory only; URL fields end up on disk only when the wizard
//     itself records them during a phase.
func resolveGUIDefaults(cmd *cobra.Command) (string, *wizard.WizardState, error) {
	exportDir, _ := cmd.Flags().GetString("export_directory")
	configFile, _ := cmd.Flags().GetString("config")
	if configFile == "" {
		return exportDir, nil, nil
	}

	extractCfg, err := extract.LoadExtractConfigFile(configFile)
	if err != nil {
		return "", nil, fmt.Errorf("loading --config: %w", err)
	}
	migrateCfg, err := migrate.LoadMigrateConfigFile(configFile)
	if err != nil {
		return "", nil, fmt.Errorf("loading --config: %w", err)
	}

	if !cmd.Flags().Changed("export_directory") {
		switch {
		case extractCfg.ExportDirectory != "":
			exportDir = extractCfg.ExportDirectory
		case migrateCfg.ExportDirectory != "":
			exportDir = migrateCfg.ExportDirectory
		}
	}

	seed := &wizard.WizardState{}
	if extractCfg.URL != "" {
		v := extractCfg.URL
		seed.SourceURL = &v
	}
	if extractCfg.Token != "" {
		v := extractCfg.Token
		seed.SourceToken = &v
	}
	if migrateCfg.URL != "" {
		v := migrateCfg.URL
		seed.TargetURL = &v
	}
	if migrateCfg.Token != "" {
		v := migrateCfg.Token
		seed.TargetToken = &v
	}
	if migrateCfg.EnterpriseKey != "" {
		v := migrateCfg.EnterpriseKey
		seed.EnterpriseKey = &v
	}
	// #516: extractCfg and migrateCfg parse the same unified top-level
	// skip_project_data_migration / skip_issue_sync config keys, so
	// extractCfg is used as the single source for the wizard's
	// migration-scope checkboxes.
	includeProjectData := !extractCfg.SkipProjectDataMigration
	seed.IncludeProjectData = &includeProjectData
	includeIssueSync := !extractCfg.SkipIssueSync
	seed.IncludeIssueSync = &includeIssueSync

	// #515: source cert settings and target default organization are
	// already parsed by LoadExtractConfigFile / LoadMigrateConfigFile
	// for every documented config shape — just carry them into the seed.
	if extractCfg.PEMFilePath != "" {
		v := extractCfg.PEMFilePath
		seed.PEMFilePath = &v
	}
	if extractCfg.KeyFilePath != "" {
		v := extractCfg.KeyFilePath
		seed.KeyFilePath = &v
	}
	if extractCfg.CertPassword != "" {
		v := extractCfg.CertPassword
		seed.CertPassword = &v
	}
	if migrateCfg.DefaultOrganization != "" {
		v := migrateCfg.DefaultOrganization
		seed.DefaultOrganization = &v
	}

	// #515: project_key has no parser in extract/migrate's config
	// loaders — it's transfer's top-level, project-scoping field
	// (#529). Reuse transfer's overlay loader directly (same package,
	// no new export) to pre-fill the wizard's project-key pattern.
	overlay, err := loadTransferOverlay(configFile)
	if err != nil {
		return "", nil, fmt.Errorf("loading --config: %w", err)
	}
	if overlay.ProjectKey != "" {
		v := overlay.ProjectKey
		seed.ProjectKeyPattern = &v
	}

	return exportDir, seed, nil
}

func openBrowserWhenReady(ctx context.Context, srv *gui.Server) {
	select {
	case <-srv.Ready():
		u := srv.URL()
		log.Printf("gui: opening browser at %s", u)
		if err := gui.OpenBrowser(u); err != nil {
			log.Printf("gui: could not open browser: %v (open %s manually)", err, u)
		}
	case <-ctx.Done():
	}
}
