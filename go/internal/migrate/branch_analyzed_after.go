// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

// resolveBranchAnalyzedAfter mirrors resolveMigrateHistory/resolveFastSync
// for the branch_analyzed_after string (#583), but for a plain string
// instead of a FlexibleBool: the target block's value wins when explicitly
// present — even an explicit "" ("no filter for migrate", overriding a
// non-empty top-level value) — else the top-level value, else "" (no
// filter, current behavior).
func resolveBranchAnalyzedAfter(phase *string, top string) string {
	if phase != nil {
		return *phase
	}
	return top
}
