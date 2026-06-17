// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	sqcTypes "github.com/sonar-solutions/sq-api-go/types"
)

// applyOrgMapping checks organizations.csv has at least one row with a
// non-empty sonarcloud_org_key. A migrate run with all rows unmapped
// used to exit 0 with nothing migrated, which silently looked like a
// successful no-op (issue #279).
//
// When defaultOrg is non-empty, behaviour follows issue #281:
//
//   - Mapping defined + defaultOrg set: defaultOrg is ignored, a WARN is
//     logged, the CSV is left untouched.
//   - No mapping + defaultOrg set: organizations.csv is rewritten with
//     defaultOrg in every sonarcloud_org_key cell, no error.
//   - No mapping + defaultOrg empty: returns the #279 exit-code-2 error.
//
// SKIPPED rows count as "mapped" — the user has deliberately chosen to
// exclude them, distinct from forgetting to fill in the file at all.
//
// Returns appliedDefault=true when the CSV was rewritten with the
// default; this drives the message variant produced by
// validateOrgsExist for issue #283.
func applyOrgMapping(exportDir, defaultOrg string, logger *slog.Logger) (appliedDefault bool, err error) {
	rows, loadErr := structure.LoadCSV(exportDir, "organizations.csv")
	if loadErr != nil {
		return false, fmt.Errorf("loading organizations.csv: %w", loadErr)
	}
	csvPath := filepath.Join(exportDir, "organizations.csv")

	if len(rows) == 0 {
		// No file at all → cannot synthesize even with defaultOrg
		// because the columns/rows aren't there yet.
		return false, missingMappingError(csvPath)
	}

	hasMapping := false
	for _, row := range rows {
		val, _ := row["sonarcloud_org_key"].(string)
		if strings.TrimSpace(val) != "" {
			hasMapping = true
			break
		}
	}

	if hasMapping {
		if defaultOrg != "" {
			logger.Warn("Since organizations.csv mapping is defined, the provided default organization parameter is ignored",
				"default_organization", defaultOrg, "file", csvPath)
		}
		return false, nil
	}

	// No mapping defined.
	if defaultOrg == "" {
		return false, missingMappingError(csvPath)
	}

	// Apply defaultOrg to every row and write back.
	if err := writeOrgCSVWithDefault(csvPath, rows, defaultOrg); err != nil {
		return false, fmt.Errorf("applying default_organization to organizations.csv: %w", err)
	}
	logger.Info("organizations.csv was empty — every project will migrate to the provided default organization",
		"default_organization", defaultOrg, "rows", len(rows))
	return true, nil
}

func missingMappingError(csvPath string) error {
	return common.NewExitError(2, fmt.Errorf(
		`No organization mapping has been defined, please review the %q file`, csvPath))
}

