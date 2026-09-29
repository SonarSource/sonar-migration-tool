// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// TestPrintTruncationBlockCarriesExactCountsAndTheArtefactPath prevents
// the block degrading into a vague "some data may be missing" notice.
// The operator has to be able to tell, without opening anything, which
// task and project lost data, how much, and where the full record is —
// a warning that cannot be acted on is the silence this change exists
// to remove (#574).
func TestPrintTruncationBlockCarriesExactCountsAndTheArtefactPath(t *testing.T) {
	state := common.TruncationState{
		Records: []common.TruncationRecord{
			{
				Endpoint:   "api/measures/component_tree",
				Reason:     common.ReasonPageLimitClamp,
				Scope:      common.TruncationScope{Task: "getProjectComponentTree", ProjectKey: "alpha", Branch: "main"},
				Total:      24000,
				TotalKnown: true,
				Fetched:    10000,
				Lost:       14000,
			},
			{
				Endpoint:   "api/hotspots/search",
				Reason:     common.ReasonPageLimitClamp,
				Scope:      common.TruncationScope{Task: "getProjectHotspotsFull", ProjectKey: "beta", Branch: "main", Detail: "status=TO_REVIEW"},
				Total:      11000,
				TotalKnown: true,
				Fetched:    10000,
				Lost:       1000,
			},
		},
	}

	var buf bytes.Buffer
	PrintTruncationBlock(&buf, state)
	got := buf.String()

	wants := []string{
		"2 truncated API response(s), 15,000 item(s) not extracted",
		"getProjectComponentTree alpha@main",
		"api/measures/component_tree fetched 10,000 of 24,000 (page_limit_clamp), 14,000 lost",
		"getProjectHotspotsFull beta@main (status=TO_REVIEW)",
		"api/hotspots/search fetched 10,000 of 11,000 (page_limit_clamp), 1,000 lost",
		TruncationEventsFile,
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("truncation block missing %q:\n%s", want, got)
		}
	}
}

// TestPrintTruncationBlockIsEmptyForACleanState prevents a clean run
// growing a scary empty warning block. A reconciliation on its own is
// evidence, not a finding, so it must not print either — otherwise
// every sliced project would announce itself as data loss (#574).
func TestPrintTruncationBlockIsEmptyForACleanState(t *testing.T) {
	cases := []struct {
		name  string
		state common.TruncationState
	}{
		{
			name:  "a run that truncated nothing",
			state: common.TruncationState{},
		},
		{
			name: "a run with a reconciliation but no truncation",
			state: common.TruncationState{
				Reconciliations: []common.Reconciliation{{
					Scope:       common.TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "alpha"},
					TotalBefore: 14903,
					TotalAfter:  14903,
					Unique:      14903,
				}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			PrintTruncationBlock(&buf, tc.state)
			if got := buf.String(); got != "" {
				t.Errorf("expected no output, got %q", got)
			}
		})
	}
}

// TestTruncationLinesCapAtTen prevents the block burying its own summary.
// An instance where every project hits the component-tree ceiling
// produces one record per project+branch, and printing 1,139 of them
// scrolls the counts off the operator's screen. The artefact still
// holds every record; only the console list is capped (#574).
func TestTruncationLinesCapAtTen(t *testing.T) {
	state := common.TruncationState{}
	for i := range 13 {
		state.Records = append(state.Records, common.TruncationRecord{
			Endpoint:   "api/measures/component_tree",
			Reason:     common.ReasonPageLimitClamp,
			Scope:      common.TruncationScope{Task: "getProjectComponentTree", ProjectKey: fmt.Sprintf("proj%02d", i), Branch: "main"},
			Total:      20000,
			TotalKnown: true,
			Fetched:    10000,
			Lost:       10000,
		})
	}

	lines := truncationLines(state)
	if len(lines) != maxTruncationLines+1 {
		t.Fatalf("expected %d lines (%d records plus the tail), got %d: %v",
			maxTruncationLines+1, maxTruncationLines, len(lines), lines)
	}
	if want := "(and 3 more)"; lines[len(lines)-1] != want {
		t.Errorf("tail line: got %q, want %q", lines[len(lines)-1], want)
	}
	// Sorted by scope label, so the cap keeps the first ten projects and
	// the eleventh is the one folded into the tail.
	if !strings.Contains(lines[0], "proj00") {
		t.Errorf("expected the first line to be the lowest-sorting scope, got %q", lines[0])
	}
	for _, line := range lines[:maxTruncationLines] {
		if strings.Contains(line, "proj10") || strings.Contains(line, "proj11") || strings.Contains(line, "proj12") {
			t.Errorf("expected the last three records to be folded into the tail, got %q", line)
		}
	}
}

// TestTruncationLineNamesAnUnknownTotalAsUnknown prevents the block
// printing "0 of 0 fetched" for the unknown_total class, which reads as
// a clean empty response and is exactly the shape this change exists to
// stop hiding: no total plus a full page means an unknown amount was
// left behind (#574).
func TestTruncationLineNamesAnUnknownTotalAsUnknown(t *testing.T) {
	got := truncationLine(common.TruncationRecord{
		Endpoint:   "api/rules/search",
		Reason:     common.ReasonUnknownTotal,
		Scope:      common.TruncationScope{Task: "getProfileRules"},
		TotalKnown: false,
		Fetched:    500,
	})
	if !strings.Contains(got, "fetched 500 of unknown (unknown_total)") {
		t.Errorf("unknown total rendered wrongly: got %q", got)
	}
	if strings.Contains(got, "lost") {
		t.Errorf("an unknown total cannot carry a lost count: got %q", got)
	}
}
