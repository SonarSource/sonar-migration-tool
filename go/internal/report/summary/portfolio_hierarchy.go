package summary

import (
	"encoding/json"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// partialPortfolioIssue is the human-readable issue text attached to a
// portfolio that has at least one subportfolio in the source. SonarQube Cloud
// has no portfolio hierarchy, so such portfolios are migrated as a flat
// project list (snapshot of the resolved projects at extract time). New
// projects that match a regex/tags subportfolio after migration will not
// appear in the SQC portfolio until it is updated manually.
const partialPortfolioIssue = "The source portfolio has subportfolios. The target portfolio is migrated as a flat list of projects. The portfolio project perimeter may be slightly different on SonarQube Cloud"

// portfoliosWithSubportfolios walks getPortfolioDetails across all extracts
// and returns the set of portfolio keys that have at least one direct
// subportfolio (i.e. non-empty subViews). Leaf portfolios — including
// subportfolios that do not themselves have children — are not included.
//
// Keys are returned as composite serverURL|portfolioKey strings so they can
// be matched against createPortfolios JSONL entries (which carry both
// server_url and source_portfolio_key).
func portfoliosWithSubportfolios(exportDir string, mapping structure.ExtractMapping) map[string]bool {
	set := make(map[string]bool)
	items, err := structure.ReadExtractData(exportDir, mapping, "getPortfolioDetails")
	if err != nil || len(items) == 0 {
		return set
	}
	for _, item := range items {
		var obj map[string]any
		if err := json.Unmarshal(item.Data, &obj); err != nil {
			continue
		}
		markPortfolioParents(item.ServerURL, obj, set)
	}
	return set
}

// markPortfolioParents walks a portfolio object (recursively through its
// subViews) and records every node whose subViews list is non-empty.
func markPortfolioParents(serverURL string, portfolio map[string]any, set map[string]bool) {
	subViewsRaw, ok := portfolio["subViews"]
	if !ok {
		return
	}
	subViews, ok := subViewsRaw.([]any)
	if !ok || len(subViews) == 0 {
		return
	}
	if key, ok := portfolio["key"].(string); ok && key != "" {
		set[serverURL+"|"+key] = true
	}
	for _, sub := range subViews {
		if subMap, ok := sub.(map[string]any); ok {
			markPortfolioParents(serverURL, subMap, set)
		}
	}
}

// markPartialPortfolios moves portfolios that have at least one subportfolio
// from Succeeded to Partial, adding partialPortfolioIssue to the entry. Leaf
// portfolios stay in Succeeded. The match is done on the composite
// serverURL|source_portfolio_key key, which both getPortfolioDetails (via the
// walker above) and createPortfolios JSONL produce.
func markPartialPortfolios(store *common.DataStore, succeeded, partial []EntityItem,
	parents map[string]bool) ([]EntityItem, []EntityItem) {

	if len(parents) == 0 || len(succeeded) == 0 {
		return succeeded, partial
	}

	// Build name → composite key map from createPortfolios output. The
	// EntityItem.Name comes from the same JSONL "name" field, so this is a
	// reliable join.
	items, err := store.ReadAll("createPortfolios")
	if err != nil || len(items) == 0 {
		return succeeded, partial
	}
	nameToCompositeKey := make(map[string]string, len(items))
	for _, item := range items {
		name := jsonStr(item, "name")
		if name == "" {
			continue
		}
		serverURL := jsonStr(item, "server_url")
		sourceKey := jsonStr(item, "source_portfolio_key")
		if serverURL == "" || sourceKey == "" {
			continue
		}
		nameToCompositeKey[name] = serverURL + "|" + sourceKey
	}

	kept := succeeded[:0:0]
	for _, item := range succeeded {
		composite := nameToCompositeKey[item.Name]
		if composite != "" && parents[composite] {
			item.Issues = append(item.Issues, partialPortfolioIssue)
			partial = append(partial, item)
			continue
		}
		kept = append(kept, item)
	}
	return kept, partial
}
