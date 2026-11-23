// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// configFileShape is the union of the four documented config-file formats
// (see examples/config-migrate.example.json, examples/config.example.json,
// examples/migration-config.example.json, and examples/config.unified.example.json).
// Issues #176, #266.
//
// Detection at parse time:
//   - source or target present -> shape 4 (unified)
//   - sonarcloud present       -> shape 3 (side-sectioned)
//   - migrate present          -> shape 2 (command-sectioned)
//   - else                     -> shape 1 (flat)
type configFileShape struct {
	// Shape 1 (flat) fields. Also reused inside Shape 2's "migrate" object.
	Token              string `json:"token"`
	EnterpriseKey      string `json:"enterprise_key"`
	URL                string `json:"url"`
	Edition            string `json:"edition"`
	ExportDirectory    string `json:"export_directory"`
	Concurrency        int    `json:"concurrency"`
	BuildConcurrency   int    `json:"project_data_build_concurrency"`
	Timeout            int    `json:"timeout"`
	RunID              string `json:"run_id"`
	TargetTask         string `json:"target_task"`
	SkipProfiles       bool   `json:"skip_profiles"`
	IncludeProjectData bool   `json:"include_project_data"`
	// SkipIssueSync controls whether the final per-issue / per-hotspot
	// metadata sync runs after project-data is replayed (#299).
	// Defaults to false (sync happens); set to true (or on / yes) to
	// skip the sync. Pointer + custom unmarshaller so we can
	// distinguish "absent" from "explicit false".
	SkipIssueSync *FlexibleBool `json:"skip_issue_sync"`
	// SkipProjectDataMigration disables the entire project-data import:
	// importProjectData plus the trailing issue + hotspot syncs (#303).
	// Defaults to false (data is migrated). Setting true (or on/yes)
	// implies SkipIssueSync — there's nothing to sync against. Same
	// FlexibleBool semantics as skip_issue_sync.
	SkipProjectDataMigration *FlexibleBool `json:"skip_project_data_migration"`
	Debug                    bool          `json:"debug"`
	ExcludeBranches          []string      `json:"exclude_branches"`
	// UnsupportedLanguages selects how projects containing files in a
	// language with no target quality profile are handled (#474):
	// "exclude" (default), "skip", or "warn".
	UnsupportedLanguages string `json:"unsupported_languages"`
	// FastSync skips tagging and back-linking hotspots (and issues) that
	// have zero user changes on the source — original state, no comments,
	// no custom tags (#527). Defaults to false (every hotspot is tagged
	// and back-linked, current behavior). Same FlexibleBool semantics as
	// skip_issue_sync.
	FastSync *FlexibleBool `json:"fast_sync"`
	// MigrateHistory opts into the project-history migration PoC (#554).
	// Defaults to false. Same FlexibleBool semantics as skip_issue_sync.
	MigrateHistory *FlexibleBool `json:"migrate_history"`
	// UseSLBForReferenceBranchNewCode migrates SonarQube Server branches whose
	// new code definition is a reference branch as short-lived branches on
	// SonarQube Cloud (#655). Same FlexibleBool semantics as migrate_history.
	UseSLBForReferenceBranchNewCode *FlexibleBool `json:"use_slb_for_reference_branch_new_code"`
	// MaxIssueComments — see MigrateConfig.MaxIssueComments (#571).
	MaxIssueComments int `json:"max_issue_comments"`
	// BranchAnalyzedAfter — see MigrateConfig.BranchAnalyzedAfter (#583).
	// Applies to shapes that have no source/target split (SonarCloud,
	// command-sectioned, flat); the unified shape resolves it via
	// unifiedTargetBlock.BranchAnalyzedAfter / resolveBranchAnalyzedAfter
	// instead, since that shape can override it per-phase.
	BranchAnalyzedAfter string `json:"branch_analyzed_after"`
	// ConfirmedOrgs is reset-only: it additively pre-populates
	// ResetConfig.ConfirmedOrgs (#550) for config-driven / programmatic
	// callers that don't go through cmd/reset.go's interactive
	// confirmation prompt or --organization flag. Ignored by
	// toMigrateConfig / MigrateConfig. See toResetConfig for the
	// outer-wins-else-nested-"migrate" resolution (mirrors
	// skip_issue_sync's precedence).
	ConfirmedOrgs []string `json:"confirmed_orgs"`

	// Objects and ProjectKey are top-level (global) fields regardless of
	// shape — the issue documents "objects" as settable "at global
	// level" (#536), mirroring extract's config_file.go. Objects, when
	// present, limits migration to the selected object categories
	// (settings, permission_templates, quality_profiles, quality_gates,
	// projects, portfolios, groups, license_profiles — aliases
	// qp/qg/pt/lp); validated and resolved into MigrateConfig.Objects by
	// LoadMigrateConfigFile via common.ParseObjects. ProjectKey is the
	// raw --project_key-equivalent regexp pattern, copied as-is into
	// MigrateConfig.ProjectKeyFilter (migrate never resolves it to a
	// concrete key list; createProjects filters locally).
	Objects    []string `json:"objects"`
	ProjectKey string   `json:"project_key"`
	// ProjectKeyRegexp is the top-level "project_key_regexp" value, the
	// replacement for the deprecated ProjectKey/"project_key" (#592) — it
	// was always a regexp, never a literal single key. In the unified
	// shape, target.project_key_regexp wins when both are set — see
	// unifiedTargetBlock. common.ResolveDeprecatedProjectKey combines
	// this with ProjectKey wherever both are read.
	ProjectKeyRegexp string `json:"project_key_regexp"`
	// BranchRegexp is the top-level regexp pattern restricting which
	// branches extract/migrate/transfer process (#582). Mirrors
	// ProjectKey's top-level/shape semantics.
	BranchRegexp string `json:"branch_regexp"`
	// PortfolioKeyRegexp / PortfolioNameRegexp restrict which portfolios
	// migrate processes (#666). See MigrateConfig.PortfolioKeyRegexp.
	PortfolioKeyRegexp  string `json:"portfolio_key_regexp"`
	PortfolioNameRegexp string `json:"portfolio_name_regexp"`
	// GroupNameRegexp is the top-level regexp pattern restricting which
	// groups extract/migrate/transfer process (#669). target.group_name_regexp
	// wins in the unified shape.
	GroupNameRegexp string `json:"group_name_regexp"`
	// PermissionTemplateRegexp is the top-level "permission_template_regexp"
	// value (alias "pt_regexp"): only permission templates whose name
	// matches are migrated. In the unified shape,
	// target.permission_template_regexp wins when both are set. #665.
	PermissionTemplateRegexp string `json:"permission_template_regexp"`
	PTRegexp                 string `json:"pt_regexp"`

	// Shape 2 (command-sectioned).
	Migrate *configFileShape `json:"migrate"`

	// Shape 3 (side-sectioned). sonarqube + extract blocks are ignored —
	// migrate/reset only consume SonarCloud-side values.
	SonarCloud *sonarCloudBlock `json:"sonarcloud"`
	Settings   *settingsBlock   `json:"settings"`

	// Shape 4 (unified, #266). Migrate / reset pull from the "target"
	// block with top-level concurrency / timeout / export_directory
	// as defaults. The "source" block is ignored.
	Source *unifiedSourceBlock `json:"source"`
	Target *unifiedTargetBlock `json:"target"`
}

