package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

func scanHistoryTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "importScanHistory",
			Editions:     common.AllEditions,
			Dependencies: []string{"createProjects", "setProjectProfiles"},
			Run:          runImportScanHistory,
		},
	}
}

func runImportScanHistory(ctx context.Context, e *Executor) error {
	projects, err := e.Store.ReadAll("createProjects")
	if err != nil {
		return fmt.Errorf("importScanHistory: reading createProjects: %w", err)
	}

	w, err := e.Store.Writer("importScanHistory")
	if err != nil {
		return err
	}

	for i, proj := range projects {
		cloudKey := extractField(proj, "cloud_project_key")
		orgKey := extractField(proj, "sonarcloud_org_key")
		serverURL := extractField(proj, "server_url")
		serverKey := extractField(proj, "key")

		if cloudKey == "" || orgKey == "" {
			continue
		}

		e.Logger.Info("importing scan history", "project", cloudKey, "progress", fmt.Sprintf("%d/%d", i+1, len(projects)))

		sqBranches := collectBranchInfo(e, serverURL, serverKey)
		if len(sqBranches) == 0 {
			sqBranches = []branchInfo{{Name: "main", IsMain: true}}
		}

		scMainBranch := fetchSCMainBranch(ctx, e, cloudKey)
		importProjectBranches(ctx, e, proj, sqBranches, scMainBranch, w)
	}
	return nil
}

// fetchSCMainBranch queries SonarCloud for the main branch name of a project.
// Returns empty string if unavailable.
func fetchSCMainBranch(ctx context.Context, e *Executor, cloudKey string) string {
	if e.Cloud == nil || e.Cloud.Branches == nil {
		return ""
	}
	scBranches, err := e.Cloud.Branches.List(ctx, cloudKey)
	if err != nil {
		e.Logger.Warn("failed to fetch SC branches, using SQ branch names", "project", cloudKey, "err", err)
		return ""
	}
	for _, b := range scBranches {
		if b.IsMain {
			return b.Name
		}
	}
	return ""
}

// importProjectBranches imports scan history for every branch of one project.
func importProjectBranches(ctx context.Context, e *Executor, proj json.RawMessage, sqBranches []branchInfo, scMainBranch string, w *common.ChunkWriter) {
	cloudKey := extractField(proj, "cloud_project_key")
	orgKey := extractField(proj, "sonarcloud_org_key")
	serverURL := extractField(proj, "server_url")
	serverKey := extractField(proj, "key")

	for _, branch := range sqBranches {
		targetBranch := branch.Name
		if branch.IsMain && scMainBranch != "" {
			targetBranch = scMainBranch
		}
		result, err := importBranch(ctx, e, importBranchInput{
			CloudKey:     cloudKey,
			OrgKey:       orgKey,
			ServerURL:    serverURL,
			ServerKey:    serverKey,
			Branch:       branch.Name,
			TargetBranch: targetBranch,
		})
		if err != nil {
			logAPIWarn(e.Logger, "scan history import failed", err, "project", cloudKey, "branch", branch.Name)
			result = &importResult{Status: "failed", Error: err.Error()}
		}

		record, _ := json.Marshal(map[string]any{
			"cloud_project_key": cloudKey,
			"branch":            branch.Name,
			"status":            result.Status,
			"task_id":           result.TaskID,
			"error":             result.Error,
		})
		w.WriteOne(record) //nolint:errcheck
	}
}

type importBranchInput struct {
	CloudKey     string
	OrgKey       string
	ServerURL    string
	ServerKey    string
	Branch       string // SQ branch name — used to filter extracted data
	TargetBranch string // SC branch name — used in protobuf metadata and CE submit
}

type importResult struct {
	Status string
	TaskID string
	Error  string
}

