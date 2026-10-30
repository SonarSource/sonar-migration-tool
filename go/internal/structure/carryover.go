// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

// CarriedKeys holds the sonarcloud_org_key values an operator filled in by
// hand in organizations.csv and projects.csv, so regenerating those files (a
// later extract into the same export directory, #664) does not blank them.
type CarriedKeys struct {
	orgs     map[string]string
	projects map[string]string
	// hadOrgs reports that the previous organizations.csv had rows.
	hadOrgs bool
}

// CarryStats says what carrying the keys over achieved.
type CarryStats struct {
	// Carried is the number of previous values written back into the
	// regenerated files.
	Carried int
	// Dropped is the number of previous non-empty values whose row no
	// longer exists, so the value could not be carried.
	Dropped int
	// OrgsWithoutKey is the number of rows in the regenerated
	// organizations.csv that still have no sonarcloud_org_key, when a
	// previous organizations.csv existed (new rows the operator must fill).
	OrgsWithoutKey int
}

// NeedsReview reports whether the operator has something to check.
func (s CarryStats) NeedsReview() bool { return s.Dropped > 0 || s.OrgsWithoutKey > 0 }

func orgRowKey(serverURL, sqOrgKey, bindingKey string) string {
	return serverURL + "\x00" + sqOrgKey + "\x00" + bindingKey
}

func projectRowKey(serverURL, key string) string { return serverURL + "\x00" + key }

// ReadCarriedKeys reads the existing organizations.csv and projects.csv of
// dir. It must be called before the files are regenerated. A missing or
// empty file contributes nothing.
func ReadCarriedKeys(dir string) (*CarriedKeys, error) {
	c := &CarriedKeys{orgs: map[string]string{}, projects: map[string]string{}}
	orgRows, err := LoadCSV(dir, OrganizationsCSVFileName)
	if err != nil {
		return nil, err
	}
	c.hadOrgs = len(orgRows) > 0
	for _, row := range orgRows {
		if v := str(row, "sonarcloud_org_key"); v != "" {
			c.orgs[orgRowKey(str(row, "server_url"), str(row, "sonarqube_org_key"), str(row, "binding_key"))] = v
		}
	}
	projectRows, err := LoadCSV(dir, ProjectsCSVFileName)
	if err != nil {
		return nil, err
	}
	for _, row := range projectRows {
		if v := str(row, "sonarcloud_org_key"); v != "" {
			c.projects[projectRowKey(str(row, "server_url"), str(row, "key"))] = v
		}
	}
	return c, nil
}

func str(row map[string]any, field string) string {
	s, _ := row[field].(string)
	return s
}

// Apply writes the carried values into the regenerated rows and reports the
// outcome. A carried value always wins over a default org key pre-filled in
// the row, because it is an operator edit; rows with no carried value keep
// whatever they were generated with. Safe on a nil receiver.
func (c *CarriedKeys) Apply(orgs []Organization, projects []Project) CarryStats {
	var stats CarryStats
	if c == nil {
		return stats
	}
	usedOrgs := map[string]bool{}
	for i := range orgs {
		k := orgRowKey(orgs[i].ServerURL, orgs[i].SonarQubeOrgKey, orgs[i].BindingKey)
		if v, ok := c.orgs[k]; ok {
			orgs[i].SonarCloudOrgKey = v
			usedOrgs[k] = true
			stats.Carried++
		}
		if c.hadOrgs && orgs[i].SonarCloudOrgKey == "" {
			stats.OrgsWithoutKey++
		}
	}
	usedProjects := map[string]bool{}
	for i := range projects {
		k := projectRowKey(projects[i].ServerURL, projects[i].Key)
		if v, ok := c.projects[k]; ok {
			projects[i].SonarCloudOrgKey = v
			usedProjects[k] = true
			stats.Carried++
		}
	}
	stats.Dropped = (len(c.orgs) - len(usedOrgs)) + (len(c.projects) - len(usedProjects))
	return stats
}
