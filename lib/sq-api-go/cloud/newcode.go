// Package cloud — newcode.go covers the /api/new_code_periods endpoints used
// by the sonar-migration-tool to migrate per-branch new-code policy.
package cloud

import (
	"context"
	"net/url"
)

// NewCodePeriodsClient provides write-path methods for SonarQube Cloud's
// /api/new_code_periods endpoints.
type NewCodePeriodsClient struct{ baseClient }

// SetNewCodePeriodParams describes the per-branch new-code policy a caller
// wants to apply on SonarQube Cloud.
//
//   - Project is required.
//   - Branch is optional; when empty the call sets the project-level
//     default that branches inherit from.
//   - Type must be one of "previous_version", "days",
//     "specific_analysis", or "reference_branch".
//   - Value is required for every type EXCEPT "previous_version", where it
//     must be empty (the type alone fully describes the policy).
//   - Organization is the SonarQube Cloud organization key.
type SetNewCodePeriodParams struct {
	Project      string
	Branch       string
	Type         string
	Value        string
	Organization string
}

// Set applies a new-code period policy via
// POST /api/new_code_periods/set.
func (n *NewCodePeriodsClient) Set(ctx context.Context, params SetNewCodePeriodParams) error {
	form := url.Values{}
	if params.Project != "" {
		form.Set("project", params.Project)
	}
	if params.Branch != "" {
		form.Set("branch", params.Branch)
	}
	if params.Type != "" {
		form.Set("type", params.Type)
	}
	if params.Value != "" {
		form.Set("value", params.Value)
	}
	if params.Organization != "" {
		form.Set("organization", params.Organization)
	}
	return n.postForm(ctx, "api/new_code_periods/set", form, nil)
}
