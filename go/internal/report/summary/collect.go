// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/analysis"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// CollectSummary reads task JSONL outputs and the analysis report to build
// a MigrationSummary for PDF rendering. exportDir is the root export directory
// containing extract runs and the run directory; if empty it defaults to the
// parent of runDir.
func CollectSummary(runDir, exportDir string) (*MigrationSummary, error) {
	if exportDir == "" {
		exportDir = filepath.Dir(runDir)
	}

	store := common.NewDataStore(runDir)
	failuresByType, err := collectFailures(runDir)
	if err != nil {
		return nil, err
	}

	configFailures, err := collectConfigFailures(runDir)
	if err != nil {
		return nil, err
	}

	projectDataMap := collectProjectData(store)
	ncdFallbackMap := collectNCDFallback(store)
	ncdBranchOverrideSet := collectNCDBranchOverrides(store)
	syncStatsMap := collectSyncStats(store)
	branchSourcePurgedMap := collectBranchSourcePurged(store)
	extractMapping, _ := structure.GetUniqueExtracts(exportDir)
	// #353 — per-object dropped-user-permission counts: SonarQube Cloud
	// has no API to grant permissions to individual users, so any user
	// permission found on a source object is dropped at migration time.
	// Surface that as a per-row "Permissions granted to N users have
	// been dropped" line via a |userPerms: marker. The status of the
	// object itself stays Succeeded — this is informational, not a
	// failure.
	droppedPerms := collectDroppedUserPermsBySection(exportDir, extractMapping, store)

	var sections []Section
	for _, def := range sectionDefs {
		section := collectSection(store, def, failuresByType, configFailures, exportDir, extractMapping)
		if def.Name == "Projects" {
			attachProjectData(section.Succeeded, projectDataMap)
			section.Succeeded, section.Partial = applyNCDFallbackPartials(section.Succeeded, section.Partial, ncdFallbackMap)
			section.Succeeded, section.Partial = applyNCDBranchOverridePartials(section.Succeeded, section.Partial, ncdBranchOverrideSet)
			// #228 — per-project follow-up operations (tags, settings,
			// group permissions, links, webhooks) that failed for an
			// otherwise-successfully-created project route the project
			// to NearPerfect (yellow) or Partial (orange) with one
			// Issues line per failing operation. Also fold in the
			// project-data / hotspot-sync skip records (orange) read
			// from the data-migration tasks' JSONL output.
			projectFailures := collectProjectFailures(runDir)
			projectFailures = append(projectFailures, collectProjectSyncSkips(store, projectDataMap)...)
			section.Succeeded, section.NearPerfect, section.Partial = applyProjectFailures(
				section.Succeeded, section.NearPerfect, section.Partial, projectFailures)
			// #356 — append a per-project "x/y issues synced (z%)"
			// line to the project's Detail field. Applied AFTER
			// applyProjectFailures so it lands on all routed buckets
			// (Succeeded, NearPerfect, Partial). Predictive reports
			// suppress this line at render time (sync success cannot
			// be predicted).
			attachSyncStats(section.Succeeded, syncStatsMap)
			attachSyncStats(section.NearPerfect, syncStatsMap)
			attachSyncStats(section.Partial, syncStatsMap)
			// #425 — note branches migrated without their source (purged
			// on the source server) in each affected project's Details
			// column. Applied to all routed buckets; the outcome itself
			// is unchanged (the branch still imports measures + issues).
			attachBranchSourcePurged(section.Succeeded, branchSourcePurgedMap)
			attachBranchSourcePurged(section.NearPerfect, branchSourcePurgedMap)
			attachBranchSourcePurged(section.Partial, branchSourcePurgedMap)
		}
		// #353 — attach the dropped-user-permission count marker to
		// every entity in every routed bucket so the per-row Details
		// column carries "Permissions granted to N users have been
		// dropped". Object status is NOT changed.
		if perms, ok := droppedPerms[def.Name]; ok && len(perms) > 0 {
			attachDroppedUserPerms(section.Succeeded, perms, def.Name)
			attachDroppedUserPerms(section.NearPerfect, perms, def.Name)
			attachDroppedUserPerms(section.Partial, perms, def.Name)
		}
		sections = append(sections, section)
	}

	runID := extractRunID(runDir)
	sum := &MigrationSummary{
		RunID:       runID,
		GeneratedAt: time.Now(),
		Sections:    sections,
		Limitations: collectLimitations(runDir, exportDir, extractMapping),
		RateLimit:   collectRateLimitReport(runDir, failuresByType),
	}

	// Fold in migrate-engine runtime telemetry (run_meta.json /
	// run_events.jsonl / requests.log). collectRuntime never returns a
	// hard error for absent files, so predictive reports — which have
	// none of these — simply leave the runtime fields at their zero
	// values and the runtime sections omit themselves.
	if rt, err := collectRuntime(runDir); err == nil {
		sum.StartedAt = rt.StartedAt
		sum.CompletedAt = rt.CompletedAt
		sum.TotalElapsed = rt.TotalElapsed
		sum.OverallStatus = rt.OverallStatus
		sum.Phases = rt.Phases
		sum.Tasks = rt.Tasks
		sum.Failures = rt.Failures
		sum.Warnings = rt.Warnings
		sum.Branches = rt.Branches
		sum.Throughput = rt.Throughput
		sum.ProjectKeys = collectProjectKeyReport(store, rt.ProjectKeyPattern)
	}

	return sum, nil
}

// collectProjectKeyReport re-derives every project's target SonarQube Cloud
// key from the generateProjectMappings output and the recorded
// project_key_pattern, then flags two problems (issue #138): target keys
// claimed by more than one source project (collisions) and keys longer than
// migrate.MaxProjectKeyLength. Returns nil when there is nothing to report.
func collectProjectKeyReport(store *common.DataStore, pattern string) *ProjectKeyReport {
	rows, err := store.ReadAll("generateProjectMappings")
	if err != nil || len(rows) == 0 {
		return nil
	}

	type bucket struct {
		sources []ProjectKeySource
		seen    map[string]bool
	}
	byTarget := make(map[string]*bucket)
	order := make([]string, 0, len(rows))
	var tooLong []ProjectKeyTooLong

	for _, row := range rows {
		srcKey := jsonStr(row, "key")
		orgKey := jsonStr(row, "sonarcloud_org_key")
		if srcKey == "" || orgKey == "" || orgKey == "SKIPPED" {
			continue
		}
		target := migrate.RenderProjectKey(pattern, srcKey, orgKey)
		src := ProjectKeySource{SourceKey: srcKey, OrgKey: orgKey}

		b := byTarget[target]
		if b == nil {
			b = &bucket{seen: map[string]bool{}}
			byTarget[target] = b
			order = append(order, target)
		}
		dedupe := srcKey + "\x00" + orgKey
		if !b.seen[dedupe] {
			b.seen[dedupe] = true
			b.sources = append(b.sources, src)
		}

		if len(target) > migrate.MaxProjectKeyLength {
			tooLong = append(tooLong, ProjectKeyTooLong{
				ProjectKeySource: src,
				TargetKey:        target,
				Length:           len(target),
			})
		}
	}

	var collisions []ProjectKeyCollision
	for _, target := range order {
		b := byTarget[target]
		if len(b.sources) > 1 {
			collisions = append(collisions, ProjectKeyCollision{TargetKey: target, Sources: b.sources})
		}
	}

	if len(collisions) == 0 && len(tooLong) == 0 {
		return nil
	}
	return &ProjectKeyReport{
		Pattern:    pattern,
		Collisions: collisions,
		TooLong:    tooLong,
	}
}

