// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import "testing"

func TestTargetFieldOrZero(t *testing.T) {
	get := func(b *unifiedTargetBlock) string { return b.BranchRegexp }
	if got := targetFieldOrZero(nil, get); got != "" {
		t.Errorf("nil target: got %q, want zero value", got)
	}
	if got := targetFieldOrZero(&unifiedTargetBlock{BranchRegexp: "^main$"}, get); got != "^main$" {
		t.Errorf("non-nil target: got %q", got)
	}
}

func TestApplyTargetBaseFields(t *testing.T) {
	var cfg MigrateConfig
	applyTargetBaseFields(&cfg, nil)
	if cfg.URL != "" || cfg.Concurrency != 0 {
		t.Errorf("nil target must leave cfg untouched, got %+v", cfg)
	}

	applyTargetBaseFields(&cfg, &unifiedTargetBlock{
		URL: "https://sc", Token: "tok", EnterpriseKey: "ent", Edition: "enterprise",
		Concurrency: 4, BuildConcurrency: 2, Timeout: 30, MaxIssueComments: 7,
		BranchRegexp: "^main$", GroupNameRegexp: "^dev",
	})
	if cfg.URL != "https://sc" || cfg.Token != "tok" || cfg.EnterpriseKey != "ent" ||
		cfg.Edition != "enterprise" || cfg.Concurrency != 4 || cfg.BuildConcurrency != 2 ||
		cfg.Timeout != 30 || cfg.MaxIssueComments != 7 ||
		cfg.BranchRegexp != "^main$" || cfg.GroupNameRegexp != "^dev" {
		t.Errorf("fields not copied: %+v", cfg)
	}
}

func TestResolvePortfolioRegexpFromTarget(t *testing.T) {
	cases := []struct {
		name         string
		target       *unifiedTargetBlock
		wantK, wantN string
	}{
		{"nil target uses top-level", nil, "topK", "topN"},
		{"empty target uses top-level", &unifiedTargetBlock{}, "topK", "topN"},
		{"target key wins as a whole", &unifiedTargetBlock{PortfolioKeyRegexp: "tK"}, "tK", ""},
		{"target name wins as a whole", &unifiedTargetBlock{PortfolioNameRegexp: "tN"}, "", "tN"},
		{"target pair", &unifiedTargetBlock{PortfolioKeyRegexp: "tK", PortfolioNameRegexp: "tN"}, "tK", "tN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, n := resolvePortfolioRegexpFromTarget(tc.target, "topK", "topN")
			if k != tc.wantK || n != tc.wantN {
				t.Errorf("got (%q,%q), want (%q,%q)", k, n, tc.wantK, tc.wantN)
			}
		})
	}
}

func TestApplyPortfolioOuterOverride(t *testing.T) {
	cfg := MigrateConfig{PortfolioKeyRegexp: "inK", PortfolioNameRegexp: "inN"}
	applyPortfolioOuterOverride(&cfg, configFileShape{})
	if cfg.PortfolioKeyRegexp != "inK" || cfg.PortfolioNameRegexp != "inN" {
		t.Errorf("unset outer must keep nested pair, got %+v", cfg)
	}
	applyPortfolioOuterOverride(&cfg, configFileShape{PortfolioKeyRegexp: "outK"})
	if cfg.PortfolioKeyRegexp != "outK" || cfg.PortfolioNameRegexp != "" {
		t.Errorf("outer must win as a whole, got %+v", cfg)
	}
}

func TestFillConcurrencyDefaults(t *testing.T) {
	s := configFileShape{Concurrency: 9, BuildConcurrency: 8, Timeout: 7, MaxIssueComments: 6}
	empty := MigrateConfig{}
	fillConcurrencyDefaults(&empty, s)
	if empty.Concurrency != 9 || empty.BuildConcurrency != 8 || empty.Timeout != 7 || empty.MaxIssueComments != 6 {
		t.Errorf("zero fields must be filled, got %+v", empty)
	}
	set := MigrateConfig{Concurrency: 1, BuildConcurrency: 2, Timeout: 3, MaxIssueComments: 4}
	fillConcurrencyDefaults(&set, s)
	if set.Concurrency != 1 || set.BuildConcurrency != 2 || set.Timeout != 3 || set.MaxIssueComments != 4 {
		t.Errorf("set fields must be kept, got %+v", set)
	}
}

