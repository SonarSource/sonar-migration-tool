package structure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// MapPortfolios maps portfolios, deduplicating by project composition. It
// also enriches each entry with selectionMode / regexp / tags from the
// corresponding getPortfolioDetails record, so the migration can preserve
// the source selection semantics rather than always producing a manual
// project list on the target.
func MapPortfolios(directory string, mapping ExtractMapping) []Portfolio {
	items, _ := ReadExtractData(directory, mapping, "getPortfolioProjects")

	// Collect portfolio projects and details.
	portfolioProjects := make(map[string]map[string]bool) // uniqueKey → set of refKeys
	portfolioDetails := make(map[string]portfolioDetail)

	for _, item := range items {
		portfolioKey := common.ExtractField(item.Data, "portfolioKey")
		refKey := common.ExtractField(item.Data, "refKey")
		uniqueKey := item.ServerURL + portfolioKey

		if portfolioProjects[uniqueKey] == nil {
			portfolioProjects[uniqueKey] = make(map[string]bool)
		}
		portfolioProjects[uniqueKey][refKey] = true

		var p map[string]any
		json.Unmarshal(item.Data, &p)
		portfolioDetails[uniqueKey] = portfolioDetail{
			SourcePortfolioKey: portfolioKey,
			Name:               getString(p, "portfolioName"),
			ServerURL:          item.ServerURL,
			Description:        getString(p, "description"),
		}
	}

	// Overlay selection metadata read from getPortfolioDetails. Each detail
	// item is the top-level portfolio object returned by api/views/show, so
	// it carries selectionMode and (when applicable) regexp + tags.
	detailItems, _ := ReadExtractData(directory, mapping, "getPortfolioDetails")
	selection := buildSelectionIndex(detailItems)
	for key, info := range selection {
		d, ok := portfolioDetails[key]
		if !ok {
			continue
		}
		d.SelectionMode = info.mode
		d.RegularExpression = info.regexp
		d.Tags = info.tags
		portfolioDetails[key] = d
	}

	// Deduplicate portfolios by project composition hash.
	uniquePortfolios := make(map[string]portfolioDetail)
	for key, projects := range portfolioProjects {
		projectList := make([]string, 0, len(projects))
		for p := range projects {
			projectList = append(projectList, p)
		}
		hashID := generateHashID(projectList)
		uniquePortfolios[hashID] = portfolioDetails[key]
	}

	result := make([]Portfolio, 0, len(uniquePortfolios))
	for _, p := range uniquePortfolios {
		result = append(result, Portfolio{
			SourcePortfolioKey: p.SourcePortfolioKey,
			Name:               p.Name,
			ServerURL:          p.ServerURL,
			Description:        p.Description,
			SelectionMode:      p.SelectionMode,
			RegularExpression:  p.RegularExpression,
			Tags:               p.Tags,
		})
	}
	return result
}

type portfolioDetail struct {
	SourcePortfolioKey string
	Name               string
	ServerURL          string
	Description        string
	SelectionMode      string
	RegularExpression  string
	Tags               string // comma-separated; SQS returns []string in the API
}

// portfolioSelection holds the selection fields read from a getPortfolioDetails record.
type portfolioSelection struct {
	mode   string
	regexp string
	tags   string
}

// buildSelectionIndex walks the getPortfolioDetails extract output and
// returns a serverURL+portfolioKey → selection map. Each item from
// api/views/show carries the full nested tree under subViews, so we recurse
// into every node and capture its own selectionMode/regexp/tags — otherwise
// leaf portfolios inside a hierarchy lose their regex during migration.
func buildSelectionIndex(items []ExtractItem) map[string]portfolioSelection {
	out := make(map[string]portfolioSelection, len(items))
	for _, item := range items {
		var obj map[string]any
		if err := json.Unmarshal(item.Data, &obj); err != nil {
			continue
		}
		collectPortfolioSelections(item.ServerURL, obj, out)
	}
	return out
}

// collectPortfolioSelections walks a portfolio node and its subViews
// recursively, recording each node's selection metadata indexed by
// serverURL+key.
func collectPortfolioSelections(serverURL string, node map[string]any, out map[string]portfolioSelection) {
	if node == nil {
		return
	}
	if key := getString(node, "key"); key != "" {
		out[serverURL+key] = portfolioSelection{
			mode:   getString(node, "selectionMode"),
			regexp: getString(node, "regexp"),
			tags:   joinAnySlice(node["tags"]),
		}
	}
	subs, ok := node["subViews"].([]any)
	if !ok {
		return
	}
	for _, sv := range subs {
		if subMap, ok := sv.(map[string]any); ok {
			collectPortfolioSelections(serverURL, subMap, out)
		}
	}
}

// joinAnySlice flattens a JSON array of strings to a comma-separated string,
// tolerating nil and non-array shapes (returns "").
func joinAnySlice(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	out := ""
	for _, e := range arr {
		s, ok := e.(string)
		if !ok || s == "" {
			continue
		}
		if out == "" {
			out = s
		} else {
			out += "," + s
		}
	}
	return out
}

// generateHashID generates a consistent UUID-formatted string from a list of strings.
// SHA-256 of JSON-sorted list → UUID format (first 16 bytes).
func generateHashID(data []string) string {
	sort.Strings(data)
	jsonBytes, _ := json.Marshal(data)
	hash := sha256.Sum256(jsonBytes)
	hex := fmt.Sprintf("%x", hash[:16])
	// Format as UUID: 8-4-4-4-12
	return strings.Join([]string{
		hex[0:8], hex[8:12], hex[12:16], hex[16:20], hex[20:32],
	}, "-")
}