// unifiedSourceBlock is here only so the unified shape parses
// without dropping fields. Migrate ignores everything in it.
type unifiedSourceBlock struct {
	URL             string `json:"url"`
	Token           string `json:"token"`
	ExtractType     string `json:"extract_type"`
	Concurrency     int    `json:"concurrency"`
	Timeout         int    `json:"timeout"`
	PEMFilePath     string `json:"pem_file_path"`
	KeyFilePath     string `json:"key_file_path"`
	CertPassword    string `json:"cert_password"`
	TargetTask      string `json:"target_task"`
	ExtractID       string `json:"extract_id"`
	EnterpriseKey   string `json:"enterprise_key"`
	OrganizationKey string `json:"organization_key"`
	Edition         string `json:"edition"`
	RunID           string `json:"run_id"`
}

// unifiedTargetBlock carries the SonarQube Cloud-side credentials for
// the unified config shape (#266). organization_key is provisional
// for future SQC-org-to-SQC-org migration and is ignored for now.
type unifiedTargetBlock struct {
	URL                 string `json:"url"`
	Token               string `json:"token"`
	EnterpriseKey       string `json:"enterprise_key"`
	Edition             string `json:"edition"`
	Concurrency         int    `json:"concurrency"`
	Timeout             int    `json:"timeout"`
	RunID               string `json:"run_id"`
	TargetTask          string `json:"target_task"`
	BuildConcurrency    int    `json:"project_data_build_concurrency"`
	OrganizationKey     string `json:"organization_key"`     // provisional, ignored
	DefaultOrganization string `json:"default_organization"` // #281
	ProjectKeyPattern   string `json:"project_key_pattern"`  // #138
	// ProjectKeyRegexp, when set, overrides the top-level
	// "project_key_regexp" for this target. #592. Not to be confused
	// with ProjectKeyPattern above, the target-key rendering template.
	ProjectKeyRegexp string   `json:"project_key_regexp"`
	ExcludeBranches  []string `json:"exclude_branches"`
	// UnsupportedLanguages — see configFileShape.UnsupportedLanguages (#474).
	UnsupportedLanguages string `json:"unsupported_languages"`
	// FastSync — see configFileShape.FastSync (#527).
	FastSync *FlexibleBool `json:"fast_sync"`
	// MigrateHistory — see configFileShape.MigrateHistory (#554).
	MigrateHistory *FlexibleBool `json:"migrate_history"`
	// UseSLBForReferenceBranchNewCode migrates SonarQube Server branches whose
	// new code definition is a reference branch as short-lived branches on
	// SonarQube Cloud (#655). Same FlexibleBool semantics as migrate_history.
	UseSLBForReferenceBranchNewCode *FlexibleBool `json:"use_slb_for_reference_branch_new_code"`
	// MaxIssueComments — see configFileShape.MaxIssueComments (#571).
	MaxIssueComments int `json:"max_issue_comments"`
	// BranchRegexp — see MigrateConfig.BranchRegexp (#582).
	BranchRegexp string `json:"branch_regexp"`
	// PortfolioKeyRegexp / PortfolioNameRegexp — see MigrateConfig (#666).
	PortfolioKeyRegexp  string `json:"portfolio_key_regexp"`
	PortfolioNameRegexp string `json:"portfolio_name_regexp"`
	// GroupNameRegexp — see MigrateConfig.GroupNameRegexp (#669).
	GroupNameRegexp string `json:"group_name_regexp"`
	// PermissionTemplateRegexp (alias PTRegexp) — see
	// configFileShape.PermissionTemplateRegexp (#665).
	PermissionTemplateRegexp string `json:"permission_template_regexp"`
	PTRegexp                 string `json:"pt_regexp"`
	// BranchAnalyzedAfter — see configFileShape.BranchAnalyzedAfter (#583).
	// Pointer (not a plain string) so an explicit "" here can override a
	// non-empty top-level value, distinguishing "target didn't set this"
	// (nil, fall through to top-level) from "target explicitly wants no
	// filter" (non-nil, even when the pointed-to value is "").
	BranchAnalyzedAfter *string `json:"branch_analyzed_after"`
}

