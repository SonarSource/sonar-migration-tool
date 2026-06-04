package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

const defaultPageSize = 500

type pagingResult struct {
	PageIndex int `json:"pageIndex"`
	PageSize  int `json:"pageSize"`
	Total     int `json:"total"`
}

// paginateAll is a generic helper that iterates through a paginated API until
// no items remain or the accumulated count reaches the reported total. The
// fetchPage closure receives a 1-based page number and must return
// (items, totalCount, error).
func paginateAll[T any](ctx context.Context, fetchPage func(context.Context, int) ([]T, int, error)) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		items, total, err := fetchPage(ctx, page)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) == 0 || len(all) >= total {
			break
		}
	}
	return all, nil
}

// fetchAllIssues paginates through /api/issues/search using the given status
// parameter name and values, returning all issues for the project.
func fetchAllIssues(ctx context.Context, client *sqapi.Client, projectKey, paramName string, statusValues []string) ([]Issue, error) {
	return paginateAll(ctx, func(ctx context.Context, page int) ([]Issue, int, error) {
		return fetchIssuesPage(ctx, client, projectKey, paramName, statusValues, page, defaultPageSize)
	})
}

func fetchIssuesPage(ctx context.Context, client *sqapi.Client, projectKey, paramName string, statusValues []string, page, pageSize int) ([]Issue, int, error) {
	params := url.Values{
		"componentKeys": {projectKey},
		paramName:       {strings.Join(statusValues, ",")},
		"p":             {strconv.Itoa(page)},
		"ps":            {strconv.Itoa(pageSize)},
	}
	u := client.BaseURL() + "api/issues/search?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("building issues request: %w", err)
	}
	resp, err := client.HTTPClient().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching issues page %d: %w", page, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("issues/search returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Paging pagingResult `json:"paging"`
		Issues []Issue      `json:"issues"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, fmt.Errorf("decoding issues response: %w", err)
	}
	return result.Issues, result.Paging.Total, nil
}

// fetchAllHotspots paginates through /api/hotspots/search, returning all
// hotspots for the project.
func fetchAllHotspots(ctx context.Context, client *sqapi.Client, projectKey string) ([]Hotspot, error) {
	return paginateAll(ctx, func(ctx context.Context, page int) ([]Hotspot, int, error) {
		return fetchHotspotsPage(ctx, client, projectKey, page, defaultPageSize)
	})
}

func fetchHotspotsPage(ctx context.Context, client *sqapi.Client, projectKey string, page, pageSize int) ([]Hotspot, int, error) {
	params := url.Values{
		"projectKey": {projectKey},
		"p":          {strconv.Itoa(page)},
		"ps":         {strconv.Itoa(pageSize)},
	}
	u := client.BaseURL() + "api/hotspots/search?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("building hotspots request: %w", err)
	}
	resp, err := client.HTTPClient().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching hotspots page %d: %w", page, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("hotspots/search returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Paging   pagingResult `json:"paging"`
		Hotspots []Hotspot    `json:"hotspots"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, fmt.Errorf("decoding hotspots response: %w", err)
	}
	return result.Hotspots, result.Paging.Total, nil
}

// fetchAllMetrics retrieves component metrics from /api/measures/component_tree.
// When batchSize > 0 the metricKeys are split into batches of that size
// (required for SQ 9.9 through 10.8); when batchSize == 0 all keys are sent in
// a single request (SQ 2025.1+). Component pagination is handled transparently.
func fetchAllMetrics(ctx context.Context, client *sqapi.Client, projectKey string, metricKeys []string, batchSize int) ([]ComponentMetrics, error) {
	if batchSize <= 0 || len(metricKeys) <= batchSize {
		return fetchComponentMetrics(ctx, client, projectKey, metricKeys)
	}
	// Batched: split metricKeys and merge per-component results.
	accumulator := make(map[string]*ComponentMetrics)
	for i := 0; i < len(metricKeys); i += batchSize {
		end := i + batchSize
		if end > len(metricKeys) {
			end = len(metricKeys)
		}
		batch, err := fetchComponentMetrics(ctx, client, projectKey, metricKeys[i:end])
		if err != nil {
			return nil, fmt.Errorf("metrics batch [%d:%d]: %w", i, end, err)
		}
		for _, cm := range batch {
			if existing, ok := accumulator[cm.Component]; ok {
				existing.Measures = append(existing.Measures, cm.Measures...)
			} else {
				copied := cm
				accumulator[cm.Component] = &copied
			}
		}
	}
	result := make([]ComponentMetrics, 0, len(accumulator))
	for _, cm := range accumulator {
		result = append(result, *cm)
	}
	return result, nil
}

