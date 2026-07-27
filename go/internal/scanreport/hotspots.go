package scanreport

import (
	"strings"
	"time"
)

// Security Hotspots on SonarQube Cloud
//
// SonarQube Cloud dropped Security Hotspots as a distinct finding kind on
// 2026-07-01. The former hotspot rules were converted in place into ordinary
// issue rules: e.g. python:S4502 is now type VULNERABILITY with clean-code
// attribute CONVENTIONAL and a SECURITY/HIGH impact, and carries the
// SonarSource system tag "former-hotspot". The conversion is not uniform —
// some rules became VULNERABILITY, others CODE_SMELL — and a handful of
// long-deprecated hotspot rules were retired outright (status REMOVED) rather
// than converted.
//
// SonarQube Server (the migration source) still has hotspots, so the migration
// tool has to bridge the two models. It does so the way the real scanner does:
// by reporting a rule key and letting the server classify the finding.
//
// The native Issue protobuf message has no type field at all — the scanner
// never tells the server what kind of finding something is. Type, clean-code
// attribute, software qualities and impact severity are all derived
// server-side from the rule's own definition in the target's rule repository.
// Mimicking the scanner therefore means:
//
//   - emit a plain native Issue naming the (repo, rule) pair, and
//   - do NOT stamp a type, an impact, or a severity override.
//
// Cloud then classifies the finding exactly as it would classify the same rule
// raised by a real scan. Anything else would fabricate a finding shape the
// rule could never actually produce.
//
// The review state (TO_REVIEW / REVIEWED + SAFE|FIXED|ACKNOWLEDGED) is not
// expressible in the report either — it is triage, not analysis — so it is
// mapped onto the equivalent issue status and applied after import by the
// metadata-sync phase, alongside the HotspotIssueTag marker.

// HotspotIssueTag is applied to every issue migrated from a SonarQube Server
// Security Hotspot, so that findings which were hotspots on the source remain
// identifiable on a target where hotspots no longer exist.
//
// This is the migration tool's own marker and is deliberately distinct from
// SonarSource's "former-hotspot" system tag, which marks the converted *rule*
// rather than an individual migrated finding.
const HotspotIssueTag = "sqs-hotspot"

// Hotspot review statuses and resolutions as reported by SonarQube Server's
// /api/hotspots/search.
const (
	HotspotStatusToReview = "TO_REVIEW"
	HotspotStatusReviewed = "REVIEWED"

	HotspotResolutionSafe         = "SAFE"
	HotspotResolutionFixed        = "FIXED"
	HotspotResolutionAcknowledged = "ACKNOWLEDGED"
)

// Issue statuses in SonarQube's unified (MQR) issue-status enum, which is what
// hotspot review states are mapped onto.
const (
	IssueStatusOpen          = "OPEN"
	IssueStatusAccepted      = "ACCEPTED"
	IssueStatusFalsePositive = "FALSE_POSITIVE"
)

// HotspotInput holds a Security Hotspot extracted from SonarQube Server,
// before conversion into an issue for the scanner report.
type HotspotInput struct {
	Key          string    // original hotspot key, for backdating and source links
	CreationDate time.Time // original creation date, preserved via changeset backdating
	RuleRepo     string
	RuleKey      string
	Message      string
	Component    string

	// VulnerabilityProbability is the hotspot's HIGH/MEDIUM/LOW review
	// priority. It is retained for reporting only: it is deliberately NOT
	// mapped onto an issue severity override, because the converted rule on
	// Cloud already carries its own impact severity and the real scanner does
	// not override it.
	VulnerabilityProbability string

	// Status and Resolution carry the hotspot's review state, used to derive
	// the target issue's triage state after import.
	Status     string
	Resolution string

	StartLine int32
	EndLine   int32
	StartOff  int32
	EndOff    int32
}

