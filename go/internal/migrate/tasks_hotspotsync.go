// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// hotspotMetadataSyncTasks returns the task definitions for syncing hotspot
// metadata (status, resolution, comments) from SonarQube Server to Cloud.
func hotspotMetadataSyncTasks() []TaskDef {
	return []TaskDef{{
		Name:         "syncHotspotMetadata",
		Editions:     common.AllEditions,
		Dependencies: []string{"importProjectData"},
		Run:          runSyncHotspotMetadata,
	}}
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// matchableHotspot is a normalised hotspot representation used for FIFO
// matching between source (SonarQube Server) and target (SonarQube Cloud).
//
// Offset is the textRange.startOffset (column) and disambiguates
// co-located hotspots on the same line (#392 follow-up). Two
// hotspots of the same rule firing on different columns of the same
// line — e.g., `sys.argv[1]` and `sys.argv[2]` on a single line —
// must NOT be collapsed to a single representative; without an
// offset key they would race and one cloud counterpart would
// silently stay TO_REVIEW. Offset is 0 when the source data
// predates `textRange` or the cloud endpoint omits it; callers
// fall back to coarser matching in that case.
type matchableHotspot struct {
	Key        string
	RuleKey    string
	Component  string
	Line       int
	Offset     int
	Status     string
	Resolution string
	Comments   []hotspotComment
	// Branch is the source SonarQube Server branch the hotspot was
	// extracted from (enriched into the extract record). Used to build a
	// branch-correct back-link to the original hotspot (#321).
	Branch string
}

// hotspotComment captures a single comment attached to a hotspot.
type hotspotComment struct {
	Login     string
	HTMLText  string
	Markdown  string
	CreatedAt string
}

// hotspotPair links a source hotspot to its matched Cloud counterpart.
type hotspotPair struct {
	source matchableHotspot
	cloud  matchableHotspot
}

// ---------------------------------------------------------------------------
// Actionable filtering (source-side, #350 / #356)
// ---------------------------------------------------------------------------

// hotspotHasManualChanges mirrors hasManualChanges for issues: returns
// true when the source hotspot carries metadata worth migrating to
// Cloud. Same criteria as the previous filterActionableHotspotPairs
// (#350) — REVIEWED with a real review resolution, or any comment —
// but applied source-side BEFORE we look at Cloud (#356).
func hotspotHasManualChanges(h matchableHotspot) bool {
	status := strings.ToUpper(h.Status)
	resolution := strings.ToUpper(h.Resolution)
	reviewed := status == "REVIEWED" && (resolution == "SAFE" || resolution == "ACKNOWLEDGED" || resolution == "FIXED")
	return reviewed || len(h.Comments) > 0
}

// HotspotHasManualChanges is the exported counterpart of
// hotspotHasManualChanges. Read by the predict pipeline (#323), where
// the synthesizer has only the raw extract record in hand and needs
// the same filter to count actionable hotspots per project.
func HotspotHasManualChanges(status, resolution string, hasComments bool) bool {
	s := strings.ToUpper(status)
	r := strings.ToUpper(resolution)
	reviewed := s == "REVIEWED" && (r == "SAFE" || r == "ACKNOWLEDGED" || r == "FIXED")
	return reviewed || hasComments
}

// IsAcknowledgedResolution reports whether a hotspot resolution is the
// SonarQube Server-only ACKNOWLEDGED state (#323). Exported so the
// predict pipeline can count these without duplicating the literal.
func IsAcknowledgedResolution(resolution string) bool {
	return strings.EqualFold(strings.TrimSpace(resolution), "ACKNOWLEDGED")
}

// hotspotResolutionPriority orders source resolutions from most to
// least "cautious" — used by dedupeActionableHotspots when several
// source-branch records collapse to the same cloud hotspot. The most
// cautious wins so a hotspot ACKNOWLEDGED on any branch is never
// silently downgraded to SAFE on Cloud by a sibling-branch record.
//
//	ACKNOWLEDGED → 0 (highest priority, will reset Cloud to TO_REVIEW)
//	TO_REVIEW    → 1
//	FIXED        → 2
//	SAFE         → 3 (lowest priority — the most permissive state)
//	(anything else) → 4
func hotspotResolutionPriority(h matchableHotspot) int {
	status := strings.ToUpper(strings.TrimSpace(h.Status))
	resolution := strings.ToUpper(strings.TrimSpace(h.Resolution))
	switch {
	case status == "REVIEWED" && resolution == "ACKNOWLEDGED":
		return 0
	case status == "TO_REVIEW":
		return 1
	case status == "REVIEWED" && resolution == "FIXED":
		return 2
	case status == "REVIEWED" && resolution == "SAFE":
		return 3
	}
	return 4
}

// dedupeActionableHotspots collapses cross-branch duplicate source
// hotspots — same (component, ruleKey, line, offset) but different
// SQS keys — into a single representative whose Comments are the
// union of all duplicates' comments. The representative carries the
// highest-priority (most cautious) status/resolution per
// hotspotResolutionPriority so an ACKNOWLEDGED branch always wins
// over a SAFE sibling. Iteration order is stable (sorted by source
// key) so the result is deterministic.
//
// Offset is part of the key (#392 follow-up) so two hotspots of the
// same rule firing on different columns of the same line — e.g.
// `sys.argv[1]` and `sys.argv[2]` on a single line — stay as two
// distinct source reps. Collapsing them caused half the cloud
// counterparts to silently stay TO_REVIEW (live evidence:
// 6 of 31 SAFE hotspots stuck on python:S4823).
func dedupeActionableHotspots(in []matchableHotspot) []matchableHotspot {
	if len(in) < 2 {
		return in
	}
	type groupKey struct {
		Component string
		RuleKey   string
		Line      int
		Offset    int
	}
	sorted := make([]matchableHotspot, len(in))
	copy(sorted, in)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })

	groups := make(map[groupKey]*matchableHotspot, len(sorted))
	order := make([]groupKey, 0, len(sorted))
	for i := range sorted {
		h := sorted[i]
		k := groupKey{Component: h.Component, RuleKey: h.RuleKey, Line: h.Line, Offset: h.Offset}
		rep, ok := groups[k]
		if !ok {
			cp := h
			groups[k] = &cp
			order = append(order, k)
			continue
		}
		if hotspotResolutionPriority(h) < hotspotResolutionPriority(*rep) {
			// New record beats the current rep on priority — promote
			// it, then re-append the previous rep's comments so the
			// new rep's Comments still reflect the union.
			prevComments := rep.Comments
			cp := h
			groups[k] = &cp
			rep = groups[k]
			rep.Comments = append(rep.Comments, prevComments...)
		} else {
			rep.Comments = append(rep.Comments, h.Comments...)
		}
	}
	out := make([]matchableHotspot, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	return out
}

