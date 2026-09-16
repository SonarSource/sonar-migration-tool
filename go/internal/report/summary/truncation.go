// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// collectTruncationLimitations reads the extract-side truncation
// artefact of every extract in the mapping and returns one Limitations
// bullet per (task, cause, window-presence) triple (#574).
//
// The artefact is a plain JSON file inside the extract directory, read
// exactly the way collectRateLimitReport reads migrate's
// rate_limit_events.json. A missing file is the normal shape of a run
// that truncated nothing, so it yields no bullets and no error.
//
// Grouping is by (task, reason, windowed) rather than by task alone,
// and each combination gets its own sentence. That split is the whole
// point: structurally different records can share the task name
// "getProjectIssuesFull" — a leaf window that genuinely could not be
// fetched, a date window that grew past the ceiling mid-run, and a
// reconciliation that simply failed to add up — and summing them under
// one "the 10,000-result ceiling" sentence would make a causal claim
// the evidence does not support, inflating the reported data loss with
// a bookkeeping discrepancy and attaching the wrong remediation to
// half of it.
func collectTruncationLimitations(exportDir string, mapping structure.ExtractMapping) []string {
	if mapping == nil {
		return nil
	}

	groups := map[truncationGroupKey]*truncationGroup{}
	var unreadable []string
	for _, extractID := range mapping {
		path := filepath.Join(exportDir, extractID, common.TruncationEventsFile)
		state, err := common.ReadTruncationState(path)
		if err != nil {
			// A truncation artefact that cannot be parsed must never
			// read as "nothing was truncated" — that is exactly the
			// silent-loss shape this feature exists to end. Say so in
			// the log and in the report.
			slog.Warn("truncation artefact could not be read for the migration report",
				"path", path, "error", err)
			unreadable = append(unreadable, fmt.Sprintf(
				"The extract's truncation record (%s) could not be read (%v), so this report cannot confirm whether every source item reached the extract.",
				filepath.Join(extractID, common.TruncationEventsFile), err))
			continue
		}
		absorbTruncationState(groups, state)
	}

	keys := make([]truncationGroupKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	// Mapping iteration is Go map order, so the bullet order has to be
	// derived rather than inherited.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].task != keys[j].task {
			return keys[i].task < keys[j].task
		}
		if keys[i].reason != keys[j].reason {
			return keys[i].reason < keys[j].reason
		}
		// Windowed last, so the "could not be narrowed" statement about
		// the whole query precedes the narrower mid-run one.
		return !keys[i].windowed && keys[j].windowed
	})

	out := make([]string, 0, len(keys)+len(unreadable))
	for _, key := range keys {
		group := groups[key]
		sort.Strings(group.scopes)
		bullet := truncationLead(key, group)
		if len(group.scopes) > 0 {
			bullet += " Affected: " + formatTruncationScopeList(group.scopes) + "."
		}
		out = append(out, bullet)
	}
	sort.Strings(unreadable)
	return append(out, unreadable...)
}

// truncationGroupKey is the (task, cause, window-presence) triple one
// bullet is rendered for. The reason is part of the key so a task with
// two different causes produces two sentences instead of one summed
// claim, and windowed is part of it because page_limit_clamp means two
// different things with two different remediations depending on
// whether the record carries date bounds.
type truncationGroupKey struct {
	task     string
	reason   common.TruncationReason
	windowed bool
}

// truncationGroup accumulates the residual and the affected scopes of
// one key. seen dedupes the scope list, which the same project can enter
// twice when two extracts of the same server both recorded it.
//
// lost and surplus are kept apart because they are opposite findings:
// a shortfall is data the report cannot account for, a surplus is data
// the extract holds and the source's own count did not predict.
// Netting them off would hide both.
type truncationGroup struct {
	lost    int
	surplus int
	scopes  []string
	seen    map[string]bool
}

// truncationResidual splits a record's unaccounted count into a
// shortfall and a surplus.
//
// Only count_drift can be a surplus, and only the reconciliation knows
// it: the slicer clamps a negative unexplained drift to zero before
// writing the record, so Lost == 0 there means either "the residual is
// unknown" or "the walk collected MORE than the source reported".
// Rendering the second as "an unknown number of issues unaccounted
// for" reports a surplus as missing data, which is the opposite of
// what happened (#574).
func truncationResidual(rec common.TruncationRecord, recons map[common.TruncationScope]common.Reconciliation) (lost, surplus int) {
	if rec.Reason != common.ReasonCountDrift {
		return rec.Lost, 0
	}
	recon, ok := recons[rec.Scope]
	if !ok {
		// No reconciliation to consult — an artefact from an older
		// run, or a hand-built one. The record's own clamped count is
		// all there is, and "unknown" is the honest rendering of it.
		return rec.Lost, 0
	}
	if drift := recon.UnexplainedDrift(); drift < 0 {
		return 0, -drift
	}
	return rec.Lost, 0
}