type sonarCloudBlock struct {
	// Legacy flat fields (Shape 3 v1).
	URL           string `json:"url"`
	Token         string `json:"token"`
	EnterpriseKey string `json:"enterprise_key"`
	Edition       string `json:"edition"`

	// New nested format.
	Enterprise    *enterpriseBlock `json:"enterprise"`
	Organizations []OrgConfigEntry `json:"organizations"`
}

type enterpriseBlock struct {
	Key string `json:"key"`
}

// OrgConfigEntry holds per-organization SonarCloud credentials used both for
// migration and to pre-populate organizations.csv during structure.
type OrgConfigEntry struct {
	Key   string `json:"key"`
	Token string `json:"token"`
	URL   string `json:"url"`
}

type settingsBlock struct {
	ExportDirectory  string `json:"export_directory"`
	Concurrency      int    `json:"concurrency"`
	BuildConcurrency int    `json:"project_data_build_concurrency"`
	Timeout          int    `json:"timeout"`
	// MaxIssueComments — see configFileShape.MaxIssueComments (#571).
	MaxIssueComments int `json:"max_issue_comments"`
}

func parseConfigFile(path string) (configFileShape, error) {
	return common.ParseJSONConfigFile[configFileShape](path)
}

func (s configFileShape) toMigrateConfig() MigrateConfig {
	switch {
	case s.Source != nil || s.Target != nil:
		return s.unifiedMigrateConfig()
	case s.SonarCloud != nil:
		return s.sonarCloudMigrateConfig()
	case s.Migrate != nil:
		return s.commandSectionedMigrateConfig()
	default:
		return s.flatMigrateConfig()
	}
}