// ---------------------------------------------------------------------------
// Resolution mapping
// ---------------------------------------------------------------------------

// mapHotspotResolution converts a SonarQube Server hotspot resolution
// into the equivalent SonarQube Cloud resolution value, or "" when the
// source resolution has no SonarQube Cloud counterpart and the caller
// must skip the status change entirely (#323 — ACKNOWLEDGED falls
// here: SQC has no equivalent, so we leave the hotspot in its default
// TO_REVIEW state and record the demotion in the per-project stats).
func mapHotspotResolution(resolution string) string {
	switch strings.ToUpper(resolution) {
	case "SAFE":
		return "SAFE"
	case "FIXED":
		return "FIXED"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Main task entry point
// ---------------------------------------------------------------------------

// runSyncHotspotMetadata is the Run function for the syncHotspotMetadata task.
// It iterates over every project created during migration and synchronises
// hotspot statuses and comments from the SonarQube Server extract to Cloud.
func runSyncHotspotMetadata(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "syncHotspotMetadata", "createProjects",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			cloudKey := extractField(item, "cloud_project_key")
			orgKey := extractField(item, "sonarcloud_org_key")
			serverURL := extractField(item, "server_url")
			serverKey := extractField(item, "key")

			if cloudKey == "" || orgKey == "" {
				return nil
			}

			result := syncProjectHotspots(ctx, e, syncHotspotInput{
				CloudKey:  cloudKey,
				OrgKey:    orgKey,
				ServerURL: serverURL,
				ServerKey: serverKey,
			})

			record, _ := json.Marshal(map[string]any{
				"cloud_project_key":    cloudKey,
				"synced":               result.Stats.A,
				"line_mismatch":        result.Stats.B,
				"not_found":            result.Stats.C,
				"acknowledged_demoted": result.Stats.AckDemoted,
				"actionable":           result.Stats.Actionable,
				"error":                result.Error,
			})
			return w.WriteOne(record)
		})
}

