// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
	"golang.org/x/sync/errgroup"
)

func projectDataTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "importProjectData",
			Editions:     common.AllEditions,
			Dependencies: []string{"createProjects", "setProjectProfiles"},
			Run:          runImportProjectData,
		},
	}
}

func runImportProjectData(ctx context.Context, e *Executor) error {
	projects, err := e.Store.ReadAll("createProjects")
	if err != nil {
		return fmt.Errorf("importProjectData: reading createProjects: %w", err)
	}

	// Process projects org-by-org, alphabetical within each org (#326),
	// so the per-project log stream reflects predictable progress.
	sortMigrateItems("importProjectData", projects)

	w, err := e.Store.Writer("importProjectData")
	if err != nil {
		return err
	}

	completed := loadCompletedBranches(e.Store)

	e.Logger.Info("starting task", "task", "importProjectData", "items", len(projects))
	prog := common.NewProgressLogger(e.Logger, "importProjectData", len(projects))

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))

	for _, proj := range projects {
		cloudKey := extractField(proj, "cloud_project_key")
		orgKey := extractField(proj, "sonarcloud_org_key")
		serverURL := extractField(proj, "server_url")
		serverKey := extractField(proj, "key")

		if cloudKey == "" || orgKey == "" {
			continue
		}

		e.Logger.Debug("importing project data", "project", cloudKey)

		g.Go(func() error {
			if gCtx.Err() != nil {
				return gCtx.Err()
			}

			sqBranches := collectBranchInfo(e, serverURL, serverKey)
			if len(sqBranches) == 0 {
				sqBranches = []branchInfo{{Name: "main", IsMain: true}}
			}
			sortBranchesMainFirst(sqBranches)
			sqBranches = filterBranches(sqBranches, e.ExcludeBranches)

			scMainBranch := fetchSCMainBranch(gCtx, e, cloudKey)

			if err := importProjectBranches(gCtx, e, proj, sqBranches, scMainBranch, completed, w); err != nil {
				e.Logger.Warn("project project data failed", "project", cloudKey, "err", err)
			}
			prog.Increment()
			return nil
		})
	}
	return g.Wait()
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

// importProjectBranches imports project data for every branch of one project.
// Main branch is imported first; if it fails, remaining branches are skipped.
func importProjectBranches(ctx context.Context, e *Executor, proj json.RawMessage,
	sqBranches []branchInfo, scMainBranch string, completed map[string]bool, w *common.ChunkWriter) error {

	cloudKey := extractField(proj, "cloud_project_key")
	orgKey := extractField(proj, "sonarcloud_org_key")
	serverURL := extractField(proj, "server_url")
	serverKey := extractField(proj, "key")

	bctx := branchImportContext{
		CloudKey:     cloudKey,
		OrgKey:       orgKey,
		ServerURL:    serverURL,
		ServerKey:    serverKey,
		SCMainBranch: scMainBranch,
		Completed:    completed,
		Writer:       w,
	}

	var mainBranch *branchInfo
	var nonMainBranches []branchInfo
	for i := range sqBranches {
		if sqBranches[i].IsMain {
			mainBranch = &sqBranches[i]
		} else {
			nonMainBranches = append(nonMainBranches, sqBranches[i])
		}
	}

	// Non-main (long-lived) branches must point their reference/merge branch
	// (scanner-report metadata field 11) at the project's MAIN branch — not at
	// themselves. On a branch's first analysis the SonarCloud CE copies issues
	// from the reference branch; a self-reference (the previous default) makes
	// that issue-sync step abort with the opaque "issue whilst processing the
	// report" error. This mirrors the real scanner, which sets
	// merge_branch_name = the reference branch. The main branch is unaffected:
	// it sends no branch characteristic, so the CE ignores its reference field.
	bctx.MainTargetName = resolveMainTargetName(scMainBranch, mainBranch)

	// Phase 1: import main branch (blocking gate).
	if mainBranch != nil {
		if err := importAndRecordBranch(ctx, e, bctx, *mainBranch); err != nil {
			e.Logger.Warn("main branch failed, skipping remaining branches",
				"project", cloudKey, "err", err)
			for _, nb := range nonMainBranches {
				recordBranchResult(w, cloudKey, nb.Name, &importResult{
					Status: "skipped", Error: "skipped: main branch CE failed",
				})
			}
			return fmt.Errorf("main branch CE failed for %s: %w", cloudKey, err)
		}
	}

	// Phase 2: import non-main branches sequentially.
	for _, branch := range nonMainBranches {
		_ = importAndRecordBranch(ctx, e, bctx, branch)
	}
	return nil
}