// targetFieldOrZero reads a field off the unified shape's "target" block,
// returning the zero value when target is nil. It centralizes the
// "only read from target when present" check so the per-shape resolvers
// below don't each need their own nil guard.
func targetFieldOrZero[T any](target *unifiedTargetBlock, get func(*unifiedTargetBlock) T) T {
	if target != nil {
		return get(target)
	}
	var zero T
	return zero
}

// applyTargetBaseFields copies the "target" sub-object's directly-mapped
// fields (#266) onto cfg. Fields with target-wins-else-top-level
// resolution (unsupported_languages, branch_regexp, ...) are handled
// separately in unifiedMigrateConfig.
func applyTargetBaseFields(cfg *MigrateConfig, target *unifiedTargetBlock) {
	if target == nil {
		return
	}
	cfg.URL = target.URL
	cfg.Token = target.Token
	cfg.EnterpriseKey = target.EnterpriseKey
	cfg.Edition = target.Edition
	cfg.RunID = target.RunID
	cfg.TargetTask = target.TargetTask
	cfg.Concurrency = target.Concurrency
	cfg.BuildConcurrency = target.BuildConcurrency
	cfg.Timeout = target.Timeout
	cfg.DefaultOrganization = target.DefaultOrganization
	cfg.ProjectKeyPattern = target.ProjectKeyPattern
	cfg.ExcludeBranches = target.ExcludeBranches
	cfg.UnsupportedLanguages = target.UnsupportedLanguages
	cfg.MaxIssueComments = target.MaxIssueComments
	cfg.BranchRegexp = target.BranchRegexp
	cfg.GroupNameRegexp = target.GroupNameRegexp
}

// resolvePortfolioRegexpFromTarget implements #666's "resolve the pair as a
// whole" rule for the unified shape: target's portfolio key/name regexps
// win together when either is set, else the top-level pair is used.
func resolvePortfolioRegexpFromTarget(target *unifiedTargetBlock, key, name string) (string, string) {
	if target != nil && (target.PortfolioKeyRegexp != "" || target.PortfolioNameRegexp != "") {
		return target.PortfolioKeyRegexp, target.PortfolioNameRegexp
	}
	return key, name
}

// applyPortfolioOuterOverride implements #666's outer-wins-as-a-whole rule
// for the command-sectioned shape: the outer portfolio key/name regexp
// pair overrides the nested "migrate" value only when either is set.
func applyPortfolioOuterOverride(cfg *MigrateConfig, s configFileShape) {
	if s.PortfolioKeyRegexp != "" || s.PortfolioNameRegexp != "" {
		cfg.PortfolioKeyRegexp, cfg.PortfolioNameRegexp = s.PortfolioKeyRegexp, s.PortfolioNameRegexp
	}
}

// fillConcurrencyDefaults fills cfg's zero-valued concurrency / timeout /
// max-issue-comments fields from the top-level config (used by the unified
// shape, where the "target" block supplies these when present).
func fillConcurrencyDefaults(cfg *MigrateConfig, s configFileShape) {
	if cfg.Concurrency == 0 {
		cfg.Concurrency = s.Concurrency
	}
	if cfg.BuildConcurrency == 0 {
		cfg.BuildConcurrency = s.BuildConcurrency
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = s.Timeout
	}
	if cfg.MaxIssueComments == 0 {
		cfg.MaxIssueComments = s.MaxIssueComments
	}
}

