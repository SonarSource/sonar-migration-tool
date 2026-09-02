// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"encoding/json"
	"testing"
	"time"
)

func mkPoint(daysFromEpoch int) historyPoint {
	return historyPoint{
		Date:           time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, daysFromEpoch),
		ProjectVersion: "1.0",
	}
}

func TestSelectBoundedHistoryPointsEmpty(t *testing.T) {
	if got := selectBoundedHistoryPoints(nil, 10, 30); got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
}

func TestSelectBoundedHistoryPointsSinglePoint(t *testing.T) {
	// A single analysis is itself "the latest" and gets dropped — nothing
	// left to treat as history.
	points := []historyPoint{mkPoint(0)}
	if got := selectBoundedHistoryPoints(points, 10, 30); got != nil {
		t.Errorf("expected nil for a single point, got %v", got)
	}
}

func TestSelectBoundedHistoryPointsDropsNewest(t *testing.T) {
	points := []historyPoint{mkPoint(0), mkPoint(40)}
	got := selectBoundedHistoryPoints(points, 10, 30)
	if len(got) != 1 {
		t.Fatalf("expected 1 point (the newest dropped), got %d", len(got))
	}
	if !got[0].Date.Equal(points[0].Date) {
		t.Errorf("expected the oldest point to survive, got date %v", got[0].Date)
	}
}

func TestSelectBoundedHistoryPointsEnforcesMinInterval(t *testing.T) {
	// Five points 10 days apart, plus a final "newest" point that gets
	// dropped. minIntervalDays=30 should keep every third point (0, 30, 60)
	// out of the 0/10/20/30/40 candidates that remain after dropping the
	// newest (50).
	points := []historyPoint{
		mkPoint(0), mkPoint(10), mkPoint(20), mkPoint(30), mkPoint(40), mkPoint(50),
	}
	got := selectBoundedHistoryPoints(points, 0, 30) // maxPoints=0: no cap, interval-only.
	wantDays := []int{0, 30}
	if len(got) != len(wantDays) {
		t.Fatalf("expected %d points, got %d: %v", len(wantDays), len(got), got)
	}
	for i, d := range wantDays {
		want := mkPoint(d).Date
		if !got[i].Date.Equal(want) {
			t.Errorf("point %d: expected %v, got %v", i, want, got[i].Date)
		}
	}
}

func TestSelectBoundedHistoryPointsCapsAndSpansFullRange(t *testing.T) {
	// 21 points 10 days apart (0..200), newest dropped -> 20 remain
	// (0..190). minIntervalDays=0 means the interval stage keeps all 20.
	// maxPoints=4 must then subsample down to 4 points spanning the WHOLE
	// range, not just the oldest 4.
	var points []historyPoint
	for i := 0; i <= 20; i++ {
		points = append(points, mkPoint(i*10))
	}
	got := selectBoundedHistoryPoints(points, 4, 0)
	if len(got) != 4 {
		t.Fatalf("expected 4 points, got %d: %v", len(got), got)
	}
	// First selected point must be the oldest surviving candidate (day 0)
	// and the last must be the newest surviving candidate (day 190) — i.e.
	// the selection spans the full remaining range instead of clustering
	// near the oldest end.
	if !got[0].Date.Equal(mkPoint(0).Date) {
		t.Errorf("expected first point at day 0, got %v", got[0].Date)
	}
	if !got[3].Date.Equal(mkPoint(190).Date) {
		t.Errorf("expected last point at day 190, got %v", got[3].Date)
	}
	// Points must be strictly increasing (no duplicates from the subsample).
	for i := 1; i < len(got); i++ {
		if !got[i].Date.After(got[i-1].Date) {
			t.Errorf("point %d (%v) is not after point %d (%v)", i, got[i].Date, i-1, got[i-1].Date)
		}
	}
}

func TestSelectBoundedHistoryPointsMaxPointsOne(t *testing.T) {
	points := []historyPoint{mkPoint(0), mkPoint(10), mkPoint(20), mkPoint(9999)}
	got := selectBoundedHistoryPoints(points, 1, 0)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 point, got %d", len(got))
	}
	if !got[0].Date.Equal(mkPoint(0).Date) {
		t.Errorf("expected the oldest point, got %v", got[0].Date)
	}
}

func TestSelectBoundedHistoryPointsNoCapWhenUnderLimit(t *testing.T) {
	points := []historyPoint{mkPoint(0), mkPoint(40), mkPoint(80), mkPoint(9999)}
	got := selectBoundedHistoryPoints(points, 10, 30)
	// 4 candidates, newest dropped -> 3 left, all >=30 days apart, under
	// the cap of 10 -> all 3 survive untouched.
	if len(got) != 3 {
		t.Fatalf("expected 3 points, got %d: %v", len(got), got)
	}
}

