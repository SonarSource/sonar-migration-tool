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
	"time"

	sqapi "github.com/sonar-solutions/sq-api-go"
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
type matchableIssue struct {
	Key        string
	Rule       string
	Component  string
	Line       int
	Status     string
	Resolution string
	Tags       []string
	Comments   []issueComment
	Assignee   string
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

// buildIssueMatchKey produces a canonical key for matching issues across
// environments. The component field from SonarQube includes the project
// key as a prefix ("myproject:src/Foo.java"); we strip it so only the
// relative file path remains. The key format is "rule|filePath|line".
//
// Returns "" if the issue lacks a rule, component, or has a non-positive line.
func buildIssueMatchKey(iss matchableIssue) string {
	if iss.Rule == "" || iss.Component == "" || iss.Line <= 0 {
		return ""
	}
	// Strip "projectKey:" prefix from component.
	filePath := iss.Component
	if idx := strings.Index(filePath, ":"); idx >= 0 {
		filePath = filePath[idx+1:]
	}
	return fmt.Sprintf("%s|%s|%d", iss.Rule, filePath, iss.Line)
}

// matchIssues performs FIFO matching: for every source issue, take the first
// Cloud candidate with the same match key. Each Cloud issue is consumed at
// most once, preventing one-to-many duplication.
//
// The candidate map is built from cloudIssues (key -> []matchableIssue).
// Source issues are iterated in order; the first available candidate for
// each key is popped from the front of the slice (FIFO).
//
// All data structures are fully built before this function returns; no
// mutation occurs during any subsequent concurrent phase.
func matchIssues(sourceIssues, cloudIssues []matchableIssue) []issuePair {
	// Build candidate map: matchKey -> ordered slice of cloud issues.
	candidates := make(map[string][]matchableIssue, len(cloudIssues))
	for _, ci := range cloudIssues {
		k := buildIssueMatchKey(ci)
		if k == "" {
			continue
		}
		candidates[k] = append(candidates[k], ci)
	}

	// FIFO consume: iterate source issues and take the first available
	// cloud candidate for each match key.
	var pairs []issuePair
	for _, si := range sourceIssues {
		k := buildIssueMatchKey(si)
		if k == "" {
			continue
		}
		bucket := candidates[k]
		if len(bucket) == 0 {
			continue
		}
		// Pop the first candidate (FIFO).
		pairs = append(pairs, issuePair{source: si, cloud: bucket[0]})
		candidates[k] = bucket[1:]
	}
	return pairs
}

// ---------------------------------------------------------------------------
// Transition logic
// ---------------------------------------------------------------------------

// getFallbackTransition maps an SQS issue's resolution and status to the
// Cloud transition name required to move the Cloud issue into an equivalent
// state.
//
// Resolution takes priority (it is the most specific signal). When the
// resolution is empty or unrecognised, we fall back to the status field.
//
// Returns "" when no transition is needed (e.g. OPEN issues).
func getFallbackTransition(resolution, status string) string {
	// Resolution-based priority — most specific.
	switch strings.ToUpper(resolution) {
	case "FALSE-POSITIVE":
		return "falsepositive"
	case "WONTFIX":
		return "wontfix"
	}

	// Status-based fallback.
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
		return "wontfix"
	case "FALSE_POSITIVE":
		return "falsepositive"
	case "IN_SANDBOX":
		return ""
	default:
		return ""
	}
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
// (OPEN, no comments, no tags, no assignee) are skipped to avoid
// unnecessary API calls.
func hasManualChanges(iss matchableIssue) bool {
	// Non-migrated comments (we consider all source comments relevant).
	if len(iss.Comments) > 0 {
		return true
	}
	// Tags.
	if len(iss.Tags) > 0 {
		return true
	}
	// Assignee.
	if iss.Assignee != "" {
		return true
	}
	// Non-OPEN status.
	if strings.ToUpper(iss.Status) != "OPEN" {
		return true
	}
	return false
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
func runSyncIssueMetadata(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "syncIssueMetadata", "createProjects",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			cloudKey := extractField(item, "cloud_project_key")
			orgKey := extractField(item, "sonarcloud_org_key")
			serverURL := extractField(item, "server_url")
			serverKey := extractField(item, "key")
			if cloudKey == "" || orgKey == "" {
				return nil
			}
			return syncProjectIssues(ctx, e, cloudKey, orgKey, serverURL, serverKey, counter)
		})
	return err
}