// applySkipSyncOverrides applies the top-level skip_issue_sync /
// skip_project_data_migration overrides (#299, #303), shared by every
// shape. Field names match MigrateConfig one-for-one, so there's no
// inversion.
func applySkipSyncOverrides(cfg *MigrateConfig, s configFileShape) {
	if s.SkipIssueSync != nil && s.SkipIssueSync.Set {
		cfg.SkipIssueSync = s.SkipIssueSync.Value
	}
	if s.SkipProjectDataMigration != nil && s.SkipProjectDataMigration.Set {
		cfg.SkipProjectDataMigration = s.SkipProjectDataMigration.Value
	}
}

// applyFlexibleBoolOverrides applies the outer-wins-else-inner
// FlexibleBool overrides shared by the sonarcloud and command-sectioned
// shapes (#527 fast_sync, #554 migrate_history, #655
// use_slb_for_reference_branch_new_code).
func applyFlexibleBoolOverrides(cfg *MigrateConfig, s configFileShape) {
	if s.FastSync != nil && s.FastSync.Set {
		cfg.FastSync = s.FastSync.Value
	}
	if s.MigrateHistory != nil && s.MigrateHistory.Set {
		cfg.MigrateHistory = s.MigrateHistory.Value
	}
	if s.UseSLBForReferenceBranchNewCode != nil && s.UseSLBForReferenceBranchNewCode.Set {
		cfg.UseSLBForReferenceBranchNewCode = s.UseSLBForReferenceBranchNewCode.Value
	}
}

// unifiedMigrateConfig builds a MigrateConfig for the #266 unified shape.
// Migrate pulls from the "target" sub-object; top-level concurrency /
// timeout / export_directory supply defaults. The "source" block is
// ignored. organization_key is provisional and ignored.
func (s configFileShape) unifiedMigrateConfig() MigrateConfig {
	var cfg MigrateConfig
	applyTargetBaseFields(&cfg, s.Target)
	// #474 — target.unsupported_languages wins, else the top-level field.
	cfg.UnsupportedLanguages = resolveUnsupportedLanguages(cfg.UnsupportedLanguages, s.UnsupportedLanguages)
	// #582 — target.branch_regexp wins, else the top-level field.
	cfg.BranchRegexp = common.FirstNonEmpty(cfg.BranchRegexp, s.BranchRegexp)
	// #666 — resolve the pair as a whole so a target-level key does not
	// conflict with a top-level name (or vice versa).
	cfg.PortfolioKeyRegexp, cfg.PortfolioNameRegexp = resolvePortfolioRegexpFromTarget(s.Target, s.PortfolioKeyRegexp, s.PortfolioNameRegexp)
	// #669 — target.group_name_regexp wins, else the top-level field.
	cfg.GroupNameRegexp = common.FirstNonEmpty(cfg.GroupNameRegexp, s.GroupNameRegexp)
	// #665 — target.permission_template_regexp wins, else the top-level field.
	targetPT := targetFieldOrZero(s.Target, func(t *unifiedTargetBlock) string {
		return common.FirstNonEmpty(t.PermissionTemplateRegexp, t.PTRegexp)
	})
	cfg.PermissionTemplateRegexp = common.FirstNonEmpty(targetPT, s.ptRegexp())
	// #527 — target.fast_sync wins, else the top-level field, else false.
	cfg.FastSync = resolveFastSync(targetFieldOrZero(s.Target, func(t *unifiedTargetBlock) *FlexibleBool { return t.FastSync }), s.FastSync)
	// #554 — target.migrate_history wins, else the top-level field, else false.
	cfg.MigrateHistory = resolveMigrateHistory(targetFieldOrZero(s.Target, func(t *unifiedTargetBlock) *FlexibleBool { return t.MigrateHistory }), s.MigrateHistory)
	// #655 — target.use_slb_for_reference_branch_new_code wins, else the
	// top-level field, else false.
	cfg.UseSLBForReferenceBranchNewCode = resolveUseSLBForReferenceBranchNewCode(
		targetFieldOrZero(s.Target, func(t *unifiedTargetBlock) *FlexibleBool { return t.UseSLBForReferenceBranchNewCode }),
		s.UseSLBForReferenceBranchNewCode)
	// #583 — target.branch_analyzed_after wins when explicitly present
	// (even if empty, meaning "no filter for migrate"), else the
	// top-level field, else "" (no filter).
	cfg.BranchAnalyzedAfter = resolveBranchAnalyzedAfter(
		targetFieldOrZero(s.Target, func(t *unifiedTargetBlock) *string { return t.BranchAnalyzedAfter }),
		s.BranchAnalyzedAfter)
	fillConcurrencyDefaults(&cfg, s)
	cfg.ExportDirectory = s.ExportDirectory
	// Top-level skip_issue_sync / skip_project_data_migration apply to
	// every shape (#299, #303).
	applySkipSyncOverrides(&cfg, s)
	cfg.objectsRaw = s.Objects
	// #592: target.project_key_regexp wins over the top-level
	// project_key_regexp, which in turn wins over the deprecated
	// project_key (top-level only — it never had a target override).
	targetProjectKeyRegexp := targetFieldOrZero(s.Target, func(t *unifiedTargetBlock) string { return t.ProjectKeyRegexp })
	cfg.ProjectKeyFilter = common.ResolveDeprecatedProjectKey(s.ProjectKey, common.FirstNonEmpty(targetProjectKeyRegexp, s.ProjectKeyRegexp))
	return cfg
}

