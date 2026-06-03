package extract

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// ceTaskTypes is the set of Compute Engine task types to query.
var ceTaskTypes = []string{
	"REPORT", "ISSUE_SYNC", "AUDIT_PURGE", "PROJECT_EXPORT",
	"APP_REFRESH", "PROJECT_IMPORT", "VIEW_REFRESH", "REPORT_SUBMIT",
	"GITHUB_AUTH_PROVISIONING", "GITHUB_PROJECT_PERMISSIONS_PROVISIONING",
	"GITLAB_AUTH_PROVISIONING", "GITLAB_PROJECT_PERMISSIONS_PROVISIONING",
}

// projectCETaskTypes is the subset used for per-project CE queries.
var projectCETaskTypes = []string{"REPORT", "ISSUE_SYNC", "PROJECT_EXPORT"}

func miscTasks() []TaskDef {
	return []TaskDef{
		{
			Name: "getTasks", Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchCETasks(ctx, e, "getTasks", ceTaskTypes, nil)
			},
		},
		{
			Name: "getProjectAnalyses", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: func(ctx context.Context, e *Executor) error {
				return forEachProjectCE(ctx, e, "getProjectAnalyses",
					"api/project_analyses/search", "analyses", "project", 500)
			},
		},
		{
			Name: "getProjectTasks", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: func(ctx context.Context, e *Executor) error {
				return forEachProjectCE(ctx, e, "getProjectTasks",
					"api/ce/activity", "tasks", "component", 1000)
			},
		},
		{Name: "getNewCodePeriods", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: perProjectArray("getNewCodePeriods", "api/new_code_periods/list", "newCodePeriods", "project", "project")},
		{
			// SonarQube Server's platform-wide new-code-period default —
			// /api/new_code_periods/show with neither project nor branch
			// returns the global setting. The migrate phase propagates
			// it to every SonarQube Cloud org so SQC orgs inherit the
			// same default (issue #136).
			Name: "getGlobalNewCodePeriod", Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWriteSingle(ctx, e, "getGlobalNewCodePeriod",
					"api/new_code_periods/show", nil, "",
					map[string]any{"serverUrl": e.ServerURL})
			},
		},
	}
}

// fetchCETasks fetches CE tasks globally for each task type.
func fetchCETasks(ctx context.Context, e *Executor, taskName string, taskTypes []string, extraParams url.Values) error {
	minDate := daysAgo(30)
	w, err := e.Store.Writer(taskName)
	if err != nil {
		return err
	}
	for _, taskType := range taskTypes {
		if err := acquireSem(ctx, e.Sem); err != nil {
			return err
		}
		items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
			Path: "api/ce/activity", ResultKey: "tasks", MaxPageSize: 1000,
			Params: mergeParams(url.Values{"type": {taskType}, "minSubmittedAt": {minDate}}, extraParams),
		})
		<-e.Sem
		if err != nil {
			// Older SonarQube Server versions (e.g. 9.9) don't recognise CE
			// task types that arrived in 10.x like GITHUB_AUTH_PROVISIONING
			// and reject the request with HTTP 400 listing the supported
			// types. Skip that type rather than aborting the whole task
			// (issue #278).
			if common.IsHTTPError(err, 400) {
				e.Logger.Warn(taskName+" skipped task type", "type", taskType, "err", err)
				continue
			}
			return err
		}
		if err := w.WriteChunk(enrichAll(items, map[string]any{"serverUrl": e.ServerURL})); err != nil {
			return err
		}
	}
	return nil
}

// forEachProjectCE runs a per-project CE/analysis query across task types.
func forEachProjectCE(ctx context.Context, e *Executor, taskName, path, resultKey, paramKey string, maxPageSize int) error {
	minDate := daysAgo(30)
	return forEachDep(ctx, e, taskName, "getProjects",
		func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
			key := extractField(item, "key")
			var allItems []json.RawMessage
			for _, taskType := range projectCETaskTypes {
				items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
					Path: path, ResultKey: resultKey, MaxPageSize: maxPageSize,
					Params: url.Values{paramKey: {key}, "type": {taskType}, "minSubmittedAt": {minDate}},
				})
				if err != nil {
					if isNonFatalHTTPErr(err) {
						e.Logger.Warn(taskName+" skipped", "project", key, "err", err)
						e.RecordSkipped(key)
						break
					}
					return err
				}
				allItems = append(allItems, items...)
			}
			return w.WriteChunk(enrichAll(allItems, map[string]any{"serverUrl": e.ServerURL}))
		})
}

func mergeParams(base, extra url.Values) url.Values {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

func daysAgo(days int) string {
	return time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02T15:04:05-0700")
}