// formatTruncationScopeList renders the affected scopes, capped the way
// formatLimitationUserList caps logins (#475) — but pointing at the
// artefact, because the names past the cap exist somewhere and a bullet
// that drops them silently leaves the operator with no way to find out
// which projects they were.
func formatTruncationScopeList(scopes []string) string {
	if len(scopes) <= maxListedLimitationUsers {
		return strings.Join(scopes, ", ")
	}
	return fmt.Sprintf("first %d: %s (and %d more; see %s in the extract directory)",
		maxListedLimitationUsers,
		strings.Join(scopes[:maxListedLimitationUsers], ", "),
		len(scopes)-maxListedLimitationUsers,
		common.TruncationEventsFile)
}

// truncationScopeEntry renders the part of a record's scope that the
// bullet's shared lead does not already say. The task name is the
// grouping key, so repeating it on every entry would only pad the list;
// a record carrying no project, branch, detail or date window at all
// contributes nothing rather than a row of "(unattributed)" filler.
func truncationScopeEntry(rec common.TruncationRecord) string {
	var parts []string
	if rec.Scope.ProjectKey != "" || rec.Scope.Branch != "" || rec.Scope.Detail != "" {
		scope := rec.Scope
		scope.Task = ""
		parts = append(parts, scope.Label())
	}
	switch {
	case rec.WindowStart != "" && rec.WindowEnd != "":
		parts = append(parts, rec.WindowStart+" to "+rec.WindowEnd)
	case rec.WindowStart != "":
		parts = append(parts, "from "+rec.WindowStart)
	case rec.WindowEnd != "":
		parts = append(parts, "before "+rec.WindowEnd)
	}
	return strings.Join(parts, " ")
}

// truncationTaskLabel names the extract task in a sentence. Records from
// call sites with no scope attached still get a readable bullet rather
// than a sentence with a hole in it.
func truncationTaskLabel(task string) string {
	if task == "" {
		return "(unattributed)"
	}
	return task
}

// truncationLostPhrase renders a lost count. A reason can be recorded
// without a trustworthy count — the server never said how many results
// existed — and in that case the bullet says so instead of printing a
// zero that reads as "nothing was lost".
func truncationLostPhrase(lost int, noun string) string {
	if lost <= 0 {
		return "an unknown number of " + noun + "s"
	}
	return truncationCountPhrase(lost, noun)
}

// truncationCountPhrase renders a count that is known to be known.
// Thousands separators match the way the rest of the report writes
// large numbers: "7,919 issue(s)", not "7919 issue(s)".
func truncationCountPhrase(n int, noun string) string {
	return fmt.Sprintf("%s %s(s)", common.FormatCount(n), noun)
}

// truncationLead renders the reason-specific sentence of a bullet.
//
// The sentences are deliberately not interchangeable. Only the reasons
// that actually establish missing data are allowed to say data is
// absent:
//
//   - page_limit_clamp, atomic_window, dates_ignored and max_depth are
//     fetch outcomes: the API refused to return the rest, so the items
//     really are not in the export. page_limit_clamp splits again on
//     window-presence — a clamp on a date window the slicer had already
//     probed is a race against issues created during the run, and a
//     re-run recovers it, which is the opposite advice from a query
//     that could never be narrowed at all.
//   - count_drift is an accounting statement about the extract's own
//     bookkeeping. It must never assert absence and must never blame
//     the 10,000-result ceiling, because it is raised precisely when
//     the arithmetic did not identify a cause. Its residual has a sign:
//     a surplus is reported as a surplus.
//   - incomplete_slice says what is on disk and that the set around it
//     is incomplete, without guessing why.
//   - unknown_total has no count at all to report.
//
// Every sentence here is also phrased to survive toPredictiveTense
// untouched. That rewriter turns "were not " into "will not be ", so
// the earlier "N result(s) were not extracted" rendered in the
// predictive report as a forecast of a loss that had already happened.
// Truncation is always a settled fact about an extract that has
// already run, in both report modes, so these leads avoid "was",
// "were", "has been" and "have been" entirely, and
// TestTruncationLeadsSurvivePredictiveTense holds that line.
// absorbTruncationState folds one artefact's records into the grouped
// tally.
//
// Reconciliations are indexed per artefact, not across artefacts: two
// extracts of two different servers can hold the same scope, and a
// drift residual only means anything against the walk that produced it.
func absorbTruncationState(groups map[truncationGroupKey]*truncationGroup, state common.TruncationState) {
	recons := make(map[common.TruncationScope]common.Reconciliation, len(state.Reconciliations))
	for _, recon := range state.Reconciliations {
		recons[recon.Scope] = recon
	}
	for _, rec := range state.Records {
		absorbTruncationRecord(groups, rec, recons)
	}
}

