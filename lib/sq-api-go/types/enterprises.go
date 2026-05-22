package types

// Enterprise represents a SonarQube Cloud enterprise returned by
// GET /enterprises/enterprises.
type Enterprise struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// EnterprisesListResponse is the response envelope for GET /enterprises/enterprises.
type EnterprisesListResponse struct {
	Enterprises []Enterprise `json:"enterprises"`
}

// Portfolio represents a SonarQube Cloud portfolio returned by the
// /enterprises/portfolios endpoints.
//
// Projects is left as json.RawMessage because the API alternates shapes:
// the create response can echo back a string list, while the list/get
// response embeds {branchId, id} objects. Consumers that only need
// existence + identity (the migration tool's case) can safely ignore it.
type Portfolio struct {
	ID                string   `json:"id"`
	Key               string   `json:"key,omitempty"`
	Name              string   `json:"name"`
	Description       string   `json:"description,omitempty"`
	EnterpriseID      string   `json:"enterpriseId,omitempty"`
	Selection         string   `json:"selection,omitempty"`
	Tags              []string `json:"tags,omitempty"`
	OrganizationIDs   []string `json:"organizationIds,omitempty"`
	RegularExpression string   `json:"regularExpression,omitempty"`
	BranchKey         string   `json:"branchKey,omitempty"`
	ProjectsMatched   int      `json:"projectsMatched,omitempty"`
	ProjectCount      int      `json:"projectCount,omitempty"`
	IsDraft           bool     `json:"isDraft,omitempty"`
	DraftStage        int      `json:"draftStage,omitempty"`
}

// PortfoliosPage describes the pagination envelope returned alongside a
// portfolios list response.
type PortfoliosPage struct {
	PageIndex int `json:"pageIndex"`
	PageSize  int `json:"pageSize"`
	Total     int `json:"total"`
}

// PortfoliosListResponse is the response envelope for
// GET /enterprises/portfolios.
type PortfoliosListResponse struct {
	Portfolios []Portfolio    `json:"portfolios"`
	Page       PortfoliosPage `json:"page"`
}
