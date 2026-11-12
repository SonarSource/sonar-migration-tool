// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// errLookupNotFound is returned (wrapped) by the lookupExisting* helpers when
// the target answered but holds no entity of that name, as opposed to a
// transport or HTTP failure.
var errLookupNotFound = errors.New("not found")

// lookupExistingProject returns the cloud project key for a project that
// already exists. The key is deterministic (orgKey + "_" + sourceKey) so no
// API call is needed.
func lookupExistingProject(orgKey, sourceKey string) string {
	return orgKey + "_" + sourceKey
}

// lookupExistingProfile searches Cloud for a quality profile by name and
// language, returning its key.
func lookupExistingProfile(ctx context.Context, raw *common.RawClient, name, language, orgKey string) (string, error) {
	params := url.Values{
		"qualityProfile": {name},
		"language":       {language},
		"organization":   {orgKey},
	}
	body, err := raw.Get(ctx, "api/qualityprofiles/search", params)
	if err != nil {
		return "", fmt.Errorf("lookupExistingProfile: %w", err)
	}
	key, err := findByField(body, "profiles", "name", name, "key")
	if err != nil {
		return "", fmt.Errorf("lookupExistingProfile %q (%s): %w", name, language, err)
	}
	return key, nil
}

// lookupExistingGate searches Cloud for a quality gate by name, returning its ID.
func lookupExistingGate(ctx context.Context, raw *common.RawClient, name, orgKey string) (string, error) {
	params := url.Values{"organization": {orgKey}}
	body, err := raw.Get(ctx, "api/qualitygates/list", params)
	if err != nil {
		return "", fmt.Errorf("lookupExistingGate: %w", err)
	}
	id, err := findByField(body, "qualitygates", "name", name, "id")
	if err != nil {
		return "", fmt.Errorf("lookupExistingGate %q: %w", name, err)
	}
	return id, nil
}

// groupSearchPageSize is the page size lookupExistingGroup requests from
// api/user_groups/search.
const groupSearchPageSize = 100

// groupSearchMaxPages bounds lookupExistingGroup's paging, so a server that
// misreports its paging can never keep it looping.
const groupSearchMaxPages = 1000

// lookupExistingGroup searches Cloud for a user group by name, returning its ID.
//
// q= matches every group whose name CONTAINS name, so in an organization
// with many groups the exact match can sit past the first page (a short
// name like "dev" among hundreds of "*-dev-*" groups). It therefore pages
// through the results until it finds the exact, case-sensitive match or
// runs out of pages.
func lookupExistingGroup(ctx context.Context, raw *common.RawClient, name, orgKey string) (string, error) {
	for page := 1; page <= groupSearchMaxPages; page++ {
		params := url.Values{
			"q":            {name},
			"organization": {orgKey},
			"ps":           {strconv.Itoa(groupSearchPageSize)},
			"p":            {strconv.Itoa(page)},
		}
		body, err := raw.Get(ctx, "api/user_groups/search", params)
		if err != nil {
			return "", fmt.Errorf("lookupExistingGroup: %w", err)
		}
		id, err := findByField(body, "groups", "name", name, "id")
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, errLookupNotFound) || isLastGroupSearchPage(body, page) {
			return "", fmt.Errorf("lookupExistingGroup %q: %w", name, err)
		}
	}
	return "", fmt.Errorf("lookupExistingGroup %q: gave up after %d pages: %w", name, groupSearchMaxPages, errLookupNotFound)
}

// isLastGroupSearchPage reports whether page is the last page of an
// api/user_groups/search response: a short page, or paging.total reached.
func isLastGroupSearchPage(body json.RawMessage, page int) bool {
	var resp struct {
		Paging struct {
			Total int `json:"total"`
		} `json:"paging"`
		Groups []json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return true
	}
	return len(resp.Groups) < groupSearchPageSize || page*groupSearchPageSize >= resp.Paging.Total
}

// lookupExistingTemplate searches Cloud for a permission template by name
// (case-insensitive), returning its ID.
func lookupExistingTemplate(ctx context.Context, raw *common.RawClient, name, orgKey string) (string, error) {
	params := url.Values{"organization": {orgKey}}
	body, err := raw.Get(ctx, "api/permissions/search_templates", params)
	if err != nil {
		return "", fmt.Errorf("lookupExistingTemplate: %w", err)
	}
	id, err := findByFieldFold(body, "permissionTemplates", "name", name, "id")
	if err != nil {
		return "", fmt.Errorf("lookupExistingTemplate %q: %w", name, err)
	}
	return id, nil
}

// findByField searches a JSON array at arrayKey for an object where matchField
// equals matchValue, and returns the string value of returnField.
func findByField(body json.RawMessage, arrayKey, matchField, matchValue, returnField string) (string, error) {
	return findByFieldImpl(body, arrayKey, matchField, matchValue, returnField, false)
}

// findByFieldFold is like findByField but uses case-insensitive matching.
func findByFieldFold(body json.RawMessage, arrayKey, matchField, matchValue, returnField string) (string, error) {
	return findByFieldImpl(body, arrayKey, matchField, matchValue, returnField, true)
}

func findByFieldImpl(body json.RawMessage, arrayKey, matchField, matchValue, returnField string, foldCase bool) (string, error) {
	items, err := common.ExtractArray(body, arrayKey)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		val := common.ExtractField(item, matchField)
		matched := val == matchValue
		if foldCase {
			matched = strings.EqualFold(val, matchValue)
		}
		if matched {
			result := extractAnyStr(item, returnField)
			if result != "" {
				return result, nil
			}
		}
	}
	return "", errLookupNotFound
}
