// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	sqapi "github.com/sonar-solutions/sq-api-go"
	sqtypes "github.com/sonar-solutions/sq-api-go/types"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// issueMetadataSyncTasks returns the task definition for synchronising
// issue metadata (status transitions, comments, tags) from the extracted
// SonarQube Server data to the newly-created SonarQube Cloud issues.
//
// The task depends on importProjectData because Cloud issues only exist
// after the scan report has been processed by the CE.
func issueMetadataSyncTasks() []TaskDef {
	return []TaskDef{{
		Name:         "syncIssueMetadata",
		Editions:     common.AllEditions,
		Dependencies: []string{"importProjectData"},
		Run:          runSyncIssueMetadata,
	}}
}

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// matchableIssue is a normalised issue representation used to pair source
// (SQS extract) issues with their Cloud counterparts. The struct is
// intentionally flat so that matching and filtering logic stays simple.
//
// Tags holds ONLY the user-added subset (issue.tags minus the rule's
// default tags+sysTags). The raw /api/issues/search response folds
// rule defaults into every issue's tags array, so the unsubtracted
// list is useless as a "manual triage" signal — see ruleTagDefaults
// (#352 follow-up).
//
// ManualSeverity mirrors the `manualSeverity` boolean on the SQ Server
// API response: true means a user has explicitly overridden the issue's
// severity vs the rule default.
//
// Branch is populated for source-side issues (from the
// getProjectIssuesFull extract's branch enrichment) and left empty for
// Cloud-side candidates. Used at sync time to log a per-project
// branch count so operators can see the project's shape.
//
// IssueStatus mirrors the modern unified `issueStatus` enum
// (SonarQube 10.4+ / MQR model: OPEN, CONFIRMED, FALSE_POSITIVE,
// ACCEPTED, FIXED). It is the authoritative triage signal and the only
// field that distinguishes an ACCEPTED issue from a legacy Won't Fix —
// an accepted issue reports status=RESOLVED, resolution=WONTFIX,
// issueStatus=ACCEPTED. Populated for source-side issues; left empty
// for Cloud candidates (the search response is consulted via
// Transitions instead). See #322.
//
// Transitions is the Cloud issue's available-transition list (from
// /api/issues/search?additionalFields=transitions). Populated only for
// Cloud-side candidates; used to verify the "accept" transition is
// offered before applying it (#322). Empty for source-side issues.
type matchableIssue struct {
	Key            string
	Rule           string
	Component      string
	Line           int
	Status         string
	Resolution     string
	IssueStatus    string
	Tags           []string
	Comments       []issueComment
	Assignee       string
	ManualSeverity bool
	Branch         string
	Transitions    []string
}

// issueComment is a normalised comment attached to a matchableIssue.
type issueComment struct {
	Login     string
	HTMLText  string
	Markdown  string
	CreatedAt string
}

// issuePair binds a source SQS issue to its Cloud counterpart after matching.
type issuePair struct {
	source matchableIssue
	cloud  matchableIssue
}

// ---------------------------------------------------------------------------
// Matching helpers
// ---------------------------------------------------------------------------

// stripProjectKeyPrefix removes the leading "projectKey:" segment from a
// SonarQube component path, returning the bare file path. SonarQube
// formats components as "projectKey:src/main/java/Foo.java"; the
// project key is environment-specific but the file path part is the
// same on source and cloud.
func stripProjectKeyPrefix(component string) string {
	if idx := strings.Index(component, ":"); idx >= 0 {
		return component[idx+1:]
	}
	return component
}

// ---------------------------------------------------------------------------
// Transition logic
// ---------------------------------------------------------------------------

// acceptTransition is the MQR-model transition that moves a Cloud issue
// into the ACCEPTED state. It supersedes the deprecated "wontfix"
// transition: applying "wontfix" lands the issue as Won't Fix, which the
// modern issue lifecycle surfaces differently from Accepted. SonarCloud
// is always the migration target and exposes "accept" (per SPEC-008), so
// accepted / won't-fix source issues map here. See issue #322.
const acceptTransition = "accept"

