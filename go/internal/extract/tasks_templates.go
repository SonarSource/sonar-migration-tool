// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"net/url"
)

// searchTemplatesTask builds a TaskDef that fetches one result key from
// /api/permissions/search_templates, enriching records with serverUrl.
func searchTemplatesTask(name, resultKey string) TaskDef {
	return TaskDef{
		Name:     name,
		Editions: AllEditions,
		Run: func(ctx context.Context, e *Executor) error {
			return fetchAndWriteArray(ctx, e, name, "api/permissions/search_templates", resultKey, nil, map[string]any{"serverUrl": e.ServerURL})
		},
	}
}

func templateTasks() []TaskDef {
	return []TaskDef{
		searchTemplatesTask("getTemplates", "permissionTemplates"),
		searchTemplatesTask("getDefaultTemplates", "defaultTemplates"),
		{
			Name:         "getTemplateGroupsScanners",
			Editions:     AllEditions,
			Dependencies: []string{"getTemplates"},
			Run:          makeTemplatePermissionTask("getTemplateGroupsScanners", "api/permissions/template_groups", "groups", "scan"),
		},
		{
			Name:         "getTemplateGroupsViewers",
			Editions:     AllEditions,
			Dependencies: []string{"getTemplates"},
			Run:          makeTemplatePermissionTask("getTemplateGroupsViewers", "api/permissions/template_groups", "groups", "user"),
		},
		{
			Name:         "getTemplateUsersScanners",
			Editions:     AllEditions,
			Dependencies: []string{"getTemplates"},
			Run:          makeTemplatePermissionTask("getTemplateUsersScanners", "api/permissions/template_users", "users", "scan"),
		},
		{
			Name:         "getTemplateUsersViewers",
			Editions:     AllEditions,
			Dependencies: []string{"getTemplates"},
			Run:          makeTemplatePermissionTask("getTemplateUsersViewers", "api/permissions/template_users", "users", "user"),
		},
	}
}

func makeTemplatePermissionTask(taskName, path, resultKey, permission string) func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, taskName, "getTemplates",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				templateID := extractField(item, "id")
				items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
					Path: path, ResultKey: resultKey, MaxPageSize: 100,
					Params: url.Values{"templateId": {templateID}, "permission": {permission}},
				})
				if err != nil {
					return err
				}
				items = enrichAll(items, map[string]any{"templateId": templateID, "serverUrl": e.ServerURL})
				return w.WriteChunk(items)
			})
	}
}
