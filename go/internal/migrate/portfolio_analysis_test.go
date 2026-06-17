// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"reflect"
	"testing"
)

func TestAnalyzePortfolio_EmptyAndPlain(t *testing.T) {
	cases := map[string]map[string]any{
		"empty":         {},
		"projects only": {"subViews": []any{}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			pc := AnalyzePortfolio(in)
			if pc.HasApps || pc.HasSubportfolios || pc.DepthGT1 || pc.MixedSelectionModes || pc.CommonSelectionMode != "" {
				t.Errorf("%s: expected zero composition, got %+v", name, pc)
			}
		})
	}
}

func TestAnalyzePortfolio_AppsOnly(t *testing.T) {
	pc := AnalyzePortfolio(map[string]any{
		"subViews": []any{
			map[string]any{"qualifier": "APP", "key": "app1"},
			map[string]any{"qualifier": "APP", "key": "app2"},
		},
	})
	if !pc.HasApps || pc.HasSubportfolios || pc.DepthGT1 {
		t.Errorf("apps-only: %+v", pc)
	}
}

func TestAnalyzePortfolio_UniformRegexp_Depth1(t *testing.T) {
	pc := AnalyzePortfolio(map[string]any{
		"subViews": []any{
			map[string]any{"qualifier": "SVW", "key": "a", "selectionMode": "REGEXP", "regexp": ".*-RETAIL-.*"},
			map[string]any{"qualifier": "SVW", "key": "b", "selectionMode": "REGEXP", "regexp": ".*-INVEST-.*"},
		},
	})
	if !pc.HasSubportfolios || pc.DepthGT1 || pc.MixedSelectionModes {
		t.Errorf("uniform regexp depth 1: %+v", pc)
	}
	if pc.CommonSelectionMode != "REGEXP" {
		t.Errorf("expected REGEXP common mode, got %q", pc.CommonSelectionMode)
	}
	if len(pc.Subportfolios) != 2 {
		t.Fatalf("expected 2 subportfolios captured, got %d", len(pc.Subportfolios))
	}
}

func TestAnalyzePortfolio_MixedModes(t *testing.T) {
	pc := AnalyzePortfolio(map[string]any{
		"subViews": []any{
			map[string]any{"qualifier": "SVW", "key": "a", "selectionMode": "REGEXP", "regexp": ".*"},
			map[string]any{"qualifier": "SVW", "key": "b", "selectionMode": "TAGS", "tags": "x,y"},
		},
	})
	if !pc.MixedSelectionModes || pc.CommonSelectionMode != "" {
		t.Errorf("mixed: %+v", pc)
	}
}

func TestAnalyzePortfolio_DepthGT1(t *testing.T) {
	pc := AnalyzePortfolio(map[string]any{
		"subViews": []any{
			map[string]any{
				"qualifier": "VW", "key": "a", "selectionMode": "REGEXP", "regexp": ".*",
				"subViews": []any{
					map[string]any{"qualifier": "SVW", "key": "a1"},
				},
			},
		},
	})
	if !pc.DepthGT1 {
		t.Errorf("expected DepthGT1, got %+v", pc)
	}
}

func TestAnalyzePortfolio_TagsAsArrayAndCSV(t *testing.T) {
	pc := AnalyzePortfolio(map[string]any{
		"subViews": []any{
			map[string]any{"qualifier": "SVW", "key": "a", "selectionMode": "TAGS", "tags": "python,backend"},
			map[string]any{"qualifier": "SVW", "key": "b", "selectionMode": "TAGS", "tags": []any{"frontend", "react"}},
		},
	})
	if pc.CommonSelectionMode != "TAGS" {
		t.Errorf("expected TAGS, got %q", pc.CommonSelectionMode)
	}
	if !reflect.DeepEqual(pc.Subportfolios[0].Tags, []string{"python", "backend"}) {
		t.Errorf("csv tags: %v", pc.Subportfolios[0].Tags)
	}
	if !reflect.DeepEqual(pc.Subportfolios[1].Tags, []string{"frontend", "react"}) {
		t.Errorf("array tags: %v", pc.Subportfolios[1].Tags)
	}
}

