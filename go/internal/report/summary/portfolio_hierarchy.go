package summary

import (
	"encoding/json"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// Issue text emitted for each #229 sub-criterion. Wording mirrors the
// issue description verbatim so report consumers and tickets stay in
// sync with the issue tracker.
const (
	// Yellow: portfolio contains application references. Apps don't
	// exist on SQC and are substituted by their enclosed projects.
	portfolioIssueApps = "The SQS portfolio contains applications that were replaced by their enclosed projects"
	// Yellow: every direct subportfolio uses the same selection mode
	// and there is no further nesting — the perimeter is perfectly
	// replicated on SQC via a combined regex / tag union / project
	// list.
	portfolioIssueSubportfoliosUniform = "The source portfolio has subportfolios with a uniform selection mode. Their criteria were combined on SonarQube Cloud — the portfolio perimeter is preserved."
	// Orange: nesting depth ≥ 2 — can't be represented as a single
	// SQC portfolio with one selection criterion; falls back to a
	// flat project list. (Past tense for the actual report; the
	// predictive renderer swaps back to "will be" via
	// toPredictiveTense — issue #167.)
	portfolioIssueNestedDepth = "The SQS portfolio has nested subportfolios depth higher than 2, it was converted to a flat list of projects in SQC. The portfolio perimeter may be slightly different"
	// Orange: direct subportfolios use mixed selection modes — can't
	// combine into a single criterion; falls back to a flat project
	// list.
	portfolioIssueMixedModes = "The SQS portfolio has nested subportfolios with different selection modes, it was converted to a flat list of projects in SQC. The portfolio perimeter may be slightly different"
	// Orange: portfolio uses REST selection mode ("rest of projects"
	// catch-all). No SQC equivalent — falls back to a flat project
	// list of whatever the source resolved at extract time.
	portfolioIssueRestMode = "The SQS portfolio is defined with REST selection mode, it was converted to a flat list of projects in SQC. The portfolio perimeter may be slightly different"
	// Grey (Skipped): portfolio's resolved project list is empty at
	// extract time — there is nothing to migrate.
	portfolioIssueEmpty = "The SQS portfolio is empty, was not migrated"
)

// portfolioClassification rolls up the per-portfolio composition flags
// produced by migrate.AnalyzePortfolio into the report-side outcome:
// which yellow / orange issue lines to attach and whether the worst
// classification is orange (Partial) or yellow (NearPerfect).
type portfolioClassification struct {
	Issues   []string
	IsOrange bool
}

// portfolioClassifications walks getPortfolioDetails and returns the
// classification for every portfolio it encounters — top-level AND
// every nested subportfolio. SQS-side subportfolios surface in the
// extract via api/views/search (qualifier VW/SVW), get a createPortfolios
// row of their own, and therefore need their own classification (a
// subportfolio that itself has subportfolios is depth=1 from its own
// frame of reference, so it still hits the same rules).
//
// The map key is the composite serverURL|portfolioKey string so it can
// be matched against createPortfolios JSONL rows.
func portfolioClassifications(exportDir string, mapping structure.ExtractMapping) map[string]portfolioClassification {
	out := make(map[string]portfolioClassification)
	items, err := structure.ReadExtractData(exportDir, mapping, "getPortfolioDetails")
	if err != nil || len(items) == 0 {
		return out
	}
	for _, item := range items {
		var obj map[string]any
		if err := json.Unmarshal(item.Data, &obj); err != nil {
			continue
		}
		walkPortfolioClassification(item.ServerURL, obj, out)
	}
	return out
}

// walkPortfolioClassification classifies a portfolio node and recurses
// into its subportfolios. Every node visited may produce a row in
// createPortfolios, so each gets its own classification entry.
func walkPortfolioClassification(serverURL string, portfolio map[string]any, out map[string]portfolioClassification) {
	cls := classificationFor(portfolio)
	if len(cls.Issues) > 0 {
		if key, _ := portfolio["key"].(string); key != "" {
			out[serverURL+"|"+key] = cls
		}
	}
	subViews, _ := portfolio["subViews"].([]any)
	for _, sub := range subViews {
		if subMap, ok := sub.(map[string]any); ok {
			walkPortfolioClassification(serverURL, subMap, out)
		}
	}
}

// classificationFor inspects a single top-level portfolio's JSON and
// returns the report-side {Issues, IsOrange} pair derived from #229:
//   - REST selection mode → Orange + REST message.
//   - Apps among direct subviews → adds the apps message (yellow on its
//     own; orange combined with another orange flag).
//   - Subportfolios with nested depth ≥ 2 → Orange + nested message.
//   - Subportfolios with mixed modes → Orange + mixed-modes message.
//   - Subportfolios uniform mode + depth=1 → Yellow + uniform message.
//
// Multiple flags can apply (e.g. REST + apps). The row carries every
// applicable Issues line and is routed to Partial if any orange flag
// is present.
func classificationFor(portfolio map[string]any) portfolioClassification {
	var cls portfolioClassification

	mode := strings.ToUpper(strFieldFromMap(portfolio, "selectionMode"))
	if mode == "REST" {
		cls.Issues = append(cls.Issues, portfolioIssueRestMode)
		cls.IsOrange = true
	}

	pc := migrate.AnalyzePortfolio(portfolio)
	if pc.HasApps {
		cls.Issues = append(cls.Issues, portfolioIssueApps)
	}
	if pc.HasSubportfolios {
		switch {
		case pc.DepthGT1:
			cls.Issues = append(cls.Issues, portfolioIssueNestedDepth)
			cls.IsOrange = true
		case pc.MixedSelectionModes:
			cls.Issues = append(cls.Issues, portfolioIssueMixedModes)
			cls.IsOrange = true
		case pc.CommonSelectionMode != "":
			cls.Issues = append(cls.Issues, portfolioIssueSubportfoliosUniform)
		}
	}
	return cls
}

func strFieldFromMap(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// emptyPortfolioInfo carries the per-portfolio data needed to emit a
// Skipped row: the entity name (so it shows up correctly in the report)
// and the composite key (so we can match against entries already
// present in other buckets and pull them out).
type emptyPortfolioInfo struct {
	Composite string
	Name      string
}

// detectEmptyPortfolios walks getPortfolioDetails (every portfolio in
// the source, including subportfolios that structure's MapPortfolios
// deduplicates out of portfolios.csv) and returns one info entry per
// portfolio whose resolved project list is empty.
//
// The resolved list reflects how SonarQube Server evaluated the
// portfolio's selection criteria, so an empty list means the portfolio
// has no projects regardless of selection mode (MANUAL with no picks,
// REGEXP/TAGS with no matches, REST that caught nothing). Walking
// getPortfolioDetails — rather than generatePortfolioMappings — is
// what lets empty subportfolios surface in the report: MapPortfolios
// drops them because their project-composition hash is the empty hash
// and all empties collapse into one row.
func detectEmptyPortfolios(store *common.DataStore, exportDir string,
	mapping structure.ExtractMapping) []emptyPortfolioInfo {

	nonEmpty := make(map[string]bool)
	items, err := structure.ReadExtractData(exportDir, mapping, "getPortfolioProjects")
	if err == nil {
		for _, it := range items {
			pk := jsonStr(it.Data, "portfolioKey")
			rk := jsonStr(it.Data, "refKey")
			if pk == "" || rk == "" {
				continue
			}
			nonEmpty[it.ServerURL+"|"+pk] = true
		}
	}

	detailItems, err := structure.ReadExtractData(exportDir, mapping, "getPortfolioDetails")
	if err != nil || len(detailItems) == 0 {
		return nil
	}
	var out []emptyPortfolioInfo
	seen := make(map[string]bool)
	classify := func(serverURL, key, name string) {
		if key == "" || name == "" {
			return
		}
		composite := serverURL + "|" + key
		if seen[composite] || nonEmpty[composite] {
			return
		}
		seen[composite] = true
		out = append(out, emptyPortfolioInfo{Composite: composite, Name: name})
	}
	// walkSVW recurses through SVW subviews of `node`. SVW (standard
	// view) subportfolios are defined inline under their parent — they
	// have no top-level api/views/show entry and use their bare key.
	// They may themselves contain nested SVW children. VW subviews are
	// skipped: those are by-reference and the same portfolio already
	// appears as a top-level entry under its bare key (the subview's
	// "key" field is compound like "Banking:Private_Banking" and would
	// not match getPortfolioProjects' bare keys). APP subviews are
	// applications, not portfolios.
	var walkSVW func(serverURL string, node map[string]any)
	walkSVW = func(serverURL string, node map[string]any) {
		subs, _ := node["subViews"].([]any)
		for _, s := range subs {
			m, ok := s.(map[string]any)
			if !ok {
				continue
			}
			if q, _ := m["qualifier"].(string); q != "SVW" {
				continue
			}
			sk, _ := m["key"].(string)
			sn, _ := m["name"].(string)
			classify(serverURL, sk, sn)
			walkSVW(serverURL, m)
		}
	}
	for _, it := range detailItems {
		var d map[string]any
		if err := json.Unmarshal(it.Data, &d); err != nil {
			continue
		}
		key, _ := d["key"].(string)
		name, _ := d["name"].(string)
		classify(it.ServerURL, key, name)
		walkSVW(it.ServerURL, d)
	}
	return out
}

// applyEmptyPortfolioSkips removes empty portfolios from every active
// bucket (Succeeded, NearPerfect, Partial) and appends one Skipped row
// per empty portfolio. Works whether migrate created the empty
// portfolio on SQC (it'll be in one of the buckets and we move it) or
// skipped it (it isn't anywhere and we add a fresh Skipped row).
func applyEmptyPortfolioSkips(store *common.DataStore, succeeded, nearPerfect, partial, skipped []EntityItem,
	empties []emptyPortfolioInfo) ([]EntityItem, []EntityItem, []EntityItem, []EntityItem) {

	if len(empties) == 0 {
		return succeeded, nearPerfect, partial, skipped
	}

	emptyNames := make(map[string]bool, len(empties))
	for _, e := range empties {
		emptyNames[e.Name] = true
	}

	// Pull any empty portfolios out of the non-Skipped buckets — they
	// may have been routed there before the empty check ran.
	filter := func(bucket []EntityItem) (kept []EntityItem) {
		kept = bucket[:0:0]
		for _, item := range bucket {
			if emptyNames[item.Name] {
				continue
			}
			kept = append(kept, item)
		}
		return kept
	}
	succeeded = filter(succeeded)
	nearPerfect = filter(nearPerfect)
	partial = filter(partial)

	for _, e := range empties {
		skipped = append(skipped, EntityItem{
			Name:       e.Name,
			Detail:     portfolioIssueEmpty,
			SkipReason: SkipReasonEmpty,
		})
	}
	return succeeded, nearPerfect, partial, skipped
}

// applyPortfolioClassifications moves classified portfolios out of
// Succeeded into either NearPerfect or Partial, attaching the Issues
// lines from the classification. Portfolios with no entry in the
// classification map stay in Succeeded.
func applyPortfolioClassifications(store *common.DataStore, succeeded, nearPerfect, partial []EntityItem,
	classifications map[string]portfolioClassification) ([]EntityItem, []EntityItem, []EntityItem) {

	if len(classifications) == 0 || len(succeeded) == 0 {
		return succeeded, nearPerfect, partial
	}

	items, err := store.ReadAll("createPortfolios")
	if err != nil || len(items) == 0 {
		return succeeded, nearPerfect, partial
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
		cls, ok := classifications[composite]
		if !ok || len(cls.Issues) == 0 {
			kept = append(kept, item)
			continue
		}
		item.Issues = append(item.Issues, cls.Issues...)
		if cls.IsOrange {
			partial = append(partial, item)
		} else {
			nearPerfect = append(nearPerfect, item)
		}
	}
	return kept, nearPerfect, partial
}
