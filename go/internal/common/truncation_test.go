// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteJSONSkipsCleanRuns prevents the failure where every extract,
// however healthy, drops a zero-information artefact into the export
// directory — which the report collector would then have to
// second-guess, and which would make "the file exists" stop meaning
// "something was lost" (#574).
func TestWriteJSONSkipsCleanRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), TruncationEventsFile)

	tracker := NewTruncationTracker()
	// A reconciliation on its own is evidence, not a finding: a walk
	// that accounted for everything must still leave no artefact.
	tracker.RecordReconciliation(Reconciliation{
		Scope:       TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "proj"},
		TotalBefore: 14903, TotalAfter: 14903, Unique: 14903,
	})
	if err := tracker.WriteJSON(path); err != nil {
		t.Fatalf("WriteJSON on a clean tracker failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("clean run wrote %s (err=%v), want no file", path, err)
	}
	if tracker.HasRecords() {
		t.Error("HasRecords() = true for a tracker holding only a reconciliation, want false")
	}
}

// TestWriteJSONMergePreservesRecordsFromASkippedTask prevents the
// failure that three independent reviewers found: an extract resumed
// with --extract_id reuses the directory and skips the tasks that
// already completed, so their truncation records exist only in the file.
// A plain write would delete the evidence that the first attempt lost
// data and leave the migration report claiming a clean run (#574).
func TestWriteJSONMergePreservesRecordsFromASkippedTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), TruncationEventsFile)

	first := NewTruncationTracker()
	first.Record(TruncationRecord{
		Endpoint: "api/measures/component_tree",
		Reason:   ReasonPageLimitClamp,
		Scope:    TruncationScope{Task: "getComponentTree", ProjectKey: "alpha", Branch: "main"},
		Total:    12000, TotalKnown: true, Fetched: 10000, Lost: 2000,
	})
	if err := first.WriteJSON(path); err != nil {
		t.Fatalf("first WriteJSON failed: %v", err)
	}

	// Second attempt: getComponentTree is skipped as already complete,
	// so this tracker has never heard of it.
	second := NewTruncationTracker()
	second.Record(TruncationRecord{
		Endpoint: "api/hotspots/search",
		Reason:   ReasonPageLimitClamp,
		Scope:    TruncationScope{Task: "getProjectHotspots", ProjectKey: "beta", Detail: "status=TO_REVIEW"},
		Total:    10500, TotalKnown: true, Fetched: 10000, Lost: 500,
	})
	if err := second.WriteJSON(path); err != nil {
		t.Fatalf("second WriteJSON failed: %v", err)
	}

	state, err := ReadTruncationState(path)
	if err != nil {
		t.Fatalf("ReadTruncationState failed: %v", err)
	}
	if len(state.Records) != 2 {
		t.Fatalf("merged state has %d record(s), want 2 (the skipped task's record was clobbered)", len(state.Records))
	}
	if state.TotalLost != 2500 {
		t.Errorf("TotalLost = %d, want 2500", state.TotalLost)
	}
	endpoints := map[string]bool{}
	for _, rec := range state.Records {
		endpoints[rec.Endpoint] = true
	}
	for _, want := range []string{"api/measures/component_tree", "api/hotspots/search"} {
		if !endpoints[want] {
			t.Errorf("merged state is missing a record for %s", want)
		}
	}
}

// TestMergeKeepsTheLargerLostOnIdentityCollision prevents the failure
// where a retried or resumed fetch that got LESS far than the first
// attempt talks the reported loss down, understating the data gap in the
// report (#574).
func TestMergeKeepsTheLargerLostOnIdentityCollision(t *testing.T) {
	scope := TruncationScope{Task: "getProjectIssuesFull", ProjectKey: "alpha", Branch: "main"}
	record := func(lost, fetched int) TruncationRecord {
		return TruncationRecord{
			Endpoint: "api/issues/search",
			Reason:   ReasonAtomicWindow,
			Scope:    scope,
			Total:    22000, TotalKnown: true, Fetched: fetched, Lost: lost,
			WindowStart: "2025-12-30T03:02:17+0000",
			WindowEnd:   "2025-12-30T03:02:18+0000",
		}
	}

	cases := []struct {
		name           string
		base, incoming TruncationRecord
		wantLost       int
	}{
		{"the second attempt lost more", record(2000, 20000), record(12000, 10000), 12000},
		{"the second attempt lost less", record(12000, 10000), record(2000, 20000), 12000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged := MergeTruncationStates(
				TruncationState{Records: []TruncationRecord{tc.base}},
				TruncationState{Records: []TruncationRecord{tc.incoming}},
			)
			if len(merged.Records) != 1 {
				t.Fatalf("merged %d record(s), want 1 — identical identity must collapse", len(merged.Records))
			}
			if got := merged.Records[0].Lost; got != tc.wantLost {
				t.Errorf("merged Lost: got %d, want %d", got, tc.wantLost)
			}
			if got := merged.TotalLost; got != tc.wantLost {
				t.Errorf("merged TotalLost: got %d, want %d (must be recomputed, never added)", got, tc.wantLost)
			}
		})
	}

	// A different window in the same project is a different truncation.
	other := record(500, 10000)
	other.WindowStart = "2026-01-02T00:00:00+0000"
	other.WindowEnd = "2026-01-02T00:00:01+0000"
	merged := MergeTruncationStates(
		TruncationState{Records: []TruncationRecord{record(2000, 20000)}},
		TruncationState{Records: []TruncationRecord{other}},
	)
	if len(merged.Records) != 2 {
		t.Errorf("two distinct windows merged to %d record(s), want 2", len(merged.Records))
	}
}

