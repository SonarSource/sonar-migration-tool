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
	// Every source hotspot needs a target visit now, not only the triaged
	// ones. SonarQube Cloud dropped hotspots on 2026-07-01, so each one lands
	// as an ordinary issue and has to be tagged `sqs-hotspot` to stay
	// identifiable as a former hotspot (#423) — including TO_REVIEW hotspots,
	// which carry no triage at all and were previously filtered out here.
	//
	// hotspotHasManualChanges is retained because the predict pipeline still
	// uses it to report how many hotspots carry migratable triage.
	actionable := make([]matchableHotspot, 0, len(sourceHotspots))
	triaged := 0
	for _, h := range sourceHotspots {
		if hotspotHasManualChanges(h) {
			triaged++
		}
		actionable = append(actionable, h)
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
		"to_sync", len(actionable),
		"carrying_triage", triaged,
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
	// The target counterpart is an ISSUE, not a hotspot: SonarQube Cloud has
	// had no hotspots since 2026-07-01, so /api/hotspots/search can never
	// return the migrated finding. Reuse the issue matcher, which additionally
	// scopes the search server-side by rule — something the hotspot endpoint
	// never accepted (#423).
	candidates, err := findCloudIssueCandidates(ctx, e, cloudKey, orgKey, filePath, src.RuleKey, src.Branch)
	if err != nil {
		logAPIWarn(e.Logger, "syncHotspotMetadata: cloud candidate lookup failed", err,
			"project", cloudKey, "source_key", src.Key, "file", filePath, "branch", src.Branch)
		return syncOutcomeLookupError
	}
	target, outcome := classifyIssueCandidatesByLine(candidates, src.Line)
	switch outcome {
	case syncOutcomeSynced:
		if err := syncOneHotspotAsIssue(ctx, e, src, target, baseURL, sourceKey); err != nil {
			counter.Fail()
			logAPIWarn(e.Logger, "syncHotspotMetadata: hotspot sync failed", err,
				"source_key", src.Key, "cloud_key", target.Key)
		} else {
			counter.Success()
		}
	case syncOutcomeNotFound:
		e.Logger.Debug("syncHotspotMetadata: no cloud counterpart on source line", "source_key", src.Key, "rule", src.RuleKey, "file", filePath, "line", src.Line)
	case syncOutcomeLineMismatch:
		keys := make([]string, 0)
		for _, c := range candidates {
			if c.Line == src.Line {
				keys = append(keys, c.Key)
			}
		}
		e.Logger.Debug("syncHotspotMetadata: multiple cloud counterparts on source line, skipping", "source_key", src.Key, "rule", src.RuleKey, "file", filePath, "line", src.Line, "candidates", keys)
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
// Unlike the issue sync, this runs for EVERY hotspot, including a TO_REVIEW one
// with no triage and no comments, because the tag itself is the deliverable:
// with no hotspot concept left on the target, the tag is the only thing that
// keeps a former hotspot identifiable.
func syncOneHotspotAsIssue(ctx context.Context, e *Executor, src matchableHotspot, target matchableIssue, baseURL, projectKey string) error {
	// 1. Review state. The hotspot's status/resolution maps onto the unified
	// issue-status enum, and the existing issue machinery turns that into a
	// transition — including gating "accept" on the Cloud issue actually
	// offering it (#322).
	//
	// ACKNOWLEDGED no longer has to be demoted to SAFE. That conflation was
	// forced by Cloud's hotspot API having no ACKNOWLEDGED resolution; the
	// issue model expresses it faithfully as ACCEPTED.
	synthetic := matchableIssue{
		Key:         src.Key,
		IssueStatus: scanreport.HotspotIssueStatus(src.Status, src.Resolution),
		Resolution:  src.Resolution,
	}
	var firstErr error
	if syncIssueTransition(ctx, e, target.Key, synthetic, target.Transitions) {
		firstErr = fmt.Errorf("transition to %s failed", synthetic.IssueStatus)
	}

	// 2. Review comments, via the issue comment path.
	if len(src.Comments) > 0 {
		if syncIssueComments(ctx, e, target.Key, hotspotCommentsAsIssueComments(src.Comments), target.Comments) && firstErr == nil {
			firstErr = fmt.Errorf("one or more comments failed")
		}
	}

	// 3. Back-link to the origin (#321). It still points at the source
	// server's security_hotspots view — that is where the finding lives there.
	addHotspotSourceLinkToIssue(ctx, e, target.Key, baseURL, projectKey, src.Key, src.Branch, target.Comments)

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

// addHotspotSourceLinkToIssue posts the "Link to [Original hotspot](…)"
// back-link as a comment on the migrated ISSUE. Best-effort and idempotent.
func addHotspotSourceLinkToIssue(ctx context.Context, e *Executor, cloudKey, baseURL, projectKey, sourceHotspotKey, branch string, cloudComments []issueComment) {
	link := hotspotSourceLinkURL(baseURL, projectKey, sourceHotspotKey, branch)
	if link == "" {
		return
	}
	for _, cc := range cloudComments {
		t := cc.Markdown
		if t == "" {
			t = cc.HTMLText
		}
		if strings.Contains(t, hotspotSourceLinkMarker) {
			return
		}
	}
	text := hotspotSourceLinkMarker + "(" + link + ")"
	if err := e.Cloud.Issues.AddComment(ctx, cloudKey, text); err != nil {
		e.Logger.Warn("syncHotspotMetadata: could not add source-link comment (non-fatal)",
			"issue", cloudKey, "reason", sourceLinkErrSummary(err))
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
