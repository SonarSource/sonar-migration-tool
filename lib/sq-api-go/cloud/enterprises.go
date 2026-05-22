// Package cloud — enterprises.go covers the /enterprises REST API available on
// SonarQube Cloud Enterprise plans. This API uses a different base URL
// (api.sonarcloud.io rather than sonarcloud.io) and JSON request/response bodies.
package cloud

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/sonar-solutions/sq-api-go/types"
)

// EnterprisesClient provides methods for the Cloud Enterprises API.
// The base sqapi.Client must be pointed at the enterprise API base URL
// (e.g. "https://api.sonarcloud.io").
type EnterprisesClient struct{ baseClient }

// List returns all enterprises accessible to the authenticated token via
// GET /enterprises/enterprises.
func (e *EnterprisesClient) List(ctx context.Context) ([]types.Enterprise, error) {
	var result types.EnterprisesListResponse
	if err := e.getJSON(ctx, "enterprises/enterprises", &result); err != nil {
		return nil, err
	}
	return result.Enterprises, nil
}

// CreatePortfolioParams holds the parameters for creating a Cloud portfolio.
type CreatePortfolioParams struct {
	EnterpriseID string
	Name         string
	Description  string
	// Selection controls how the portfolio selects projects. Defaults to "projects".
	Selection string
}

