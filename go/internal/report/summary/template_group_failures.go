// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"sort"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// templateGroupUnresolvedStore is the DataStore key under which the migrate
// task setTemplateGroupPermissions records every (template, group) pair it
// could not grant because the group could not be resolved in the target
// organization (#685). No HTTP call is made for such a pair, so requests.log
// has nothing to report; this sidecar is the only trace.
const templateGroupUnresolvedStore = "setTemplateGroupPermissions:unresolved"

const (
	unresolvedReasonMissing      = "missing"
	unresolvedReasonLookupFailed = "lookup_failed"
)

type templateRef struct{ name, org string }

// applyTemplateGroupResolutionFailures moves Permission Templates rows out of
// Succeeded (and NearPerfect, when the default-template rule already moved
// them) into Partial when some group grants of the template were dropped,
// with an Issue naming the groups. The template itself was created; only its
// group permissions are missing.
func applyTemplateGroupResolutionFailures(store *common.DataStore,
	succeeded, nearPerfect, partial []EntityItem) ([]EntityItem, []EntityItem, []EntityItem) {

	records, err := store.ReadAll(templateGroupUnresolvedStore)
	if err != nil || len(records) == 0 {
		return succeeded, nearPerfect, partial
	}
	templates, _ := store.ReadAll("createPermissionTemplates")
	refByCloudID := make(map[string]templateRef, len(templates))
	for _, t := range templates {
		id, name := jsonStr(t, "cloud_template_id"), jsonStr(t, "name")
		if id != "" && name != "" {
			refByCloudID[id] = templateRef{name: name, org: jsonStr(t, "sonarcloud_org_key")}
		}
	}

	missing := map[templateRef]map[string]bool{}
	lookupFailed := map[templateRef]map[string]bool{}
	for _, rec := range records {
		ref, ok := refByCloudID[jsonStr(rec, "cloud_template_id")]
		group := jsonStr(rec, "group")
		if !ok || group == "" {
			continue
		}
		if org := jsonStr(rec, "sonarcloud_org_key"); org != "" {
			ref.org = org
		}
		target := missing
		if jsonStr(rec, "reason") == unresolvedReasonLookupFailed {
			target = lookupFailed
		}
		if target[ref] == nil {
			target[ref] = map[string]bool{}
		}
		target[ref][group] = true
	}
	if len(missing) == 0 && len(lookupFailed) == 0 {
		return succeeded, nearPerfect, partial
	}

	issuesFor := func(item EntityItem) []string {
		var issues []string
		for ref, groups := range missing {
			if matchesTemplateRef(item, ref) {
				issues = append(issues, "Group permissions not granted for group(s) "+
					sortedKeys(groups)+": they do not exist in the target organization. "+
					"Migrate them first (--objects groups), then re-run --objects permission_templates.")
				break
			}
		}
		for ref, groups := range lookupFailed {
			if matchesTemplateRef(item, ref) {
				issues = append(issues, "Group permissions not granted for group(s) "+
					sortedKeys(groups)+": they could not be looked up in the target organization "+
					"(see the run log), re-run --objects permission_templates.")
				break
			}
		}
		return issues
	}

	move := func(items []EntityItem) []EntityItem {
		keep := items[:0:0]
		for _, item := range items {
			issues := issuesFor(item)
			if len(issues) == 0 {
				keep = append(keep, item)
				continue
			}
			moved := item
			moved.Issues = append(append([]string(nil), item.Issues...), issues...)
			partial = append(partial, moved)
		}
		return keep
	}
	return move(succeeded), move(nearPerfect), partial
}

// matchesTemplateRef matches on name, and on organization when both sides
// carry one (template names are only unique per organization).
func matchesTemplateRef(item EntityItem, ref templateRef) bool {
	if item.Name != ref.name {
		return false
	}
	return item.Organization == "" || ref.org == "" || item.Organization == ref.org
}

func sortedKeys(set map[string]bool) string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
