package scanreport

import (
	"regexp"
	"strconv"
	"strings"

	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

// This file mirrors CloudVoyager's MQR (multi-quality-rating / clean-code)
// mapping logic so the scanner reports we submit carry the same
// software-quality impacts, clean-code attributes, and effort that
// SonarCloud's Compute Engine requires when registering ad-hoc rules and
// importing external issues. Source of truth:
// CloudVoyager sq-2025/protobuf/build-external-issues/helpers/enum-mappers.js
// (mapSoftwareQuality, mapImpactSeverity, mapOldSeverityToImpact,
// mapCleanCodeAttribute, defaultCleanCodeAttribute, parseEffortToMinutes) and
// build-impacts.js / resolve-clean-code-attr.js.

// ImpactInput is a raw software-quality/severity impact pair carried from the
// extract (api/issues/search "impacts" array) into the protobuf builders.
type ImpactInput struct {
	SoftwareQuality string
	Severity        string
}

// mapSoftwareQuality mirrors CloudVoyager mapSoftwareQuality: it accepts both
// MQR software-quality names and legacy issue types, defaulting to
// MAINTAINABILITY.
func mapSoftwareQuality(s string) pb.SoftwareQuality {
	switch strings.ToUpper(s) {
	case "MAINTAINABILITY", "CODE_SMELL":
		return pb.SoftwareQuality_MAINTAINABILITY
	case "RELIABILITY", "BUG":
		return pb.SoftwareQuality_RELIABILITY
	case "SECURITY", "VULNERABILITY", "SECURITY_HOTSPOT":
		return pb.SoftwareQuality_SECURITY
	default:
		return pb.SoftwareQuality_MAINTAINABILITY
	}
}

// mapImpactSeverity mirrors CloudVoyager mapImpactSeverity (MQR impact
// severities), defaulting to MEDIUM.
func mapImpactSeverity(s string) pb.ImpactSeverity {
	switch strings.ToUpper(s) {
	case "LOW":
		return pb.ImpactSeverity_ImpactSeverity_LOW
	case "MEDIUM":
		return pb.ImpactSeverity_ImpactSeverity_MEDIUM
	case "HIGH":
		return pb.ImpactSeverity_ImpactSeverity_HIGH
	case "INFO":
		return pb.ImpactSeverity_ImpactSeverity_INFO
	case "BLOCKER":
		return pb.ImpactSeverity_ImpactSeverity_BLOCKER
	default:
		return pb.ImpactSeverity_ImpactSeverity_MEDIUM
	}
}

// mapOldSeverityToImpact mirrors CloudVoyager mapOldSeverityToImpact: it maps a
// classic issue severity (INFO/MINOR/MAJOR/CRITICAL/BLOCKER) to an MQR impact
// severity, defaulting to MEDIUM.
func mapOldSeverityToImpact(s string) pb.ImpactSeverity {
	switch strings.ToUpper(s) {
	case "INFO", "MINOR":
		return pb.ImpactSeverity_ImpactSeverity_LOW
	case "MAJOR":
		return pb.ImpactSeverity_ImpactSeverity_MEDIUM
	case "CRITICAL", "BLOCKER":
		return pb.ImpactSeverity_ImpactSeverity_HIGH
	default:
		return pb.ImpactSeverity_ImpactSeverity_MEDIUM
	}
}

// mapCleanCodeAttribute mirrors CloudVoyager mapCleanCodeAttribute: it maps a
// clean-code attribute name to its enum value, defaulting to CONVENTIONAL.
func mapCleanCodeAttribute(s string) pb.CleanCodeAttribute {
	if v, ok := pb.CleanCodeAttribute_value[strings.ToUpper(strings.TrimSpace(s))]; ok && v != 0 {
		return pb.CleanCodeAttribute(v)
	}
	return pb.CleanCodeAttribute_CONVENTIONAL
}

// defaultCleanCodeAttribute mirrors CloudVoyager defaultCleanCodeAttribute:
// CODE_SMELL->CONVENTIONAL, BUG->LOGICAL, VULNERABILITY/SECURITY_HOTSPOT->TRUSTWORTHY.
func defaultCleanCodeAttribute(issueType string) pb.CleanCodeAttribute {
	switch strings.ToUpper(issueType) {
	case "BUG":
		return pb.CleanCodeAttribute_LOGICAL
	case "VULNERABILITY", "SECURITY_HOTSPOT":
		return pb.CleanCodeAttribute_TRUSTWORTHY
	default: // CODE_SMELL and unknown
		return pb.CleanCodeAttribute_CONVENTIONAL
	}
}

// resolveImpacts mirrors CloudVoyager buildImpacts/resolveImpacts: honor the
// issue's extracted impacts, otherwise derive a single impact from the issue
// type + classic severity. Always returns at least one Impact, which the CE
// requires for MQR registration of external issues and ad-hoc rules.
func resolveImpacts(impacts []ImpactInput, issueType, oldSeverity string) []*pb.Impact {
	if len(impacts) > 0 {
		out := make([]*pb.Impact, 0, len(impacts))
		for _, im := range impacts {
			out = append(out, &pb.Impact{
				SoftwareQuality: mapSoftwareQuality(im.SoftwareQuality),
				Severity:        mapImpactSeverity(im.Severity),
			})
		}
		return out
	}
	return []*pb.Impact{{
		SoftwareQuality: mapSoftwareQuality(issueType),
		Severity:        mapOldSeverityToImpact(oldSeverity),
	}}
}

// resolveCleanCodeAttr mirrors CloudVoyager resolveCleanCodeAttr: honor the
// issue's extracted clean-code attribute, otherwise default by type.
func resolveCleanCodeAttr(attr, issueType string) pb.CleanCodeAttribute {
	if strings.TrimSpace(attr) != "" {
		return mapCleanCodeAttribute(attr)
	}
	return defaultCleanCodeAttribute(issueType)
}

var (
	effortHourRe = regexp.MustCompile(`(\d+)h`)
	effortMinRe  = regexp.MustCompile(`(\d+)min`)
)

// parseEffortToMinutes mirrors CloudVoyager parseEffortToMinutes: it parses a
// SonarQube effort/debt string such as "2h", "30min", or "1h30min" into total
// minutes.
func parseEffortToMinutes(effort string) int64 {
	if effort == "" {
		return 0
	}
	var minutes int64
	if m := effortHourRe.FindStringSubmatch(effort); m != nil {
		if h, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			minutes += h * 60
		}
	}
	if m := effortMinRe.FindStringSubmatch(effort); m != nil {
		if mm, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			minutes += mm
		}
	}
	return minutes
}
