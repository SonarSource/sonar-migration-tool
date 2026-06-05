// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"encoding/json"
	"fmt"
	"os"
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
	URL                string `json:"url"`
	Token              string `json:"token"`
	ExportDirectory    string `json:"export_directory"`
	ExtractType        string `json:"extract_type"`
	PEMFilePath        string `json:"pem_file_path"`
	KeyFilePath        string `json:"key_file_path"`
	CertPassword       string `json:"cert_password"`
	Concurrency        int    `json:"concurrency"`
	Timeout            int    `json:"timeout"`
	ExtractID                string `json:"extract_id"`
	TargetTask               string `json:"target_task"`
	SkipProjectDataMigration bool   `json:"skip_project_data_migration"`

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

func (s configFileShape) toExtractConfig() ExtractConfig {
	var cfg ExtractConfig
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
	case s.SonarQube != nil:
		cfg.URL = s.SonarQube.URL
		cfg.Token = s.SonarQube.Token
		if s.Settings != nil {
			cfg.ExportDirectory = s.Settings.ExportDirectory
			cfg.Concurrency = s.Settings.Concurrency
			cfg.Timeout = s.Settings.Timeout
		}
		cfg.SkipProjectDataMigration = s.SkipProjectDataMigration
	case s.Extract != nil:
		return s.Extract.toExtractConfig()
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
	}
	return cfg
}

// LoadExtractConfigFile parses a JSON config file in any of the three
// documented shapes and returns the populated ExtractConfig.
func LoadExtractConfigFile(path string) (ExtractConfig, error) {
	shape, err := parseConfigFile(path)
	if err != nil {
		return ExtractConfig{}, err
	}
	return shape.toExtractConfig(), nil
}
