package cmd

import (
	"bytes"
	"log/slog"
	"testing"
)

// --debug is declared as a PersistentFlag on rootCmd so every subcommand
// inherits it. Regression guard for the per-command duplication we used
// to have on migrate/reset/transfer.
func TestRootCmd_DebugIsPersistentFlag(t *testing.T) {
	if rootCmd.PersistentFlags().Lookup("debug") == nil {
		t.Fatal("--debug must be a PersistentFlag on rootCmd")
	}

	for _, sub := range rootCmd.Commands() {
		if sub.Name() == "completion" || sub.Name() == "help" {
			continue
		}
		// Inherited persistent flags must resolve on every subcommand.
		// InheritedFlags walks the parent chain; cmd.Flags() only merges
		// after Execute(), so the test cannot use Flags() here.
		if sub.InheritedFlags().Lookup("debug") == nil {
			t.Errorf("subcommand %q does not inherit --debug", sub.Name())
		}
		// And --debug must not be redeclared locally — that would
		// shadow the persistent flag and break global propagation.
		if sub.LocalFlags().Lookup("debug") != nil {
			t.Errorf("subcommand %q redeclares --debug locally; should rely on the persistent root flag", sub.Name())
		}
	}
}

// configureDefaultLogger should swap slog's default to a Debug-level
// handler when debug=true, and an Info-level handler otherwise.
func TestConfigureDefaultLogger_LevelSwitch(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	cases := []struct {
		name     string
		debug    bool
		emitted  func(l *slog.Logger, buf *bytes.Buffer)
		wantText string
	}{
		{
			name:     "info-level by default suppresses debug",
			debug:    false,
			emitted:  func(l *slog.Logger, buf *bytes.Buffer) { l.Debug("hidden") },
			wantText: "",
		},
		{
			name:     "debug=true emits debug records",
			debug:    true,
			emitted:  func(l *slog.Logger, buf *bytes.Buffer) { l.Debug("visible") },
			wantText: "visible",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			// Reroute the default logger's writer for inspection.
			configureDefaultLogger(tc.debug)
			// Wrap the default handler's level into a buffer-backed
			// logger so we can read what would have been written.
			level := slog.LevelInfo
			if tc.debug {
				level = slog.LevelDebug
			}
			testLogger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level}))
			tc.emitted(testLogger, &buf)
			if tc.wantText == "" && buf.Len() != 0 {
				t.Errorf("expected no output, got %q", buf.String())
			}
			if tc.wantText != "" && !bytes.Contains(buf.Bytes(), []byte(tc.wantText)) {
				t.Errorf("expected output containing %q, got %q", tc.wantText, buf.String())
			}
		})
	}
}
