package extract

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// sqs10 is the boundary at which several SonarQube API parameter names
// changed (e.g. /api/hotspots/search's project filter). Versions strictly
// below it require the legacy name.
var sqs10 = common.MustParseVersion("10")

var (
	issueTypes      = []string{"CODE_SMELL", "BUG", "VULNERABILITY"}
	issueSeverities = []string{"INFO", "MINOR", "MAJOR", "CRITICAL", "BLOCKER"}
	resolutions     = []string{"FALSE-POSITIVE", "WONTFIX", "FIXED", "REMOVED"}
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
		{Name: "getProjectFixedIssueTypes", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: makeExpandedIssueTask("getProjectFixedIssueTypes", resolutions, nil)},
		{Name: "getProjectRecentIssueTypes", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: makeExpandedIssueTask("getProjectRecentIssueTypes", nil, url.Values{"createdInLast": {"30d"}})},
		{Name: "getSafeHotspots", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: safeHotspotsTask()},
		{Name: "getPluginIssues", Editions: AllEditions, Dependencies: []string{"getPluginRules"},
			Run: pluginIssuesTask()},
		{Name: "getProjectPluginIssues", Editions: AllEditions,
			Dependencies: []string{"getProjects", "getPluginRules"},
			Run:          makeJoinedRuleIssueTask("getProjectPluginIssues", "getPluginRules")},
		{Name: "getProjectTemplateIssues", Editions: AllEditions,
			Dependencies: []string{"getProjects", "getTemplateRules"},
			Run:          makeJoinedRuleIssueTask("getProjectTemplateIssues", "getTemplateRules")},
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

func pluginIssuesTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getPluginIssues", "getPluginRules",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				ruleKey := extractField(item, "key")
				raw, err := e.Raw.Get(ctx, issuesSearchAPI, url.Values{"rules": {ruleKey}})
				if err != nil {
					return err
				}
				return w.WriteOne(EnrichRaw(raw, map[string]any{"serverUrl": e.ServerURL}))
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
		params := url.Values{"components": {projectKey}, "ps": {"1"}}
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

// makeJoinedRuleIssueTask joins rule keys with commas and queries issues per project x chunked rules.
func makeJoinedRuleIssueTask(taskName, ruleDepTask string) func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		ruleKeys, err := collectRuleKeys(e, ruleDepTask)
		if err != nil {
			return err
		}
		chunks := chunkStrings(ruleKeys, 30)
		return forEachDep(ctx, e, taskName, "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				meta := map[string]any{"projectKey": key, "serverUrl": e.ServerURL}
				return fetchChunkedRuleIssues(ctx, e, w, key, chunks, meta)
			})
	}
}

func collectRuleKeys(e *Executor, ruleDepTask string) ([]string, error) {
	ruleItems, err := e.Store.ReadAll(ruleDepTask)
	if err != nil {
		return nil, err
	}
	var ruleKeys []string
	for _, item := range ruleItems {
		if k := extractField(item, "key"); k != "" {
			ruleKeys = append(ruleKeys, k)
		}
	}
	return ruleKeys, nil
}

func fetchChunkedRuleIssues(ctx context.Context, e *Executor, w *ChunkWriter,
	projectKey string, chunks [][]string, meta map[string]any) error {
	for _, chunk := range chunks {
		raw, err := e.Raw.Get(ctx, issuesSearchAPI,
			url.Values{"components": {projectKey}, "rules": {strings.Join(chunk, ",")}, "ps": {"1"}})
		if err != nil {
			return err
		}
		if err := w.WriteOne(EnrichRaw(raw, meta)); err != nil {
			return err
		}
	}
	return nil
}

func chunkStrings(items []string, size int) [][]string {
	var chunks [][]string
	for i := 0; i < len(items); i += size {
		end := min(i+size, len(items))
		chunks = append(chunks, items[i:end])
	}
	return chunks
}
