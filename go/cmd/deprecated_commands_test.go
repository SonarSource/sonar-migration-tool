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
