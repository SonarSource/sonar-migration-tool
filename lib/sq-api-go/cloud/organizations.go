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
