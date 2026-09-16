//go:build smoke

// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

// This file exists solely to hold the run-summary writer, and its name is
// load-bearing.
//
// Go runs tests in source-file order, then in declaration order within each
// file. A summary test that must observe every other test's outcome therefore
// has to live in the file that sorts LAST — "zzz_" sorts after "harness_" and
// every "tierN_". Putting the same function in harness_test.go makes it run
// FIRST, where it records nothing and silently skips.
package smoke

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestZZZ_WriteSummary writes the tier x command x status table to stdout and
// to .smoke/report-<timestamp>.md, then fails if any check failed.
//
// It is included in every Makefile -run filter on purpose, so the operator
// always gets the table regardless of which tiers ran.
func TestZZZ_WriteSummary(t *testing.T) {
	outcomesMu.Lock()
	rows := make([]outcome, len(outcomes))
	copy(rows, outcomes)
	outcomesMu.Unlock()

	if len(rows) == 0 {
		t.Skip("no outcomes recorded — no tier tests ran in this invocation")
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Tier != rows[j].Tier {
			return rows[i].Tier < rows[j].Tier
		}
		return rows[i].Command < rows[j].Command
	})

	var b strings.Builder
	var pass, fail, skip int
	b.WriteString("# Live smoke suite run\n\n")
	fmt.Fprintf(&b, "Generated: %s\n\n", time.Now().Format(time.RFC3339))
	b.WriteString("| Tier | Command | Status |\n|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", r.Tier, r.Command, r.Status)
		switch r.Status {
		case "PASS":
			pass++
		case "FAIL":
			fail++
		case "SKIP":
			skip++
		}
	}
	fmt.Fprintf(&b, "\n**%d passed, %d failed, %d skipped.**\n", pass, fail, skip)

	report := b.String()
	path := filepath.Join(smokeDir(t), fmt.Sprintf("report-%s.md", time.Now().UTC().Format("20060102T150405Z")))
	if err := os.WriteFile(path, []byte(scrubSecrets(report)), 0o644); err != nil {
		t.Errorf("writing summary report: %v", err)
	}

	// Also to stdout so `make smoke` shows the table without opening a file.
	fmt.Printf("\n%s\nWritten to %s\n", report, path)

	if fail > 0 {
		t.Errorf("%d smoke check(s) failed — see the table above", fail)
	}
}
