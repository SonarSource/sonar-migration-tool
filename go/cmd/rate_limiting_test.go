// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// #573 — --api_max_rate_per_min must abort out-of-range values rather than
// clamp them; 0 (unset) is valid and resolved to 1500 downstream by each
// config's applyDefaults.
func TestValidateAPIMaxRatePerMin(t *testing.T) {
	cases := []struct {
		name    string
		value   int
		wantErr bool
	}{
		{"unset (zero) is valid", 0, false},
		{"just below minimum", 99, true},
		{"minimum is valid", 100, false},
		{"typical value is valid", 750, false},
		{"maximum is valid", 1500, false},
		{"just above maximum", 1501, true},
		{"far above maximum", 100000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAPIMaxRatePerMin(tc.value)
			if tc.wantErr && err == nil {
				t.Errorf("validateAPIMaxRatePerMin(%d): expected error, got nil", tc.value)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateAPIMaxRatePerMin(%d): unexpected error: %v", tc.value, err)
			}
		})
	}
}

// #573 — a concurrency value is deprecated for SonarQube Cloud targets:
// having one in effect must log a warning, regardless of whether it came
// from --concurrency on the CLI or from a config file's "concurrency"
// field — both produce the same resolved int by the time this is called.
// Concurrency now only seeds the ConcurrencyLimiter's starting point (it
// is always dynamically re-evaluated every 30s regardless), but
// --api_max_rate_per_min is the supported way to influence the target
// rate, so the warning still fires. A live run against a pre-#573 config
// file (concurrency set, no --concurrency flag) previously produced
// neither this warning nor any recalculation log, with nothing to
// explain why — this test guards against that regression.
func TestWarnIfConcurrencyDeprecated(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	cases := []struct {
		name        string
		concurrency int
		wantLog     bool
	}{
		{"unset (zero) logs nothing", 0, false},
		{"explicitly set (CLI flag or config file — both resolve to the same int) logs the warning", 10, true},
		{"resolved to the pre-#573 default (25) still logs — the source doesn't matter, only that it's set", 25, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

			warnIfConcurrencyDeprecated(tc.concurrency)

			gotLog := buf.Len() > 0
			if gotLog != tc.wantLog {
				t.Errorf("warnIfConcurrencyDeprecated(%d): got log=%v (output %q), want log=%v", tc.concurrency, gotLog, buf.String(), tc.wantLog)
			}
			if gotLog && !strings.Contains(buf.String(), "deprecated") {
				t.Errorf("expected warning to mention deprecation, got %q", buf.String())
			}
		})
	}
}