// ---------------------------------------------------------------------------
// Per-project sync
// ---------------------------------------------------------------------------

// syncProjectIssues handles the full issue-metadata sync for a single project:
//
//  1. Load source issues from extract
//  2. Wait for Cloud indexing
//  3. Fetch Cloud issues
//  4. Match (FIFO)
//  5. Pre-filter (hasManualChanges)
//  6. Sync pairs with bounded concurrency
//  7. Log summary
func syncProjectIssues(ctx context.Context, e *Executor, cloudKey, orgKey, serverURL, serverKey string, counter *TaskCounter) error {
	// 1. Load source issues from extract data.
	sourceIssues := loadMatchableIssues(e, serverURL, serverKey)
	if len(sourceIssues) == 0 {
		e.Logger.Debug("syncIssueMetadata: no source issues", "project", cloudKey)
		return nil
	}

	// 2. Wait for Cloud indexing to complete before fetching.
	err := waitForCloudIndexing(ctx, func() (int, error) {
		params := url.Values{}
		params.Set("componentKeys", cloudKey)
		params.Set("organization", orgKey)
		return e.Cloud.Issues.Count(ctx, params)
	})
	if err != nil {
		logAPIWarn(e.Logger, "syncIssueMetadata: indexing wait failed", err, "project", cloudKey)
		return nil // non-fatal
	}

	// 3. Fetch all Cloud issues for the project.
	cloudIssues, err := loadCloudMatchableIssues(ctx, e, cloudKey, orgKey)
	if err != nil {
		logAPIWarn(e.Logger, "syncIssueMetadata: fetch cloud issues failed", err, "project", cloudKey)
		return nil // non-fatal — skip project
	}
	if len(cloudIssues) == 0 {
		// Source had issues worth syncing, but Cloud has nothing to
		// match against. Most common cause: the project-data CE task
		// for this project failed (or was skipped), so the report was
		// never indexed and Cloud has no issues yet. Promote to INFO
		// so the operator can correlate the skip with the upstream
		// failure log instead of wondering why nothing happened. #299.
		e.Logger.Info("syncIssueMetadata: skipping project — no Cloud issues to match (project-data CE task likely failed or was skipped)",
			"project", cloudKey, "source_issues", len(sourceIssues))
		return nil
	}

	// 4. Match issues (FIFO). Built entirely before launching goroutines.
	matchedPairs := matchIssues(sourceIssues, cloudIssues)
	if len(matchedPairs) == 0 {
		e.Logger.Debug("syncIssueMetadata: no matched pairs", "project", cloudKey)
		return nil
	}

	// 5. Pre-filter: only sync pairs where the source has manual changes.
	var actionable []issuePair
	for _, p := range matchedPairs {
		if hasManualChanges(p.source) {
			actionable = append(actionable, p)
		}
	}
	if len(actionable) == 0 {
		e.Logger.Debug("syncIssueMetadata: no actionable pairs after filter", "project", cloudKey)
		return nil
	}

	e.Logger.Info("syncIssueMetadata: syncing pairs",
		"project", cloudKey,
		"matched", len(matchedPairs),
		"actionable", len(actionable),
	)

	// 6. Sync pairs with bounded concurrency, emitting a per-
	// project "Issue sync: N/M - X%" line every 20 completions
	// (#300). The shared runProjectSyncLoop handles the errgroup,
	// the semaphore bound, and the progress logger.
	//
	// RACE-CONDITION SAFETY:
	//   - actionable slice is read-only during this phase.
	//   - Each goroutine receives exactly ONE issuePair by value.
	//   - counter uses atomic operations (existing pattern).
	//   - No shared mutable state is accessed.
	runProjectSyncLoop(ctx, e, actionable, "Issue sync:", 20,
		func(gctx context.Context, pair issuePair) {
			syncOnePair(gctx, e, pair, counter)
		})

	return nil
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
	transFailed := syncIssueTransition(ctx, e, cloudKey, pair.source)
	commentFailed := syncIssueComments(ctx, e, cloudKey, pair.source.Comments, pair.cloud.Comments)
	tagsFailed := syncIssueTags(ctx, e, cloudKey, pair.source.Tags)

	if transFailed || commentFailed || tagsFailed {
		counter.Fail()
	} else {
		counter.Success()
	}
}

