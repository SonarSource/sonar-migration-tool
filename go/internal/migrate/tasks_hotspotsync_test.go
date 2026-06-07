// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"testing"
)

func TestFilterActionableHotspotPairs(t *testing.T) {
	comment := hotspotComment{Login: "alice", Markdown: "please review"}

	tests := []struct {
		name           string
		pairs          []hotspotPair
		wantActionable int
	}{
		{
			name:           "empty input",
			pairs:          nil,
			wantActionable: 0,
		},
		{
			name: "TO_REVIEW without comments is not actionable",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "TO_REVIEW", Comments: nil}},
			},
			wantActionable: 0,
		},
		{
			name: "TO_REVIEW with comments is actionable",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "TO_REVIEW", Comments: []hotspotComment{comment}}},
			},
			wantActionable: 1,
		},
		// #350: REVIEWED without a real resolution carries nothing to
		// sync — drop those.
		{
			name: "REVIEWED with no resolution is NOT actionable",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "REVIEWED", Resolution: "", Comments: nil}},
			},
			wantActionable: 0,
		},
		{
			name: "REVIEWED + SAFE is actionable",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "REVIEWED", Resolution: "SAFE", Comments: nil}},
			},
			wantActionable: 1,
		},
		{
			name: "REVIEWED + ACKNOWLEDGED is actionable",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "REVIEWED", Resolution: "ACKNOWLEDGED", Comments: nil}},
			},
			wantActionable: 1,
		},
		{
			name: "REVIEWED + FIXED is actionable",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "REVIEWED", Resolution: "FIXED", Comments: nil}},
			},
			wantActionable: 1,
		},
		{
			name: "REVIEWED + unknown resolution is NOT actionable without comments",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "REVIEWED", Resolution: "WHATEVER", Comments: nil}},
			},
			wantActionable: 0,
		},
		{
			name: "REVIEWED + unknown resolution still picked up via comments",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "REVIEWED", Resolution: "WHATEVER", Comments: []hotspotComment{comment}}},
			},
			wantActionable: 1,
		},
		{
			name: "REVIEWED + SAFE with comments is actionable",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "REVIEWED", Resolution: "SAFE", Comments: []hotspotComment{comment}}},
			},
			wantActionable: 1,
		},
		{
			name: "mixed bag of pairs",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "TO_REVIEW", Comments: nil}},                                            // not actionable
				{source: matchableHotspot{Status: "TO_REVIEW", Comments: []hotspotComment{comment}}},                      // actionable (comment)
				{source: matchableHotspot{Status: "REVIEWED", Resolution: "", Comments: nil}},                             // NOT actionable (no resolution)
				{source: matchableHotspot{Status: "REVIEWED", Resolution: "SAFE", Comments: nil}},                         // actionable (resolution)
				{source: matchableHotspot{Status: "REVIEWED", Resolution: "FIXED", Comments: []hotspotComment{comment}}},  // actionable (both)
			},
			wantActionable: 3,
		},
		{
			name: "status / resolution comparison is case-insensitive",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "reviewed", Resolution: "safe", Comments: nil}},
				{source: matchableHotspot{Status: "Reviewed", Resolution: "Acknowledged", Comments: nil}},
			},
			wantActionable: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterActionableHotspotPairs(tc.pairs)
			if len(got) != tc.wantActionable {
				t.Errorf("filterActionableHotspotPairs() returned %d pairs, want %d", len(got), tc.wantActionable)
			}
		})
	}
}
