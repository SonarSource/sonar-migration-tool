package structure

import (
	"encoding/json"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// Cloud endpoint patterns for ALM binding detection.
var cloudEndpoints = []string{"dev.azure.com", "gitlab.com", "api.github.com", "bitbucket.org"}

// newCodeMappings maps SonarQube Server NCD types to the equivalent
// SonarQube Cloud "type" values accepted by /api/projects/create and
// /api/new_code_periods/set. Only the types that SonarQube Cloud
// actually accepts are listed; REFERENCE_BRANCH and SPECIFIC_ANALYSIS
// do NOT exist on SQC (issue #135), so projects whose main-branch NCD
// uses one of those types fall back to the SQC org default by
// returning a zero newCodeDefinition from getNewCodeDef. The
// limitation is surfaced in the PDF report via collectLimitations.
var newCodeMappings = map[string]string{
	"NUMBER_OF_DAYS":   "days",
	"PREVIOUS_VERSION": "previous_version",
}

// IsCloudBinding checks whether a binding URL matches a known cloud endpoint.
func IsCloudBinding(bindingURL string) bool {
	for _, ep := range cloudEndpoints {
		if strings.Contains(bindingURL, ep) {
			return true
		}
	}
	return false
}

// GenerateUniqueProjectKey generates a unique project key.
// For ALM-bound non-monorepo projects: "{alm}_{repository}"
// Otherwise: "{server_url}{key}"
func GenerateUniqueProjectKey(serverURL, key, alm, repository string, monorepo bool) string {
	if repository != "" && alm != "" && !monorepo {
		return alm + "_" + repository
	}
	return serverURL + key
}

// GenerateUniqueBindingKey generates a unique binding/organization key.
func GenerateUniqueBindingKey(serverURL, key, alm, bindingURL, repository string) string {
	if alm == "" || bindingURL == "" {
		return serverURL
	}
	baseURL := stripScheme(bindingURL)
	var orgKey string
	switch alm {
	case "gitlab":
		orgKey = key + " - " + serverURL
	case "github":
		parts := strings.SplitN(repository, "/", 2)
		orgKey = parts[0]
	default: // azure, bitbucket
		trimmed := strings.TrimRight(bindingURL, "/")
		parts := strings.Split(trimmed, "/")
		orgKey = parts[len(parts)-1]
	}
	return baseURL + "/" + orgKey
}

// stripScheme removes https:// or http:// and returns the host portion.
func stripScheme(url string) string {
	url = strings.Replace(url, "https://", "", 1)
	url = strings.Replace(url, "http://", "", 1)
	parts := strings.SplitN(url, "/", 2)
	return parts[0]
}

// newCodeDefinition holds a mapped new code definition.
type newCodeDefinition struct {
	Type  string
	Value any
}

// MapNewCodeDefinitions reads new code period data from extracts.
// Returns map[serverURL][projectKey][branchKey] → newCodeDefinition
func MapNewCodeDefinitions(directory string, mapping ExtractMapping) map[string]map[string]map[string]newCodeDefinition {
	result := make(map[string]map[string]map[string]newCodeDefinition)
	items, _ := ReadExtractData(directory, mapping, "getNewCodePeriods")

	for _, item := range items {
		ncdType := common.ExtractField(item.Data, "type")
		mappedType, ok := newCodeMappings[ncdType]
		if !ok {
			continue
		}
		ncdValue := extractAnyField(item.Data, "value")
		if ncdValue == nil {
			ncdValue = 30
		}
		projectKey := common.ExtractField(item.Data, "projectKey")
		branchKey := common.ExtractField(item.Data, "branchKey")

		// SonarQube Cloud expects no value for the "previous_version" mode —
		// the type alone fully describes the policy. Previous releases of
		// this tool serialised the literal string "previous_version" as the
		// value, which leaked into projects.csv and then into the SQC
		// create payload as newCodeDefinitionValue=previous_version. Leave
		// the value empty here so the migrate-side check skips that param.
		value := ncdValue
		if ncdType == "PREVIOUS_VERSION" {
			value = nil
		}

		if result[item.ServerURL] == nil {
			result[item.ServerURL] = make(map[string]map[string]newCodeDefinition)
		}
		if result[item.ServerURL][projectKey] == nil {
			result[item.ServerURL][projectKey] = make(map[string]newCodeDefinition)
		}
		result[item.ServerURL][projectKey][branchKey] = newCodeDefinition{
			Type:  mappedType,
			Value: value,
		}
	}
	return result
}

// MapProjectStructure reads extract data and produces unique bindings and projects.
func MapProjectStructure(directory string, mapping ExtractMapping) ([]Binding, []Project) {
	newCodeDefs := MapNewCodeDefinitions(directory, mapping)

	// Build binding map: serverURL+key → binding data.
	bindingItems, _ := ReadExtractData(directory, mapping, "getBindings")
	bindingMap := make(map[string]map[string]any)
	for _, item := range bindingItems {
		key := common.ExtractField(item.Data, "key")
		var obj map[string]any
		json.Unmarshal(item.Data, &obj)
		bindingMap[item.ServerURL+key] = obj
	}

	// Build project binding map: serverURL+projectKey → {binding, project_binding}.
	projBindingItems, _ := ReadExtractData(directory, mapping, "getProjectBindings")
	projectBindings := make(map[string]projectBindingEntry)
	for _, item := range projBindingItems {
		projectKey := common.ExtractField(item.Data, "projectKey")
		bindingKey := common.ExtractField(item.Data, "key")
		var pb map[string]any
		json.Unmarshal(item.Data, &pb)

		binding := bindingMap[item.ServerURL+bindingKey]
		if binding == nil {
			binding = map[string]any{"key": item.ServerURL}
		}
		projectBindings[item.ServerURL+projectKey] = projectBindingEntry{
			Binding:        binding,
			ProjectBinding: pb,
		}
	}

	// Build projects and unique bindings.
	uniqueBindings := make(map[string]*Binding)
	projects := make(map[string]Project)

	detailItems, _ := ReadExtractData(directory, mapping, "getProjectDetails")
	for _, item := range detailItems {
		var detail map[string]any
		json.Unmarshal(item.Data, &detail)
		projectKey := getString(detail, "key")

		pb := projectBindings[item.ServerURL+projectKey]
		if pb.Binding == nil {
			pb.Binding = map[string]any{"key": item.ServerURL}
		}

		bindingKeyStr := getString(pb.Binding, "key")
		bindingALM := getString(pb.Binding, "alm")
		bindingURL := getString(pb.Binding, "url")
		pbRepo := getNestedString(pb.ProjectBinding, "repository")

		uniqueBindingKey := GenerateUniqueBindingKey(
			item.ServerURL, bindingKeyStr, bindingALM, bindingURL, pbRepo,
		)

		if uniqueBindings[uniqueBindingKey] == nil {
			uniqueBindings[uniqueBindingKey] = &Binding{
				Key:        uniqueBindingKey,
				BindingKey: bindingKeyStr,
				ALM:        bindingALM,
				URL:        bindingURL,
				ServerURL:  item.ServerURL,
				IsCloud:    IsCloudBinding(bindingURL),
			}
		}
		uniqueBindings[uniqueBindingKey].ProjectCount++

		pbMonorepo := getNestedBool(pb.ProjectBinding, "monorepo")
		uniqueProjectKey := GenerateUniqueProjectKey(
			item.ServerURL, projectKey, bindingALM, pbRepo, pbMonorepo,
		)

		branchName := getString(detail, "branch")
		if branchName == "" {
			branchName = "master"
		}

		ncd := getNewCodeDef(newCodeDefs, item.ServerURL, projectKey, branchName)

		projects[uniqueProjectKey] = Project{
			Key:                    projectKey,
			Name:                   getString(detail, "name"),
			GateName:               getMapString(detail, "qualityGate", "name"),
			Profiles:               detail["qualityProfiles"],
			ServerURL:              item.ServerURL,
			SonarQubeOrgKey:        uniqueBindingKey,
			MainBranch:             branchName,
			IsCloudBinding:         IsCloudBinding(bindingURL),
			NewCodeDefinitionType:  ncd.Type,
			NewCodeDefinitionValue: ncd.Value,
			ALM:                    bindingALM,
			Repository:             pbRepo,
			Slug:                   getNestedString(pb.ProjectBinding, "slug"),
			Monorepo:               pbMonorepo,
			SummaryCommentEnabled:  getNestedBool(pb.ProjectBinding, "summaryCommentEnabled"),
		}
	}

	bindingList := make([]Binding, 0, len(uniqueBindings))
	for _, b := range uniqueBindings {
		bindingList = append(bindingList, *b)
	}
	projectList := make([]Project, 0, len(projects))
	for _, p := range projects {
		projectList = append(projectList, p)
	}
	return bindingList, projectList
}

type projectBindingEntry struct {
	Binding        map[string]any
	ProjectBinding map[string]any
}

func getNewCodeDef(defs map[string]map[string]map[string]newCodeDefinition, serverURL, projectKey, branch string) newCodeDefinition {
	if s, ok := defs[serverURL]; ok {
		if p, ok := s[projectKey]; ok {
			if b, ok := p[branch]; ok {
				return b
			}
		}
	}
	return newCodeDefinition{Type: "days", Value: 30}
}

// Helper functions for safely extracting values from map[string]any.

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return ""
}

func getNestedString(m map[string]any, key string) string {
	return getString(m, key)
}

func getNestedBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	b, ok := v.(bool)
	if ok {
		return b
	}
	return false
}

func getMapString(m map[string]any, mapKey, fieldKey string) string {
	if m == nil {
		return ""
	}
	sub, ok := m[mapKey]
	if !ok || sub == nil {
		return ""
	}
	subMap, ok := sub.(map[string]any)
	if !ok {
		return ""
	}
	return getString(subMap, fieldKey)
}

// extractAnyField extracts a value from a json.RawMessage by key, returning any type.
func extractAnyField(raw json.RawMessage, key string) any {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	val, ok := obj[key]
	if !ok {
		return nil
	}
	var result any
	if err := json.Unmarshal(val, &result); err != nil {
		return nil
	}
	return result
}