// ---------------------------------------------------------------------------
// Per-project sync
// ---------------------------------------------------------------------------

type syncHotspotInput struct {
	CloudKey  string
	OrgKey    string
	ServerURL string
	ServerKey string
}

// syncHotspotResult holds the per-project sync outcome. Stats carries
// the a/b/c breakdown (#356); Error captures a fatal lookup failure
// that prevented the project from being processed at all.
type syncHotspotResult struct {
	Stats projectSyncStats
	Error string
}

// syncProjectHotspots synchronises hotspot metadata for a single
// project using the targeted per-actionable-source-hotspot search
// approach introduced in #356. Replaces the previous fetch-all + FIFO
// match scheme.
func syncProjectHotspots(ctx context.Context, e *Executor, input syncHotspotInput) syncHotspotResult {
	projStart := time.Now()
	counter := NewTaskCounter("syncHotspotMetadata:" + input.CloudKey)
	defer func() { counter.LogSummary(e.Logger, time.Since(projStart)) }()

	var result syncHotspotResult

	// 1. Load + pre-filter source hotspots to the actionable set.
	sourceHotspots, err := loadMatchableHotspots(e, input.ServerURL, input.ServerKey)
	if err != nil {
		result.Error = err.Error()
		logAPIWarn(e.Logger, "syncHotspotMetadata: load source hotspots failed", err, "project", input.CloudKey)
		return result
	}
	if len(sourceHotspots) == 0 {
		return result
	}
	var actionable []matchableHotspot
	for _, h := range sourceHotspots {
		if hotspotHasManualChanges(h) {
			actionable = append(actionable, h)
		}
	}
	// #323 follow-up: the source extract carries one hotspot record per
	// branch of the SQS project, but a single SQC hotspot exists per
	// (file, line, rule). Without dedup, two source records that map
	// to the same cloud hotspot race in the dispatch loop — the loser
	// silently overwrites the winner's change_status call. When one is
	// ACKNOWLEDGED (→ TO_REVIEW reset) and another is REVIEWED/SAFE
	// (→ change_status SAFE), the SAFE call wins on order and the
	// ACKNOWLEDGED demotion is lost. Dedup by (component, ruleKey,
	// line) before dispatch, picking the most cautious resolution per
	// group so an ACK on any branch wins over SAFE/FIXED on another.
	preDedupCount := len(actionable)
	actionable = dedupeActionableHotspots(actionable)
	if dropped := preDedupCount - len(actionable); dropped > 0 {
		e.Logger.Info("syncHotspotMetadata: deduplicated cross-branch source hotspots",
			"project", input.CloudKey, "before", preDedupCount, "after", len(actionable), "dropped", dropped)
	}
	result.Stats.Actionable = int64(len(actionable))
	if len(actionable) == 0 {
		e.Logger.Info("syncHotspotMetadata: no actionable source hotspots after filter", "project", input.CloudKey, "source_total", len(sourceHotspots))
		return result
	}

	// 2. Wait for Cloud indexing — proves the CE task is done.
	_ = waitForCloudIndexing(ctx, func() (int, error) {
		return e.Cloud.Hotspots.Count(ctx, input.CloudKey, input.OrgKey)
	})

	e.Logger.Info("syncHotspotMetadata: syncing pairs",
		"project", input.CloudKey,
		"source_total", len(sourceHotspots),
		"actionable", len(actionable),
	)

	// 3 + 4. Per-actionable-source: targeted search + resolve by
	// (ruleKey, line). Race-safety: actionable is read-only, each
	// goroutine takes one hotspot by value, stats counters are
	// atomic.
	// Public base URL for back-links — prefer the SQS sonar.core.serverBaseURL
	// setting over the (often localhost) connection URL (#321).
	baseURL := resolveSourceBaseURL(e, input.ServerURL)

	var a, b, c, ack atomic.Int64
	label := "Project key " + input.CloudKey + " hotspot sync:"
	runProjectSyncLoop(ctx, e, actionable, label, 10,
		func(gctx context.Context, src matchableHotspot) {
			outcome := resolveAndSyncHotspot(gctx, e, input.CloudKey, input.OrgKey, baseURL, input.ServerKey, src, counter)
			switch outcome {
			case syncOutcomeSynced:
				a.Add(1)
			case syncOutcomeLineMismatch:
				b.Add(1)
			case syncOutcomeNotFound:
				c.Add(1)
			case syncOutcomeAckDemoted:
				ack.Add(1)
			}
		})
	result.Stats.A = a.Load()
	result.Stats.B = b.Load()
	result.Stats.C = c.Load()
	result.Stats.AckDemoted = ack.Load()
	return result
}

