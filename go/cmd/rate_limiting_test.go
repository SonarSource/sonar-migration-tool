// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"bytes"
	"log/slog"
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

// #573 — --concurrency is deprecated for SonarQube Cloud targets: passing
// it must log a warning, but only when the flag was actually set (not
// merely registered with a nonzero default, e.g. reset's `concurrency 25`).
func TestWarnIfConcurrencyDeprecated(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	cases := []struct {
		name    string
		args    []string
		wantLog bool
	}{
		{"concurrency not passed", nil, false},
		{"concurrency explicitly passed", []string{"--concurrency", "10"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

			cmd := newTransferTestCmd()
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatal(err)
			}

			warnIfConcurrencyDeprecated(cmd)

			gotLog := buf.Len() > 0
			if gotLog != tc.wantLog {
				t.Errorf("warnIfConcurrencyDeprecated: got log=%v (output %q), want log=%v", gotLog, buf.String(), tc.wantLog)
			}
			if gotLog && !bytes.Contains(buf.Bytes(), []byte("deprecated")) {
				t.Errorf("expected warning to mention deprecation, got %q", buf.String())
			}
		})
	}
}