// collectLimitations builds the free-text bullet list rendered in the
// "Migration limitations" section at the end of the report (issue
// #154). Each call appends a separate bullet:
//
//   - SonarQube Server's Applications feature has no SonarQube Cloud
//     counterpart (#154).
//   - Per-branch new-code-definition overrides — SonarQube Cloud has
//     no per-branch NCD concept, so any branch-level override on SQS
//     is dropped (#134).
//   - Project new-code-definition types not supported on SonarQube
//     Cloud — currently reference_branch and specific_analysis. The
//     migrated project is left with the org default (#135).
func collectLimitations(runDir, exportDir string, mapping structure.ExtractMapping) []string {
	var out []string
	if appCount := countExtractItems(exportDir, mapping, "getApplications"); appCount > 0 {
		out = append(out,
			fmt.Sprintf("Applications do not exist on SonarQube Cloud, %d SQS applications were not migrated.",
				appCount))
	}
	if projectDataGloballySkipped(runDir) {
		out = append(out,
			"Project data migration was skipped by configuration (--skip_project_data_migration). No source code, history, issue triage, or hotspot review state was imported.")
	}
	out = append(out, collectNCDLimitations(runDir, exportDir, mapping)...)
	out = append(out, collectSASTCustomizationLimitation(exportDir, mapping)...)
	out = append(out, collectUserPermissionLimitations(exportDir, mapping)...)
	out = append(out, collectGlobalSettingMappingLimitations(exportDir, mapping)...)
	return out
}

// collectUserPermissionLimitations covers #230 Y3 and Y4. SonarQube
// Cloud does not expose a way to grant permissions to individual
// users via API — only to groups — so any SQS user permission on a
// permission template or at the global scope is dropped during
// migration. Surface a single limitation note per scope listing
// the affected logins so the operator can re-grant them via the
// SQC UI or via group membership.
func collectUserPermissionLimitations(exportDir string, mapping structure.ExtractMapping) []string {
	if mapping == nil {
		return nil
	}
	collect := func(taskKey string) []string {
		items, _ := structure.ReadExtractData(exportDir, mapping, taskKey)
		if len(items) == 0 {
			return nil
		}
		seen := map[string]bool{}
		var logins []string
		for _, it := range items {
			login := jsonStr(it.Data, "login")
			if login == "" || seen[login] {
				continue
			}
			seen[login] = true
			logins = append(logins, login)
		}
		sort.Strings(logins)
		return logins
	}
	var notes []string
	if tplLogins := collect("getTemplateUsersScanners"); len(tplLogins) > 0 ||
		len(collect("getTemplateUsersViewers")) > 0 {
		// Combine both feeds without losing logins that appear only
		// in one of them.
		seen := map[string]bool{}
		var combined []string
		for _, feed := range []string{"getTemplateUsersScanners", "getTemplateUsersViewers"} {
			for _, l := range collect(feed) {
				if !seen[l] {
					seen[l] = true
					combined = append(combined, l)
				}
			}
		}
		sort.Strings(combined)
		notes = append(notes, fmt.Sprintf(
			"SonarQube Cloud does not support user permissions via API. The following %d user(s) had permissions on SonarQube Server permission templates and were not migrated: %s.",
			len(combined), strings.Join(combined, ", ")))
	}
	if globalLogins := collect("getUserPermissions"); len(globalLogins) > 0 {
		notes = append(notes, fmt.Sprintf(
			"SonarQube Cloud does not support user permissions via API. The following %d user(s) had global SonarQube Server permissions and were not migrated: %s.",
			len(globalLogins), strings.Join(globalLogins, ", ")))
	}
	return notes
}

// sqsToSQCSettingMap captures known SQS → SQC setting key equivalences
// where SonarQube Cloud uses a different key (or the feature was moved
// to an org-level config). When the SQS extract carries one of these
// keys and the migration didn't successfully map it (the catch-all
// path treats it as "not on SQC" because the literal key is absent
// from list_definitions on the cloud side), we surface a single
// limitation note so the operator can re-create the configuration
// manually on SonarQube Cloud.
var sqsToSQCSettingMap = map[string]string{
	"sonar.qualitygate.ignoreSmallChanges": "Ignore duplication and coverage on small changes (org-level)",
	"sonar.projects.defaultVisibility":     "Allow only private projects (org-level)",
	// sonar.ai.suggestions.enabled / sonar.ai.codefix.hidden are now
	// migrated end-to-end (#251) — their rows live in the Global
	// Settings section, so they no longer need a limitation note.
}

// collectGlobalSettingMappingLimitations covers #230 Y7, Y8 and O7
// — three SQS configurations whose SQC equivalents live elsewhere
// (org-level UI settings, not under the same list_definitions key).
// If the SQS extract carries any of these settings as non-empty, emit
// one limitation bullet per affected setting describing the manual
// follow-up required on the SonarQube Cloud side.
func collectGlobalSettingMappingLimitations(exportDir string, mapping structure.ExtractMapping) []string {
	if mapping == nil {
		return nil
	}
	items, _ := structure.ReadExtractData(exportDir, mapping, "getServerSettings")
	if len(items) == 0 {
		return nil
	}
	var notes []string
	seen := map[string]bool{}
	for _, it := range items {
		key := jsonStr(it.Data, "key")
		target, ok := sqsToSQCSettingMap[key]
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		notes = append(notes, fmt.Sprintf(
			"%s is set on SonarQube Server but has no /api/settings/set equivalent on SonarQube Cloud. Configure %q manually after migration.",
			key, target))
	}
	sort.Strings(notes)
	return notes
}

// sastCustomizationKeys are SonarQube settings whose presence on the
// source indicates the customer used the SAST-engine customization
// feature (custom security rules / JSON config). SonarQube Cloud
// doesn't expose this feature, so any such configuration is dropped
// silently during migration — surface a single limitation note (#228
// orange) listing the impacted projects.
//
// Keys cover both the global-scope and project-scope variants. The
// list is deliberately small and explicit so a setting that merely
// happens to start with "sonar.security" doesn't trip the heuristic.
var sastCustomizationKeys = map[string]bool{
	"sonar.security.config.javasecurity":                     true,
	"sonar.security.config.phpsecurity":                      true,
	"sonar.security.config.pythonsecurity":                   true,
	"sonar.security.config.roslyn.sonaranalyzer.security.cs": true,
	"sonar.security.config.jssecurity":                       true,
	"sonar.security.config.tssecurity":                       true,
	"sonar.security.sources.javasecurity":                    true,
	"sonar.security.sources.phpsecurity":                     true,
	"sonar.security.sources.pythonsecurity":                  true,
	"sonar.security.sources.jssecurity":                      true,
	"sonar.security.sources.tssecurity":                      true,
}