// ConvertHotspotToIssue converts one Security Hotspot into the native issue
// the scanner would have reported for the same rule.
//
// Only the fields a real scanner reports are populated: the rule coordinates,
// the message and the text range. Severity is left empty so that BuildIssues
// emits no overridden_severity and Cloud applies the rule's own severity.
// Key and CreationDate are carried through so BackdateChangesets can restore
// the original creation date.
func ConvertHotspotToIssue(h HotspotInput) IssueInput {
	startLine, endLine := h.StartLine, h.EndLine
	if endLine < startLine {
		endLine = startLine
	}
	startOff, endOff := h.StartOff, h.EndOff
	// A text range is only meaningful with a line. Offsets without a line
	// would be rejected by the CE, so drop them together.
	if startLine <= 0 {
		startLine, endLine, startOff, endOff = 0, 0, 0, 0
	}

	return IssueInput{
		Key:          h.Key,
		CreationDate: h.CreationDate,
		RuleRepo:     h.RuleRepo,
		RuleKey:      h.RuleKey,
		Message:      h.Message,
		// Severity intentionally left empty — see the package comment above.
		Severity:  "",
		StartLine: startLine,
		EndLine:   endLine,
		StartOff:  startOff,
		EndOff:    endOff,
		Component: h.Component,
	}
}

// ConvertHotspotsToIssues converts a batch of hotspots into native issues.
// Hotspots with no rule key are skipped: a finding whose rule cannot be
// identified cannot be recreated on the target, and an issue naming an empty
// rule would be rejected by the Compute Engine.
func ConvertHotspotsToIssues(hotspots []HotspotInput) (issues []IssueInput, skipped int) {
	issues = make([]IssueInput, 0, len(hotspots))
	for _, h := range hotspots {
		if h.RuleRepo == "" || h.RuleKey == "" {
			skipped++
			continue
		}
		issues = append(issues, ConvertHotspotToIssue(h))
	}
	return issues, skipped
}

// HotspotIssueStatus maps a hotspot's review state onto the equivalent
// SonarQube unified issue status.
//
// No official SonarSource mapping was published for this, so the tool defines
// one, guided by what each hotspot resolution asserts about the finding:
//
//	TO_REVIEW               -> OPEN            still needs review
//	REVIEWED + SAFE         -> FALSE_POSITIVE  reviewed; there is no risk
//	REVIEWED + FIXED        -> ACCEPTED        risk was real and was addressed
//	REVIEWED + ACKNOWLEDGED -> ACCEPTED        risk is real, accepted as-is
//
// A REVIEWED hotspot with an unrecognised resolution maps to ACCEPTED, which
// preserves the fact that somebody triaged it rather than silently reopening
// it.
//
// Mapping ACKNOWLEDGED onto ACCEPTED is a fidelity gain over the previous
// hotspot-to-hotspot sync: Cloud's hotspot API had no ACKNOWLEDGED resolution,
// so ACKNOWLEDGED used to be downgraded to SAFE. The issue model can represent
// the distinction, so it is no longer lost.
func HotspotIssueStatus(status, resolution string) string {
	if !strings.EqualFold(strings.TrimSpace(status), HotspotStatusReviewed) {
		// TO_REVIEW, or anything unrecognised: leave the issue open rather
		// than inventing a triage decision.
		return IssueStatusOpen
	}
	switch strings.ToUpper(strings.TrimSpace(resolution)) {
	case HotspotResolutionSafe:
		return IssueStatusFalsePositive
	case HotspotResolutionFixed, HotspotResolutionAcknowledged:
		return IssueStatusAccepted
	default:
		return IssueStatusAccepted
	}
}

// HotspotNeedsTriage reports whether a hotspot's review state differs from the
// default state a freshly imported issue lands in. Only these hotspots need a
// status transition on the target; TO_REVIEW ones are already correct.
func HotspotNeedsTriage(status, resolution string) bool {
	return HotspotIssueStatus(status, resolution) != IssueStatusOpen
}
