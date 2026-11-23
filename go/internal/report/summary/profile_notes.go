// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// profileFinding mirrors migrate.ProfileFinding for the read-side.
// Keeping a separate copy avoids a summary → migrate import flip
// while the field set is small enough to be redundant.
type profileFinding struct {
	CloudProfileKey string `json:"cloud_profile_key"`
	ProfileName     string `json:"profile_name"`
	Language        string `json:"language"`
	Kind            string `json:"finding"`
	RuleKey         string `json:"rule_key"`
	Detail          string `json:"detail,omitempty"`
}

// profileFindings groups the per-rule findings emitted by the
// analyzeProfileRules task into one human-readable Issues list per
// quality profile. The outer map is keyed by cloud_profile_key.
//
// HasOrangeCriterion is true when at least one finding belongs to a
// criterion treated as Partial (orange) rather than NearPerfect
// (yellow) — currently just kindThirdParty rules. Those represent a
// real loss of coverage on SQC (rules dropped from the profile), so
// the QP should be classified Partial.
type profileFindings struct {
	Issues             []string
	HasOrangeCriterion bool
}

// ruleEntry pairs a rule key with its (possibly empty) detail string —
// the unit collectProfileFindings buckets per (cloud_profile_key, kind).
type ruleEntry struct{ key, detail string }

// collectProfileFindings reads analyzeProfileRules JSONL and folds
// the per-rule rows into one Issues list per cloud_profile_key. Each
// of the six #226 yellow criteria becomes a single bullet, prefixed
// by a human-readable label and followed by the rule-key list (and
// optional per-rule details).
func collectProfileFindings(store *common.DataStore) map[string]*profileFindings {
	items, err := store.ReadAll("analyzeProfileRules")
	if err != nil || len(items) == 0 {
		return nil
	}

	// Bucket findings: cloud_profile_key → kind → []rule-with-detail.
	byProfile := bucketProfileFindings(items)

	out := make(map[string]*profileFindings, len(byProfile))
	for cloudKey, kinds := range byProfile {
		// Instantiated rules (template-instance criterion) always
		// have a custom severity, so they'd also appear under the
		// custom-severity criterion. Suppress them there since the
		// template-instance message already says the rule is not
		// migrated at all — reporting a severity revert for the
		// same rule is misleading.
		suppressTemplateInstanceDuplicates(kinds)

		// Render in a stable order — matches the criterion order in
		// the issue so the report reads the same way every time.
		lines := buildProfileLines(kinds)
		if len(lines) == 0 {
			continue
		}
		_, hasOrange := kinds[kindThirdParty]
		out[cloudKey] = &profileFindings{
			Issues:             lines,
			HasOrangeCriterion: hasOrange,
		}
	}
	return out
}

// bucketProfileFindings groups analyzeProfileRules rows into
// cloud_profile_key → kind → []rule-with-detail, dropping rows missing
// any of the three required fields.
func bucketProfileFindings(items []json.RawMessage) map[string]map[string][]ruleEntry {
	byProfile := make(map[string]map[string][]ruleEntry)
	for _, raw := range items {
		var f profileFinding
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}
		if f.CloudProfileKey == "" || f.Kind == "" || f.RuleKey == "" {
			continue
		}
		if byProfile[f.CloudProfileKey] == nil {
			byProfile[f.CloudProfileKey] = make(map[string][]ruleEntry)
		}
		byProfile[f.CloudProfileKey][f.Kind] = append(
			byProfile[f.CloudProfileKey][f.Kind],
			ruleEntry{key: f.RuleKey, detail: f.Detail},
		)
	}
	return byProfile
}

// suppressTemplateInstanceDuplicates drops, from kindCustomSeverity, any
// rule key that also appears under kindTemplateInstance: instantiated
// rules always carry a custom severity, so they'd otherwise double up —
// the template-instance message already says the rule is not migrated at
// all, making a severity-revert note for the same rule misleading.
func suppressTemplateInstanceDuplicates(kinds map[string][]ruleEntry) {
	templateEntries, hasTemplate := kinds[kindTemplateInstance]
	if !hasTemplate {
		return
	}
	instantiated := make(map[string]bool, len(templateEntries))
	for _, e := range templateEntries {
		instantiated[e.key] = true
	}
	csEntries := kinds[kindCustomSeverity]
	if len(csEntries) == 0 {
		return
	}
	filtered := csEntries[:0]
	for _, e := range csEntries {
		if instantiated[e.key] {
			continue
		}
		filtered = append(filtered, e)
	}
	if len(filtered) == 0 {
		delete(kinds, kindCustomSeverity)
	} else {
		kinds[kindCustomSeverity] = filtered
	}
}