// collectSASTCustomizationLimitation scans server-level and project-
// level settings for SAST-engine customization keys (#228 orange) and
// returns one bullet if any are present. The bullet lists up to a
// handful of impacted project keys so the operator knows where to
// look — global SAST customization is rendered as "(global)".
func collectSASTCustomizationLimitation(exportDir string, mapping structure.ExtractMapping) []string {
	if mapping == nil {
		return nil
	}
	// Global-scope.
	hits := map[string]bool{}
	serverItems, _ := structure.ReadExtractData(exportDir, mapping, "getServerSettings")
	for _, it := range serverItems {
		if sastCustomizationKeys[jsonStr(it.Data, "key")] {
			hits["(global)"] = true
		}
	}
	// Project-scope. getProjectSettings emits one record per project
	// with a nested settings[] array.
	projItems, _ := structure.ReadExtractData(exportDir, mapping, "getProjectSettings")
	for _, it := range projItems {
		var obj struct {
			ProjectKey string `json:"projectKey"`
			Settings   []struct {
				Key string `json:"key"`
			} `json:"settings"`
		}
		if err := json.Unmarshal(it.Data, &obj); err != nil {
			continue
		}
		for _, s := range obj.Settings {
			if sastCustomizationKeys[s.Key] {
				key := obj.ProjectKey
				if key == "" {
					key = "(unknown project)"
				}
				hits[key] = true
				break
			}
		}
	}
	if len(hits) == 0 {
		return nil
	}
	keys := make([]string, 0, len(hits))
	for k := range hits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return []string{
		fmt.Sprintf("SonarQube SAST engine customization (custom security rules / JSON config) is not supported on SonarQube Cloud. Affected: %s.",
			strings.Join(keys, ", ")),
	}
}

// collectNCDLimitations scans the getNewCodePeriods extract and
// returns one bullet per category of NCD that cannot be migrated to
// SonarQube Cloud — keyed by the migrate-side logic in
// runSetNewCodePeriods so the report and the runtime agree on what
// was actually skipped.
func collectNCDLimitations(runDir, exportDir string, mapping structure.ExtractMapping) []string {
	if mapping == nil {
		return nil
	}
	items, err := structure.ReadExtractData(exportDir, mapping, "getNewCodePeriods")
	if err != nil || len(items) == 0 {
		return nil
	}

	// Main branch per (serverURL, projectKey) — read from
	// createProjects (the migrate-side authority). Falls back to
	// "master" when the field is absent, matching runSetNewCodePeriods.
	store := common.NewDataStore(runDir)
	createdProjects, _ := store.ReadAll("createProjects")
	mainBranchByKey := make(map[string]string, len(createdProjects))
	for _, p := range createdProjects {
		server := common.ExtractField(p, "server_url")
		key := common.ExtractField(p, "key")
		main := common.ExtractField(p, "main_branch")
		if main == "" {
			main = "master"
		}
		mainBranchByKey[server+key] = main
	}

	// SQS's /api/new_code_periods/list returns one record per (project,
	// branch). The `inherited` flag is BRANCH-level — it tells you the
	// branch inherits the project setting, NOT that the project itself
	// is inheriting from somewhere upstream. Implications for the
	// limitation counts:
	//
	//   - branchKey == mainBranch (with or without inherited): this
	//     IS the project-level NCD. Counts toward unsupported-type if
	//     the type is not migratable; otherwise it's a normal apply.
	//   - branchKey != mainBranch && inherited == false: explicit
	//     per-branch override → counts toward #134.
	//   - branchKey != mainBranch && inherited == true: branch just
	//     reflects the project setting → ignore (the main-branch
	//     record already covers it).
	perBranch := 0
	unsupportedTypeProjects := make(map[string]bool)
	for _, item := range items {
		var obj map[string]any
		_ = json.Unmarshal(item.Data, &obj)
		inherited, _ := obj["inherited"].(bool)

		projectKey := common.ExtractField(item.Data, "projectKey")
		branch := common.ExtractField(item.Data, "branchKey")
		ncdType := common.ExtractField(item.Data, "type")

		mainBranch := mainBranchByKey[item.ServerURL+projectKey]
		if mainBranch == "" {
			mainBranch = "master"
		}

		if branch != "" && branch != mainBranch {
			if !inherited {
				perBranch++
			}
			continue
		}
		if _, supported := sqcNewCodeTypes[ncdType]; !supported {
			unsupportedTypeProjects[item.ServerURL+projectKey] = true
		}
	}

	var out []string
	if perBranch > 0 {
		out = append(out, fmt.Sprintf(
			"SonarQube Cloud has no per-branch new-code-definition concept; "+
				"%d branch-level new code definition(s) on SonarQube Server were not migrated.",
			perBranch))
	}
	if len(unsupportedTypeProjects) > 0 {
		out = append(out, fmt.Sprintf(
			"SonarQube Cloud does not support the reference_branch or specific_analysis "+
				"new-code-definition types; %d project(s) were migrated with the SonarQube "+
				"Cloud organization default instead.",
			len(unsupportedTypeProjects)))
	}
	return out
}

// sqcNewCodeTypes mirrors the migrate-side sqcNewCodeType map in
// internal/migrate/tasks_associate.go. Keeping a local copy avoids a
// cross-package import cycle (migrate already imports report/summary
// indirectly via the binary). Update both maps together when SQC
// adds a new supported type.
var sqcNewCodeTypes = map[string]bool{
	"NUMBER_OF_DAYS":   true,
	"PREVIOUS_VERSION": true,
}

// countExtractItems returns the number of JSONL records the extract
// task wrote across every server URL in the mapping. Returns 0 if
// the extract didn't run or the read failed — the limitations
// collector treats absence as "no records," which is the correct
// fall-back behaviour.
func countExtractItems(exportDir string, mapping structure.ExtractMapping, taskKey string) int {
	if mapping == nil {
		return 0
	}
	items, err := structure.ReadExtractData(exportDir, mapping, taskKey)
	if err != nil {
		return 0
	}
	return len(items)
}

// collectFailures parses the requests.log and groups failures by entity type.
func collectFailures(runDir string) (map[string][]analysis.ReportRow, error) {
	rows, err := analysis.ParseRequestsLog(runDir)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]analysis.ReportRow)
	for _, row := range rows {
		if row.Outcome == "failure" {
			result[row.EntityType] = append(result[row.EntityType], row)
		}
	}
	return result, nil
}