// fetchComponentMetrics fetches all leaf-component metrics for the given keys,
// paging through the response automatically. Components appearing in multiple
// pages are merged so each component appears once in the result.
func fetchComponentMetrics(ctx context.Context, client *sqapi.Client, projectKey string, metricKeys []string) ([]ComponentMetrics, error) {
	pages, err := paginateAll(ctx, func(ctx context.Context, page int) ([]ComponentMetrics, int, error) {
		return fetchComponentMetricsPage(ctx, client, projectKey, metricKeys, page, defaultPageSize)
	})
	if err != nil {
		return nil, err
	}
	return mergeComponentMetrics(pages), nil
}

// mergeComponentMetrics folds any duplicate component keys into a single entry
// whose Measures slice is the concatenation of all occurrences.
func mergeComponentMetrics(pages []ComponentMetrics) []ComponentMetrics {
	if len(pages) == 0 {
		return nil
	}
	seen := make(map[string]int, len(pages))
	out := make([]ComponentMetrics, 0, len(pages))
	for _, cm := range pages {
		if idx, ok := seen[cm.Component]; ok {
			out[idx].Measures = append(out[idx].Measures, cm.Measures...)
			continue
		}
		seen[cm.Component] = len(out)
		out = append(out, cm)
	}
	return out
}

func fetchComponentMetricsPage(ctx context.Context, client *sqapi.Client, projectKey string, metricKeys []string, page, pageSize int) ([]ComponentMetrics, int, error) {
	params := url.Values{
		"component":  {projectKey},
		"metricKeys": {strings.Join(metricKeys, ",")},
		"strategy":   {"leaves"},
		"p":          {strconv.Itoa(page)},
		"ps":         {strconv.Itoa(pageSize)},
	}
	u := client.BaseURL() + "api/measures/component_tree?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("building metrics request: %w", err)
	}
	resp, err := client.HTTPClient().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching metrics page %d: %w", page, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("measures/component_tree returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Paging     pagingResult `json:"paging"`
		Components []struct {
			Key      string    `json:"key"`
			Measures []Measure `json:"measures"`
		} `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, fmt.Errorf("decoding metrics response: %w", err)
	}
	out := make([]ComponentMetrics, 0, len(result.Components))
	for _, c := range result.Components {
		out = append(out, ComponentMetrics{Component: c.Key, Measures: c.Measures})
	}
	return out, result.Paging.Total, nil
}

// fetchAllGroups retrieves all groups via the standard /api/user_groups/search
// endpoint, used by the SQ 9.9, 10.0, and 10.4 pipelines.
func fetchAllGroups(ctx context.Context, client *sqapi.Client) ([]Group, error) {
	return paginateAll(ctx, func(ctx context.Context, page int) ([]Group, int, error) {
		return fetchGroupsPage(ctx, client, page, defaultPageSize)
	})
}

func fetchGroupsPage(ctx context.Context, client *sqapi.Client, page, pageSize int) ([]Group, int, error) {
	params := url.Values{
		"p":  {strconv.Itoa(page)},
		"ps": {strconv.Itoa(pageSize)},
	}
	u := client.BaseURL() + "api/user_groups/search?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("building groups request: %w", err)
	}
	resp, err := client.HTTPClient().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching groups page %d: %w", page, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("user_groups/search returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Paging pagingResult `json:"paging"`
		Groups []Group      `json:"groups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, fmt.Errorf("decoding groups response: %w", err)
	}
	return result.Groups, result.Paging.Total, nil
}