func importBranch(ctx context.Context, e *Executor, input importBranchInput) (*importResult, error) {
	targetBranch := input.TargetBranch
	if targetBranch == "" {
		targetBranch = input.Branch
	}

	issues := loadExtractedIssues(e, input.ServerURL, input.ServerKey, input.Branch)
	hotspotIssues := loadExtractedHotspots(e, input.ServerURL, input.ServerKey, input.Branch)
	extIssues, adHocRules := loadExtractedExternalIssues(e, input.ServerURL, input.ServerKey, input.Branch)
	allComponents := loadExtractedComponents(e, input.ServerURL, input.ServerKey, input.Branch)
	sources := loadExtractedSources(e, input.ServerURL, input.ServerKey, input.Branch)
	activeRules := loadExtractedActiveRules(e, input.ServerURL, input.ServerKey)

	// Combine native issues with hotspot issues for the regular Issue protobuf.
	issues = append(issues, hotspotIssues...)

	// Only include components that have matching source code (matches cloudvoyager behavior).
	sourceKeys := buildSourceKeySet(sources)
	components := filterComponentsWithSource(allComponents, sourceKeys)

	if len(components) == 0 {
		return &importResult{Status: "skipped"}, nil
	}

	// Fetch SC quality profiles (CloudVoyager uses SC profile keys, not SQ keys).
	// The CE validates that qprofile keys in the metadata exist in the SC instance.
	scProfileByLang := buildSCProfileMap(ctx, e, input.OrgKey)

	// Filter profiles and rules to languages present in the project (matches cloudvoyager).
	projectLangs := collectProjectLanguages(components)
	activeRules = filterRulesByLanguage(activeRules, projectLangs)

	qprofiles := buildProjectQProfiles(projectLangs, scProfileByLang)
	remapActiveRuleProfiles(activeRules, scProfileByLang)

	root, fileComps, cr := scanreport.BuildComponents(input.CloudKey, components)
	pbIssues := scanreport.BuildIssues(issues, cr)
	pbExtIssues := scanreport.BuildExternalIssues(extIssues, cr)
	pbAdHocRules := scanreport.BuildAdHocRules(adHocRules)
	pbActiveRules := scanreport.BuildActiveRules(activeRules)

	pbSources := make(map[int32]string)
	for _, s := range sources {
		if ref, ok := cr.Refs()[s.Component]; ok {
			pbSources[ref] = s.Source
		}
	}

	now := time.Now()
	changesets := buildChangesetMap(cr, components, now)

	// Backdate changesets so each issue gets its original SonarQube creation date.
	// Build a component-key-keyed alias map (same pointers) for BackdateChangesets.
	changesetsByKey := make(map[string]*pb.Changesets, len(changesets))
	for compKey, ref := range cr.Refs() {
		if cs, ok := changesets[ref]; ok {
			changesetsByKey[compKey] = cs
		}
	}
	extracted := toExtractedIssues(issues)
	scanreport.BackdateChangesets(extracted, changesetsByKey, now)

	metadata := scanreport.BuildMetadata(scanreport.MetadataInput{
		AnalysisDate:   now,
		OrgKey:         input.OrgKey,
		ProjectKey:     input.CloudKey,
		BranchName:     targetBranch,
		BranchType:     pb.Metadata_BRANCH,
		QProfiles:      qprofiles,
		FileCountByExt: countFilesByExt(components),
	}, root.Ref)

	reportData := &scanreport.ReportData{
		Metadata:       metadata,
		RootComponent:  root,
		FileComponents: fileComps,
		Issues:         pbIssues,
		ExternalIssues: pbExtIssues,
		Measures:       make(map[int32][]*pb.Measure),
		Changesets:     changesets,
		ActiveRules:    pbActiveRules,
		AdHocRules:     pbAdHocRules,
		Sources:        pbSources,
	}

	zipBytes, err := scanreport.PackageReport(reportData)
	if err != nil {
		return nil, fmt.Errorf("packaging report: %w", err)
	}

	e.Logger.Info("report packaged",
		"project", input.CloudKey, "sourceBranch", input.Branch, "targetBranch", targetBranch,
		"zipSizeBytes", len(zipBytes),
		"zipSizeMB", fmt.Sprintf("%.1f", float64(len(zipBytes))/(1024*1024)),
		"components", len(fileComps),
		"issues", len(issues),
		"externalIssues", len(extIssues),
		"sources", len(pbSources),
		"activeRules", len(activeRules),
	)

	cfg := scanreport.SubmitConfig{
		CloudURL:   e.CloudURL,
		ProjectKey: input.CloudKey,
		OrgKey:     input.OrgKey,
		BranchName: targetBranch,
	}

	result, err := scanreport.SubmitReport(ctx, e.Raw.HTTPClient(), cfg, zipBytes)
	if err != nil {
		return nil, fmt.Errorf("submitting report: %w", err)
	}

	e.Logger.Info("CE task submitted", "project", input.CloudKey, "targetBranch", targetBranch, "taskId", result.TaskID)

	if err := scanreport.PollCETask(ctx, e.Raw.HTTPClient(), e.CloudURL, result.TaskID, e.Logger); err != nil {
		return nil, fmt.Errorf("CE task failed: %w", err)
	}

	return &importResult{Status: "success", TaskID: result.TaskID}, nil
}

