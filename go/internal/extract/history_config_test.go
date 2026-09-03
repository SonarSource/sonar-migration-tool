// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"os"
	"path/filepath"
	"testing"
)

// Config / defaults plumbing for the #554 project-history migration PoC:
// the three settings (migrate_history, history_max_points,
// history_min_interval_days) must survive every documented config-file
// shape, and applyDefaults must resolve the bounds *only* when the feature
// was actually requested.

// histCfgWrite writes body to a throwaway config file and returns its path.
// Named with a "histCfg" prefix so it cannot collide with helpers defined in
// the other history test files in this package.
func histCfgWrite(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

// The PoC bounds are part of the documented CLI contract (#554): a bare
// --migrate_history run extracts at most 10 snapshots per project+branch,
// spaced at least 30 days apart. Pin them so a silent constant change is a
// test failure rather than a silent change in what gets extracted.
func TestHistCfgDefaultConstants(t *testing.T) {
	if DefaultHistoryMaxPoints != 10 {
		t.Errorf("DefaultHistoryMaxPoints = %d, want 10", DefaultHistoryMaxPoints)
	}
	if DefaultHistoryMinIntervalDays != 30 {
		t.Errorf("DefaultHistoryMinIntervalDays = %d, want 30", DefaultHistoryMinIntervalDays)
	}
}

// applyDefaults resolves each history bound independently. The two bounds
// differ deliberately (#554): HistoryMaxPoints treats <=0 as unset, because 0
// points is meaningless; HistoryMinIntervalDays treats only NEGATIVE values
// (HistoryUnset) as unset, because an explicit 0 is a real request — "no
// spacing rule, take every analysis" — and must survive defaulting.
func TestHistCfgApplyDefaultsResolvesBoundsWhenEnabled(t *testing.T) {
	cases := []struct {
		name                  string
		inMax, inInterval     int
		wantMax, wantInterval int
	}{
		{"both unset", 0, HistoryUnset, DefaultHistoryMaxPoints, DefaultHistoryMinIntervalDays},
		{"both negative", -1, -5, DefaultHistoryMaxPoints, DefaultHistoryMinIntervalDays},
		{"both explicit", 3, 7, 3, 7},
		{"only max explicit", 3, HistoryUnset, 3, DefaultHistoryMinIntervalDays},
		{"only interval explicit", 0, 7, DefaultHistoryMaxPoints, 7},
		// An explicit 0 disables spacing; it must NOT be read as unset.
		{"interval explicitly zero means no spacing", 0, 0, DefaultHistoryMaxPoints, 0},
		{"explicit values above the defaults", 250, 365, 250, 365},
		{"explicit value of one", 1, 1, 1, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ExtractConfig{
				MigrateHistory:         true,
				HistoryMaxPoints:       tc.inMax,
				HistoryMinIntervalDays: tc.inInterval,
			}
			cfg.applyDefaults()

			if cfg.HistoryMaxPoints != tc.wantMax {
				t.Errorf("HistoryMaxPoints = %d, want %d (in=%d)", cfg.HistoryMaxPoints, tc.wantMax, tc.inMax)
			}
			if cfg.HistoryMinIntervalDays != tc.wantInterval {
				t.Errorf("HistoryMinIntervalDays = %d, want %d (in=%d)",
					cfg.HistoryMinIntervalDays, tc.wantInterval, tc.inInterval)
			}
			if !cfg.MigrateHistory {
				t.Error("applyDefaults cleared MigrateHistory")
			}
		})
	}
}

// The #554 default-off guarantee at the config layer: with MigrateHistory
// unset, applyDefaults must not touch the history bounds at all — not even
// to normalise a negative value. Anything else would hand the extract phase
// a non-zero budget for a feature nobody asked for.
func TestHistCfgApplyDefaultsLeavesBoundsAloneWhenDisabled(t *testing.T) {
	cases := []struct {
		name              string
		inMax, inInterval int
	}{
		{"zero", 0, 0},
		{"negative", -1, -5},
		{"positive", 3, 7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ExtractConfig{
				HistoryMaxPoints:       tc.inMax,
				HistoryMinIntervalDays: tc.inInterval,
			}
			cfg.applyDefaults()

			if cfg.MigrateHistory {
				t.Error("applyDefaults turned MigrateHistory on")
			}
			if cfg.HistoryMaxPoints != tc.inMax {
				t.Errorf("HistoryMaxPoints = %d, want it left at %d", cfg.HistoryMaxPoints, tc.inMax)
			}
			if cfg.HistoryMinIntervalDays != tc.inInterval {
				t.Errorf("HistoryMinIntervalDays = %d, want it left at %d", cfg.HistoryMinIntervalDays, tc.inInterval)
			}
			// Guard against a vacuous pass: applyDefaults really did run.
			if cfg.Concurrency != 25 || cfg.Timeout != 60 || cfg.ExtractType != "all" {
				t.Errorf("applyDefaults did not run: concurrency=%d timeout=%d extractType=%q",
					cfg.Concurrency, cfg.Timeout, cfg.ExtractType)
			}
		})
	}
}

