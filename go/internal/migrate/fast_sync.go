// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

// resolveFastSync mirrors resolveUnsupportedLanguages for the fast_sync
// tri-state (#527): the target block's value wins when explicitly set,
// else the top-level value, else the default (false — every hotspot is
// tagged and back-linked, the pre-#527 behavior).
func resolveFastSync(target, top *FlexibleBool) bool {
	if target != nil && target.Set {
		return target.Value
	}
	if top != nil && top.Set {
		return top.Value
	}
	return false
}