func TestApplySkipSyncAndFlexibleBoolOverrides(t *testing.T) {
	cfg := MigrateConfig{SkipIssueSync: true, FastSync: true}
	applySkipSyncOverrides(&cfg, configFileShape{})
	applyFlexibleBoolOverrides(&cfg, configFileShape{})
	if !cfg.SkipIssueSync || !cfg.FastSync {
		t.Errorf("nil overrides must not change cfg: %+v", cfg)
	}
	// Present but unset must not override either.
	applySkipSyncOverrides(&cfg, configFileShape{SkipIssueSync: &FlexibleBool{}})
	applyFlexibleBoolOverrides(&cfg, configFileShape{FastSync: &FlexibleBool{}})
	if !cfg.SkipIssueSync || !cfg.FastSync {
		t.Errorf("unset FlexibleBool must not override: %+v", cfg)
	}

	on := &FlexibleBool{Set: true, Value: true}
	off := &FlexibleBool{Set: true, Value: false}
	cfg = MigrateConfig{SkipIssueSync: true, SkipProjectDataMigration: false, FastSync: true}
	applySkipSyncOverrides(&cfg, configFileShape{SkipIssueSync: off, SkipProjectDataMigration: on})
	applyFlexibleBoolOverrides(&cfg, configFileShape{FastSync: off, MigrateHistory: on, UseSLBForReferenceBranchNewCode: on})
	if cfg.SkipIssueSync || !cfg.SkipProjectDataMigration || cfg.FastSync || !cfg.MigrateHistory || !cfg.UseSLBForReferenceBranchNewCode {
		t.Errorf("explicit overrides not applied: %+v", cfg)
	}
}

func TestUnifiedMigrateConfigTargetWinsElseTopLevel(t *testing.T) {
	path := writeConfigFixture(t, `{
  "target": {
    "url": "https://sc", "token": "tok",
    "branch_regexp": "^tgt$", "group_name_regexp": "^tg",
    "pt_regexp": "^tpt", "fast_sync": true, "migrate_history": true,
    "use_slb_for_reference_branch_new_code": true,
    "branch_analyzed_after": "", "project_key_regexp": "^tp",
    "portfolio_key_regexp": "tK"
  },
  "branch_regexp": "^top$", "group_name_regexp": "^topg",
  "permission_template_regexp": "^toppt", "branch_analyzed_after": "2024-01-01",
  "portfolio_key_regexp": "topK", "portfolio_name_regexp": "topN",
  "project_key_regexp": "^topp", "concurrency": 5, "timeout": 11,
  "export_directory": "./x", "skip_issue_sync": true, "objects": ["projects"]
}`)
	cfg, err := LoadMigrateConfigFile(path)
	if err != nil {
		t.Fatalf("LoadMigrateConfigFile: %v", err)
	}
	if cfg.BranchRegexp != "^tgt$" || cfg.GroupNameRegexp != "^tg" || cfg.PermissionTemplateRegexp != "^tpt" {
		t.Errorf("target must win: %+v", cfg)
	}
	if cfg.PortfolioKeyRegexp != "tK" || cfg.PortfolioNameRegexp != "" {
		t.Errorf("portfolio pair must resolve from target as a whole: %q/%q", cfg.PortfolioKeyRegexp, cfg.PortfolioNameRegexp)
	}
	if !cfg.FastSync || !cfg.MigrateHistory || !cfg.UseSLBForReferenceBranchNewCode || !cfg.SkipIssueSync {
		t.Errorf("flexible bools not resolved: %+v", cfg)
	}
	if cfg.BranchAnalyzedAfter != "" {
		t.Errorf("explicit empty target branch_analyzed_after must win, got %q", cfg.BranchAnalyzedAfter)
	}
	if cfg.ProjectKeyFilter != "^tp" {
		t.Errorf("ProjectKeyFilter = %q, want target regexp", cfg.ProjectKeyFilter)
	}
	if cfg.Concurrency != 5 || cfg.Timeout != 11 || cfg.ExportDirectory != "./x" {
		t.Errorf("top-level defaults not filled: %+v", cfg)
	}
}

