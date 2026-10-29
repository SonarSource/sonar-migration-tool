// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"regexp"
	"strings"

	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
	"github.com/sonar-solutions/sq-api-go/types"
)

// #655 — SonarQube Cloud has no "reference branch" new code definition, but a
// short-lived branch (SLB) is compared against its target branch, which behaves
// much like one. With use_slb_for_reference_branch_new_code, every SonarQube
// Server branch whose new code is a reference branch is migrated as an SLB.
//
// The migration registers an SLB explicitly as "short" in the create-analysis
// handshake and submits it with branchType=SHORT (see preCreateBranchAnalysis).
// As a secondary safeguard, in case the CE still consults the project's
// long-lived branch pattern, the pattern is temporarily adjusted so that it
// does not match the SLBs for the duration of the branch imports.

// slbReferenceNCDType is the new-code-definition type that marks a branch.
const slbReferenceNCDType = "REFERENCE_BRANCH"

// defaultSQCLongLivedBranchRegex is the pattern SonarQube Cloud applies to a
// project that has no explicit long-lived branch pattern of its own.
const defaultSQCLongLivedBranchRegex = "(branch|release)-.*"

// neverMatchBranchRegex is a valid Java regex that no branch name matches.
const neverMatchBranchRegex = "(?!)"

// resolveUseSLBForReferenceBranchNewCode mirrors resolveMigrateHistory: the
// target block's value wins when explicitly set, else the top-level value,
// else false (every branch keeps its long-lived type, the pre-#655 behavior).
func resolveUseSLBForReferenceBranchNewCode(target, top *FlexibleBool) bool {
	return resolveMigrateHistory(target, top)
}

// referenceBranchNCDBranches returns the names of the project's branches whose
// effective new code definition on the source is a reference branch. A branch
// with no entry of its own inherits the project-level definition.
func referenceBranchNCDBranches(e *Executor, serverURL, serverKey string, branches []branchInfo) map[string]bool {
	scope := extractScope{ServerURL: serverURL, ProjectKey: serverKey}
	projectLevelRef := false
	perBranch := map[string]bool{} // branch → its own definition is a reference branch
	for item := range scopedExtractItems(e, "getNewCodePeriods", scope) {
		isRef := strings.EqualFold(extractField(item.Data, "type"), slbReferenceNCDType)
		branchKey := extractField(item.Data, "branchKey")
		if branchKey == "" {
			projectLevelRef = isRef
			continue
		}
		perBranch[branchKey] = isRef
	}
	out := map[string]bool{}
	for _, b := range branches {
		if isRef, ok := perBranch[b.Name]; ok {
			if isRef {
				out[b.Name] = true
			}
		} else if projectLevelRef {
			out[b.Name] = true
		}
	}
	return out
}

// markShortLivedBranches flags the non-main branches whose new code is a
// reference branch as short-lived. The main branch can never be short-lived.
// Returns the names flagged.
func markShortLivedBranches(branches []branchInfo, refBranches map[string]bool) []string {
	var names []string
	for i := range branches {
		if branches[i].IsMain || !refBranches[branches[i].Name] {
			continue
		}
		branches[i].ShortLived = true
		names = append(names, branches[i].Name)
	}
	return names
}

// patternMatchesAny reports whether the (Java, full-match) branch pattern
// matches any name. A pattern Go cannot compile is assumed to match, so the
// caller errs on the side of overriding it.
func patternMatchesAny(pattern string, names []string) bool {
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return true
	}
	for _, n := range names {
		if re.MatchString(n) {
			return true
		}
	}
	return false
}