// buildProfileLines renders one Issues bullet per non-empty criterion in
// kinds, in the fixed profileFindingKindOrder so the Issues column reads
// consistently across reports.
func buildProfileLines(kinds map[string][]ruleEntry) []string {
	var lines []string
	for _, kind := range profileFindingKindOrder {
		entries, ok := kinds[kind]
		if !ok || len(entries) == 0 {
			continue
		}
		lines = append(lines, renderProfileFindingLine(kind, entries))
	}
	return lines
}

// dedupRuleEntries sorts entries by rule key and folds them onto one
// ordered list of distinct rule keys plus their deduplicated detail
// strings. The analyzer can emit several rows per rule (one per parameter
// for criterion #4, one per source-org-mapped profile when several SQS
// orgs migrate into the same SQC org).
func dedupRuleEntries(entries []ruleEntry) (order []string, byRule map[string][]string) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	byRule = make(map[string][]string)
	seenDetail := make(map[string]map[string]bool)
	for _, e := range entries {
		if _, seen := byRule[e.key]; !seen {
			order = append(order, e.key)
			byRule[e.key] = nil
			seenDetail[e.key] = make(map[string]bool)
		}
		if e.detail == "" || seenDetail[e.key][e.detail] {
			continue
		}
		seenDetail[e.key][e.detail] = true
		byRule[e.key] = append(byRule[e.key], e.detail)
	}
	return order, byRule
}

// renderProfileFindingLine renders the single Issues bullet for one
// criterion. A few criteria collapse to a fixed sentence with the rule
// keys comma-separated (see profileFindingKindMessage); the rest render
// one line per rule, with deduplicated detail strings inlined in
// parentheses. All messages describe what migrate ACTUALLY did to the
// profile, so they are written in past tense — the predictive report
// swaps "were X" → "will be X" via toPredictiveTense at render time
// (#167).
func renderProfileFindingLine(kind string, entries []ruleEntry) string {
	order, byRule := dedupRuleEntries(entries)
	if msg := profileFindingKindMessage(kind, order); msg != "" {
		return msg
	}
	var ruleLines []string
	for _, k := range order {
		if details := byRule[k]; len(details) > 0 {
			ruleLines = append(ruleLines, fmt.Sprintf("%s (%s)", k, strings.Join(details, ", ")))
		} else {
			ruleLines = append(ruleLines, k)
		}
	}
	return profileFindingKindLabel[kind] + ":\n" + strings.Join(ruleLines, "\n")
}

// profileFindingKindMessage returns the fixed single-sentence bullet for
// the criteria that collapse to one message instead of a per-rule list,
// or "" for the criteria that render per-rule via renderProfileFindingLine.
// custom-severity drops the severity-transition detail (the message
// itself states the outcome — revert to default), and third-party drops
// the repo name (the message itself says the rules will be removed).
func profileFindingKindMessage(kind string, order []string) string {
	joined := strings.Join(order, ", ")
	switch kind {
	case kindCustomSeverity:
		return "Because rules custom severities are not supported in SQC, the following rules were reverted to their default severities: " + joined
	case kindThirdParty:
		return "Because SQC does not support 3rd party plugins, the following 3rd party rules were removed from the quality profile: " + joined
	case "prioritized":
		return "Since SQC does not support prioritized rules, the following rules were migrated in the profile as regular rules: " + joined
	case kindTemplateInstance:
		return "Because rule templates and instantiated rules are not supported in SQC, the following rules were not migrated: " + joined
	case kindCustomParams:
		return "The following rules custom parameters could not be migrated due to an unexpected error: " + joined
	case kindDisabledInherited:
		return "Since SQC does not support parent profile rules disabled in child profiles, the following rules were enabled in the profile: " + joined
	default:
		return ""
	}
}

