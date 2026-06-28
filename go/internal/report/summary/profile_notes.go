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
	type ruleEntry struct{ key, detail string }
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

	out := make(map[string]*profileFindings, len(byProfile))
	for cloudKey, kinds := range byProfile {
		// Instantiated rules (template-instance criterion) always
		// have a custom severity, so they'd also appear under the
		// custom-severity criterion. Suppress them there since the
		// template-instance message already says the rule is not
		// migrated at all — reporting a severity revert for the
		// same rule is misleading.
		if templateEntries, hasTemplate := kinds[kindTemplateInstance]; hasTemplate {
			instantiated := make(map[string]bool, len(templateEntries))
			for _, e := range templateEntries {
				instantiated[e.key] = true
			}
			if csEntries := kinds[kindCustomSeverity]; len(csEntries) > 0 {
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
		}

		var lines []string
		// Render in a stable order — matches the criterion order in
		// the issue so the report reads the same way every time.
		for _, kind := range profileFindingKindOrder {
			entries, ok := kinds[kind]
			if !ok || len(entries) == 0 {
				continue
			}
			// Dedup by rule key — the analyzer can emit several rows
			// per rule (one per parameter for criterion #4, one per
			// source-org-mapped profile when several SQS orgs migrate
			// into the same SQC org). Fold onto a single bullet per
			// rule with deduplicated detail strings inlined.
			sort.SliceStable(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
			byRule := make(map[string][]string)
			seenDetail := make(map[string]map[string]bool)
			var order []string
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
			// Per-criterion rendering: a few criteria collapse to a
			// single sentence with the rule keys comma-separated.
			// custom-severity drops the severity-transition detail
			// (the message itself states the outcome — revert to
			// default), and third-party drops the repo name (the
			// message itself says the rules will be removed).
			// All issue messages here describe what migrate ACTUALLY
			// did to the profile, so they are written in past tense.
			// The predictive report swaps "were X" → "will be X" via
			// toPredictiveTense at render time (#167).
			if kind == kindCustomSeverity {
				lines = append(lines, "Because rules custom severities are not supported in SQC, the following rules were reverted to their default severities: "+strings.Join(order, ", "))
				continue
			}
			if kind == kindThirdParty {
				lines = append(lines, "Because SQC does not support 3rd party plugins, the following 3rd party rules were removed from the quality profile: "+strings.Join(order, ", "))
				continue
			}
			if kind == "prioritized" {
				lines = append(lines, "Since SQC does not support prioritized rules, the following rules were migrated in the profile as regular rules: "+strings.Join(order, ", "))
				continue
			}
			if kind == kindTemplateInstance {
				lines = append(lines, "Because rule templates and instantiated rules are not supported in SQC, the following rules were not migrated: "+strings.Join(order, ", "))
				continue
			}
			if kind == kindCustomParams {
				lines = append(lines, "The following rules custom parameters could not be migrated due to an unexpected error: "+strings.Join(order, ", "))
				continue
			}
			if kind == kindDisabledInherited {
				lines = append(lines, "Since SQC does not support parent profile rules disabled in child profiles, the following rules were enabled in the profile: "+strings.Join(order, ", "))
				continue
			}
			var ruleLines []string
			for _, k := range order {
				if details := byRule[k]; len(details) > 0 {
					ruleLines = append(ruleLines, fmt.Sprintf("%s (%s)", k, strings.Join(details, ", ")))
				} else {
					ruleLines = append(ruleLines, k)
				}
			}
			lines = append(lines, profileFindingKindLabel[kind]+":\n"+strings.Join(ruleLines, "\n"))
		}
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
	"prioritized":        "Prioritized rules (no equivalent on SonarQube Cloud)",
	kindThirdParty:        "Third-party plugin rules (not available on SonarQube Cloud)",
	kindCustomParams:      "Rules with customised parameter values (revert to defaults on SonarQube Cloud)",
	kindTemplateInstance:  "Rule-template instantiations (not migratable to SonarQube Cloud)",
	kindDisabledInherited: "Rules disabled in this child profile but enabled in the parent — will be re-enabled on SonarQube Cloud (no sonar.qualityProfiles.allowDisableInheritedRules)",
}
