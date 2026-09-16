// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// TruncationEventsFile is the filename written under the extract
// directory when one or more responses were truncated during the run.
// The migration report's collector reads it to decide whether the
// Limitations section needs a data-loss bullet. A run that truncated
// nothing writes no file at all, following the precedent set by
// migrate's rate_limit_events.json (#574).
const TruncationEventsFile = "extract_truncation.json"

// ResultWindowLimit is SonarQube's hard search ceiling. The cap is on
// the OFFSET, not on the reported total: p*ps beyond 10,000 is HTTP 400
// "Can return only the first 10000 results", while paging.total stays
// truthful above it. That asymmetry is what makes truncation detectable
// at all (#574).
const ResultWindowLimit = 10000

// TruncationReason names the true cause of an incomplete fetch. The
// report renders one sentence per reason, so a mechanical clamp is never
// described with the same words as an API limit that no amount of
// slicing can work around.
type TruncationReason string

const (
	// ReasonPageLimitClamp is PaginatedOpts.PageLimit stopping
	// pagination short of paging.total — the clamp at the capped call
	// sites, which exists because reaching past the offset ceiling is
	// a hard HTTP 400.
	ReasonPageLimitClamp TruncationReason = "page_limit_clamp"

	// ReasonUnknownTotal is a response with no parseable total and a
	// completely full first page: pagination stopped with no way to
	// know how much was left behind. This class has already shipped in
	// this repo once (see the comment in
	// migrate/tasks_compare_profiles.go), which is why the observer is
	// installed on the client rather than per call site.
	ReasonUnknownTotal TruncationReason = "unknown_total"

	// ReasonAtomicWindow is more than ResultWindowLimit issues sharing
	// a single creation second, so date bisection has nothing left to
	// subdivide. Measured on a real migrated project: 8,916 of 14,903
	// issues in one second, all stamped by the first analysis.
	ReasonAtomicWindow TruncationReason = "atomic_window"

	// ReasonDatesIgnored is an endpoint that answered HTTP 200 with an
	// unchanged total for both halves of a split, proving it silently
	// drops the date parameters (the /api/hotspots/search shape). The
	// fetch falls back to the clamped result; it never recurses.
	ReasonDatesIgnored TruncationReason = "dates_ignored"

	// ReasonDatesRejected is the opposite failure to
	// ReasonDatesIgnored: the server answered an HTTP error to every
	// request that carried a createdAfter / createdBefore bound while
	// answering the same query without one. SQDateLayout is verified
	// against a single server version out of the 9.9-to-2026.x range
	// this tool supports, and a WAF or proxy in front of the instance
	// can refuse the parameters just as effectively as a version whose
	// date parsing differs. The label is only ever written once the
	// UNDATED fallback has succeeded, so it is a proven statement
	// rather than a guess (#574).
	ReasonDatesRejected TruncationReason = "dates_rejected"

	// ReasonMaxDepth is the bisection depth backstop firing. It is not
	// the normal terminator — a one-second window is — so seeing this
	// reason means the window arithmetic misbehaved.
	ReasonMaxDepth TruncationReason = "max_depth"

	// ReasonIncompleteSlice is an error interrupting a window walk
	// after at least one chunk was already written. The data on disk
	// stays there; this record says the set around it is incomplete.
	ReasonIncompleteSlice TruncationReason = "incomplete_slice"

	// ReasonCountDrift is reconciliation being unable to account for
	// every item after a walk, beyond what concurrent server-side churn
	// explains. It is an accounting statement, never a claim that data
	// is missing.
	ReasonCountDrift TruncationReason = "count_drift"
)

