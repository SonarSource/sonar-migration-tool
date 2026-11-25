// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/redact"
	"github.com/spf13/cobra"
)

func TestParseRedactLogsValue(t *testing.T) {
	for raw, want := range map[string]bool{"true": true, "TRUE": true, "false": false, "False": false, "off": false, "1": true} {
		got, err := parseRedactLogsValue(raw)
		if err != nil || got != want {
			t.Errorf("parseRedactLogsValue(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "maybe", "nope"} {
		if _, err := parseRedactLogsValue(raw); err == nil || !strings.Contains(err.Error(), "invalid --redact_logs value") {
			t.Errorf("parseRedactLogsValue(%q) error = %v; want invalid value error", raw, err)
		}
	}
}

func writeRedactConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRedactLogsFromConfigFile(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantVal   bool
		wantSet   bool
		wantError bool
	}{
		{"json bool false", `{"redact_logs": false}`, false, true, false},
		{"json bool true", `{"redact_logs": true}`, true, true, false},
		{"string false", `{"redact_logs": "false"}`, false, true, false},
		{"string off", `{"redact_logs": "Off"}`, false, true, false},
		{"absent", `{"source": {}}`, false, false, false},
		{"invalid string", `{"redact_logs": "maybe"}`, false, false, true},
		{"not json", `not json`, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, set, err := redactLogsFromConfigFile(writeRedactConfig(t, tc.body))
			if (err != nil) != tc.wantError || v != tc.wantVal || set != tc.wantSet {
				t.Errorf("got (%v, %v, %v); want (%v, %v, err=%v)", v, set, err, tc.wantVal, tc.wantSet, tc.wantError)
			}
		})
	}
	if _, set, err := redactLogsFromConfigFile(filepath.Join(t.TempDir(), "missing.json")); set || err != nil {
		t.Errorf("missing file: set=%v err=%v; want silent", set, err)
	}
}

func newRedactTestCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "x", Run: func(*cobra.Command, []string) {}}
	c.Flags().String("config", "", "")
	c.Flags().String(flagRedactLogs, "true", "")
	if err := c.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestApplyRedactLogs_Precedence(t *testing.T) {
	prev := redact.Enabled()
	t.Cleanup(func() { redact.SetEnabled(prev) })
	cfgFalse := writeRedactConfig(t, `{"redact_logs": "false"}`)
	cfgTrue := writeRedactConfig(t, `{"redact_logs": true}`)

	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"default", nil, true},
		{"flag space false", []string{"--redact_logs", "false"}, false},
		{"flag equals false", []string{"--redact_logs=false"}, false},
		{"config false", []string{"--config", cfgFalse}, false},
		{"flag true beats config false", []string{"--config", cfgFalse, "--redact_logs", "true"}, true},
		{"flag false beats config true", []string{"--config", cfgTrue, "--redact_logs=false"}, false},
		{"config true", []string{"--config", cfgTrue}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			redact.SetEnabled(!tc.want) // prove the helper sets it, not leaves it
			if err := applyRedactLogs(newRedactTestCmd(t, tc.args...)); err != nil {
				t.Fatal(err)
			}
			if redact.Enabled() != tc.want {
				t.Errorf("Enabled() = %v; want %v", redact.Enabled(), tc.want)
			}
		})
	}
}

func TestApplyRedactLogs_InvalidValue(t *testing.T) {
	if err := applyRedactLogs(newRedactTestCmd(t, "--redact_logs", "bogus")); err == nil {
		t.Fatal("want error for invalid flag value")
	}
	bad := writeRedactConfig(t, `{"redact_logs": "bogus"}`)
	if err := applyRedactLogs(newRedactTestCmd(t, "--config", bad)); err == nil {
		t.Fatal("want error for invalid config value")
	}
}

func TestRootCmd_RedactLogsIsPersistentFlag(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup(flagRedactLogs)
	if f == nil || f.DefValue != "true" {
		t.Fatalf("--redact_logs must be a persistent string flag defaulting to true, got %+v", f)
	}
}
