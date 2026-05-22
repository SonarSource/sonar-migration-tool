package types

// Organization represents a SonarQube Cloud organization as returned by
// GET /api/organizations/search.
//
// The portfolio PATCH endpoint expects organization references as UUIDs
// (the ID field below), not as user-facing keys.
type Organization struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// OrganizationsSearchResponse is the response envelope for
// GET /api/organizations/search.
type OrganizationsSearchResponse struct {
	Organizations []Organization `json:"organizations"`
}
