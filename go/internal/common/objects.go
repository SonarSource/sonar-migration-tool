// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"fmt"
	"strings"
)

// Canonical object-category names accepted by --objects (issue #536).
// extract and migrate each maintain their own category -> task-name
// mapping (internal/extract/objects_categories.go,
// internal/migrate/objects_categories.go); these constants are the
// shared vocabulary both sides validate CLI/config input against.
const (
	ObjectSettings            = "settings"
	ObjectPermissionTemplates = "permission_templates"
	ObjectQualityProfiles     = "quality_profiles"
	ObjectQualityGates        = "quality_gates"
	ObjectProjects            = "projects"
	ObjectPortfolios          = "portfolios"
	ObjectGroups              = "groups"
	// ObjectLicenseProfiles is accepted as a valid --objects value but
	// not yet implemented: ParseObjects returns it in the result set,
	// and the caller is expected to log a one-time "not yet supported"
	// warning and otherwise proceed as if it hadn't been selected — no
	// license_profiles task-name mapping exists on either side.
	ObjectLicenseProfiles = "license_profiles"
)

// AllObjects lists every canonical --objects value, in the order the
// issue documents them.
var AllObjects = []string{
	ObjectSettings,
	ObjectPermissionTemplates,
	ObjectQualityProfiles,
	ObjectQualityGates,
	ObjectProjects,
	ObjectPortfolios,
	ObjectGroups,
	ObjectLicenseProfiles,
}

// objectAliases maps the issue's short aliases to their canonical name.
var objectAliases = map[string]string{
	"qp": ObjectQualityProfiles,
	"qg": ObjectQualityGates,
	"pt": ObjectPermissionTemplates,
	"lp": ObjectLicenseProfiles,
}

// SplitObjectsCSV splits a raw --objects flag value on commas and trims
// surrounding whitespace from each token, so
// `--objects " quality_profiles, quality_gates , groups "` resolves the
// same as `--objects "quality_profiles,quality_gates,groups"` (#536).
// Returns nil for an empty/whitespace-only input.
func SplitObjectsCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseObjects validates and normalizes a list of object names/aliases —
// from the CLI via SplitObjectsCSV, or directly from a config file's JSON
// array. Aliases (qp/qg/pt/lp) are resolved to their canonical name and
// the result is deduplicated. Empty input means "everything": ParseObjects
// returns (nil, nil), and callers must treat a nil result as "no filter"
// rather than "filter matched nothing" (#536).
//
// An unrecognized name aborts with an error naming the invalid value, so
// the caller can fail the command with a non-zero exit before any API
// call is made.
func ParseObjects(values []string) (map[string]bool, error) {
	if len(values) == 0 {
		return nil, nil
	}
	valid := make(map[string]bool, len(AllObjects))
	for _, v := range AllObjects {
		valid[v] = true
	}
	out := make(map[string]bool, len(values))
	for _, v := range values {
		name := strings.ToLower(strings.TrimSpace(v))
		if canon, ok := objectAliases[name]; ok {
			name = canon
		}
		if !valid[name] {
			return nil, fmt.Errorf("invalid --objects value %q: expected one of %s (aliases: qp, qg, pt, lp)",
				v, strings.Join(AllObjects, ", "))
		}
		out[name] = true
	}
	return out, nil
}

// ExcludedTasks returns every task name belonging to a category NOT
// present in selected (nil selected == everything selected == no
// exclusions), given a category -> task-names table. Shared by
// extract's excludedExtractTasks and migrate's excludedMigrateTasks,
// whose bodies were otherwise identical aside from which package-local
// table they iterated (#536).
func ExcludedTasks(categoryTasks map[string][]string, selected map[string]bool) map[string]bool {
	if selected == nil {
		return nil
	}
	excluded := make(map[string]bool)
	for category, tasks := range categoryTasks {
		if selected[category] {
			continue
		}
		for _, t := range tasks {
			excluded[t] = true
		}
	}
	return excluded
}
