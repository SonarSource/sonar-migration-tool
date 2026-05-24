package summary

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/analysis"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
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

	scanHistoryMap := collectScanHistory(store)
	extractMapping, _ := structure.GetUniqueExtracts(exportDir)

	var sections []Section
	for _, def := range sectionDefs {
		section := collectSection(store, def, failuresByType, configFailures, exportDir, extractMapping)
		if def.Name == "Projects" {
			attachScanHistory(section.Succeeded, scanHistoryMap)
		}
		sections = append(sections, section)
	}

	runID := extractRunID(runDir)
	return &MigrationSummary{
		RunID:       runID,
		GeneratedAt: time.Now(),
		Sections:    sections,
		Limitations: collectLimitations(runDir, exportDir, extractMapping),
	}, nil
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
	out = append(out, collectNCDLimitations(runDir, exportDir, mapping)...)
	return out
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

	// Augment skipped with built-in / unused items derived from extract data.
	if def.ExtractTask != "" && extractMapping != nil {
		skipped = append(skipped, collectExtractSkipped(def, exportDir, extractMapping, store)...)
	}

	// SonarQube Cloud has no portfolio hierarchy: any source portfolio that
	// has subportfolios is migrated as a flat project list, so its perimeter
	// may differ from the source. Mark those as Partial.
	if def.Name == "Portfolios" && extractMapping != nil {
		parents := portfoliosWithSubportfolios(exportDir, extractMapping)
		succeeded, partial = markPartialPortfolios(store, succeeded, partial, parents)
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

	// Quality gates whose conditions were remapped (#143) or dropped
	// because no SQC equivalent exists are reported as Partial. The
	// per-condition decisions are written by addGateConditions to a
	// sidecar JSONL.
	if def.Name == "Quality Gates" {
		notes := collectGateMappingNotes(store.BaseDir())
		succeeded, partial = applyGateMappingNotes(succeeded, partial, notes)
	}

	return Section{
		Name:      def.Name,
		Succeeded: succeeded,
		Partial:   partial,
		Failed:    failed,
		Skipped:   skipped,
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
	if def.Name == "Quality Profiles" {
		return name + "|" + language
	}
	return name
}

func extractKeyFor(def sectionDef, name, language, serverURL string) string {
	if def.Name == "Quality Profiles" {
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

// collectScanHistory reads importScanHistory JSONL and returns a map of
// cloud_project_key -> status ("success", "failed", "skipped").
func collectScanHistory(store *common.DataStore) map[string]string {
	items, err := store.ReadAll("importScanHistory")
	if err != nil || len(items) == 0 {
		return nil
	}
	result := make(map[string]string)
	for _, item := range items {
		key := jsonStr(item, "cloud_project_key")
		status := jsonStr(item, "status")
		if key != "" {
			result[key] = status
		}
	}
	return result
}

// attachScanHistory adds scan history status to project EntityItems.
func attachScanHistory(projects []EntityItem, scanMap map[string]string) {
	if scanMap == nil {
		return
	}
	for i := range projects {
		cloudKey := projects[i].Detail
		if status, ok := scanMap[cloudKey]; ok {
			projects[i].Detail = cloudKey + "|scan:" + status
		}
	}
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

	var succeeded, partial, skipped, failed []EntityItem
	for _, raw := range items {
		key := jsonStr(raw, def.NameField)
		for _, oc := range parseOutcomes(raw) {
			item := EntityItem{
				Name:         key,
				Organization: oc.Org,
				Detail:       oc.Detail,
			}
			switch oc.Status {
			case "applied", "applied-to-projects":
				succeeded = append(succeeded, item)
			case "partial":
				// Per-row Detail already enumerates the
				// exception projects — keep Issues unset.
				partial = append(partial, item)
			case "skipped":
				item.SkipReason = oc.Reason
				skipped = append(skipped, item)
			case "failed":
				item.ErrorMessage = oc.Reason
				failed = append(failed, item)
			}
		}
	}
	return Section{
		Name:      def.Name,
		Succeeded: succeeded,
		Partial:   partial,
		Skipped:   skipped,
		Failed:    failed,
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
