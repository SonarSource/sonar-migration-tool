// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"fmt"
	"regexp"
	"strings"
)

// Project-key renaming strategy (issue #138).
//
// When a project is migrated from SonarQube Server to SonarQube Cloud its
// key is rewritten according to a configurable pattern built from two
// placeholders:
//
//	<ORIGINAL_PROJECT_KEY>  the source project key (mandatory)
//	<ORGANIZATION_KEY>      the target SonarQube Cloud organization key
//
// `<PROJECT_KEY>` is accepted as an alias of `<ORIGINAL_PROJECT_KEY>`.
// Placeholder names are recognised case-insensitively but canonicalised to
// the uppercase forms above.
//
// The default pattern reproduces SonarQube Cloud's own convention and the
// tool's historical behaviour (orgKey + "_" + sourceKey).
const (
	DefaultProjectKeyPattern = "<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>"

	// MaxProjectKeyLength is SonarQube's hard limit on a project key. Keys
	// rendered longer than this are surfaced in the migration report.
	MaxProjectKeyLength = 400

	// minSinglePlaceholderPrefix is the minimum length of the static text
	// that must accompany a lone <ORIGINAL_PROJECT_KEY> placeholder. A
	// short, generic prefix is too collision-prone to allow.
	minSinglePlaceholderPrefix = 5

	phOriginal = "<ORIGINAL_PROJECT_KEY>"
	phOrg      = "<ORGANIZATION_KEY>"
)

// placeholderRe matches any <...> token so unknown placeholders can be rejected.
var placeholderRe = regexp.MustCompile(`<[^>]*>`)

// canonicalPlaceholders maps the uppercased inner name of a recognised
// placeholder (including the <PROJECT_KEY> alias) to its canonical token.
var canonicalPlaceholders = map[string]string{
	"ORIGINAL_PROJECT_KEY": phOriginal,
	"PROJECT_KEY":          phOriginal, // alias
	"ORGANIZATION_KEY":     phOrg,
}

// normalizeProjectKeyPattern canonicalises every recognised placeholder to
// its uppercase form (so <organization_key>, <ORGANIZATION_KEY> and the
// <PROJECT_KEY> alias all collapse onto the same token). Unrecognised
// <...> tokens are left untouched so ValidateProjectKeyPattern can reject them.
func normalizeProjectKeyPattern(pattern string) string {
	return placeholderRe.ReplaceAllStringFunc(pattern, func(tok string) string {
		inner := strings.ToUpper(tok[1 : len(tok)-1])
		if canon, ok := canonicalPlaceholders[inner]; ok {
			return canon
		}
		return tok
	})
}

// resolveProjectKeyPattern normalizes the pattern and falls back to the
// default when it is empty. RunMigrate defaults the pattern before use, but
// the runtime helpers below default defensively too so a caller that forgot
// to set it still gets SonarQube Cloud's standard behaviour rather than an
// empty key. Validation deliberately does NOT default — an empty pattern in
// a config file is an error worth surfacing.
func resolveProjectKeyPattern(pattern string) string {
	if strings.TrimSpace(pattern) == "" {
		return DefaultProjectKeyPattern
	}
	return normalizeProjectKeyPattern(pattern)
}

// ValidateProjectKeyPattern enforces the rules from issue #138:
//   - only the two documented placeholders are allowed;
//   - <ORIGINAL_PROJECT_KEY> must appear (which also rules out a pattern
//     with zero placeholders);
//   - when the pattern does not use <ORGANIZATION_KEY>, the only placeholder
//     is <ORIGINAL_PROJECT_KEY>: a bare pattern (no static text) is allowed
//     (the "keep the key unchanged" mode), but any static prefix/suffix must
//     total at least minSinglePlaceholderPrefix characters so a short generic
//     prefix can't produce collision-prone keys.
func ValidateProjectKeyPattern(pattern string) error {
	p := normalizeProjectKeyPattern(pattern)
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("project_key_pattern must not be empty")
	}
	for _, tok := range placeholderRe.FindAllString(p, -1) {
		switch tok {
		case phOriginal, phOrg:
		default:
			return fmt.Errorf("project_key_pattern contains unknown placeholder %q; allowed placeholders are %s and %s",
				tok, phOriginal, phOrg)
		}
	}
	if !strings.Contains(p, phOriginal) {
		return fmt.Errorf("project_key_pattern %q must include the %s placeholder", pattern, phOriginal)
	}
	if !strings.Contains(p, phOrg) {
		// Lone <ORIGINAL_PROJECT_KEY>. The literal text is everything that
		// is not the placeholder itself.
		literal := strings.ReplaceAll(p, phOriginal, "")
		if n := len(literal); n > 0 && n < minSinglePlaceholderPrefix {
			return fmt.Errorf(
				"project_key_pattern %q: a single %s placeholder combined with a static prefix and/or postfix requires at least %d characters of literal text (got %d) — e.g. \"acme_%s\"",
				pattern, phOriginal, minSinglePlaceholderPrefix, n, phOriginal)
		}
	}
	return nil
}

// RenderProjectKey substitutes the placeholders in pattern to produce the
// target SonarQube Cloud project key.
func RenderProjectKey(pattern, originalKey, orgKey string) string {
	return strings.NewReplacer(
		phOrg, orgKey,
		phOriginal, originalKey,
	).Replace(resolveProjectKeyPattern(pattern))
}

// ProjectKeyAffixes returns the literal text that surrounds the
// <ORIGINAL_PROJECT_KEY> placeholder once <ORGANIZATION_KEY> is substituted.
// Permission-template and portfolio regexes — which match source project
// keys — are wrapped with these affixes so they keep matching after renaming.
func ProjectKeyAffixes(pattern, orgKey string) (prefix, suffix string) {
	p := resolveProjectKeyPattern(pattern)
	idx := strings.Index(p, phOriginal)
	if idx < 0 {
		return "", ""
	}
	rep := strings.NewReplacer(phOrg, orgKey)
	return rep.Replace(p[:idx]), rep.Replace(p[idx+len(phOriginal):])
}

// PatternUsesOrg reports whether the pattern includes <ORGANIZATION_KEY>. When
// it does not, the static prefix is the same for every project and could
// collide with an existing organization key — the migrate command checks
// for that before running.
func PatternUsesOrg(pattern string) bool {
	return strings.Contains(resolveProjectKeyPattern(pattern), phOrg)
}
