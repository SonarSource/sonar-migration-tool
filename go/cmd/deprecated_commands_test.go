// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// #503: the notice says the command is deprecated, names it, and points
// at extract.
func TestWarnDeprecatedByExtract(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	warnDeprecatedByExtract("mappings")

	out := buf.String()
	for _, want := range []string{"level=WARN", "deprecated", "extract", "command=mappings"} {
		if !strings.Contains(out, want) {
			t.Errorf("notice %q does not contain %q", out, want)
		}
	}
}

// #503: -h lists structure and mappings as deprecated, and still lists
// them (cobra's Command.Deprecated would hide them).
func TestStructureAndMappingsMarkedDeprecatedInHelp(t *testing.T) {
	for _, c := range []*cobra.Command{structureCmd, mappingsCmd} {
		if !strings.HasPrefix(c.Short, deprecatedByExtractShortPrefix) {
			t.Errorf("%s: Short %q does not start with %q", c.Name(), c.Short, deprecatedByExtractShortPrefix)
		}
		if !strings.HasPrefix(c.Long, "Deprecated:") {
			t.Errorf("%s: Long does not start with the deprecation note", c.Name())
		}
		if c.Hidden || c.Deprecated != "" {
			t.Errorf("%s: must stay visible in -h", c.Name())
		}
	}
}
