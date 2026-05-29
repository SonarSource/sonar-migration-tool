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

// profileFindingsByGate groups the per-rule findings emitted by the
// analyzeProfileRules task into one human-readable Issues list per
// quality profile. The outer map is keyed by cloud_profile_key.
type profileFindings struct {
	Issues []string
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
		var lines []string
		// Render in a stable order — matches the criterion order in
		// the issue so the report reads the same way every time.
		for _, kind := range profileFindingKindOrder {
			entries, ok := kinds[kind]
			if !ok || len(entries) == 0 {
				continue
			}
			// Dedup by rule key — the analyzer can emit several rows
			// per rule (one per parameter for criterion #4); fold them
			// onto a single bullet per rule with all details inlined.
			sort.SliceStable(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
			byRule := make(map[string][]string)
			var order []string
			for _, e := range entries {
				if _, seen := byRule[e.key]; !seen {
					order = append(order, e.key)
				}
				if e.detail != "" {
					byRule[e.key] = append(byRule[e.key], e.detail)
				} else {
					byRule[e.key] = byRule[e.key] // ensure key present
				}
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
		out[cloudKey] = &profileFindings{Issues: lines}
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
		nearPerfect = append(nearPerfect, moved)
	}

	// Extend Partial entries with the yellow findings (orange dominates).
	for cloudKey, f := range findings {
		if idx, ok := partialIdx[cloudKey]; ok {
			partial[idx].Issues = append(partial[idx].Issues, f.Issues...)
		}
	}
	return keep, nearPerfect, partial
}

// profileFindingKindOrder fixes the rendering order of the six
// criteria so the Issues column reads consistently across reports.
var profileFindingKindOrder = []string{
	"custom-severity",
	"prioritized",
	"third-party",
	"custom-params",
	"template-instance",
	"disabled-inherited",
}

// profileFindingKindLabel is the bullet header for each criterion —
// short enough to fit at the head of a multi-line Detail block.
var profileFindingKindLabel = map[string]string{
	"custom-severity":    "Rules with custom severities (not portable to SonarQube Cloud)",
	"prioritized":        "Prioritized rules (no equivalent on SonarQube Cloud)",
	"third-party":        "Third-party plugin rules (not available on SonarQube Cloud)",
	"custom-params":      "Rules with customised parameter values (revert to defaults on SonarQube Cloud)",
	"template-instance":  "Rule-template instantiations (not migratable to SonarQube Cloud)",
	"disabled-inherited": "Rules disabled in this child profile but enabled in the parent — will be re-enabled on SonarQube Cloud (no sonar.qualityProfiles.allowDisableInheritedRules)",
}