// collectSection builds a Section from task JSONL data, analysis failures and extract data.
func collectSection(store *common.DataStore, def sectionDef,
	failuresByType map[string][]analysis.ReportRow,
	configFailures []configFailure,
	exportDir string, extractMapping structure.ExtractMapping) Section {

	// Global settings (issue #186) don't match the generic create*/
	// generate*Mappings pattern — each output record carries its own
	// per-org outcome buckets. Render that section through a dedicated
	// fan-out helper instead of the standard succeeded/skipped/failed
	// path.
	if def.Name == "Global Settings" {
		return collectGlobalSettings(store, def)
	}

	succeeded := collectSucceeded(store, def)
	skipped := collectSkipped(store, def)
	failed := collectFailed(failuresByType, def)
	partial := collectPartial(def, configFailures, succeeded)
	var nearPerfect []EntityItem

	// Augment skipped with built-in / unused items derived from extract data.
	if def.ExtractTask != "" && extractMapping != nil {
		skipped = append(skipped, collectExtractSkipped(def, exportDir, extractMapping, store)...)
	}

	// Portfolio classification (#229): apps and combinable subportfolios
	// (depth=1 with a uniform selection mode) route to NearPerfect with
	// the appropriate Issues line; deeper nesting or mixed-mode
	// subportfolios route to Partial. Plain portfolios stay in Succeeded.
	if def.Name == "Portfolios" && extractMapping != nil {
		classifications := portfolioClassifications(exportDir, extractMapping)
		succeeded, nearPerfect, partial = applyPortfolioClassifications(store, succeeded, nearPerfect, partial, classifications)

		// Empty portfolios (no resolved projects on the source) are
		// not worth migrating — surface them in the Skipped bucket
		// with a standard message and remove from any non-Skipped
		// bucket they may have landed in.
		empties := detectEmptyPortfolios(store, exportDir, extractMapping)
		succeeded, nearPerfect, partial, skipped = applyEmptyPortfolioSkips(store, succeeded, nearPerfect, partial, skipped, empties)
	}

	// Portfolio PATCH/DELETE failures encode the id in the URL — re-parse
	// the request log to attribute them back to a portfolio and move that
	// entity from Succeeded into Failed.
	if def.Name == "Portfolios" {
		runDir := store.BaseDir()
		if portfolioFails := collectPortfolioFailures(runDir); len(portfolioFails) > 0 {
			succeeded, failed, partial = applyPortfolioFailures(store, succeeded, failed, partial, portfolioFails)
		}
	}

	// Quality gates whose conditions had to be remapped to close
	// SonarQube Cloud equivalents (#143) are NearPerfect (yellow, #227).
	// Gates with any dropped condition — no SQC equivalent — are Partial
	// (orange, #227). The per-condition decisions are written by
	// addGateConditions to a sidecar JSONL.
	if def.Name == sectionQualityGates {
		notes := collectGateMappingNotes(store.BaseDir())
		succeeded, nearPerfect, partial = applyGateMappingNotes(succeeded, nearPerfect, partial, notes)
	}

	// Quality profiles flagged by analyzeProfileRules (#226 yellow
	// criteria) move from Succeeded into NearPerfect with per-rule
	// Issues. Orange (Partial) dominates yellow per the #224 rule.
	if def.Name == sectionQualityProfiles {
		findings := collectProfileFindings(store)
		succeeded, nearPerfect, partial = applyProfileFindings(succeeded, nearPerfect, partial, findings)
	}

	// SonarQube Server built-in groups (e.g. "sonar-users") are skipped
	// at create time — surface them in the Skipped bucket with the
	// curated note so the operator knows why the group did not migrate.
	if def.Name == "Groups" {
		skipped = appendBuiltInGroupSkips(store, skipped)
		// #230 O4: org-level (no projectKey) /api/permissions/add_group
		// failures route the corresponding Groups row to Partial.
		runDir := store.BaseDir()
		succeeded, partial = applyGlobalGroupPermFailures(runDir, succeeded, partial)
	}

	// #230 Y5: a permission template that couldn't be set as default
	// on SonarQube Cloud routes to NearPerfect (yellow) on the
	// Permission Templates section — the template itself was created.
	if def.Name == "Permission Templates" {
		runDir := store.BaseDir()
		succeeded, nearPerfect = applyTemplateDefaultFailures(runDir, store, succeeded, nearPerfect)
	}

	return Section{
		Name:        def.Name,
		Succeeded:   succeeded,
		NearPerfect: nearPerfect,
		Partial:     partial,
		Failed:      failed,
		Skipped:     skipped,
	}
}

// collectSucceeded reads items from the output (create*) task JSONL.
//
// Issue #165 — the create* tasks (createGates, createProfiles, ...)
// iterate generate*Mappings, which contains one record per
// (source_org, entity_name) pair. When a single source-side entity
// exists under N different SonarQube Server orgs that all map to the
// same SonarCloud org, the create task emits N JSONL records for the
// SAME cloud entity. Without dedup the report renders the same row
// N times and the migrated-count is inflated by N-1.
//
// We dedupe by the composite (Organization, Detail, Name, Language).
// Detail carries the cloud-side unique id for every section
// (cloud_gate_id, cloud_profile_key, etc.) so identical cloud
// entities collapse to one row; the rest of the composite keeps
// distinct entities with the same name in different orgs separate.
func collectSucceeded(store *common.DataStore, def sectionDef) []EntityItem {
	items, err := store.ReadAll(def.OutputTask)
	if err != nil {
		return nil
	}
	var result []EntityItem
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		entry := EntityItem{
			Name:         jsonStr(item, def.NameField),
			Language:     jsonStr(item, "language"),
			Organization: jsonStr(item, "sonarcloud_org_key"),
			Detail:       jsonStr(item, def.DetailField),
		}
		key := entry.Organization + "\x00" + entry.Detail + "\x00" + entry.Name + "\x00" + entry.Language
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, entry)
	}
	return result
}

// collectSkipped reads the input (generate*Mappings) task and finds items
// where sonarcloud_org_key is empty or "SKIPPED".
//
// Portfolios are created at the enterprise level rather than per organization,
// so an organization-level skip is not meaningful for them; no skipped rows
// are emitted for that section.
func collectSkipped(store *common.DataStore, def sectionDef) []EntityItem {
	if def.Name == "Portfolios" {
		return nil
	}
	items, err := store.ReadAll(def.InputTask)
	if err != nil {
		return nil
	}
	var result []EntityItem
	for _, item := range items {
		orgKey := jsonStr(item, "sonarcloud_org_key")
		if orgKey == "" || orgKey == skippedOrgSentinel {
			result = append(result, EntityItem{
				Name:         jsonStr(item, def.NameField),
				Language:     jsonStr(item, "language"),
				Organization: jsonStr(item, "sonarqube_org_key"),
				Detail:       "Organization skipped",
				SkipReason:   SkipReasonOrgSkipped,
			})
		}
	}
	return result
}

// collectExtractSkipped derives skipped entries directly from extract data
// for sections that have a source extract task (Quality Gates, Quality Profiles).
// It marks isBuiltIn items as "built-in" and items that are not referenced in
// the generated mappings as "unused".
func collectExtractSkipped(def sectionDef, exportDir string,
	mapping structure.ExtractMapping, store *common.DataStore) []EntityItem {

	items, err := structure.ReadExtractData(exportDir, mapping, def.ExtractTask)
	if err != nil || len(items) == 0 {
		return nil
	}

	mappedKeys := buildMappedKeys(def, store)

	var result []EntityItem
	seen := make(map[string]bool)
	for _, item := range items {
		name := common.ExtractField(item.Data, "name")
		if name == "" {
			continue
		}
		language := common.ExtractField(item.Data, "language")
		isBuiltIn := common.ExtractBool(item.Data, "isBuiltIn")
		key := extractKeyFor(def, name, language, item.ServerURL)
		if seen[key] {
			continue
		}
		seen[key] = true

		if isBuiltIn {
			result = append(result, EntityItem{
				Name:       name,
				Language:   language,
				Detail:     "Built-in, not migrated",
				SkipReason: SkipReasonBuiltIn,
			})
			continue
		}
		mapKey := mappedKeyFor(def, name, language)
		if mappedKeys[mapKey] {
			continue
		}
		result = append(result, EntityItem{
			Name:       name,
			Language:   language,
			Detail:     "Not used by any migrated project",
			SkipReason: SkipReasonUnused,
		})
	}
	return result
}

// buildMappedKeys returns the set of (name[+language]) keys present in the
// generate*Mappings task output. Used to detect unused extract items.
func buildMappedKeys(def sectionDef, store *common.DataStore) map[string]bool {
	items, err := store.ReadAll(def.InputTask)
	if err != nil {
		return nil
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		name := jsonStr(item, def.NameField)
		if name == "" {
			continue
		}
		language := jsonStr(item, "language")
		set[mappedKeyFor(def, name, language)] = true
	}
	return set
}

func mappedKeyFor(def sectionDef, name, language string) string {
	if def.Name == sectionQualityProfiles {
		return name + "|" + language
	}
	return name
}

func extractKeyFor(def sectionDef, name, language, serverURL string) string {
	if def.Name == sectionQualityProfiles {
		return serverURL + "|" + name + "|" + language
	}
	return serverURL + "|" + name
}

