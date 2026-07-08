// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package scanreport

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
	"time"

	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
	"google.golang.org/protobuf/proto"
)

func TestPackageReportMinimal(t *testing.T) {
	zipBytes := packageMinimalReport(t)

	t.Run("zip structure", func(t *testing.T) {
		verifyZipStructure(t, zipBytes)
	})

	t.Run("issues and sources", func(t *testing.T) {
		verifyIssuesAndSources(t, zipBytes)
	})
}

func packageMinimalReport(t *testing.T) []byte {
	t.Helper()
	root, files, cr := BuildComponents("proj", []ComponentInput{
		{Key: "proj:main.go", Name: "main.go", Path: "main.go", Language: "go", Lines: 5},
	})
	fileRef := cr.Refs()["proj:main.go"]

	data := &ReportData{
		Metadata: BuildMetadata(MetadataInput{
			AnalysisDate: time.Now(),
			OrgKey:       "org",
			ProjectKey:   "proj",
			BranchName:   "main",
			BranchType:   pb.Metadata_BRANCH,
		}, root.Ref),
		RootComponent:  root,
		FileComponents: files,
		Issues: map[int32][]*pb.Issue{
			fileRef: {{RuleRepository: "go", RuleKey: "S1", Msg: "test issue"}},
		},
		Measures:    make(map[int32][]*pb.Measure),
		Changesets:  make(map[int32]*pb.Changesets),
		ActiveRules: []*pb.ActiveRule{{RuleRepository: "go", RuleKey: "S1"}},
		Sources:     map[int32]string{fileRef: "package main\n\nfunc main() {}\n"},
	}

	zipBytes, err := PackageReport(data)
	if err != nil {
		t.Fatalf("PackageReport: %v", err)
	}
	if len(zipBytes) == 0 {
		t.Fatal("expected non-empty zip")
	}
	return zipBytes
}

func verifyZipStructure(t *testing.T, zipBytes []byte) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("reading zip: %v", err)
	}

	expected := map[string]bool{
		"metadata.pb":      false,
		"analytics.pb":     false,
		"component-1.pb":   false,
		"component-2.pb":   false,
		"activerules.pb":   false,
		"context-props.pb": false,
	}
	for _, f := range zr.File {
		if _, ok := expected[f.Name]; ok {
			expected[f.Name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing expected file: %s", name)
		}
	}
}

func verifyIssuesAndSources(t *testing.T, zipBytes []byte) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("reading zip: %v", err)
	}

	hasIssues := false
	hasSource := false
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "issues-") {
			hasIssues = true
		}
		if strings.HasPrefix(f.Name, "source-") {
			hasSource = true
		}
	}
	if !hasIssues {
		t.Error("expected issues-*.pb file in zip")
	}
	if !hasSource {
		t.Error("expected source-*.txt file in zip")
	}
}

func TestPackageReportEmptyData(t *testing.T) {
	root := &pb.Component{
		Ref:  1,
		Name: "proj",
		Type: pb.Component_PROJECT,
		Key:  "proj",
	}

	data := &ReportData{
		Metadata: &pb.Metadata{
			AnalysisDate:     time.Now().UnixMilli(),
			ProjectKey:       "proj",
			RootComponentRef: 1,
		},
		RootComponent:  root,
		FileComponents: nil,
		Issues:         nil,
		Measures:       nil,
		Changesets:     nil,
		ActiveRules:    nil,
		Sources:        nil,
	}

	zipBytes, err := PackageReport(data)
	if err != nil {
		t.Fatalf("PackageReport: %v", err)
	}
	if len(zipBytes) == 0 {
		t.Fatal("expected non-empty zip even for empty data")
	}
}

func TestWriteDelimited(t *testing.T) {
	rule := &pb.ActiveRule{
		RuleRepository: "go",
		RuleKey:        "S1234",
		Severity:       pb.Severity_MAJOR,
	}

	var buf bytes.Buffer
	if err := writeDelimited(&buf, rule); err != nil {
		t.Fatalf("writeDelimited: %v", err)
	}

	data := buf.Bytes()
	if len(data) == 0 {
		t.Fatal("expected non-empty output")
	}

	// First byte should be the varint length.
	msgLen := data[0]
	if int(msgLen) != len(data)-1 {
		t.Errorf("expected length prefix %d, got %d", len(data)-1, msgLen)
	}

	// Verify we can unmarshal the message portion.
	var decoded pb.ActiveRule
	if err := proto.Unmarshal(data[1:], &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.RuleKey != "S1234" {
		t.Errorf("expected S1234, got %s", decoded.RuleKey)
	}
}