// resolveMainTargetName returns the project's main branch name on the target,
// used as the reference/merge branch for non-main branch imports. It prefers
// the SonarCloud main branch name (which may have been renamed during project
// creation) and falls back to the source main branch name.
func resolveMainTargetName(scMainBranch string, mainBranch *branchInfo) string {
	if scMainBranch != "" {
		return scMainBranch
	}
	if mainBranch != nil {
		return mainBranch.Name
	}
	return ""
}

type branchImportContext struct {
	CloudKey     string
	OrgKey       string
	ServerURL    string
	ServerKey    string
	SCMainBranch string
	// MainTargetName is the project's main branch name on the SonarCloud target
	// (the SC main branch if known, else the SQ main branch name). Non-main
	// branches use it as their reference/merge branch on submit.
	MainTargetName string
	Completed      map[string]bool
	Writer         *common.ChunkWriter
}

func importAndRecordBranch(ctx context.Context, e *Executor, bctx branchImportContext, branch branchInfo) error {
	if shouldSkipBranch(bctx.Completed, bctx.CloudKey, branch.Name) {
		e.Logger.Debug("skipping already-completed branch", "project", bctx.CloudKey, "branch", branch.Name)
		return nil
	}

	targetBranch := branch.Name
	if branch.IsMain && bctx.SCMainBranch != "" {
		targetBranch = bctx.SCMainBranch
	}
	// Non-main branches reference the main branch; the main branch references
	// nothing (BuildMetadata falls back to its own name, preserving the working
	// main-branch behavior).
	referenceBranch := ""
	if !branch.IsMain {
		referenceBranch = bctx.MainTargetName
	}
	result, err := importBranch(ctx, e, importBranchInput{
		CloudKey:        bctx.CloudKey,
		OrgKey:          bctx.OrgKey,
		ServerURL:       bctx.ServerURL,
		ServerKey:       bctx.ServerKey,
		Branch:          branch.Name,
		TargetBranch:    targetBranch,
		ReferenceBranch: referenceBranch,
		IsMain:          branch.IsMain,
	})
	if err != nil {
		logAPIWarn(e.Logger, "project data import failed", err, "project", bctx.CloudKey, "branch", branch.Name)
		result = &importResult{Status: "failed", Error: err.Error()}
	}
	recordBranchResult(bctx.Writer, bctx.CloudKey, branch.Name, result)
	return err
}

func recordBranchResult(w *common.ChunkWriter, cloudKey, branchName string, result *importResult) {
	record, _ := json.Marshal(map[string]any{
		"cloud_project_key": cloudKey,
		"branch":            branchName,
		"status":            result.Status,
		"task_id":           result.TaskID,
		"error":             result.Error,
	})
	w.WriteOne(record) //nolint:errcheck
}

type importBranchInput struct {
	CloudKey        string
	OrgKey          string
	ServerURL       string
	ServerKey       string
	Branch          string // SQ branch name — used to filter extracted data
	TargetBranch    string // SC branch name — used in protobuf metadata and CE submit
	ReferenceBranch string // reference/merge branch (metadata field 11); empty for main
	IsMain          bool   // main/default branch — suppresses branch characteristics on submit
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

	report, skip, err := buildBranchReport(ctx, e, input, targetBranch)
	if err != nil {
		return nil, err
	}
	if skip != nil {
		return skip, nil
	}

	cfg := scanreport.SubmitConfig{
		CloudURL:       e.CloudURL,
		ProjectKey:     input.CloudKey,
		OrgKey:         input.OrgKey,
		BranchName:     targetBranch,
		ProjectVersion: report.ProjectVersion,
		IsMain:         input.IsMain,
	}

	result, err := scanreport.SubmitReport(ctx, e.Raw.HTTPClient(), cfg, report.ZIP)
	if err != nil {
		return nil, fmt.Errorf("submitting report: %w", err)
	}

	e.Logger.Info("CE task submitted", "project", input.CloudKey, "targetBranch", targetBranch, "taskId", result.TaskID)

	if err := scanreport.PollCETask(ctx, e.Raw.HTTPClient(), e.CloudURL, result.TaskID, e.Logger); err != nil {
		return nil, fmt.Errorf("CE task failed: %w", err)
	}

	return &importResult{Status: "success", TaskID: result.TaskID}, nil
}

// branchReport is a packaged scanner report for one branch, ready to submit.
type branchReport struct {
	ZIP            []byte
	ProjectVersion string
}

