// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/spf13/cobra"
)

// newExtractHistoryTestCmd mirrors extractCmd's flag set so
// buildExtractConfig can be exercised in isolation without invoking RunE,
// in the same style as newMigrateTestCmd / newTransferTestCmd.
func newExtractHistoryTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "extract"}
	f := cmd.Flags()
	f.String("config", "", "")
	f.String(flagSourceURL, "", "")
	f.String(flagSourceToken, "", "")
	f.String("export_directory", DefaultExportDirectory, "")
	f.Bool(flagSkipProjectDataMigration, false, "")
	f.Bool(flagSkipIssueSync, false, "")
	f.Bool(flagMigrateHistory, false, "")
	f.Int(flagHistoryMaxPoints, 0, "")
	f.Int(flagHistoryMinIntervalDays, 0, "")
	return cmd
}

// Issue #554: --migrate_history is deliberately one-way on the extract
// side, matching --skip_issue_sync. Passing the flag opts in; passing
// --migrate_history=false must NOT undo a config file that already opted
// in. A naive `cfg.MigrateHistory = v` would silently turn the feature
// back off for anyone whose wrapper script always spells the flag out.
func TestBuildExtractConfig_MigrateHistoryFlagIsOneWay(t *testing.T) {
	cases := []struct {
		name       string
		configJSON string
		args       []string
		want       bool
	}{
		{
			name:       "unset everywhere stays off",
			configJSON: `{"url": "https://sq.invalid", "token": "fake-token"}`,
			want:       false,
		},
		{
			name:       "cli flag opts in",
			configJSON: `{"url": "https://sq.invalid", "token": "fake-token"}`,
			args:       []string{"--" + flagMigrateHistory},
			want:       true,
		},
		{
			name:       "config file alone opts in",
			configJSON: `{"url": "https://sq.invalid", "migrate_history": true}`,
			want:       true,
		},
		{
			name:       "explicit cli false does not undo the config file",
			configJSON: `{"url": "https://sq.invalid", "migrate_history": true}`,
			args:       []string{"--" + flagMigrateHistory + "=false"},
			want:       true,
		},
		{
			name:       "explicit cli false with config false stays off",
			configJSON: `{"url": "https://sq.invalid", "migrate_history": false}`,
			args:       []string{"--" + flagMigrateHistory + "=false"},
			want:       false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeTransferConfig(t, c.configJSON)
			cmd := newExtractHistoryTestCmd()
			args := append([]string{"--config", path}, c.args...)
			if err := cmd.ParseFlags(args); err != nil {
				t.Fatal(err)
			}

			cfg, err := buildExtractConfig(cmd, nil)
			if err != nil {
				t.Fatalf("buildExtractConfig: %v", err)
			}
			if cfg.MigrateHistory != c.want {
				t.Errorf("MigrateHistory: got %v, want %v", cfg.MigrateHistory, c.want)
			}
			// Guard against a config file that silently failed to load:
			// every fixture sets source_url, so an empty URL here would
			// mean the MigrateHistory assertion passed vacuously.
			if cfg.URL != "https://sq.invalid" {
				t.Errorf("URL: got %q, want the config file's value", cfg.URL)
			}
		})
	}
}

