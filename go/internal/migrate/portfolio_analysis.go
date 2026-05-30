package migrate

import "strings"

// PortfolioComposition summarises how a SonarQube Server portfolio
// resolves on SonarQube Cloud, which has no portfolio hierarchy and
// no concept of applications. Issue #229.
//
// The four flags drive both the migrate-side configuration of the SQC
// portfolio and the report-side green/yellow/orange classification.
// A portfolio whose direct subviews are only projects (no APP, no VW
// or SVW) returns the zero value — i.e. no special handling required.
type PortfolioComposition struct {
	// HasApps is true when at least one direct subview is an
	// application reference (qualifier APP). Apps do not exist on
	// SonarQube Cloud and are substituted by the projects they
	// enclose at extract time.
	HasApps bool
	// HasSubportfolios is true when at least one direct subview is a
	// subportfolio (qualifier VW or SVW). SQC has no portfolio
	// hierarchy, so the source structure has to be flattened.
	HasSubportfolios bool
	// DepthGT1 is true when at least one direct subportfolio itself
	// has subportfolios — the source structure cannot be replicated
	// as a single SQC portfolio with one selection criterion.
	DepthGT1 bool
	// CommonSelectionMode is the shared selection_mode of every
	// direct subportfolio when they all agree on one of REGEXP,
	// TAGS, MANUAL. Empty otherwise.
	CommonSelectionMode string
	// MixedSelectionModes is true when direct subportfolios use more
	// than one selection_mode (and CommonSelectionMode is empty), or
	// when at least one direct subportfolio has no selection_mode at
	// all and cannot be combined.
	MixedSelectionModes bool
	// Subportfolios captures the per-subportfolio selection criteria
	// needed by the migrate-side combination (regex alternation,
	// tag union, manual fallback). Only populated for direct VW/SVW
	// children; never includes APP nodes.
	Subportfolios []SubportfolioCriteria
}

// SubportfolioCriteria is one direct subportfolio's selection data,
// used by the parent-side combination when CommonSelectionMode is set.
type SubportfolioCriteria struct {
	Key           string
	Name          string
	SelectionMode string
	Regexp        string
	Tags          []string
}

// AnalyzePortfolio inspects a getPortfolioDetails record and
// classifies its composition. The caller passes the JSON-decoded map
// for the top-level portfolio (api/views/show response).
func AnalyzePortfolio(details map[string]any) PortfolioComposition {
	var pc PortfolioComposition
	subViews, _ := details["subViews"].([]any)
	if len(subViews) == 0 {
		return pc
	}
	seenModes := map[string]bool{}
	for _, sub := range subViews {
		m, ok := sub.(map[string]any)
		if !ok {
			continue
		}
		switch strOf(m, "qualifier") {
		case "APP":
			pc.HasApps = true
		case "VW", "SVW":
			pc.HasSubportfolios = true
			if grand, _ := m["subViews"].([]any); len(grand) > 0 {
				pc.DepthGT1 = true
			}
			crit := SubportfolioCriteria{
				Key:           strOf(m, "key"),
				Name:          strOf(m, "name"),
				SelectionMode: strings.ToUpper(strOf(m, "selectionMode")),
				Regexp:        strOf(m, "regexp"),
				Tags:          tagsOf(m),
			}
			if crit.SelectionMode == "REGEXP" || crit.SelectionMode == "TAGS" || crit.SelectionMode == "MANUAL" {
				seenModes[crit.SelectionMode] = true
			}
			pc.Subportfolios = append(pc.Subportfolios, crit)
		}
	}
	if pc.HasSubportfolios {
		switch len(seenModes) {
		case 1:
			for m := range seenModes {
				pc.CommonSelectionMode = m
			}
			// All direct subportfolios share one of the three
			// combinable modes. If one of them has no selection
			// mode at all (e.g. nested-VW), the combination is no
			// longer clean — flag mixed.
			for _, sp := range pc.Subportfolios {
				if sp.SelectionMode != pc.CommonSelectionMode {
					pc.CommonSelectionMode = ""
					pc.MixedSelectionModes = true
					break
				}
			}
		default:
			pc.MixedSelectionModes = true
		}
	}
	return pc
}

func strOf(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// tagsOf reads the "tags" field of a subview, handling both the
// comma-separated string form (used by api/views/show output and the
// portfolios.csv mapping) and the JSON array form (some SQS versions).
func tagsOf(m map[string]any) []string {
	if s, _ := m["tags"].(string); s != "" {
		parts := strings.Split(s, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	if arr, _ := m["tags"].([]any); len(arr) > 0 {
		out := make([]string, 0, len(arr))
		for _, v := range arr {
			if s, _ := v.(string); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
