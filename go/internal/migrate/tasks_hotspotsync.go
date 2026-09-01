// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
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

	// Message, Author and VulnerabilityProbability feed the approximate-
	// match scorer (matchscore.go, issue #412) — they play no role in the
	// status / comment sync logic below.
	Message                  string
	Author                   string
	VulnerabilityProbability string
}

// hotspotComment captures a single comment attached to a hotspot.
type hotspotComment struct {
	Login     string
	HTMLText  string
	Markdown  string
	CreatedAt string
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

// hotspotHasUserComment reports whether at least one comment on the
// hotspot was authored by a real user, as opposed to a technical
// comment SonarQube itself might create (#527). A non-empty Login is
// the signal: system/automated comments carry no (or a blank) login.
func hotspotHasUserComment(comments []hotspotComment) bool {
	for _, c := range comments {
		if strings.TrimSpace(c.Login) != "" {
			return true
		}
	}
	return false
}

// HotspotSyncEligibility implements #527's sync-eligibility rule:
//
//   - eligible: the hotspot's status is not TO_REVIEW, OR it carries a
//     user (non-technical) comment. Eligible hotspots get the full
//     state-transition + comment sync.
//   - acknowledged: the hotspot is REVIEWED + ACKNOWLEDGED. This is
//     orthogonal to eligible (an ACKNOWLEDGED hotspot is always
//     "eligible" by the first clause, since its status is REVIEWED) but
//     callers MUST treat it as an override: never apply the state
//     transition, and only sync comments when hasUserComment is true.
//     ACKNOWLEDGED hotspots are always inventoried in the reporting
//     denominator, whether or not they carry a user comment.
//
// A hotspot that is neither eligible nor acknowledged (TO_REVIEW, no
// user comment) is excluded from sync entirely — it is still resolved
// against the target and tagged (#423), but gets no state/comment sync
// and is not counted in the %-synced denominator.
func HotspotSyncEligibility(status, resolution string, hasUserComment bool) (eligible, acknowledged bool) {
	s := strings.ToUpper(strings.TrimSpace(status))
	r := strings.ToUpper(strings.TrimSpace(resolution))
	acknowledged = s == "REVIEWED" && r == "ACKNOWLEDGED"
	eligible = s != "TO_REVIEW" || hasUserComment
	return eligible, acknowledged
}

// hotspotSyncCategory buckets a source hotspot for dispatch and
// reporting (#527).
type hotspotSyncCategory int

const (
	// hotspotCategoryExcluded — TO_REVIEW with no user comment. Still
	// resolved against Cloud and tagged (#423), but gets no
	// transition/comment sync and is not counted in the %-synced
	// denominator.
	hotspotCategoryExcluded hotspotSyncCategory = iota
	// hotspotCategoryAcknowledged — REVIEWED + ACKNOWLEDGED. Always
	// tagged and always counted in the denominator; comment-synced only
	// when it carries a user comment; never state-transitioned.
	hotspotCategoryAcknowledged
	// hotspotCategoryEligible — full sync: state transition + comments.
	hotspotCategoryEligible
)

// classifyHotspotForSync applies HotspotSyncEligibility to a
// matchableHotspot, deriving hasUserComment from its parsed comments.
func classifyHotspotForSync(h matchableHotspot) hotspotSyncCategory {
	eligible, acknowledged := HotspotSyncEligibility(h.Status, h.Resolution, hotspotHasUserComment(h.Comments))
	switch {
	case acknowledged:
		return hotspotCategoryAcknowledged
	case eligible:
		return hotspotCategoryEligible
	default:
		return hotspotCategoryExcluded
	}
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

// ---------------------------------------------------------------------------
// Bulk Cloud issue index (#527)
// ---------------------------------------------------------------------------

// cloudIssueIndexKey mirrors the (file, rule) scope that
// findCloudIssueCandidates used to search per-hotspot.
type cloudIssueIndexKey struct {
	File    string
	RuleKey string
}

// cloudIssueIndex is a read-only-after-build, per-branch cache of Cloud
// issue candidates keyed by (bare file path, rule key). Built once per
// (project, branch) via buildCloudIssueIndex instead of issuing one
// /api/issues/search call per source hotspot — the per-item search was
// the actual cost #527 wants eliminated, since every hotspot still has
// to be resolved and tagged regardless of sync eligibility (#423).
type cloudIssueIndex map[cloudIssueIndexKey][]matchableIssue

// cloudIssueSearchRuleChunkSize caps how many rule keys are joined into
// one `rules=` query parameter per buildCloudIssueIndex request.
// SonarQube doesn't document a hard limit on rules= cardinality, but
// reverse proxies commonly cap the request line around 8KB; 60 keys
// keeps every chunk's encoded query well under that even for long
// "repo:RuleKey" names.
const cloudIssueSearchRuleChunkSize = 60

// buildCloudIssueIndex fetches every Cloud issue whose rule is in
// ruleKeys, scoped to the whole project on the given branch, and
// indexes results by (bare file path, rule key) — the same scope/shape
// findCloudIssueCandidates used per-hotspot, just fetched once for the
// whole branch instead of once per source item (#527).
func buildCloudIssueIndex(ctx context.Context, e *Executor, cloudKey, orgKey, branch string, ruleKeys []string) (cloudIssueIndex, error) {
	idx := make(cloudIssueIndex)
	for _, chunk := range chunkStrings(ruleKeys, cloudIssueSearchRuleChunkSize) {
		params := url.Values{}
		params.Set("componentKeys", cloudKey)
		params.Set("organization", orgKey)
		params.Set("rules", strings.Join(chunk, ","))
		if branch != "" {
			params.Set("branch", branch)
		}
		// Same statuses/fields findCloudIssueCandidates asked for per-item.
		params.Set("issueStatuses", "OPEN,CONFIRMED,FALSE_POSITIVE,ACCEPTED")
		params.Set("additionalFields", "transitions,comments")
		apiIssues, err := e.Cloud.Issues.SearchAll(ctx, params)
		if err != nil {
			return idx, err
		}
		for _, ai := range apiIssues {
			m := apiIssueToMatchable(ai)
			key := cloudIssueIndexKey{File: stripProjectKeyPrefix(m.Component), RuleKey: m.Rule}
			idx[key] = append(idx[key], m)
		}
	}
	return idx, nil
}

// chunkStrings splits ss into slices of at most n elements each.
func chunkStrings(ss []string, n int) [][]string {
	if n <= 0 || len(ss) == 0 {
		return nil
	}
	var out [][]string
	for i := 0; i < len(ss); i += n {
		end := i + n
		if end > len(ss) {
			end = len(ss)
		}
		out = append(out, ss[i:end])
	}
	return out
}

// groupHotspotsByBranch buckets hotspots by their (source) Branch field
// so one buildCloudIssueIndex call is issued per (project, branch) pair
// — /api/issues/search resolves componentKeys against a single branch.
func groupHotspotsByBranch(hotspots []matchableHotspot) map[string][]matchableHotspot {
	out := make(map[string][]matchableHotspot)
	for _, h := range hotspots {
		out[h.Branch] = append(out[h.Branch], h)
	}
	return out
}

// distinctRuleKeys returns the de-duplicated, sorted set of RuleKey
// values across hotspots (sorted for deterministic chunk boundaries).
func distinctRuleKeys(hotspots []matchableHotspot) []string {
	seen := make(map[string]bool)
	var out []string
	for _, h := range hotspots {
		if h.RuleKey == "" || seen[h.RuleKey] {
			continue
		}
		seen[h.RuleKey] = true
		out = append(out, h.RuleKey)
	}
	sort.Strings(out)
	return out
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
			if isFailedMigrateRecord(item) {
				return nil
			}
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

// classifiedHotspot pairs a source hotspot with the sync category
// classifyHotspotForSync assigned it, computed once up front so the
// dispatch loop doesn't re-derive it per goroutine.
type classifiedHotspot struct {
	h   matchableHotspot
	cat hotspotSyncCategory
}

// hotspotCategoryCounts tallies a project's hotspots by sync category,
// for logging and for the #527 %-synced reporting denominator.
type hotspotCategoryCounts struct {
	eligible, ack, excluded, triaged int
}

// classifyAndCountHotspots classifies every hotspot for dispatch and
// tallies the category counts, in one pass. Split out of
// syncProjectHotspots to keep that function's cognitive complexity low.
func classifyAndCountHotspots(all []matchableHotspot) ([]classifiedHotspot, hotspotCategoryCounts) {
	items := make([]classifiedHotspot, 0, len(all))
	var counts hotspotCategoryCounts
	for _, h := range all {
		if hotspotHasManualChanges(h) {
			counts.triaged++
		}
		cat := classifyHotspotForSync(h)
		switch cat {
		case hotspotCategoryEligible:
			counts.eligible++
		case hotspotCategoryAcknowledged:
			counts.ack++
		default:
			counts.excluded++
		}
		items = append(items, classifiedHotspot{h: h, cat: cat})
	}
	return items, counts
}

// buildBranchIndexes builds one cloudIssueIndex per branch present among
// all's hotspots (#527's bulk-fetch, see buildCloudIssueIndex). A branch
// whose index build fails is recorded in the returned failure set rather
// than aborting the whole project — its hotspots are skipped this run.
// Split out of syncProjectHotspots to keep that function's cognitive
// complexity low.
func buildBranchIndexes(ctx context.Context, e *Executor, input syncHotspotInput, all []matchableHotspot) (map[string]cloudIssueIndex, map[string]bool) {
	byBranch := groupHotspotsByBranch(all)
	indexes := make(map[string]cloudIssueIndex, len(byBranch))
	failedBranches := make(map[string]bool, len(byBranch))
	for branch, hs := range byBranch {
		idx, err := buildCloudIssueIndex(ctx, e, input.CloudKey, input.OrgKey, branch, distinctRuleKeys(hs))
		if err != nil {
			logAPIWarn(e.Logger, "syncHotspotMetadata: bulk candidate index build failed", err,
				"project", input.CloudKey, "branch", branch, "rules", len(distinctRuleKeys(hs)))
			failedBranches[branch] = true
			continue
		}
		indexes[branch] = idx
	}
	return indexes, failedBranches
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
	preDedupCount := len(sourceHotspots)
	all := dedupeActionableHotspots(sourceHotspots)
	if dropped := preDedupCount - len(all); dropped > 0 {
		e.Logger.Info("syncHotspotMetadata: deduplicated cross-branch source hotspots",
			"project", input.CloudKey, "before", preDedupCount, "after", len(all), "dropped", dropped)
	}

	// Every source hotspot still needs a target visit: SonarQube Cloud
	// dropped hotspots on 2026-07-01, so each one lands as an ordinary
	// issue and has to be tagged `sqs-hotspot` to stay identifiable as a
	// former hotspot (#423), regardless of whether it needs a state/
	// comment sync. What #527 changes is which hotspots get that
	// state/comment sync applied, and what counts toward the %-synced
	// denominator:
	//
	//   - hotspotCategoryExcluded    (TO_REVIEW, no user comment): tagged
	//     only, not counted in the denominator.
	//   - hotspotCategoryAcknowledged (REVIEWED+ACKNOWLEDGED): tagged
	//     always, comment-synced only with a user comment, never
	//     state-transitioned, always counted in the denominator.
	//   - hotspotCategoryEligible: tagged + fully state/comment synced,
	//     counted in the denominator.
	items, counts := classifyAndCountHotspots(all)
	result.Stats.Actionable = int64(counts.eligible + counts.ack)
	result.Stats.AckDemoted = int64(counts.ack)
	if len(items) == 0 {
		e.Logger.Info("syncHotspotMetadata: no source hotspots to sync", "project", input.CloudKey, "source_total", len(sourceHotspots))
		return result
	}

	// 2. Wait for Cloud indexing — proves the CE task is done. Counted over
	// issues, not hotspots: the imported findings are issues on the target.
	_ = waitForCloudIndexing(ctx, func() (int, error) {
		params := url.Values{}
		params.Set("componentKeys", input.CloudKey)
		params.Set("organization", input.OrgKey)
		return e.Cloud.Issues.Count(ctx, params)
	})

	e.Logger.Info("syncHotspotMetadata: syncing hotspots as issues",
		"project", input.CloudKey,
		"source_total", len(sourceHotspots),
		"eligible", counts.eligible,
		"acknowledged", counts.ack,
		"excluded", counts.excluded,
		"carrying_triage", counts.triaged,
	)

	// 3. Bulk-fetch Cloud issue candidates once per (project, branch) —
	// #527: this replaces the previous one-search-per-hotspot approach,
	// which was the actual cost driver since every hotspot (eligible or
	// not) had to be resolved for tagging (#423). Built as an in-memory
	// index; per-hotspot resolution below is then a zero-network lookup.
	indexes, failedBranches := buildBranchIndexes(ctx, e, input, all)

	// 4. Resolve + dispatch every hotspot from the cached index. Race-
	// safety: items/indexes are read-only from here, each goroutine takes
	// one item by value, stats counters are atomic.
	// Public base URL for back-links — prefer the SQS sonar.core.serverBaseURL
	// setting over the (often localhost) connection URL (#321).
	baseURL := resolveSourceBaseURL(e, input.ServerURL)

	resolveParams := hotspotResolveParams{CloudKey: input.CloudKey, BaseURL: baseURL, SourceKey: input.ServerKey}
	var a, b, c atomic.Int64
	label := "Project key " + input.CloudKey + " hotspot sync:"
	runProjectSyncLoop(ctx, e, items, label, 10,
		func(gctx context.Context, it classifiedHotspot) {
			if failedBranches[it.h.Branch] {
				return
			}
			outcome := resolveAndSyncHotspot(gctx, e, resolveParams, it.h, it.cat, indexes[it.h.Branch], counter)
			if it.cat == hotspotCategoryExcluded {
				return // tagged only; not in the denominator, so not counted as b/c
			}
			switch outcome {
			case syncOutcomeSynced:
				if it.cat == hotspotCategoryEligible {
					a.Add(1)
				}
			case syncOutcomeLineMismatch:
				b.Add(1)
			case syncOutcomeNotFound:
				c.Add(1)
			}
		})
	result.Stats.A = a.Load()
	result.Stats.B = b.Load()
	result.Stats.C = c.Load()
	return result
}

// hotspotResolveParams bundles the per-project constants resolveAndSyncHotspot
// needs alongside each hotspot, keeping the function's parameter count
// within the project's limit — every hotspot in a project shares the same
// CloudKey/BaseURL/SourceKey.
type hotspotResolveParams struct {
	CloudKey  string
	BaseURL   string
	SourceKey string
}

// resolveAndSyncHotspot resolves the source hotspot's target issue from
// the pre-built per-branch cloudIssueIndex (#527) — a plain map lookup,
// no network call — then applies whatever sync the hotspot's category
// calls for. Returns the case a/b/c outcome.
func resolveAndSyncHotspot(ctx context.Context, e *Executor, p hotspotResolveParams, src matchableHotspot, cat hotspotSyncCategory, index cloudIssueIndex, counter *TaskCounter) syncOutcome {
	// Strip "projectKey:" and any trailing "moduleKey:" segments so the bare
	// file path can be used against the index. Multi-module (monorepo)
	// projects add a module key after the project key; SonarCloud has no
	// module layer so only the plain file path matches the cloud component.
	filePath := stripProjectKeyPrefix(src.Component)
	if filePath == "" || src.RuleKey == "" || src.Line <= 0 {
		e.Logger.Debug("syncHotspotMetadata: source hotspot not matchable", "key", src.Key, "rule", src.RuleKey, "component", src.Component, "line", src.Line)
		return syncOutcomeNotFound
	}
	// The target counterpart is an ISSUE, not a hotspot: SonarQube Cloud has
	// had no hotspots since 2026-07-01, so /api/hotspots/search can never
	// return the migrated finding. The index was built from the issue
	// matcher's shape, which additionally scopes by rule — something the
	// hotspot endpoint never accepted (#423).
	candidates := index[cloudIssueIndexKey{File: filePath, RuleKey: src.RuleKey}]
	target, outcome := classifyIssueCandidatesByLine(candidates, src.Line)
	switch outcome {
	case syncOutcomeSynced:
		if err := syncOneHotspotAsIssue(ctx, e, src, target, p.BaseURL, p.SourceKey, cat); err != nil {
			failAPI(counter, e.Logger, "syncHotspotMetadata: hotspot sync failed", err,
				"source_key", src.Key, "cloud_key", target.Key)
		} else {
			counter.Success()
		}
	case syncOutcomeNotFound:
		e.Logger.Debug("syncHotspotMetadata: no cloud counterpart matched", "source_key", src.Key, "rule", src.RuleKey, "file", filePath, "line", src.Line)
	case syncOutcomeLineMismatch:
		keys := make([]string, 0)
		for _, c := range candidates {
			if c.Line == src.Line {
				keys = append(keys, c.Key)
			}
		}
		e.Logger.Debug("syncHotspotMetadata: multiple cloud counterparts matched, skipping", "source_key", src.Key, "rule", src.RuleKey, "file", filePath, "line", src.Line, "candidates", keys)
	}
	return outcome
}

// ---------------------------------------------------------------------------
// Per-hotspot sync
// ---------------------------------------------------------------------------

// syncOneHotspotAsIssue reproduces one source hotspot's review state on its
// migrated Cloud counterpart, which is an ordinary issue (#423).
//
// Order mirrors syncOnePair — transition, comments, back-link, tags — with the
// sqs-hotspot tag applied last so its presence signals that everything before
// it completed.
//
// This runs for EVERY hotspot, including a TO_REVIEW one with no triage and
// no comments, because the back-link and tag are normally always applied:
// with no hotspot concept left on the target, the tag is the only thing that
// keeps a former hotspot identifiable (#423). Which of the transition/
// comment steps actually run depends on cat, per #527's eligibility rules:
//
//   - hotspotCategoryEligible: transition + comments, as before.
//   - hotspotCategoryAcknowledged: never transitioned (the state is not
//     synced by policy, not because Cloud can't represent it); comments
//     synced only if the hotspot carries a user comment.
//   - hotspotCategoryExcluded: neither transition nor comments.
//
// e.FastSync (#527 follow-up) additionally skips the back-link and tag
// steps for hotspotCategoryExcluded hotspots — TO_REVIEW with no user
// comment is "zero user changes" on the source, and back-linking to a
// hotspot nobody will ever review has no audience. It's opt-in (default
// false) because it trades away #423's identifiability guarantee for
// exactly this subset. ACKNOWLEDGED is deliberately excluded from this
// skip: reaching that state is itself a user action, so it's never
// "zero changes" even without a comment.
func syncOneHotspotAsIssue(ctx context.Context, e *Executor, src matchableHotspot, target matchableIssue, baseURL, projectKey string, cat hotspotSyncCategory) error {
	var firstErr error
	skipTagAndLink := e.FastSync && cat == hotspotCategoryExcluded

	// 1. Review state. The hotspot's status/resolution maps onto the unified
	// issue-status enum, and the existing issue machinery turns that into a
	// transition — including gating "accept" on the Cloud issue actually
	// offering it (#322). Skipped for ACKNOWLEDGED and excluded hotspots.
	if cat == hotspotCategoryEligible {
		synthetic := matchableIssue{
			Key:         src.Key,
			IssueStatus: scanreport.HotspotIssueStatus(src.Status, src.Resolution),
			Resolution:  src.Resolution,
		}
		if syncIssueTransition(ctx, e, target.Key, synthetic, target.Transitions) {
			firstErr = fmt.Errorf("transition to %s failed", synthetic.IssueStatus)
		}
	}

	// 2. Review comments, via the issue comment path. Eligible hotspots
	// always sync comments; ACKNOWLEDGED hotspots only when they carry a
	// user comment (#527); excluded hotspots never do.
	syncComments := cat == hotspotCategoryEligible ||
		(cat == hotspotCategoryAcknowledged && hotspotHasUserComment(src.Comments))
	if syncComments && len(src.Comments) > 0 {
		if syncIssueComments(ctx, e, target.Key, hotspotCommentsAsIssueComments(src.Comments), target.Comments) && firstErr == nil {
			firstErr = fmt.Errorf("one or more comments failed")
		}
	}

	if skipTagAndLink {
		return firstErr
	}

	// 3. Back-link to the origin (#321). It still points at the source
	// server's security_hotspots view — that is where the finding lives there.
	addHotspotSourceLinkToIssue(ctx, e, hotspotSourceLinkTarget{
		IssueKey:   target.Key,
		BaseURL:    baseURL,
		ProjectKey: projectKey,
		HotspotKey: src.Key,
		Branch:     src.Branch,
	}, target.Comments)

	// 4. The sqs-hotspot tag (#423).
	if syncHotspotIssueTags(ctx, e, target.Key, target.Tags) && firstErr == nil {
		firstErr = fmt.Errorf("set tags failed")
	}

	return firstErr
}

// hotspotCommentsAsIssueComments adapts hotspot comments to the issue comment
// shape so the issue comment sync can be reused verbatim.
func hotspotCommentsAsIssueComments(in []hotspotComment) []issueComment {
	out := make([]issueComment, 0, len(in))
	for _, c := range in {
		out = append(out, issueComment{
			Login:     c.Login,
			HTMLText:  c.HTMLText,
			Markdown:  c.Markdown,
			CreatedAt: c.CreatedAt,
		})
	}
	return out
}

// syncHotspotIssueTags tags a migrated hotspot with scanreport.HotspotIssueTag
// so it stays identifiable as a former Security Hotspot on a target that no
// longer has the concept (#423).
//
// /api/issues/set_tags replaces the whole tag set, so the Cloud issue's
// existing tags are carried over rather than clobbered. The metadata-sync
// marker is added too, for consistency with the issue sync.
func syncHotspotIssueTags(ctx context.Context, e *Executor, cloudKey string, existingTags []string) bool {
	tags := make([]string, 0, len(existingTags)+2)
	tags = append(tags, existingTags...)
	for _, t := range []string{scanreport.HotspotIssueTag, metadataSyncTag} {
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	if err := e.Cloud.Issues.SetTags(ctx, cloudKey, tags); err != nil {
		logAPIWarn(e.Logger, "syncHotspotMetadata: set tags failed", err, "issue", cloudKey)
		return true
	}
	return false
}

// hotspotSourceLinkTarget identifies where a hotspot back-link should be
// posted and what it should point at.
type hotspotSourceLinkTarget struct {
	IssueKey   string // Cloud issue the hotspot migrated into
	BaseURL    string // public base URL of the source server
	ProjectKey string // source project key
	HotspotKey string // source hotspot key
	Branch     string // source branch, if any
}

// addHotspotSourceLinkToIssue posts the "Link to [Original hotspot](…)"
// back-link as a comment on the migrated ISSUE. Best-effort and idempotent.
func addHotspotSourceLinkToIssue(ctx context.Context, e *Executor, tgt hotspotSourceLinkTarget, cloudComments []issueComment) {
	link := hotspotSourceLinkURL(tgt.BaseURL, tgt.ProjectKey, tgt.HotspotKey, tgt.Branch)
	if link == "" {
		return
	}
	if issueCommentsContain(cloudComments, hotspotSourceLinkMarker) {
		return
	}
	text := hotspotSourceLinkMarker + "(" + link + ")"
	if err := e.Cloud.Issues.AddComment(ctx, tgt.IssueKey, text); err != nil {
		e.Logger.Warn("syncHotspotMetadata: could not add source-link comment (non-fatal)",
			"issue", tgt.IssueKey, "reason", sourceLinkErrSummary(err))
	}
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

// ---------------------------------------------------------------------------
// Loading source hotspots from extract data
// ---------------------------------------------------------------------------

// loadMatchableHotspots reads the getProjectHotspotsFull extract items and
// converts them into normalised matchableHotspot structs.
func loadMatchableHotspots(e *Executor, serverURL, serverKey string) ([]matchableHotspot, error) {
	// Project-scoped across every branch. Streaming matters here: this runs
	// once per project at concurrency 25, and syncHotspotMetadata shares a
	// phase with syncIssueMetadata, so both corpora were resident at once.
	scope := extractScope{ServerURL: serverURL, ProjectKey: serverKey}

	var hotspots []matchableHotspot
	for item := range scopedHotspotItems(e, scope) {
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
		Branch:                   extractField(data, "branch"),
		Message:                  extractField(data, "message"),
		Author:                   extractField(data, "author"),
		VulnerabilityProbability: extractField(data, "vulnerabilityProbability"),
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