// resolveAndSyncHotspot searches Cloud for hotspots in the source
// hotspot's file, then resolves by (ruleKey, line). Returns the case
// a/b/c/lookup outcome.
func resolveAndSyncHotspot(ctx context.Context, e *Executor, cloudKey, orgKey, baseURL, sourceKey string, src matchableHotspot, counter *TaskCounter) syncOutcome {
	// Strip "projectKey:" and any trailing "moduleKey:" segments so the bare
	// file path can be used in the cloud search. Multi-module (monorepo)
	// projects add a module key after the project key; SonarCloud has no
	// module layer so only the plain file path matches the cloud component.
	filePath := stripProjectKeyPrefix(src.Component)
	if filePath == "" || src.RuleKey == "" || src.Line <= 0 {
		e.Logger.Debug("syncHotspotMetadata: source hotspot not matchable", "key", src.Key, "rule", src.RuleKey, "component", src.Component, "line", src.Line)
		return syncOutcomeNotFound
	}
	candidates, err := findCloudHotspotCandidates(ctx, e, cloudKey, orgKey, filePath, src.Branch)
	if err != nil {
		logAPIWarn(e.Logger, "syncHotspotMetadata: cloud candidate lookup failed", err,
			"project", cloudKey, "source_key", src.Key, "file", filePath, "branch", src.Branch)
		return syncOutcomeLookupError
	}
	target, outcome := classifyHotspotCandidatesByLine(candidates, src.RuleKey, src.Line, src.Offset)
	switch outcome {
	case syncOutcomeSynced:
		pair := hotspotPair{source: src, cloud: target}
		if err := syncOneHotspot(ctx, e, pair, baseURL, sourceKey); err != nil {
			counter.Fail()
			logAPIWarn(e.Logger, "syncHotspotMetadata: hotspot sync failed", err,
				"source_key", src.Key, "cloud_key", target.Key)
		} else {
			counter.Success()
		}
		// #323: matched a cloud counterpart but the source resolution
		// is ACKNOWLEDGED, which SQC doesn't support. syncOneHotspot
		// has already skipped the status change (still synced comments)
		// — re-classify here so the per-project tally records a
		// demotion instead of a clean sync.
		if IsAcknowledgedResolution(src.Resolution) {
			outcome = syncOutcomeAckDemoted
		}
	case syncOutcomeNotFound:
		e.Logger.Debug("syncHotspotMetadata: no cloud counterpart on source line", "source_key", src.Key, "rule", src.RuleKey, "file", filePath, "line", src.Line)
	case syncOutcomeLineMismatch:
		keys := make([]string, 0)
		for _, c := range candidates {
			if c.Line == src.Line && (c.RuleKey == "" || c.RuleKey == src.RuleKey) {
				keys = append(keys, c.Key)
			}
		}
		e.Logger.Debug("syncHotspotMetadata: multiple cloud counterparts on source line, skipping", "source_key", src.Key, "rule", src.RuleKey, "file", filePath, "line", src.Line, "candidates", keys)
	}
	return outcome
}

