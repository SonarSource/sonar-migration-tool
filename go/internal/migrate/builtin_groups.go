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