// sonarCloudMigrateConfig builds a MigrateConfig for the side-sectioned
// "sonarcloud" shape.
func (s configFileShape) sonarCloudMigrateConfig() MigrateConfig {
	cfg := s.SonarCloud.toMigrateConfig(s.Settings)
	cfg.UnsupportedLanguages = resolveUnsupportedLanguages(cfg.UnsupportedLanguages, s.UnsupportedLanguages)
	// sonarCloudBlock.toMigrateConfig never sets BranchRegexp, so this is
	// just the top-level field, but written with FirstNonEmpty for
	// consistency with the unified-shape resolution above (#582).
	cfg.BranchRegexp = common.FirstNonEmpty(cfg.BranchRegexp, s.BranchRegexp)
	cfg.PortfolioKeyRegexp = common.FirstNonEmpty(cfg.PortfolioKeyRegexp, s.PortfolioKeyRegexp)
	cfg.PortfolioNameRegexp = common.FirstNonEmpty(cfg.PortfolioNameRegexp, s.PortfolioNameRegexp)
	cfg.GroupNameRegexp = common.FirstNonEmpty(cfg.GroupNameRegexp, s.GroupNameRegexp)
	cfg.PermissionTemplateRegexp = s.ptRegexp()
	applySkipSyncOverrides(&cfg, s)
	applyFlexibleBoolOverrides(&cfg, s)
	cfg.objectsRaw = s.Objects
	cfg.ProjectKeyFilter = common.ResolveDeprecatedProjectKey(s.ProjectKey, s.ProjectKeyRegexp)
	cfg.BranchAnalyzedAfter = s.BranchAnalyzedAfter
	return cfg
}

// commandSectionedMigrateConfig builds a MigrateConfig for the
// command-sectioned "migrate" shape, where outer-level fields win over the
// same fields nested inside "migrate" when set, else fall back to the
// value already resolved by the recursive call.
func (s configFileShape) commandSectionedMigrateConfig() MigrateConfig {
	cfg := s.Migrate.toMigrateConfig()
	cfg.UnsupportedLanguages = resolveUnsupportedLanguages(cfg.UnsupportedLanguages, s.UnsupportedLanguages)
	// Outer-level skip_issue_sync / skip_project_data_migration win when
	// both outer and inner set them (#299, #303). If only outer is set,
	// propagate it down.
	applySkipSyncOverrides(&cfg, s)
	// Same outer-wins-else-inner semantics for fast_sync (#527),
	// migrate_history (#554), and use_slb_for_reference_branch_new_code (#655).
	applyFlexibleBoolOverrides(&cfg, s)
	// #536: outer-level "objects" / "project_key" win over the same
	// fields nested inside "migrate" — but fall back to the nested
	// value (already captured above by the recursive call) when the
	// outer level didn't set them.
	if len(s.Objects) > 0 {
		cfg.objectsRaw = s.Objects
	}
	if outer := common.ResolveDeprecatedProjectKey(s.ProjectKey, s.ProjectKeyRegexp); outer != "" {
		cfg.ProjectKeyFilter = outer
	}
	// #582: same outer-wins-else-nested semantics for branch_regexp.
	if s.BranchRegexp != "" {
		cfg.BranchRegexp = s.BranchRegexp
	}
	// #666: the outer portfolio regexp pair wins as a whole when set.
	applyPortfolioOuterOverride(&cfg, s)
	if s.GroupNameRegexp != "" {
		cfg.GroupNameRegexp = s.GroupNameRegexp
	}
	// #665: same outer-wins-else-nested semantics for permission_template_regexp.
	if pt := s.ptRegexp(); pt != "" {
		cfg.PermissionTemplateRegexp = pt
	}
	// Same outer-wins-else-inner semantics for branch_analyzed_after (#583).
	if s.BranchAnalyzedAfter != "" {
		cfg.BranchAnalyzedAfter = s.BranchAnalyzedAfter
	}
	return cfg
}