// Every documented config-file shape must carry the three history keys into
// ExtractConfig. Each shape uses a distinct URL (so the assertion proves the
// intended shape branch ran) and distinct, mutually different max/interval
// values (so a swapped or copy-pasted assignment fails).
func TestHistCfgLoadExtractConfigFileHistoryKeysPerShape(t *testing.T) {
	cases := []struct {
		name                  string
		body                  string
		wantURL               string
		wantMax, wantInterval int
	}{
		{
			name: "shape 1 - flat",
			body: `{
  "url": "http://flat.invalid:9000",
  "token": "fake-token",
  "migrate_history": true,
  "history_max_points": 4,
  "history_min_interval_days": 60
}`,
			wantURL: "http://flat.invalid:9000", wantMax: 4, wantInterval: 60,
		},
		{
			// Shape 2 delegates wholesale to the nested "extract" object,
			// so the history keys live inside it like every other flat field.
			name: "shape 2 - command-sectioned",
			body: `{
  "extract": {
    "url": "http://sectioned.invalid:9000",
    "token": "fake-token",
    "migrate_history": true,
    "history_max_points": 3,
    "history_min_interval_days": 45
  },
  "migrate": { "url": "http://target.invalid/", "token": "fake-target-token" }
}`,
			wantURL: "http://sectioned.invalid:9000", wantMax: 3, wantInterval: 45,
		},
		{
			name: "shape 3 - side-sectioned",
			body: `{
  "sonarqube": { "url": "http://side.invalid:9000", "token": "fake-token" },
  "settings": { "export_directory": "./files", "concurrency": 10, "timeout": 60 },
  "migrate_history": true,
  "history_max_points": 5,
  "history_min_interval_days": 90
}`,
			wantURL: "http://side.invalid:9000", wantMax: 5, wantInterval: 90,
		},
		{
			name: "shape 4 - unified",
			body: `{
  "export_directory": "./out",
  "source": { "url": "http://unified.invalid:9000", "token": "fake-token" },
  "target": { "url": "http://target.invalid/", "token": "fake-target-token" },
  "migrate_history": true,
  "history_max_points": 6,
  "history_min_interval_days": 15
}`,
			wantURL: "http://unified.invalid:9000", wantMax: 6, wantInterval: 15,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadExtractConfigFile(histCfgWrite(t, tc.body))
			if err != nil {
				t.Fatalf("LoadExtractConfigFile: %v", err)
			}
			// Pins which shape branch actually ran, so a fixture that
			// silently fell through to the flat branch cannot pass.
			if cfg.URL != tc.wantURL {
				t.Fatalf("URL = %q, want %q — wrong config shape branch taken", cfg.URL, tc.wantURL)
			}
			if !cfg.MigrateHistory {
				t.Error("MigrateHistory = false, want true")
			}
			if cfg.HistoryMaxPoints != tc.wantMax {
				t.Errorf("HistoryMaxPoints = %d, want %d", cfg.HistoryMaxPoints, tc.wantMax)
			}
			if cfg.HistoryMinIntervalDays != tc.wantInterval {
				t.Errorf("HistoryMinIntervalDays = %d, want %d", cfg.HistoryMinIntervalDays, tc.wantInterval)
			}
		})
	}
}

// migrate_history: false must round-trip as false in every shape, so an
// explicit opt-out in a config file can never be read as an opt-in.
func TestHistCfgLoadExtractConfigFileExplicitFalsePerShape(t *testing.T) {
	cases := []struct{ name, body string }{
		{"shape 1 - flat", `{"url":"http://flat.invalid:9000","token":"fake-token","migrate_history":false}`},
		{"shape 2 - command-sectioned", `{"extract":{"url":"http://sectioned.invalid:9000","token":"fake-token","migrate_history":false}}`},
		{"shape 3 - side-sectioned", `{"sonarqube":{"url":"http://side.invalid:9000","token":"fake-token"},"migrate_history":false}`},
		{"shape 4 - unified", `{"source":{"url":"http://unified.invalid:9000","token":"fake-token"},"migrate_history":false}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadExtractConfigFile(histCfgWrite(t, tc.body))
			if err != nil {
				t.Fatalf("LoadExtractConfigFile: %v", err)
			}
			if cfg.MigrateHistory {
				t.Error("MigrateHistory = true, want false")
			}
		})
	}
}

// A config file with no history keys at all must leave the feature entirely
// off and unbounded, in every shape — the "behaves exactly as before this
// feature existed" contract for every config file already in the wild.
func TestHistCfgLoadExtractConfigFileAbsentKeysStayZero(t *testing.T) {
	cases := []struct{ name, body string }{
		{"shape 1 - flat", `{"url":"http://flat.invalid:9000","token":"fake-token"}`},
		{"shape 2 - command-sectioned", `{"extract":{"url":"http://sectioned.invalid:9000","token":"fake-token"}}`},
		{"shape 3 - side-sectioned", `{"sonarqube":{"url":"http://side.invalid:9000","token":"fake-token"}}`},
		{"shape 4 - unified", `{"source":{"url":"http://unified.invalid:9000","token":"fake-token"}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadExtractConfigFile(histCfgWrite(t, tc.body))
			if err != nil {
				t.Fatalf("LoadExtractConfigFile: %v", err)
			}
			if cfg.MigrateHistory {
				t.Error("MigrateHistory = true, want false")
			}
			if cfg.HistoryMaxPoints != 0 {
				t.Errorf("HistoryMaxPoints = %d, want 0", cfg.HistoryMaxPoints)
			}
			// #554: absent loads as the "caller said nothing" sentinel, not
			// 0 — an explicit 0 is a real request ("no spacing rule").
			if cfg.HistoryMinIntervalDays != HistoryUnset {
				t.Errorf("HistoryMinIntervalDays = %d, want HistoryUnset (%d)", cfg.HistoryMinIntervalDays, HistoryUnset)
			}
		})
	}
}

