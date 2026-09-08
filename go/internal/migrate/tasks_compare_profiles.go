// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// BuiltInProfileDiff is the JSONL record compareBuiltInProfiles emits for
// each source built-in quality profile it was able to match against a
// target built-in profile. The summary report joins these back onto the
// existing "Built-in, not migrated" skip row (issue #309) by
// (ServerURL, Name, Language) — the same key it already dedups extract
// data on for the Quality Profiles section.
type BuiltInProfileDiff struct {
	ServerURL    string `json:"server_url"`
	Name         string `json:"name"`
	Language     string `json:"language"`
	RulesAdded   int    `json:"rules_added"`
	RulesRemoved int    `json:"rules_removed"`
}

// compareTasks returns the task that diffs source vs target built-in
// quality profiles' active rule sets (#309).
func compareTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "compareBuiltInProfiles",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runCompareBuiltInProfiles,
		},
	}
}

// runCompareBuiltInProfiles walks every source built-in quality profile
// (e.g. "Sonar way") and, for each one, live-fetches the matching
// built-in profile's active rules on the target organization to compute
// how many rules were added/removed on SQC relative to the source. Built-
// in profiles are never migrated (structure.addProfile drops them before
// profiles.csv is generated), so this is the only place that ever looks
// at their target-side rule set.
//
// Unlike runAnalyzeProfileRules, this task DOES call the live Cloud API —
// it needs the target's actual rule set, which extract data can't provide.
// That also means it only ever runs during a real migrate: the predictive
// report's synthetic run directory never synthesizes this task's output,
// so collectExtractSkipped simply finds no sidecar and keeps the static
// "Built-in, not migrated" text for predictive runs.
func runCompareBuiltInProfiles(ctx context.Context, e *Executor) error {
	orgKeys := firstValidOrgPerServer(e)
	activeByProfile := indexExtractByServerAndField(e, "getActiveProfileRules", "profileKey")

	counter := TaskCounterFromContext(ctx)
	// One Search per target org, not one per source built-in profile.
	builtInKeys := map[string]string{} // orgKey\x00lang\x00name -> target profile key
	searched := map[string]bool{}
	var mu sync.Mutex
	return forEachExtractItem(ctx, e, "compareBuiltInProfiles", "getProfiles",
		func(ctx context.Context, item structure.ExtractItem, w *common.ChunkWriter) error {
			if !extractBool(item.Data, "isBuiltIn") {
				return nil
			}
			name := extractField(item.Data, "name")
			language := extractField(item.Data, "language")
			profileKey := extractField(item.Data, "key")
			orgKey := orgKeys[item.ServerURL]
			if name == "" || language == "" || profileKey == "" || orgKey == "" {
				return nil
			}

			mu.Lock()
			if !searched[orgKey] {
				searched[orgKey] = true
				if targetProfiles, err := e.Cloud.QualityProfiles.Search(ctx, orgKey); err != nil {
					failAPI(counter, e.Logger, "compareBuiltInProfiles: searching target profiles failed", err,
						"organization", orgKey)
				} else {
					for _, p := range targetProfiles {
						// Match on name AND language, not language alone: a
						// platform can ship more than one built-in profile per
						// language (e.g. "Sonar way" and "Sonar agentic AI"
						// both built-in for java). Matching by language only
						// would pair every source built-in of that language
						// against whichever built-in profile happened to come
						// first in the target's search results.
						if isBuiltInProfile(p) {
							builtInKeys[orgKey+"\x00"+strings.ToLower(p.Language)+"\x00"+strings.ToLower(p.Name)] = p.Key
						}
					}
				}
			}
			targetKey := builtInKeys[orgKey+"\x00"+strings.ToLower(language)+"\x00"+strings.ToLower(name)]
			mu.Unlock()
			if targetKey == "" {
				// No matching built-in profile on the target with this
				// exact name — leave the row on its default text.
				return nil
			}

			targetItems, err := e.Raw.GetPaginated(ctx, common.PaginatedOpts{
				// SonarQube Cloud's api/rules/search responds with a
				// top-level "total" field and no "paging" object at all
				// (unlike SonarQube Server, which nests it under
				// "paging.total" too — the default TotalKey). Without
				// this override, ExtractTotal silently reads 0 from the
				// Cloud response, TotalPages then also returns 0, and
				// GetPaginated stops after page 1 — truncating any
				// profile with more active rules than one page (500).
				Path: "api/rules/search", ResultKey: "rules", TotalKey: "total", MaxPageSize: 500,
				Params: url.Values{
					"inheritance": {"NONE"},
					"qprofile":    {targetKey},
					"activation":  {"true"},
				},
			})
			if err != nil {
				failAPI(counter, e.Logger, "compareBuiltInProfiles: fetching target rules failed", err,
					"organization", orgKey, "language", language)
				return nil
			}

			sourceRules := ruleKeySet(activeByProfile[item.ServerURL+"\x00"+profileKey])
			targetRules := ruleKeySet(targetItems)
			added, removed := diffRuleKeySets(sourceRules, targetRules)

			b, mErr := json.Marshal(BuiltInProfileDiff{
				ServerURL:    item.ServerURL,
				Name:         name,
				Language:     language,
				RulesAdded:   added,
				RulesRemoved: removed,
			})
			if mErr != nil {
				return nil
			}
			counter.Success()
			return w.WriteOne(b)
		})
}

// firstValidOrgPerServer maps each source server URL to the first target
// organization it's bound to that actually has a SonarQube Cloud
// counterpart. Unlike buildServerOrgLookup — which blindly keeps the LAST
// generateOrganizationMappings row per server, including rows with an
// empty or "SKIPPED" sonarcloud_org_key from an unbound ALM binding — this
// is safe when one source server fans out into several target
// organizations (a common consolidation setup): the built-in profile's
// rule content is identical across every org on the same target
// platform/edition, so any one bound org is representative for the diff.
func firstValidOrgPerServer(e *Executor) map[string]string {
	orgItems, _ := e.Store.ReadAll("generateOrganizationMappings")
	out := make(map[string]string, len(orgItems))
	for _, o := range orgItems {
		serverURL := extractField(o, "server_url")
		cloudKey := extractField(o, "sonarcloud_org_key")
		if serverURL == "" || cloudKey == "" || shouldSkipOrg(cloudKey) {
			continue
		}
		if _, ok := out[serverURL]; !ok {
			out[serverURL] = cloudKey
		}
	}
	return out
}

// ruleKeySet extracts the "key" field of each raw rule record into a set.
func ruleKeySet(items []json.RawMessage) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		if key := extractField(item, "key"); key != "" {
			set[key] = true
		}
	}
	return set
}

// diffRuleKeySets returns how many keys are present in target but not in
// source (added) and present in source but not in target (removed).
func diffRuleKeySets(source, target map[string]bool) (added, removed int) {
	for key := range target {
		if !source[key] {
			added++
		}
	}
	for key := range source {
		if !target[key] {
			removed++
		}
	}
	return added, removed
}
