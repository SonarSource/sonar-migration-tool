package migrate

// builtInGroupSkipNotes lists SonarQube Server built-in groups that
// have no SonarQube Cloud equivalent (or are managed by SQC itself),
// keyed by group name. The value is the explanatory Detail string the
// migration report displays in the Skipped bucket.
//
// Both real migrate (runCreateGroups) and the predictive-report
// pipeline short-circuit creation for these names. The summary
// collector then injects a single Skipped row per built-in group
// found in generateGroupMappings so the operator sees what happened.
var builtInGroupSkipNotes = map[string]string{
	"sonar-users": "Built-in group on SonarQube Server; replaced by the implicit 'Members' group on SonarQube Cloud.",
}

// builtInGroupCloudAliases maps SQS built-in group names to their
// SonarQube Cloud equivalents for permission-granting purposes.
// Issue #269: SQS's `sonar-users` is the "everyone in this server"
// group; SQC's equivalent is `Members`. Permissions granted to
// `sonar-users` on SQS should be re-granted to `Members` on SQC so
// the migration tool doesn't try to add permissions to a non-existent
// group (404 from /api/permissions/add_group).
//
// Empty target = "don't re-grant on SQC" (no equivalent group exists).
var builtInGroupCloudAliases = map[string]string{
	"sonar-users": "Members",
}

// IsBuiltInGroup reports whether the named group is a SonarQube Server
// built-in that should be skipped during migration.
func IsBuiltInGroup(name string) bool {
	_, ok := builtInGroupSkipNotes[name]
	return ok
}

// BuiltInGroupSkipNote returns the user-facing Detail string for a
// built-in group name, plus ok=true when the name is recognised. Used
// by the summary collector to inject a Skipped row.
func BuiltInGroupSkipNote(name string) (string, bool) {
	note, ok := builtInGroupSkipNotes[name]
	return note, ok
}

// MapGroupNameToCloud returns the SQC-side group name to use when
// granting a permission originally held by the named SQS group. For
// most groups this is just the input — only built-in groups with a
// known SQC counterpart (today: `sonar-users` → `Members`) are
// remapped. Returns (name, true) when a permission should actually be
// granted; returns ("", false) when no SQC equivalent exists and the
// caller should skip the grant entirely.
func MapGroupNameToCloud(name string) (string, bool) {
	if alias, ok := builtInGroupCloudAliases[name]; ok {
		if alias == "" {
			return "", false
		}
		return alias, true
	}
	return name, true
}
