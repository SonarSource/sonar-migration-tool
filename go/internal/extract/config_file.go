// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"fmt"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// configFileShape is the union of the four documented config-file
// formats for the extract command (issue #158, #266):
//
//   - examples/config-extract.example.json — flat top-level keys
//   - examples/config.example.json         — "extract" sub-object
//   - examples/migration-config.example.json — "sonarqube" + "settings"
//     sub-objects (the SonarCloud-side blocks are ignored by extract)
//   - examples/config.unified.example.json — top-level
//     concurrency/timeout/export_directory + "source"/"target"
//     sub-objects (#266 unified shape)
//
// Detection at parse time:
//   - source or target present -> shape 4 (unified)
//   - sonarqube present        -> shape 3 (side-sectioned)
//   - extract present          -> shape 2 (command-sectioned)
//   - else                     -> shape 1 (flat)
type configFileShape struct {
	// Shape 1 (flat) fields. Reused inside Shape 2's "extract" object.
	URL                      string `json:"url"`
	Token                    string `json:"token"`
	ExportDirectory          string `json:"export_directory"`
	ExtractType              string `json:"extract_type"`
	PEMFilePath              string `json:"pem_file_path"`
	KeyFilePath              string `json:"key_file_path"`
	CertPassword             string `json:"cert_password"`
	Concurrency              int    `json:"concurrency"`
	Timeout                  int    `json:"timeout"`
	ExtractID                string `json:"extract_id"`
	TargetTask               string `json:"target_task"`
	SkipProjectDataMigration bool   `json:"skip_project_data_migration"`
	SkipIssueSync            bool   `json:"skip_issue_sync"` // #398
	// MigrateHistory / HistoryMaxPoints / HistoryMinIntervalDays — see
	// ExtractConfig doc comments. #554. Top-level only: like
	// skip_project_data_migration / skip_issue_sync, this is a plain bool
	// with no per-shape nesting.
	MigrateHistory   bool `json:"migrate_history"`
	HistoryMaxPoints int  `json:"history_max_points"`
	// Pointer, unlike HistoryMaxPoints: 0 is a legal explicit value here
	// ("no spacing rule"), so absent and 0 must stay distinguishable.
	HistoryMinIntervalDays *int `json:"history_min_interval_days"`
	// Objects and ProjectKey are always read from the top level of the
	// config file regardless of shape (#536: the issue documents "objects"
	// as settable "at global level"). For shape 2 (command-sectioned),
	// toExtractConfig recurses into s.Extract.toExtractConfig() for
	// everything else, which would only see a NESTED "extract.objects" /
	// "extract.project_key" (same struct type, reused) — so that branch
	// explicitly re-applies the outer-level values afterward, letting the
	// global value win but still falling back to the command-scoped one.
	Objects    []string `json:"objects"`
	ProjectKey string   `json:"project_key"`

	// Shape 2 (command-sectioned).
	Extract *configFileShape `json:"extract"`

	// Shape 3 (side-sectioned). The SonarCloud side of
	// migration-config.example.json is ignored — extract only consumes
	// the SonarQube-side credentials and shared settings block.
	SonarQube *sonarQubeBlock `json:"sonarqube"`
	Settings  *settingsBlock  `json:"settings"`

	// Shape 4 (unified, #266). Extract pulls from the "source" block,
	// with top-level concurrency / timeout / export_directory as
	// defaults. The "target" block exists in this shape for migrate
	// but is ignored by extract.
	Source *unifiedSourceBlock `json:"source"`
	Target *unifiedTargetBlock `json:"target"`
}

// unifiedSourceBlock mirrors the "source" sub-object documented in
// #266. The enterprise_key / organization_key / edition / run_id
// fields are accepted but currently ignored — they're provisional for
// future SQC-to-SQC migration work.
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
	EnterpriseKey   string `json:"enterprise_key"`   // provisional, ignored
	OrganizationKey string `json:"organization_key"` // provisional, ignored
	Edition         string `json:"edition"`          // provisional, ignored
	RunID           string `json:"run_id"`           // ignored by extract
}

// unifiedTargetBlock mirrors the "target" sub-object documented in
// #266. Lives here so the unified shape parses successfully for the
// extract command even though extract ignores the block.
type unifiedTargetBlock struct {
	URL             string `json:"url"`
	Token           string `json:"token"`
	EnterpriseKey   string `json:"enterprise_key"`
	Edition         string `json:"edition"`
	Concurrency     int    `json:"concurrency"`
	Timeout         int    `json:"timeout"`
	RunID           string `json:"run_id"`
	TargetTask      string `json:"target_task"`
	OrganizationKey string `json:"organization_key"` // provisional, ignored
}

type sonarQubeBlock struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type settingsBlock struct {
	ExportDirectory string `json:"export_directory"`
	Concurrency     int    `json:"concurrency"`
	Timeout         int    `json:"timeout"`
}

func parseConfigFile(path string) (configFileShape, error) {
	return common.ParseJSONConfigFile[configFileShape](path)
}

