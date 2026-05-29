package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/sonar-solutions/sonar-migration-tool/internal/gui"
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
}

func runGUI(cmd *cobra.Command, args []string) error {
	exportDir, _ := cmd.Flags().GetString("export_directory")
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

		go runWizardAsync(wizCtx, prompter, hub, exportDir, &wizMu, &wizActive)
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

func runWizardAsync(ctx context.Context, prompter *gui.WebPrompter, hub *gui.Hub, exportDir string, mu *sync.Mutex, active *bool) {
	err := wizard.Run(ctx, prompter, exportDir)

	mu.Lock()
	*active = false
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