type branchInfo struct {
	Name   string
	IsMain bool
}

// collectBranchInfo reads extracted branch data for a project, returning
// each LONG branch's name and whether it is the main branch.
func collectBranchInfo(e *Executor, serverURL, serverKey string) []branchInfo {
	items, err := readExtractItems(e, "getBranches")
	if err != nil {
		return nil
	}
	var branches []branchInfo
	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}
		projKey := extractField(item.Data, "projectKey")
		if projKey != serverKey {
			continue
		}
		branchType := strings.ToUpper(extractField(item.Data, "type"))
		if branchType == "SHORT" {
			continue
		}
		name := extractField(item.Data, "name")
		if name != "" {
			isMain := common.ExtractBool(item.Data, "isMain")
			branches = append(branches, branchInfo{Name: name, IsMain: isMain})
		}
	}
	return branches
}

type sourceRecord struct {
	Component string
	Source    string
}

func loadExtractedSources(e *Executor, serverURL, serverKey, branch string) []sourceRecord {
	items, err := readExtractItems(e, "getProjectSourceCode")
	if err != nil {
		return nil
	}
	var sources []sourceRecord
	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}
		if extractField(item.Data, "projectKey") != serverKey {
			continue
		}
		if extractField(item.Data, "branch") != branch {
			continue
		}
		sources = append(sources, sourceRecord{
			Component: extractField(item.Data, "key"),
			Source:    extractField(item.Data, "source"),
		})
	}
	return sources
}

func loadExtractedIssues(e *Executor, serverURL, serverKey, branch string) []scanreport.IssueInput {
	items, err := readExtractItems(e, "getProjectIssuesFull")
	if err != nil {
		return nil
	}
	var issues []scanreport.IssueInput
	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}
		if extractField(item.Data, "projectKey") != serverKey {
			continue
		}
		if extractField(item.Data, "branch") != branch {
			continue
		}
		// Exclude CLOSED issues and issues resolved by code fix (FIXED).
		// These have no Cloud counterpart — the scanner report only creates
		// them as OPEN, so recreating CLOSED/FIXED would create phantom issues.
		status := strings.ToUpper(extractField(item.Data, "status"))
		resolution := strings.ToUpper(extractField(item.Data, "resolution"))
		if status == "CLOSED" {
			continue
		}
		if resolution == "FIXED" {
			continue
		}
		rule := extractField(item.Data, "rule")
		repo, key := splitRule(rule)
		// Skip external issues — they use a different protobuf message type.
		if !sonarCloudRuleRepos[repo] || strings.HasPrefix(repo, "external_") {
			continue
		}
		issues = append(issues, scanreport.IssueInput{
			Key:          extractField(item.Data, "key"),
			CreationDate: parseISODate(extractField(item.Data, "creationDate")),
			RuleRepo:     repo,
			RuleKey:      key,
			Message:      extractField(item.Data, "message"),
			Severity:     extractField(item.Data, "severity"),
			StartLine:    extractInt32(item.Data, "textRange", "startLine"),
			EndLine:      extractInt32(item.Data, "textRange", "endLine"),
			StartOff:     extractInt32(item.Data, "textRange", "startOffset"),
			EndOff:       extractInt32(item.Data, "textRange", "endOffset"),
			Component:    extractField(item.Data, "component"),
		})
	}
	return issues
}

