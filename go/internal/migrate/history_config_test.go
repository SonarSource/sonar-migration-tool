// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"reflect"
	"strings"
	"testing"
)

// This file covers the config-file plumbing for `migrate_history` (#554) end
// to end through LoadMigrateConfigFile, i.e. JSON on disk -> parseConfigFile
// -> toMigrateConfig -> MigrateConfig.MigrateHistory. TestResolveMigrateHistory
// in history_test.go only exercises the tri-state resolver in isolation, which
// leaves the per-shape wiring (the assignment inside each of the four shape
// branches in toMigrateConfig) unverified — a field can be declared on the
// struct and still never be copied into MigrateConfig for a given shape.

// histCfgBoolForms is every value form the FlexibleBool schema accepts, plus
// JSON null. Reused by each shape's table so all four shapes are proven to go
// through the same tri-state parsing rather than, say, a plain `bool` that
// would silently ignore "yes" / 1.
var histCfgBoolForms = []struct {
	name string
	// value is the raw JSON token written after `"migrate_history":`.
	value string
	want  bool
}{
	{"bool true", `true`, true},
	{"bool false", `false`, false},
	{"numeric 1", `1`, true},
	{"numeric 0", `0`, false},
	{"string yes", `"yes"`, true},
	{"string no", `"no"`, false},
	{"string on", `"on"`, true},
	{"string off", `"off"`, false},
	{"string true", `"true"`, true},
	{"string OFF is case-insensitive", `"OFF"`, false},
	{"string Yes is case-insensitive", `"Yes"`, true},
	{"json null is treated as absent", `null`, false},
}

// histCfgLoad writes body to a temp config file and loads it, failing the test
// if the loader rejects it.
func histCfgLoad(t *testing.T, body string) MigrateConfig {
	t.Helper()
	cfg, err := LoadMigrateConfigFile(writeConfigFixture(t, body))
	if err != nil {
		t.Fatalf("LoadMigrateConfigFile(%s): %v", body, err)
	}
	return cfg
}

// Shape 1 (flat): a top-level migrate_history alongside flat credentials must
// land in MigrateConfig.MigrateHistory. This is the branch a plain
// `{"url":..,"token":..}` config file takes.
func TestLoadMigrateConfigFile_MigrateHistoryFlatShape(t *testing.T) {
	t.Run("absent defaults to false", func(t *testing.T) {
		cfg := histCfgLoad(t, `{"url": "u", "token": "t"}`)
		if cfg.MigrateHistory {
			t.Error("MigrateHistory: got true, want false when migrate_history is absent")
		}
	})

	for _, c := range histCfgBoolForms {
		t.Run(c.name, func(t *testing.T) {
			cfg := histCfgLoad(t, `{"url": "u", "token": "t", "migrate_history": `+c.value+`}`)
			if cfg.MigrateHistory != c.want {
				t.Errorf("MigrateHistory for %s: got %v, want %v", c.value, cfg.MigrateHistory, c.want)
			}
			// The flat branch also copies the ordinary fields; if the shape
			// were misdetected (e.g. migrate_history somehow tripping the
			// unified branch) these would come back empty.
			if cfg.URL != "u" || cfg.Token != "t" {
				t.Errorf("flat shape fields lost: URL=%q Token=%q", cfg.URL, cfg.Token)
			}
		})
	}
}