// writeOrgCSVWithDefault rewrites the file so every row's
// sonarcloud_org_key cell carries defaultOrg, preserving all other
// columns and their order. The file is read once to recover the
// canonical header order — map iteration would otherwise scramble it.
func writeOrgCSVWithDefault(path string, rows []map[string]any, defaultOrg string) error {
	headers, err := readOrgCSVHeaders(path)
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if err := w.Write(headers); err != nil {
		_ = f.Close()
		return err
	}
	for _, row := range rows {
		out := make([]string, len(headers))
		for i, h := range headers {
			if h == "sonarcloud_org_key" {
				out[i] = defaultOrg
				continue
			}
			out[i] = fmt.Sprintf("%v", row[h])
			if row[h] == nil {
				out[i] = ""
			}
		}
		if err := w.Write(out); err != nil {
			_ = f.Close()
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// orgLookup is the contract validateOrgsExist needs from the SQC SDK.
// Defined as an interface so unit tests can stub it without spinning
// up a full client. Production code passes cc.Organizations.
type orgLookup interface {
	Search(ctx context.Context, keys ...string) ([]sqcTypes.Organization, error)
}

// validateOrgsExist checks that every SonarQube Cloud organization the
// migration is about to use actually exists / is visible to the
// authenticated token. The two failure modes from issue #283:
//
//   - appliedDefault=true: the only org in play is defaultOrg; missing
//     org → "Default organization …" message.
//   - appliedDefault=false: every distinct non-empty/non-SKIPPED
//     sonarcloud_org_key in organizations.csv is checked. Missing
//     org → "Organization X specified in <csv>" message.
//
// Returns an *common.ExitCodeError with code 3 on failure.
func validateOrgsExist(ctx context.Context, lookup orgLookup, exportDir, enterpriseKey, defaultOrg string, appliedDefault bool) error {
	csvPath := filepath.Join(exportDir, "organizations.csv")

	if appliedDefault {
		if defaultOrg == "" {
			return nil
		}
		known, err := orgsByKey(ctx, lookup, []string{defaultOrg})
		if err != nil {
			return fmt.Errorf("looking up default organization in SonarQube Cloud: %w", err)
		}
		if _, ok := known[defaultOrg]; !ok {
			return defaultOrgMissingError(defaultOrg, enterpriseKey)
		}
		return nil
	}

	rows, err := structure.LoadCSV(exportDir, "organizations.csv")
	if err != nil {
		return fmt.Errorf("loading organizations.csv: %w", err)
	}
	seen := make(map[string]bool)
	ordered := make([]string, 0)
	for _, row := range rows {
		k, _ := row["sonarcloud_org_key"].(string)
		k = strings.TrimSpace(k)
		if k == "" || k == "SKIPPED" || seen[k] {
			continue
		}
		seen[k] = true
		ordered = append(ordered, k)
	}
	if len(ordered) == 0 {
		return nil
	}
	known, err := orgsByKey(ctx, lookup, ordered)
	if err != nil {
		return fmt.Errorf("looking up organizations in SonarQube Cloud: %w", err)
	}
	for _, k := range ordered {
		if _, ok := known[k]; !ok {
			return csvOrgMissingError(k, csvPath, enterpriseKey)
		}
	}
	return nil
}

// orgsByKey returns a set of organization keys that the SQC search
// endpoint confirmed exist and are visible to the caller. Chunks the
// request at 50 keys per call to stay under SonarCloud's URL-length
// limit on /api/organizations/search?organizations=...
func orgsByKey(ctx context.Context, lookup orgLookup, keys []string) (map[string]bool, error) {
	out := make(map[string]bool, len(keys))
	const chunkSize = 50
	for i := 0; i < len(keys); i += chunkSize {
		end := i + chunkSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]
		orgs, err := lookup.Search(ctx, batch...)
		if err != nil {
			return nil, err
		}
		for _, o := range orgs {
			if o.Key != "" {
				out[o.Key] = true
			}
		}
	}
	return out, nil
}

// validatePatternOrgCollision implements the issue #138 guard: when the
// project-key pattern does NOT use <ORGANIZATION_KEY>, the static prefix is the same
// for every migrated project. If that prefix (minus a trailing underscore)
// matches an existing SonarQube Cloud organization key, the resulting keys
// would look org-scoped while not being mapped through one — ambiguous
// enough that we abort and ask the operator to disambiguate the pattern.
//
// Patterns that include <ORGANIZATION_KEY> are inherently org-scoped and skip this
// check. So does a bare <ORIGINAL_PROJECT_KEY> (keep-unchanged), which has
// no prefix to collide.
func validatePatternOrgCollision(ctx context.Context, lookup orgLookup, pattern string) error {
	if PatternUsesOrg(pattern) {
		return nil
	}
	prefix, _ := ProjectKeyAffixes(pattern, "")
	candidate := strings.TrimRight(prefix, "_")
	if candidate == "" {
		return nil
	}
	known, err := orgsByKey(ctx, lookup, []string{candidate})
	if err != nil {
		return fmt.Errorf("checking project_key_pattern prefix against SonarQube Cloud organizations: %w", err)
	}
	if known[candidate] {
		return common.NewExitError(2, fmt.Errorf(
			`project_key_pattern prefix %q collides with existing SonarQube Cloud organization %q; use <ORGANIZATION_KEY> in the pattern or choose a prefix that is not an organization key`,
			prefix, candidate))
	}
	return nil
}

func defaultOrgMissingError(defaultOrg, enterpriseKey string) error {
	return common.NewExitError(3, fmt.Errorf(
		`Default organization %q does not exists in SonarQube Cloud, or is not part of Enterprise %q`,
		defaultOrg, enterpriseKey))
}

func csvOrgMissingError(orgKey, csvPath, enterpriseKey string) error {
	return common.NewExitError(3, fmt.Errorf(
		`Organization %q specified in %q does not exists in SonarQube Cloud, or does not belong to Enterprise %q`,
		orgKey, csvPath, enterpriseKey))
}

func readOrgCSVHeaders(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	headers, err := r.Read()
	if err != nil {
		return nil, err
	}
	return headers, nil
}