// applyHistoryTo copies the three #554 history settings onto cfg. Extracted
// from toExtractConfig because every shape branch needs the identical block,
// and the nil check for HistoryMinIntervalDays — absent must stay
// distinguishable from an explicit 0 — is easy to get subtly wrong three
// times over.
func (s configFileShape) applyHistoryTo(cfg *ExtractConfig) {
	cfg.MigrateHistory = s.MigrateHistory
	cfg.HistoryMaxPoints = s.HistoryMaxPoints
	if s.HistoryMinIntervalDays != nil {
		cfg.HistoryMinIntervalDays = *s.HistoryMinIntervalDays
	}
}

func (s configFileShape) toExtractConfig() ExtractConfig {
	var cfg ExtractConfig
	// Start the spacing at the "caller said nothing" sentinel so an absent
	// history_min_interval_days defaults to 30 while an explicit 0 survives
	// as 0. Every shape branch below overwrites it only when the key was
	// actually present in the JSON.
	cfg.HistoryMinIntervalDays = HistoryUnset
	switch {
	case s.Source != nil || s.Target != nil:
		// #266 unified shape. Extract pulls from the "source"
		// sub-object; top-level concurrency / timeout / export_directory
		// supply defaults. The "target" sub-object is ignored.
		if s.Source != nil {
			cfg.URL = s.Source.URL
			cfg.Token = s.Source.Token
			cfg.ExtractType = s.Source.ExtractType
			cfg.PEMFilePath = s.Source.PEMFilePath
			cfg.KeyFilePath = s.Source.KeyFilePath
			cfg.CertPassword = s.Source.CertPassword
			cfg.TargetTask = s.Source.TargetTask
			cfg.ExtractID = s.Source.ExtractID
			cfg.Concurrency = s.Source.Concurrency
			cfg.Timeout = s.Source.Timeout
		}
		// Fall back to top-level for concurrency / timeout when the
		// source block didn't override.
		if cfg.Concurrency == 0 {
			cfg.Concurrency = s.Concurrency
		}
		if cfg.Timeout == 0 {
			cfg.Timeout = s.Timeout
		}
		cfg.ExportDirectory = s.ExportDirectory
		// #303: top-level skip_project_data_migration drives whether
		// the extract pulls issue / source / SCM-blame data.
		cfg.SkipProjectDataMigration = s.SkipProjectDataMigration
		cfg.SkipIssueSync = s.SkipIssueSync
		s.applyHistoryTo(&cfg)
		cfg.objectsRaw = s.Objects
		cfg.ProjectKey = s.ProjectKey
	case s.SonarQube != nil:
		cfg.URL = s.SonarQube.URL
		cfg.Token = s.SonarQube.Token
		if s.Settings != nil {
			cfg.ExportDirectory = s.Settings.ExportDirectory
			cfg.Concurrency = s.Settings.Concurrency
			cfg.Timeout = s.Settings.Timeout
		}
		cfg.SkipProjectDataMigration = s.SkipProjectDataMigration
		cfg.SkipIssueSync = s.SkipIssueSync
		s.applyHistoryTo(&cfg)
		cfg.objectsRaw = s.Objects
		cfg.ProjectKey = s.ProjectKey
	case s.Extract != nil:
		cfg = s.Extract.toExtractConfig()
		// #536: "objects" / "project_key" set at the outermost (global)
		// level of a command-sectioned config win over the same fields
		// nested inside "extract" — but fall back to the nested value
		// (already captured above by the recursive call) when the outer
		// level didn't set them.
		if len(s.Objects) > 0 {
			cfg.objectsRaw = s.Objects
		}
		if s.ProjectKey != "" {
			cfg.ProjectKey = s.ProjectKey
		}
	default:
		cfg.URL = s.URL
		cfg.Token = s.Token
		cfg.ExportDirectory = s.ExportDirectory
		cfg.ExtractType = s.ExtractType
		cfg.PEMFilePath = s.PEMFilePath
		cfg.KeyFilePath = s.KeyFilePath
		cfg.CertPassword = s.CertPassword
		cfg.Concurrency = s.Concurrency
		cfg.Timeout = s.Timeout
		cfg.ExtractID = s.ExtractID
		cfg.TargetTask = s.TargetTask
		cfg.SkipProjectDataMigration = s.SkipProjectDataMigration
		cfg.SkipIssueSync = s.SkipIssueSync
		s.applyHistoryTo(&cfg)
		cfg.objectsRaw = s.Objects
		cfg.ProjectKey = s.ProjectKey
	}
	return cfg
}

// LoadExtractConfigFile parses a JSON config file in any of the four
// documented shapes and returns the populated ExtractConfig. The
// config-file "objects" array (any shape) is validated and resolved into
// ExtractConfig.Objects here — the single place --objects values get
// parsed, whether they come from this file or, via cmd/extract.go, the
// --objects CLI flag (#536).
func LoadExtractConfigFile(path string) (ExtractConfig, error) {
	shape, err := parseConfigFile(path)
	if err != nil {
		return ExtractConfig{}, err
	}
	cfg := shape.toExtractConfig()
	objects, err := common.ParseObjects(cfg.objectsRaw)
	if err != nil {
		return ExtractConfig{}, fmt.Errorf("config file %s: %w", path, err)
	}
	cfg.Objects = objects
	cfg.objectsRaw = nil
	return cfg, nil
}