// applyProfileFindings moves Quality Profile entries from Succeeded
// into NearPerfect (#226 yellow), attaching the per-profile Issues
// list collected above. Profiles already in Partial — orange wins
// over yellow per the dominance rule in #224 / #227 — keep their
// classification but absorb the yellow Issues so the operator still
// sees the rule listings.
func applyProfileFindings(succeeded, nearPerfect, partial []EntityItem, findings map[string]*profileFindings) ([]EntityItem, []EntityItem, []EntityItem) {
	if len(findings) == 0 || (len(succeeded) == 0 && len(partial) == 0) {
		return succeeded, nearPerfect, partial
	}

	// Index Partial by Detail (cloud_profile_key) so we can extend
	// in place when an orange-classified profile also has yellow findings.
	partialIdx := make(map[string]int, len(partial))
	for i, item := range partial {
		if item.Detail != "" {
			partialIdx[item.Detail] = i
		}
	}

	keep := succeeded[:0:0]
	for _, item := range succeeded {
		f, ok := findings[item.Detail]
		if !ok {
			keep = append(keep, item)
			continue
		}
		moved := EntityItem{
			Name:         item.Name,
			Language:     item.Language,
			Organization: item.Organization,
			Detail:       item.Detail,
			Issues:       append([]string(nil), f.Issues...),
		}
		// 3rd-party rules (and any other future orange criteria) are
		// a real loss of coverage on SQC — route the QP into Partial
		// rather than NearPerfect. All other yellow criteria stay
		// NearPerfect.
		if f.HasOrangeCriterion {
			partial = append(partial, moved)
		} else {
			nearPerfect = append(nearPerfect, moved)
		}
	}

	// Extend Partial entries with the yellow findings (orange dominates).
	for cloudKey, f := range findings {
		if idx, ok := partialIdx[cloudKey]; ok {
			partial[idx].Issues = append(partial[idx].Issues, f.Issues...)
		}
	}
	return keep, nearPerfect, partial
}

// builtInProfileDiff mirrors migrate.BuiltInProfileDiff for the read-side
// (#309). Keeping a separate copy avoids a summary → migrate import flip,
// same rationale as profileFinding above.
type builtInProfileDiff struct {
	ServerURL    string `json:"server_url"`
	Name         string `json:"name"`
	Language     string `json:"language"`
	RulesAdded   int    `json:"rules_added"`
	RulesRemoved int    `json:"rules_removed"`
}

// readBuiltInProfileDiffs reads the compareBuiltInProfiles JSONL sidecar
// (only ever written by a real migrate run — never by the predictive
// report's synthetic run directory) and keys each record the same way
// collectExtractSkipped's own dedup key is built: serverURL+"|"+name+"|"+language.
func readBuiltInProfileDiffs(store *common.DataStore) map[string]builtInProfileDiff {
	items, err := store.ReadAll("compareBuiltInProfiles")
	if err != nil || len(items) == 0 {
		return nil
	}
	out := make(map[string]builtInProfileDiff, len(items))
	for _, raw := range items {
		var d builtInProfileDiff
		if err := json.Unmarshal(raw, &d); err != nil {
			continue
		}
		if d.Name == "" || d.Language == "" {
			continue
		}
		out[d.ServerURL+"|"+d.Name+"|"+d.Language] = d
	}
	return out
}

// formatBuiltInProfileDiff renders the rules-added/removed counts for a
// built-in quality profile's Detail column (#309): empty when the source
// and target rule sets are identical, and never mentions a zero count.
func formatBuiltInProfileDiff(added, removed int) string {
	switch {
	case added == 0 && removed == 0:
		return ""
	case removed == 0:
		return fmt.Sprintf("%d rule(s) added", added)
	case added == 0:
		return fmt.Sprintf("%d rule(s) removed", removed)
	default:
		return fmt.Sprintf("%d rule(s) added, %d rule(s) removed", added, removed)
	}
}

// Profile finding kind constants used throughout profile_notes.go.
const (
	kindTemplateInstance  = "template-instance"
	kindCustomSeverity    = "custom-severity"
	kindThirdParty        = "third-party"
	kindCustomParams      = "custom-params"
	kindDisabledInherited = "disabled-inherited"
)

// profileFindingKindOrder fixes the rendering order of the six
// criteria so the Issues column reads consistently across reports.
var profileFindingKindOrder = []string{
	kindCustomSeverity,
	"prioritized",
	kindThirdParty,
	kindCustomParams,
	kindTemplateInstance,
	kindDisabledInherited,
}

// profileFindingKindLabel is the bullet header for each criterion —
// short enough to fit at the head of a multi-line Detail block.
var profileFindingKindLabel = map[string]string{
	kindCustomSeverity:    "Rules with custom severities (not portable to SonarQube Cloud)",
	"prioritized":         "Prioritized rules (no equivalent on SonarQube Cloud)",
	kindThirdParty:        "Third-party plugin rules (not available on SonarQube Cloud)",
	kindCustomParams:      "Rules with customised parameter values (revert to defaults on SonarQube Cloud)",
	kindTemplateInstance:  "Rule-template instantiations (not migratable to SonarQube Cloud)",
	kindDisabledInherited: "Rules disabled in this child profile but enabled in the parent — will be re-enabled on SonarQube Cloud (no sonar.qualityProfiles.allowDisableInheritedRules)",
}
