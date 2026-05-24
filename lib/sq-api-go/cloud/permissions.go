package cloud

import (
	"context"
	"net/url"

	"github.com/sonar-solutions/sq-api-go/types"
)

// PermissionsClient provides write-path methods for SonarQube Cloud permissions.
type PermissionsClient struct{ baseClient }

// CreateTemplateParams holds the parameters for creating a permission template.
type CreateTemplateParams struct {
	Name              string
	Description       string
	Organization      string
	ProjectKeyPattern string
}

// CreateTemplate creates a permission template via /api/permissions/create_template.
func (p *PermissionsClient) CreateTemplate(ctx context.Context, params CreateTemplateParams) (*types.PermissionTemplate, error) {
	form := url.Values{}
	form.Set("name", params.Name)
	form.Set("organization", params.Organization)
	if params.Description != "" {
		form.Set("description", params.Description)
	}
	if params.ProjectKeyPattern != "" {
		form.Set("projectKeyPattern", params.ProjectKeyPattern)
	}

	var result types.PermissionTemplateCreateResponse
	if err := p.postForm(ctx, "api/permissions/create_template", form, &result); err != nil {
		return nil, err
	}
	return &result.PermissionTemplate, nil
}

// DeleteTemplate deletes a permission template via /api/permissions/delete_template.
func (p *PermissionsClient) DeleteTemplate(ctx context.Context, templateID, organization string) error {
	form := url.Values{}
	form.Set("templateId", templateID)
	form.Set("organization", organization)
	return p.postForm(ctx, "api/permissions/delete_template", form, nil)
}

// SetDefaultTemplate sets a permission template as the default for a qualifier via
// /api/permissions/set_default_template.
// qualifier is typically "TRK" for projects.
func (p *PermissionsClient) SetDefaultTemplate(ctx context.Context, templateID, qualifier, organization string) error {
	form := url.Values{}
	form.Set("templateId", templateID)
	form.Set("qualifier", qualifier)
	form.Set("organization", organization)
	return p.postForm(ctx, "api/permissions/set_default_template", form, nil)
}

// AddGroup grants a permission to a group at the organization or project level via
// /api/permissions/add_group.
// projectKey is optional; omit for org-level permissions.
func (p *PermissionsClient) AddGroup(ctx context.Context, groupName, permission, organization, projectKey string) error {
	form := url.Values{}
	form.Set("groupName", groupName)
	form.Set("permission", permission)
	form.Set("organization", organization)
	if projectKey != "" {
		form.Set("projectKey", projectKey)
	}
	return p.postForm(ctx, "api/permissions/add_group", form, nil)
}

// AddGroupToTemplate grants a permission to a group within a permission template via
// /api/permissions/add_group_to_template.
func (p *PermissionsClient) AddGroupToTemplate(ctx context.Context, templateID, groupName, permission, organization string) error {
	form := url.Values{}
	form.Set("templateId", templateID)
	form.Set("groupName", groupName)
	form.Set("permission", permission)
	form.Set("organization", organization)
	return p.postForm(ctx, "api/permissions/add_group_to_template", form, nil)
}

// AddUser grants a permission to a user at the organization or
// project level via /api/permissions/add_user. login is the SQC
// user's login (NOT the display name). projectKey is optional;
// omit for org-level grants. Used by the migration to grant the
// migration user elevated perms on every newly-created project
// (issue #190) so subsequent per-project mutations don't fail with
// "Insufficient privileges".
func (p *PermissionsClient) AddUser(ctx context.Context, login, permission, organization, projectKey string) error {
	form := url.Values{}
	form.Set("login", login)
	form.Set("permission", permission)
	form.Set("organization", organization)
	if projectKey != "" {
		form.Set("projectKey", projectKey)
	}
	return p.postForm(ctx, "api/permissions/add_user", form, nil)
}