// Shape 2 (command-sectioned): migrate_history is read from the nested
// "migrate" section, and an outer value wins when both are set — the same
// outer-wins-else-inner precedence skip_issue_sync and fast_sync use.
func TestLoadMigrateConfigFile_MigrateHistoryMigrateSectionedShape(t *testing.T) {
	t.Run("absent defaults to false", func(t *testing.T) {
		cfg := histCfgLoad(t, `{"migrate": {"url": "u", "token": "t"}}`)
		if cfg.MigrateHistory {
			t.Error("MigrateHistory: got true, want false when migrate_history is absent")
		}
	})

	for _, c := range histCfgBoolForms {
		t.Run("inside the migrate section: "+c.name, func(t *testing.T) {
			cfg := histCfgLoad(t, `{"migrate": {"url": "u", "token": "t", "migrate_history": `+c.value+`}}`)
			if cfg.MigrateHistory != c.want {
				t.Errorf("MigrateHistory for %s: got %v, want %v", c.value, cfg.MigrateHistory, c.want)
			}
			if cfg.URL != "u" {
				t.Errorf("migrate-section fields lost: URL=%q", cfg.URL)
			}
		})

		t.Run("outer level: "+c.name, func(t *testing.T) {
			cfg := histCfgLoad(t, `{"migrate_history": `+c.value+`, "migrate": {"url": "u", "token": "t"}}`)
			if cfg.MigrateHistory != c.want {
				t.Errorf("MigrateHistory for outer %s: got %v, want %v", c.value, cfg.MigrateHistory, c.want)
			}
		})
	}

	// Precedence: an explicit outer false must beat an inner true. This is the
	// case that distinguishes the implemented `.Set` check from a naive
	// `.Value` check (which would leave the inner true standing).
	t.Run("outer false wins over inner true", func(t *testing.T) {
		cfg := histCfgLoad(t, `{"migrate_history": false, "migrate": {"url": "u", "token": "t", "migrate_history": true}}`)
		if cfg.MigrateHistory {
			t.Error("MigrateHistory: got true, want false (an explicit outer false must win over the inner true)")
		}
	})

	t.Run("outer true wins over inner false", func(t *testing.T) {
		cfg := histCfgLoad(t, `{"migrate_history": true, "migrate": {"url": "u", "token": "t", "migrate_history": false}}`)
		if !cfg.MigrateHistory {
			t.Error("MigrateHistory: got false, want true (an explicit outer true must win over the inner false)")
		}
	})

	// An absent outer value must not clobber the inner one back to the default.
	t.Run("inner true survives an absent outer value", func(t *testing.T) {
		cfg := histCfgLoad(t, `{"concurrency": 4, "migrate": {"url": "u", "token": "t", "migrate_history": "on"}}`)
		if !cfg.MigrateHistory {
			t.Error("MigrateHistory: got false, want true (inner value must propagate when the outer one is absent)")
		}
	})
}

// Shape 3 (side-sectioned): the "sonarcloud" block carries no migrate_history
// of its own, so the top-level field is the only way to opt in for this shape.
// sonarCloudBlock.toMigrateConfig builds a fresh MigrateConfig, so the
// top-level value has to be re-applied afterwards or it is dropped entirely.
func TestLoadMigrateConfigFile_MigrateHistorySideSectionedShape(t *testing.T) {
	t.Run("absent defaults to false", func(t *testing.T) {
		cfg := histCfgLoad(t, `{"sonarcloud": {"url": "u", "token": "t"}}`)
		if cfg.MigrateHistory {
			t.Error("MigrateHistory: got true, want false when migrate_history is absent")
		}
	})

	for _, c := range histCfgBoolForms {
		t.Run(c.name, func(t *testing.T) {
			body := `{"migrate_history": ` + c.value + `,
			  "sonarcloud": {"url": "u", "token": "t"},
			  "settings": {"concurrency": 3}}`
			cfg := histCfgLoad(t, body)
			if cfg.MigrateHistory != c.want {
				t.Errorf("MigrateHistory for %s: got %v, want %v", c.value, cfg.MigrateHistory, c.want)
			}
			// Guard against the top-level re-apply being done by *replacing*
			// the config the sonarcloud block produced.
			if cfg.Token != "t" || cfg.Concurrency != 3 {
				t.Errorf("side-sectioned fields lost: Token=%q Concurrency=%d", cfg.Token, cfg.Concurrency)
			}
		})
	}
}

// Shape 4 (unified, #266): target.migrate_history wins when explicitly set,
// else the top-level value, else false.
func TestLoadMigrateConfigFile_MigrateHistoryUnifiedShape(t *testing.T) {
	t.Run("absent defaults to false", func(t *testing.T) {
		cfg := histCfgLoad(t, `{"target": {"url": "u", "token": "t"}}`)
		if cfg.MigrateHistory {
			t.Error("MigrateHistory: got true, want false when migrate_history is absent")
		}
	})

	for _, c := range histCfgBoolForms {
		t.Run("target block: "+c.name, func(t *testing.T) {
			cfg := histCfgLoad(t, `{"target": {"url": "u", "token": "t", "migrate_history": `+c.value+`}}`)
			if cfg.MigrateHistory != c.want {
				t.Errorf("MigrateHistory for target %s: got %v, want %v", c.value, cfg.MigrateHistory, c.want)
			}
			if cfg.URL != "u" || cfg.Token != "t" {
				t.Errorf("target fields lost: URL=%q Token=%q", cfg.URL, cfg.Token)
			}
		})

		t.Run("top level with a target block: "+c.name, func(t *testing.T) {
			cfg := histCfgLoad(t, `{"migrate_history": `+c.value+`, "target": {"url": "u", "token": "t"}}`)
			if cfg.MigrateHistory != c.want {
				t.Errorf("MigrateHistory for top-level %s: got %v, want %v", c.value, cfg.MigrateHistory, c.want)
			}
		})
	}

	// Explicit target false beats top-level true: the resolver checks .Set,
	// not .Value, so "I turned it off for this target" is honored.
	t.Run("target false wins over top-level true", func(t *testing.T) {
		cfg := histCfgLoad(t, `{"migrate_history": true, "target": {"url": "u", "token": "t", "migrate_history": false}}`)
		if cfg.MigrateHistory {
			t.Error("MigrateHistory: got true, want false (an explicit target false must win over the top-level true)")
		}
	})

	t.Run("target true wins over top-level false", func(t *testing.T) {
		cfg := histCfgLoad(t, `{"migrate_history": false, "target": {"url": "u", "token": "t", "migrate_history": true}}`)
		if !cfg.MigrateHistory {
			t.Error("MigrateHistory: got false, want true (an explicit target true must win over the top-level false)")
		}
	})

	// A file with only a "source" block still takes the unified branch, where
	// s.Target is nil; the top-level value must still be honored instead of
	// being lost to the nil target.
	t.Run("source-only unified file still honors the top-level value", func(t *testing.T) {
		cfg := histCfgLoad(t, `{"migrate_history": "yes", "source": {"url": "ignored", "token": "ignored"}}`)
		if !cfg.MigrateHistory {
			t.Error("MigrateHistory: got false, want true (top-level value must survive a nil target block)")
		}
	})
}

