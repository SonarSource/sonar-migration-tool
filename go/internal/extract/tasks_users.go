// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"net/url"
)

func userTasks() []TaskDef {
	return []TaskDef{
		{
			Name:     "getUsers",
			Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWritePaginated(ctx, e, "getUsers", PaginatedOpts{
					Path: "api/users/search", ResultKey: "users", MaxPageSize: 100,
				}, map[string]any{"serverUrl": e.ServerURL})
			},
		},
		{
			Name:     "getUserPermissions",
			Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWritePaginated(ctx, e, "getUserPermissions", PaginatedOpts{
					Path: "api/permissions/users", ResultKey: "users", MaxPageSize: 100,
				}, map[string]any{"serverUrl": e.ServerURL})
			},
		},
		{
			Name:         "getUserGroups",
			Editions:     AllEditions,
			Dependencies: []string{"getGroups"},
			Run: func(ctx context.Context, e *Executor) error {
				return forEachDepFiltered(ctx, e, "getUserGroups", "getGroups",
					func(item json.RawMessage) bool {
						return extractField(item, "name") != "Anyone"
					},
					func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
						name := extractField(item, "name")
						groupID := extractField(item, "id")
						items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
							Path: "api/user_groups/users", ResultKey: "users",
							Params: url.Values{"name": {name}},
						})
						if err != nil {
							return err
						}
						items = enrichAll(items, map[string]any{"groupId": groupID, "serverUrl": e.ServerURL})
						return w.WriteChunk(items)
					})
			},
		},
		{
			Name:         "getUserTokens",
			Editions:     AllEditions,
			Dependencies: []string{"getUsers"},
			Run: func(ctx context.Context, e *Executor) error {
				return forEachDep(ctx, e, "getUserTokens", "getUsers",
					func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
						login := extractField(item, "login")
						items, err := e.Raw.GetArray(ctx, "api/user_tokens/search", "userTokens",
							url.Values{"login": {login}})
						if err != nil {
							return err
						}
						items = enrichAll(items, map[string]any{"login": login, "serverUrl": e.ServerURL})
						return w.WriteChunk(items)
					})
			},
		},
		{
			Name:     "getGroups",
			Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWritePaginatedFiltered(ctx, e, "getGroups", PaginatedOpts{
					Path: "api/permissions/groups", ResultKey: "groups", MaxPageSize: 100,
				}, map[string]any{"serverUrl": e.ServerURL}, keepGroupByRegexp)
			},
		},
	}
}

// keepGroupByRegexp decides whether a raw "api/permissions/groups" item
// survives extraction: no filter configured, it's the "Anyone" pseudo-group
// (never a real group, always kept), or its name matches e.GroupRe. #669.
func keepGroupByRegexp(e *Executor, item json.RawMessage) bool {
	if e.GroupRe == nil {
		return true
	}
	name := extractField(item, "name")
	return name == "Anyone" || e.GroupRe.MatchString(name)
}
