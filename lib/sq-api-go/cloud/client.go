// Package cloud provides typed SonarQube Cloud API clients for the endpoints
// used by the sonar-migration-tool. It covers write-path (migration) operations:
// creating, updating, and deleting projects, groups, quality profiles, quality
// gates, permission templates, rules, settings, portfolios, and DOP bindings.
//
// Usage:
//
//	base := sqapi.NewCloudClient("https://sonarcloud.io", "squ_token")
//	cc := cloud.New(base)
//
//	// SonarCloud standard API
//	proj, err := cc.Projects.Create(ctx, cloud.CreateProjectParams{...})
//
//	// Enterprises API (different base URL required)
//	apiBase := sqapi.NewCloudClient("https://api.sonarcloud.io", "squ_token")
//	ec := cloud.New(apiBase)
//	portfolios, err := ec.Enterprises.List(ctx, "my-enterprise-id")
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

const headerContentType = "Content-Type"

// Client is the entry point for all SonarQube Cloud API write operations.
// Each field is a sub-client for a specific API domain.
type Client struct {
	Projects        *ProjectsClient
	Groups          *GroupsClient
	QualityProfiles *QualityProfilesClient
	QualityGates    *QualityGatesClient
	Permissions     *PermissionsClient
	Branches        *BranchesClient
	Rules           *RulesClient
	Settings        *SettingsClient
	Enterprises     *EnterprisesClient
	Organizations   *OrganizationsClient
	DOP             *DOPClient
}

// New wraps a base sqapi.Client with typed Cloud write-path endpoint methods.
func New(c *sqapi.Client) *Client {
	b := baseClient{c: c}
	return &Client{
		Projects:        &ProjectsClient{b},
		Groups:          &GroupsClient{b},
		QualityProfiles: &QualityProfilesClient{b},
		QualityGates:    &QualityGatesClient{b},
		Permissions:     &PermissionsClient{b},
		Branches:        &BranchesClient{b},
		Rules:           &RulesClient{b},
		Settings:        &SettingsClient{b},
		Enterprises:     &EnterprisesClient{b},
		Organizations:   &OrganizationsClient{b},
		DOP:             &DOPClient{b},
	}
}

// baseClient is embedded by all sub-clients to share HTTP + JSON logic.
// path arguments must not include a leading slash — baseURL already ends with '/'.
type baseClient struct{ c *sqapi.Client }

// postForm executes a POST request with application/x-www-form-urlencoded body,
// decodes the JSON response into out, and returns an sqapi.APIError for non-2xx status.
// Pass out=nil to discard the response body (for endpoints that return 204 or empty 200).
func (b *baseClient) postForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		b.c.BaseURL()+path,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return err
	}
	req.Header.Set(headerContentType, "application/x-www-form-urlencoded")
	return b.do(req, out)
}

// postJSON executes a POST request with application/json body,
// decodes the JSON response into out, and returns an sqapi.APIError for non-2xx status.
// Pass out=nil to discard the response body.
func (b *baseClient) postJSON(ctx context.Context, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("cloud: marshal request body: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		b.c.BaseURL()+path,
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set(headerContentType, "application/json")
	return b.do(req, out)
}

// patchJSON executes a PATCH request with application/json body,
// decodes the JSON response into out, and returns an sqapi.APIError for non-2xx status.
// Pass out=nil to discard the response body.
func (b *baseClient) patchJSON(ctx context.Context, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("cloud: marshal request body: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPatch,
		b.c.BaseURL()+path,
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set(headerContentType, "application/json")
	return b.do(req, out)
}

// deleteReq executes a DELETE request and returns an sqapi.APIError for non-2xx status.
func (b *baseClient) deleteReq(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodDelete,
		b.c.BaseURL()+path,
		nil,
	)
	if err != nil {
		return err
	}
	return b.do(req, nil)
}

// postMultipart executes a POST request with multipart/form-data body,
// decodes the JSON response into out, and returns an sqapi.APIError for non-2xx status.
// fields is a map of field names to their values; fileField/fileName/fileContent
// add a single file part.
func (b *baseClient) postMultipart(
	ctx context.Context,
	path string,
	fields map[string]string,
	fileField, fileName string,
	fileContent []byte,
	out any,
) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return fmt.Errorf("cloud: write multipart field %q: %w", k, err)
		}
	}
	if fileField != "" && fileContent != nil {
		fw, err := mw.CreateFormFile(fileField, fileName)
		if err != nil {
			return fmt.Errorf("cloud: create form file %q: %w", fileField, err)
		}
		if _, err = fw.Write(fileContent); err != nil {
			return fmt.Errorf("cloud: write form file %q: %w", fileField, err)
		}
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("cloud: close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		b.c.BaseURL()+path,
		&buf,
	)
	if err != nil {
		return err
	}
	req.Header.Set(headerContentType, mw.FormDataContentType())
	return b.do(req, out)
}

// getJSON executes a GET request, decodes the JSON response into out, and
// returns an sqapi.APIError for non-2xx status.
func (b *baseClient) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.c.BaseURL()+path, nil)
	if err != nil {
		return err
	}
	return b.do(req, out)
}

// do executes req, checks the status code, and optionally decodes JSON into out.
func (b *baseClient) do(req *http.Request, out any) error {
	resp, err := b.c.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return &sqapi.APIError{
			StatusCode: resp.StatusCode,
			Method:     req.Method,
			URL:        req.URL.String(),
			Body:       string(body),
		}
	}

	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}

	return json.NewDecoder(resp.Body).Decode(out)
}
