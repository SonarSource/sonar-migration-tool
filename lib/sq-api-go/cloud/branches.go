// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cloud

import (
	"context"
	"fmt"
	"net/url"

	"github.com/sonar-solutions/sq-api-go/types"
)

// BranchesClient provides write-path methods for SonarQube Cloud project branches.
type BranchesClient struct{ baseClient }

// Rename renames the main branch of a project via /api/project_branches/rename.
func (b *BranchesClient) Rename(ctx context.Context, projectKey, name string) error {
	form := url.Values{}
	form.Set("project", projectKey)
	form.Set("name", name)
	return b.postForm(ctx, "api/project_branches/rename", form, nil)
}

// Delete deletes a non-main branch via /api/project_branches/delete.
func (b *BranchesClient) Delete(ctx context.Context, projectKey, branch string) error {
	form := url.Values{}
	form.Set("project", projectKey)
	form.Set("branch", branch)
	return b.postForm(ctx, "api/project_branches/delete", form, nil)
}

// List returns all branches of the given project via
// GET /api/project_branches/list.
func (b *BranchesClient) List(ctx context.Context, projectKey string) ([]types.Branch, error) {
	q := url.Values{}
	q.Set("project", projectKey)
	var result types.BranchesResponse
	if err := b.getJSON(ctx, "api/project_branches/list?"+q.Encode(), &result); err != nil {
		return nil, err
	}
	return result.Branches, nil
}

// MainBranchID returns the UUID (branchId) of the project's main branch. It
// is the value SonarQube Cloud requires in projects[].branchId on the
// enterprise portfolios PATCH endpoint when selection is "projects".
func (b *BranchesClient) MainBranchID(ctx context.Context, projectKey string) (string, error) {
	branches, err := b.List(ctx, projectKey)
	if err != nil {
		return "", err
	}
	for _, br := range branches {
		if br.IsMain && br.BranchID != "" {
			return br.BranchID, nil
		}
	}
	return "", fmt.Errorf("project %q has no main branch UUID", projectKey)
}