// Issue #554: the two bounds are plain last-one-wins overrides (unlike
// the one-way boolean), and buildExtractConfig must not substitute the
// 10/30 defaults itself — that happens later, in extract.applyDefaults,
// which is what lets a 0 here still mean "not set".
func TestBuildExtractConfig_HistoryBoundsCLIOverridesConfig(t *testing.T) {
	const withBounds = `{
		"url": "https://sq.invalid",
		"token": "fake-token",
		"history_max_points": 4,
		"history_min_interval_days": 60
	}`

	cases := []struct {
		name         string
		configJSON   string
		args         []string
		wantMax      int
		wantInterval int
	}{
		{
			name:         "config file values survive when no flag is passed",
			configJSON:   withBounds,
			wantMax:      4,
			wantInterval: 60,
		},
		{
			name:         "cli overrides both",
			configJSON:   withBounds,
			args:         []string{"--" + flagHistoryMaxPoints, "7", "--" + flagHistoryMinIntervalDays, "90"},
			wantMax:      7,
			wantInterval: 90,
		},
		{
			name:         "cli overrides only the max",
			configJSON:   withBounds,
			args:         []string{"--" + flagHistoryMaxPoints, "7"},
			wantMax:      7,
			wantInterval: 60,
		},
		{
			name:         "cli overrides only the interval",
			configJSON:   withBounds,
			args:         []string{"--" + flagHistoryMinIntervalDays, "90"},
			wantMax:      4,
			wantInterval: 90,
		},
		{
			// #554: an absent history_min_interval_days must arrive here as
			// the HistoryUnset sentinel, NOT 0 — 0 is a real request ("no
			// spacing rule") and applyDefaults must be able to tell them apart.
			name:         "no defaulting happens at this layer",
			configJSON:   `{"url": "https://sq.invalid", "migrate_history": true}`,
			wantMax:      0,
			wantInterval: extract.HistoryUnset,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeTransferConfig(t, c.configJSON)
			cmd := newExtractHistoryTestCmd()
			args := append([]string{"--config", path}, c.args...)
			if err := cmd.ParseFlags(args); err != nil {
				t.Fatal(err)
			}

			cfg, err := buildExtractConfig(cmd, nil)
			if err != nil {
				t.Fatalf("buildExtractConfig: %v", err)
			}
			if cfg.HistoryMaxPoints != c.wantMax {
				t.Errorf("HistoryMaxPoints: got %d, want %d", cfg.HistoryMaxPoints, c.wantMax)
			}
			if cfg.HistoryMinIntervalDays != c.wantInterval {
				t.Errorf("HistoryMinIntervalDays: got %d, want %d", cfg.HistoryMinIntervalDays, c.wantInterval)
			}
		})
	}
}

// Issue #554: transfer resolves the same three settings, reading them
// from the extract half of the shared config file and then applying the
// CLI flags with the same one-way boolean / last-one-wins int semantics
// as the standalone extract command.
func TestResolveTransferConfig_MigrateHistory(t *testing.T) {
	cases := []struct {
		name         string
		configBody   string
		args         []string
		wantOn       bool
		wantMax      int
		wantInterval int
	}{
		{
			name:         "off by default",
			configBody:   `"export_dir": "/tmp/x"`,
			wantOn:       false,
			wantInterval: extract.HistoryUnset,
		},
		{
			name:       "config file opts in and carries the bounds",
			configBody: `"migrate_history": true, "history_max_points": 4, "history_min_interval_days": 60`,
			wantOn:     true, wantMax: 4, wantInterval: 60,
		},
		{
			name:         "cli flag opts in over a config file that did not",
			configBody:   `"migrate_history": false`,
			args:         []string{"--" + flagMigrateHistory},
			wantOn:       true,
			wantInterval: extract.HistoryUnset,
		},
		{
			name:         "explicit cli false does not undo the config file",
			configBody:   `"migrate_history": true`,
			args:         []string{"--" + flagMigrateHistory + "=false"},
			wantOn:       true,
			wantInterval: extract.HistoryUnset,
		},
		{
			name:       "cli bounds override the config file",
			configBody: `"migrate_history": true, "history_max_points": 4, "history_min_interval_days": 60`,
			args: []string{
				"--" + flagHistoryMaxPoints, "7",
				"--" + flagHistoryMinIntervalDays, "90",
			},
			wantOn: true, wantMax: 7, wantInterval: 90,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeTransferConfig(t, "{"+c.configBody+`,
				"source": {"url": "https://sq.invalid", "token": "fake-source-token"},
				"target": {"url": "https://sc.invalid", "token": "fake-target-token"}
			}`)
			cmd := newTransferTestCmd()
			args := append([]string{"-c", path}, c.args...)
			if err := cmd.ParseFlags(args); err != nil {
				t.Fatal(err)
			}

			cfg, err := resolveTransferConfig(cmd)
			if err != nil {
				t.Fatalf("resolveTransferConfig: %v", err)
			}
			if cfg.migrateHistory != c.wantOn {
				t.Errorf("migrateHistory: got %v, want %v", cfg.migrateHistory, c.wantOn)
			}
			if cfg.historyMaxPoints != c.wantMax {
				t.Errorf("historyMaxPoints: got %d, want %d", cfg.historyMaxPoints, c.wantMax)
			}
			if cfg.historyMinIntervalDays != c.wantInterval {
				t.Errorf("historyMinIntervalDays: got %d, want %d", cfg.historyMinIntervalDays, c.wantInterval)
			}
			// Shape guard: every fixture carries a source block, so a
			// misdetected config shape would show up here rather than
			// letting the history assertions pass vacuously.
			if cfg.sourceURL != "https://sq.invalid" {
				t.Errorf("sourceURL: got %q, want the config file's value", cfg.sourceURL)
			}
		})
	}
}