// loadExtractedExternalIssues loads external issues (from third-party linters)
// that require the ExternalIssue protobuf message. Classification follows
// CloudVoyager's is-external-issue.js: issues from repos NOT in
// sonarCloudRuleRepos or prefixed with "external_" are external.
func loadExtractedExternalIssues(e *Executor, serverURL, serverKey, branch string) ([]scanreport.ExternalIssueInput, []scanreport.AdHocRuleInput) {
	items, err := readExtractItems(e, "getProjectIssuesFull")
	if err != nil {
		return nil, nil
	}
	seenRules := make(map[string]bool)
	var extIssues []scanreport.ExternalIssueInput
	var adHocRules []scanreport.AdHocRuleInput

	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}
		if extractField(item.Data, "projectKey") != serverKey {
			continue
		}
		if extractField(item.Data, "branch") != branch {
			continue
		}
		issue, rule, ok := classifyExternalIssue(item.Data)
		if !ok {
			continue
		}
		extIssues = append(extIssues, issue)
		if !seenRules[rule.EngineID+":"+rule.RuleID] {
			seenRules[rule.EngineID+":"+rule.RuleID] = true
			adHocRules = append(adHocRules, rule)
		}
	}
	return extIssues, adHocRules
}

// classifyExternalIssue checks whether a single extracted issue record is an
// external issue (third-party linter). If so, it returns the ExternalIssueInput
// and a corresponding AdHocRuleInput. Returns ok=false for native or excluded issues.
func classifyExternalIssue(data json.RawMessage) (scanreport.ExternalIssueInput, scanreport.AdHocRuleInput, bool) {
	status := strings.ToUpper(extractField(data, "status"))
	resolution := strings.ToUpper(extractField(data, "resolution"))
	if status == "CLOSED" || resolution == "FIXED" {
		return scanreport.ExternalIssueInput{}, scanreport.AdHocRuleInput{}, false
	}
	rule := extractField(data, "rule")
	repo, key := splitRule(rule)
	if repo == "" {
		return scanreport.ExternalIssueInput{}, scanreport.AdHocRuleInput{}, false
	}
	if !strings.HasPrefix(repo, "external_") && sonarCloudRuleRepos[repo] {
		return scanreport.ExternalIssueInput{}, scanreport.AdHocRuleInput{}, false
	}
	engineID := strings.TrimPrefix(repo, "external_")
	return scanreport.ExternalIssueInput{
		EngineID:  engineID,
		RuleID:    key,
		Message:   extractField(data, "message"),
		Severity:  extractField(data, "severity"),
		Type:      extractField(data, "type"),
		StartLine: extractInt32(data, "textRange", "startLine"),
		EndLine:   extractInt32(data, "textRange", "endLine"),
		StartOff:  extractInt32(data, "textRange", "startOffset"),
		EndOff:    extractInt32(data, "textRange", "endOffset"),
		Component: extractField(data, "component"),
	}, scanreport.AdHocRuleInput{
		EngineID:    engineID,
		RuleID:      key,
		Name:        key,
		Description: fmt.Sprintf("Rule from %s plugin", engineID),
		Severity:    extractField(data, "severity"),
		Type:        extractField(data, "type"),
	}, true
}

