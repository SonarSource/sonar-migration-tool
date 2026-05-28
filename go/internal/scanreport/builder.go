package scanreport

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

// ComponentRef tracks the mapping between SonarQube component keys and
// protobuf integer references used in the scanner report.
type ComponentRef struct {
	refs    map[string]int32
	nextRef int32
}

// NewComponentRef creates a new ref allocator starting at 1.
func NewComponentRef() *ComponentRef {
	return &ComponentRef{refs: make(map[string]int32), nextRef: 1}
}

// Refs returns the internal key-to-ref map for read access.
func (cr *ComponentRef) Refs() map[string]int32 {
	return cr.refs
}

// Get returns the ref for a component key, allocating a new one if needed.
func (cr *ComponentRef) Get(key string) int32 {
	if ref, ok := cr.refs[key]; ok {
		return ref
	}
	ref := cr.nextRef
	cr.refs[key] = ref
	cr.nextRef++
	return ref
}

// QProfileInfo holds quality profile info needed for the metadata message.
type QProfileInfo struct {
	Key            string
	Name           string
	Language       string
	RulesUpdatedAt time.Time
}

// MetadataInput holds the inputs for building the Metadata protobuf message.
type MetadataInput struct {
	AnalysisDate        time.Time
	OrgKey              string
	ProjectKey          string
	BranchName          string
	ReferenceBranchName string
	BranchType          pb.Metadata_BranchType
	ProjectVersion      string
	QProfiles           []QProfileInfo
	FileCountByExt      map[string]int32
}

// BuildMetadata constructs the Metadata protobuf message.
func BuildMetadata(input MetadataInput, rootRef int32) *pb.Metadata {
	scmRevision := randomHex(20)
	if input.ProjectVersion == "" {
		input.ProjectVersion = "1.0.0"
	}

	refBranch := input.ReferenceBranchName
	if refBranch == "" {
		refBranch = input.BranchName
	}

	qprofiles := make(map[string]*pb.Metadata_QProfile, len(input.QProfiles))
	for _, qp := range input.QProfiles {
		rulesUpdated := qp.RulesUpdatedAt.UnixMilli()
		if rulesUpdated <= 0 {
			rulesUpdated = input.AnalysisDate.UnixMilli()
		}
		qprofiles[qp.Language] = &pb.Metadata_QProfile{
			Key:            qp.Key,
			Name:           qp.Name,
			Language:       qp.Language,
			RulesUpdatedAt: rulesUpdated,
		}
	}

	fileCounts := input.FileCountByExt
	if fileCounts == nil {
		fileCounts = make(map[string]int32)
	}

	return &pb.Metadata{
		AnalysisDate:                    input.AnalysisDate.UnixMilli(),
		OrganizationKey:                 input.OrgKey,
		ProjectKey:                      input.ProjectKey,
		RootComponentRef:                rootRef,
		BranchName:                      input.BranchName,
		BranchType:                      input.BranchType,
		ReferenceBranchName:             refBranch,
		ScmRevisionId:                   scmRevision,
		ProjectVersion:                  input.ProjectVersion,
		QprofilesPerLanguage:            qprofiles,
		AnalyzedIndexedFileCountPerType: fileCounts,
	}
}

// ComponentInput holds extracted component data for building Component messages.
type ComponentInput struct {
	Key      string
	Name     string
	Path     string // project-relative path
	Language string
	Lines    int32
}

// BuildComponents creates the project root component and file components.
// Returns the root component, file components, and the ComponentRef tracker.
func BuildComponents(projectKey string, files []ComponentInput) (*pb.Component, []*pb.Component, *ComponentRef) {
	cr := NewComponentRef()
	rootRef := cr.Get(projectKey)

	childRefs := make([]int32, 0, len(files))
	fileComponents := make([]*pb.Component, 0, len(files))

	for _, f := range files {
		ref := cr.Get(f.Key)
		childRefs = append(childRefs, ref)
		fileComponents = append(fileComponents, &pb.Component{
			Ref:                 ref,
			Type:                pb.Component_FILE,
			Language:            sanitizeLanguage(f.Language),
			Lines:               f.Lines,
			Status:              pb.Component_ADDED,
			ProjectRelativePath: f.Path,
			// Key is intentionally omitted for FILE components — only PROJECT gets a key.
			// Setting Key on files causes CE processing errors in SonarCloud.
		})
	}

	root := &pb.Component{
		Ref:      rootRef,
		Type:     pb.Component_PROJECT,
		Key:      projectKey,
		ChildRef: childRefs,
	}

	return root, fileComponents, cr
}

