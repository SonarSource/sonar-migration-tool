package extract

import (
	"context"
	"encoding/json"
	"net/url"
)

func profileTasks() []TaskDef {
	return []TaskDef{
		{
			Name:     "getProfiles",
			Editions: AllEditions,
			Run: func(ctx context.Context, e *Executor) error {
				return fetchAndWriteArray(ctx, e, "getProfiles", "api/qualityprofiles/search", "profiles", nil, map[string]any{"serverUrl": e.ServerURL})
			},
		},
		{
			Name: "getProfileBackups", Editions: AllEditions, Dependencies: []string{"getProfiles"},
			Run: func(ctx context.Context, e *Executor) error {
				return forEachDep(ctx, e, "getProfileBackups", "getProfiles",
					func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
						name := extractField(item, "name")
						lang := extractField(item, "language")
						profileKey := extractField(item, "key")
						raw, err := e.Raw.GetRaw(ctx, "api/qualityprofiles/backup",
							url.Values{"language": {lang}, "qualityProfile": {name}})
						if err != nil {
							return err
						}
						meta := map[string]any{
							"profileName": name, "language": lang,
							"profileKey": profileKey, "serverUrl": e.ServerURL,
							"backup": string(raw),
						}
						b, _ := json.Marshal(meta)
						return w.WriteOne(b)
					})
			},
		},
		{Name: "getProfileGroups", Editions: AllEditions, Dependencies: []string{"getProfiles"},
			Run: perProfilePaginated("getProfileGroups", "api/qualityprofiles/search_groups", "groups")},
		{Name: "getProfileUsers", Editions: AllEditions, Dependencies: []string{"getProfiles"},
			Run: perProfilePaginated("getProfileUsers", "api/qualityprofiles/search_users", "users")},
		{
			// getProfileProjects lists the projects EXPLICITLY assigned
			// to each non-built-in quality profile (selected=selected).
			// Used by migrate's setProjectProfiles as the source of truth
			// for project→profile assignments. The qualityProfiles array
			// returned by api/navigation/component (in getProjectDetails)
			// reports the profile used at the LAST ANALYSIS, which can
			// drift from the current explicit assignment if a project
			// was unassigned after its last analysis — bug observed in
			// issue #160 where a profile showed projectCount=1 on SQS
			// but the navigation/component data fingered 2 projects,
			// leaving the second project incorrectly attached to the
			// custom profile on SQC after migration.
			Name:         "getProfileProjects",
			Editions:     AllEditions,
			Dependencies: []string{"getProfiles"},
			Run: func(ctx context.Context, e *Executor) error {
				return forEachDepFiltered(ctx, e, "getProfileProjects", "getProfiles", notBuiltIn,
					func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
						name := extractField(item, "name")
						lang := extractField(item, "language")
						profileKey := extractField(item, "key")
						// /api/qualityprofiles/projects is keyed by the
					// profile UUID, NOT by qualityProfile+language like
					// search_groups / search_users. Using the wrong
					// parameter shape produces an empty result set, which
					// is what was happening when this fix first shipped.
					items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
						Path: "api/qualityprofiles/projects", ResultKey: "results",
						Params: url.Values{
							"key":      {profileKey},
							"selected": {"selected"},
						},
					})
						if err != nil {
							return err
						}
						return w.WriteChunk(enrichAll(items, map[string]any{
							"profileKey":  profileKey,
							"profileName": name,
							"language":    lang,
							"serverUrl":   e.ServerURL,
						}))
					})
			},
		},
	}
}

// perProfilePaginated fetches paginated results for non-built-in profiles.
func perProfilePaginated(taskName, path, resultKey string) func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDepFiltered(ctx, e, taskName, "getProfiles", notBuiltIn,
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				name := extractField(item, "name")
				lang := extractField(item, "language")
				profileKey := extractField(item, "key")
				items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
					Path: path, ResultKey: resultKey,
					Params: url.Values{"qualityProfile": {name}, "language": {lang}},
				})
				if err != nil {
					return err
				}
				return w.WriteChunk(enrichAll(items, map[string]any{"profileKey": profileKey, "serverUrl": e.ServerURL}))
			})
	}
}