// flatMigrateConfig builds a MigrateConfig for the flat (shape 1) config
// file, the default when none of the other shapes' marker fields are set.
func (s configFileShape) flatMigrateConfig() MigrateConfig {
	cfg := MigrateConfig{
		Token:              s.Token,
		EnterpriseKey:      s.EnterpriseKey,
		URL:                s.URL,
		Edition:            s.Edition,
		ExportDirectory:    s.ExportDirectory,
		Concurrency:        s.Concurrency,
		BuildConcurrency:   s.BuildConcurrency,
		Timeout:            s.Timeout,
		MaxIssueComments:   s.MaxIssueComments,
		RunID:              s.RunID,
		TargetTask:         s.TargetTask,
		SkipProfiles:       s.SkipProfiles,
		IncludeProjectData: s.IncludeProjectData,
		Debug:              s.Debug,
		ExcludeBranches:    s.ExcludeBranches,
		// #474 — flat shape reads the field directly.
		UnsupportedLanguages: s.UnsupportedLanguages,
		BranchRegexp:         s.BranchRegexp,
		// #666 — flat shape reads the fields directly.
		PortfolioKeyRegexp:  s.PortfolioKeyRegexp,
		PortfolioNameRegexp: s.PortfolioNameRegexp,
		GroupNameRegexp:     s.GroupNameRegexp,
		// #665 — flat shape reads the field directly.
		PermissionTemplateRegexp: s.ptRegexp(),
		// #583 — flat shape reads the field directly.
		BranchAnalyzedAfter: s.BranchAnalyzedAfter,
	}
	applySkipSyncOverrides(&cfg, s)
	applyFlexibleBoolOverrides(&cfg, s)
	cfg.objectsRaw = s.Objects
	cfg.ProjectKeyFilter = common.ResolveDeprecatedProjectKey(s.ProjectKey, s.ProjectKeyRegexp)
	return cfg
}

// ptRegexp returns the permission_template_regexp value, falling back to its
// pt_regexp alias. #665.
func (s configFileShape) ptRegexp() string {
	return common.FirstNonEmpty(s.PermissionTemplateRegexp, s.PTRegexp)
}

func (sc sonarCloudBlock) toMigrateConfig(settings *settingsBlock) MigrateConfig {
	cfg := MigrateConfig{Edition: sc.Edition}

	if sc.Enterprise != nil && sc.Enterprise.Key != "" {
		cfg.EnterpriseKey = sc.Enterprise.Key
	} else {
		cfg.EnterpriseKey = sc.EnterpriseKey
	}

	if len(sc.Organizations) > 0 {
		first := sc.Organizations[0]
		cfg.Token = first.Token
		cfg.URL = first.URL
	}
	if cfg.Token == "" {
		cfg.Token = sc.Token
	}
	if cfg.URL == "" {
		cfg.URL = sc.URL
	}

	if settings != nil {
		cfg.ExportDirectory = settings.ExportDirectory
		cfg.Concurrency = settings.Concurrency
		cfg.BuildConcurrency = settings.BuildConcurrency
		cfg.Timeout = settings.Timeout
		cfg.MaxIssueComments = settings.MaxIssueComments
	}
	return cfg
}

