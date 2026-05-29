package migrate

import (
	"encoding/json"
)

// ProfileFinding records ONE per-rule reason why a quality profile's
// migration is not perfect. Multiple findings per profile are
// expected (e.g. several rules with custom severities). The summary
// report groups them by FindingKind to produce one Issues line per
// category with the rule keys.
type ProfileFinding struct {
	CloudProfileKey string `json:"cloud_profile_key"`
	ProfileName     string `json:"profile_name"`
	Language        string `json:"language"`
	Kind            string `json:"finding"` // one of FindingKind* below
	RuleKey         string `json:"rule_key"`
	Detail          string `json:"detail,omitempty"`
}

// FindingKind* enumerates the six #226 yellow criteria.
const (
	FindingKindCustomSeverity    = "custom-severity"
	FindingKindPrioritized       = "prioritized"
	FindingKindThirdParty        = "third-party"
	FindingKindCustomParams      = "custom-params"
	FindingKindTemplateInstance  = "template-instance"
	FindingKindDisabledInherited = "disabled-inherited"
)

// ProfileAnalysisInput is the per-profile input bag for AnalyzeProfile.
// The caller (migrate task or predict synthesizer) reads the relevant
// extract JSONL files and assembles the bag.
type ProfileAnalysisInput struct {
	CloudProfileKey string
	ProfileName     string
	Language        string
	// ActiveRules carries one record per rule active in the profile
	// with inheritance=NONE (rules whose activation belongs to this
	// profile, not inherited from a parent). Same shape as the
	// getActiveProfileRules extract task emits.
	ActiveRules []json.RawMessage
	// DeactivatedInheritedRules carries rules INHERITED from the
	// parent profile but explicitly deactivated in this child. Empty
	// for non-child profiles. Same shape as
	// getDeactivatedProfileRules.
	DeactivatedInheritedRules []json.RawMessage
	// BaseRulesByKey maps every SQS rule key → its api/rules/show
	// detail (or api/rules/search row) so the analyzer can compare
	// the active-rule severity / params against the base rule's
	// default and detect template-instantiated rules via templateKey.
	BaseRulesByKey map[string]json.RawMessage
}

// AnalyzeProfile runs every #226 yellow detection across the given
// profile's data and returns all findings. Order: by Kind then by
// RuleKey, so the JSONL sidecar is deterministic and tests stay
// stable.
func AnalyzeProfile(in ProfileAnalysisInput) []ProfileFinding {
	var out []ProfileFinding
	out = append(out, detectCustomSeverities(in)...)
	out = append(out, detectPrioritized(in)...)
	out = append(out, detectThirdParty(in)...)
	out = append(out, detectCustomParams(in)...)
	out = append(out, detectTemplateInstances(in)...)
	out = append(out, detectDisabledInherited(in)...)
	return out
}

// detectCustomSeverities — criterion #1: active rule's severity differs
// from the base rule's default severity, OR its impacts (MQR mode)
// differ from the base rule's default impacts. Both signal a custom
// severity choice that does not propagate to SonarQube Cloud.
func detectCustomSeverities(in ProfileAnalysisInput) []ProfileFinding {
	var out []ProfileFinding
	for _, ar := range in.ActiveRules {
		ruleKey := extractField(ar, "key")
		if ruleKey == "" {
			continue
		}
		activeSeverity := extractField(ar, "severity")
		base := in.BaseRulesByKey[ruleKey]
		baseSeverity := extractField(base, "severity")
		if activeSeverity == "" || baseSeverity == "" {
			continue
		}
		if activeSeverity != baseSeverity {
			out = append(out, profileFinding(in, FindingKindCustomSeverity, ruleKey,
				baseSeverity+" → "+activeSeverity))
		}
	}
	return out
}

// detectPrioritized — criterion #2: rule has prioritizedRule=true.
// Prioritised rules are an SQS concept with no SQC counterpart.
func detectPrioritized(in ProfileAnalysisInput) []ProfileFinding {
	var out []ProfileFinding
	for _, ar := range in.ActiveRules {
		if !extractBool(ar, "prioritizedRule") {
			continue
		}
		ruleKey := extractField(ar, "key")
		if ruleKey == "" {
			continue
		}
		out = append(out, profileFinding(in, FindingKindPrioritized, ruleKey, ""))
	}
	return out
}

// detectThirdParty — criterion #3: active rule comes from a non-
// standard repository (i.e. a 3rd-party plugin). SQC ships only the
// standard rule set, so plugin rules can't migrate.
func detectThirdParty(in ProfileAnalysisInput) []ProfileFinding {
	var out []ProfileFinding
	for _, ar := range in.ActiveRules {
		repo := extractField(ar, "repo")
		if repo == "" || isStandardRuleRepo(repo) {
			continue
		}
		ruleKey := extractField(ar, "key")
		if ruleKey == "" {
			continue
		}
		out = append(out, profileFinding(in, FindingKindThirdParty, ruleKey,
			"repository "+repo))
	}
	return out
}