// Migrate-side combination for the three uniform-subportfolio modes.
func TestResolveEffectivePortfolioConfig_RegexpAlternationWithOrgPrefix(t *testing.T) {
	pc := PortfolioComposition{
		HasSubportfolios:    true,
		CommonSelectionMode: "REGEXP",
		Subportfolios: []SubportfolioCriteria{
			{SelectionMode: "REGEXP", Regexp: ".*-RETAIL-.*"},
			{SelectionMode: "REGEXP", Regexp: ".*-INVEST-.*"},
		},
	}
	mode, regex, _ := resolveEffectivePortfolioConfig("", "", "", pc, []string{"acme"}, DefaultProjectKeyPattern)
	if mode != "REGEXP" {
		t.Fatalf("expected REGEXP, got %q", mode)
	}
	// transformPortfolioRegex prepends "<org>_" (the SQC project-key
	// prefix) to each subportfolio regex before alternation.
	if regex != "(acme_.*-RETAIL-.*|acme_.*-INVEST-.*)" {
		t.Errorf("combined regex: %q", regex)
	}
}

func TestResolveEffectivePortfolioConfig_TagsUnion(t *testing.T) {
	pc := PortfolioComposition{
		HasSubportfolios:    true,
		CommonSelectionMode: "TAGS",
		Subportfolios: []SubportfolioCriteria{
			{SelectionMode: "TAGS", Tags: []string{"python", "backend"}},
			{SelectionMode: "TAGS", Tags: []string{"frontend", "python"}}, // dedup
		},
	}
	mode, _, tags := resolveEffectivePortfolioConfig("", "", "", pc, nil, DefaultProjectKeyPattern)
	if mode != "TAGS" {
		t.Fatalf("expected TAGS, got %q", mode)
	}
	if !reflect.DeepEqual(tags, []string{"python", "backend", "frontend"}) {
		t.Errorf("union tags: %v", tags)
	}
}

func TestResolveEffectivePortfolioConfig_MixedFlattensToManual(t *testing.T) {
	pc := PortfolioComposition{
		HasSubportfolios:    true,
		MixedSelectionModes: true,
	}
	mode, _, _ := resolveEffectivePortfolioConfig("", "", "", pc, nil, DefaultProjectKeyPattern)
	if mode != "MANUAL" {
		t.Errorf("expected MANUAL for mixed, got %q", mode)
	}
}

func TestResolveEffectivePortfolioConfig_DepthGT1FlattensToManual(t *testing.T) {
	pc := PortfolioComposition{
		HasSubportfolios: true,
		DepthGT1:         true,
	}
	mode, _, _ := resolveEffectivePortfolioConfig("", "", "", pc, nil, DefaultProjectKeyPattern)
	if mode != "MANUAL" {
		t.Errorf("expected MANUAL for depth>1, got %q", mode)
	}
}

func TestResolveEffectivePortfolioConfig_RestModeFlattensToManual(t *testing.T) {
	mode, _, _ := resolveEffectivePortfolioConfig("REST", "", "", PortfolioComposition{}, nil, DefaultProjectKeyPattern)
	if mode != "MANUAL" {
		t.Errorf("REST should flatten to MANUAL, got %q", mode)
	}
}

func TestResolveEffectivePortfolioConfig_AppsOnlyFlattensToManual(t *testing.T) {
	pc := PortfolioComposition{HasApps: true}
	mode, _, _ := resolveEffectivePortfolioConfig("", "", "", pc, nil, DefaultProjectKeyPattern)
	if mode != "MANUAL" {
		t.Errorf("apps-only should flatten to MANUAL, got %q", mode)
	}
}

func TestResolveEffectivePortfolioConfig_OwnRegexpUnchanged(t *testing.T) {
	mode, regex, _ := resolveEffectivePortfolioConfig("REGEXP", "^pre", "", PortfolioComposition{}, []string{"acme"}, DefaultProjectKeyPattern)
	if mode != "REGEXP" {
		t.Fatalf("expected REGEXP, got %q", mode)
	}
	if regex != "^acme_pre" {
		t.Errorf("transformed own regex: %q", regex)
	}
}

func TestResolveEffectivePortfolioConfig_EmptyPlainPortfolio(t *testing.T) {
	mode, _, _ := resolveEffectivePortfolioConfig("", "", "", PortfolioComposition{}, nil, DefaultProjectKeyPattern)
	if mode != "" {
		t.Errorf("plain empty portfolio: expected no config, got %q", mode)
	}
}