// IssueInput holds extracted issue data for building Issue protobuf messages.
type IssueInput struct {
	Key          string    // original SonarQube issue key — used for BackdateChangesets
	CreationDate time.Time // original creation date — used for BackdateChangesets
	RuleRepo     string
	RuleKey      string
	Message      string
	Severity     string
	StartLine    int32
	EndLine      int32
	StartOff     int32
	EndOff       int32
	Component    string // component key for ref lookup
}

// BuildIssues groups issues by component ref and returns a map of ref->[]Issue.
func BuildIssues(issues []IssueInput, cr *ComponentRef) map[int32][]*pb.Issue {
	result := make(map[int32][]*pb.Issue)
	for _, iss := range issues {
		ref, ok := cr.refs[iss.Component]
		if !ok {
			continue
		}
		pbIssue := &pb.Issue{
			RuleRepository: iss.RuleRepo,
			RuleKey:        iss.RuleKey,
			Msg:            iss.Message,
		}
		if sev := mapSeverity(iss.Severity); sev != pb.Severity_UNSET_SEVERITY {
			pbIssue.OverriddenSeverity = &sev
		}
		if iss.StartLine > 0 {
			pbIssue.TextRange = &pb.TextRange{
				StartLine:   iss.StartLine,
				EndLine:     iss.EndLine,
				StartOffset: iss.StartOff,
				EndOffset:   iss.EndOff,
			}
		}
		result[ref] = append(result[ref], pbIssue)
	}
	return result
}

// ExternalIssueInput holds extracted data for building ExternalIssue protobuf messages.
type ExternalIssueInput struct {
	EngineID  string
	RuleID    string
	Message   string
	Severity  string
	Type      string // CODE_SMELL, BUG, VULNERABILITY
	StartLine int32
	EndLine   int32
	StartOff  int32
	EndOff    int32
	Component string
}

// BuildExternalIssues groups external issues by component ref and returns a map of ref->[]ExternalIssue.
func BuildExternalIssues(issues []ExternalIssueInput, cr *ComponentRef) map[int32][]*pb.ExternalIssue {
	result := make(map[int32][]*pb.ExternalIssue)
	for _, iss := range issues {
		ref, ok := cr.refs[iss.Component]
		if !ok {
			continue
		}
		pbIss := &pb.ExternalIssue{
			EngineId: iss.EngineID,
			RuleId:   iss.RuleID,
			Msg:      iss.Message,
		}
		if sev := mapSeverity(iss.Severity); sev != pb.Severity_UNSET_SEVERITY {
			pbIss.Severity = &sev
		}
		if it := mapIssueType(iss.Type); it != pb.IssueType_ISSUE_TYPE_UNSET {
			pbIss.Type = &it
		}
		if iss.StartLine > 0 {
			pbIss.TextRange = &pb.TextRange{
				StartLine:   iss.StartLine,
				EndLine:     iss.EndLine,
				StartOffset: iss.StartOff,
				EndOffset:   iss.EndOff,
			}
		}
		result[ref] = append(result[ref], pbIss)
	}
	return result
}

// AdHocRuleInput holds data for building AdHocRule messages.
type AdHocRuleInput struct {
	EngineID    string
	RuleID      string
	Name        string
	Description string
	Severity    string
	Type        string
}

// BuildAdHocRules creates AdHocRule protobuf messages from the input slice.
func BuildAdHocRules(rules []AdHocRuleInput) []*pb.AdHocRule {
	result := make([]*pb.AdHocRule, 0, len(rules))
	for _, r := range rules {
		pbr := &pb.AdHocRule{
			EngineId:    r.EngineID,
			RuleId:      r.RuleID,
			Name:        r.Name,
			Description: r.Description,
		}
		if sev := mapSeverity(r.Severity); sev != pb.Severity_UNSET_SEVERITY {
			pbr.Severity = &sev
		}
		if it := mapIssueType(r.Type); it != pb.IssueType_ISSUE_TYPE_UNSET {
			pbr.Type = &it
		}
		result = append(result, pbr)
	}
	return result
}

