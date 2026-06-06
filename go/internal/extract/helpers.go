// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"golang.org/x/sync/errgroup"
)

// acquireSem is a convenience alias.
var acquireSem = common.AcquireSem

// Common format strings and API paths.
const (
	taskErrFmt      = "%s: %w"
	issuesSearchAPI = "api/issues/search"
	rulesSearchAPI  = "api/rules/search"
	skippedSuffix   = " skipped"
)

// EnrichRaw merges additional key-value pairs into a raw JSON object.
var EnrichRaw = common.EnrichRaw

// enrichAll applies EnrichRaw to every item in a slice.
var enrichAll = common.EnrichAll

// extractField extracts a string value from a json.RawMessage by key.
var extractField = common.ExtractField

// extractBool extracts a boolean value from a json.RawMessage by key.
var extractBool = common.ExtractBool

// Expansion defines a set of values for cross-product iteration.
type Expansion = common.Expansion

// expandCombinations returns all combinations from a list of expansions.
var expandCombinations = common.ExpandCombinations

// forEachDep reads all items from a dependency task and calls fn for each,
// concurrently (bounded by the Executor's semaphore).
func forEachDep(ctx context.Context, e *Executor, taskName, depTask string,
	fn func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error) error {

	return forEachDepFiltered(ctx, e, taskName, depTask,
		func(item json.RawMessage) bool {
			key := extractField(item, "key")
			return key == "" || !e.IsSkipped(key)
		}, fn)
}

// forEachDepFiltered is like forEachDep but skips items where filterFn returns false.
func forEachDepFiltered(ctx context.Context, e *Executor, taskName, depTask string,
	filterFn func(json.RawMessage) bool,
	fn func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error) error {

	items, err := e.Store.ReadAll(depTask)
	if err != nil {
		return fmt.Errorf("%s: reading dependency %s: %w", taskName, depTask, err)
	}

	// Pre-filter to get accurate count for progress logging (#340).
	var filtered []json.RawMessage
	for _, item := range items {
		if filterFn != nil && !filterFn(item) {
			continue
		}
		filtered = append(filtered, item)
	}

	e.Logger.Info("starting task", "task", taskName, "items", len(filtered))
	prog := common.NewProgressLogger(e.Logger, taskName, len(filtered))

	w, err := e.Store.Writer(taskName)
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)
	for _, item := range filtered {
		g.Go(func() error {
			err := runWithSem(ctx, e, taskName, item, w, fn)
			prog.Increment()
			return err
		})
	}
	return g.Wait()
}

// runWithSem acquires the semaphore, runs fn, and handles non-fatal HTTP errors.
func runWithSem(ctx context.Context, e *Executor, taskName string,
	item json.RawMessage, w *ChunkWriter,
	fn func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error) error {

	if err := acquireSem(ctx, e.Sem); err != nil {
		return err
	}
	defer func() { <-e.Sem }()

	if err := fn(ctx, item, w); err != nil {
		return handleNonFatalErr(e, taskName, item, err)
	}
	return nil
}

// handleNonFatalErr logs and records skipped items for 403/404 errors, otherwise returns the error.
func handleNonFatalErr(e *Executor, taskName string, item json.RawMessage, err error) error {
	if !isNonFatalHTTPErr(err) {
		return err
	}
	key := extractField(item, "key")
	e.Logger.Warn(taskName+skippedSuffix, "key", key, "err", err)
	if key != "" {
		e.RecordSkipped(key)
	}
	return nil
}

// fetchAndWritePaginated fetches a paginated endpoint and writes results as JSONL.
func fetchAndWritePaginated(ctx context.Context, e *Executor, taskName string,
	opts PaginatedOpts, metadata map[string]any) error {

	if err := acquireSem(ctx, e.Sem); err != nil {
		return fmt.Errorf(taskErrFmt, taskName, err)
	}
	items, err := e.Raw.GetPaginated(ctx, opts)
	<-e.Sem
	if err != nil {
		return fmt.Errorf(taskErrFmt, taskName, err)
	}
	if metadata != nil {
		items = enrichAll(items, metadata)
	}
	w, err := e.Store.Writer(taskName)
	if err != nil {
		return err
	}
	return w.WriteChunk(items)
}

// fetchAndWriteArray fetches a non-paginated endpoint that returns an array at resultKey.
func fetchAndWriteArray(ctx context.Context, e *Executor, taskName, path, resultKey string,
	params url.Values, metadata map[string]any) error {

	if err := acquireSem(ctx, e.Sem); err != nil {
		return fmt.Errorf(taskErrFmt, taskName, err)
	}
	items, err := e.Raw.GetArray(ctx, path, resultKey, params)
	<-e.Sem
	if err != nil {
		return fmt.Errorf(taskErrFmt, taskName, err)
	}
	if metadata != nil {
		items = enrichAll(items, metadata)
	}
	w, err := e.Store.Writer(taskName)
	if err != nil {
		return err
	}
	return w.WriteChunk(items)
}