// classifyHotspotCandidatesByLine resolves a cloud counterpart for one
// source hotspot from the per-file candidate set returned by
// /api/hotspots/search?files=<filePath>.
//
// Three-phase match (#392 + follow-up):
//
//  1. PRECISE — (ruleKey, line, offset) match. textRange.startOffset
//     disambiguates two hotspots of the same rule firing on different
//     columns of the same line (e.g. `sys.argv[1]` and `sys.argv[2]`).
//     Without it, those collapse to syncOutcomeLineMismatch and stay
//     TO_REVIEW. Skipped when either side has Offset == 0 (older API
//     shapes that omit textRange).
//
//  2. RULE+LINE — (ruleKey, line) match. The common case: rule
//     populated on both sides, no column ambiguity needed. Restores
//     the pre-offset behaviour that already covered 25 of the 31
//     SAFE hotspots in the live run.
//
//  3. EMPTY-RULE FALLBACK — line-only against cloud candidates whose
//     ruleKey is empty. The 2026-06-09 audit recorded a case where
//     every cloud hotspot parsed with RuleKey == "" (per-version /
//     per-endpoint omission); without this fallback the entire
//     project's REVIEWED hotspots stay TO_REVIEW. Candidates with a
//     non-empty ruleKey that doesn't match the source are deliberately
//     NOT considered here — they're a different rule firing on the
//     same line.
//
// Returns syncOutcomeSynced when exactly one candidate qualifies,
// syncOutcomeLineMismatch when several do (caller skips rather than
// guess), and syncOutcomeNotFound when none do.
func classifyHotspotCandidatesByLine(candidates []matchableHotspot, sourceRule string, sourceLine, sourceOffset int) (matchableHotspot, syncOutcome) {
	// Phase 1: precise (ruleKey, line, offset). Both sides must carry
	// a non-zero offset for this phase to be considered.
	if sourceOffset > 0 {
		var pick matchableHotspot
		matches := 0
		for _, c := range candidates {
			if c.RuleKey == sourceRule && c.Line == sourceLine && c.Offset > 0 && c.Offset == sourceOffset {
				matches++
				if matches == 1 {
					pick = c
				}
			}
		}
		if matches == 1 {
			return pick, syncOutcomeSynced
		}
		// matches == 0 (offset not present or no exact column hit) →
		// fall through to phase 2. matches > 1 means duplicate offsets
		// on the cloud (extremely unusual) — also fall through and let
		// phase 2's rule+line check resolve to line_mismatch.
	}

	// Phase 2: (ruleKey, line) match.
	var pick matchableHotspot
	matches := 0
	for _, c := range candidates {
		if c.RuleKey == sourceRule && c.Line == sourceLine {
			matches++
			if matches == 1 {
				pick = c
			}
		}
	}
	if matches > 0 {
		if matches == 1 {
			return pick, syncOutcomeSynced
		}
		return matchableHotspot{}, syncOutcomeLineMismatch
	}

	// Phase 3: cloud candidates with no ruleKey are eligible for a
	// line-only fallback.
	matches = 0
	for _, c := range candidates {
		if c.RuleKey == "" && c.Line == sourceLine {
			matches++
			if matches == 1 {
				pick = c
			}
		}
	}
	switch matches {
	case 0:
		return matchableHotspot{}, syncOutcomeNotFound
	case 1:
		return pick, syncOutcomeSynced
	default:
		return matchableHotspot{}, syncOutcomeLineMismatch
	}
}