// collectFailed maps analysis report failure rows to EntityItems.
func collectFailed(failuresByType map[string][]analysis.ReportRow, def sectionDef) []EntityItem {
	rows := failuresByType[def.AnalysisEntity]
	var result []EntityItem
	for _, row := range rows {
		result = append(result, EntityItem{
			Name:         row.EntityName,
			Organization: row.Organization,
			ErrorMessage: row.ErrorMessage,
		})
	}
	return result
}

// projectDataOutcome holds the per-project state of the
// importProjectData task plus a human-readable reason for the skipped /
// failed cases (#359). It replaces the older single-status string that
// could only render "Yes / No / Failed" in the report.
type projectDataOutcome struct {
	// State is one of "success", "skipped", "failed", or empty when
	// no record exists for the project (e.g. config-skipped).
	State string
	// Reason carries a free-text operator-friendly explanation for
	// State=="skipped" / "failed". Empty when the state is success
	// or when no signal could be derived.
	Reason string
	// NeverAnalyzed is true for the specific skip case where the source
	// project was created but never analyzed (no components on any
	// branch). Per #432 this must NOT degrade the project outcome to
	// Partial — its non-analysis settings still migrate — and the Details
	// note is informational rather than a "migration skipped" warning.
	NeverAnalyzed bool
}

// collectProjectData reads importProjectData JSONL and returns the
// per-project outcome. A project may have multiple branch records;
// the worst non-success outcome wins (failed > skipped > success),
// because that's the signal an operator should see in the report.
func collectProjectData(store *common.DataStore) map[string]projectDataOutcome {
	items, err := store.ReadAll("importProjectData")
	if err != nil || len(items) == 0 {
		return nil
	}
	type acc struct {
		states  map[string][]string // state → branch detail (for the reason)
		errs    map[string]string   // state → first non-empty error
		ordered []string            // first-seen state order
	}
	by := make(map[string]*acc)
	for _, item := range items {
		key := jsonStr(item, "cloud_project_key")
		if key == "" {
			continue
		}
		state := jsonStr(item, "status")
		branch := jsonStr(item, "branch")
		errMsg := jsonStr(item, "error")
		bucket := by[key]
		if bucket == nil {
			bucket = &acc{states: map[string][]string{}, errs: map[string]string{}}
			by[key] = bucket
		}
		if _, seen := bucket.states[state]; !seen {
			bucket.ordered = append(bucket.ordered, state)
		}
		bucket.states[state] = append(bucket.states[state], branch)
		if errMsg != "" && bucket.errs[state] == "" {
			bucket.errs[state] = errMsg
		}
	}

	result := make(map[string]projectDataOutcome, len(by))
	for key, a := range by {
		switch {
		case len(a.states["failed"]) > 0:
			result[key] = projectDataOutcome{State: "failed", Reason: projectDataFailureReason(a.errs["failed"])}
		case len(a.states["skipped"]) > 0:
			skipErr := a.errs["skipped"]
			if skipErr == "" {
				// #432 — provisioned but never analyzed: the project's
				// settings still migrate and the outcome must NOT be
				// degraded. Carry an informational note instead.
				result[key] = projectDataOutcome{
					State:         "skipped",
					Reason:        "Source project was provisioned but never analyzed, project settings migrated anyway",
					NeverAnalyzed: true,
				}
			} else {
				result[key] = projectDataOutcome{State: "skipped", Reason: projectDataSkipReason(skipErr)}
			}
		case len(a.states["success"]) > 0:
			result[key] = projectDataOutcome{State: "success"}
		default:
			// State string we don't recognise — surface as skipped so
			// the report still warns the operator instead of silently
			// degrading to "success".
			result[key] = projectDataOutcome{State: "skipped", Reason: projectDataSkipReason("")}
		}
	}
	return result
}

// projectDataSkipReason maps the captured per-branch error message
// (or absence thereof) to one of the operator-friendly reasons the
// issue lists. Empty error means "no components / no analysis data"
// (#359 wording: "Source project was provisioned but never analyzed").
func projectDataSkipReason(errMsg string) string {
	if errMsg == "" {
		return "Source project was provisioned but never analyzed"
	}
	lower := strings.ToLower(errMsg)
	switch {
	case strings.Contains(lower, "permission") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "403"):
		return "Not enough permission on the source project to extract data"
	case strings.Contains(lower, "main branch") && strings.Contains(lower, "ce failed"):
		return "Main branch import failed; remaining branches skipped"
	}
	return errMsg
}

// projectDataFailureReason wraps the captured "failed" error message
// with the operator-friendly framing the issue spec uses ("API error
// when migrating project data"). Empty errors fall back to the bare
// framing so we never lose the signal entirely.
func projectDataFailureReason(errMsg string) string {
	if errMsg == "" {
		return "API error when migrating project data"
	}
	return "API error when migrating project data: " + errMsg
}

// attachProjectData stamps a per-project |scan:<state>:<reason>
// marker on each project's Detail field for projects that have an
// explicit importProjectData record (success / skipped / failed).
//
// Note: --skip_project_data_migration (i.e. importProjectData was
// never scheduled, projectDataGloballySkipped is true) is intentionally
// NOT stamped here. The operator explicitly opted out; a per-project
// "skipped: skipped by configuration" line on every row would be pure
// noise. A single limitation entry at the end of the report covers
// the operator-facing signal — see collectLimitations.
func attachProjectData(projects []EntityItem, scanMap map[string]projectDataOutcome) {
	if len(scanMap) == 0 {
		return
	}
	for i := range projects {
		cloudKey := projects[i].Detail
		outcome, ok := scanMap[cloudKey]
		if !ok || outcome.State == "" {
			continue
		}
		// #432 — the never-analyzed case uses a distinct marker state so the
		// renderer shows an informational note (not a "migration skipped"
		// warning) and does not treat it as a degraded outcome.
		markerState := outcome.State
		if outcome.NeverAnalyzed {
			markerState = "noanalysis"
		}
		marker := markerState
		if outcome.Reason != "" {
			marker += ":" + outcome.Reason
		}
		projects[i].Detail = cloudKey + "|scan:" + marker
	}
}