func (s configFileShape) toResetConfig() ResetConfig {
	m := s.toMigrateConfig()
	cfg := ResetConfig{
		Token:           m.Token,
		EnterpriseKey:   m.EnterpriseKey,
		Edition:         m.Edition,
		URL:             m.URL,
		Concurrency:     m.Concurrency,
		ExportDirectory: m.ExportDirectory,
		Debug:           m.Debug,
	}
	// #550 — confirmed_orgs is additive and reset-only (see the field's
	// doc comment on configFileShape); it does not flow through
	// MigrateConfig. Outer-level value wins when set, mirroring
	// skip_issue_sync's outer-wins-else-inner semantics; otherwise fall
	// back to a nested "migrate" section's value (shape 2).
	switch {
	case len(s.ConfirmedOrgs) > 0:
		cfg.ConfirmedOrgs = s.ConfirmedOrgs
	case s.Migrate != nil && len(s.Migrate.ConfirmedOrgs) > 0:
		cfg.ConfirmedOrgs = s.Migrate.ConfirmedOrgs
	}
	return cfg
}

// LoadSonarCloudOrgsFromConfigFile returns the organizations list from a
// side-sectioned config file. Returns nil when the file uses a different shape
// or no organizations are defined.
//
// Only shape 3 carries per-organization credentials, so this deliberately
// does NOT synthesize an entry from the unified shape's
// target.default_organization — an entry with no token or URL would be a
// lie. Callers that only need an organization key should fall back to
// LoadDefaultOrganizationFromConfigFile when this returns nothing (#566).
func LoadSonarCloudOrgsFromConfigFile(path string) ([]OrgConfigEntry, error) {
	shape, err := parseConfigFile(path)
	if err != nil {
		return nil, err
	}
	if shape.SonarCloud == nil {
		return nil, nil
	}
	return shape.SonarCloud.Organizations, nil
}

// LoadDefaultOrganizationFromConfigFile returns the SonarQube Cloud
// organization key a migrate run would stamp onto every unmapped row of
// organizations.csv — target.default_organization in the unified shape
// (#281). Returns "" when the config file defines none.
//
// Resolution goes through toMigrateConfig rather than reading
// shape.Target directly, so the helper keeps matching migrate exactly if
// another shape starts carrying the field later. Used by structure and
// predictive-report, which otherwise leave sonarcloud_org_key empty and
// then report every entity as "Organization skipped" (#566).
func LoadDefaultOrganizationFromConfigFile(path string) (string, error) {
	shape, err := parseConfigFile(path)
	if err != nil {
		return "", err
	}
	return shape.toMigrateConfig().DefaultOrganization, nil
}

// LoadMigrateConfigFile parses a JSON config file in any of the three
// documented shapes and returns the populated MigrateConfig. The
// config-file "objects" array (any shape) is validated and resolved into
// MigrateConfig.Objects via common.ParseObjects (#536); an unrecognized
// value aborts with an error before any API call is made.
func LoadMigrateConfigFile(path string) (MigrateConfig, error) {
	shape, err := parseConfigFile(path)
	if err != nil {
		return MigrateConfig{}, err
	}
	cfg := shape.toMigrateConfig()
	objects, err := common.ParseObjects(cfg.objectsRaw)
	if err != nil {
		return MigrateConfig{}, err
	}
	cfg.Objects = objects
	cfg.objectsRaw = nil
	// Mirror cmd/migrate.go's applyMigrateProjectKeyFlag: the filter
	// only applies when the "projects" category is selected (#536).
	if cfg.Objects != nil && !cfg.Objects[common.ObjectProjects] {
		cfg.ProjectKeyFilter = ""
	}
	return cfg, nil
}

// LoadResetConfigFile parses a JSON config file in any of the three
// documented shapes and returns the populated ResetConfig.
func LoadResetConfigFile(path string) (ResetConfig, error) {
	shape, err := parseConfigFile(path)
	if err != nil {
		return ResetConfig{}, err
	}
	return shape.toResetConfig(), nil
}