// fetchAndWriteSingle fetches a single JSON object and writes it as one JSONL line.
// If resultKey is non-empty, extracts the value at that key first.
func fetchAndWriteSingle(ctx context.Context, e *Executor, taskName, path string,
	params url.Values, resultKey string, metadata map[string]any) error {

	if err := acquireSem(ctx, e.Sem); err != nil {
		return fmt.Errorf(taskErrFmt, taskName, err)
	}
	raw, err := e.Raw.Get(ctx, path, params)
	<-e.Sem
	if err != nil {
		return fmt.Errorf(taskErrFmt, taskName, err)
	}
	if resultKey != "" {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err == nil {
			if v, ok := obj[resultKey]; ok {
				raw = v
			}
		}
	}
	if metadata != nil {
		raw = EnrichRaw(raw, metadata)
	}
	w, err := e.Store.Writer(taskName)
	if err != nil {
		return err
	}
	return w.WriteOne(raw)
}

// isNonFatalHTTPErr returns true for HTTP 403/404 errors that should be
// logged and skipped rather than failing the entire task.
func isNonFatalHTTPErr(err error) bool {
	return common.IsHTTPError(err, 403, 404)
}

// perProjectArray runs a per-project task that fetches an array from an endpoint.
func perProjectArray(taskName, path, resultKey, paramKey, metaKey string) func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, taskName, "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				items, err := e.Raw.GetArray(ctx, path, resultKey,
					url.Values{paramKey: {key}})
				if err != nil {
					if isNonFatalHTTPErr(err) {
						e.Logger.Warn(taskName+skippedSuffix, "project", key, "err", err)
						e.RecordSkipped(key)
						return nil
					}
					return err
				}
				return w.WriteChunk(enrichAll(items, map[string]any{metaKey: key, "serverUrl": e.ServerURL}))
			})
	}
}

// perProjectSingle runs a per-project task that fetches a single object.
func perProjectSingle(taskName, path, paramKey string) func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, taskName, "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				raw, err := e.Raw.Get(ctx, path, url.Values{paramKey: {key}})
				if err != nil {
					if isNonFatalHTTPErr(err) {
						e.Logger.Warn(taskName+skippedSuffix, "project", key, "err", err)
						e.RecordSkipped(key)
						return nil
					}
					return err
				}
				return w.WriteOne(EnrichRaw(raw, map[string]any{"projectKey": key, "serverUrl": e.ServerURL}))
			})
	}
}

// perProjectPaginated runs a per-project task that fetches paginated results.
func perProjectPaginated(taskName, path, resultKey, paramKey, metaKey string, maxPageSize int) func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, taskName, "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
					Path: path, ResultKey: resultKey, MaxPageSize: maxPageSize,
					Params: url.Values{paramKey: {key}},
				})
				if err != nil {
					if isNonFatalHTTPErr(err) {
						e.Logger.Warn(taskName+skippedSuffix, "project", key, "err", err)
						e.RecordSkipped(key)
						return nil
					}
					return err
				}
				return w.WriteChunk(enrichAll(items, map[string]any{metaKey: key, "serverUrl": e.ServerURL}))
			})
	}
}

// perProjectIssueCount runs a per-project issues/search with ps=1 (count-only).
func perProjectIssueCount(taskName string, extraParams url.Values) func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, taskName, "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				params := url.Values{"components": {key}, "ps": {"1"}}
				for k, v := range extraParams {
					params[k] = v
				}
				raw, err := e.Raw.Get(ctx, "api/issues/search", params)
				if err != nil {
					if isNonFatalHTTPErr(err) {
						e.Logger.Warn(taskName+skippedSuffix, "project", key, "err", err)
						e.RecordSkipped(key)
						return nil
					}
					return err
				}
				return w.WriteOne(EnrichRaw(raw, map[string]any{"projectKey": key, "serverUrl": e.ServerURL}))
			})
	}
}

// perProjectPermissionUsers runs a per-project paginated permission/users query.
func perProjectPermissionUsers(taskName, permission string) func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, taskName, "getProjects",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				key := extractField(item, "key")
				items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
					Path: "api/permissions/users", ResultKey: "users", MaxPageSize: 100,
					Params: url.Values{"projectKey": {key}, "permission": {permission}},
				})
				if err != nil {
					if isNonFatalHTTPErr(err) {
						e.Logger.Warn(taskName+skippedSuffix, "project", key, "err", err)
						e.RecordSkipped(key)
						return nil
					}
					return err
				}
				return w.WriteChunk(enrichAll(items, map[string]any{"project": key, "serverUrl": e.ServerURL}))
			})
	}
}