// syncIssueTransition applies the fallback status transition on the Cloud issue.
// Returns true if the transition failed with an unexpected error.
func syncIssueTransition(ctx context.Context, e *Executor, cloudKey string, src matchableIssue) bool {
	transition := getFallbackTransition(src.Resolution, src.Status)
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

// loadMatchableIssues reads the extracted SQS issues for a project and
// converts them to matchableIssue values. Issues with CLOSED status or
// FIXED resolution are excluded because they have no Cloud counterpart
// (the scan report does not reproduce them).
func loadMatchableIssues(e *Executor, serverURL, serverKey string) []matchableIssue {
	items, err := readExtractItems(e, "getProjectIssuesFull")
	if err != nil {
		return nil
	}

	var issues []matchableIssue
	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}
		if extractField(item.Data, "projectKey") != serverKey {
			continue
		}

		status := strings.ToUpper(extractField(item.Data, "status"))
		resolution := strings.ToUpper(extractField(item.Data, "resolution"))

		// Exclude CLOSED and FIXED — these won't exist in Cloud.
		if status == "CLOSED" {
			continue
		}
		if resolution == "FIXED" {
			continue
		}

		line := int(extractInt32Field(item.Data, "line"))

		comments := parseIssueComments(item.Data)
		tags := extractStringArray(item.Data, "tags")

		issues = append(issues, matchableIssue{
			Key:        extractField(item.Data, "key"),
			Rule:       extractField(item.Data, "rule"),
			Component:  extractField(item.Data, "component"),
			Line:       line,
			Status:     extractField(item.Data, "status"),
			Resolution: extractField(item.Data, "resolution"),
			Tags:       tags,
			Comments:   comments,
			Assignee:   extractField(item.Data, "assignee"),
		})
	}
	return issues
}

// loadCloudMatchableIssues fetches all issues from Cloud for a given
// project and converts them to matchableIssue values.
func loadCloudMatchableIssues(ctx context.Context, e *Executor, cloudKey, orgKey string) ([]matchableIssue, error) {
	params := url.Values{}
	params.Set("componentKeys", cloudKey)
	params.Set("organization", orgKey)
	// Fetch all statuses to enable accurate matching.
	params.Set("statuses", "OPEN,CONFIRMED,REOPENED,RESOLVED,CLOSED")

	apiIssues, err := e.Cloud.Issues.SearchAll(ctx, params)
	if err != nil {
		return nil, err
	}

	issues := make([]matchableIssue, 0, len(apiIssues))
	for _, ai := range apiIssues {
		comments := make([]issueComment, 0, len(ai.Comments))
		for _, c := range ai.Comments {
			comments = append(comments, issueComment{
				Login:     c.Login,
				HTMLText:  c.HTMLText,
				Markdown:  c.Markdown,
				CreatedAt: c.CreatedAt,
			})
		}
		issues = append(issues, matchableIssue{
			Key:        ai.Key,
			Rule:       ai.Rule,
			Component:  ai.Component,
			Line:       ai.Line,
			Status:     ai.Status,
			Resolution: ai.Resolution,
			Tags:       ai.Tags,
			Comments:   comments,
			Assignee:   ai.Assignee,
		})
	}
	return issues, nil
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