// getFallbackTransition maps an SQS issue to the Cloud transition name
// required to move the Cloud issue into the equivalent state.
//
// Precedence — most authoritative first:
//  1. issueStatus: the modern unified enum (SonarQube 10.4+ / MQR
//     model). It is the ONLY field that distinguishes ACCEPTED from a
//     legacy Won't Fix — an accepted issue reports status=RESOLVED,
//     resolution=WONTFIX, issueStatus=ACCEPTED. The previous code
//     checked resolution first and so mapped every ACCEPTED issue to
//     "wontfix", landing it as Won't Fix on Cloud (issue #322). Reading
//     issueStatus first fixes that.
//  2. resolution: legacy signal for pre-10.4 servers that emit no
//     issueStatus.
//  3. status: final legacy fallback.
//
// Accepted / won't-fix issues resolve to acceptTransition; availability
// of that transition on the specific Cloud issue is verified separately
// by resolveTransition, which leaves the issue OPEN (rather than
// mislabeling it) when "accept" is not offered.
//
// Returns "" when no transition is needed (e.g. OPEN issues).
func getFallbackTransition(issueStatus, resolution, status string) string {
	// 1. Modern unified issueStatus enum — most authoritative.
	switch strings.ToUpper(issueStatus) {
	case "ACCEPTED":
		return acceptTransition
	case "FALSE_POSITIVE":
		return "falsepositive"
	case "CONFIRMED":
		return "confirm"
	case "OPEN", "FIXED":
		// OPEN needs no transition; FIXED issues are excluded at load
		// time (no Cloud counterpart) and should not reach here.
		return ""
	}

	// 2. Legacy resolution-based priority (pre-10.4 servers that emit no
	// issueStatus). A genuine legacy WONTFIX keeps mapping to the
	// deprecated-but-still-accepted "wontfix" transition, per SPEC-008,
	// to avoid regressing pre-10.4 migrations. Only the MODERN
	// issueStatus=ACCEPTED case (handled above) routes to "accept";
	// modern accepted issues never reach here because issueStatus is
	// checked first.
	switch strings.ToUpper(resolution) {
	case "FALSE-POSITIVE":
		return "falsepositive"
	case "WONTFIX":
		return "wontfix"
	}

	// 3. Legacy status-based fallback.
	switch strings.ToUpper(status) {
	case "CONFIRMED":
		return "confirm"
	case "REOPENED":
		return "reopen"
	case "OPEN":
		return ""
	case "RESOLVED", "CLOSED":
		return "resolve"
	case "ACCEPTED":
		return acceptTransition
	case "FALSE_POSITIVE":
		return "falsepositive"
	case "IN_SANDBOX":
		return ""
	default:
		return ""
	}
}

// resolveTransition decides the Cloud transition to apply for a source
// issue, gating the MQR "accept" transition on its availability on the
// matched Cloud issue.
//
// cloudTransitions is the matched Cloud issue's available-transition
// list (from /api/issues/search?additionalFields=transitions). A
// non-empty list that does NOT contain "accept" means the Cloud issue
// cannot be accepted from its current state. Rather than downgrade to
// "wontfix" — which would mislabel the issue as Won't Fix — we leave it
// OPEN (return "") and report downgraded=true so the caller can log the
// fidelity loss (issue #322). An empty/unknown list is treated
// optimistically: we still attempt "accept" and let do_transition +
// isExpectedTransitionError absorb a rejection.
func resolveTransition(src matchableIssue, cloudTransitions []string) (transition string, downgraded bool) {
	desired := getFallbackTransition(src.IssueStatus, src.Resolution, src.Status)
	if desired == acceptTransition && len(cloudTransitions) > 0 && !slices.Contains(cloudTransitions, acceptTransition) {
		return "", true
	}
	return desired, false
}

// ---------------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------------

// metadataSyncTag is the idempotency marker applied to Cloud issues after
// their metadata has been synchronised. Its presence prevents redundant
// re-application on subsequent runs.
const metadataSyncTag = "metadata-synchronized"

