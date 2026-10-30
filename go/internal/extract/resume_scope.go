// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// recordedScope is the scope a run wrote to extract.json (see
// writeMetadataFile).
type recordedScope struct {
	Partial             bool     `json:"partial"`
	ProjectKeys         []string `json:"project_keys"`
	Objects             []string `json:"objects"`
	PortfolioKeyRegexp  string   `json:"portfolio_key_regexp"`
	PortfolioNameRegexp string   `json:"portfolio_name_regexp"`
}

// RestoreResumeScope keeps a resumed slice a slice (#666). extract.json is
// rewritten on every run, so resuming a partial extract with --extract_id
// alone would otherwise record it as full, and structure would then let it
// hide every earlier slice. When the resumed extract was recorded as partial:
//   - a run with no scope flags of its own takes the recorded scope, so its
//     unfinished tasks extract the same slice;
//   - a run with scope flags keeps them, with a warning, and stays partial.
//
// A new extract, a full one, or one with no readable extract.json is left as
// it is. Callers that need the effective scope after RunExtract (for example,
// to decide whether to run structure) can restore it before invoking RunExtract;
// the operation is idempotent.
func RestoreResumeScope(cfg *ExtractConfig) {
	restoreResumeScope(cfg, slog.Default())
}

func restoreResumeScope(cfg *ExtractConfig, logger *slog.Logger) {
	if cfg.resumedPartial || cfg.ExtractID == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(cfg.ExportDirectory, cfg.ExtractID, "extract.json"))
	if err != nil {
		return
	}
	var rec recordedScope
	if json.Unmarshal(data, &rec) != nil || !rec.Partial {
		return
	}
	cfg.resumedPartial = true
	if hasScope(*cfg) {
		if cfg.ProjectKey != "" {
			cfg.ProjectKeys = matchingProjectKeys(rec.ProjectKeys, cfg.ProjectKey)
		} else if len(cfg.ProjectKeys) == 0 {
			// Non-project scope changes (for example, --objects) still use
			// the projects recorded by the original partial extract.
			cfg.ProjectKeys = rec.ProjectKeys
		}
		logger.Warn("resuming a partial extract with scope flags of its own: they replace the original scope, and the extract stays partial",
			"extract_id", cfg.ExtractID)
		return
	}
	cfg.ProjectKeys = rec.ProjectKeys
	cfg.PortfolioKeyRegexp = rec.PortfolioKeyRegexp
	cfg.PortfolioNameRegexp = rec.PortfolioNameRegexp
	if len(rec.Objects) > 0 {
		if objects, err := common.ParseObjects(rec.Objects); err == nil {
			cfg.Objects = objects
		}
	}
	logger.Info("resuming a partial extract with the scope it was started with",
		"extract_id", cfg.ExtractID, "projects", len(rec.ProjectKeys), "objects", rec.Objects,
		"portfolio_key_regexp", rec.PortfolioKeyRegexp, "portfolio_name_regexp", rec.PortfolioNameRegexp)
}

func matchingProjectKeys(keys []string, pattern string) []string {
	projectPattern, err := CompileProjectKeyPattern(pattern)
	if err != nil {
		return append([]string(nil), keys...)
	}
	matched := make([]string, 0, len(keys))
	for _, key := range keys {
		if projectPattern.MatchString(key) {
			matched = append(matched, key)
		}
	}
	return matched
}

// sortedObjects returns the selected --objects categories, sorted, or nil.
func sortedObjects(objects map[string]bool) []string {
	if len(objects) == 0 {
		return nil
	}
	out := make([]string, 0, len(objects))
	for name, on := range objects {
		if on {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
