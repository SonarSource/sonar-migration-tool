// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// maxTruncationLines caps how many truncation records the end-of-run
// console block spells out. An instance where every one of 1,139
// projects hits the component-tree ceiling would otherwise print 1,139
// lines and bury the summary it is supposed to deliver; the artefact
// keeps every record either way. Mirrors maxListedLimitationUsers in
// the report's limitation notes (#475, #574).
const maxTruncationLines = 10

// flushTruncation persists the run's truncation records under the
// extract directory, if there are any.
//
// It is called on BOTH exits of the phase loop. A run that dies in
// phase 4 still lost data in phase 2, and that evidence exists nowhere
// but in the tracker — losing it would leave the operator with a failed
// run and no record of what the successful part of it silently dropped.
//
// A write failure cannot be returned without masking the task error
// that may be on its way out, so it is logged at Error level: the
// artefact is the only channel to the migration report, so failing to
// write it means the report will claim a clean run.
func flushTruncation(extractDir string, tracker *TruncationTracker, logger *slog.Logger) {
	if !tracker.HasRecords() {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	path := filepath.Join(extractDir, TruncationEventsFile)
	if err := tracker.WriteJSON(path); err != nil {
		logger.Error("writing the truncation artefact failed - the migration report will not report this run's lost data",
			"path", path, "error", err)
		return
	}
	state := tracker.State()
	logger.Warn("some API responses were truncated - see the truncation artefact for the full set",
		"path", path, "records", len(state.Records), "lost", state.TotalLost)
}

// PrintTruncationBlock writes the end-of-run truncation summary to w,
// and nothing at all when the run truncated nothing.
//
// The call site writes to os.Stderr BEFORE the "Extract Complete" line:
// a data-loss warning printed after a success banner is a warning the
// operator scrolls past. The format follows the "N project(s) skipped"
// block in cmd/extract.go so the two read as one family (#574).
func PrintTruncationBlock(w io.Writer, state common.TruncationState) {
	lines := truncationLines(state)
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s truncated API response(s), %s item(s) not extracted:\n",
		common.FormatCount(len(state.Records)), common.FormatCount(lostTotal(state)))
	for _, line := range lines {
		fmt.Fprintf(w, "  - %s\n", line)
	}
	fmt.Fprintf(w, "  Full details: %s in the extract directory.\n", TruncationEventsFile)
}

// truncationLines renders one line per record, sorted by scope then
// endpoint then reason so two runs over the same instance print the same
// block in the same order, and capped with an "(and N more)" tail.
func truncationLines(state common.TruncationState) []string {
	if len(state.Records) == 0 {
		return nil
	}
	recs := append([]common.TruncationRecord(nil), state.Records...)
	sort.Slice(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		if la, lb := a.Scope.Label(), b.Scope.Label(); la != lb {
			return la < lb
		}
		if a.Endpoint != b.Endpoint {
			return a.Endpoint < b.Endpoint
		}
		return a.Reason < b.Reason
	})

	lines := make([]string, 0, len(recs)+1)
	for _, rec := range recs {
		if len(lines) == maxTruncationLines {
			lines = append(lines, fmt.Sprintf("(and %s more)", common.FormatCount(len(recs)-maxTruncationLines)))
			break
		}
		lines = append(lines, truncationLine(rec))
	}
	return lines
}

// truncationLine describes one record in one sentence. A total the
// server never reported renders as "unknown" rather than as 0, because
// "0 of 0 fetched" reads like a clean empty response.
//
// Counts carry thousands separators so the console block and the
// migration report's Limitations bullet write the same loss the same
// way: "7,919", never "7919" in one place and "7,919" in the other
// (#574).
func truncationLine(rec common.TruncationRecord) string {
	total := "unknown"
	if rec.TotalKnown {
		total = common.FormatCount(rec.Total)
	}
	line := fmt.Sprintf("%s: %s fetched %s of %s (%s)",
		rec.Scope.Label(), rec.Endpoint, common.FormatCount(rec.Fetched), total, rec.Reason)
	if rec.Lost > 0 {
		line += fmt.Sprintf(", %s lost", common.FormatCount(rec.Lost))
	}
	if rec.WindowStart != "" || rec.WindowEnd != "" {
		line += fmt.Sprintf(", window [%s, %s)", rec.WindowStart, rec.WindowEnd)
	}
	return line
}

// lostTotal sums the records rather than trusting state.TotalLost. The
// field is filled by the tracker's snapshot and by the artefact reader,
// but a hand-built state is a legitimate caller too, and a block
// announcing "0 item(s) not extracted" above a list of losses is worse
// than no block at all.
func lostTotal(state common.TruncationState) int {
	total := 0
	for _, rec := range state.Records {
		total += rec.Lost
	}
	return total
}