// CreatePortfolio creates a portfolio within an enterprise via
// POST /enterprises/portfolios.
func (e *EnterprisesClient) CreatePortfolio(ctx context.Context, params CreatePortfolioParams) (*types.Portfolio, error) {
	selection := params.Selection
	if selection == "" {
		selection = "projects"
	}
	body := map[string]any{
		"enterpriseId": params.EnterpriseID,
		"name":         params.Name,
		"description":  params.Description,
		"selection":    selection,
	}

	var result types.Portfolio
	if err := e.postJSON(ctx, "enterprises/portfolios", body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// PortfolioProjectRef identifies a single project assignment in a portfolio.
// The SonarQube Cloud portfolios PATCH endpoint represents project membership
// as objects with a branchId (UUID of the project branch the portfolio
// tracks), not as a list of project keys.
type PortfolioProjectRef struct {
	BranchID string `json:"branchId"`
}

// UpdatePortfolioParams holds parameters accepted by
// PATCH /enterprises/portfolios/{id}. Only non-zero fields are sent; the
// server treats missing fields as "no change".
//
// Mapping rules:
//   - Selection "" (default) — server keeps the existing selection mode.
//   - Selection "projects"   — Projects is the authoritative list.
//   - Selection "regexp"     — RegularExpression + OrganizationIDs apply.
//   - Selection "tags"       — Tags + OrganizationIDs apply.
//
// ProjectKeys is a legacy convenience: if set (and Projects is empty),
// callers that still hold cloud project keys can pass them and we forward
// them as branchId values. The SonarQube Cloud API rejects this shape, so
// new code should populate Projects directly.
type UpdatePortfolioParams struct {
	PortfolioID       string
	Selection         string
	Name              string
	Description       string
	BranchKey         string
	RegularExpression string
	Tags              []string
	OrganizationIDs   []string
	Projects          []PortfolioProjectRef

	// Deprecated: kept for backward compatibility with callers that still
	// pass project keys. Use Projects + PortfolioProjectRef{BranchID} instead.
	ProjectKeys []string
}

// UpdatePortfolio updates a portfolio via PATCH /enterprises/portfolios/{id}.
// Only non-empty fields are included in the request body.
func (e *EnterprisesClient) UpdatePortfolio(ctx context.Context, params UpdatePortfolioParams) error {
	body := buildPortfolioPatchBody(params)
	path := fmt.Sprintf("enterprises/portfolios/%s", params.PortfolioID)
	return e.patchJSON(ctx, path, body, nil)
}

// buildPortfolioPatchBody assembles the JSON body honouring the
// "only-send-set-fields" rule. Exposed only for testability.
func buildPortfolioPatchBody(params UpdatePortfolioParams) map[string]any {
	body := map[string]any{}
	if params.Name != "" {
		body["name"] = params.Name
	}
	if params.Description != "" {
		body["description"] = params.Description
	}
	if params.Selection != "" {
		body["selection"] = params.Selection
	}
	if params.BranchKey != "" {
		body["branchKey"] = params.BranchKey
	}
	if params.RegularExpression != "" {
		body["regularExpression"] = params.RegularExpression
	}
	if len(params.Tags) > 0 {
		body["tags"] = params.Tags
	}
	if len(params.OrganizationIDs) > 0 {
		body["organizationIds"] = params.OrganizationIDs
	}
	switch {
	case len(params.Projects) > 0:
		body["projects"] = params.Projects
	case len(params.ProjectKeys) > 0:
		// Legacy adapter — let callers that have not been migrated still
		// send something. The SQC API will likely reject this if branchId
		// is required to be a UUID.
		refs := make([]PortfolioProjectRef, len(params.ProjectKeys))
		for i, k := range params.ProjectKeys {
			refs[i] = PortfolioProjectRef{BranchID: k}
		}
		body["projects"] = refs
	}
	return body
}

// DeletePortfolio deletes a portfolio via DELETE /enterprises/portfolios/{id}.
func (e *EnterprisesClient) DeletePortfolio(ctx context.Context, portfolioID string) error {
	path := fmt.Sprintf("enterprises/portfolios/%s", portfolioID)
	return e.deleteReq(ctx, path)
}

// ListPortfoliosParams holds optional parameters for ListPortfolios. Only
// EnterpriseID is required in normal usage; the API also accepts a search
// query and pagination knobs.
type ListPortfoliosParams struct {
	EnterpriseID string
	Query        string
	PageIndex    int
	PageSize     int
}

// listPortfoliosDefaultPageSize is the API's default and maximum page size.
const listPortfoliosDefaultPageSize = 50

// ListPortfoliosPage fetches a single page of enterprise portfolios via
// GET /enterprises/portfolios. PageIndex is 1-based; PageSize defaults to 50.
func (e *EnterprisesClient) ListPortfoliosPage(ctx context.Context, params ListPortfoliosParams) (*types.PortfoliosListResponse, error) {
	q := url.Values{}
	if params.EnterpriseID != "" {
		q.Set("enterpriseId", params.EnterpriseID)
	}
	if params.Query != "" {
		q.Set("q", params.Query)
	}
	pageIndex := params.PageIndex
	if pageIndex <= 0 {
		pageIndex = 1
	}
	q.Set("pageIndex", strconv.Itoa(pageIndex))
	pageSize := params.PageSize
	if pageSize <= 0 {
		pageSize = listPortfoliosDefaultPageSize
	}
	q.Set("pageSize", strconv.Itoa(pageSize))

	var result types.PortfoliosListResponse
	path := "enterprises/portfolios?" + q.Encode()
	if err := e.getJSON(ctx, path, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListPortfolios fetches every page of portfolios for the given enterprise
// (and optional search query) and returns the concatenated list. The caller
// should typically supply an EnterpriseID; the API also allows omitting it
// when Favorite=true on the underlying endpoint, but that mode is not
// exposed here as it is not useful for migration tooling.
func (e *EnterprisesClient) ListPortfolios(ctx context.Context, params ListPortfoliosParams) ([]types.Portfolio, error) {
	if params.PageSize <= 0 {
		params.PageSize = listPortfoliosDefaultPageSize
	}
	if params.PageIndex <= 0 {
		params.PageIndex = 1
	}
	var all []types.Portfolio
	for {
		page, err := e.ListPortfoliosPage(ctx, params)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Portfolios...)
		if len(page.Portfolios) < params.PageSize {
			break
		}
		if page.Page.Total > 0 && len(all) >= page.Page.Total {
			break
		}
		params.PageIndex++
	}
	return all, nil
}
