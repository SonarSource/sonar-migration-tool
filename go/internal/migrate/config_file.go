// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"fmt"
	"os"
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
	URL                 string   `json:"url"`
	Token               string   `json:"token"`
	EnterpriseKey       string   `json:"enterprise_key"`
	Edition             string   `json:"edition"`
	Concurrency         int      `json:"concurrency"`
	Timeout             int      `json:"timeout"`
	RunID               string   `json:"run_id"`
	TargetTask          string   `json:"target_task"`
	OrganizationKey     string   `json:"organization_key"`     // provisional, ignored
	DefaultOrganization string   `json:"default_organization"` // #281
	ExcludeBranches     []string `json:"exclude_branches"`
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
	ExportDirectory string `json:"export_directory"`
	Concurrency     int    `json:"concurrency"`
	Timeout         int    `json:"timeout"`
}

func parseConfigFile(path string) (configFileShape, error) {
	var shape configFileShape
	data, err := os.ReadFile(path)
	if err != nil {
		return shape, fmt.Errorf("reading config file: %w", err)
	}
	if len(data) == 0 {
		return shape, fmt.Errorf("config file %s is empty", path)
	}
	if err := json.Unmarshal(data, &shape); err != nil {
		return shape, fmt.Errorf("parsing config file: %w", err)
	}
	return shape, nil
}

func (s configFileShape) toMigrateConfig() MigrateConfig {
	switch {
	case s.Source != nil || s.Target != nil:
		// #266 unified shape. Migrate pulls from the "target"
		// sub-object; top-level concurrency / timeout /
		// export_directory supply defaults. The "source" block is
		// ignored. organization_key is provisional and ignored.
		var cfg MigrateConfig
		if s.Target != nil {
			cfg.URL = s.Target.URL
			cfg.Token = s.Target.Token
			cfg.EnterpriseKey = s.Target.EnterpriseKey
			cfg.Edition = s.Target.Edition
			cfg.RunID = s.Target.RunID
			cfg.TargetTask = s.Target.TargetTask
			cfg.Concurrency = s.Target.Concurrency
			cfg.Timeout = s.Target.Timeout
			cfg.DefaultOrganization = s.Target.DefaultOrganization
			cfg.ExcludeBranches = s.Target.ExcludeBranches
		}
		if cfg.Concurrency == 0 {
			cfg.Concurrency = s.Concurrency
		}
		if cfg.Timeout == 0 {
			cfg.Timeout = s.Timeout
		}
		cfg.ExportDirectory = s.ExportDirectory
		// Top-level skip_issue_sync applies to every shape (#299).
		// The field name matches the MigrateConfig field one-for-one
		// so there's no inversion.
		if s.SkipIssueSync != nil && s.SkipIssueSync.Set {
			cfg.SkipIssueSync = s.SkipIssueSync.Value
		}
		if s.SkipProjectDataMigration != nil && s.SkipProjectDataMigration.Set {
			cfg.SkipProjectDataMigration = s.SkipProjectDataMigration.Value
		}
		return cfg
	case s.SonarCloud != nil:
		cfg := s.SonarCloud.toMigrateConfig(s.Settings)
		if s.SkipIssueSync != nil && s.SkipIssueSync.Set {
			cfg.SkipIssueSync = s.SkipIssueSync.Value
		}
		if s.SkipProjectDataMigration != nil && s.SkipProjectDataMigration.Set {
			cfg.SkipProjectDataMigration = s.SkipProjectDataMigration.Value
		}
		return cfg
	case s.Migrate != nil:
		cfg := s.Migrate.toMigrateConfig()
		// Outer-level skip_issue_sync wins when both outer and inner
		// set it (#299). If only outer is set, propagate it down.
		if s.SkipIssueSync != nil && s.SkipIssueSync.Set {
			cfg.SkipIssueSync = s.SkipIssueSync.Value
		}
		if s.SkipProjectDataMigration != nil && s.SkipProjectDataMigration.Set {
			cfg.SkipProjectDataMigration = s.SkipProjectDataMigration.Value
		}
		return cfg
	default:
		cfg := MigrateConfig{
			Token:              s.Token,
			EnterpriseKey:      s.EnterpriseKey,
			URL:                s.URL,
			Edition:            s.Edition,
			ExportDirectory:    s.ExportDirectory,
			Concurrency:        s.Concurrency,
			Timeout:            s.Timeout,
			RunID:              s.RunID,
			TargetTask:         s.TargetTask,
			SkipProfiles:       s.SkipProfiles,
			IncludeProjectData: s.IncludeProjectData,
			Debug:              s.Debug,
			ExcludeBranches:    s.ExcludeBranches,
		}
		if s.SkipIssueSync != nil && s.SkipIssueSync.Set {
			cfg.SkipIssueSync = s.SkipIssueSync.Value
		}
		if s.SkipProjectDataMigration != nil && s.SkipProjectDataMigration.Set {
			cfg.SkipProjectDataMigration = s.SkipProjectDataMigration.Value
		}
		return cfg
	}
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
		cfg.Timeout = settings.Timeout
	}
	return cfg
}

func (s configFileShape) toResetConfig() ResetConfig {
	m := s.toMigrateConfig()
	return ResetConfig{
		Token:           m.Token,
		EnterpriseKey:   m.EnterpriseKey,
		Edition:         m.Edition,
		URL:             m.URL,
		Concurrency:     m.Concurrency,
		ExportDirectory: m.ExportDirectory,
		Debug:           m.Debug,
	}
}

// LoadSonarCloudOrgsFromConfigFile returns the organizations list from a
// side-sectioned config file. Returns nil when the file uses a different shape
// or no organizations are defined.
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

// LoadMigrateConfigFile parses a JSON config file in any of the three
// documented shapes and returns the populated MigrateConfig.
func LoadMigrateConfigFile(path string) (MigrateConfig, error) {
	shape, err := parseConfigFile(path)
	if err != nil {
		return MigrateConfig{}, err
	}
	return shape.toMigrateConfig(), nil
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