// findCloudHotspotCandidates queries /api/hotspots/search?files=…
// for cloud hotspots in a single file. Hotspots are scarce enough
// that we can skip the rule filter here and resolve in memory; the
// /api/hotspots/search endpoint also does not accept a rules
// parameter.
//
// The endpoint defaults to status=TO_REVIEW when no status is
// supplied — a hotspot already moved to REVIEWED by a prior migration
// run would be invisible (#323). We issue one paginated request per
// status (TO_REVIEW + REVIEWED) and merge the results so re-runs can
// see (and correct) cloud hotspots in any state, deduplicating by
// hotspot key on the off chance the same key surfaces in both
// responses.
//
// Uses e.Raw (the cloud-side raw client) because the typed
// HotspotsClient's SearchAll doesn't expose a per-file filter.
//
// The branch parameter is essential for multi-branch projects: without it
// /api/hotspots/search resolves against the project's main branch only, so
// hotspots on non-main branches never find their cloud counterpart and go
// unsynced. Source and target branch names match 1:1 (the main branch is
// renamed to the source name on import — #428), so the source branch name
// is the correct cloud branch to search.
func findCloudHotspotCandidates(ctx context.Context, e *Executor, cloudKey, orgKey, filePath, branch string) ([]matchableHotspot, error) {
	baseParams := func() url.Values {
		p := url.Values{}
		p.Set("projectKey", cloudKey)
		p.Set("files", filePath)
		if orgKey != "" {
			p.Set("organization", orgKey)
		}
		if branch != "" {
			p.Set("branch", branch)
		}
		return p
	}

	out := make([]matchableHotspot, 0)
	seen := make(map[string]bool)
	for _, status := range []string{"TO_REVIEW", "REVIEWED"} {
		params := baseParams()
		params.Set("status", status)
		items, err := e.Raw.GetPaginated(ctx, common.PaginatedOpts{
			Path:      "api/hotspots/search",
			Params:    params,
			ResultKey: "hotspots",
			PageLimit: 5, // hotspots-in-one-file is small; cap for safety
		})
		if err != nil {
			return nil, err
		}
		for _, raw := range items {
			h := parseMatchableHotspot(raw)
			if h.Key == "" || seen[h.Key] {
				continue
			}
			seen[h.Key] = true
			out = append(out, h)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Per-hotspot sync
// ---------------------------------------------------------------------------

// syncOneHotspot synchronises a single hotspot's status and comments, then
// appends a back-link to the original SonarQube Server hotspot (#321).
// Operations are sequential within each hotspot: status first, then comments,
// then the source-link comment.
func syncOneHotspot(ctx context.Context, e *Executor, pair hotspotPair, baseURL, projectKey string) error {
	// 1. Sync status. Three branches when the source is REVIEWED:
	//   - SAFE/FIXED → mapHotspotResolution returns the SQC resolution;
	//     post change_status REVIEWED+<resolution>.
	//   - ACKNOWLEDGED → SQC has no equivalent resolution. Actively
	//     reset the cloud hotspot to TO_REVIEW with no resolution so a
	//     re-run undoes any SAFE state a previous (buggy) migration
	//     may have left on SQC. Idempotent — TO_REVIEW is also the
	//     cloud default for a never-touched hotspot. #323.
	//   - Unknown resolution → no API call (defensive).
	// A status-sync failure is recorded but does NOT short-circuit the
	// rest of the function — the comment and source-link steps must still
	// run. Returning early here was why already-synced hotspots (whose
	// change_status is rejected because they're already REVIEWED on a
	// re-run) never got a back-link (#321).
	var statusErr error
	if strings.ToUpper(pair.source.Status) == "REVIEWED" {
		resolution := mapHotspotResolution(pair.source.Resolution)
		switch {
		case resolution != "":
			if err := e.Cloud.Hotspots.ChangeStatus(ctx, pair.cloud.Key, "REVIEWED", resolution); err != nil {
				statusErr = fmt.Errorf("change status: %w", err)
			}
		case IsAcknowledgedResolution(pair.source.Resolution):
			if err := e.Cloud.Hotspots.ChangeStatus(ctx, pair.cloud.Key, "TO_REVIEW", ""); err != nil {
				statusErr = fmt.Errorf("reset ACKNOWLEDGED to TO_REVIEW: %w", err)
			}
		}
	}

	// 2. Sync comments + the source-link comment. Cloud detail is fetched
	// once up front for the idempotency checks of both. Unlike before, we
	// fetch even when the source has no comments, because the source-link
	// comment (#321) is always appended for a matched hotspot.
	cloudComments := fetchCloudHotspotComments(ctx, e, pair.cloud.Key)
	for _, comment := range pair.source.Comments {
		if isAlreadyMigratedComment(comment, cloudComments) {
			continue
		}

		text := formatMigratedHotspotComment(comment)
		if text == "" {
			continue
		}

		if err := e.Cloud.Hotspots.AddComment(ctx, pair.cloud.Key, text); err != nil {
			logAPIWarn(e.Logger, "syncHotspotMetadata: add comment failed", err,
				"cloud_key", pair.cloud.Key,
				"comment_author", comment.Login,
			)
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	// 3. Append a back-link to the original SonarQube Server hotspot so
	// engineers can click through to its origin (#321). Best-effort and
	// idempotent — consistent with the migrated-comment handling above.
	// Always attempted, even if the status sync above failed.
	addHotspotSourceLink(ctx, e, pair.cloud.Key, baseURL, projectKey, pair.source.Key, pair.source.Branch, cloudComments)

	return statusErr
}

// hotspotSourceLinkURL builds the SonarQube Server deep link back to the
// original hotspot, or "" when any component is missing. #321.
func hotspotSourceLinkURL(baseURL, projectKey, hotspotKey, branch string) string {
	if baseURL == "" || projectKey == "" || hotspotKey == "" {
		return ""
	}
	base := strings.TrimRight(baseURL, "/")
	u := fmt.Sprintf("%s/security_hotspots?id=%s&hotspots=%s",
		base, url.QueryEscape(projectKey), url.QueryEscape(hotspotKey))
	if branch != "" {
		u += "&branch=" + url.QueryEscape(branch)
	}
	return u
}

// hotspotSourceLinkMarker is the stable, per-hotspot-unique prefix used to
// detect an already-added source-link comment (see issueSourceLinkMarker for
// why we match on the marker rather than the full URL).
const hotspotSourceLinkMarker = "Link to [Original hotspot]"

// addHotspotSourceLink posts a one-line "Link to [Original hotspot](…)"
// comment pointing back to the source hotspot (#321). Best-effort and
// idempotent: skipped when a cloud comment already carries the marker.
func addHotspotSourceLink(ctx context.Context, e *Executor, cloudKey, baseURL, projectKey, sourceHotspotKey, branch string, cloudComments []hotspotComment) {
	link := hotspotSourceLinkURL(baseURL, projectKey, sourceHotspotKey, branch)
	if link == "" {
		return
	}
	if hotspotCommentsContain(cloudComments, hotspotSourceLinkMarker) {
		return
	}
	text := hotspotSourceLinkMarker + "(" + link + ")"
	if err := e.Cloud.Hotspots.AddComment(ctx, cloudKey, text); err != nil {
		e.Logger.Warn("syncHotspotMetadata: could not add source-link comment (non-fatal)",
			"cloud_key", cloudKey, "reason", sourceLinkErrSummary(err))
	}
}

// hotspotCommentsContain reports whether any cloud comment's text contains substr.
func hotspotCommentsContain(cloudComments []hotspotComment, substr string) bool {
	for _, cc := range cloudComments {
		t := cc.Markdown
		if t == "" {
			t = cc.HTMLText
		}
		if strings.Contains(t, substr) {
			return true
		}
	}
	return false
}

// fetchCloudHotspotComments retrieves comments for a Cloud hotspot via the
// /api/hotspots/show endpoint. Returns nil on failure (non-fatal — worst case
// is duplicate comments, not data loss).
func fetchCloudHotspotComments(ctx context.Context, e *Executor, cloudKey string) []hotspotComment {
	detail, err := e.Cloud.Hotspots.Show(ctx, cloudKey)
	if err != nil {
		e.Logger.Debug("syncHotspotMetadata: could not fetch cloud hotspot detail",
			"cloud_key", cloudKey, "err", err)
		return nil
	}
	comments := make([]hotspotComment, 0, len(detail.Comment))
	for _, c := range detail.Comment {
		comments = append(comments, hotspotComment{
			Login:    c.Login,
			HTMLText: c.HTMLText,
			Markdown: c.Markdown,
		})
	}
	return comments
}

// migratedCommentPrefix is prepended to every migrated comment.
const migratedCommentPrefix = "[Migrated from SonarQube]"

// formatMigratedHotspotComment builds the comment text to post to Cloud.
func formatMigratedHotspotComment(c hotspotComment) string {
	body := c.Markdown
	if body == "" {
		body = c.HTMLText
	}
	if body == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(migratedCommentPrefix)
	sb.WriteString("\n\n")
	if c.Login != "" {
		sb.WriteString("**Author:** ")
		sb.WriteString(c.Login)
		sb.WriteString("\n")
	}
	if c.CreatedAt != "" {
		sb.WriteString("**Date:** ")
		sb.WriteString(c.CreatedAt)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(body)
	return sb.String()
}

// isAlreadyMigratedComment checks whether a source comment has already been
// migrated to Cloud by looking for the migration prefix in Cloud comments.
func isAlreadyMigratedComment(source hotspotComment, cloudComments []hotspotComment) bool {
	body := source.Markdown
	if body == "" {
		body = source.HTMLText
	}
	if body == "" {
		return true // empty comment, treat as already handled
	}

	for _, cc := range cloudComments {
		ccText := cc.Markdown
		if ccText == "" {
			ccText = cc.HTMLText
		}
		if strings.Contains(ccText, migratedCommentPrefix) && strings.Contains(ccText, body) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Loading source hotspots from extract data
// ---------------------------------------------------------------------------

// loadMatchableHotspots reads the getProjectHotspotsFull extract items and
// converts them into normalised matchableHotspot structs.
func loadMatchableHotspots(e *Executor, serverURL, serverKey string) ([]matchableHotspot, error) {
	items, err := readExtractItems(e, "getProjectHotspotsFull")
	if err != nil {
		return nil, fmt.Errorf("loadMatchableHotspots: %w", err)
	}

	var hotspots []matchableHotspot
	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}

		// Filter by project key.
		projKey := extractField(item.Data, "project")
		if projKey == "" {
			projKey = extractField(item.Data, "projectKey")
		}
		if projKey != serverKey {
			continue
		}

		h := parseMatchableHotspot(item.Data)
		if h.Key == "" {
			continue
		}
		hotspots = append(hotspots, h)
	}
	return hotspots, nil
}

// parseMatchableHotspot extracts a matchableHotspot from a raw JSON object.
func parseMatchableHotspot(data json.RawMessage) matchableHotspot {
	key := extractField(data, "key")
	component := extractField(data, "component")
	status := extractField(data, "status")
	resolution := extractField(data, "resolution")
	line := extractHotspotLine(data)
	offset := extractHotspotStartOffset(data)

	// ruleKey may be at top level or nested inside a "rule" object.
	ruleKey := extractField(data, "ruleKey")
	if ruleKey == "" {
		ruleKey = extractNestedField(data, "rule", "key")
	}

	comments := parseHotspotComments(data)

	return matchableHotspot{
		Key:        key,
		RuleKey:    ruleKey,
		Component:  component,
		Line:       line,
		Offset:     offset,
		Status:     status,
		Resolution: resolution,
		Comments:   comments,
		// Present on source-side records (enriched at extract time);
		// absent/empty on cloud candidates, which don't need it.
		Branch: extractField(data, "branch"),
	}
}

// extractHotspotLine reads the "line" field from a hotspot JSON object.
func extractHotspotLine(data json.RawMessage) int {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return 0
	}
	raw, ok := obj["line"]
	if !ok {
		return 0
	}
	var v int
	if json.Unmarshal(raw, &v) == nil {
		return v
	}
	// Try as string.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, _ := strconv.Atoi(s)
		return n
	}
	return 0
}

// extractHotspotStartOffset reads the textRange.startOffset column
// from a hotspot JSON object. Returns 0 when the field is absent —
// callers MUST treat 0 as "unknown" rather than "column 0" so the
// matcher's offset-based disambiguation falls back gracefully on
// older API shapes.
func extractHotspotStartOffset(data json.RawMessage) int {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return 0
	}
	tr, ok := obj["textRange"]
	if !ok {
		return 0
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(tr, &inner); err != nil {
		return 0
	}
	raw, ok := inner["startOffset"]
	if !ok {
		return 0
	}
	var v int
	if json.Unmarshal(raw, &v) == nil {
		return v
	}
	return 0
}

// extractNestedField reads obj[outerKey][innerKey] as a string.
func extractNestedField(data json.RawMessage, outerKey, innerKey string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return ""
	}
	nested, ok := obj[outerKey]
	if !ok {
		return ""
	}
	return extractField(nested, innerKey)
}

// ---------------------------------------------------------------------------
// Parsing comments from extract data
// ---------------------------------------------------------------------------

// parseHotspotComments extracts the comment array from a hotspot detail JSON.
// The SonarQube API uses "comment" (singular) as the field name for the array.
func parseHotspotComments(data json.RawMessage) []hotspotComment {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}

	// The field name is "comment" (singular) in the hotspot detail response.
	raw, ok := obj["comment"]
	if !ok {
		// Also try "comments" (plural) in case extract data uses a different format.
		raw, ok = obj["comments"]
		if !ok {
			return nil
		}
	}

	var rawComments []json.RawMessage
	if err := json.Unmarshal(raw, &rawComments); err != nil {
		return nil
	}

	comments := make([]hotspotComment, 0, len(rawComments))
	for _, rc := range rawComments {
		c := hotspotComment{
			Login:     extractField(rc, "login"),
			HTMLText:  extractField(rc, "htmlText"),
			Markdown:  extractField(rc, "markdown"),
			CreatedAt: extractField(rc, "createdAt"),
		}
		if c.HTMLText != "" || c.Markdown != "" {
			comments = append(comments, c)
		}
	}
	return comments
}

// ---------------------------------------------------------------------------
// Compile-time interface assertion to ensure ExtractItem is used correctly.
// ---------------------------------------------------------------------------

var _ = (structure.ExtractItem{}).ServerURL
