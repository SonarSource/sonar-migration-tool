// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
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
						return keepGroupMembersByRegexp(e, item)
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
// survives extraction: see GroupInScope. #669.
func keepGroupByRegexp(e *Executor, item json.RawMessage) bool {
	return GroupInScope(e.GroupRe, extractField(item, "name"))
}

// GroupInScope reports whether the named source group is in scope of a
// --group_name_regexp slice (#669): always when re is nil, always for the
// built-in groups (the "Anyone" pseudo-group and sonar-users, granted as
// Members on SonarQube Cloud), otherwise when its name matches re. The
// built-ins are not groups a slice migrates but grantees every project,
// template and profile may carry, so every slice keeps their permissions,
// as an unsliced migration does. Extract and migrate share this rule.
func GroupInScope(re *regexp.Regexp, name string) bool {
	return re == nil || isBuiltInGroupName(name) || re.MatchString(name)
}

func isBuiltInGroupName(name string) bool {
	return name == "Anyone" || name == "sonar-users"
}

// keepGroupMembersByRegexp decides whether getUserGroups lists the members
// of a getGroups item. "Anyone" has none; sonar-users holds every user and
// is never migrated as a group, so a slice lists it only when its pattern
// names it, rather than once per slice.
func keepGroupMembersByRegexp(e *Executor, item json.RawMessage) bool {
	name := extractField(item, "name")
	if name == "Anyone" {
		return false
	}
	return e.GroupRe == nil || e.GroupRe.MatchString(name)
}
