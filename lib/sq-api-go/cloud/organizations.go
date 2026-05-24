// Package cloud — organizations.go covers the lookup endpoints we need to
// resolve a human-readable organization key (e.g. "my-org") to the UUID that
// other SonarQube Cloud enterprise endpoints expect — in particular
// PATCH /enterprises/portfolios when selection is "regexp" or "tags",
// which requires organizationIds to be a list of UUIDs.
package cloud

import (
	"context"
	"fmt"
	"net/url"

	"github.com/sonar-solutions/sq-api-go/types"
)

// OrganizationsClient provides read access to SonarQube Cloud organizations.
type OrganizationsClient struct{ baseClient }

// Search fetches organizations whose key matches the given input. The SQC
// search endpoint accepts a comma-separated `organizations` query parameter
// containing one or more keys and returns the matching organization records.
func (o *OrganizationsClient) Search(ctx context.Context, keys ...string) ([]types.Organization, error) {
	q := url.Values{}
	if len(keys) > 0 {
		q.Set("organizations", joinNonEmpty(keys, ","))
	}
	var result types.OrganizationsSearchResponse
	path := "api/organizations/search"
	if encoded := q.Encode(); encoded != "" {
		path = path + "?" + encoded
	}
	if err := o.getJSON(ctx, path, &result); err != nil {
		return nil, err
	}
	return result.Organizations, nil
}

// LookupID returns the UUID of the organization whose key matches orgKey, or
// an error if no such organization is visible to the caller.
func (o *OrganizationsClient) LookupID(ctx context.Context, orgKey string) (string, error) {
	if orgKey == "" {
		return "", fmt.Errorf("organization key is required")
	}
	orgs, err := o.Search(ctx, orgKey)
	if err != nil {
		return "", err
	}
	for _, org := range orgs {
		if org.Key == orgKey && org.ID != "" {
			return org.ID, nil
		}
	}
	return "", fmt.Errorf("organization %q not found or has no id", orgKey)
}

// UpdateOrganizationParams describes the fields PATCH
// /organizations/{id} accepts. All fields are optional; only those
// explicitly set are forwarded in the JSON body.
//
// Reference: https://api.sonarcloud.io/openapi.html — PATCH
// /organizations/{organizationId}. The endpoint lives on the
// SonarQube Cloud Enterprise API base (api.sonarcloud.io), so the
// owning client must be constructed with that base URL.
type UpdateOrganizationParams struct {
	Name                  *string
	Description           *string
	NewProjectPrivate     *bool
	OnlyPrivateProjects   *bool
	URL                   *string
	Avatar                *string
	DefaultLeakPeriod     *string // e.g. "30" for 30 days
	DefaultLeakPeriodType *string // "days" | "previous_version" | "reference_branch" | "specific_analysis"
}

// UpdateOrganization patches an organization. Used by the migration
// tool (issue #136) to set defaultLeakPeriodType and defaultLeakPeriod
// from the SonarQube Server platform-wide new-code-period default.
//
// orgRef is the {organizationId} path parameter — SonarCloud accepts
// both the UUID and the human-readable org key. The migration tool
// passes the key because /api/organizations/search doesn't return the
// UUID. Must be called on a client constructed against
// api.sonarcloud.io; the regular sonarcloud.io base does not expose
// /organizations/{id}.
func (o *OrganizationsClient) UpdateOrganization(ctx context.Context, orgRef string, params UpdateOrganizationParams) error {
	if orgRef == "" {
		return fmt.Errorf("organization reference is required")
	}
	body := buildUpdateOrgBody(params)
	if len(body) == 0 {
		// Sending an empty PATCH would either no-op or 400 depending
		// on the server; refuse it client-side so callers notice the
		// missing fields.
		return fmt.Errorf("UpdateOrganization called with no fields to update")
	}
	// SonarCloud's Enterprise API path is /organizations/organizations/{ref}
	// — the doubled segment is real, mirroring /enterprises/enterprises
	// and /enterprises/portfolios for the same Enterprise API base.
	return o.patchJSON(ctx, "organizations/organizations/"+orgRef, body, nil)
}

// buildUpdateOrgBody assembles the JSON body following SonarCloud's
// "only-include-fields-the-caller-set" convention so a PATCH with
// just defaultLeakPeriodType doesn't unintentionally overwrite name,
// description, etc.
func buildUpdateOrgBody(p UpdateOrganizationParams) map[string]any {
	body := map[string]any{}
	if p.Name != nil {
		body["name"] = *p.Name
	}
	if p.Description != nil {
		body["description"] = *p.Description
	}
	if p.NewProjectPrivate != nil {
		body["newProjectPrivate"] = *p.NewProjectPrivate
	}
	if p.OnlyPrivateProjects != nil {
		body["onlyPrivateProjects"] = *p.OnlyPrivateProjects
	}
	if p.URL != nil {
		body["url"] = *p.URL
	}
	if p.Avatar != nil {
		body["avatar"] = *p.Avatar
	}
	if p.DefaultLeakPeriod != nil {
		body["defaultLeakPeriod"] = *p.DefaultLeakPeriod
	}
	if p.DefaultLeakPeriodType != nil {
		body["defaultLeakPeriodType"] = *p.DefaultLeakPeriodType
	}
	return body
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out == "" {
			out = p
			continue
		}
		out += sep + p
	}
	return out
}
