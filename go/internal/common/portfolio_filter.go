// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"errors"
	"fmt"
	"regexp"
)

// PortfolioFilter selects which portfolios extract and migrate process, so a
// large instance can be migrated in slices (#666). It matches on the portfolio
// key or on its name, never both: the two patterns are mutually exclusive.
// Patterns are implicitly anchored (^...$), like --project_key_regexp, so a
// plain key matches only itself. The zero value (and a nil pointer) matches
// every portfolio.
type PortfolioFilter struct {
	keyRe  *regexp.Regexp
	nameRe *regexp.Regexp
}

// ErrPortfolioRegexpConflict is returned when both a key and a name pattern are given.
var ErrPortfolioRegexpConflict = errors.New("--portfolio_key_regexp and --portfolio_name_regexp are mutually exclusive: use only one")

// NewPortfolioFilter compiles the key and name patterns. An empty pattern is
// unset. Setting both is an error (ErrPortfolioRegexpConflict); an invalid
// pattern is an error naming the offending option.
func NewPortfolioFilter(keyPattern, namePattern string) (*PortfolioFilter, error) {
	if keyPattern != "" && namePattern != "" {
		return nil, ErrPortfolioRegexpConflict
	}
	f := &PortfolioFilter{}
	var err error
	if keyPattern != "" {
		if f.keyRe, err = regexp.Compile("^(?:" + keyPattern + ")$"); err != nil {
			return nil, fmt.Errorf("invalid portfolio key regexp pattern %q: %w", keyPattern, err)
		}
	}
	if namePattern != "" {
		if f.nameRe, err = regexp.Compile("^(?:" + namePattern + ")$"); err != nil {
			return nil, fmt.Errorf("invalid portfolio name regexp pattern %q: %w", namePattern, err)
		}
	}
	return f, nil
}

// Active reports whether the filter restricts anything.
func (f *PortfolioFilter) Active() bool {
	return f != nil && (f.keyRe != nil || f.nameRe != nil)
}

// Match reports whether the portfolio with this key and name is selected.
func (f *PortfolioFilter) Match(key, name string) bool {
	if !f.Active() {
		return true
	}
	if f.keyRe != nil {
		return f.keyRe.MatchString(key)
	}
	return f.nameRe.MatchString(name)
}