// buildBranchReport loads the extracted data for one branch, applies the
// CE-compatibility fixes, and returns the packaged report. A non-nil skip
// result means the branch must not be submitted (no components, or no source
// to anchor its issues).
func buildBranchReport(ctx context.Context, e *Executor, input importBranchInput, targetBranch string) (*branchReport, *importResult, error) {
	issues := loadExtractedIssues(e, input.ServerURL, input.ServerKey, input.Branch)
	hotspotIssues := loadExtractedHotspots(e, input.ServerURL, input.ServerKey, input.Branch)
	extIssues, adHocRules := loadExtractedExternalIssues(e, input.ServerURL, input.ServerKey, input.Branch)
	// Include ALL FIL components, not just those with source code; external
	// issues can reference files without source. CloudVoyager does the same.
	components := loadExtractedComponents(e, input.ServerURL, input.ServerKey, input.Branch)
	sources := loadExtractedSources(e, input.ServerURL, input.ServerKey, input.Branch)
	activeRules := loadExtractedActiveRules(e, input.ServerURL, input.ServerKey)
	// Per-file measures (ncloc et al.) — without these the branch renders as
	// "main branch is empty". Real per-line SCM blame — without it the Code
	// view shows no author/date per line.
	componentMeasures := loadComponentMeasures(e, input.ServerURL, input.ServerKey, input.Branch)
	scmByComponent := loadExtractedSCM(e, input.ServerURL, input.ServerKey, input.Branch)
	// Per-line syntax highlighting (colors in the Code view); without it the
	// migrated source renders as raw, uncolored text (issue #420).
	syntaxHighlighting := loadExtractedSyntaxHighlighting(e, input.ServerURL, input.ServerKey, input.Branch)

	if len(components) == 0 {
		return nil, &importResult{Status: "skipped"}, nil
	}

	// The source server returns no source TEXT for this branch even though line
	// measures may still exist (SonarQube housekeeping purges source/SCM data
	// for old or inactive branches while keeping aggregate measures and issues;
	// /api/sources/{raw,lines} then return empty). Without source, issues cannot
	// be anchored to real lines: the report would declare N-line files with
	// empty source, and the SonarCloud CE rejects that inconsistency. Skip
	// rather than submit a doomed report. (The main branch is actively analyzed
	// and always carries source, so it is unaffected.)
	if (len(issues)+len(hotspotIssues)+len(extIssues)) > 0 && totalSourceLen(sources) == 0 {
		e.Logger.Warn("skipping branch: source code not retrievable (line measures may remain, but source text is gone — likely purged by housekeeping for an inactive branch; re-analyze the branch to restore it)",
			"project", input.CloudKey, "branch", input.Branch,
			"findings", len(issues)+len(hotspotIssues)+len(extIssues))
		return nil, &importResult{Status: "skipped", Error: "source code not retrievable for this branch (line measures may remain, but source text is gone — likely purged by SonarQube housekeeping); re-analyze the branch on the source server to migrate it"}, nil
	}

	// Fix component line counts (see fixComponentLineCounts).
	fixComponentLineCounts(components, buildSourceLineCountMap(sources),
		maxIssueEndLineByComponent(issues, hotspotIssues, extIssues))

	// Fetch SC quality profiles (CloudVoyager uses SC profile keys, not SQ keys).
	// The CE validates that qprofile keys in the metadata exist in the SC instance.
	scProfileByLang := buildSCProfileMap(ctx, e, input.OrgKey)

	// Filter profiles and rules to languages present in the project (matches cloudvoyager).
	projectLangs := collectProjectLanguages(components)
	activeRules = filterRulesByLanguage(activeRules, projectLangs)

	qprofiles := buildProjectQProfiles(projectLangs, scProfileByLang)
	remapActiveRuleProfiles(activeRules, scProfileByLang)
	// Remapping collapses every source profile for a language onto a single
	// SonarCloud profile key, so a rule activated in more than one source
	// profile (e.g. "Sonar way" + "Olivier Way" for py) becomes a duplicate
	// (repo, ruleKey, qProfileKey). SonarCloud's CE rejects a report whose
	// activerules.pb activates the same rule twice in a profile, so dedup
	// here — exactly once per rule, mirroring CloudVoyager's output.
	activeRules = dedupActiveRules(activeRules)

	// Drop native issues whose rule is not among the active rules. The CE
	// requires every native issue's rule to be activated in the analysis; an
	// orphan rule (e.g. a "secrets" finding when secrets rules were never
	// extracted as active rules) aborts the entire report. Such issues cannot
	// be recreated on the target regardless. Hotspots are appended afterward —
	// they are validated against hotspot rules, not the active-rule set.
	issues, droppedOrphanIssues := dropIssuesWithInactiveRules(issues, activeRules)
	if droppedOrphanIssues > 0 {
		e.Logger.Warn("dropped native issues referencing inactive rules",
			"project", input.CloudKey, "branch", input.Branch, "dropped", droppedOrphanIssues)
	}
	issues = append(issues, hotspotIssues...)

	now := time.Now()

	root, fileComps, cr := scanreport.BuildComponents(input.CloudKey, components)
	pbSources := make(map[int32]string)
	for _, s := range sources {
		if ref, ok := cr.Refs()[s.Component]; ok && s.Source != "" {
			pbSources[ref] = s.Source
		}
	}

	changesets := buildChangesetMap(cr, components, pbSources, scmByComponent, now)

	// Backdate changesets so each issue gets its original SonarQube creation date.
	// Build a component-key-keyed alias map (same pointers) for BackdateChangesets.
	changesetsByKey := make(map[string]*pb.Changesets, len(changesets))
	for compKey, ref := range cr.Refs() {
		if cs, ok := changesets[ref]; ok {
			changesetsByKey[compKey] = cs
		}
	}
	extracted := toExtractedIssues(issues)
	extracted = append(extracted, extIssuesToExtracted(extIssues)...)
	scanreport.BackdateChangesets(extracted, changesetsByKey, now)

	projectVersion := resolveProjectVersion(e, input.ServerURL, input.ServerKey, input.Branch)

	// For non-main branches, perform the SonarCloud "Create analysis" handshake.
	// It anchors the branch row server-side and returns an analysis id that we
	// stamp into the report metadata (analysis_uuid, field 19), so the CE binds
	// this report to the pre-created branch. Without it, the CE accepts the
	// report (task SUCCESS) but never creates the branch. The main branch needs
	// no handshake — its first analysis establishes it.
	var analysisUUID string
	if !input.IsMain {
		res, hErr := scanreport.PreCreateAnalysis(ctx, e.RawAPI.HTTPClient(), scanreport.AnalysisConfig{
			APIURL:         e.APIURL,
			OrgKey:         input.OrgKey,
			ProjectKey:     input.CloudKey,
			ProjectVersion: projectVersion,
			BranchName:     targetBranch,
			TargetBranch:   input.ReferenceBranch,
			// Migrate every non-main branch as a long-lived branch so it keeps
			// its full issue history (matches SonarQube Server, where all
			// branches are long-lived). Without this, branches whose names don't
			// match the target's long-lived-branch regex would be created as
			// short-lived (PR-like, auto-deleted, no overall-code history).
			BranchType: "long",
		})
		if hErr != nil {
			return nil, nil, fmt.Errorf("create-analysis handshake (branch %s): %w", input.Branch, hErr)
		}
		analysisUUID = res.AnalysisUUID
		e.Logger.Info("analysis pre-created (branch anchored on target)",
			"project", input.CloudKey, "branch", targetBranch,
			"analysisUuid", analysisUUID, "branchType", res.BranchType, "referenceBranch", res.ReferenceBranchName)
	}

	reportData := &scanreport.ReportData{
		Metadata: scanreport.BuildMetadata(scanreport.MetadataInput{
			AnalysisDate:        now,
			OrgKey:              input.OrgKey,
			ProjectKey:          input.CloudKey,
			BranchName:          targetBranch,
			BranchType:          pb.Metadata_BRANCH,
			ReferenceBranchName: input.ReferenceBranch,
			ProjectVersion:      projectVersion,
			QProfiles:           qprofiles,
			FileCountByExt:      countFilesByExt(components),
			AnalysisUUID:        analysisUUID,
		}, root.Ref),
		RootComponent:      root,
		FileComponents:     fileComps,
		Issues:             scanreport.BuildIssues(issues, cr),
		ExternalIssues:     scanreport.BuildExternalIssues(extIssues, cr),
		Measures:           scanreport.BuildMeasures(componentMeasures, cr),
		Changesets:         changesets,
		ActiveRules:        scanreport.BuildActiveRules(activeRules, now.UnixMilli()),
		AdHocRules:         scanreport.BuildAdHocRules(adHocRules),
		Sources:            pbSources,
		SyntaxHighlighting: scanreport.BuildSyntaxHighlighting(syntaxHighlighting, pbSources, cr),
	}

	zipBytes, err := scanreport.PackageReport(reportData)
	if err != nil {
		return nil, nil, fmt.Errorf("packaging report: %w", err)
	}

	if dumpDir := os.Getenv("SMT_DUMP_REPORT_DIR"); dumpDir != "" {
		safe := strings.NewReplacer("/", "_", ":", "_", ",", "_").Replace(input.CloudKey + "-" + input.Branch)
		dumpPath := filepath.Join(dumpDir, safe+".zip")
		if werr := os.WriteFile(dumpPath, zipBytes, 0o644); werr != nil {
			e.Logger.Warn("failed to dump report zip", "path", dumpPath, "err", werr)
		} else {
			e.Logger.Info("dumped report zip", "path", dumpPath)
		}
	}

	e.Logger.Info("report packaged",
		"project", input.CloudKey, "sourceBranch", input.Branch, "targetBranch", targetBranch,
		"projectVersion", projectVersion,
		"zipSizeBytes", len(zipBytes),
		"zipSizeMB", fmt.Sprintf("%.1f", float64(len(zipBytes))/(1024*1024)),
		"components", len(fileComps),
		"issues", len(issues),
		"externalIssues", len(extIssues),
		"sources", len(pbSources),
		"activeRules", len(activeRules),
	)

	return &branchReport{ZIP: zipBytes, ProjectVersion: projectVersion}, nil, nil
}