// loadExtractedHotspots loads hotspots from the extract and converts them
// to IssueInput for inclusion in the scanner report protobuf. Hotspots
// are mapped to regular issues with severity derived from vulnerability
// probability (matching CloudVoyager behavior).
func loadExtractedHotspots(e *Executor, serverURL, serverKey, branch string) []scanreport.IssueInput {
	items, err := readExtractItems(e, "getProjectHotspotsFull")
	if err != nil {
		return nil
	}
	var hotspots []scanreport.IssueInput
	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}
		projKey := extractField(item.Data, "project")
		if projKey == "" {
			projKey = extractField(item.Data, "projectKey")
		}
		if projKey != serverKey {
			continue
		}
		br := extractField(item.Data, "branch")
		if br != "" && br != branch {
			continue
		}
		ruleKey := extractField(item.Data, "ruleKey")
		if ruleKey == "" {
			// Try nested rule.key
			ruleKey = extractNestedRuleKey(item.Data)
		}
		repo, key := splitRule(ruleKey)
		line := extractInt32Field(item.Data, "line")
		severity := mapVulnProbToSeverity(extractField(item.Data, "vulnerabilityProbability"))
		hotspots = append(hotspots, scanreport.IssueInput{
			Key:          extractField(item.Data, "key"),
			CreationDate: parseISODate(extractField(item.Data, "creationDate")),
			RuleRepo:     repo,
			RuleKey:      key,
			Message:      extractField(item.Data, "message"),
			Severity:     severity,
			StartLine:    line,
			EndLine:      line,
			Component:    extractField(item.Data, "component"),
		})
	}
	return hotspots
}

func extractNestedRuleKey(data json.RawMessage) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return ""
	}
	ruleRaw, ok := obj["rule"]
	if !ok {
		return ""
	}
	var rule map[string]json.RawMessage
	if err := json.Unmarshal(ruleRaw, &rule); err != nil {
		return ""
	}
	keyRaw, ok := rule["key"]
	if !ok {
		return ""
	}
	var key string
	json.Unmarshal(keyRaw, &key)
	return key
}

func mapVulnProbToSeverity(prob string) string {
	switch strings.ToUpper(prob) {
	case "HIGH":
		return "CRITICAL"
	case "MEDIUM":
		return "MAJOR"
	case "LOW":
		return "MINOR"
	default:
		return "MAJOR"
	}
}

func loadExtractedComponents(e *Executor, serverURL, serverKey, branch string) []scanreport.ComponentInput {
	items, err := readExtractItems(e, "getProjectComponentTree")
	if err != nil {
		return nil
	}
	var components []scanreport.ComponentInput
	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}
		if extractField(item.Data, "projectKey") != serverKey {
			continue
		}
		if extractField(item.Data, "branch") != branch {
			continue
		}
		lines := extractInt32Field(item.Data, "lines")
		if lines == 0 {
			lines = extractMeasureInt32(item.Data, "ncloc")
		}
		components = append(components, scanreport.ComponentInput{
			Key:      extractField(item.Data, "key"),
			Name:     extractField(item.Data, "name"),
			Path:     extractField(item.Data, "path"),
			Language: extractField(item.Data, "language"),
			Lines:    lines,
		})
	}
	return components
}

// sonarCloudRuleRepos lists rule repositories known to exist in SonarCloud.
// Rules from external/third-party repos are excluded from the report to
// prevent CE processing errors.
var sonarCloudRuleRepos = map[string]bool{
	"common-java": true, "java": true, "squid": true,
	"common-js": true, "javascript": true, "typescript": true,
	"common-ts": true, "css": true, "web": true,
	"common-py": true, "python": true, "pythonbugs": true,
	"common-cs": true, "csharpsquid": true, "roslyn.sonaranalyzer.security.cs": true,
	"common-vbnet": true, "vbnet": true,
	"common-kotlin": true, "kotlin": true,
	"common-ruby": true, "ruby": true,
	"common-scala": true, "scala": true,
	"common-go": true, "go": true,
	"common-php": true, "php": true, "phpsecurity": true,
	"common-swift": true, "swift": true,
	"common-c": true, "c": true, "cpp": true, "common-cpp": true,
	"common-objc": true, "objc": true,
	"common-xml": true, "xml": true,
	"common-html": true, "html": true,
	"common-text": true, "text": true, "secrets": true,
	"plsql": true, "tsql": true, "abap": true, "cobol": true, "rpg": true,
	"flex": true, "pli": true, "apex": true, "cloudformation": true,
	"terraform": true, "docker": true, "kubernetes": true,
	"azureresourcemanager": true, "ipython": true,
	"jssecurity": true, "tssecurity": true, "javasecurity": true,
}

