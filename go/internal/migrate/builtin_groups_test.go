package migrate

import "testing"

func TestMapGroupNameToCloud_RegularGroupPassesThrough(t *testing.T) {
	got, ok := MapGroupNameToCloud("devs")
	if !ok || got != "devs" {
		t.Errorf("regular group should pass through unchanged, got (%q, %v)", got, ok)
	}
}

func TestMapGroupNameToCloud_SonarUsersBecomesMembers(t *testing.T) {
	// Issue #269: permissions granted to SQS's built-in `sonar-users`
	// must be re-applied to SQC's built-in `Members` group, which is
	// the closest equivalent.
	got, ok := MapGroupNameToCloud("sonar-users")
	if !ok {
		t.Fatal("sonar-users should be remapped, not skipped")
	}
	if got != "Members" {
		t.Errorf("sonar-users should map to Members, got %q", got)
	}
}

func TestIsBuiltInGroup_SonarUsersStillSkippedForCreation(t *testing.T) {
	// IsBuiltInGroup gates createGroups: even though sonar-users now
	// has a permission-target alias, we must NOT try to create it as
	// a custom group on SQC.
	if !IsBuiltInGroup("sonar-users") {
		t.Error("sonar-users must remain on the built-in skip list for createGroups")
	}
}
