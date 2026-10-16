// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestPageLimitAt(t *testing.T) {
	cases := []struct {
		name                           string
		limit, requestedSize, pageSize int
		want                           int
	}{
		{"no cap stays uncapped", 0, 500, 50, 0},
		{"no fallback", 20, 500, 500, 20},
		{"caller asked for a small size", 20, 100, 100, 20},
		{"500 to 200", 20, 500, 200, 50},
		{"500 to 100", 20, 500, 100, 100},
		{"500 to 50", 20, 500, 50, 200},
		{"webhook sample to 200", 10, 500, 200, 25},
		{"webhook sample to 50", 10, 500, 50, 100},
		{"rounds up", 3, 150, 100, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pageLimitAt(tc.limit, tc.requestedSize, tc.pageSize)
			if got != tc.want {
				t.Fatalf("pageLimitAt(%d, %d, %d) = %d, want %d",
					tc.limit, tc.requestedSize, tc.pageSize, got, tc.want)
			}
			if tc.limit > 0 && got*tc.pageSize < tc.limit*tc.requestedSize {
				t.Errorf("ceiling fell: %d x %d < %d x %d",
					got, tc.pageSize, tc.limit, tc.requestedSize)
			}
		})
	}
}

// TestAdaptiveFallbackKeepsPageCapCeiling reproduces the review finding
// on #645: an earlier fetch drops the endpoint to ps=100, and a later
// capped fetch on the same endpoint must still read every item up to the
// 20 x 500 ceiling it was sized for, not 20 x 100 = 2,000. A project past
// that ceiling is still clamped at exactly 10,000, never beyond it.
func TestAdaptiveFallbackKeepsPageCapCeiling(t *testing.T) {
	const bigProject = 2500
	totals := map[string]int{"small": 10, "big": bigProject, "over": 12000}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if size, _ := strconv.Atoi(r.URL.Query().Get("ps")); size > 100 {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		writeAdaptiveTestPage(t, w, r, totals[r.URL.Query().Get("project")])
	}))
	defer server.Close()

	raw := NewRawClient(server.Client(), server.URL+"/")
	captureDefaultLogger(t)
	var records []TruncationRecord
	raw.SetTruncationObserver(func(rec TruncationRecord) { records = append(records, rec) })

	fetch := func(project string) PageResult {
		t.Helper()
		res, err := raw.GetPaginatedResult(context.Background(), PaginatedOpts{
			Path:      "api/hotspots/search",
			Params:    url.Values{"project": {project}},
			ResultKey: "items",
			PageLimit: 20,
		})
		if err != nil {
			t.Fatalf("fetch %s: %v", project, err)
		}
		return res
	}

	if first := fetch("small"); first.PageSize != 100 {
		t.Fatalf("first fetch page size = %d, want the fallback to 100", first.PageSize)
	}

	res := fetch("big")
	if res.PageSize != 100 || res.PageLimit != 100 {
		t.Errorf("page size %d, page limit %d: want 100 and 100", res.PageSize, res.PageLimit)
	}
	if res.Truncated || res.Fetched != bigProject {
		t.Errorf("fetched %d of %d (truncated=%v, reason=%q), want every item",
			res.Fetched, bigProject, res.Truncated, res.Reason)
	}
	assertCompleteUniqueKeys(t, res.Items, bigProject)
	if len(records) != 0 {
		t.Fatalf("truncation records = %+v, want none", records)
	}

	// The record must state the cap the fetch really used, so the report
	// names the server's limit and not a lower one the tool set.
	over := fetch("over")
	if over.Fetched != ResultWindowLimit || !over.Truncated || over.Reason != ReasonPageLimitClamp {
		t.Errorf("over project: fetched=%d truncated=%v reason=%q, want %d, true, %q",
			over.Fetched, over.Truncated, over.Reason, ResultWindowLimit, ReasonPageLimitClamp)
	}
	if len(records) != 1 || records[0].Lost != 2000 || records[0].PageSize != 100 || records[0].PageLimit != 100 {
		t.Errorf("records = %+v, want one: 2,000 lost at page size 100, page limit 100", records)
	}
}
