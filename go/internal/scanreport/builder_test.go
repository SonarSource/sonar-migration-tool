// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package scanreport

import (
	"testing"
	"time"

	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

func TestComponentRef(t *testing.T) {
	cr := NewComponentRef()
	r1 := cr.Get("proj")
	r2 := cr.Get("file1")
	r3 := cr.Get("file2")
	r1b := cr.Get("proj") // should reuse

	if r1 != 1 || r2 != 2 || r3 != 3 {
		t.Errorf("expected refs 1,2,3 got %d,%d,%d", r1, r2, r3)
	}
	if r1b != r1 {
		t.Errorf("expected same ref for proj, got %d vs %d", r1, r1b)
	}

	refs := cr.Refs()
	if len(refs) != 3 {
		t.Errorf("expected 3 refs, got %d", len(refs))
	}
}

func TestBuildMetadata(t *testing.T) {
	input := MetadataInput{
		AnalysisDate: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
		OrgKey:       "my-org",
		ProjectKey:   "my-proj",
		BranchName:   "main",
		BranchType:   pb.Metadata_BRANCH,
		QProfiles: []QProfileInfo{
			{Key: "qp1", Name: "Go Profile", Language: "go", RulesUpdatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
		FileCountByExt: map[string]int32{"go": 10},
	}

	md := BuildMetadata(input, 1)
	if md.ProjectKey != "my-proj" {
		t.Errorf("expected project key my-proj, got %s", md.ProjectKey)
	}
	if md.OrganizationKey != "my-org" {
		t.Errorf("expected org key my-org, got %s", md.OrganizationKey)
	}
	if md.BranchName != "main" {
		t.Errorf("expected branch main, got %s", md.BranchName)
	}
	if md.RootComponentRef != 1 {
		t.Errorf("expected root ref 1, got %d", md.RootComponentRef)
	}
	if md.ProjectVersion != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", md.ProjectVersion)
	}
	if len(md.ScmRevisionId) == 0 {
		t.Error("expected non-empty scm revision")
	}
	if len(md.QprofilesPerLanguage) != 1 {
		t.Errorf("expected 1 qprofile, got %d", len(md.QprofilesPerLanguage))
	}
	goProfile := md.QprofilesPerLanguage["go"]
	if goProfile == nil || goProfile.Key != "qp1" {
		t.Error("expected go qprofile with key qp1")
	}
}

func TestBuildComponents(t *testing.T) {
	files := []ComponentInput{
		{Key: "proj:src/main.go", Name: "main.go", Path: "src/main.go", Language: "go", Lines: 50},
		{Key: "proj:src/util.go", Name: "util.go", Path: "src/util.go", Language: "Go", Lines: 30},
	}

	root, fileComps, cr := BuildComponents("proj", files)

	if root.Type != pb.Component_PROJECT {
		t.Error("root should be PROJECT type")
	}
	if root.Key != "proj" {
		t.Errorf("root key: got %s", root.Key)
	}
	if len(root.ChildRef) != 2 {
		t.Errorf("expected 2 child refs, got %d", len(root.ChildRef))
	}
	if len(fileComps) != 2 {
		t.Errorf("expected 2 file components, got %d", len(fileComps))
	}

	// Language should be lowercased.
	if fileComps[1].Language != "go" {
		t.Errorf("expected language 'go', got %q", fileComps[1].Language)
	}
	if fileComps[0].Status != pb.Component_ADDED {
		t.Error("expected file status ADDED")
	}

	if len(cr.Refs()) != 3 { // proj + 2 files
		t.Errorf("expected 3 refs, got %d", len(cr.Refs()))
	}
}

func TestBuildIssues(t *testing.T) {
	cr := NewComponentRef()
	cr.Get("proj")
	fileRef := cr.Get("proj:file.go")

	issues := []IssueInput{
		{RuleRepo: "go", RuleKey: "S1234", Message: "fix this", Severity: "MAJOR",
			StartLine: 10, EndLine: 15, Component: "proj:file.go"},
		{RuleRepo: "go", RuleKey: "S5678", Message: "fix that", Severity: "MINOR",
			StartLine: 20, EndLine: 20, Component: "proj:file.go"},
		{RuleRepo: "go", RuleKey: "S9999", Message: "missing", Component: "unknown"},
	}

	result := BuildIssues(issues, cr)
	if len(result[fileRef]) != 2 {
		t.Errorf("expected 2 issues for file ref, got %d", len(result[fileRef]))
	}
	if len(result) != 1 {
		t.Errorf("expected 1 component with issues, got %d", len(result))
	}

	iss := result[fileRef][0]
	if iss.RuleRepository != "go" || iss.RuleKey != "S1234" {
		t.Errorf("issue rule: got %s:%s", iss.RuleRepository, iss.RuleKey)
	}
	if iss.TextRange.StartLine != 10 || iss.TextRange.EndLine != 15 {
		t.Errorf("text range: got %d-%d", iss.TextRange.StartLine, iss.TextRange.EndLine)
	}
}

func TestBuildMeasures(t *testing.T) {
	cr := NewComponentRef()
	cr.Get("proj")
	fileRef := cr.Get("proj:file.go")

	measures := []MeasureInput{
		{Component: "proj:file.go", MetricKey: "ncloc", Value: "100"},
		{Component: "proj:file.go", MetricKey: "coverage", Value: "85.5"},
		{Component: "proj:file.go", MetricKey: "alert_status", Value: "OK"},
		{Component: "unknown", MetricKey: "ncloc", Value: "50"},
	}

	result := BuildMeasures(measures, cr)
	if len(result[fileRef]) != 3 {
		t.Errorf("expected 3 measures, got %d", len(result[fileRef]))
	}

	// Check int value
	m0 := result[fileRef][0]
	if m0.MetricKey != "ncloc" {
		t.Errorf("expected ncloc, got %s", m0.MetricKey)
	}
	if iv, ok := m0.Value.(*pb.Measure_IntValue_); !ok || iv.IntValue.Value != 100 {
		t.Error("expected int value 100")
	}

	// Check double value
	m1 := result[fileRef][1]
	if dv, ok := m1.Value.(*pb.Measure_DoubleValue_); !ok || dv.DoubleValue.Value != 85.5 {
		t.Error("expected double value 85.5")
	}

	// Check string value
	m2 := result[fileRef][2]
	if sv, ok := m2.Value.(*pb.Measure_StringValue_); !ok || sv.StringValue.Value != "OK" {
		t.Error("expected string value OK")
	}
}

func TestBuildActiveRules(t *testing.T) {
	rules := []ActiveRuleInput{
		{RuleRepo: "go", RuleKey: "S1234", Severity: "MAJOR", QProfileKey: "qp1"},
		{RuleRepo: "java", RuleKey: "S5678", Severity: "BLOCKER", QProfileKey: "qp2"},
	}

	const ts = int64(1700000000000)
	result := BuildActiveRules(rules, ts)
	if len(result) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(result))
	}
	if result[0].Severity != pb.Severity_MAJOR {
		t.Errorf("expected MAJOR severity, got %v", result[0].Severity)
	}
	if result[1].Severity != pb.Severity_BLOCKER {
		t.Errorf("expected BLOCKER severity, got %v", result[1].Severity)
	}
	// The reference scanner always sets a non-nil params map and non-zero
	// timestamps; verify our mirror does too.
	if result[0].ParamsByKey == nil {
		t.Error("expected non-nil ParamsByKey")
	}
	if result[0].CreatedAt != ts || result[0].UpdatedAt != ts {
		t.Errorf("expected createdAt/updatedAt to default to %d, got %d/%d", ts, result[0].CreatedAt, result[0].UpdatedAt)
	}
}