// hasManualChanges returns true when the source issue carries metadata
// that should be propagated to Cloud — i.e. the issue was manually
// triaged on the source server. Issues that have never been touched
// are skipped to avoid unnecessary API calls.
//
// Triggers (per #350):
//   - Triage state: status ACCEPTED / FALSE_POSITIVE (modern unified
//     issueStatus enum, post-10.4), or legacy resolution FALSE-POSITIVE
//     / WONTFIX. CONFIRMED is intentionally excluded per the issue spec.
//   - manualSeverity == true on the API response — user has overridden
//     the rule's default severity.
//   - User-added tags (rule defaults already subtracted at load time).
//   - Any comment on the issue.
//
// Assignee was previously a trigger but was dropped per the issue spec:
// auto-assigned issues (e.g. via "default assignee") are common and
// inflate the actionable set without carrying real triage signal.
func hasManualChanges(iss matchableIssue) bool {
	status := strings.ToUpper(iss.Status)
	resolution := strings.ToUpper(iss.Resolution)
	issueStatus := strings.ToUpper(iss.IssueStatus)
	// Modern unified issueStatus enum (10.4+) is the authoritative
	// triage signal: an accepted issue can report status=RESOLVED (or
	// OPEN) with the triage state living only in issueStatus, so check
	// it directly rather than relying on the legacy fields (#322).
	if issueStatus == "ACCEPTED" || issueStatus == "FALSE_POSITIVE" {
		return true
	}
	if status == "ACCEPTED" || status == "FALSE_POSITIVE" {
		return true
	}
	if resolution == "FALSE-POSITIVE" || resolution == "WONTFIX" {
		return true
	}
	if iss.ManualSeverity {
		return true
	}
	if len(iss.Tags) > 0 {
		return true
	}
	if len(iss.Comments) > 0 {
		return true
	}
	return false
}

// actionableReasonBreakdown counts each issue under EVERY signal it
// trips (an issue with both ACCEPTED status and a user tag is counted
// once under acceptedOrFP and once under customTags). The numbers
// therefore sum to ≥ len(actionable). Surfaced at INFO at the start
// of the per-project sync so operators can see what's driving the
// migration cost.
type actionableReasonBreakdown struct {
	acceptedOrFP   int
	customTags     int
	manualSeverity int
	comments       int
}

func classifyActionableReasons(actionable []matchableIssue) actionableReasonBreakdown {
	var b actionableReasonBreakdown
	for _, iss := range actionable {
		status := strings.ToUpper(iss.Status)
		resolution := strings.ToUpper(iss.Resolution)
		issueStatus := strings.ToUpper(iss.IssueStatus)
		if issueStatus == "ACCEPTED" || issueStatus == "FALSE_POSITIVE" ||
			status == "ACCEPTED" || status == "FALSE_POSITIVE" ||
			resolution == "FALSE-POSITIVE" || resolution == "WONTFIX" {
			b.acceptedOrFP++
		}
		if len(iss.Tags) > 0 {
			b.customTags++
		}
		if iss.ManualSeverity {
			b.manualSeverity++
		}
		if len(iss.Comments) > 0 {
			b.comments++
		}
	}
	return b
}

// countDistinctBranches returns the number of unique branch names in
// the source issues for a project. Reported at sync start so operators
// can see project shape (single-branch vs. fan-out across feature
// branches) when reading the log.
func countDistinctBranches(issues []matchableIssue) int {
	seen := make(map[string]struct{})
	for _, iss := range issues {
		if iss.Branch != "" {
			seen[iss.Branch] = struct{}{}
		}
	}
	return len(seen)
}

// ---------------------------------------------------------------------------
// Backoff / Cloud indexing wait
// ---------------------------------------------------------------------------

// waitForCloudIndexing polls fetchFn with exponential backoff until a
// non-zero total is returned or the maximum number of retries is exhausted.
//
// This accommodates the delay between CE task completion and the issues
// becoming searchable via /api/issues/search. If the max retries are
// exhausted without results the function returns nil (non-fatal) so the
// sync proceeds with zero matches — the alternative would be a hard
// failure that blocks later projects unnecessarily.
func waitForCloudIndexing(ctx context.Context, fetchFn func() (int, error)) error {
	const (
		initialDelay = 10 * time.Second
		maxDelay     = 60 * time.Second
		maxRetries   = 10
	)

	delay := initialDelay
	for attempt := 0; attempt < maxRetries; attempt++ {
		total, err := fetchFn()
		if err != nil {
			return err
		}
		if total > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		// Exponential backoff capped at maxDelay.
		delay = delay * 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
	// Non-fatal: proceed with 0 matches.
	return nil
}

// ---------------------------------------------------------------------------
// Expected error classification
// ---------------------------------------------------------------------------

// isExpectedTransitionError returns true for API errors that are harmless
// and expected during transition replay. Cloud returns 400 when a transition
// is invalid for the current issue state — this is normal because the Cloud
// issue may already be in the target state from a previous run or because the
// available transitions differ between SQ and SC.
func isExpectedTransitionError(err error) bool {
	var apiErr *sqapi.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == 400
}

// ---------------------------------------------------------------------------
// Main task entry point
// ---------------------------------------------------------------------------

// runSyncIssueMetadata iterates every migrated project and synchronises
// the issue metadata (transitions, comments, tags) from the SQS extract
// to the corresponding Cloud issues.
//
// Rule-default tags are indexed ONCE up front and shared across all
// projects — the index is read-only after construction so it's safe
// to fan out concurrently.
func runSyncIssueMetadata(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	ruleDefaults := loadRuleTagDefaults(e)
	err := forEachMigrateItem(ctx, e, "syncIssueMetadata", "createProjects",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			cloudKey := extractField(item, "cloud_project_key")
			orgKey := extractField(item, "sonarcloud_org_key")
			serverURL := extractField(item, "server_url")
			serverKey := extractField(item, "key")
			if cloudKey == "" || orgKey == "" {
				return nil
			}
			stats := syncProjectIssues(ctx, e, cloudKey, orgKey, serverURL, serverKey, counter, ruleDefaults)
			record, _ := json.Marshal(map[string]any{
				"cloud_project_key": cloudKey,
				"synced":            stats.A,
				"line_mismatch":     stats.B,
				"not_found":         stats.C,
				"actionable":        stats.Actionable,
			})
			return w.WriteOne(record)
		})
	return err
}

