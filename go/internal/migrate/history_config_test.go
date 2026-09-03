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

// --- assertion helpers ----------------------------------------------------
//
// Every shape's table asserts the same handful of facts once per value form,
// so the assertions live in named helpers rather than inline in the subtest
// closures. Each helper is scored on its own, which keeps the shape tests flat
// and — more usefully — keeps each failure message written in one place.

// histCfgWantHistory fails when the resolved MigrateHistory flag is not want.
// why states the rule being enforced so a failure explains itself without the
// reader having to go and find the fixture that produced it.
func histCfgWantHistory(t *testing.T, cfg MigrateConfig, want bool, why string) {
	t.Helper()
	if cfg.MigrateHistory != want {
		t.Errorf("MigrateHistory: got %v, want %v (%s)", cfg.MigrateHistory, want, why)
	}
}

// histCfgWantHistoryFor loads body and asserts the MigrateHistory it resolves
// to. Used wherever the flag is the only thing a case cares about.
func histCfgWantHistoryFor(t *testing.T, body string, want bool, why string) {
	t.Helper()
	histCfgWantHistory(t, histCfgLoad(t, body), want, why)
}

// histCfgWantField guards one ordinary (non-history) config field. These are
// the "did the shape lose everything else?" assertions: if migrate_history
// tripped the wrong shape branch, or the top-level re-apply replaced the
// config a block had already produced, the surrounding fields come back empty.
func histCfgWantField(t *testing.T, scope, field string, got, want any, why string) {
	t.Helper()
	if got == want {
		return
	}
	detail := ""
	if why != "" {
		detail = " — " + why
	}
	t.Errorf("%s: %s = %v, want %v%s", scope, field, got, want, detail)
}

// histCfgWantHistoryOnly asserts a `migrate_history: true` file opted into
// history migration and into nothing else — the copy-paste guard described on
// TestLoadMigrateConfigFile_MigrateHistoryDoesNotAliasOtherFlags.
func histCfgWantHistoryOnly(t *testing.T, shape string, cfg MigrateConfig) {
	t.Helper()
	scope := shape + ": migrate_history"
	for _, c := range []struct {
		field string
		got   bool
		want  bool
		why   string
	}{
		{"MigrateHistory", cfg.MigrateHistory, true, ""},
		{"FastSync", cfg.FastSync, false, "migrate_history must not set fast_sync"},
		{"SkipIssueSync", cfg.SkipIssueSync, false, "migrate_history must not set skip_issue_sync"},
		{"SkipProjectDataMigration", cfg.SkipProjectDataMigration, false, "migrate_history must not set skip_project_data_migration"},
	} {
		histCfgWantField(t, scope, c.field, c.got, c.want, c.why)
	}
}

// histCfgWantFastSyncOnly is the mirror image: a `fast_sync: true` file must
// set FastSync and must not opt into history migration.
func histCfgWantFastSyncOnly(t *testing.T, shape string, cfg MigrateConfig) {
	t.Helper()
	scope := shape + ": fast_sync"
	histCfgWantField(t, scope, "FastSync", cfg.FastSync, true, "")
	histCfgWantField(t, scope, "MigrateHistory", cfg.MigrateHistory, false,
		"fast_sync must not opt into history migration")
}

// Shape 1 (flat): a top-level migrate_history alongside flat credentials must
// land in MigrateConfig.MigrateHistory. This is the branch a plain
// `{"url":..,"token":..}` config file takes.
func TestLoadMigrateConfigFile_MigrateHistoryFlatShape(t *testing.T) {
	t.Run("absent defaults to false", func(t *testing.T) {
		histCfgWantHistoryFor(t, `{"url": "u", "token": "t"}`, false,
			"migrate_history is absent")
	})

	for _, c := range histCfgBoolForms {
		t.Run(c.name, func(t *testing.T) {
			cfg := histCfgLoad(t, `{"url": "u", "token": "t", "migrate_history": `+c.value+`}`)
			histCfgWantHistory(t, cfg, c.want, "flat migrate_history "+c.value)
			// The flat branch also copies the ordinary fields; if the shape
			// were misdetected (e.g. migrate_history somehow tripping the
			// unified branch) these would come back empty.
			histCfgWantField(t, "flat shape", "URL", cfg.URL, "u", "flat-shape fields must survive")
			histCfgWantField(t, "flat shape", "Token", cfg.Token, "t", "flat-shape fields must survive")
		})
	}
}