// fixComponentLineCounts sets each component's line count to the best available
// value: the real source line count when known, otherwise the extracted ncloc —
// but never below the largest line any issue points at. The extract provides
// ncloc (code lines only) while the CE expects total source lines; when source
// is unavailable for a branch the count would fall back to ncloc and the CE
// would reject out-of-range issue lines.
func fixComponentLineCounts(components []scanreport.ComponentInput, sourceLinesByKey map[string]int, maxEndLineByKey map[string]int32) {
	for i := range components {
		lines := components[i].Lines
		if sl, ok := sourceLinesByKey[components[i].Key]; ok && sl > 0 {
			lines = int32(sl)
		}
		if me := maxEndLineByKey[components[i].Key]; me > lines {
			lines = me
		}
		components[i].Lines = lines
	}
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

func sortBranchesMainFirst(branches []branchInfo) {
	slices.SortStableFunc(branches, func(a, b branchInfo) int {
		if a.IsMain && !b.IsMain {
			return -1
		}
		if !a.IsMain && b.IsMain {
			return 1
		}
		return 0
	})
}

func filterBranches(branches []branchInfo, excludePatterns []string) []branchInfo {
	if len(excludePatterns) == 0 {
		return branches
	}
	var filtered []branchInfo
	for _, b := range branches {
		if b.IsMain {
			filtered = append(filtered, b)
			continue
		}
		if matchesAnyGlob(b.Name, excludePatterns) {
			continue
		}
		filtered = append(filtered, b)
	}
	return filtered
}

func matchesAnyGlob(name string, patterns []string) bool {
	for _, p := range patterns {
		if matched, _ := filepath.Match(p, name); matched {
			return true
		}
	}
	return false
}

func loadCompletedBranches(store *common.DataStore) map[string]bool {
	items, err := store.ReadAll("importProjectData")
	if err != nil || len(items) == 0 {
		return nil
	}
	done := make(map[string]bool)
	for _, item := range items {
		if extractField(item, "status") == "success" {
			key := extractField(item, "cloud_project_key") + ":" + extractField(item, "branch")
			done[key] = true
		}
	}
	return done
}

func shouldSkipBranch(completed map[string]bool, cloudKey, branchName string) bool {
	if completed == nil {
		return false
	}
	return completed[cloudKey+":"+branchName]
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

// loadExtractedSyntaxHighlighting reads the per-line highlighted HTML captured
// alongside source code (getProjectSourceCode → "highlightedLines") for the
// given branch, returning one HighlightInput per file. The highlighting is
// parsed into protobuf rules later by scanreport.BuildSyntaxHighlighting so the
// migrated Code view renders with colors (issue #420).
func loadExtractedSyntaxHighlighting(e *Executor, serverURL, serverKey, branch string) []scanreport.HighlightInput {
	items, err := readExtractItems(e, "getProjectSourceCode")
	if err != nil {
		return nil
	}
	var inputs []scanreport.HighlightInput
	for _, item := range items {
		if item.ServerURL != serverURL {
			continue
		}
		var rec struct {
			Key              string   `json:"key"`
			ProjectKey       string   `json:"projectKey"`
			Branch           string   `json:"branch"`
			HighlightedLines []string `json:"highlightedLines"`
		}
		if err := json.Unmarshal(item.Data, &rec); err != nil {
			continue
		}
		if rec.ProjectKey != serverKey || rec.Branch != branch || rec.Key == "" {
			continue
		}
		if len(rec.HighlightedLines) == 0 {
			continue
		}
		inputs = append(inputs, scanreport.HighlightInput{
			Component: rec.Key,
			Lines:     rec.HighlightedLines,
		})
	}
	return inputs
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
	issueType := extractField(data, "type")
	severity := extractField(data, "severity")
	cleanCode := extractField(data, "cleanCodeAttribute")
	effort := extractField(data, "effort")
	if effort == "" {
		effort = extractField(data, "debt")
	}
	impacts := extractImpactInputs(data, "impacts")
	return scanreport.ExternalIssueInput{
			EngineID:           engineID,
			RuleID:             key,
			Message:            extractField(data, "message"),
			Severity:           severity,
			Type:               issueType,
			StartLine:          extractInt32(data, "textRange", "startLine"),
			EndLine:            extractInt32(data, "textRange", "endLine"),
			StartOff:           extractInt32(data, "textRange", "startOffset"),
			EndOff:             extractInt32(data, "textRange", "endOffset"),
			Component:          extractField(data, "component"),
			CreationDate:       parseISODate(extractField(data, "creationDate")),
			Effort:             effort,
			CleanCodeAttribute: cleanCode,
			Impacts:            impacts,
		}, scanreport.AdHocRuleInput{
			EngineID:           engineID,
			RuleID:             key,
			Name:               key,
			Description:        fmt.Sprintf("Rule from %s plugin", engineID),
			Severity:           severity,
			Type:               issueType,
			CleanCodeAttribute: cleanCode,
			Impacts:            impacts,
		}, true
}

// extractImpactInputs parses an MQR "impacts" array (e.g. from
// api/issues/search) into ImpactInput pairs. Returns nil when absent.
func extractImpactInputs(data json.RawMessage, field string) []scanreport.ImpactInput {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return nil
	}
	raw, ok := obj[field]
	if !ok {
		return nil
	}
	var arr []struct {
		SoftwareQuality string `json:"softwareQuality"`
		Severity        string `json:"severity"`
	}
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	out := make([]scanreport.ImpactInput, 0, len(arr))
	for _, im := range arr {
		out = append(out, scanreport.ImpactInput{SoftwareQuality: im.SoftwareQuality, Severity: im.Severity})
	}
	return out
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
		severity := mapVulnProbToSeverity(extractField(item.Data, "vulnerabilityProbability"))
		// Prefer the full textRange (with column offsets) so the report
		// matches CloudVoyager / the real scanner; fall back to the bare
		// line when no textRange is present.
		startLine := extractInt32(item.Data, "textRange", "startLine")
		endLine := extractInt32(item.Data, "textRange", "endLine")
		startOff := extractInt32(item.Data, "textRange", "startOffset")
		endOff := extractInt32(item.Data, "textRange", "endOffset")
		if startLine == 0 {
			line := extractInt32Field(item.Data, "line")
			startLine = line
			endLine = line
		}
		hotspots = append(hotspots, scanreport.IssueInput{
			Key:          extractField(item.Data, "key"),
			CreationDate: parseISODate(extractField(item.Data, "creationDate")),
			RuleRepo:     repo,
			RuleKey:      key,
			Message:      extractField(item.Data, "message"),
			Severity:     severity,
			StartLine:    startLine,
			EndLine:      endLine,
			StartOff:     startOff,
			EndOff:       endOff,
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

// loadComponentMeasures reads the per-file measures extracted from
// /api/measures/component_tree (ncloc, comment_lines, complexity, ...) and
// returns them as MeasureInputs keyed by component. Without these, the
// packaged report carries no measures-*.pb, SonarCloud's CE computes a null
// project ncloc, and the migrated branch renders as "main branch is empty".
func loadComponentMeasures(e *Executor, serverURL, serverKey, branch string) []scanreport.MeasureInput {
	items, err := readExtractItems(e, "getProjectComponentTree")
	if err != nil {
		return nil
	}
	var measures []scanreport.MeasureInput
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
		key := extractField(item.Data, "key")
		if key == "" {
			continue
		}
		for _, m := range extractMeasurePairs(item.Data) {
			measures = append(measures, scanreport.MeasureInput{
				Component: key,
				MetricKey: m.metric,
				Value:     m.value,
			})
		}
	}
	return measures
}

type measurePair struct {
	metric string
	value  string
}

// extractMeasurePairs reads the "measures":[{"metric":..,"value":..}] array
// from a component-tree record. Measures with an empty value are skipped.
func extractMeasurePairs(data json.RawMessage) []measurePair {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	raw, ok := obj["measures"]
	if !ok {
		return nil
	}
	var arr []struct {
		Metric string `json:"metric"`
		Value  string `json:"value"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]measurePair, 0, len(arr))
	for _, m := range arr {
		if m.Metric == "" || m.Value == "" {
			continue
		}
		out = append(out, measurePair{metric: m.Metric, value: m.Value})
	}
	return out
}

// blameLine is one run-length SCM entry: the source line where a commit run
// begins, plus its author/date/revision. /api/sources/scm omits lines whose
// blame matches the previous line, so each entry starts a run that continues
// until the next listed line (handled by scanreport.BuildChangesetsFromBlame).
type blameLine struct {
	Line     int32
	Author   string
	Date     time.Time
	Revision string
}

// loadExtractedSCM reads getProjectSCMData and returns per-component blame runs
// (sorted by line) for the given branch. The migrate side previously ignored
// this data and shipped synthetic changesets; loading it lets the report carry
// real per-line author/date/revision for the SonarCloud Code view.
func loadExtractedSCM(e *Executor, serverURL, serverKey, branch string) map[string][]blameLine {
	items, err := readExtractItems(e, "getProjectSCMData")
	if err != nil {
		return nil
	}
	result := make(map[string][]blameLine)
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
		key := extractField(item.Data, "key")
		if key == "" {
			continue
		}
		if lines := parseBlameLines(item.Data); len(lines) > 0 {
			result[key] = lines
		}
	}
	return result
}

// parseBlameLines parses the "scm":[[line,author,datetime,revision],...] array
// from a getProjectSCMData record into blameLines sorted by line.
func parseBlameLines(data json.RawMessage) []blameLine {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	raw, ok := obj["scm"]
	if !ok {
		return nil
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil
	}
	out := make([]blameLine, 0, len(rows))
	for _, r := range rows {
		if len(r) < 3 {
			continue
		}
		var line int32
		json.Unmarshal(r[0], &line) //nolint:errcheck
		var author, dateStr, rev string
		json.Unmarshal(r[1], &author)  //nolint:errcheck
		json.Unmarshal(r[2], &dateStr) //nolint:errcheck
		if len(r) >= 4 {
			json.Unmarshal(r[3], &rev) //nolint:errcheck
		}
		out = append(out, blameLine{
			Line:     line,
			Author:   author,
			Date:     parseISODate(dateStr),
			Revision: rev,
		})
	}
	slices.SortFunc(out, func(a, b blameLine) int {
		switch {
		case a.Line < b.Line:
			return -1
		case a.Line > b.Line:
			return 1
		default:
			return 0
		}
	})
	return out
}

// blameRunsFor converts extracted blame lines into BlameRuns, but only when the
// blame is meaningful — at least one line must carry a real author or revision.
// Projects analyzed without git history return blame with empty author AND
// revision for every line (only a date); for those we return nil so the caller
// falls back to a synthetic changeset rather than shipping empty blame.
func blameRunsFor(lines []blameLine) []scanreport.BlameRun {
	if len(lines) == 0 {
		return nil
	}
	meaningful := false
	runs := make([]scanreport.BlameRun, 0, len(lines))
	for _, l := range lines {
		if l.Line <= 0 {
			continue
		}
		if l.Author != "" || l.Revision != "" {
			meaningful = true
		}
		runs = append(runs, scanreport.BlameRun{
			StartLine: l.Line,
			Author:    l.Author,
			Date:      l.Date,
			Revision:  l.Revision,
		})
	}
	if !meaningful {
		return nil
	}
	return runs
}

// sonarCloudRuleRepos lists rule repositories known to exist in SonarCloud.
// Rules from external/third-party repos are excluded from the report to
// prevent CE processing errors.
var sonarCloudRuleRepos = map[string]bool{
	"common-java": true, "java": true, "squid": true, "javabugs": true,
	"javasecurity": true, "javaarchitecture": true,
	"common-js": true, "javascript": true, "typescript": true,
	"common-ts": true, "css": true, "web": true, "Web": true,
	"jssecurity": true, "tssecurity": true,
	"jsarchitecture": true, "tsarchitecture": true,
	"common-py": true, "python": true, "pythonbugs": true,
	"pythonenterprise": true, "pythonsecurity": true,
	"common-cs": true, "csharpsquid": true, "roslyn.sonaranalyzer.security.cs": true,
	"common-vbnet": true, "vbnet": true, "vbnetsecurity": true, "vb": true,
	"common-kotlin": true, "kotlin": true, "kotlinsecurity": true,
	"common-ruby": true, "ruby": true, "rubydre": true,
	"common-scala": true, "scala": true,
	"common-go": true, "go": true, "godre": true, "gosecurity": true,
	"common-php": true, "php": true, "phpsecurity": true,
	"common-swift": true, "swift": true,
	"common-c": true, "c": true, "cpp": true, "common-cpp": true,
	"common-objc": true, "objc": true,
	"common-xml": true, "xml": true,
	"common-html": true, "html": true,
	"common-text": true, "text": true, "secrets": true,
	"plsql": true, "tsql": true, "abap": true, "cobol": true, "rpg": true,
	"flex": true, "pli": true, "apex": true, "apexdre": true,
	"cloudformation": true, "terraform": true, "docker": true, "kubernetes": true,
	"azureresourcemanager": true, "ipython": true, "ipythonenterprise": true,
	"shell": true, "shelldre": true,
	"dart": true, "rust": true,
	"ansible": true, "githubactions": true,
	"groovydre": true,
	"json":      true, "yaml": true,
	"jcl": true,
}

// resolveProjectVersion reads the extracted project version for a specific
// project+branch combination. Returns the version string, or empty if not found
// (the caller's BuildMetadata defaults to "1.0.0").
func resolveProjectVersion(e *Executor, serverURL, serverKey, branch string) string {
	items, err := readExtractItems(e, "getProjectVersions")
	if err != nil {
		return ""
	}
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
		version := extractField(item.Data, "version")
		if version != "" && version != "not provided" {
			return version
		}
	}
	return ""
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

// dedupActiveRules removes duplicate active rules keyed by
// (RuleRepo, RuleKey, QProfileKey), keeping the first occurrence. After
// remapActiveRuleProfiles, multiple source profiles for a language share one
// SonarCloud profile key, so the same rule can appear more than once. The CE
// rejects a report that activates the same rule twice in a profile.
func dedupActiveRules(rules []scanreport.ActiveRuleInput) []scanreport.ActiveRuleInput {
	seen := make(map[string]bool, len(rules))
	out := make([]scanreport.ActiveRuleInput, 0, len(rules))
	for _, r := range rules {
		k := r.RuleRepo + "|" + r.RuleKey + "|" + r.QProfileKey
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}

// maxIssueEndLineByComponent returns, per component key, the largest text-range
// end line referenced by any issue across the given groups. A component's
// declared line count must never be smaller than this, or the CE rejects the
// report with an out-of-range line error — which happens when source (and thus
// the real line count) is unavailable for a branch and the count falls back to
// ncloc.
func maxIssueEndLineByComponent(native, hotspots []scanreport.IssueInput, external []scanreport.ExternalIssueInput) map[string]int32 {
	m := make(map[string]int32)
	bump := func(comp string, start, end int32) {
		if start > end {
			end = start
		}
		if comp != "" && end > m[comp] {
			m[comp] = end
		}
	}
	for _, iss := range native {
		bump(iss.Component, iss.StartLine, iss.EndLine)
	}
	for _, iss := range hotspots {
		bump(iss.Component, iss.StartLine, iss.EndLine)
	}
	for _, iss := range external {
		bump(iss.Component, iss.StartLine, iss.EndLine)
	}
	return m
}

// dropIssuesWithInactiveRules removes native issues whose (repo, key) is not in
// the active-rule set, returning the kept issues and the dropped count. An issue
// on a rule the analysis doesn't activate makes the CE abort the whole report,
// and could not be recreated on the target anyway.
func dropIssuesWithInactiveRules(issues []scanreport.IssueInput, activeRules []scanreport.ActiveRuleInput) (kept []scanreport.IssueInput, dropped int) {
	active := make(map[string]struct{}, len(activeRules))
	for _, r := range activeRules {
		active[r.RuleRepo+":"+r.RuleKey] = struct{}{}
	}
	kept = make([]scanreport.IssueInput, 0, len(issues))
	for _, iss := range issues {
		if _, ok := active[iss.RuleRepo+":"+iss.RuleKey]; ok {
			kept = append(kept, iss)
		} else {
			dropped++
		}
	}
	return kept, dropped
}

func buildChangesetMap(cr *scanreport.ComponentRef, components []scanreport.ComponentInput, pbSources map[int32]string, scmByComp map[string][]blameLine, date time.Time) map[int32]*pb.Changesets {
	changesets := make(map[int32]*pb.Changesets)
	for _, comp := range components {
		ref, ok := cr.Refs()[comp.Key]
		if !ok {
			continue
		}
		lineCount := 0
		if src, hasSrc := pbSources[ref]; hasSrc && src != "" {
			lineCount = strings.Count(src, "\n") + 1
		}
		if lineCount == 0 {
			lineCount = int(comp.Lines)
		}
		if lineCount <= 0 {
			continue
		}
		// Prefer real per-line SCM blame so the SonarCloud Code view shows who
		// changed each line and when (matching SonarQube Server). Fall back to
		// a synthetic changeset when the source has no usable blame for the file
		// (e.g. a project analyzed without git history — every line empty).
		if runs := blameRunsFor(scmByComp[comp.Key]); len(runs) > 0 {
			if cs := scanreport.BuildChangesetsFromBlame(ref, runs, lineCount, date); cs != nil {
				changesets[ref] = cs
				continue
			}
		}
		changesets[ref] = scanreport.BuildDefaultChangesets(ref, lineCount, date)
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

func extIssuesToExtracted(extIssues []scanreport.ExternalIssueInput) []scanreport.ExtractedIssue {
	result := make([]scanreport.ExtractedIssue, 0, len(extIssues))
	for _, iss := range extIssues {
		result = append(result, scanreport.ExtractedIssue{
			Component:    iss.Component,
			CreationDate: iss.CreationDate,
			StartLine:    iss.StartLine,
			EndLine:      iss.EndLine,
		})
	}
	return result
}

// totalSourceLen returns the combined byte length of all extracted source. Zero
// means the source server returned no source for any component on this branch.
func totalSourceLen(sources []sourceRecord) int {
	total := 0
	for _, s := range sources {
		total += len(s.Source)
	}
	return total
}

func buildSourceLineCountMap(sources []sourceRecord) map[string]int {
	m := make(map[string]int, len(sources))
	for _, s := range sources {
		if s.Source != "" {
			m[s.Component] = strings.Count(s.Source, "\n") + 1
		}
	}
	return m
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