// MeasureInput holds a metric key-value pair for a component.
type MeasureInput struct {
	Component string
	MetricKey string
	Value     string
}

// BuildMeasures groups measures by component ref and returns a map of ref->[]Measure.
func BuildMeasures(measures []MeasureInput, cr *ComponentRef) map[int32][]*pb.Measure {
	result := make(map[int32][]*pb.Measure)
	for _, m := range measures {
		ref, ok := cr.refs[m.Component]
		if !ok {
			continue
		}
		pbm := buildMeasureValue(m.MetricKey, m.Value)
		if pbm != nil {
			result[ref] = append(result[ref], pbm)
		}
	}
	return result
}

// ActiveRuleInput holds data for building an ActiveRule message.
type ActiveRuleInput struct {
	RuleRepo    string
	RuleKey     string
	Severity    string
	QProfileKey string
	Language    string
}

// BuildActiveRules creates ActiveRule protobuf messages from the input slice.
func BuildActiveRules(rules []ActiveRuleInput) []*pb.ActiveRule {
	result := make([]*pb.ActiveRule, 0, len(rules))
	for _, r := range rules {
		result = append(result, &pb.ActiveRule{
			RuleRepository: r.RuleRepo,
			RuleKey:        r.RuleKey,
			Severity:       mapSeverity(r.Severity),
			QProfileKey:    r.QProfileKey,
		})
	}
	return result
}

// BuildDefaultChangesets creates a Changesets message for a component with
// lineCount lines, all attributed to a single date. Use BackdateChangesets
// to rewrite these with per-issue dates afterward.
func BuildDefaultChangesets(compRef int32, lineCount int, date time.Time) *pb.Changesets {
	dateMs := date.UnixMilli()
	cs := &pb.Changesets{
		ComponentRef: compRef,
		Changeset: []*pb.Changesets_Changeset{{
			Revision: "migration-initial",
			Author:   stubAuthor,
			Date:     dateMs,
		}},
		ChangesetIndexByLine: make([]int32, lineCount),
	}
	return cs
}

// mapSeverity converts a string severity to its protobuf enum value.
func mapSeverity(s string) pb.Severity {
	switch strings.ToUpper(s) {
	case "INFO":
		return pb.Severity_INFO
	case "MINOR":
		return pb.Severity_MINOR
	case "MAJOR":
		return pb.Severity_MAJOR
	case "CRITICAL":
		return pb.Severity_CRITICAL
	case "BLOCKER":
		return pb.Severity_BLOCKER
	default:
		return pb.Severity_UNSET_SEVERITY
	}
}

// mapIssueType converts a string issue type to its protobuf enum value.
func mapIssueType(t string) pb.IssueType {
	switch strings.ToUpper(t) {
	case "CODE_SMELL":
		return pb.IssueType_CODE_SMELL
	case "BUG":
		return pb.IssueType_BUG
	case "VULNERABILITY":
		return pb.IssueType_VULNERABILITY
	case "SECURITY_HOTSPOT":
		return pb.IssueType_SECURITY_HOTSPOT
	default:
		return pb.IssueType_ISSUE_TYPE_UNSET
	}
}

// sanitizeLanguage normalizes language identifiers.
func sanitizeLanguage(lang string) string {
	return strings.ToLower(strings.TrimSpace(lang))
}

// buildMeasureValue creates a Measure with the appropriate typed value.
func buildMeasureValue(metricKey, value string) *pb.Measure {
	m := &pb.Measure{MetricKey: metricKey}

	switch {
	case value == "true" || value == "false":
		m.Value = &pb.Measure_BooleanValue{BooleanValue: &pb.Measure_BoolValue{Value: value == "true"}}
	case isInt(value):
		v, _ := strconv.ParseInt(value, 10, 32)
		m.Value = &pb.Measure_IntValue_{IntValue: &pb.Measure_IntValue{Value: int32(v)}}
	case isFloat(value):
		v, _ := strconv.ParseFloat(value, 64)
		m.Value = &pb.Measure_DoubleValue_{DoubleValue: &pb.Measure_DoubleValue{Value: v}}
	default:
		m.Value = &pb.Measure_StringValue_{StringValue: &pb.Measure_StringValue{Value: value}}
	}
	return m
}

func isInt(s string) bool {
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

func isFloat(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// fallback to timestamp-based ID
		return fmt.Sprintf("migration-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