// A negative --history_min_interval_days is clamped to 0 rather than
// producing a negative minInterval. With a negative minInterval the
// spacing test (p.Date.Sub(last) >= minInterval) would also accept a
// point that is EARLIER than the one already selected, so the clamp is
// what guarantees the returned slice is strictly advancing in time.
func TestSelectBoundedHistoryPointsNegativeIntervalTreatedAsZero(t *testing.T) {
	// Already-ordered input: a negative interval must behave exactly like
	// 0 — every candidate survives once the newest is dropped.
	ordered := []historyPoint{mkPoint(0), mkPoint(1), mkPoint(2), mkPoint(3)}
	got := selectBoundedHistoryPoints(ordered, 0, -5)
	want := selectBoundedHistoryPoints(ordered, 0, 0)
	if len(got) != 3 || len(want) != 3 {
		t.Fatalf("expected 3 points for both, got %d (negative) and %d (zero)", len(got), len(want))
	}
	for i := range got {
		if !got[i].Date.Equal(want[i].Date) {
			t.Errorf("point %d: negative interval gave %v, zero interval gave %v",
				i, got[i].Date, want[i].Date)
		}
	}

	// Out-of-order input is what actually distinguishes the clamp from its
	// absence: day 5 comes after day 10, so Sub(last) is -5 days. Clamped to
	// 0 it is rejected; unclamped (-5 days) it would slip through and the
	// result would contain a point that goes backwards in time.
	regressing := []historyPoint{mkPoint(0), mkPoint(10), mkPoint(5), mkPoint(100)}
	out := selectBoundedHistoryPoints(regressing, 0, -5)
	if len(out) != 2 {
		t.Fatalf("expected the regressing point to be dropped (2 points), got %d: %v", len(out), out)
	}
	for i := 1; i < len(out); i++ {
		if !out[i].Date.After(out[i-1].Date) {
			t.Errorf("result is not strictly advancing: point %d (%v) does not follow point %d (%v)",
				i, out[i].Date, i-1, out[i-1].Date)
		}
	}
}

func TestMatchHistoricalMeasuresExactTimestamp(t *testing.T) {
	raw := json.RawMessage(`{
		"measures": [
			{"metric": "ncloc", "history": [
				{"date": "2024-05-14T10:27:58+0000", "value": "100"},
				{"date": "2024-05-14T10:45:33+0000", "value": "200"},
				{"date": "2024-05-14T11:10:14+0000", "value": "300"}
			]}
		]
	}`)
	target := parseHistoryDate("2024-05-14T10:45:33+0000")
	got := matchHistoricalMeasures(raw, target)
	if len(got) != 1 || got[0]["metric"] != "ncloc" || got[0]["value"] != "200" {
		t.Fatalf("expected ncloc=200 at the exact timestamp, got %v", got)
	}
}

func TestMatchHistoricalMeasuresFallsBackToClosestBefore(t *testing.T) {
	raw := json.RawMessage(`{
		"measures": [
			{"metric": "ncloc", "history": [
				{"date": "2024-05-14T10:00:00+0000", "value": "100"},
				{"date": "2024-05-14T14:00:00+0000", "value": "999"}
			]}
		]
	}`)
	// No exact match for this timestamp; the closest entry AT OR BEFORE it
	// (10:00, not the later 14:00) should win.
	target := parseHistoryDate("2024-05-14T12:00:00+0000")
	got := matchHistoricalMeasures(raw, target)
	if len(got) != 1 || got[0]["value"] != "100" {
		t.Fatalf("expected the closest prior value (100), got %v", got)
	}
}

func TestMatchHistoricalMeasuresSkipsEmptyMetric(t *testing.T) {
	raw := json.RawMessage(`{"measures": [{"metric": "coverage", "history": []}]}`)
	got := matchHistoricalMeasures(raw, time.Now())
	if len(got) != 0 {
		t.Errorf("expected no measures for an empty history, got %v", got)
	}
}

// A response body that does not decode must yield no measures at all —
// never a half-decoded set. The second case is the one that actually
// pins the `return nil` arm: encoding/json rejects the whole document up
// front on a *syntax* error (so a truncated body decodes to nothing
// either way), but on a *type* error it populates everything it managed
// to read before failing. Without the error check, that partial first
// measure would be reported as if it were a complete result.
func TestMatchHistoricalMeasuresInvalidJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"syntax error", `{"measures": [{"metric": "ncloc"`},
		{"not an object", `[]`},
		{
			// First measure is well-formed and would decode; the second
			// has a number where the schema wants a string.
			"type error after a decodable measure",
			`{"measures": [
				{"metric": "ncloc", "history": [{"date": "2024-01-01T00:00:00+0000", "value": "100"}]},
				{"metric": 123, "history": []}
			]}`,
		},
	}
	target := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchHistoricalMeasures(json.RawMessage(c.raw), target); got != nil {
				t.Errorf("expected nil for an undecodable response, got %v", got)
			}
		})
	}
}

func TestParseHistoryDateFormats(t *testing.T) {
	cases := []string{
		"2024-05-14T10:45:33Z",
		"2024-05-14T10:45:33+00:00",
		"2024-05-14T10:45:33+0000",
	}
	for _, c := range cases {
		if parseHistoryDate(c).IsZero() {
			t.Errorf("expected %q to parse", c)
		}
	}
	if !parseHistoryDate("not-a-date").IsZero() {
		t.Error("expected an unparseable date to return the zero time")
	}
	if !parseHistoryDate("").IsZero() {
		t.Error("expected an empty string to return the zero time")
	}
}
