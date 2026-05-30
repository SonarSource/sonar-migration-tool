package cloud

import (
	"context"
	"net/url"
	"strconv"

	"github.com/sonar-solutions/sq-api-go/types"
)

// GroupsClient provides write-path methods for SonarQube Cloud user groups.
type GroupsClient struct{ baseClient }

// CreateGroupParams holds the parameters for creating a Cloud group.
type CreateGroupParams struct {
	Name         string
	Description  string
	Organization string
}

// Create creates a new group via /api/user_groups/create and returns its details.
func (g *GroupsClient) Create(ctx context.Context, params CreateGroupParams) (*types.Group, error) {
	form := url.Values{}
	form.Set("name", params.Name)
	form.Set("organization", params.Organization)
	if params.Description != "" {
		form.Set("description", params.Description)
	}

	var result types.GroupCreateResponse
	if err := g.postForm(ctx, "api/user_groups/create", form, &result); err != nil {
		return nil, err
	}
	return &result.Group, nil
}

// Delete deletes a group by ID via /api/user_groups/delete.
func (g *GroupsClient) Delete(ctx context.Context, groupID int, organization string) error {
	form := url.Values{}
	form.Set("id", strconv.Itoa(groupID))
	form.Set("organization", organization)
	return g.postForm(ctx, "api/user_groups/delete", form, nil)
}

// DeleteByName deletes a group by name via /api/user_groups/delete.
// SonarQube Cloud accepts either `id` or `name` as the identifier;
// the migration-group cleanup path doesn't carry IDs (the create
// task only records names) so the name form is the simpler choice.
func (g *GroupsClient) DeleteByName(ctx context.Context, name, organization string) error {
	form := url.Values{}
	form.Set("name", name)
	form.Set("organization", organization)
	return g.postForm(ctx, "api/user_groups/delete", form, nil)
}

// List returns every group visible in the given SonarQube Cloud
// organization. Paginates through /api/user_groups/search until the
// result set is exhausted. Used by the reset path to enumerate
// groups for deletion (the migrate-side createGroups JSONL lives in
// a different run directory and is not accessible from a fresh
// reset run).
func (g *GroupsClient) List(ctx context.Context, organization string) ([]types.Group, error) {
	var all []types.Group
	page := 1
	for {
		q := url.Values{}
		q.Set("organization", organization)
		q.Set("p", strconv.Itoa(page))
		q.Set("ps", "500")
		var resp types.GroupsSearchResponse
		if err := g.getJSON(ctx, "api/user_groups/search?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Groups...)
		if len(resp.Groups) == 0 || resp.Paging.PageIndex*resp.Paging.PageSize >= resp.Paging.Total {
			break
		}
		page++
	}
	return all, nil
}

// AddUser adds a user to a group via /api/user_groups/add_user.
func (g *GroupsClient) AddUser(ctx context.Context, groupName, login, organization string) error {
	form := url.Values{}
	form.Set("name", groupName)
	form.Set("login", login)
	form.Set("organization", organization)
	return g.postForm(ctx, "api/user_groups/add_user", form, nil)
}