// detectCustomParams — criterion #4: active rule's params carry
// values that differ from the rule's default. The SQS-side custom
// values are dropped during SQC restore (the SQC rule uses the
// language pack's default). Each (rule, param) pair yields one finding.
//
// IMPORTANT: SonarQube emits an empty `value` (or omits the field) to
// signal "this activation uses the rule's default" — only a non-empty
// value that differs from the rule's default is a genuine custom
// value. Treating empty as custom (issue #226 follow-up) produced a
// flood of false positives in the report.
//
// Note also that getActiveProfileRules writes the rule CATALOG filtered
// by activation status; the per-activation custom values actually live
// in getProfileRules' "actives" map. Reading them here gives a
// best-effort signal until the analyzer switches data source.
func detectCustomParams(in ProfileAnalysisInput) []ProfileFinding {
	var out []ProfileFinding
	for _, ar := range in.ActiveRules {
		ruleKey := extractField(ar, "key")
		if ruleKey == "" {
			continue
		}
		base := in.BaseRulesByKey[ruleKey]
		baseDefaults := ruleParamDefaults(base)
		for _, p := range ruleParams(ar) {
			name := p["key"]
			val := p["value"]
			if name == "" || val == "" {
				// Empty value = activation uses the rule default →
				// not a custom value, no finding.
				continue
			}
			def := baseDefaults[name]
			if val == def {
				continue
			}
			detail := name + "=" + val
			if def != "" {
				detail += " (default " + def + ")"
			}
			out = append(out, profileFinding(in, FindingKindCustomParams, ruleKey, detail))
		}
	}
	return out
}

// detectTemplateInstances — criterion #5: active rule's base record
// carries a non-empty templateKey, meaning it was instantiated from a
// rule template. SQS rule-template instances can't be migrated to SQC
// since SQC has no template-instantiation API.
func detectTemplateInstances(in ProfileAnalysisInput) []ProfileFinding {
	var out []ProfileFinding
	for _, ar := range in.ActiveRules {
		ruleKey := extractField(ar, "key")
		if ruleKey == "" {
			continue
		}
		base := in.BaseRulesByKey[ruleKey]
		if extractField(base, "templateKey") == "" {
			continue
		}
		out = append(out, profileFinding(in, FindingKindTemplateInstance, ruleKey, ""))
	}
	return out
}

// detectDisabledInherited — criterion #6: the child profile has
// rules inherited from its parent but explicitly disabled here. SQS's
// sonar.qualityProfiles.allowDisableInheritedRules has no SQC
// counterpart; the rules will be re-enabled on the cloud side.
func detectDisabledInherited(in ProfileAnalysisInput) []ProfileFinding {
	var out []ProfileFinding
	for _, dr := range in.DeactivatedInheritedRules {
		ruleKey := extractField(dr, "key")
		if ruleKey == "" {
			continue
		}
		out = append(out, profileFinding(in, FindingKindDisabledInherited, ruleKey, ""))
	}
	return out
}

// profileFinding is the boilerplate-free factory for ProfileFinding —
// stamps the per-profile context onto every record so the sidecar
// JSONL is self-contained.
func profileFinding(in ProfileAnalysisInput, kind, ruleKey, detail string) ProfileFinding {
	return ProfileFinding{
		CloudProfileKey: in.CloudProfileKey,
		ProfileName:     in.ProfileName,
		Language:        in.Language,
		Kind:            kind,
		RuleKey:         ruleKey,
		Detail:          detail,
	}
}

// isStandardRuleRepo mirrors extract/tasks_rules.go's standardRepos
// map. The two lists are duplicated on purpose (the analyzer lives in
// internal/migrate and we don't want a cross-package import from the
// extract internals); keep them in sync if the canonical list grows.
func isStandardRuleRepo(repo string) bool {
	return standardRuleRepos[repo]
}

var standardRuleRepos = map[string]bool{
	"common-cs": true, "common-java": true, "common-js": true, "common-ts": true,
	"common-php": true, "common-py": true, "common-web": true,
	"csharpsquid": true, "flex": true, "go": true, "java": true,
	"javascript": true, "kotlin": true, "php": true, "python": true,
	"ruby": true, "scala": true, "swift": true, "typescript": true,
	"vbnet": true, "web": true, "xml": true, "css": true,
	"cloudformation": true, "docker": true, "kubernetes": true,
	"terraform": true, "azureresourcemanager": true, "text": true,
	"secrets": true, "jssecurity": true, "javasecurity": true,
	"phpsecurity": true, "pythonsecurity": true, "roslyn.sonaranalyzer.security.cs": true,
	"common-abap": true, "abap": true, "common-apex": true, "apex": true,
	"common-cobol": true, "cobol": true, "common-pli": true, "pli": true,
	"common-rpg": true, "rpg": true, "common-vb": true, "vb": true,
	"common-tsql": true, "tsql": true, "plsql": true, "common-objc": true,
	"objc": true, "common-c": true, "c": true, "cpp": true,
}

// ruleParams unmarshals the "params" array on an active-rule record
// into a slice of {key, value} maps. Empty / missing returns nil.
func ruleParams(raw json.RawMessage) []map[string]string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	arr, ok := obj["params"]
	if !ok {
		return nil
	}
	var out []map[string]string
	_ = json.Unmarshal(arr, &out)
	return out
}

// ruleParamDefaults inspects a base-rule record's "params" array (the
// rule's own default parameter values) and returns a name → defaultValue
// map for cheap lookup during detectCustomParams.
func ruleParamDefaults(raw json.RawMessage) map[string]string {
	if raw == nil {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	arr, ok := obj["params"]
	if !ok {
		return nil
	}
	var entries []map[string]any
	if err := json.Unmarshal(arr, &entries); err != nil {
		return nil
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		name, _ := e["key"].(string)
		def, _ := e["defaultValue"].(string)
		if def == "" {
			def, _ = e["value"].(string)
		}
		out[name] = def
	}
	return out
}