// projectSyncStats is the per-project breakdown reported back from
// syncProjectIssues / syncProjectHotspots for #356. A counts case a
// (1 cloud counterpart on the same line — synced); B counts case b
// (>1 counterparts on the same line — ambiguous, skipped); C counts
// case c (no counterpart on the source's line — skipped). Actionable
// is the size of the input set, so A+B+C ≤ Actionable (some pairs may
// short-circuit before classification, e.g. lookup errors).
type projectSyncStats struct {
	Actionable int64
	A          int64
	B          int64
	C          int64
}

// ---------------------------------------------------------------------------
// Per-project sync
// ---------------------------------------------------------------------------

// syncProjectIssues handles the full issue-metadata sync for a single project:
//
//  1. Load + pre-filter source issues (only "actionable" via hasManualChanges)
//  2. Wait for Cloud indexing to complete
//  3. For each actionable source issue, search Cloud scoped to its file + rule
//  4. Resolve by line: 1 hit → sync, 0 hits → not_found, >1 → line_mismatch
//  5. Persist per-project a/b/c stats
//
// Replaces the previous fetch-all + FIFO match scheme (#356). The old
// approach paginated /api/issues/search across every cloud issue in the
// project (10k cap, O(cloud) per project regardless of source size);
// the new approach issues one targeted search per ACTIONABLE source
// issue and resolves it in memory. For projects where actionable is in
// the dozens, this is dramatically faster — and the 10k cap no longer
// bites large projects whose total issue count exceeds it.
func syncProjectIssues(ctx context.Context, e *Executor, cloudKey, orgKey, serverURL, serverKey string, counter *TaskCounter, ruleDefaults *ruleTagDefaults) projectSyncStats {
	var stats projectSyncStats

	// 1. Load source issues + pre-filter to actionable.
	sourceIssues := loadMatchableIssues(e, serverURL, serverKey, ruleDefaults)
	if len(sourceIssues) == 0 {
		e.Logger.Debug("syncIssueMetadata: no source issues", "project", cloudKey)
		return stats
	}
	var actionable []matchableIssue
	for _, s := range sourceIssues {
		if hasManualChanges(s) {
			actionable = append(actionable, s)
		}
	}
	stats.Actionable = int64(len(actionable))
	if len(actionable) == 0 {
		e.Logger.Debug("syncIssueMetadata: no actionable source issues after filter", "project", cloudKey, "source_total", len(sourceIssues))
		return stats
	}

	// 2. Wait for Cloud indexing — proves the CE task is done so per-
	// issue searches return real data.
	if err := waitForCloudIndexing(ctx, func() (int, error) {
		params := url.Values{}
		params.Set("componentKeys", cloudKey)
		params.Set("organization", orgKey)
		return e.Cloud.Issues.Count(ctx, params)
	}); err != nil {
		logAPIWarn(e.Logger, "syncIssueMetadata: indexing wait failed", err, "project", cloudKey)
		return stats
	}

	breakdown := classifyActionableReasons(actionable)
	e.Logger.Info("syncIssueMetadata: syncing pairs",
		"project", cloudKey,
		"source_total", len(sourceIssues),
		"actionable", len(actionable),
		"branches", countDistinctBranches(sourceIssues),
		"accepted_or_false_positive", breakdown.acceptedOrFP,
		"custom_tags", breakdown.customTags,
		"manual_severity", breakdown.manualSeverity,
		"comments", breakdown.comments,
	)

	// 3 + 4. Per-actionable-source: targeted search, resolve by line,
	// dispatch. Counted via atomics on the shared stats. Race-safety:
	// the actionable slice is read-only, each goroutine receives one
	// source by value, and stats.{A,B,C} use atomic adds.
	var a, b, c atomic.Int64
	label := "Project key " + cloudKey + " issue sync:"
	runProjectSyncLoop(ctx, e, actionable, label, 20,
		func(gctx context.Context, src matchableIssue) {
			outcome := resolveAndSyncIssue(gctx, e, cloudKey, orgKey, src, counter)
			switch outcome {
			case syncOutcomeSynced:
				a.Add(1)
			case syncOutcomeLineMismatch:
				b.Add(1)
			case syncOutcomeNotFound:
				c.Add(1)
			}
		})
	stats.A = a.Load()
	stats.B = b.Load()
	stats.C = c.Load()
	return stats
}

