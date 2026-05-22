package structure

// Organization represents a SonarQube Cloud organization derived from DevOps bindings.
type Organization struct {
	SonarQubeOrgKey  string `csv:"sonarqube_org_key" json:"sonarqube_org_key"`
	SonarCloudOrgKey string `csv:"sonarcloud_org_key" json:"sonarcloud_org_key"`
	BindingKey       string `csv:"binding_key" json:"binding_key"`
	ServerURL        string `csv:"server_url" json:"server_url"`
	ALM              string `csv:"alm" json:"alm"`
	URL              string `csv:"url" json:"url"`
	IsCloud          bool   `csv:"is_cloud" json:"is_cloud"`
	ProjectCount     int    `csv:"project_count" json:"project_count"`
}

// Project represents a mapped project with its org assignment and metadata.
type Project struct {
	Key                    string `csv:"key" json:"key"`
	Name                   string `csv:"name" json:"name"`
	GateName               string `csv:"gate_name" json:"gate_name"`
	Profiles               any    `csv:"profiles" json:"profiles"`
	ServerURL              string `csv:"server_url" json:"server_url"`
	SonarQubeOrgKey        string `csv:"sonarqube_org_key" json:"sonarqube_org_key"`
	MainBranch             string `csv:"main_branch" json:"main_branch"`
	IsCloudBinding         bool   `csv:"is_cloud_binding" json:"is_cloud_binding"`
	NewCodeDefinitionType  string `csv:"new_code_definition_type" json:"new_code_definition_type"`
	NewCodeDefinitionValue any    `csv:"new_code_definition_value" json:"new_code_definition_value"`
	ALM                    string `csv:"alm" json:"alm"`
	Repository             string `csv:"repository" json:"repository"`
	Slug                   string `csv:"slug" json:"slug"`
	Monorepo               bool   `csv:"monorepo" json:"monorepo"`
	SummaryCommentEnabled  bool   `csv:"summary_comment_enabled" json:"summary_comment_enabled"`
}

// Profile represents a mapped quality profile with org assignment.
type Profile struct {
	UniqueKey        string `csv:"unique_key" json:"unique_key"`
	Name             string `csv:"name" json:"name"`
	Language         string `csv:"language" json:"language"`
	ParentName       string `csv:"parent_name" json:"parent_name"`
	ServerURL        string `csv:"server_url" json:"server_url"`
	IsDefault        bool   `csv:"is_default" json:"is_default"`
	SourceProfileKey string `csv:"source_profile_key" json:"source_profile_key"`
	SonarQubeOrgKey  string `csv:"sonarqube_org_key" json:"sonarqube_org_key"`
}

// Gate represents a mapped quality gate with org assignment.
type Gate struct {
	Name            string `csv:"name" json:"name"`
	ServerURL       string `csv:"server_url" json:"server_url"`
	SourceGateKey   string `csv:"source_gate_key" json:"source_gate_key"`
	IsDefault       bool   `csv:"is_default" json:"is_default"`
	SonarQubeOrgKey string `csv:"sonarqube_org_key" json:"sonarqube_org_key"`
}

// Group represents a mapped user group with org assignment.
type Group struct {
	Name            string `csv:"name" json:"name"`
	ServerURL       string `csv:"server_url" json:"server_url"`
	Description     string `csv:"description" json:"description"`
	SonarQubeOrgKey string `csv:"sonarqube_org_key" json:"sonarqube_org_key"`
}

// Template represents a mapped permission template with org assignment.
type Template struct {
	UniqueKey         string `csv:"unique_key" json:"unique_key"`
	SourceTemplateKey string `csv:"source_template_key" json:"source_template_key"`
	Name              string `csv:"name" json:"name"`
	Description       string `csv:"description" json:"description"`
	ProjectKeyPattern string `csv:"project_key_pattern" json:"project_key_pattern"`
	ServerURL         string `csv:"server_url" json:"server_url"`
	IsDefault         bool   `csv:"is_default" json:"is_default"`
	SonarQubeOrgKey   string `csv:"sonarqube_org_key" json:"sonarqube_org_key"`
}

// Portfolio represents a mapped portfolio.
//
// SelectionMode / RegularExpression / Tags reflect how the source portfolio
// composed its project set on SonarQube Server; they are used during
// migration to set up an equivalent selection on SonarQube Cloud (e.g. a
// REGEXP portfolio on SQS becomes a regexp-selection portfolio on SQC with
// the regex transformed to account for project-key renaming).
type Portfolio struct {
	SourcePortfolioKey string `csv:"source_portfolio_key" json:"source_portfolio_key"`
	Name               string `csv:"name" json:"name"`
	ServerURL          string `csv:"server_url" json:"server_url"`
	Description        string `csv:"description" json:"description"`
	SelectionMode      string `csv:"selection_mode" json:"selection_mode"`
	RegularExpression  string `csv:"regexp" json:"regexp"`
	Tags               string `csv:"tags" json:"tags"`
}

// Binding is an intermediate struct for unique DevOps bindings.
type Binding struct {
	Key          string
	BindingKey   string
	ALM          string
	URL          string
	ServerURL    string
	IsCloud      bool
	ProjectCount int
}
