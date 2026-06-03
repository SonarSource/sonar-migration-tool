package extract

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// acceptedIssuesRename is the SonarQube Server release that renamed
// wont_fix_issues → accepted_issues.
var acceptedIssuesRename = common.MustParseVersion("10.2")

// measureMetricKeysFor returns the comma-separated metric keys requested
// from /api/measures/search for each project. The "accepted" issue metric
// was renamed in SonarQube Server 10.2: prior versions exposed it as
// "wont_fix_issues", 10.2+ uses "accepted_issues". Issue #278.
func measureMetricKeysFor(version common.Version) string {
	acceptedMetric := "accepted_issues"
	if version.Less(acceptedIssuesRename) {
		acceptedMetric = "wont_fix_issues"
	}
	return "ncloc_language_distribution,new_violations," + acceptedMetric +
		",alert_status,false_positive_issues,violations," +
		"new_lines_to_cover,lines_to_cover,new_coverage,coverage," +
		"uncovered_lines,new_uncovered_lines"
}

// projectPermissions is the set of permissions expanded for getProjectGroupsPermissions.
var projectPermissions = []string{"admin", "codeviewer", "issueadmin", "securityhotspotadmin", "scan", "user"}

func projectTasks() []TaskDef {
	return []TaskDef{
		{
			Name:     "getProjects",
			Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				opts := PaginatedOpts{Path: "api/projects/search", ResultKey: "components"}
				if len(e.ProjectKeys) > 0 {
					opts.Params = url.Values{"projects": {strings.Join(e.ProjectKeys, ",")}}
				}
				return fetchAndWritePaginated(ctx, e, "getProjects", opts,
					map[string]any{"serverUrl": e.ServerURL})
			},
		},
		{
			Name:         "getProjectTags",
			Editions:     AllEditions,
			Dependencies: []string{"getProjects"},
			Run:          projectTagsTask(),
		},
		{Name: "getProjectDetails", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: perProjectSingle("getProjectDetails", "api/navigation/component", "component")},
		{Name: "getProjectSettings", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: projectSettingsTask()},
		{Name: "getProjectLinks", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: perProjectArray("getProjectLinks", "api/project_links/search", "links", "projectKey", "projectKey")},
		{Name: "getProjectMeasures", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: projectMeasuresTask()},
		{Name: "getProjectWebhooks", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: perProjectArray("getProjectWebhooks", "api/webhooks/list", "webhooks", "project", "projectKey")},
		{Name: "getProjectBindings", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: projectBindingsTask()},
		{Name: "getProjectGroupsPermissions", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: projectGroupsPermissionsTask()},
		{Name: "getProjectUsersScanners", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: perProjectPermissionUsers("getProjectUsersScanners", "scan")},
		{Name: "getProjectUsersViewers", Editions: AllEditions, Dependencies: []string{"getProjects"},
			Run: perProjectPermissionUsers("getProjectUsersViewers", "user")},
	}
}

// projectTagsTask fetches every project's tags via
// GET /api/components/show?component=<key> and writes one record per
// project with {projectKey, tags, serverUrl}.
//
// Why per-project instead of a single bulk call: on some SonarQube Server
// versions /api/projects/search returns tags=null even with f=tags, while
// /api/components/show reliably exposes the project's tags array. Projects
// with no tags are skipped to keep the output lean.
func projectTagsTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getProjectTags", "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				if key == "" {
					return nil
				}
				raw, err := e.Raw.Get(ctx, "api/components/show",
					url.Values{"component": {key}})
				if err != nil {
					return nil // best-effort — don't fail the whole extract on one project
				}
				comp := extractSubKey(raw, "component")
				tags := extractTagsArray(comp)
				if len(tags) == 0 {
					return nil
				}
				rec, err := json.Marshal(map[string]any{
					"projectKey": key,
					"tags":       tags,
					"serverUrl":  e.ServerURL,
				})
				if err != nil {
					return nil
				}
				return w.WriteOne(rec)
			})
	}
}

// extractTagsArray pulls a non-empty "tags" string array out of a JSON
// record, tolerating absent or non-array shapes.
func extractTagsArray(raw json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	arrRaw, ok := obj["tags"]
	if !ok {
		return nil
	}
	var arr []string
	if json.Unmarshal(arrRaw, &arr) != nil {
		return nil
	}
	return arr
}

func projectSettingsTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getProjectSettings", "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				items, err := e.Raw.GetArray(ctx, "api/settings/values", "settings",
					url.Values{"component": {key}})
				if err != nil {
					return err
				}
				filtered := filterNonInherited(items)
				return w.WriteChunk(enrichAll(filtered, map[string]any{"project": key, "serverUrl": e.ServerURL}))
			})
	}
}

func filterNonInherited(items []json.RawMessage) []json.RawMessage {
	var filtered []json.RawMessage
	for _, s := range items {
		if !extractBool(s, "inherited") {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

func projectMeasuresTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getProjectMeasures", "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				items, err := e.Raw.GetArray(ctx, "api/measures/search", "measures",
					url.Values{"projectKeys": {key}, "metricKeys": {measureMetricKeysFor(e.Version)}})
				if err != nil {
					return err
				}
				return w.WriteChunk(enrichAll(items, map[string]any{"projectKey": key, "serverUrl": e.ServerURL}))
			})
	}
}

func projectBindingsTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getProjectBindings", "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				raw, err := e.Raw.Get(ctx, "api/alm_settings/get_binding",
					url.Values{"project": {key}})
				if err != nil {
					return nil // Binding may not exist; skip gracefully.
				}
				return w.WriteOne(EnrichRaw(raw, map[string]any{"projectKey": key, "serverUrl": e.ServerURL}))
			})
	}
}

func projectGroupsPermissionsTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getProjectGroupsPermissions", "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				meta := map[string]any{"project": key, "serverUrl": e.ServerURL}
				return fetchAllProjectPermissions(ctx, e, w, key, meta)
			})
	}
}

func fetchAllProjectPermissions(ctx context.Context, e *Executor, w *ChunkWriter, key string, meta map[string]any) error {
	for _, perm := range projectPermissions {
		items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
			Path: "api/permissions/groups", ResultKey: "groups", MaxPageSize: 100,
			Params: url.Values{"projectKey": {key}, "permission": {perm}},
		})
		if err != nil {
			return err
		}
		if err := w.WriteChunk(enrichAll(items, meta)); err != nil {
			return err
		}
	}
	return nil
}