// syncOutcome is the per-source classification produced by
// resolveAndSyncIssue (#356).
type syncOutcome int

const (
	// syncOutcomeSynced — exactly one cloud counterpart on the same
	// line; pair was sync'd (case a).
	syncOutcomeSynced syncOutcome = iota
	// syncOutcomeLineMismatch — multiple cloud counterparts on the
	// same line (case b); ambiguous, skipped.
	syncOutcomeLineMismatch
	// syncOutcomeNotFound — zero cloud counterparts on the source's
	// line (case c); unexpected for a freshly migrated project,
	// skipped.
	syncOutcomeNotFound
	// syncOutcomeLookupError — the per-source search call failed
	// (network, 5xx). Reported but not classified into a/b/c so a
	// noisy network doesn't pollute the near-perfect signal.
	syncOutcomeLookupError
)

// resolveAndSyncIssue searches Cloud for counterparts of src, resolves
// to one by line, and dispatches the sync. Returns the case-a/b/c/lookup
// outcome.
func resolveAndSyncIssue(ctx context.Context, e *Executor, cloudKey, orgKey string, src matchableIssue, counter *TaskCounter) syncOutcome {
	filePath := stripProjectKeyPrefix(src.Component)
	if filePath == "" || src.Rule == "" || src.Line <= 0 {
		e.Logger.Debug("syncIssueMetadata: source issue not matchable", "key", src.Key, "rule", src.Rule, "component", src.Component, "line", src.Line)
		return syncOutcomeNotFound
	}
	candidates, err := findCloudIssueCandidates(ctx, e, cloudKey, orgKey, filePath, src.Rule)
	if err != nil {
		logAPIWarn(e.Logger, "syncIssueMetadata: cloud candidate lookup failed", err,
			"project", cloudKey, "source_key", src.Key, "rule", src.Rule, "file", filePath)
		return syncOutcomeLookupError
	}
	target, outcome := classifyIssueCandidatesByLine(candidates, src.Line)
	switch outcome {
	case syncOutcomeSynced:
		syncOnePair(ctx, e, issuePair{source: src, cloud: target}, counter)
	case syncOutcomeNotFound:
		e.Logger.Debug("syncIssueMetadata: no cloud counterpart on source line", "source_key", src.Key, "rule", src.Rule, "file", filePath, "line", src.Line)
	case syncOutcomeLineMismatch:
		keys := make([]string, 0)
		for _, c := range candidates {
			if c.Line == src.Line {
				keys = append(keys, c.Key)
			}
		}
		e.Logger.Debug("syncIssueMetadata: multiple cloud counterparts on source line, skipping", "source_key", src.Key, "rule", src.Rule, "file", filePath, "line", src.Line, "candidates", keys)
	}
	return outcome
}

// classifyIssueCandidatesByLine implements the case a/b/c decision
// from #356: among candidates returned by /api/issues/search, pick
// the one on the source's line. 1 → synced, 0 → not_found, n>1 →
// line_mismatch. Factored out so the per-pair logic is unit testable
// without HTTP mocking.
func classifyIssueCandidatesByLine(candidates []matchableIssue, sourceLine int) (matchableIssue, syncOutcome) {
	var pick matchableIssue
	matches := 0
	for _, c := range candidates {
		if c.Line == sourceLine {
			matches++
			if matches == 1 {
				pick = c
			}
		}
	}
	switch matches {
	case 0:
		return matchableIssue{}, syncOutcomeNotFound
	case 1:
		return pick, syncOutcomeSynced
	default:
		return matchableIssue{}, syncOutcomeLineMismatch
	}
}