func loadExtractedActiveRules(e *Executor, serverURL, serverKey string) []scanreport.ActiveRuleInput {
	items, err := readExtractItems(e, "getActiveProfileRules")
	if err != nil {
		return nil
	}
	var rules []scanreport.ActiveRuleInput
	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}
		rule := extractField(item.Data, "key")
		repo, key := splitRule(rule)
		// Only include rules from known SonarCloud repositories.
		if !sonarCloudRuleRepos[repo] {
			continue
		}
		rules = append(rules, scanreport.ActiveRuleInput{
			RuleRepo:    repo,
			RuleKey:     key,
			Severity:    extractField(item.Data, "severity"),
			QProfileKey: extractField(item.Data, "qProfile"),
			Language:    extractField(item.Data, "lang"),
		})
	}
	return rules
}

func loadExtractedQProfiles(e *Executor, serverURL, serverKey string) []scanreport.QProfileInfo {
	items, err := readExtractItems(e, "getProfiles")
	if err != nil {
		return nil
	}
	var profiles []scanreport.QProfileInfo
	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}
		profiles = append(profiles, scanreport.QProfileInfo{
			Key:      extractField(item.Data, "key"),
			Name:     extractField(item.Data, "name"),
			Language: extractField(item.Data, "language"),
		})
	}
	return profiles
}

// buildSCProfileMap fetches quality profiles from SonarCloud and returns them
// keyed by lower-cased language. Falls back to an empty map on error.
func buildSCProfileMap(ctx context.Context, e *Executor, orgKey string) map[string]scanreport.QProfileInfo {
	profiles := make(map[string]scanreport.QProfileInfo)
	if e.Cloud == nil || e.Cloud.QualityProfiles == nil {
		return profiles
	}
	scProfiles, err := e.Cloud.QualityProfiles.Search(ctx, orgKey)
	if err != nil {
		e.Logger.Warn("failed to fetch SC profiles, falling back to extract profiles", "err", err)
		return profiles
	}
	for _, p := range scProfiles {
		lang := strings.ToLower(p.Language)
		if _, exists := profiles[lang]; exists {
			continue
		}
		var rulesUpdated time.Time
		if p.RulesUpdatedAt != "" {
			rulesUpdated, _ = time.Parse(time.RFC3339, p.RulesUpdatedAt)
		}
		profiles[lang] = scanreport.QProfileInfo{
			Key:            p.Key,
			Name:           p.Name,
			Language:       lang,
			RulesUpdatedAt: rulesUpdated,
		}
	}
	return profiles
}

// buildProjectQProfiles returns the SC QProfileInfo values for each language
// present in the project.
func buildProjectQProfiles(projectLangs map[string]bool, scProfileByLang map[string]scanreport.QProfileInfo) []scanreport.QProfileInfo {
	var qprofiles []scanreport.QProfileInfo
	for lang := range projectLangs {
		if scP, ok := scProfileByLang[lang]; ok {
			qprofiles = append(qprofiles, scP)
		}
	}
	return qprofiles
}

// remapActiveRuleProfiles rewrites each rule's QProfileKey to the matching SC
// profile key for its language.
func remapActiveRuleProfiles(rules []scanreport.ActiveRuleInput, scProfileByLang map[string]scanreport.QProfileInfo) {
	for i := range rules {
		lang := strings.ToLower(rules[i].Language)
		if scP, ok := scProfileByLang[lang]; ok {
			rules[i].QProfileKey = scP.Key
		}
	}
}

func buildChangesetMap(cr *scanreport.ComponentRef, components []scanreport.ComponentInput, date time.Time) map[int32]*pb.Changesets {
	changesets := make(map[int32]*pb.Changesets)
	for _, comp := range components {
		if ref, ok := cr.Refs()[comp.Key]; ok && comp.Lines > 0 {
			changesets[ref] = scanreport.BuildDefaultChangesets(ref, int(comp.Lines), date)
		}
	}
	return changesets
}

