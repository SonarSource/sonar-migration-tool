// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package wizard

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// SkippedOrgSentinel marks an organization as skipped during org mapping.
const SkippedOrgSentinel = "SKIPPED"

// phaseSequence is the ordered list of executable wizard phases.
var phaseSequence = []WizardPhase{
	PhaseExtract, PhaseStructure, PhaseOrgMapping,
	PhaseMappings, PhaseValidate, PhaseMigrate,
}

// phaseDisplayNames maps phases to human-readable names.
var phaseDisplayNames = map[WizardPhase]string{
	PhaseInit:       "Start",
	PhaseExtract:    "Extract",
	PhaseStructure:  "Structure",
	PhaseOrgMapping: "Organization Mapping",
	PhaseMappings:   "Mappings",
	PhaseValidate:   "Validate",
	PhaseMigrate:    "Migrate",
	PhaseComplete:   "Complete",
}

// PhaseDisplayName returns the human-readable name for a phase.
func PhaseDisplayName(phase WizardPhase) string {
	if name, ok := phaseDisplayNames[phase]; ok {
		return name
	}
	return string(phase)
}

// PhaseIndex returns the 1-based position of a phase in the sequence.
func PhaseIndex(phase WizardPhase) int {
	for i, p := range phaseSequence {
		if p == phase {
			return i + 1
		}
	}
	return 0
}

// PhaseCount returns the total number of executable phases (7).
func PhaseCount() int {
	return len(phaseSequence)
}

// nextPhase returns the phase after the given one, or PhaseComplete.
func nextPhase(current WizardPhase) WizardPhase {
	for i, p := range phaseSequence {
		if p == current && i+1 < len(phaseSequence) {
			return phaseSequence[i+1]
		}
	}
	return PhaseComplete
}

// phasesUpTo returns phase display names from Extract up to (inclusive) the given phase.
func phasesUpTo(phase WizardPhase) []string {
	var names []string
	for _, p := range phaseSequence {
		names = append(names, PhaseDisplayName(p))
		if p == phase {
			break
		}
	}
	return names
}

// phaseByIndex returns the phase at the given 0-based index in phaseSequence.
func phaseByIndex(idx int) WizardPhase {
	if idx >= 0 && idx < len(phaseSequence) {
		return phaseSequence[idx]
	}
	return PhaseExtract
}

// isSSLError walks the error chain looking for TLS/certificate errors.
func isSSLError(err error) bool {
	if err == nil {
		return false
	}
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &certErr) || errors.As(err, &unknownAuth) {
		return true
	}
	// Check for common TLS error strings in wrapped errors.
	msg := err.Error()
	return strings.Contains(msg, "x509") || strings.Contains(msg, "certificate")
}

// validateServerURL checks that a URL has a valid scheme and hostname.
func validateServerURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("URL cannot be empty")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("URL must use http or https scheme")
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("URL must have a hostname")
	}
	return nil
}

// isLocalhostURL detects loopback addresses (127.0.0.1, localhost, ::1).
func isLocalhostURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// normalizeTrailingSlash ensures a URL ends with "/".
func normalizeTrailingSlash(u string) string {
	if u != "" && !strings.HasSuffix(u, "/") {
		return u + "/"
	}
	return u
}

// checkRequiredFiles returns the names of files that do not exist in dir.
func checkRequiredFiles(dir string, files []string) []string {
	var missing []string
	for _, f := range files {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, f)
		}
	}
	return missing
}

// orgsFromMaps converts LoadCSV map rows back to typed Organization structs.
func orgsFromMaps(rows []map[string]any) []structure.Organization {
	orgs := make([]structure.Organization, 0, len(rows))
	for _, row := range rows {
		org := structure.Organization{
			SonarQubeOrgKey:  mapStr(row, "sonarqube_org_key"),
			SonarCloudOrgKey: mapStr(row, "sonarcloud_org_key"),
			BindingKey:       mapStr(row, "binding_key"),
			ServerURL:        mapStr(row, "server_url"),
			ALM:              mapStr(row, "alm"),
			URL:              mapStr(row, "url"),
			IsCloud:          mapBool(row, "is_cloud"),
			ProjectCount:     mapInt(row, "project_count"),
		}
		orgs = append(orgs, org)
	}
	return orgs
}

func mapStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func mapBool(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func mapInt(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string { return &s }

// strPtrOrNil returns nil for an empty string, else a pointer to it.
// Used for optional WizardState fields (#515) so an unset value
// serializes as JSON null and mergeSeed's non-empty checks stay
// meaningful, instead of persisting an empty-string sentinel.
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ptrStr safely dereferences a string pointer, returning "" for nil.
func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ptrBoolOr safely dereferences a bool pointer, returning def for nil.
func ptrBoolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
