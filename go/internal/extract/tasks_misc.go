// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// ceTaskTypes is the maximal set of Compute Engine task types to query —
// not every edition/version of SonarQube Server supports all of them
// (issue #533). fetchCETasks discovers and adapts to the server's real
// supported set from the first HTTP 400 it gets.
var ceTaskTypes = []string{
	"REPORT", "ISSUE_SYNC", "AUDIT_PURGE", "PROJECT_EXPORT",
	"APP_REFRESH", "SCA_RESCAN_BRANCH", "PROJECT_IMPORT", "VIEW_REFRESH", "REPORT_SUBMIT",
	"GITHUB_AUTH_PROVISIONING", "GITHUB_PROJECT_PERMISSIONS_PROVISIONING",
	"GITLAB_AUTH_PROVISIONING", "GITLAB_PROJECT_PERMISSIONS_PROVISIONING",
}

// validTaskTypesRe matches the supported-type list SonarQube Server
// reports in a 400 response when `type` isn't one of the types it
// recognises, e.g. "Value of parameter 'type' (X) must be one of:
// [A, B, C]".
var validTaskTypesRe = regexp.MustCompile(`must be one of:\s*\[([^\]]*)\]`)

// parseValidCETaskTypes extracts the server-reported list of valid CE
// task types from a 400 response's error message. ok is false when the
// message doesn't match the expected "must be one of: [...]" shape, so
// callers can fall back to the pre-#533 per-type warning behavior.
func parseValidCETaskTypes(msg string) (types []string, ok bool) {
	m := validTaskTypesRe.FindStringSubmatch(msg)
	if m == nil {
		return nil, false
	}
	for _, t := range strings.Split(m[1], ",") {
		if t = strings.TrimSpace(t); t != "" {
			types = append(types, t)
		}
	}
	return types, len(types) > 0
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
//
// Older SonarQube Server versions (e.g. 9.9) don't recognise CE task
// types that arrived in 10.x like GITHUB_AUTH_PROVISIONING and reject the
// request with HTTP 400 listing the types they do support. Skipping that
// type rather than aborting the whole task (issue #278) used to log one
// WARN per unsupported type — noisy on a server missing several of them.
// Since the 400's error message names the server's real supported set,
// the first such error narrows taskTypes down to it: every later type not
// in that set is skipped with no request and no log line at all, and only
// the very first occurrence logs (at INFO, not WARN — it's expected, not
// a problem) (issue #533).
func fetchCETasks(ctx context.Context, e *Executor, taskName string, taskTypes []string, extraParams url.Values) error {
	minDate := daysAgo(30)
	w, err := e.Store.Writer(taskName)
	if err != nil {
		return err
	}
	var allowedTypes map[string]bool
	var loggedFirstUnsupported bool
	for _, taskType := range taskTypes {
		if allowedTypes != nil && !allowedTypes[taskType] {
			continue
		}
		if err := acquireSem(ctx, e.Sem); err != nil {
			return err
		}
		items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
			Path: "api/ce/activity", ResultKey: "tasks", MaxPageSize: 1000,
			Params: mergeParams(url.Values{"type": {taskType}, "minSubmittedAt": {minDate}}, extraParams),
		})
		<-e.Sem
		if err != nil {
			if !common.IsHTTPError(err, 400) {
				return err
			}
			if allowedTypes == nil {
				allowedTypes = allowedCETaskTypesFromError(err)
			}
			logUnsupportedCETaskType(e, taskName, taskType, err, &loggedFirstUnsupported)
			continue
		}
		if err := w.WriteChunk(enrichAll(items, map[string]any{"serverUrl": e.ServerURL})); err != nil {
			return err
		}
	}
	return nil
}

// allowedCETaskTypesFromError extracts the server's real supported CE
// task types from a 400 response's error message (issue #533). Returns
// nil when the error isn't an *common.HTTPError or its message doesn't
// match the expected "must be one of: [...]" shape — callers then fall
// back to the pre-#533 per-type warning behavior.
func allowedCETaskTypesFromError(err error) map[string]bool {
	var httpErr *common.HTTPError
	if !errors.As(err, &httpErr) {
		return nil
	}
	valid, ok := parseValidCETaskTypes(httpErr.Message())
	if !ok {
		return nil
	}
	allowed := make(map[string]bool, len(valid))
	for _, v := range valid {
		allowed[v] = true
	}
	return allowed
}

// logUnsupportedCETaskType logs a "skipped task type" message: INFO for
// the first occurrence this run (expected on many servers, not a
// problem), WARN for any later occurrence — only reachable when the
// error message didn't parse and fetchCETasks falls back to warning on
// every unsupported type (issue #533).
func logUnsupportedCETaskType(e *Executor, taskName, taskType string, err error, loggedFirst *bool) {
	if !*loggedFirst {
		e.Logger.Info(taskName+" skipped task type", "type", taskType, "err", err)
		*loggedFirst = true
		return
	}
	e.Logger.Warn(taskName+" skipped task type", "type", taskType, "err", err)
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
