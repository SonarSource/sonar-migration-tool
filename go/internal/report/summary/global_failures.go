// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// global_failures.go covers two #230 criteria whose failure path goes
// through requests.log (no per-task JSONL output exists today):
//
//   Y5: /api/permissions/set_default_template — when SonarQube Cloud
//       rejects setting a permission template as the default, the
//       template itself was created (green); only the "make default"
//       step failed, so the row routes to NearPerfect (yellow).
//   O4: /api/permissions/add_group with no projectKey/templateId in
//       the body — this is the org-level (global) group permission
//       grant. SQS-side group permissions don't make it to SQC when
//       this fails; the corresponding group row routes to Partial
//       (orange) since the group itself is migrated but its
//       permissions aren't.

// applyTemplateDefaultFailures (Y5) moves PermissionTemplates rows out
// of Succeeded into NearPerfect when a setDefaultTemplates POST
// against /api/permissions/set_default_template failed for them.
func applyTemplateDefaultFailures(runDir string, store *common.DataStore,
	succeeded, nearPerfect []EntityItem) ([]EntityItem, []EntityItem) {

	failedTemplateIDs := scanRequestsLogForFailedTemplateIDs(runDir,
		"/api/permissions/set_default_template")
	if len(failedTemplateIDs) == 0 {
		return succeeded, nearPerfect
	}
	// Resolve cloud_template_id → row Name via createPermissionTemplates.
	templates, _ := store.ReadAll("createPermissionTemplates")
	nameByCloudID := make(map[string]string, len(templates))
	for _, t := range templates {
		cloudID := jsonStr(t, "cloud_template_id")
		name := jsonStr(t, "name")
		if cloudID != "" && name != "" {
			nameByCloudID[cloudID] = name
		}
	}
	targetNames := make(map[string]bool, len(failedTemplateIDs))
	for id := range failedTemplateIDs {
		if name, ok := nameByCloudID[id]; ok {
			targetNames[name] = true
		}
	}
	if len(targetNames) == 0 {
		return succeeded, nearPerfect
	}
	keep := succeeded[:0:0]
	for _, item := range succeeded {
		if !targetNames[item.Name] {
			keep = append(keep, item)
			continue
		}
		moved := item
		moved.Issues = append(append([]string(nil), item.Issues...),
			"Could not be set as the default permission template on SonarQube Cloud — set it manually after migration.")
		nearPerfect = append(nearPerfect, moved)
	}
	return keep, nearPerfect
}

// applyGlobalGroupPermFailures (O4) moves Groups rows out of Succeeded
// into Partial when an org-level (no projectKey) /api/permissions/
// add_group POST failed for them. Per-project group permission
// failures are handled by applyProjectFailures (#228).
func applyGlobalGroupPermFailures(runDir string,
	succeeded, partial []EntityItem) ([]EntityItem, []EntityItem) {

	failedGroups := scanRequestsLogForFailedGlobalGroupPerms(runDir)
	if len(failedGroups) == 0 {
		return succeeded, partial
	}
	keep := succeeded[:0:0]
	for _, item := range succeeded {
		perms, ok := failedGroups[item.Name]
		if !ok {
			keep = append(keep, item)
			continue
		}
		moved := item
		moved.Issues = append(append([]string(nil), item.Issues...),
			"Global permission(s) not migrated: "+strings.Join(perms, ", "))
		partial = append(partial, moved)
	}
	return keep, partial
}

// openRequestsLogScanner opens requests.log under runDir and returns a line
// scanner ready for iteration. The caller must invoke the returned close func
// when done. Returns (nil, nop, false) when the file cannot be opened.
func openRequestsLogScanner(runDir string) (*bufio.Scanner, func(), bool) {
	f, err := os.Open(filepath.Join(runDir, "requests.log"))
	if err != nil {
		return nil, func() {}, false
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	return sc, func() { f.Close() }, true
}

// scanRequestsLogForFailedTemplateIDs returns the set of templateId
// request-body values from failed POSTs to the given URL suffix.
func scanRequestsLogForFailedTemplateIDs(runDir, urlSuffix string) map[string]bool {
	out := map[string]bool{}
	scanner, close, ok := openRequestsLogScanner(runDir)
	if !ok {
		return out
	}
	defer close()
	for scanner.Scan() {
		entry, ok := parseConfigLogLine(scanner.Text())
		if !ok {
			continue
		}
		payload, _ := entry["payload"].(map[string]any)
		if payload == nil ||
			asString(payload["method"]) != "POST" ||
			!strings.HasSuffix(asString(payload["url"]), urlSuffix) ||
			!isFailure(payload["status"], asString(entry["status"])) {
			continue
		}
		body := configRequestBody(payload)
		if tid := asString(body["templateId"]); tid != "" {
			out[tid] = true
		}
	}
	return out
}

// scanRequestsLogForFailedGlobalGroupPerms returns group name →
// failed permission list for failed POSTs to /api/permissions/add_group
// with no projectKey (global scope).
func scanRequestsLogForFailedGlobalGroupPerms(runDir string) map[string][]string {
	out := map[string][]string{}
	seen := map[string]bool{}
	scanner, close, ok := openRequestsLogScanner(runDir)
	if !ok {
		return out
	}
	defer close()
	for scanner.Scan() {
		entry, ok := parseConfigLogLine(scanner.Text())
		if !ok {
			continue
		}
		payload, _ := entry["payload"].(map[string]any)
		if payload == nil ||
			asString(payload["method"]) != "POST" ||
			!strings.HasSuffix(asString(payload["url"]), "/api/permissions/add_group") ||
			!isFailure(payload["status"], asString(entry["status"])) {
			continue
		}
		body := configRequestBody(payload)
		if asString(body["projectKey"]) != "" {
			continue // per-project — handled by applyProjectFailures
		}
		group := asString(body["groupName"])
		perm := asString(body["permission"])
		if group == "" || perm == "" {
			continue
		}
		key := fmt.Sprintf("%s\x00%s", group, perm)
		if seen[key] {
			continue
		}
		seen[key] = true
		out[group] = append(out[group], perm)
	}
	return out
}
