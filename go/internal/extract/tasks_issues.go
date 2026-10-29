// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// sqs10 is the boundary at which several SonarQube API parameter names
// changed (e.g. /api/hotspots/search's project filter). Versions strictly
// below it require the legacy name.
var sqs10 = common.MustParseVersion("10")

var (
	issueTypes      = []string{"CODE_SMELL", "BUG", "VULNERABILITY"}
	issueSeverities = []string{"INFO", "MINOR", "MAJOR", "CRITICAL", "BLOCKER"}
)

// hotspotsProjectParam returns the parameter name /api/hotspots/search
// expects for the project filter. SonarQube Server 9.9 requires
// "projectKey"; 10.x and later renamed it to "project". Issue #278.
func hotspotsProjectParam(version common.Version) string {
	if version.Less(sqs10) {
		return "projectKey"
	}
	return "project"
}

func issueTasks() []TaskDef {
	return []TaskDef{
		{Name: "getProjectIssues", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: perProjectIssueCount("getProjectIssues", nil)},
		{Name: "getAcceptedIssues", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: perProjectIssueCount("getAcceptedIssues", url.Values{"issueStatuses": {"ACCEPTED,CONFIRMED"}})},
		{Name: "getProjectIssueTypes", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: makeExpandedIssueTask("getProjectIssueTypes", nil, nil)},
		{Name: "getProjectRecentIssueTypes", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: makeExpandedIssueTask("getProjectRecentIssueTypes", nil, url.Values{"createdInLast": {"30d"}})},
		{Name: "getSafeHotspots", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: safeHotspotsTask()},
	}
}

func safeHotspotsTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getSafeHotspots", "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				raw, err := e.Raw.Get(ctx, "api/hotspots/search",
					url.Values{hotspotsProjectParam(e.Version): {key}, "status": {"REVIEWED"}, "resolution": {"SAFE"}, "ps": {"1"}})
				if err != nil {
					return err
				}
				return w.WriteOne(EnrichRaw(raw, map[string]any{"projectKey": key, "serverUrl": e.ServerURL}))
			})
	}
}

// makeExpandedIssueTask creates a task with cross-product of types x severities (and optionally resolutions).
func makeExpandedIssueTask(taskName string, resolutionValues []string, extraParams url.Values) func(ctx context.Context, e *Executor) error {
	combos := buildIssueCombos(resolutionValues)
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, taskName, "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				meta := map[string]any{"projectKey": key, "serverUrl": e.ServerURL}
				return fetchIssueCombos(ctx, e, w, key, combos, extraParams, meta)
			})
	}
}

func fetchIssueCombos(ctx context.Context, e *Executor, w *ChunkWriter, projectKey string,
	combos []map[string]string, extraParams url.Values, meta map[string]any) error {
	for _, combo := range combos {
		params := url.Values{"componentKeys": {projectKey}, "ps": {"1"}}
		for k, v := range combo {
			params.Set(k, v)
		}
		for k, v := range extraParams {
			params[k] = v
		}
		raw, err := e.Raw.Get(ctx, issuesSearchAPI, params)
		if err != nil {
			return err
		}
		if err := w.WriteOne(EnrichRaw(raw, meta)); err != nil {
			return err
		}
	}
	return nil
}

func buildIssueCombos(resolutionValues []string) []map[string]string {
	expansions := []Expansion{
		{Key: "types", Values: issueTypes},
		{Key: "severities", Values: issueSeverities},
	}
	if len(resolutionValues) > 0 {
		expansions = append([]Expansion{{Key: "resolutions", Values: resolutionValues}}, expansions...)
	}
	return expandCombinations(expansions)
}
