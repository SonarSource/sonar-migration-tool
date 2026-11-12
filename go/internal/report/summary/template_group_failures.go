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

// unresolvedReasonLookupFailed is the record reason for a failed lookup.
// Any other reason ("missing") means the target answered without the group.
const unresolvedReasonLookupFailed = "lookup_failed"

type templateRef struct{ name, org string }

// templateGroupMiss is the set of groups of one template whose grants were
// dropped, split by why.
type templateGroupMiss struct {
	missing      map[string]bool // the target answered without the group
	lookupFailed map[string]bool // the lookup itself failed
}

func newTemplateGroupMiss() *templateGroupMiss {
	return &templateGroupMiss{missing: map[string]bool{}, lookupFailed: map[string]bool{}}
}

// issues renders the Details strings for the template's row.
func (m *templateGroupMiss) issues() []string {
	var out []string
	if len(m.missing) > 0 {
		out = append(out, "Group permissions not granted for group(s) "+sortedKeys(m.missing)+
			": they do not exist in the target organization. "+
			"Migrate them first (--objects groups), then re-run --objects permission_templates.")
	}
	if len(m.lookupFailed) > 0 {
		out = append(out, "Group permissions not granted for group(s) "+sortedKeys(m.lookupFailed)+
			": they could not be looked up in the target organization "+
			"(see the run log), re-run --objects permission_templates.")
	}
	return out
}

// unresolvedTemplateGroups maps each template to its dropped groups.
type unresolvedTemplateGroups map[templateRef]*templateGroupMiss

func (u unresolvedTemplateGroups) add(ref templateRef, group, reason string) {
	m := u[ref]
	if m == nil {
		m = newTemplateGroupMiss()
		u[ref] = m
	}
	if reason == unresolvedReasonLookupFailed {
		m.lookupFailed[group] = true
	} else {
		m.missing[group] = true
	}
}

// issuesFor returns the Details strings for item, merging every recorded
// template that matches it.
func (u unresolvedTemplateGroups) issuesFor(item EntityItem) []string {
	merged := newTemplateGroupMiss()
	for ref, m := range u {
		if !matchesTemplateRef(item, ref) {
			continue
		}
		for g := range m.missing {
			merged.missing[g] = true
		}
		for g := range m.lookupFailed {
			merged.lookupFailed[g] = true
		}
	}
	return merged.issues()
}

// applyTemplateGroupResolutionFailures moves Permission Templates rows out of
// Succeeded (and NearPerfect, when the default-template rule already moved
// them) into Partial when some group grants of the template were dropped,
// with an Issue naming the groups. The template itself was created; only its
// group permissions are missing.
func applyTemplateGroupResolutionFailures(store *common.DataStore,
	succeeded, nearPerfect, partial []EntityItem) ([]EntityItem, []EntityItem, []EntityItem) {

	unresolved := readUnresolvedTemplateGroups(store)
	if len(unresolved) == 0 {
		return succeeded, nearPerfect, partial
	}
	succeeded, partial = moveUnresolvedTemplates(succeeded, partial, unresolved)
	nearPerfect, partial = moveUnresolvedTemplates(nearPerfect, partial, unresolved)
	return succeeded, nearPerfect, partial
}

// readUnresolvedTemplateGroups reads the unresolved records and resolves
// each cloud_template_id to the template's name and organization through
// createPermissionTemplates. A record naming no known template is ignored.
func readUnresolvedTemplateGroups(store *common.DataStore) unresolvedTemplateGroups {
	records, err := store.ReadAll(templateGroupUnresolvedStore)
	if err != nil || len(records) == 0 {
		return nil
	}
	refByCloudID := templateRefsByCloudID(store)
	out := unresolvedTemplateGroups{}
	for _, rec := range records {
		ref, ok := refByCloudID[jsonStr(rec, "cloud_template_id")]
		group := jsonStr(rec, "group")
		if !ok || group == "" {
			continue
		}
		if org := jsonStr(rec, "sonarcloud_org_key"); org != "" {
			ref.org = org
		}
		out.add(ref, group, jsonStr(rec, "reason"))
	}
	return out
}

// templateRefsByCloudID maps cloud_template_id → (name, org) from the
// createPermissionTemplates output.
func templateRefsByCloudID(store *common.DataStore) map[string]templateRef {
	templates, _ := store.ReadAll("createPermissionTemplates")
	out := make(map[string]templateRef, len(templates))
	for _, t := range templates {
		id, name := jsonStr(t, "cloud_template_id"), jsonStr(t, "name")
		if id != "" && name != "" {
			out[id] = templateRef{name: name, org: jsonStr(t, "sonarcloud_org_key")}
		}
	}
	return out
}

// moveUnresolvedTemplates moves the rows of items that lost group grants
// into partial, with the issues appended, and returns both slices.
func moveUnresolvedTemplates(items, partial []EntityItem, unresolved unresolvedTemplateGroups) ([]EntityItem, []EntityItem) {
	keep := items[:0:0]
	for _, item := range items {
		issues := unresolved.issuesFor(item)
		if len(issues) == 0 {
			keep = append(keep, item)
			continue
		}
		moved := item
		moved.Issues = append(append([]string(nil), item.Issues...), issues...)
		partial = append(partial, moved)
	}
	return keep, partial
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
