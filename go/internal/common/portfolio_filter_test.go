// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"errors"
	"testing"
)

func TestPortfolioFilter(t *testing.T) {
	f, err := NewPortfolioFilter("bank", "")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Match("bank", "Whatever") || f.Match("bank-eu", "bank") || f.Match("xbank", "") {
		t.Error("key pattern must be implicitly anchored and ignore the name")
	}

	f, err = NewPortfolioFilter("BANK_.+", "")
	if err != nil || !f.Match("BANK_EU", "") || f.Match("BANK_", "") {
		t.Errorf("regexp key pattern mismatch (err=%v)", err)
	}

	f, err = NewPortfolioFilter("", "Retail.*")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Match("zzz", "Retail EU") || f.Match("Retail", "Corporate") {
		t.Error("name pattern must match on the name only")
	}

	// Alternation must stay anchored as a whole.
	f, _ = NewPortfolioFilter("a|b", "")
	if f.Match("ab", "") || !f.Match("a", "") || !f.Match("b", "") {
		t.Error("alternation must be anchored as a group")
	}

	var nilF *PortfolioFilter
	if nilF.Active() || !nilF.Match("k", "n") {
		t.Error("nil filter must match everything")
	}
	if f0, _ := NewPortfolioFilter("", ""); f0.Active() || !f0.Match("k", "n") {
		t.Error("empty patterns must match everything")
	}
}

func TestPortfolioFilterErrors(t *testing.T) {
	if _, err := NewPortfolioFilter("a", "b"); !errors.Is(err, ErrPortfolioRegexpConflict) {
		t.Errorf("both patterns: want ErrPortfolioRegexpConflict, got %v", err)
	}
	if _, err := NewPortfolioFilter("(", ""); err == nil {
		t.Error("invalid key pattern must error")
	}
	if _, err := NewPortfolioFilter("", "["); err == nil {
		t.Error("invalid name pattern must error")
	}
}