func TestUnifiedMigrateConfigTopLevelFallback(t *testing.T) {
	path := writeConfigFixture(t, `{
  "target": {"url": "https://sc", "token": "tok"},
  "branch_regexp": "^top$", "group_name_regexp": "^topg", "pt_regexp": "^toppt",
  "branch_analyzed_after": "2024-01-01", "portfolio_name_regexp": "topN",
  "fast_sync": true
}`)
	cfg, err := LoadMigrateConfigFile(path)
	if err != nil {
		t.Fatalf("LoadMigrateConfigFile: %v", err)
	}
	if cfg.BranchRegexp != "^top$" || cfg.GroupNameRegexp != "^topg" || cfg.PermissionTemplateRegexp != "^toppt" ||
		cfg.BranchAnalyzedAfter != "2024-01-01" || cfg.PortfolioNameRegexp != "topN" || !cfg.FastSync {
		t.Errorf("top-level fallback not applied: %+v", cfg)
	}
}

func TestSonarCloudMigrateConfigShape(t *testing.T) {
	path := writeConfigFixture(t, `{
  "sonarcloud": {
    "enterprise": {"key": "ent"}, "edition": "enterprise",
    "organizations": [{"key": "o", "token": "otok", "url": "https://o"}]
  },
  "settings": {"export_directory": "./f", "concurrency": 3, "project_data_build_concurrency": 2, "timeout": 9, "max_issue_comments": 5},
  "branch_regexp": "^b$", "group_name_regexp": "^g", "portfolio_key_regexp": "pk", "portfolio_name_regexp": "pn",
  "pt_regexp": "^pt", "branch_analyzed_after": "2024-02-02", "project_key": "legacy",
  "skip_project_data_migration": true, "fast_sync": true, "migrate_history": true,
  "use_slb_for_reference_branch_new_code": true, "objects": ["projects"]
}`)
	cfg, err := LoadMigrateConfigFile(path)
	if err != nil {
		t.Fatalf("LoadMigrateConfigFile: %v", err)
	}
	if cfg.EnterpriseKey != "ent" || cfg.Token != "otok" || cfg.URL != "https://o" || cfg.Edition != "enterprise" {
		t.Errorf("sonarcloud block not resolved: %+v", cfg)
	}
	if cfg.ExportDirectory != "./f" || cfg.Concurrency != 3 || cfg.BuildConcurrency != 2 || cfg.Timeout != 9 || cfg.MaxIssueComments != 5 {
		t.Errorf("settings not applied: %+v", cfg)
	}
	if cfg.BranchRegexp != "^b$" || cfg.GroupNameRegexp != "^g" || cfg.PortfolioKeyRegexp != "pk" ||
		cfg.PortfolioNameRegexp != "pn" || cfg.PermissionTemplateRegexp != "^pt" ||
		cfg.BranchAnalyzedAfter != "2024-02-02" || cfg.ProjectKeyFilter != "legacy" {
		t.Errorf("top-level fields not applied: %+v", cfg)
	}
	if !cfg.SkipProjectDataMigration || !cfg.FastSync || !cfg.MigrateHistory || !cfg.UseSLBForReferenceBranchNewCode {
		t.Errorf("bool overrides not applied: %+v", cfg)
	}
}

