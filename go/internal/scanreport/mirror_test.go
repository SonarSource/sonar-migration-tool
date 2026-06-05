package scanreport

import (
	"context"
	"testing"

	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

// These tests lock in the CloudVoyager-mirroring behavior that makes the
// SonarCloud Compute Engine accept our scanner report: MQR impacts/clean-code
// on external issues and ad-hoc rules, and omission of the branch
// characteristic for the project's main branch.

func TestBuildExternalIssuesHonorsExtractedMQR(t *testing.T) {
	cr := NewComponentRef()
	cr.Get("comp")
	res := BuildExternalIssues([]ExternalIssueInput{{
		EngineID: "pylint", RuleID: "W0718", Message: "m", Severity: "MAJOR", Type: "CODE_SMELL",
		Component: "comp", Effort: "5min", CleanCodeAttribute: "CONVENTIONAL",
		Impacts: []ImpactInput{{SoftwareQuality: "MAINTAINABILITY", Severity: "MEDIUM"}},
	}}, cr)

	got := res[cr.Get("comp")]
	if len(got) != 1 {
		t.Fatalf("want 1 external issue, got %d", len(got))
	}
	e := got[0]
	if len(e.Impacts) != 1 ||
		e.Impacts[0].SoftwareQuality != pb.SoftwareQuality_MAINTAINABILITY ||
		e.Impacts[0].Severity != pb.ImpactSeverity_ImpactSeverity_MEDIUM {
		t.Errorf("impacts not honored: %v", e.Impacts)
	}
	if e.CleanCodeAttribute == nil || *e.CleanCodeAttribute != pb.CleanCodeAttribute_CONVENTIONAL {
		t.Errorf("cleanCodeAttribute not honored: %v", e.CleanCodeAttribute)
	}
	if e.Effort != 5 {
		t.Errorf("effort: want 5, got %d", e.Effort)
	}
}

func TestBuildExternalIssuesFallsBackToTypeAndSeverity(t *testing.T) {
	cr := NewComponentRef()
	cr.Get("c")
	res := BuildExternalIssues([]ExternalIssueInput{{
		EngineID: "e", RuleID: "r", Severity: "BLOCKER", Type: "BUG", Component: "c",
	}}, cr)
	e := res[cr.Get("c")][0]
	// No extracted impacts/cleanCode -> derive from type+severity, always >=1.
	if len(e.Impacts) != 1 ||
		e.Impacts[0].SoftwareQuality != pb.SoftwareQuality_RELIABILITY ||
		e.Impacts[0].Severity != pb.ImpactSeverity_ImpactSeverity_HIGH {
		t.Errorf("fallback impacts wrong: %v", e.Impacts)
	}
	if e.CleanCodeAttribute == nil || *e.CleanCodeAttribute != pb.CleanCodeAttribute_LOGICAL {
		t.Errorf("fallback cleanCode wrong: %v", e.CleanCodeAttribute)
	}
}

func TestBuildAdHocRulesAlwaysSetsImpactsAndCleanCode(t *testing.T) {
	res := BuildAdHocRules([]AdHocRuleInput{{
		EngineID: "e", RuleID: "r", Name: "r", Type: "VULNERABILITY", Severity: "CRITICAL",
	}})
	if len(res) != 1 {
		t.Fatalf("want 1 ad-hoc rule, got %d", len(res))
	}
	r := res[0]
	if len(r.DefaultImpacts) != 1 || r.DefaultImpacts[0].SoftwareQuality != pb.SoftwareQuality_SECURITY {
		t.Errorf("ad-hoc defaultImpacts wrong: %v", r.DefaultImpacts)
	}
	if r.CleanCodeAttribute == nil || *r.CleanCodeAttribute != pb.CleanCodeAttribute_TRUSTWORTHY {
		t.Errorf("ad-hoc cleanCode wrong: %v", r.CleanCodeAttribute)
	}
}

func TestParseEffortToMinutes(t *testing.T) {
	cases := map[string]int64{"": 0, "5min": 5, "2h": 120, "1h30min": 90, "45min": 45}
	for in, want := range cases {
		if got := parseEffortToMinutes(in); got != want {
			t.Errorf("parseEffortToMinutes(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSubmitReportMainBranchOmitsCharacteristic(t *testing.T) {
	srv, cap := newSubmitServer(t)
	defer srv.Close()
	if _, err := SubmitReport(context.Background(), srv.Client(), SubmitConfig{
		CloudURL: srv.URL, ProjectKey: "p", OrgKey: "o", BranchName: "master", IsMain: true,
	}, []byte("zip")); err != nil {
		t.Fatal(err)
	}
	if v, ok := cap.fields["characteristic"]; ok {
		t.Errorf("main branch must NOT send a branch characteristic, got %q", v)
	}
}

func TestSubmitReportNonMainBranchSendsCharacteristic(t *testing.T) {
	srv, cap := newSubmitServer(t)
	defer srv.Close()
	if _, err := SubmitReport(context.Background(), srv.Client(), SubmitConfig{
		CloudURL: srv.URL, ProjectKey: "p", OrgKey: "o", BranchName: "feature/x", IsMain: false,
	}, []byte("zip")); err != nil {
		t.Fatal(err)
	}
	// parseMultipart keeps the last value for a repeated field name; the
	// branchType=LONG characteristic is sent last for non-main branches.
	if cap.fields["characteristic"] != "branchType=LONG" {
		t.Errorf("non-main branch must send branch characteristics, got %q", cap.fields["characteristic"])
	}
}
