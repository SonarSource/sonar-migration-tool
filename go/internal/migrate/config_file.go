package migrate

import (
	"encoding/json"
	"fmt"
	"os"
)

// configFileShape is the union of the three documented config-file formats
// (see examples/config-migrate.example.json, examples/config.example.json,
// and examples/migration-config.example.json). Issue #176.
//
// Detection at parse time:
//   - sonarcloud present -> shape 3 (side-sectioned)
//   - migrate present    -> shape 2 (command-sectioned)
//   - else               -> shape 1 (flat)
type configFileShape struct {
	// Shape 1 (flat) fields. Also reused inside Shape 2's "migrate" object.
	Token              string `json:"token"`
	EnterpriseKey      string `json:"enterprise_key"`
	URL                string `json:"url"`
	Edition            string `json:"edition"`
	ExportDirectory    string `json:"export_directory"`
	Concurrency        int    `json:"concurrency"`
	RunID              string `json:"run_id"`
	TargetTask         string `json:"target_task"`
	SkipProfiles       bool   `json:"skip_profiles"`
	IncludeScanHistory bool   `json:"include_scan_history"`
	Debug              bool   `json:"debug"`

	// Shape 2 (command-sectioned).
	Migrate *configFileShape `json:"migrate"`

	// Shape 3 (side-sectioned). sonarqube + extract blocks are ignored —
	// migrate/reset only consume SonarCloud-side values.
	SonarCloud *sonarCloudBlock `json:"sonarcloud"`
	Settings   *settingsBlock   `json:"settings"`
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
	case s.SonarCloud != nil:
		return s.SonarCloud.toMigrateConfig(s.Settings)
	case s.Migrate != nil:
		return s.Migrate.toMigrateConfig()
	default:
		return MigrateConfig{
			Token:              s.Token,
			EnterpriseKey:      s.EnterpriseKey,
			URL:                s.URL,
			Edition:            s.Edition,
			ExportDirectory:    s.ExportDirectory,
			Concurrency:        s.Concurrency,
			RunID:              s.RunID,
			TargetTask:         s.TargetTask,
			SkipProfiles:       s.SkipProfiles,
			IncludeScanHistory: s.IncludeScanHistory,
			Debug:              s.Debug,
		}
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