// TruncationScope attributes a record to the work that produced it.
// Every field is optional because the client-level observer covers call
// sites that have no project context at all, but an unattributed record
// is still better than none.
type TruncationScope struct {
	Task       string `json:"task,omitempty"`
	ProjectKey string `json:"projectKey,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// Label renders the scope for a log line, the console block and the
// report bullet. An empty scope renders as "(unattributed)" rather than
// as a blank, so a record from an unlabelled call site still reads as a
// sentence.
func (s TruncationScope) Label() string {
	parts := make([]string, 0, 3)
	if s.Task != "" {
		parts = append(parts, s.Task)
	}
	switch {
	case s.ProjectKey != "" && s.Branch != "":
		parts = append(parts, s.ProjectKey+"@"+s.Branch)
	case s.ProjectKey != "":
		parts = append(parts, s.ProjectKey)
	case s.Branch != "":
		parts = append(parts, "@"+s.Branch)
	}
	if s.Detail != "" {
		parts = append(parts, "("+s.Detail+")")
	}
	if len(parts) == 0 {
		return "(unattributed)"
	}
	return strings.Join(parts, " ")
}

// TruncationRecord is one observed truncation. Total/TotalKnown are kept
// separate so a record can say "the server did not tell us how many
// there were" instead of claiming a total of zero.
//
// WindowStart / WindowEnd carry the SQDateLayout bounds of the date
// window the truncation happened in, empty for fetches that carry no
// date parameters. They are part of the record's identity, so two
// different atomic seconds in the same project never collapse into one.
type TruncationRecord struct {
	Endpoint    string           `json:"endpoint"`
	Reason      TruncationReason `json:"reason"`
	Scope       TruncationScope  `json:"scope"`
	Total       int              `json:"total"`
	TotalKnown  bool             `json:"totalKnown"`
	Fetched     int              `json:"fetched"`
	Lost        int              `json:"lost"`
	PageSize    int              `json:"pageSize,omitempty"`
	PageLimit   int              `json:"pageLimit,omitempty"`
	WindowStart string           `json:"windowStart,omitempty"`
	WindowEnd   string           `json:"windowEnd,omitempty"`
	ObservedAt  time.Time        `json:"observedAt"`
}

// identity is the merge key: the same endpoint, cause, scope and date
// window observed twice (a retry, or a resumed extract re-running the
// task) is one truncation, not two.
func (r TruncationRecord) identity() string {
	return strings.Join([]string{
		r.Endpoint,
		string(r.Reason),
		r.Scope.Task,
		r.Scope.ProjectKey,
		r.Scope.Branch,
		r.Scope.Detail,
		r.WindowStart,
		r.WindowEnd,
	}, "\x00")
}

// Reconciliation is the post-walk audit of a sliced fetch. Every raw
// number is persisted rather than only the verdict, so a reader can
// re-derive the arithmetic and disagree with it.
//
// Keyless is counted, never dropped: items the response gave us without
// an identifier are real data, and hiding them to protect a bookkeeping
// invariant is how a slicer ends up claiming completeness it does not
// have.
type Reconciliation struct {
	Scope TruncationScope `json:"scope"`
	// TotalBefore is the unfiltered total probed before the walk,
	// TotalAfter the same probe repeated after it.
	TotalBefore int `json:"totalBefore"`
	TotalAfter  int `json:"totalAfter"`
	Unique      int `json:"unique"`
	Duplicates  int `json:"duplicates"`
	Keyless     int `json:"keyless"`
	// ExpectedLoss is the SUM of the Lost values of the records this
	// walk produced — a count, not a flag. Summing it is what lets a
	// dropped seam show up as drift on a project that also has a
	// genuinely atomic window.
	ExpectedLoss int `json:"expectedLoss"`
	Windows      int `json:"windows"`
	Requests     int `json:"requests"`
}

// Churn is the movement in the source's own count while the walk ran.
// Positive means issues were created, negative means they were closed or
// purged.
func (r Reconciliation) Churn() int {
	return r.TotalAfter - r.TotalBefore
}

// Drift is what the walk failed to account for: the total it started
// from, minus the unique items it collected, minus the loss it already
// recorded and explained.
func (r Reconciliation) Drift() int {
	return r.TotalBefore - (r.Unique + r.ExpectedLoss)
}

// UnexplainedDrift is the part of Drift that churn cannot explain, and
// the only thing that should ever produce a ReasonCountDrift record.
//
// Churn of -7 (seven issues disappeared mid-walk) explains a drift of up
// to +7, and churn of +3 explains a drift down to -3, so the honest
// tolerance band is [min(0, -Churn), max(0, -Churn)]. On a quiet server
// churn is 0, the band collapses to a point, and the check is exact.
// Without the band, one issue closed during a multi-hour extract would
// be reported as a data-loss defect.
func (r Reconciliation) UnexplainedDrift() int {
	drift := r.Drift()
	low, high := 0, -r.Churn()
	if low > high {
		low, high = high, low
	}
	switch {
	case drift > high:
		return drift - high
	case drift < low:
		return drift - low
	default:
		return 0
	}
}

// TruncationState is the JSON shape persisted to TruncationEventsFile.
// TotalLost is always recomputed from Records, never accumulated, so a
// merge cannot inflate it.
type TruncationState struct {
	Records         []TruncationRecord `json:"records"`
	Reconciliations []Reconciliation   `json:"reconciliations,omitempty"`
	TotalLost       int                `json:"totalLost"`
}

// TruncationTracker accumulates truncation observations for one extract
// run and persists them as a single merged JSON artefact.
//
// It is safe for concurrent use — Record is handed to the raw client as
// an observer and fires from arbitrary task goroutines — and every
// method is nil-safe, so a hand-built Executor with no tracker attached
// behaves exactly as it did before #574 rather than panicking.
type TruncationTracker struct {
	mu              sync.Mutex
	records         []TruncationRecord
	reconciliations []Reconciliation
}

// NewTruncationTracker returns an empty tracker ready to receive records.
func NewTruncationTracker() *TruncationTracker {
	return &TruncationTracker{}
}

// Record stores one truncation observation. Its signature is the raw
// client's observer type, so installation is
// raw.SetTruncationObserver(tracker.Record).
func (t *TruncationTracker) Record(rec TruncationRecord) {
	if t == nil {
		return
	}
	if rec.ObservedAt.IsZero() {
		rec.ObservedAt = time.Now().UTC()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = append(t.records, rec)
}

// RecordReconciliation stores the audit of one sliced fetch. It is kept
// separate from Record because a reconciliation is evidence, not a
// finding: it is persisted alongside the records but never on its own.
func (t *TruncationTracker) RecordReconciliation(rec Reconciliation) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reconciliations = append(t.reconciliations, rec)
}

// HasRecords reports whether anything was truncated during the run.
func (t *TruncationTracker) HasRecords() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.records) > 0
}

// State returns a snapshot of the accumulated state with TotalLost
// computed. The returned slices are copies, so the caller can print or
// sort them while tasks are still running.
func (t *TruncationTracker) State() TruncationState {
	if t == nil {
		return TruncationState{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked()
}

// snapshotLocked collapses the accumulated records through the same
// identity merge the file uses, so the console block, the log and the
// artefact all report one number for one truncation.
func (t *TruncationTracker) snapshotLocked() TruncationState {
	return MergeTruncationStates(TruncationState{}, TruncationState{
		Records:         t.records,
		Reconciliations: t.reconciliations,
	})
}

// WriteJSON persists the tracker's state to path, merging with whatever
// is already there.
//
// The read-merge-write is not an optimisation. An extract resumed with
// --extract_id reuses the same directory, and the tasks that already
// completed are skipped rather than re-run, so their truncation records
// exist only in the file. A plain write would delete the evidence that
// the first attempt lost data and leave the report claiming a clean run.
//
// Writing is skipped entirely when no record was accumulated: a clean
// run leaves no artefact, and a reconciliation on its own is not a
// finding worth a file.
func (t *TruncationTracker) WriteJSON(path string) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.records) == 0 {
		return nil
	}

	base, err := ReadTruncationState(path)
	if err != nil {
		// The new records matter more than the unreadable old ones, so
		// this degrades to an overwrite — loudly, naming the file, so
		// the loss of the earlier evidence is on the record.
		slog.Default().Warn("existing truncation artefact could not be read — it will be overwritten",
			"path", path, "error", err)
		base = TruncationState{}
	}

	merged := MergeTruncationStates(base, t.snapshotLocked())
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling truncation state: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// ReadTruncationState loads the artefact at path. A missing file is not
// an error — it is the normal shape of a run that truncated nothing —
// and yields the zero state, whose empty Records is the same signal. Any
// other read failure, and any malformed content, is returned as an error
// rather than reported as "nothing was truncated".
func ReadTruncationState(path string) (TruncationState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return TruncationState{}, nil
		}
		return TruncationState{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var state TruncationState
	if err := json.Unmarshal(data, &state); err != nil {
		return TruncationState{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	state.TotalLost = sumLost(state.Records)
	return state, nil
}

// MergeTruncationStates combines two states into one. Records sharing an
// identity are collapsed, keeping the larger Lost: a retried or resumed
// fetch that got further is the better observation, and a fetch that got
// less far must not be allowed to talk the reported loss down.
// Reconciliations are keyed by scope, with incoming replacing base
// because it audits the more recent walk. TotalLost is recomputed from
// the merged records, never added up across states.
//
// Base ordering is preserved and new entries append, so repeated merges
// produce a stable file.
func MergeTruncationStates(base, incoming TruncationState) TruncationState {
	merged := TruncationState{
		Records:         make([]TruncationRecord, 0, len(base.Records)+len(incoming.Records)),
		Reconciliations: make([]Reconciliation, 0, len(base.Reconciliations)+len(incoming.Reconciliations)),
	}

	index := make(map[string]int, len(base.Records)+len(incoming.Records))
	for _, rec := range append(append([]TruncationRecord(nil), base.Records...), incoming.Records...) {
		id := rec.identity()
		at, seen := index[id]
		if !seen {
			index[id] = len(merged.Records)
			merged.Records = append(merged.Records, rec)
			continue
		}
		if rec.Lost > merged.Records[at].Lost {
			merged.Records[at] = rec
		}
	}

	recIndex := make(map[string]int, len(base.Reconciliations)+len(incoming.Reconciliations))
	for _, rec := range append(append([]Reconciliation(nil), base.Reconciliations...), incoming.Reconciliations...) {
		id := rec.Scope.Label()
		at, seen := recIndex[id]
		if !seen {
			recIndex[id] = len(merged.Reconciliations)
			merged.Reconciliations = append(merged.Reconciliations, rec)
			continue
		}
		merged.Reconciliations[at] = rec
	}

	merged.TotalLost = sumLost(merged.Records)
	return merged
}

// sumLost is the single derivation of TotalLost.
func sumLost(records []TruncationRecord) int {
	total := 0
	for _, rec := range records {
		total += rec.Lost
	}
	return total
}