// migrate_history must be wired to its own MigrateConfig field in every shape.
// The three non-unified branches were added by copy-pasting the neighbouring
// fast_sync block, so a mis-edited copy would set the wrong field and still
// compile; these assertions fail loudly if that ever happens.
func TestLoadMigrateConfigFile_MigrateHistoryDoesNotAliasOtherFlags(t *testing.T) {
	shapes := []struct {
		name    string
		history string // body with migrate_history: true only
		fast    string // the same body with fast_sync: true only
	}{
		{
			name:    "flat",
			history: `{"url": "u", "token": "t", "migrate_history": true}`,
			fast:    `{"url": "u", "token": "t", "fast_sync": true}`,
		},
		{
			name:    "command-sectioned",
			history: `{"migrate": {"url": "u", "token": "t", "migrate_history": true}}`,
			fast:    `{"migrate": {"url": "u", "token": "t", "fast_sync": true}}`,
		},
		{
			name:    "side-sectioned",
			history: `{"migrate_history": true, "sonarcloud": {"url": "u", "token": "t"}}`,
			fast:    `{"fast_sync": true, "sonarcloud": {"url": "u", "token": "t"}}`,
		},
		{
			name:    "unified",
			history: `{"target": {"url": "u", "token": "t", "migrate_history": true}}`,
			fast:    `{"target": {"url": "u", "token": "t", "fast_sync": true}}`,
		},
	}

	for _, s := range shapes {
		t.Run(s.name+": migrate_history sets only MigrateHistory", func(t *testing.T) {
			cfg := histCfgLoad(t, s.history)
			if !cfg.MigrateHistory {
				t.Error("MigrateHistory: got false, want true")
			}
			if cfg.FastSync {
				t.Error("FastSync: got true, want false — migrate_history must not set fast_sync")
			}
			if cfg.SkipIssueSync {
				t.Error("SkipIssueSync: got true, want false — migrate_history must not set skip_issue_sync")
			}
			if cfg.SkipProjectDataMigration {
				t.Error("SkipProjectDataMigration: got true, want false")
			}
		})

		t.Run(s.name+": fast_sync does not set MigrateHistory", func(t *testing.T) {
			cfg := histCfgLoad(t, s.fast)
			if !cfg.FastSync {
				t.Error("FastSync: got false, want true")
			}
			if cfg.MigrateHistory {
				t.Error("MigrateHistory: got true, want false — fast_sync must not opt into history migration")
			}
		})
	}
}

// A typo in migrate_history must fail at parse time rather than silently
// defaulting to false: a user who wrote "ture" would otherwise get a
// successful run with no history migrated and no indication why.
func TestLoadMigrateConfigFile_MigrateHistoryRejectsUnrecognisedValue(t *testing.T) {
	bodies := map[string]string{
		"flat":              `{"url": "u", "token": "t", "migrate_history": "ture"}`,
		"target block":      `{"target": {"url": "u", "token": "t", "migrate_history": "maybe"}}`,
		"migrate section":   `{"migrate": {"url": "u", "token": "t", "migrate_history": "sometimes"}}`,
		"unsupported shape": `{"migrate_history": [true], "target": {"url": "u", "token": "t"}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			cfg, err := LoadMigrateConfigFile(writeConfigFixture(t, body))
			if err == nil {
				t.Fatalf("expected an error for %s, got cfg=%+v", body, cfg)
			}
			if !strings.Contains(err.Error(), "parsing config file") {
				t.Errorf("error should say the config file failed to parse, got %q", err)
			}
			if !reflect.DeepEqual(cfg, MigrateConfig{}) {
				t.Errorf("expected the zero MigrateConfig on a parse failure, got %+v", cfg)
			}
		})
	}
}
