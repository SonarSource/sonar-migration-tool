// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Issue #606: schemas/config.schema.json drifted from the code. It declared
// defaults of 10 / 30 and a minimum of 1 for the two history bounds, while
// the real defaults are 0 ("no cap" / "no spacing rule"). That made the real
// default fail schema validation in editors. Pin the schema to the Go
// constants so the two cannot drift apart again.
func TestConfigSchema_HistoryBoundsMatchCode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", "config.schema.json"))
	if err != nil {
		t.Fatalf("reading schema: %v", err)
	}
	// Other properties carry non-integer defaults, so decode each one lazily.
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parsing schema: %v", err)
	}

	cases := []struct {
		field       string
		wantDefault int
	}{
		{"history_max_points", DefaultHistoryMaxPoints},
		{"history_min_interval_days", DefaultHistoryMinIntervalDays},
	}
	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			rawProp, ok := schema.Properties[c.field]
			if !ok {
				t.Fatalf("schema has no %q property", c.field)
			}
			var p struct {
				Minimum *int `json:"minimum"`
				Default *int `json:"default"`
			}
			if err := json.Unmarshal(rawProp, &p); err != nil {
				t.Fatalf("parsing %q: %v", c.field, err)
			}
			if p.Default == nil || *p.Default != c.wantDefault {
				t.Errorf("default: got %v, want %d", schemaIntOrNil(p.Default), c.wantDefault)
			}
			// The default must itself be valid under the schema.
			if p.Minimum != nil && *p.Minimum > c.wantDefault {
				t.Errorf("minimum %d rejects the real default %d", *p.Minimum, c.wantDefault)
			}
		})
	}
}

func schemaIntOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