func TestBuildDefaultChangesets(t *testing.T) {
	date := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cs := BuildDefaultChangesets(5, 10, date)

	if cs.ComponentRef != 5 {
		t.Errorf("expected ref 5, got %d", cs.ComponentRef)
	}
	if len(cs.Changeset) != 1 {
		t.Fatalf("expected 1 changeset entry, got %d", len(cs.Changeset))
	}
	if cs.Changeset[0].Date != date.UnixMilli() {
		t.Error("expected changeset date to match")
	}
	if len(cs.ChangesetIndexByLine) != 10 {
		t.Errorf("expected 10 line indices, got %d", len(cs.ChangesetIndexByLine))
	}
}

func TestMapSeverity(t *testing.T) {
	cases := []struct {
		input    string
		expected pb.Severity
	}{
		{"INFO", pb.Severity_INFO},
		{"MINOR", pb.Severity_MINOR},
		{"MAJOR", pb.Severity_MAJOR},
		{"CRITICAL", pb.Severity_CRITICAL},
		{"BLOCKER", pb.Severity_BLOCKER},
		{"info", pb.Severity_INFO},
		{"unknown", pb.Severity_UNSET_SEVERITY},
		{"", pb.Severity_UNSET_SEVERITY},
	}
	for _, tc := range cases {
		got := mapSeverity(tc.input)
		if got != tc.expected {
			t.Errorf("mapSeverity(%q): got %v, want %v", tc.input, got, tc.expected)
		}
	}
}

func TestBuildMeasureValueBool(t *testing.T) {
	m := buildMeasureValue("test", "true")
	if _, ok := m.Value.(*pb.Measure_BooleanValue); !ok {
		t.Error("expected bool value for 'true'")
	}
	m2 := buildMeasureValue("test", "false")
	if bv, ok := m2.Value.(*pb.Measure_BooleanValue); !ok || bv.BooleanValue.Value {
		t.Error("expected bool value false")
	}
}