// shortLivedPatternGuard makes the project's long-lived branch pattern not
// match the given branch names and returns a function restoring the original
// setting. When the current pattern already excludes them nothing is changed
// and the restore is a no-op. A failure to read or change the pattern is
// logged and yields a no-op restore: the branches then migrate with whatever
// type the pattern gives them rather than failing the project.
func shortLivedPatternGuard(ctx context.Context, e *Executor, cloudKey string, names []string) func() {
	noop := func() {}
	if len(names) == 0 || e.Cloud == nil || e.Cloud.Settings == nil {
		return noop
	}
	settings, err := e.Cloud.Settings.Values(ctx, cloudKey, "")
	if err != nil {
		e.Logger.Warn("could not read the long-lived branch pattern; short-lived branches may migrate as long-lived",
			"project", cloudKey, "err", err)
		return noop
	}
	original, hadOwn := "", false
	for _, s := range settings {
		if s.Key == sqcBranchRegexSetting {
			original, hadOwn = s.Value, !s.Inherited
			break
		}
	}
	effective := original
	if effective == "" {
		effective = defaultSQCLongLivedBranchRegex
	}
	if !patternMatchesAny(effective, names) {
		return noop
	}
	if err := e.Cloud.Settings.Set(ctx, cloudKey, sqcBranchRegexSetting, neverMatchBranchRegex, ""); err != nil {
		e.Logger.Warn("could not adjust the long-lived branch pattern; short-lived branches may migrate as long-lived",
			"project", cloudKey, "err", err)
		return noop
	}
	e.Logger.Info("long-lived branch pattern temporarily adjusted to migrate reference-branch branches as short-lived",
		"project", cloudKey, "original", original, "branches", names)
	return func() {
		// The run may be cancelled; the pattern must still be put back.
		rctx := context.WithoutCancel(ctx)
		var err error
		if hadOwn {
			err = e.Cloud.Settings.Set(rctx, cloudKey, sqcBranchRegexSetting, original, "")
		} else {
			err = e.Cloud.Settings.Reset(rctx, cloudKey, []string{sqcBranchRegexSetting}, "")
		}
		if err != nil {
			logAPIWarn(e.Logger, "failed to restore the long-lived branch pattern", err,
				"project", cloudKey, "setting", sqcBranchRegexSetting, "original", original)
			return
		}
		e.Logger.Info("long-lived branch pattern restored", "project", cloudKey)
	}
}

// dropLongLivedBeforeShort deletes the target branches that must migrate as
// short-lived but already exist as long-lived (typically from an earlier run
// without the option): a branch's type cannot change, so the CE would reject
// the report ("Invalid branch type 'SHORT'. Branch ... already exists with type
// 'LONG'"). The branch is recreated as short-lived by its import. A branch the
// import will skip as already completed in this run is never deleted, since
// nothing would recreate it. Returns the names deleted; a failure is logged and
// leaves that branch to fail on import.
func dropLongLivedBeforeShort(ctx context.Context, e *Executor, cloudKey string, existing []types.Branch, branches []branchInfo, completed map[string]bool) []string {
	if e.Cloud == nil || e.Cloud.Branches == nil {
		return nil
	}
	longLived := map[string]bool{}
	for _, b := range existing {
		if !b.IsMain && strings.EqualFold(b.Type, "LONG") {
			longLived[b.Name] = true
		}
	}
	var deleted []string
	for _, b := range branches {
		if !b.ShortLived || !longLived[b.Name] || shouldSkipBranch(completed, cloudKey, b.Name) {
			continue
		}
		if err := e.Cloud.Branches.Delete(ctx, cloudKey, b.Name); err != nil {
			logAPIWarn(e.Logger, "could not delete the long-lived branch to recreate it as short-lived", err,
				"project", cloudKey, "branch", b.Name)
			continue
		}
		e.Logger.Info("deleted long-lived branch to recreate it as short-lived",
			"project", cloudKey, "branch", b.Name)
		deleted = append(deleted, b.Name)
	}
	return deleted
}

// reportBranchTypeFor is the metadata branch_type of a branch's report. A real
// scanner's short-lived branch report (#655) says PULL_REQUEST — with the branch
// name and reference branch but no pull_request_key; BRANCH is read as long-lived.
func reportBranchTypeFor(shortLived bool) pb.Metadata_BranchType {
	if shortLived {
		return pb.Metadata_PULL_REQUEST
	}
	return pb.Metadata_BRANCH
}