// projectDataGloballySkipped reports whether the importProjectData
// task was excluded from the migration run plan (i.e. the operator
// passed --skip_project_data_migration). Detected by checking
// run_meta.json's task list — if the task isn't listed, it didn't
// run. Tolerant of a missing or unparseable file: returns false so
// the per-project logic still applies. #359.
func projectDataGloballySkipped(runDir string) bool {
	data, err := os.ReadFile(filepath.Join(runDir, "run_meta.json"))
	if err != nil {
		return false
	}
	var meta struct {
		Tasks []struct {
			Name string `json:"name"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	if len(meta.Tasks) == 0 {
		// No task list means we can't tell. Default to false.
		return false
	}
	for _, t := range meta.Tasks {
		if t.Name == "importProjectData" {
			return false
		}
	}
	return true
}

// collectDroppedUserPermsBySection returns the per-entity dropped-
// user-permission counts (#353), keyed by section name. The inner
// map is keyed by the identifier the per-section attachDroppedUserPerms
// will use to look up each EntityItem: cloud_*_id (or _key) for
// Projects / Quality Profiles / Permission Templates, and gate name
// for Quality Gates (the user-perm extract record carries gateName
// directly, not the cloud gate id).
//
// SonarQube Cloud's API does not let us grant permissions to
// individual users (only to groups), so any user found in a source
// permission extract is dropped at migration time. The numbers here
// drive the per-row "Permissions granted to N users have been
// dropped" line surfaced via the |userPerms: marker.
func collectDroppedUserPermsBySection(exportDir string, mapping structure.ExtractMapping, store *common.DataStore) map[string]map[string]int {
	out := map[string]map[string]int{}

	// Projects: getProjectUsersScanners + getProjectUsersViewers both
	// carry a `project` field equal to the source project key. We
	// translate via createProjects' (key → cloud_project_key) records.
	if projMap := buildSourceToCloudMap(store, "createProjects", "key", "cloud_project_key"); len(projMap) > 0 {
		out["Projects"] = aggregateUserPermsByEntity(exportDir, mapping,
			[]string{"getProjectUsersScanners", "getProjectUsersViewers"},
			"project", projMap)
	}

	// Quality Profiles: getProfileUsers carries `profileKey` (the
	// source profile's UUID). createProfiles writes the SAME value
	// under the `source_profile_key` field — not `key`, which is
	// reserved for use by the input mappings — so we translate via
	// source_profile_key → cloud_profile_key.
	if profMap := buildSourceToCloudMap(store, "createProfiles", "source_profile_key", "cloud_profile_key"); len(profMap) > 0 {
		out[sectionQualityProfiles] = aggregateUserPermsByEntity(exportDir, mapping,
			[]string{"getProfileUsers"}, "profileKey", profMap)
	}

	// Permission Templates: getTemplateUsersScanners +
	// getTemplateUsersViewers both carry `templateId` (the source
	// template's UUID). createPermissionTemplates writes the same
	// value under `source_template_key`.
	if tplMap := buildSourceToCloudMap(store, "createPermissionTemplates", "source_template_key", "cloud_template_id"); len(tplMap) > 0 {
		out["Permission Templates"] = aggregateUserPermsByEntity(exportDir, mapping,
			[]string{"getTemplateUsersScanners", "getTemplateUsersViewers"},
			"templateId", tplMap)
	}

	// Quality Gates: getGateUsers carries `gateName`, which is the
	// same string the EntityItem.Name field holds — no translation
	// needed. The identity map below routes the lookup straight
	// through gate name.
	out[sectionQualityGates] = aggregateUserPermsByEntity(exportDir, mapping,
		[]string{"getGateUsers"}, "gateName", nil)

	return out
}

// buildSourceToCloudMap reads a create-* task's records from the data
// store and builds a map from the record's source identifier (e.g.
// "key", "id") to the cloud identifier (e.g. "cloud_project_key",
// "cloud_template_id"). Used by collectDroppedUserPermsBySection to
// route per-section lookups.
func buildSourceToCloudMap(store *common.DataStore, taskName, sourceField, cloudField string) map[string]string {
	items, err := store.ReadAll(taskName)
	if err != nil || len(items) == 0 {
		return nil
	}
	out := make(map[string]string, len(items))
	for _, raw := range items {
		src := jsonStr(raw, sourceField)
		cloud := jsonStr(raw, cloudField)
		if src != "" && cloud != "" {
			out[src] = cloud
		}
	}
	return out
}

// aggregateUserPermsByEntity reads the listed user-permission extract
// tasks, groups distinct user logins per parent entity, and returns
// map[entityKey]userCount. parentField names the field on the
// extract record that holds the source identifier of the parent
// entity. When sourceToCloud is non-nil, the source identifier is
// translated via that map (entities not in the map are dropped from
// the result — they no longer exist on the cloud side). When
// sourceToCloud is nil, the source identifier is used directly as
// the result key (used by Quality Gates whose extract carries the
// gate name and where the EntityItem.Name is the same string).
func aggregateUserPermsByEntity(exportDir string, mapping structure.ExtractMapping, taskNames []string, parentField string, sourceToCloud map[string]string) map[string]int {
	if mapping == nil {
		return nil
	}
	// (entityKey → set of logins) so we don't double-count a user
	// who appears in both the "scan" and "user" permission feeds.
	loginsByEntity := map[string]map[string]struct{}{}
	for _, task := range taskNames {
		items, _ := structure.ReadExtractData(exportDir, mapping, task)
		for _, it := range items {
			source := jsonStr(it.Data, parentField)
			if source == "" {
				continue
			}
			key := source
			if sourceToCloud != nil {
				translated, ok := sourceToCloud[source]
				if !ok {
					continue
				}
				key = translated
			}
			login := jsonStr(it.Data, "login")
			if login == "" {
				continue
			}
			set := loginsByEntity[key]
			if set == nil {
				set = map[string]struct{}{}
				loginsByEntity[key] = set
			}
			set[login] = struct{}{}
		}
	}
	if len(loginsByEntity) == 0 {
		return nil
	}
	out := make(map[string]int, len(loginsByEntity))
	for k, set := range loginsByEntity {
		out[k] = len(set)
	}
	return out
}

// attachDroppedUserPerms stamps a |userPerms:N marker on each
// EntityItem whose lookup key carries N > 0 dropped user permissions.
// The lookup key is the EntityItem.Name for the sectionQualityGates
// section (gate names match directly) and the stripped cloud
// identifier from Detail for the other sections.
func attachDroppedUserPerms(items []EntityItem, perms map[string]int, sectionName string) {
	if len(perms) == 0 || len(items) == 0 {
		return
	}
	for i := range items {
		var key string
		if sectionName == sectionQualityGates {
			key = items[i].Name
		} else {
			key = projectCloudKey(items[i].Detail)
		}
		if n, ok := perms[key]; ok && n > 0 {
			items[i].Detail = items[i].Detail + "|userPerms:" + strconv.Itoa(n)
		}
	}
}

// projectSyncCounts holds the issue/hotspot sync a/b/c counts for a
// single cloud project. Built by collectSyncStats from the JSONL
// records syncIssueMetadata and syncHotspotMetadata write per project.
//
// HotspotAckDemoted (#323) is the count of ACKNOWLEDGED source
// hotspots that had to be left in SQC's default TO_REVIEW state. It
// is NOT included in HotspotSynced — the user-facing "synced" notion
// is reserved for hotspots whose state was fully preserved on SQC.
type projectSyncCounts struct {
	IssueActionable   int
	IssueSynced       int
	HotspotActionable int
	HotspotSynced     int
	HotspotAckDemoted int
}

// collectSyncStats reads per-project sync records and returns a map
// keyed by cloud project key. The per-project record schema is
// {synced, line_mismatch, not_found, acknowledged_demoted, actionable};
// `synced`, `actionable`, and (hotspots only) `acknowledged_demoted`
// surface in the Details line (#356 + #323).
func collectSyncStats(store *common.DataStore) map[string]projectSyncCounts {
	out := map[string]projectSyncCounts{}
	items, err := store.ReadAll("syncIssueMetadata")
	if err == nil {
		for _, raw := range items {
			key := jsonStr(raw, "cloud_project_key")
			if key == "" {
				continue
			}
			counts := out[key]
			counts.IssueSynced = jsonInt(raw, "synced")
			counts.IssueActionable = jsonInt(raw, "actionable")
			out[key] = counts
		}
	}
	items, err = store.ReadAll("syncHotspotMetadata")
	if err == nil {
		for _, raw := range items {
			key := jsonStr(raw, "cloud_project_key")
			if key == "" {
				continue
			}
			counts := out[key]
			counts.HotspotSynced = jsonInt(raw, "synced")
			counts.HotspotActionable = jsonInt(raw, "actionable")
			counts.HotspotAckDemoted = jsonInt(raw, "acknowledged_demoted")
			out[key] = counts
		}
	}
	return out
}

// attachSyncStats appends a "|syncStats:i=<synced>/<actionable>(<pct>),h=..."
// marker to each project's Detail field when at least one of issues
// or hotspots had actionable items for that project. The renderer
// parses this marker and emits a one-line "X% of manually-triaged
// items successfully synchronized" comment (PDF + markdown only —
// the predictive report skips it because sync success cannot be
// predicted). #356.
func attachSyncStats(projects []EntityItem, syncMap map[string]projectSyncCounts) {
	if len(syncMap) == 0 || len(projects) == 0 {
		return
	}
	for i := range projects {
		key := projectCloudKey(projects[i].Detail)
		counts, ok := syncMap[key]
		if !ok {
			continue
		}
		if counts.IssueActionable == 0 && counts.HotspotActionable == 0 {
			continue
		}
		projects[i].Detail = projects[i].Detail + "|syncStats:" + encodeSyncStats(counts)
	}
}

// encodeSyncStats renders projectSyncCounts as a compact marker
// payload: "i=<synced>/<actionable>,h=<synced>/<actionable>[,ack=N]".
// Each half is omitted when its actionable count is zero so the
// renderer can split-and-render only the segments that exist. The
// ack= segment is emitted whenever HotspotAckDemoted > 0 — #323's
// "ACKNOWLEDGED demoted to TO_REVIEW" callout.
func encodeSyncStats(c projectSyncCounts) string {
	var parts []string
	if c.IssueActionable > 0 {
		parts = append(parts, fmt.Sprintf("i=%d/%d", c.IssueSynced, c.IssueActionable))
	}
	if c.HotspotActionable > 0 {
		parts = append(parts, fmt.Sprintf("h=%d/%d", c.HotspotSynced, c.HotspotActionable))
	}
	if c.HotspotAckDemoted > 0 {
		parts = append(parts, fmt.Sprintf("ack=%d", c.HotspotAckDemoted))
	}
	return strings.Join(parts, ",")
}

// collectBranchSourcePurged reads importProjectData JSONL and returns,
// per cloud project key, the ordered list of branch names whose source
// text was purged on the source server and were therefore migrated
// without it (issue #425). Branches are de-duplicated and kept in
// first-seen order so the report lists each affected branch once.
func collectBranchSourcePurged(store *common.DataStore) map[string][]string {
	items, err := store.ReadAll("importProjectData")
	if err != nil || len(items) == 0 {
		return nil
	}
	result := make(map[string][]string)
	seen := make(map[string]bool)
	for _, item := range items {
		if !jsonBool(item, "source_purged") {
			continue
		}
		key := jsonStr(item, "cloud_project_key")
		branch := jsonStr(item, "branch")
		if key == "" || branch == "" {
			continue
		}
		dedupKey := key + "\x00" + branch
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true
		result[key] = append(result[key], branch)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// attachBranchSourcePurged appends a "|srcPurged:branchA,branchB" marker
// to each affected project's Detail field. The renderer turns it into a
// one-line "Source code of branch(es) X, Y is missing (likely purged in
// SQS). Migration is executed without the sources." note in the Details
// column, shown in both the actual and predictive reports (#425). The
// project's outcome is unchanged — the branches still migrate their
// measures and issues.
func attachBranchSourcePurged(projects []EntityItem, purgedMap map[string][]string) {
	if len(purgedMap) == 0 || len(projects) == 0 {
		return
	}
	for i := range projects {
		key := projectCloudKey(projects[i].Detail)
		branches, ok := purgedMap[key]
		if !ok || len(branches) == 0 {
			continue
		}
		projects[i].Detail = projects[i].Detail + "|srcPurged:" + strings.Join(branches, ",")
	}
}

// collectNCDFallback reads the setNewCodePeriods JSONL and returns a
// map of cloud_project_key -> the SQS-side NCD type that triggered
// the org-default fallback. Only records with ncd_fallback=true are
// captured (those whose source type wasn't supported at SonarCloud
// project scope — REFERENCE_BRANCH and SPECIFIC_ANALYSIS as of May
// 2026). Issue #135.
func collectNCDFallback(store *common.DataStore) map[string]string {
	items, err := store.ReadAll("setNewCodePeriods")
	if err != nil || len(items) == 0 {
		return nil
	}
	result := make(map[string]string)
	for _, item := range items {
		if !jsonBool(item, "ncd_fallback") {
			continue
		}
		cloudKey := jsonStr(item, "cloud_project_key")
		sqsType := jsonStr(item, "source_ncd_type")
		if cloudKey != "" {
			result[cloudKey] = sqsType
		}
	}
	return result
}

// applyNCDFallbackPartials moves projects whose SQS new-code-definition
// type isn't supported at project scope on SonarQube Cloud
// (REFERENCE_BRANCH, SPECIFIC_ANALYSIS) from the Succeeded bucket into
// Partial — issue #135 / follow-up to #240. The project's Detail
// retains the cloud key (and any |scan: suffix) so dedup and rendering
// keep working; an explanatory Issue is appended explaining what was
// substituted.
func applyNCDFallbackPartials(succeeded, partial []EntityItem, ncdMap map[string]string) ([]EntityItem, []EntityItem) {
	if len(ncdMap) == 0 {
		return succeeded, partial
	}
	keep := succeeded[:0:0]
	for _, item := range succeeded {
		detail := item.Detail
		cloudKey := detail
		if idx := strings.Index(detail, "|scan:"); idx >= 0 {
			cloudKey = detail[:idx]
		}
		sqsType, ok := ncdMap[cloudKey]
		if !ok {
			keep = append(keep, item)
			continue
		}
		moved := item
		moved.Issues = append(append([]string(nil), item.Issues...),
			fmt.Sprintf("The new code period %q does not exist on SonarQube Cloud and has been replaced by the org default.",
				ncdTypeLabel(sqsType)))
		partial = append(partial, moved)
	}
	return keep, partial
}

// ncdTypeLabel maps the SonarQube Server new-code-definition enum
// (REFERENCE_BRANCH, SPECIFIC_ANALYSIS, ...) to the human-readable
// label used in the migration report.
func ncdTypeLabel(sqsType string) string {
	switch sqsType {
	case "REFERENCE_BRANCH":
		return "reference branch"
	case "SPECIFIC_ANALYSIS":
		return "analysis id"
	}
	return sqsType
}

// collectNCDBranchOverrides reads the setNewCodePeriods JSONL and
// returns the set of cloud_project_keys for projects that had at
// least one per-branch NCD override on SonarQube Server. SonarQube
// Cloud has no per-branch NCD; those branches silently fall back to
// the project-level NCD, so the report should flag the project as
// Partial (#240 follow-up).
func collectNCDBranchOverrides(store *common.DataStore) map[string]bool {
	items, err := store.ReadAll("setNewCodePeriods")
	if err != nil || len(items) == 0 {
		return nil
	}
	result := make(map[string]bool)
	for _, item := range items {
		if !jsonBool(item, "ncd_branch_override") {
			continue
		}
		cloudKey := jsonStr(item, "cloud_project_key")
		if cloudKey != "" {
			result[cloudKey] = true
		}
	}
	return result
}

// applyNCDBranchOverridePartials moves projects whose branches had
// custom NCD overrides on SonarQube Server from Succeeded into
// Partial — those overrides have no SonarQube Cloud equivalent and
// the branches will silently inherit the project-level NCD.
func applyNCDBranchOverridePartials(succeeded, partial []EntityItem, overrides map[string]bool) ([]EntityItem, []EntityItem) {
	if len(overrides) == 0 {
		return succeeded, partial
	}
	const issue = "Per-branch new code period overrides do not exist on SonarQube Cloud; branches will inherit the project-level new code period."
	// First: tag matching projects already in Partial so they accumulate
	// the explanatory Issue rather than appearing twice.
	for i := range partial {
		key := projectCloudKey(partial[i].Detail)
		if overrides[key] {
			partial[i].Issues = append(partial[i].Issues, issue)
			delete(overrides, key)
		}
	}
	// Then move matching Succeeded entries to Partial.
	keep := succeeded[:0:0]
	for _, item := range succeeded {
		key := projectCloudKey(item.Detail)
		if !overrides[key] {
			keep = append(keep, item)
			continue
		}
		moved := item
		moved.Issues = append(append([]string(nil), item.Issues...), issue)
		partial = append(partial, moved)
	}
	return keep, partial
}

// projectCloudKey strips trailing | markers (|scan:, |ncdFallback:) so
// the raw cloud_project_key alone is available for lookup.
func projectCloudKey(detail string) string {
	if idx := strings.Index(detail, "|"); idx >= 0 {
		return detail[:idx]
	}
	return detail
}

// jsonBool extracts a bool value for a key from a JSONL record.
// Falls back to false on missing key, non-bool value, or parse error.
// jsonInt extracts an integer field from a json.RawMessage. Returns 0
// when the field is missing or not a number; missing fields are
// indistinguishable from explicit zeros, which matches the callers'
// "non-zero means something happened" gates.
func jsonInt(raw json.RawMessage, key string) int {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0
	}
	v, ok := obj[key]
	if !ok {
		return 0
	}
	var n float64
	if err := json.Unmarshal(v, &n); err != nil {
		return 0
	}
	return int(n)
}

func jsonBool(raw json.RawMessage, key string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	v, ok := obj[key]
	if !ok {
		return false
	}
	var b bool
	_ = json.Unmarshal(v, &b)
	return b
}

// extractRunID extracts the run ID from a directory path (last path component).
func extractRunID(runDir string) string {
	for i := len(runDir) - 1; i >= 0; i-- {
		if runDir[i] == '/' || runDir[i] == '\\' {
			return runDir[i+1:]
		}
	}
	return runDir
}

// jsonStr extracts a string field from a json.RawMessage.
func jsonStr(raw json.RawMessage, key string) string {
	return common.ExtractField(raw, key)
}

// collectGlobalSettings renders the Global Settings section (issue
// #186). Each setGlobalSettings JSONL record carries an outcomes[]
// list — one entry per (setting × org) — with a pre-rendered Detail
// string specific to that org. The collector emits one EntityItem per
// outcome and routes it to the right bucket by status. Detail is
// forwarded verbatim so the migrate task fully controls the wording
// (e.g. "Applied (value=X)" for direct org applies,
// "Applied to all projects (values=[...]) (failed: projX)" for the
// runtime fan-out path).
func collectGlobalSettings(store *common.DataStore, def sectionDef) Section {
	// Read records from BOTH tasks that contribute to this section:
	// the regular setGlobalSettings output AND
	// setGlobalNewCodePeriod, which writes a single synthetic record
	// (key="newCodePeriod") with one outcome per org so the
	// new-code-period migration shows up alongside the other global
	// settings (issue #136 follow-up).
	var items [][]byte
	for _, item := range readJSONLOrNil(store, def.OutputTask) {
		items = append(items, item)
	}
	for _, item := range readJSONLOrNil(store, "setGlobalNewCodePeriod") {
		items = append(items, item)
	}
	if len(items) == 0 {
		return Section{Name: def.Name}
	}

	var succeeded, nearPerfect, partial, skipped, failed []EntityItem
	for _, raw := range items {
		key := jsonStr(raw, def.NameField)
		// #230 Y1: the synthetic newCodePeriod record is emitted by
		// setGlobalNewCodePeriod, one outcome per org. A failed org
		// means the global new-code-period couldn't be set on that
		// SQC org — the source is migrated, just not the value, so
		// the row routes to NearPerfect (yellow) rather than Failed.
		ncdRecord := key == "newCodePeriod"
		for _, oc := range parseOutcomes(raw) {
			// AI Code Fix rows attach a |nearperfect suffix to the
			// Detail when the row should land in the NearPerfect
			// bucket rather than Succeeded (#251). Strip the marker
			// here so it doesn't reach the rendered cell.
			detail := oc.Detail
			markedNearPerfect := false
			if strings.HasSuffix(detail, migrate.AiCodeFixNearPerfectMarker) {
				detail = strings.TrimSuffix(detail, migrate.AiCodeFixNearPerfectMarker)
				markedNearPerfect = true
			}
			item := EntityItem{
				Name:         key,
				Organization: oc.Org,
				Detail:       detail,
			}
			switch oc.Status {
			case "applied", "applied-to-projects":
				if markedNearPerfect {
					nearPerfect = append(nearPerfect, item)
				} else {
					succeeded = append(succeeded, item)
				}
			case "partial":
				// Per-row Detail already enumerates the
				// exception projects — keep Issues unset.
				partial = append(partial, item)
			case "skipped":
				item.SkipReason = oc.Reason
				skipped = append(skipped, item)
			case "failed":
				item.ErrorMessage = oc.Reason
				if ncdRecord {
					nearPerfect = append(nearPerfect, item)
				} else {
					failed = append(failed, item)
				}
			}
		}
	}
	return Section{
		Name:        def.Name,
		Succeeded:   succeeded,
		NearPerfect: nearPerfect,
		Partial:     partial,
		Skipped:     skipped,
		Failed:      failed,
	}
}

// readJSONLOrNil reads JSONL items from a task, returning nil (rather
// than an error) when the task hasn't run. Used by sections that merge
// records from multiple tasks where any one of them is optional.
func readJSONLOrNil(store *common.DataStore, taskName string) []json.RawMessage {
	items, err := store.ReadAll(taskName)
	if err != nil {
		return nil
	}
	return items
}

// outcomeRecord mirrors the orgOutcome shape that
// setGlobalSettings writes for each (setting × org). Kept private to
// the report package because the migrate package owns the schema.
type outcomeRecord struct {
	Org    string `json:"org"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Reason string `json:"reason"`
}

// appendBuiltInGroupSkips injects a single Skipped EntityItem into the
// Groups section for every SonarQube Server built-in group (e.g.
// "sonar-users") found in generateGroupMappings. Both real migrate
// (runCreateGroups) and the predictive synthesizer short-circuit
// creation for these names, so without this injection the report
// would have no row at all for them.
func appendBuiltInGroupSkips(store *common.DataStore, skipped []EntityItem) []EntityItem {
	items, err := store.ReadAll("generateGroupMappings")
	if err != nil {
		return skipped
	}
	seen := make(map[string]bool, len(items))
	for _, raw := range items {
		name := jsonStr(raw, "name")
		if name == "" || seen[name] {
			continue
		}
		note, ok := migrate.BuiltInGroupSkipNote(name)
		if !ok {
			continue
		}
		seen[name] = true
		skipped = append(skipped, EntityItem{
			Name:       name,
			Detail:     note,
			SkipReason: SkipReasonBuiltIn,
		})
	}
	return skipped
}

// parseOutcomes decodes the outcomes[] field from a setGlobalSettings
// JSONL record. Returns nil when the field is missing or malformed.
func parseOutcomes(raw json.RawMessage) []outcomeRecord {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	arr, ok := obj["outcomes"]
	if !ok {
		return nil
	}
	var out []outcomeRecord
	_ = json.Unmarshal(arr, &out)
	return out
}
