package structure

// MapOrganizationStructure converts bindings to organizations.
// When defaultOrgKey is non-empty it pre-populates sonarcloud_org_key for every
// row — useful when a single SonarCloud org is defined in the config file.
func MapOrganizationStructure(bindings []Binding, defaultOrgKey ...string) []Organization {
	orgKey := ""
	if len(defaultOrgKey) > 0 {
		orgKey = defaultOrgKey[0]
	}
	var orgs []Organization
	for _, b := range bindings {
		if b.ProjectCount < 1 {
			continue
		}
		orgs = append(orgs, Organization{
			SonarQubeOrgKey:  b.Key,
			SonarCloudOrgKey: orgKey,
			BindingKey:       b.BindingKey,
			ServerURL:        b.ServerURL,
			ALM:              b.ALM,
			URL:              b.URL,
			IsCloud:          b.IsCloud,
			ProjectCount:     b.ProjectCount,
		})
	}
	return orgs
}