// absorbTruncationRecord adds one record to its (task, reason, windowed)
// group, deduplicating the scope list so a project that truncated on
// several branches is named once per branch and never twice.
func absorbTruncationRecord(
	groups map[truncationGroupKey]*truncationGroup,
	rec common.TruncationRecord,
	recons map[common.TruncationScope]common.Reconciliation,
) {
	key := truncationGroupKey{
		task:     rec.Scope.Task,
		reason:   rec.Reason,
		windowed: rec.WindowStart != "" || rec.WindowEnd != "",
	}
	group := groups[key]
	if group == nil {
		group = &truncationGroup{seen: map[string]bool{}}
		groups[key] = group
	}
	lost, surplus := truncationResidual(rec, recons)
	group.lost += lost
	group.surplus += surplus
	if entry := truncationScopeEntry(rec); entry != "" && !group.seen[entry] {
		group.seen[entry] = true
		group.scopes = append(group.scopes, entry)
	}
}

func truncationLead(key truncationGroupKey, group *truncationGroup) string {
	name := truncationTaskLabel(key.task)
	switch key.reason {
	case common.ReasonPageLimitClamp:
		if key.windowed {
			return fmt.Sprintf(
				"The %s extract hit SonarQube's 10,000-result search ceiling inside a creation-date window that still fitted when the slicer probed it: issues created during the extraction pushed that window past the ceiling before it could be fetched. %s are missing from the extract, and re-running the extract recovers them.",
				name, truncationLostPhrase(group.lost, "result"))
		}
		return fmt.Sprintf(
			"The %s extract hit SonarQube's 10,000-result search ceiling, and the query could not be narrowed into smaller date windows to retrieve the rest: %s are missing from the extract and cannot be migrated.",
			name, truncationLostPhrase(group.lost, "result"))
	case common.ReasonAtomicWindow:
		return fmt.Sprintf(
			"The %s extract hit SonarQube's 10,000-result search ceiling inside a single second: more than 10,000 issues share one creation timestamp, and date slicing cannot subdivide a one-second window any further. %s are missing from the extract and cannot be migrated.",
			name, truncationLostPhrase(group.lost, "issue"))
	case common.ReasonDatesIgnored:
		return fmt.Sprintf(
			"The %s extract could not be split into smaller date windows: the source API returned the same result count with and without creation-date filters, so it ignores them. The fetch fell back to the first 10,000 results, and %s are missing from the extract.",
			name, truncationLostPhrase(group.lost, "result"))
	case common.ReasonDatesRejected:
		// The record is only ever written after the undated fallback
		// succeeded, so "the same query without one succeeded" is a
		// claim this code can vouch for.
		return fmt.Sprintf(
			"The %s extract could not be split into smaller date windows: the source rejected every request carrying a creation-date filter, though the same query without one succeeded. The fetch fell back to the first 10,000 results, and %s are missing from the extract.",
			name, truncationLostPhrase(group.lost, "result"))
	case common.ReasonMaxDepth:
		return fmt.Sprintf(
			"Date slicing for the %s extract reached its depth backstop before the window became small enough to fetch completely: %s are missing from the extract and cannot be migrated.",
			name, truncationLostPhrase(group.lost, "issue"))
	case common.ReasonIncompleteSlice:
		return fmt.Sprintf(
			"The %s extract stopped part-way with an error; the issues already written are on disk but the set is incomplete (%s unaccounted for).",
			name, truncationLostPhrase(group.lost, "issue"))
	case common.ReasonCountDrift:
		return truncationCountDriftLead(name, group)
	case common.ReasonUnknownTotal:
		return fmt.Sprintf(
			"The %s extract received a full page of results with no total count, so pagination stopped with no way to know how much it left behind. An unknown number of items may be absent from the migration.",
			name)
	default:
		// An unrecognised reason is still evidence. Naming it verbatim
		// beats dropping the record, and beats describing it with a
		// cause this code cannot vouch for.
		return fmt.Sprintf(
			"The %s extract recorded a truncation (%s): %s are missing from the extract.",
			name, key.reason, truncationLostPhrase(group.lost, "item"))
	}
}

// truncationCountDriftLead renders the accounting sentence, which is
// the one bullet whose residual can point either way.
//
// A shortfall and a surplus are reported separately and never netted:
// one group can hold both when two projects drifted in opposite
// directions, and "3 unaccounted for" next to "4 more than expected"
// tells the operator something that a single "1" would not.
func truncationCountDriftLead(name string, group *truncationGroup) string {
	const tail = " This is a discrepancy in the extract's own bookkeeping, not a confirmed loss of data."
	switch {
	case group.lost > 0 && group.surplus > 0:
		return fmt.Sprintf(
			"For the %s extract, the source issue count changed during extraction and could not be reconciled (%s unaccounted for, plus a separate surplus of %s that the source's own count did not predict).%s",
			name, truncationCountPhrase(group.lost, "issue"),
			truncationCountPhrase(group.surplus, "issue"), tail)
	case group.surplus > 0:
		return fmt.Sprintf(
			"For the %s extract, the source issue count changed during extraction and could not be reconciled: the walk collected %s more than the source reported, a surplus rather than a shortfall, so nothing is missing on this account.%s",
			name, truncationCountPhrase(group.surplus, "issue"), tail)
	default:
		return fmt.Sprintf(
			"For the %s extract, the source issue count changed during extraction and could not be reconciled (%s unaccounted for).%s",
			name, truncationLostPhrase(group.lost, "issue"), tail)
	}
}
