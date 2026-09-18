// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"encoding/json"
	"regexp"
	"testing"
)

func TestKeepBranchByRegexp_NilRegexKeepsEverything(t *testing.T) {
	e := &Executor{}
	item := json.RawMessage(`{"name":"feature/whatever","isMain":false}`)
	if !keepBranchByRegexp(e, item) {
		t.Error("expected true when no BranchRe is configured")
	}
}

func TestKeepBranchByRegexp_NonMatchingNonMainDropped(t *testing.T) {
	e := &Executor{BranchRe: regexp.MustCompile("^main$")}
	item := json.RawMessage(`{"name":"feature/foo","isMain":false}`)
	if keepBranchByRegexp(e, item) {
		t.Error("expected false for a non-matching, non-main branch")
	}
}

func TestKeepBranchByRegexp_NonMatchingMainKept(t *testing.T) {
	e := &Executor{BranchRe: regexp.MustCompile("^release/.+$")}
	item := json.RawMessage(`{"name":"main","isMain":true}`)
	if !keepBranchByRegexp(e, item) {
		t.Error("expected true: the main branch always survives the filter")
	}
}

func TestKeepBranchByRegexp_MatchingBranchKept(t *testing.T) {
	e := &Executor{BranchRe: regexp.MustCompile("^release/.+$")}
	item := json.RawMessage(`{"name":"release/1.0","isMain":false}`)
	if !keepBranchByRegexp(e, item) {
		t.Error("expected true for a branch matching BranchRe")
	}
}
