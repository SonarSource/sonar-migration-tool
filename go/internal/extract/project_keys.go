// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"fmt"
	"regexp"
	"sort"
)

// CompileProjectKeyPattern compiles pattern as a full-match regex,
// implicitly anchored with ^ and $ (mirrors cmd/transfer.go's
// anchoredProjectKeyPattern, #529) — "BANKING_.+" matches only keys
// starting with "BANKING_", never a key with that substring somewhere
// in the middle. A plain literal key with no regex metacharacters
// matches only itself.
func CompileProjectKeyPattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile("^(?:" + pattern + ")$")
}

// ResolveProjectKeys lists every project visible to cfg's credentials
// via ListAllProjectKeys and returns the sorted subset whose key fully
// matches pattern (#515, mirrors cmd/transfer.go's
// resolveTransferProjectKeys). Returns an error — not an empty slice —
// when nothing matches, so callers fail fast instead of silently
// extracting zero projects.
func ResolveProjectKeys(ctx context.Context, cfg ExtractConfig, pattern string) ([]string, error) {
	re, err := CompileProjectKeyPattern(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid project key pattern %q: %w", pattern, err)
	}
	allKeys, err := ListAllProjectKeys(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("listing projects on %s: %w", cfg.URL, err)
	}
	var matched []string
	for _, k := range allKeys {
		if re.MatchString(k) {
			matched = append(matched, k)
		}
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf(
			"no project on %s matches pattern %q (keys are case-sensitive; verify with GET /api/projects/search)",
			cfg.URL, pattern)
	}
	sort.Strings(matched)
	return matched, nil
}
