// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// dbCleanerBranchesSetting is the SQS key that #249 migrates as a regex.
const dbCleanerBranchesSetting = "sonar.dbcleaner.branchesToKeepWhenInactive"

// sqcBranchRegexSetting is the SQC target key. Same role: list of
// branches to keep when inactive. SQC accepts a single regex string.
const sqcBranchRegexSetting = "sonar.branch.longLivedBranches.regex"

// extractDbCleanerBranches peels off the dbcleaner setting from a
// slice of getServerSettings records. Returns the matching record
// (or nil if absent) and the remainder. Operates on the same
// json.RawMessage slice shape that runSetGlobalSettings already uses.
func extractDbCleanerBranches(values []json.RawMessage) (dbCleaner json.RawMessage, rest []json.RawMessage) {
	rest = values[:0]
	for _, raw := range values {
		if extractField(raw, "key") == dbCleanerBranchesSetting {
			dbCleaner = raw
			continue
		}
		rest = append(rest, raw)
	}
	return dbCleaner, rest
}

// CombineBranchesAsRegex turns a list of SQS branch patterns / names
// into the single regex string SonarQube Cloud expects at
// sonar.branch.longLivedBranches.regex. Empty list → empty string. A
// single entry passes through verbatim (already a regex on SQS); two
// or more are wrapped as "(a|b|c)" so any of them matches.
//
// Exported so the predict pipeline can preview the exact value the
// real migrate task will POST.
func CombineBranchesAsRegex(values []string) string {
	if len(values) == 0 {
		return ""
	}
	if len(values) == 1 {
		return values[0]
	}
	out := "("
	for i, v := range values {
		if i > 0 {
			out += "|"
		}
		out += v
	}
	out += ")"
	return out
}

// DbCleanerBranchesTransformNote returns the human-readable Detail
// the report should display for a #249 multi-value transformation.
// Empty when the source list has a single value (no transformation
// to document — it's a 1:1 mapping by issue's request).
func DbCleanerBranchesTransformNote(values []string, regex string) string {
	if len(values) < 2 {
		return ""
	}
	return fmt.Sprintf(
		"Combined %d SonarQube Server branch patterns into a single SonarQube Cloud regex %q (target setting: %s).",
		len(values), regex, sqcBranchRegexSetting)
}

// applyDbCleanerBranchesGlobal handles #249 part 1: takes the SQS
// sonar.dbcleaner.branchesToKeepWhenInactive record and writes its
// regex form to every target SQC org under
// sonar.branch.longLivedBranches.regex. Emits one outcome per org
// (status=applied) so the Global Settings section renders the
// transformation as Perfect (green). When the source list has 2+
// values the per-org Detail also documents the regex combination.
func applyDbCleanerBranchesGlobal(ctx context.Context, e *Executor, raw json.RawMessage, orgList []string,
	w *common.ChunkWriter, mu *sync.Mutex, counter *TaskCounter) {

	if raw == nil {
		return
	}
	values := extractStringArray(raw, "values")
	if len(values) == 0 {
		if v := extractField(raw, "value"); v != "" {
			values = []string{v}
		}
	}
	if len(values) == 0 {
		return
	}
	regex := CombineBranchesAsRegex(values)
	note := DbCleanerBranchesTransformNote(values, regex)

	rec := globalSettingResult{Key: dbCleanerBranchesSetting, Values: values}
	for _, org := range orgList {
		err := e.Cloud.Settings.Set(ctx, "", sqcBranchRegexSetting, regex, org)
		if err != nil {
			failAPI(counter, e.Logger, "setGlobalSettings dbcleaner branches → regex failed", err,
				"org", org, "target_key", sqcBranchRegexSetting, "value", regex)
			rec.Outcomes = append(rec.Outcomes, orgOutcome{
				Org: org, Status: outcomeFailed, Reason: err.Error(),
				Detail: fmt.Sprintf("Failed to migrate as %s on SonarQube Cloud", sqcBranchRegexSetting),
			})
			continue
		}
		counter.Success()
		detail := fmt.Sprintf("Migrated to %s on SonarQube Cloud", sqcBranchRegexSetting)
		if note != "" {
			detail = note
		}
		rec.Outcomes = append(rec.Outcomes, orgOutcome{
			Org: org, Status: outcomeApplied, Detail: detail,
		})
	}
	b, _ := json.Marshal(rec)
	mu.Lock()
	_ = w.WriteOne(b)
	mu.Unlock()
}