func TestCommandSectionedMigrateConfigOuterWins(t *testing.T) {
	path := writeConfigFixture(t, `{
  "migrate": {
    "token": "tok", "url": "https://sc",
    "branch_regexp": "^in$", "group_name_regexp": "^ing", "permission_template_regexp": "^inpt",
    "portfolio_key_regexp": "inK", "portfolio_name_regexp": "inN",
    "branch_analyzed_after": "2020-01-01"
  },
  "branch_regexp": "^out$", "group_name_regexp": "^outg", "pt_regexp": "^outpt",
  "portfolio_name_regexp": "outN", "branch_analyzed_after": "2024-03-03",
  "project_key_regexp": "^outp", "skip_issue_sync": true, "fast_sync": true,
  "objects": ["projects"]
}`)
	cfg, err := LoadMigrateConfigFile(path)
	if err != nil {
		t.Fatalf("LoadMigrateConfigFile: %v", err)
	}
	if cfg.BranchRegexp != "^out$" || cfg.GroupNameRegexp != "^outg" || cfg.PermissionTemplateRegexp != "^outpt" ||
		cfg.BranchAnalyzedAfter != "2024-03-03" || cfg.ProjectKeyFilter != "^outp" {
		t.Errorf("outer must win: %+v", cfg)
	}
	if cfg.PortfolioKeyRegexp != "" || cfg.PortfolioNameRegexp != "outN" {
		t.Errorf("outer portfolio pair must win as a whole: %q/%q", cfg.PortfolioKeyRegexp, cfg.PortfolioNameRegexp)
	}
	if !cfg.SkipIssueSync || !cfg.FastSync {
		t.Errorf("outer bools not propagated: %+v", cfg)
	}
}

func TestCommandSectionedMigrateConfigFallsBackToNested(t *testing.T) {
	path := writeConfigFixture(t, `{
  "migrate": {"token": "tok", "url": "https://sc", "branch_regexp": "^in$", "group_name_regexp": "^ing",
              "portfolio_key_regexp": "inK"}
}`)
	cfg, err := LoadMigrateConfigFile(path)
	if err != nil {
		t.Fatalf("LoadMigrateConfigFile: %v", err)
	}
	if cfg.BranchRegexp != "^in$" || cfg.GroupNameRegexp != "^ing" || cfg.PortfolioKeyRegexp != "inK" {
		t.Errorf("nested values must survive when outer unset: %+v", cfg)
	}
}

func TestFlatMigrateConfigShape(t *testing.T) {
	path := writeConfigFixture(t, `{
  "token": "t", "url": "https://sc", "enterprise_key": "e", "edition": "enterprise",
  "export_directory": "./f", "concurrency": 3, "timeout": 9,
  "branch_regexp": "^b$", "group_name_regexp": "^g", "portfolio_key_regexp": "pk",
  "pt_regexp": "^pt", "branch_analyzed_after": "2024-04-04", "project_key": "legacy",
  "skip_issue_sync": true, "migrate_history": true, "objects": ["projects"]
}`)
	cfg, err := LoadMigrateConfigFile(path)
	if err != nil {
		t.Fatalf("LoadMigrateConfigFile: %v", err)
	}
	if cfg.Token != "t" || cfg.EnterpriseKey != "e" || cfg.Concurrency != 3 || cfg.Timeout != 9 ||
		cfg.BranchRegexp != "^b$" || cfg.GroupNameRegexp != "^g" || cfg.PortfolioKeyRegexp != "pk" ||
		cfg.PermissionTemplateRegexp != "^pt" || cfg.BranchAnalyzedAfter != "2024-04-04" ||
		cfg.ProjectKeyFilter != "legacy" || !cfg.SkipIssueSync || !cfg.MigrateHistory {
		t.Errorf("flat shape not resolved: %+v", cfg)
	}
}

func TestSonarCloudBlockToMigrateConfigFallbacks(t *testing.T) {
	sc := sonarCloudBlock{Token: "flatTok", URL: "https://flat", EnterpriseKey: "flatEnt", Edition: "ed"}
	cfg := sc.toMigrateConfig(nil)
	if cfg.Token != "flatTok" || cfg.URL != "https://flat" || cfg.EnterpriseKey != "flatEnt" || cfg.Edition != "ed" {
		t.Errorf("flat fallbacks not used: %+v", cfg)
	}
}
