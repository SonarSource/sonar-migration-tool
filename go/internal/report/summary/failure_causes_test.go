// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import "testing"

// The ledger used to print a raw API message and leave the reader to work out
// whether anything could be done. Failures are now grouped into one
// explanation per distinct cause, so a run with tens of thousands of
// identical failures explains itself in a few lines.
func TestGroupFailureCauses(t *testing.T) {
	rows := []FailureRow{
		// Same class AND same explanation -> one group.
		{EntityName: "a", Cause: "by-design", Why: "no project scope", Remediation: "none"},
		{EntityName: "b", Cause: "by-design", Why: "no project scope", Remediation: "none"},
		{EntityName: "c", Cause: "by-design", Why: "no project scope", Remediation: "none"},
		// Same class, DIFFERENT explanation -> must stay separate, or the
		// report would describe these failures with someone else's reason.
		{EntityName: "d", Cause: "customer-environment-issue", Why: "no private projects", Remediation: "upgrade plan"},
		{EntityName: "e", Cause: "customer-environment-issue", Why: "token lacks permission", Remediation: "grant Administer"},
		{EntityName: "f", Cause: "customer-environment-issue", Why: "token lacks permission", Remediation: "grant Administer"},
		// Reportable must survive grouping.
		{EntityName: "g", Cause: "bug", Why: "unrecognised rejection", Remediation: "report it", Reportable: true},
		// Unclassified rows are not invented into a cause.
		{EntityName: "h"},
	}

	got := groupFailureCauses(rows)

	if len(got) != 4 {
		var seen []string
		for _, c := range got {
			seen = append(seen, c.Cause+"/"+c.Why)
		}
		t.Fatalf("expected 4 distinct explanations, got %d: %v", len(got), seen)
	}

	// Most frequent first, so the dominant cause leads.
	if got[0].Cause != "by-design" || got[0].Count != 3 {
		t.Errorf("first group = %s/%d, want by-design/3", got[0].Cause, got[0].Count)
	}
	if got[1].Why != "token lacks permission" || got[1].Count != 2 {
		t.Errorf("second group = %q/%d, want token lacks permission/2", got[1].Why, got[1].Count)
	}

	byWhy := map[string]FailureCause{}
	for _, c := range got {
		byWhy[c.Why] = c
	}
	if c := byWhy["no private projects"]; c.Count != 1 || c.Remediation != "upgrade plan" {
		t.Errorf("the second environment explanation was lost or merged: %+v", c)
	}
	if c := byWhy["unrecognised rejection"]; !c.Reportable {
		t.Error("Reportable must survive grouping — it is what tells the operator to file a report")
	}
	total := 0
	for _, c := range got {
		total += c.Count
	}
	if total != 7 {
		t.Errorf("grouped counts total %d, want 7 (the unclassified row is excluded)", total)
	}
}

// A sample of names orients the reader; it must not become an inventory.
func TestGroupFailureCausesCapsExamples(t *testing.T) {
	rows := make([]FailureRow, 0, 50)
	for i := 0; i < 50; i++ {
		rows = append(rows, FailureRow{
			EntityName: string(rune('a' + i%26)),
			Cause:      "by-design", Why: "same reason",
		})
	}
	got := groupFailureCauses(rows)
	if len(got) != 1 {
		t.Fatalf("expected 1 group, got %d", len(got))
	}
	if got[0].Count != 50 {
		t.Errorf("count = %d, want 50", got[0].Count)
	}
	if len(got[0].Entities) != maxCauseSampleEntities {
		t.Errorf("examples = %d, want the cap of %d", len(got[0].Entities), maxCauseSampleEntities)
	}
}

func TestGroupFailureCausesEmpty(t *testing.T) {
	if got := groupFailureCauses(nil); got != nil {
		t.Errorf("expected nil for no rows, got %v", got)
	}
}

// The table cell must read as plain English, not as an internal enum.
func TestFailureCauseLabel(t *testing.T) {
	for in, want := range map[string]string{
		"by-design":                  "Not supported on Cloud",
		"already-done":               "Already present",
		"customer-environment-issue": "Environment",
		"bug":                        "Needs reporting",
		"":                           "Unclassified",
		"something-new":              "something-new",
	} {
		if got := failureCauseLabel(in); got != want {
			t.Errorf("failureCauseLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
