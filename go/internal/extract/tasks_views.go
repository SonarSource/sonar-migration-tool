// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"net/url"
)

func viewTasks() []TaskDef {
	return []TaskDef{
		{Name: "getPortfolios", Editions: []Edition{EditionEnterprise, EditionDatacenter},
			Run: getPortfoliosTask()},
		{Name: "getPortfolioDetails", Editions: []Edition{EditionEnterprise, EditionDatacenter},
			Dependencies: []string{"getPortfolios"},
			Run:          getPortfolioDetailsTask()},
		{Name: "getPortfolioProjects", Editions: []Edition{EditionEnterprise, EditionDatacenter},
			Dependencies: []string{"getPortfolios"},
			Run:          getPortfolioProjectsTask()},
		{Name: "getApplications", Editions: []Edition{EditionDeveloper, EditionEnterprise, EditionDatacenter},
			Run: getApplicationsTask()},
		{Name: "getApplicationDetails", Editions: []Edition{EditionDeveloper, EditionEnterprise, EditionDatacenter},
			Dependencies: []string{"getApplications"},
			Run:          getApplicationDetailsTask()},
	}
}

func getPortfoliosTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return fetchAndWritePaginatedFiltered(ctx, e, "getPortfolios", PaginatedOpts{
			Path: "api/views/search", ResultKey: "components",
		}, map[string]any{"serverUrl": e.ServerURL}, keepPortfolioByFilter)
	}
}

// keepPortfolioByFilter decides whether a raw "api/views/search" item
// survives extraction: no filter configured, or its key/name matches
// e.PortfolioFilter. #666.
func keepPortfolioByFilter(e *Executor, item json.RawMessage) bool {
	return e.PortfolioFilter.Match(extractField(item, "key"), extractField(item, "name"))
}

func getPortfolioDetailsTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getPortfolioDetails", "getPortfolios",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				raw, err := e.Raw.Get(ctx, "api/views/show", url.Values{"key": {key}})
				if err != nil {
					return err
				}
				return w.WriteOne(EnrichRaw(raw, map[string]any{"serverUrl": e.ServerURL}))
			})
	}
}

func getPortfolioProjectsTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getPortfolioProjects", "getPortfolios",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				name := extractField(item, "name")
				items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
					Path: "api/views/projects_status", ResultKey: "projects", MaxPageSize: 500,
					Params: url.Values{"portfolio": {key}},
				})
				if err != nil {
					return err
				}
				return w.WriteChunk(enrichAll(items, map[string]any{
					"portfolioKey": key, "portfolioName": name, "serverUrl": e.ServerURL,
				}))
			})
	}
}

func getApplicationsTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return fetchAndWritePaginated(ctx, e, "getApplications", PaginatedOpts{
			Path: "api/components/search", ResultKey: "components", MaxPageSize: 500,
			Params: url.Values{"qualifiers": {"APP"}},
		}, map[string]any{"serverUrl": e.ServerURL})
	}
}

func getApplicationDetailsTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getApplicationDetails", "getApplications",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				raw, err := e.Raw.Get(ctx, "api/applications/show", url.Values{"application": {key}})
				if err != nil {
					return err
				}
				raw = extractSubKey(raw, "application")
				return w.WriteOne(EnrichRaw(raw, map[string]any{"serverUrl": e.ServerURL}))
			})
	}
}
