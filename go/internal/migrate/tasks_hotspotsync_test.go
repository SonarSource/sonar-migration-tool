package migrate

import (
	"testing"
)

func TestFilterActionableHotspotPairs(t *testing.T) {
	comment := hotspotComment{Login: "alice", Markdown: "please review"}

	tests := []struct {
		name          string
		pairs         []hotspotPair
		wantActionable int
	}{
		{
			name:          "empty input",
			pairs:         nil,
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
		{
			name: "REVIEWED without comments is actionable",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "REVIEWED", Comments: nil}},
			},
			wantActionable: 1,
		},
		{
			name: "REVIEWED with comments is actionable",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "REVIEWED", Comments: []hotspotComment{comment}}},
			},
			wantActionable: 1,
		},
		{
			name: "mixed bag of pairs",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "TO_REVIEW", Comments: nil}},      // not actionable
				{source: matchableHotspot{Status: "TO_REVIEW", Comments: []hotspotComment{comment}}}, // actionable (comment)
				{source: matchableHotspot{Status: "REVIEWED", Comments: nil}},        // actionable (status)
				{source: matchableHotspot{Status: "REVIEWED", Comments: []hotspotComment{comment}}},  // actionable (both)
			},
			wantActionable: 3,
		},
		{
			name: "status comparison is case-insensitive",
			pairs: []hotspotPair{
				{source: matchableHotspot{Status: "reviewed", Comments: nil}},
				{source: matchableHotspot{Status: "Reviewed", Comments: nil}},
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