// TestUnexplainedDriftChurnBand prevents two opposite failures: a single
// issue closed during a multi-hour extract being reported as a data-loss
// defect, and a genuinely dropped seam being written off as churn on a
// project that also has a legitimately atomic window — the case the
// earlier "switch on Unsplittable first" ordering hid (#574).
func TestUnexplainedDriftChurnBand(t *testing.T) {
	cases := []struct {
		before, after, unique, expectedLoss int
		wantDrift, wantChurn, wantResidual  int
	}{
		// Quiet server, everything accounted for.
		{14903, 14903, 14903, 0, 0, 0, 0},
		// Quiet server, seven issues missing: a real bug signal.
		{14903, 14903, 14896, 0, 7, 0, 7},
		// Seven issues purged mid-walk: the drift is fully explained.
		{14903, 14896, 14896, 0, 7, -7, 0},
		// Three issues created mid-walk and collected: negative drift,
		// still fully explained.
		{14903, 14906, 14906, 0, -3, 3, 0},
		// An atomic window: its loss is recorded, so drift is zero and
		// no count_drift record is produced on top of it.
		{22000, 22000, 10000, 12000, 0, 0, 0},
		// Same atomic window plus a genuinely dropped seam second: the
		// recorded loss must not absorb it.
		{22000, 22000, 9993, 12000, 7, 0, 7},
		// Churn explains part of a larger drift, not all of it.
		{14903, 14896, 14893, 0, 10, -7, 3},
	}
	for _, tc := range cases {
		rec := Reconciliation{
			TotalBefore: tc.before, TotalAfter: tc.after,
			Unique: tc.unique, ExpectedLoss: tc.expectedLoss,
		}
		if got := rec.Churn(); got != tc.wantChurn {
			t.Errorf("Churn() with before=%d after=%d = %d, want %d",
				tc.before, tc.after, got, tc.wantChurn)
		}
		if got := rec.Drift(); got != tc.wantDrift {
			t.Errorf("Drift() with before=%d unique=%d expectedLoss=%d = %d, want %d",
				tc.before, tc.unique, tc.expectedLoss, got, tc.wantDrift)
		}
		if got := rec.UnexplainedDrift(); got != tc.wantResidual {
			t.Errorf("UnexplainedDrift() with before=%d after=%d unique=%d expectedLoss=%d = %d, want %d",
				tc.before, tc.after, tc.unique, tc.expectedLoss, got, tc.wantResidual)
		}
	}
}

// TestNilTrackerIsInert prevents the failure where the hand-built
// Executors in the existing tests — none of which know about a tracker —
// panic on a nil pointer the first time any task truncates (#574).
func TestNilTrackerIsInert(t *testing.T) {
	var tracker *TruncationTracker

	// Exactly how the raw client receives it: a method value taken from
	// a nil pointer, called from the request path.
	observer := tracker.Record
	observer(TruncationRecord{Endpoint: "api/issues/search", Reason: ReasonUnknownTotal})
	tracker.RecordReconciliation(Reconciliation{TotalBefore: 1})

	if tracker.HasRecords() {
		t.Error("HasRecords() = true on a nil tracker, want false")
	}
	if state := tracker.State(); len(state.Records) != 0 || state.TotalLost != 0 {
		t.Errorf("State() on a nil tracker = %+v, want the zero state", state)
	}
	path := filepath.Join(t.TempDir(), TruncationEventsFile)
	if err := tracker.WriteJSON(path); err != nil {
		t.Errorf("WriteJSON on a nil tracker returned %v, want nil", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("nil tracker wrote %s (err=%v), want no file", path, err)
	}
}

// TestScopeLabelNamesUnattributedRecords prevents the failure where a
// record from an unlabelled call site renders as an empty string, giving
// the operator a truncation warning that names nothing at all (#574).
func TestScopeLabelNamesUnattributedRecords(t *testing.T) {
	cases := []struct {
		name  string
		scope TruncationScope
		want  string
	}{
		{"no attribution at all", TruncationScope{}, "(unattributed)"},
		{"task only", TruncationScope{Task: "getWebhookDeliveries"}, "getWebhookDeliveries"},
		{"task with project and branch",
			TruncationScope{Task: "getComponentTree", ProjectKey: "alpha", Branch: "main"},
			"getComponentTree alpha@main"},
		{"task with project and detail",
			TruncationScope{Task: "getProjectHotspots", ProjectKey: "alpha", Detail: "status=TO_REVIEW"},
			"getProjectHotspots alpha (status=TO_REVIEW)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.scope.Label(); got != tc.want {
				t.Errorf("Label(): got %q, want %q", got, tc.want)
			}
		})
	}
}