// The loader copies the two bounds unconditionally; only applyDefaults reads
// migrate_history. Bounds supplied without the flag therefore survive the
// load, and are then left untouched because the feature is off.
func TestHistCfgLoadExtractConfigFileBoundsIndependentOfFlag(t *testing.T) {
	body := `{
  "url": "http://flat.invalid:9000",
  "token": "fake-token",
  "history_max_points": 42,
  "history_min_interval_days": 11
}`
	cfg, err := LoadExtractConfigFile(histCfgWrite(t, body))
	if err != nil {
		t.Fatalf("LoadExtractConfigFile: %v", err)
	}
	if cfg.MigrateHistory {
		t.Error("MigrateHistory = true, want false — bounds alone must not enable the feature")
	}
	if cfg.HistoryMaxPoints != 42 || cfg.HistoryMinIntervalDays != 11 {
		t.Errorf("bounds after load = (%d, %d), want (42, 11)", cfg.HistoryMaxPoints, cfg.HistoryMinIntervalDays)
	}

	cfg.applyDefaults()
	if cfg.HistoryMaxPoints != 42 || cfg.HistoryMinIntervalDays != 11 {
		t.Errorf("bounds after applyDefaults = (%d, %d), want them untouched at (42, 11)",
			cfg.HistoryMaxPoints, cfg.HistoryMinIntervalDays)
	}
	if cfg.MigrateHistory {
		t.Error("applyDefaults turned MigrateHistory on")
	}
}

// Shape 2 returns s.Extract.toExtractConfig() wholesale, so — exactly as for
// skip_issue_sync and every other flat field — the history keys are only
// read from inside the "extract" object. Top-level copies are dropped.
func TestHistCfgLoadExtractConfigFileCommandSectionedIgnoresTopLevelKeys(t *testing.T) {
	body := `{
  "migrate_history": true,
  "history_max_points": 9,
  "history_min_interval_days": 99,
  "extract": { "url": "http://sectioned.invalid:9000", "token": "fake-token" }
}`
	cfg, err := LoadExtractConfigFile(histCfgWrite(t, body))
	if err != nil {
		t.Fatalf("LoadExtractConfigFile: %v", err)
	}
	if cfg.URL != "http://sectioned.invalid:9000" {
		t.Fatalf("URL = %q, want the nested extract URL", cfg.URL)
	}
	if cfg.MigrateHistory {
		t.Error("MigrateHistory = true; shape 2 reads history keys only from the nested \"extract\" object")
	}
	if cfg.HistoryMaxPoints != 0 || cfg.HistoryMinIntervalDays != HistoryUnset {
		t.Errorf("bounds = (%d, %d), want (0, %d)", cfg.HistoryMaxPoints, cfg.HistoryMinIntervalDays, HistoryUnset)
	}
}

// End to end over the two halves of the plumbing: a config file that opts in
// without naming any bound is loaded unbounded, and only applyDefaults fills
// in the PoC budget.
func TestHistCfgLoadThenApplyDefaults(t *testing.T) {
	body := `{"url":"http://flat.invalid:9000","token":"fake-token","migrate_history":true}`
	cfg, err := LoadExtractConfigFile(histCfgWrite(t, body))
	if err != nil {
		t.Fatalf("LoadExtractConfigFile: %v", err)
	}
	if !cfg.MigrateHistory {
		t.Fatal("MigrateHistory = false, want true")
	}
	if cfg.HistoryMaxPoints != 0 || cfg.HistoryMinIntervalDays != HistoryUnset {
		t.Fatalf("loader applied defaults itself: (%d, %d), want (0, %d)",
			cfg.HistoryMaxPoints, cfg.HistoryMinIntervalDays, HistoryUnset)
	}

	cfg.applyDefaults()
	if cfg.HistoryMaxPoints != DefaultHistoryMaxPoints {
		t.Errorf("HistoryMaxPoints = %d, want %d", cfg.HistoryMaxPoints, DefaultHistoryMaxPoints)
	}
	if cfg.HistoryMinIntervalDays != DefaultHistoryMinIntervalDays {
		t.Errorf("HistoryMinIntervalDays = %d, want %d", cfg.HistoryMinIntervalDays, DefaultHistoryMinIntervalDays)
	}
}