func toExtractedIssues(issues []scanreport.IssueInput) []scanreport.ExtractedIssue {
	result := make([]scanreport.ExtractedIssue, 0, len(issues))
	for _, iss := range issues {
		result = append(result, scanreport.ExtractedIssue{
			Key:          iss.Key,
			Component:    iss.Component,
			CreationDate: iss.CreationDate,
			StartLine:    iss.StartLine,
			EndLine:      iss.EndLine,
		})
	}
	return result
}

// buildSourceKeySet returns a set of component keys that have extracted source code.
func buildSourceKeySet(sources []sourceRecord) map[string]bool {
	keys := make(map[string]bool, len(sources))
	for _, s := range sources {
		keys[s.Component] = true
	}
	return keys
}

// filterComponentsWithSource returns only components that have matching source code.
func filterComponentsWithSource(components []scanreport.ComponentInput, sourceKeys map[string]bool) []scanreport.ComponentInput {
	var filtered []scanreport.ComponentInput
	for _, c := range components {
		if sourceKeys[c.Key] {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func countFilesByExt(components []scanreport.ComponentInput) map[string]int32 {
	counts := make(map[string]int32)
	for _, c := range components {
		if c.Language != "" {
			counts[strings.ToLower(c.Language)]++
		}
	}
	return counts
}

// parseISODate parses a SonarQube date string in RFC3339 or legacy UTC-offset format.
// Returns zero time on parse failure.
func parseISODate(dateStr string) time.Time {
	if dateStr == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, dateStr)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05-0700", dateStr)
	}
	if err != nil {
		return time.Time{}
	}
	return t
}

func splitRule(rule string) (string, string) {
	idx := strings.Index(rule, ":")
	if idx < 0 {
		return rule, ""
	}
	return rule[:idx], rule[idx+1:]
}

func extractInt32(data json.RawMessage, objectKey, fieldKey string) int32 {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return 0
	}
	nested, ok := obj[objectKey]
	if !ok {
		return 0
	}
	return extractInt32Field(nested, fieldKey)
}

func extractInt32Field(data json.RawMessage, key string) int32 {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return 0
	}
	raw, ok := obj[key]
	if !ok {
		return 0
	}
	var v int32
	json.Unmarshal(raw, &v)
	return v
}

// extractMeasureInt32 reads a numeric value from the "measures" array by metric key.
// The measures array format is: [{"metric":"ncloc","value":"50"}, ...]
func extractMeasureInt32(data json.RawMessage, metricKey string) int32 {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return 0
	}
	raw, ok := obj["measures"]
	if !ok {
		return 0
	}
	var measures []struct {
		Metric string `json:"metric"`
		Value  string `json:"value"`
	}
	if err := json.Unmarshal(raw, &measures); err != nil {
		return 0
	}
	for _, m := range measures {
		if m.Metric == metricKey {
			v, _ := strconv.ParseInt(m.Value, 10, 32)
			return int32(v)
		}
	}
	return 0
}

// collectProjectLanguages returns the set of languages present in the project's components.
func collectProjectLanguages(components []scanreport.ComponentInput) map[string]bool {
	langs := make(map[string]bool)
	for _, c := range components {
		if c.Language != "" {
			langs[strings.ToLower(c.Language)] = true
		}
	}
	return langs
}

// filterProfilesByLanguage keeps only profiles whose language is in the project.
func filterProfilesByLanguage(profiles []scanreport.QProfileInfo, langs map[string]bool) []scanreport.QProfileInfo {
	var filtered []scanreport.QProfileInfo
	for _, p := range profiles {
		if langs[strings.ToLower(p.Language)] {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

// filterRulesByLanguage keeps only active rules whose language is in the project.
func filterRulesByLanguage(rules []scanreport.ActiveRuleInput, langs map[string]bool) []scanreport.ActiveRuleInput {
	var filtered []scanreport.ActiveRuleInput
	for _, r := range rules {
		if langs[strings.ToLower(r.Language)] {
			filtered = append(filtered, r)
		}
	}
	return filtered
}