// findCloudIssueCandidates queries /api/issues/search for cloud issues
// matching the given file path + rule. Typical result set is 1–3
// issues; pagination cost is dwarfed by the savings from no longer
// fetching every issue in the project.
func findCloudIssueCandidates(ctx context.Context, e *Executor, cloudKey, orgKey, filePath, ruleKey string) ([]matchableIssue, error) {
	params := url.Values{}
	params.Set("componentKeys", cloudKey+":"+filePath)
	params.Set("organization", orgKey)
	params.Set("rules", ruleKey)
	// Same statuses we already use on the source side — we want to be
	// idempotent against issues that were already migrated in a prior
	// run (the cloud counterpart may be in ACCEPTED / FALSE_POSITIVE
	// state already).
	params.Set("issueStatuses", "OPEN,CONFIRMED,FALSE_POSITIVE,ACCEPTED")
	// Ask for the per-issue available-transition list so we can verify
	// the "accept" transition is offered before applying it (#322).
	params.Set("additionalFields", "transitions")
	apiIssues, err := e.Cloud.Issues.SearchAll(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]matchableIssue, 0, len(apiIssues))
	for _, ai := range apiIssues {
		out = append(out, apiIssueToMatchable(ai))
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Per-pair sync
// ---------------------------------------------------------------------------

// syncOnePair synchronises a single source-to-cloud issue pair.
// Operations are strictly sequential within the pair to maintain
// ordering guarantees (transition before comments before tags).
//
// Idempotency: if the cloud issue already carries the metadataSyncTag
// the pair is skipped entirely.
func syncOnePair(ctx context.Context, e *Executor, pair issuePair, counter *TaskCounter) {
	if slices.Contains(pair.cloud.Tags, metadataSyncTag) {
		return
	}

	cloudKey := pair.cloud.Key
	transFailed := syncIssueTransition(ctx, e, cloudKey, pair.source, pair.cloud.Transitions)
	commentFailed := syncIssueComments(ctx, e, cloudKey, pair.source.Comments, pair.cloud.Comments)
	tagsFailed := syncIssueTags(ctx, e, cloudKey, pair.source.Tags)

	if transFailed || commentFailed || tagsFailed {
		counter.Fail()
	} else {
		counter.Success()
	}
}

// syncIssueTransition applies the status transition on the Cloud issue.
// cloudTransitions is the matched Cloud issue's available-transition list,
// used to gate the "accept" transition (see resolveTransition).
// Returns true if the transition failed with an unexpected error.
func syncIssueTransition(ctx context.Context, e *Executor, cloudKey string, src matchableIssue, cloudTransitions []string) bool {
	transition, downgraded := resolveTransition(src, cloudTransitions)
	if downgraded {
		// Fidelity loss surfaced explicitly per #322: the source issue
		// is ACCEPTED but the Cloud issue cannot take "accept", so we
		// leave it OPEN rather than mislabeling it as Won't Fix.
		e.Logger.Warn("syncIssueMetadata: 'accept' transition unavailable on Cloud issue; leaving it OPEN rather than mislabeling it Won't Fix",
			"issue", cloudKey,
			"source_issue_status", src.IssueStatus,
			"source_resolution", src.Resolution,
			"available_transitions", cloudTransitions)
	}
	if transition == "" {
		return false
	}
	e.Logger.Debug("syncIssueMetadata: transition", "issue", cloudKey, "transition", transition)
	if err := e.Cloud.Issues.DoTransition(ctx, cloudKey, transition); err != nil {
		if !isExpectedTransitionError(err) {
			logAPIWarn(e.Logger, "syncIssueMetadata: transition failed", err,
				"issue", cloudKey, "transition", transition)
			return true
		}
	}
	return false
}

// migratedIssueCommentPrefix is the marker prepended to every migrated issue comment.
// Its presence in a Cloud comment indicates that comment was already migrated.
const migratedIssueCommentPrefix = "[Migrated from"

// syncIssueComments migrates all source comments to the Cloud issue.
// Skips comments that are already present (idempotency via prefix match).
// Returns true if any comment failed to be added.
func syncIssueComments(ctx context.Context, e *Executor, cloudKey string, sourceComments []issueComment, cloudComments []issueComment) bool {
	var failed bool
	for _, c := range sourceComments {
		text := c.Markdown
		if text == "" {
			text = c.HTMLText
		}
		if text == "" {
			continue
		}
		prefix := fmt.Sprintf("[Migrated from %s", c.Login)
		if c.CreatedAt != "" {
			prefix += " on " + c.CreatedAt
		}
		prefix += "]\n\n"
		fullText := prefix + text

		if isAlreadyMigratedIssueComment(text, cloudComments) {
			continue
		}

		if err := e.Cloud.Issues.AddComment(ctx, cloudKey, fullText); err != nil {
			logAPIWarn(e.Logger, "syncIssueMetadata: add comment failed", err,
				"issue", cloudKey, "login", c.Login)
			failed = true
		}
	}
	return failed
}

// isAlreadyMigratedIssueComment returns true when a migrated comment containing
// body already exists in the Cloud issue's comment list, preventing duplicates on re-run.
// Mirrors the hotspot pattern: checks for the migration prefix AND the original body text.
func isAlreadyMigratedIssueComment(body string, cloudComments []issueComment) bool {
	for _, cc := range cloudComments {
		ccText := cc.Markdown
		if ccText == "" {
			ccText = cc.HTMLText
		}
		if strings.Contains(ccText, migratedIssueCommentPrefix) && strings.Contains(ccText, body) {
			return true
		}
	}
	return false
}

// syncIssueTags sets the source tags plus the idempotency marker on the Cloud issue.
// Returns true if the API call failed.
func syncIssueTags(ctx context.Context, e *Executor, cloudKey string, sourceTags []string) bool {
	tags := make([]string, 0, len(sourceTags)+1)
	tags = append(tags, sourceTags...)
	if !slices.Contains(tags, metadataSyncTag) {
		tags = append(tags, metadataSyncTag)
	}
	if err := e.Cloud.Issues.SetTags(ctx, cloudKey, tags); err != nil {
		logAPIWarn(e.Logger, "syncIssueMetadata: set tags failed", err, "issue", cloudKey)
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Extract loaders
// ---------------------------------------------------------------------------

// ruleTagDefaults indexes the default tag set (rule.tags ∪ rule.sysTags)
// by serverURL → ruleKey. Issue extracts fold these defaults into every
// issue's `tags` array, so a plain `len(tags) > 0` check would treat
// every issue as user-tagged. Subtracting these defaults at load time
// keeps `matchableIssue.Tags` honest as a "user-added tags" signal
// (#352 follow-up).
type ruleTagDefaults struct {
	bySrv map[string]map[string]map[string]struct{}
}

// loadRuleTagDefaults reads the getRuleDetails extract and indexes
// each rule's default tag set. Safe to call when the extract is
// missing or empty — returns a non-nil receiver that simply yields no
// subtraction, in which case issue.Tags falls back to the raw API
// values (the pre-fix behaviour, no worse than what shipped).
func loadRuleTagDefaults(e *Executor) *ruleTagDefaults {
	r := &ruleTagDefaults{bySrv: make(map[string]map[string]map[string]struct{})}
	items, err := readExtractItems(e, "getRuleDetails")
	if err != nil {
		return r
	}
	for _, item := range items {
		key := extractField(item.Data, "key")
		if key == "" {
			continue
		}
		defaults := make(map[string]struct{})
		for _, t := range extractStringArray(item.Data, "tags") {
			defaults[t] = struct{}{}
		}
		for _, t := range extractStringArray(item.Data, "sysTags") {
			defaults[t] = struct{}{}
		}
		inner := r.bySrv[item.ServerURL]
		if inner == nil {
			inner = make(map[string]map[string]struct{})
			r.bySrv[item.ServerURL] = inner
		}
		inner[key] = defaults
	}
	return r
}

// UserTagsOnly returns the subset of allTags that are NOT default tags
// on the given rule. When the rule isn't indexed (missing extract,
// stale cache) the input is returned unchanged — the caller pays the
// cost of an over-trigger, which is no worse than the pre-fix behaviour.
func (r *ruleTagDefaults) UserTagsOnly(serverURL, ruleKey string, allTags []string) []string {
	if r == nil || len(allTags) == 0 {
		return allTags
	}
	inner := r.bySrv[serverURL]
	if inner == nil {
		return allTags
	}
	defaults, ok := inner[ruleKey]
	if !ok {
		return allTags
	}
	out := make([]string, 0, len(allTags))
	for _, t := range allTags {
		if _, isDefault := defaults[t]; !isDefault {
			out = append(out, t)
		}
	}
	return out
}

// loadMatchableIssues reads the extracted SQS issues for a project and
// converts them to matchableIssue values.
//
// Issues with no Cloud counterpart are excluded — intentionally and,
// per #322, explicitly (the FIXED count is logged rather than silently
// dropped):
//   - CLOSED status: the issue was removed by analysis.
//   - FIXED (legacy resolution=FIXED or modern issueStatus=FIXED): the
//     underlying code was fixed, so a fresh Cloud scan does not re-raise
//     the issue. There is nothing to sync onto — this is a documented
//     exclusion, not a bug.
//
// ruleDefaults is used to strip rule-default tags from each issue's
// tag list so that matchableIssue.Tags holds only the user-added tags
// — see the type doc on ruleTagDefaults.
func loadMatchableIssues(e *Executor, serverURL, serverKey string, ruleDefaults *ruleTagDefaults) []matchableIssue {
	items, err := readExtractItems(e, "getProjectIssuesFull")
	if err != nil {
		return nil
	}

	var issues []matchableIssue
	var excludedFixed int
	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}
		if extractField(item.Data, "projectKey") != serverKey {
			continue
		}

		status := strings.ToUpper(extractField(item.Data, "status"))
		resolution := strings.ToUpper(extractField(item.Data, "resolution"))
		issueStatus := strings.ToUpper(extractField(item.Data, "issueStatus"))

		// Exclude CLOSED and FIXED — these have no Cloud counterpart.
		if status == "CLOSED" {
			continue
		}
		if resolution == "FIXED" || issueStatus == "FIXED" {
			excludedFixed++
			continue
		}

		line := int(extractInt32Field(item.Data, "line"))

		comments := parseIssueComments(item.Data)
		rule := extractField(item.Data, "rule")
		userTags := ruleDefaults.UserTagsOnly(serverURL, rule, extractStringArray(item.Data, "tags"))

		issues = append(issues, matchableIssue{
			Key:            extractField(item.Data, "key"),
			Rule:           rule,
			Component:      extractField(item.Data, "component"),
			Line:           line,
			Status:         extractField(item.Data, "status"),
			Resolution:     extractField(item.Data, "resolution"),
			IssueStatus:    extractField(item.Data, "issueStatus"),
			Tags:           userTags,
			Comments:       comments,
			Assignee:       extractField(item.Data, "assignee"),
			ManualSeverity: extractBool(item.Data, "manualSeverity"),
			Branch:         extractField(item.Data, "branch"),
		})
	}

	// Surface the FIXED exclusion explicitly (#322) rather than dropping
	// silently: FIXED issues are resolved because the code was fixed, so
	// a fresh Cloud scan does not re-raise them and there is nothing to
	// sync onto. Operators should still see how many were skipped.
	if excludedFixed > 0 {
		e.Logger.Info("syncIssueMetadata: excluding FIXED source issues from status sync (code was fixed; no Cloud counterpart is re-raised by analysis)",
			"project", serverKey, "excluded_fixed", excludedFixed)
	}
	return issues
}

// apiIssueToMatchable converts an sq-api-go Issue into the local
// matchableIssue shape used by the sync orchestration.
func apiIssueToMatchable(ai sqtypes.Issue) matchableIssue {
	comments := make([]issueComment, 0, len(ai.Comments))
	for _, c := range ai.Comments {
		comments = append(comments, issueComment{
			Login:     c.Login,
			HTMLText:  c.HTMLText,
			Markdown:  c.Markdown,
			CreatedAt: c.CreatedAt,
		})
	}
	return matchableIssue{
		Key:         ai.Key,
		Rule:        ai.Rule,
		Component:   ai.Component,
		Line:        ai.Line,
		Status:      ai.Status,
		Resolution:  ai.Resolution,
		Tags:        ai.Tags,
		Comments:    comments,
		Assignee:    ai.Assignee,
		Transitions: ai.Transitions,
	}
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

// parseIssueComments extracts the "comments" array from an issue's raw
// JSON data and returns them as issueComment values.
func parseIssueComments(data json.RawMessage) []issueComment {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	commentsRaw, ok := obj["comments"]
	if !ok {
		return nil
	}
	var raw []struct {
		Login     string `json:"login"`
		HTMLText  string `json:"htmlText"`
		Markdown  string `json:"markdown"`
		CreatedAt string `json:"createdAt"`
	}
	if err := json.Unmarshal(commentsRaw, &raw); err != nil {
		return nil
	}
	comments := make([]issueComment, 0, len(raw))
	for _, r := range raw {
		comments = append(comments, issueComment{
			Login:     r.Login,
			HTMLText:  r.HTMLText,
			Markdown:  r.Markdown,
			CreatedAt: r.CreatedAt,
		})
	}
	return comments
}
