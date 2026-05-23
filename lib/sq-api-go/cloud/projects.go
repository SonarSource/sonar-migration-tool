package cloud

import (
	"context"
	"net/url"

	"github.com/sonar-solutions/sq-api-go/types"
)

// ProjectsClient provides write-path methods for SonarQube Cloud projects.
type ProjectsClient struct{ baseClient }

// CreateProjectParams holds the parameters for creating a Cloud project.
type CreateProjectParams struct {
	// ProjectKey is the project key on Cloud. For org-scoped projects this is
	// typically "<org>_<serverKey>".
	ProjectKey   string
	Name         string
	Organization string
	Visibility   string
	// NewCodeDefinitionType and NewCodeDefinitionValue are optional new-code
	// period settings. SonarQube Cloud /api/projects/create rejects requests
	// that include type without value (HTTP 400: "Both newCodeDefinitionType
	// and newCodeDefinitionValue must be provided"), so we only forward the
	// pair when BOTH are non-empty. "previous_version" — which has no value —
	// is therefore omitted entirely and the project inherits the SQC default
	// (which is previous_version); project-level NCD can be set later via
	// /api/new_code_periods/set.
	NewCodeDefinitionType  string
	NewCodeDefinitionValue string
}

// Create creates a new project via /api/projects/create and returns its details.
func (p *ProjectsClient) Create(ctx context.Context, params CreateProjectParams) (*types.Project, error) {
	form := url.Values{}
	form.Set("project", params.ProjectKey)
	form.Set("name", params.Name)
	form.Set("organization", params.Organization)
	if params.Visibility != "" {
		form.Set("visibility", params.Visibility)
	}
	if params.NewCodeDefinitionType != "" && params.NewCodeDefinitionValue != "" {
		form.Set("newCodeDefinitionType", params.NewCodeDefinitionType)
		form.Set("newCodeDefinitionValue", params.NewCodeDefinitionValue)
	}

	var result types.ProjectCreateResponse
	if err := p.postForm(ctx, "api/projects/create", form, &result); err != nil {
		return nil, err
	}
	return &result.Project, nil
}

// Delete deletes a project via /api/projects/delete.
func (p *ProjectsClient) Delete(ctx context.Context, projectKey string) error {
	form := url.Values{}
	form.Set("project", projectKey)
	return p.postForm(ctx, "api/projects/delete", form, nil)
}

// SetTags sets the tags on a project via /api/project_tags/set.
// tags is a comma-separated string (e.g. "java,backend").
func (p *ProjectsClient) SetTags(ctx context.Context, projectKey, tags string) error {
	form := url.Values{}
	form.Set("project", projectKey)
	form.Set("tags", tags)
	return p.postForm(ctx, "api/project_tags/set", form, nil)
}

// ExistsInOrg reports whether a project with the given key is
// accessible in the given SonarQube Cloud organization. Used to
// disambiguate /api/projects/create's "key already exists" response
// (issue #193): SQC project keys are globally unique, so a 400
// rejection doesn't tell us whether the existing project is in our
// target org or some other org that happens to claim the same key.
// A positive result confirms our migration can adopt the existing
// project; a negative result means createProjects should treat the
// case as a failure and skip the project from downstream tasks.
//
// Implementation: GET /api/projects/search filtered to (org, projects).
// SQC returns the project in the response only when both match.
func (p *ProjectsClient) ExistsInOrg(ctx context.Context, projectKey, organization string) (bool, error) {
	q := url.Values{}
	q.Set("organization", organization)
	q.Set("projects", projectKey)
	var resp types.ProjectsSearchResponse
	if err := p.getJSON(ctx, "api/projects/search?"+q.Encode(), &resp); err != nil {
		return false, err
	}
	for _, proj := range resp.Components {
		if proj.Key == projectKey {
			return true, nil
		}
	}
	return false, nil
}