// Shape 2 (command-sectioned): migrate_history is read from the nested
// "migrate" section, and an outer value wins when both are set — the same
// outer-wins-else-inner precedence skip_issue_sync and fast_sync use.
func TestLoadMigrateConfigFile_MigrateHistoryMigrateSectionedShape(t *testing.T) {
	t.Run("absent defaults to false", func(t *testing.T) {
		histCfgWantHistoryFor(t, `{"migrate": {"url": "u", "token": "t"}}`, false,
			"migrate_history is absent")
	})

	for _, c := range histCfgBoolForms {
		t.Run("inside the migrate section: "+c.name, func(t *testing.T) {
			cfg := histCfgLoad(t, `{"migrate": {"url": "u", "token": "t", "migrate_history": `+c.value+`}}`)
			histCfgWantHistory(t, cfg, c.want, "migrate-section migrate_history "+c.value)
			histCfgWantField(t, "migrate section", "URL", cfg.URL, "u",
				"the migrate section's own fields must survive")
		})

		t.Run("outer level: "+c.name, func(t *testing.T) {
			histCfgWantHistoryFor(t,
				`{"migrate_history": `+c.value+`, "migrate": {"url": "u", "token": "t"}}`,
				c.want, "outer migrate_history "+c.value)
		})
	}

	// Precedence: an explicit outer false must beat an inner true. This is the
	// case that distinguishes the implemented `.Set` check from a naive
	// `.Value` check (which would leave the inner true standing).
	t.Run("outer false wins over inner true", func(t *testing.T) {
		histCfgWantHistoryFor(t,
			`{"migrate_history": false, "migrate": {"url": "u", "token": "t", "migrate_history": true}}`,
			false, "an explicit outer false must win over the inner true")
	})

	t.Run("outer true wins over inner false", func(t *testing.T) {
		histCfgWantHistoryFor(t,
			`{"migrate_history": true, "migrate": {"url": "u", "token": "t", "migrate_history": false}}`,
			true, "an explicit outer true must win over the inner false")
	})

	// An absent outer value must not clobber the inner one back to the default.
	t.Run("inner true survives an absent outer value", func(t *testing.T) {
		histCfgWantHistoryFor(t,
			`{"concurrency": 4, "migrate": {"url": "u", "token": "t", "migrate_history": "on"}}`,
			true, "the inner value must propagate when the outer one is absent")
	})
}

// Shape 3 (side-sectioned): the "sonarcloud" block carries no migrate_history
// of its own, so the top-level field is the only way to opt in for this shape.
// sonarCloudBlock.toMigrateConfig builds a fresh MigrateConfig, so the
// top-level value has to be re-applied afterwards or it is dropped entirely.
func TestLoadMigrateConfigFile_MigrateHistorySideSectionedShape(t *testing.T) {
	t.Run("absent defaults to false", func(t *testing.T) {
		histCfgWantHistoryFor(t, `{"sonarcloud": {"url": "u", "token": "t"}}`, false,
			"migrate_history is absent")
	})

	for _, c := range histCfgBoolForms {
		t.Run(c.name, func(t *testing.T) {
			cfg := histCfgLoad(t, `{"migrate_history": `+c.value+`,
			  "sonarcloud": {"url": "u", "token": "t"},
			  "settings": {"concurrency": 3}}`)
			histCfgWantHistory(t, cfg, c.want, "side-sectioned migrate_history "+c.value)
			// Guard against the top-level re-apply being done by *replacing*
			// the config the sonarcloud block produced.
			histCfgWantField(t, "side-sectioned shape", "Token", cfg.Token, "t",
				"the sonarcloud block's fields must survive the top-level re-apply")
			histCfgWantField(t, "side-sectioned shape", "Concurrency", cfg.Concurrency, 3,
				"the settings block's fields must survive the top-level re-apply")
		})
	}
}

// Shape 4 (unified, #266): target.migrate_history wins when explicitly set,
// else the top-level value, else false.
func TestLoadMigrateConfigFile_MigrateHistoryUnifiedShape(t *testing.T) {
	t.Run("absent defaults to false", func(t *testing.T) {
		histCfgWantHistoryFor(t, `{"target": {"url": "u", "token": "t"}}`, false,
			"migrate_history is absent")
	})

	for _, c := range histCfgBoolForms {
		t.Run("target block: "+c.name, func(t *testing.T) {
			cfg := histCfgLoad(t, `{"target": {"url": "u", "token": "t", "migrate_history": `+c.value+`}}`)
			histCfgWantHistory(t, cfg, c.want, "target-block migrate_history "+c.value)
			histCfgWantField(t, "unified shape", "URL", cfg.URL, "u", "target-block fields must survive")
			histCfgWantField(t, "unified shape", "Token", cfg.Token, "t", "target-block fields must survive")
		})

		t.Run("top level with a target block: "+c.name, func(t *testing.T) {
			histCfgWantHistoryFor(t,
				`{"migrate_history": `+c.value+`, "target": {"url": "u", "token": "t"}}`,
				c.want, "top-level migrate_history "+c.value)
		})
	}

	// Explicit target false beats top-level true: the resolver checks .Set,
	// not .Value, so "I turned it off for this target" is honored.
	t.Run("target false wins over top-level true", func(t *testing.T) {
		histCfgWantHistoryFor(t,
			`{"migrate_history": true, "target": {"url": "u", "token": "t", "migrate_history": false}}`,
			false, "an explicit target false must win over the top-level true")
	})

	t.Run("target true wins over top-level false", func(t *testing.T) {
		histCfgWantHistoryFor(t,
			`{"migrate_history": false, "target": {"url": "u", "token": "t", "migrate_history": true}}`,
			true, "an explicit target true must win over the top-level false")
	})

	// A file with only a "source" block still takes the unified branch, where
	// s.Target is nil; the top-level value must still be honored instead of
	// being lost to the nil target.
	t.Run("source-only unified file still honors the top-level value", func(t *testing.T) {
		histCfgWantHistoryFor(t,
			`{"migrate_history": "yes", "source": {"url": "ignored", "token": "ignored"}}`,
			true, "the top-level value must survive a nil target block")
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
			histCfgWantHistoryOnly(t, s.name, histCfgLoad(t, s.history))
		})

		t.Run(s.name+": fast_sync does not set MigrateHistory", func(t *testing.T) {
			histCfgWantFastSyncOnly(t, s.name, histCfgLoad(t, s.fast))
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
