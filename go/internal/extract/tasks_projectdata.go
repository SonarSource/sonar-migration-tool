// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// issueStatusesRename is the SonarQube Server release where the issue
// search swapped its statuses parameter to issueStatuses + the new
// ACCEPTED value.
var issueStatusesRename = common.MustParseVersion("10.4")

// projectDataTasks returns extract tasks needed for project data migration.
// These tasks extract full issue data, component trees, source code, and SCM
// blame data — all per-project, per-branch.
func projectDataTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "getProjectIssuesFull",
			Editions:     AllEditions,
			Dependencies: []string{"getProjects", "getBranches"},
			Run:          projectIssuesFullTask(),
		},
		{
			Name:         "getProjectHotspotsFull",
			Editions:     AllEditions,
			Dependencies: []string{"getProjects", "getBranches"},
			Run:          projectHotspotsFullTask(),
		},
		{
			Name:         "getProjectComponentTree",
			Editions:     AllEditions,
			Dependencies: []string{"getProjects", "getBranches"},
			Run:          projectComponentTreeTask(),
		},
		{
			Name:         "getProjectSourceCode",
			Editions:     AllEditions,
			Dependencies: []string{"getProjectComponentTree"},
			Run:          projectSourceCodeTask(),
		},
		{
			Name:         "getProjectSCMData",
			Editions:     AllEditions,
			Dependencies: []string{"getProjectComponentTree"},
			Run:          projectSCMDataTask(),
		},
		{
			Name:         "getProjectVersions",
			Editions:     AllEditions,
			Dependencies: []string{"getProjects", "getBranches"},
			Run:          projectVersionsTask(),
		},
	}
}

// projectIssuesFullTask extracts all open issues per project per branch with
// full metadata (creation date, text range, flows, etc.).
func projectIssuesFullTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachProjectBranch(ctx, e, "getProjectIssuesFull",
			func(ctx context.Context, projectKey, branch string, w *ChunkWriter) error {
				params := url.Values{
					// componentKeys (not components) is the canonical project
					// filter on /api/issues/search across every SQ version we
					// support — 9.9, 10.x, current, and SQC. SQ 9.9 silently
					// ignores `components`, returning the global issue set
					// instead, which then gets enriched per-project and
					// pollutes every project's import with the first ~10k
					// issues server-wide. #400.
					"componentKeys": {projectKey},
					"branch":        {branch},
					"ps":            {"500"},
				}
				// additionalFields=_all is what brings comments and
				// changelog into the response — the data only the
				// migrate-side per-issue sync would consume. Skip it
				// when the operator opted out of that sync. #398.
				if !e.SkipIssueSync {
					params.Set("additionalFields", "_all")
				}
				if e.Version.Less(issueStatusesRename) {
					params.Set("statuses", "OPEN,CONFIRMED,REOPENED,RESOLVED")
				} else {
					params.Set("issueStatuses", "OPEN,CONFIRMED,FALSE_POSITIVE,ACCEPTED")
				}
				items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
					Path:      issuesSearchAPI,
					Params:    params,
					ResultKey: "issues",
					PageLimit: 20, // SonarQube caps at 10,000 results
				})
				if err != nil {
					if isNonFatalHTTPErr(err) {
						e.Logger.Warn("getProjectIssuesFull skipped", "project", projectKey, "branch", branch, "err", err)
						return nil
					}
					return err
				}
				meta := map[string]any{
					"projectKey": projectKey,
					"branch":     branch,
					"serverUrl":  e.ServerURL,
				}
				return w.WriteChunk(enrichAll(items, meta))
			})
	}
}

// projectHotspotsFullTask extracts all hotspots per project per branch.
// For REVIEWED hotspots, it fetches detail via /api/hotspots/show to get
// comments and ruleKey.
//
// SonarQube Server's /api/hotspots/search applies a TO_REVIEW default
// on several versions when the `status` parameter is omitted, which
// silently drops every REVIEWED hotspot (including ACKNOWLEDGED) from
// the extract — and therefore from migration's source set (#323). To
// be version-independent, the extract issues one paginated request
// per status (TO_REVIEW + REVIEWED) and concatenates the results.
func projectHotspotsFullTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachProjectBranch(ctx, e, "getProjectHotspotsFull",
			func(ctx context.Context, projectKey, branch string, w *ChunkWriter) error {
				baseParams := func() url.Values {
					p := url.Values{
						hotspotsProjectParam(e.Version): {projectKey},
						"ps":                            {"500"},
					}
					if branch != "" {
						p.Set("branch", branch)
					}
					return p
				}

				var all []json.RawMessage
				seen := make(map[string]bool)
				for _, status := range []string{"TO_REVIEW", "REVIEWED"} {
					params := baseParams()
					params.Set("status", status)
					items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
						Path:      "api/hotspots/search",
						Params:    params,
						ResultKey: "hotspots",
						PageLimit: 20,
					})
					if err != nil {
						if isNonFatalHTTPErr(err) {
							e.Logger.Warn("getProjectHotspotsFull skipped", "project", projectKey, "branch", branch, "status", status, "err", err)
							return nil
						}
						return err
					}
					for _, raw := range items {
						key := extractField(raw, "key")
						if key == "" || seen[key] {
							continue
						}
						seen[key] = true
						all = append(all, raw)
					}
				}

				meta := map[string]any{
					"projectKey": projectKey,
					"branch":     branch,
					"serverUrl":  e.ServerURL,
				}

				enriched := enrichAll(all, meta)
				// enrichHotspotDetails issues one /api/hotspots/show
				// call per REVIEWED hotspot purely to pick up comments
				// + rule — data only the migrate-side hotspot sync
				// would consume. Skip when opted out. #398.
				if !e.SkipIssueSync {
					enrichHotspotDetails(ctx, e, enriched)
				}
				return w.WriteChunk(enriched)
			})
	}
}

// enrichHotspotDetails fetches /api/hotspots/show for each REVIEWED hotspot
// and merges the detail (comments, rule) into the enriched items in-place.
func enrichHotspotDetails(ctx context.Context, e *Executor, enriched []json.RawMessage) {
	for i, item := range enriched {
		status := extractField(item, "status")
		if status != "REVIEWED" {
			continue
		}
		hotspotKey := extractField(item, "key")
		if hotspotKey == "" {
			continue
		}
		detail, err := e.Raw.Get(ctx, "api/hotspots/show", url.Values{"hotspot": {hotspotKey}})
		if err != nil {
			e.Logger.Debug("hotspot detail fetch failed", "hotspot", hotspotKey, "err", err)
			continue
		}
		enriched[i] = mergeHotspotDetail(item, detail)
	}
}

func mergeHotspotDetail(base, detail json.RawMessage) json.RawMessage {
	var baseMap map[string]json.RawMessage
	if err := json.Unmarshal(base, &baseMap); err != nil {
		return base
	}
	var detailMap map[string]json.RawMessage
	if err := json.Unmarshal(detail, &detailMap); err != nil {
		return base
	}
	if comments, ok := detailMap["comment"]; ok {
		baseMap["comment"] = comments
	}
	if rule, ok := detailMap["rule"]; ok {
		baseMap["rule"] = rule
		var ruleObj map[string]json.RawMessage
		if err := json.Unmarshal(rule, &ruleObj); err == nil {
			if ruleKey, ok := ruleObj["key"]; ok {
				baseMap["ruleKey"] = ruleKey
			}
		}
	}
	merged, err := json.Marshal(baseMap)
	if err != nil {
		return base
	}
	return merged
}

// projectComponentTreeTask extracts the file component tree per project per branch.
func projectComponentTreeTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachProjectBranch(ctx, e, "getProjectComponentTree",
			func(ctx context.Context, projectKey, branch string, w *ChunkWriter) error {
				params := url.Values{
					"component":  {projectKey},
					"branch":     {branch},
					"qualifiers": {"FIL,UTS"},
					// Per-file size/complexity measures. Without measures the
					// packaged report carries no measures-*.pb, SonarCloud's CE
					// computes a null project ncloc, and the branch renders as
					// "main branch is empty". ncloc alone clears the overlay
					// (CloudVoyager precedent); the rest populate the dashboard.
					// Data metrics (ncloc_data, executable_lines_data) are NOT
					// requestable via component_tree — the API requires
					// api/measures/component (one call per file) — so they are
					// intentionally omitted here.
					"metricKeys": {"ncloc,comment_lines,complexity,cognitive_complexity,functions,statements,classes"},
					"ps":         {"500"},
				}
				items, err := e.Raw.GetPaginated(ctx, PaginatedOpts{
					Path:      "api/measures/component_tree",
					Params:    params,
					ResultKey: "components",
					PageLimit: 20, // SonarQube caps at 10,000 results
				})
				if err != nil {
					if isNonFatalHTTPErr(err) {
						e.Logger.Warn("getProjectComponentTree skipped", "project", projectKey, "branch", branch, "err", err)
						return nil
					}
					return err
				}
				meta := map[string]any{
					"projectKey": projectKey,
					"branch":     branch,
					"serverUrl":  e.ServerURL,
				}
				return w.WriteChunk(enrichAll(items, meta))
			})
	}
}

// projectSourceCodeTask extracts source code for each file component.
func projectSourceCodeTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getProjectSourceCode", "getProjectComponentTree",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				return fetchSourceCode(ctx, e, item, w)
			})
	}
}

func fetchSourceCode(ctx context.Context, e *Executor, item json.RawMessage, w *ChunkWriter) error {
	fileKey := extractField(item, "key")
	branch := extractField(item, "branch")
	if fileKey == "" {
		return nil
	}
	if branch == "" {
		e.Logger.Warn("getProjectSourceCode skipped: component has no branch field", "file", fileKey)
		return nil
	}
	params := fileParams(fileKey, branch)
	raw, err := e.Raw.GetRaw(ctx, "api/sources/raw", params)
	if err != nil {
		if isNonFatalHTTPErr(err) {
			e.Logger.Warn("getProjectSourceCode skipped", "file", fileKey, "branch", branch, "err", err)
			// Write an empty source record so the branch×file is tracked even when source
			// is absent (purged or restricted). Without this, zero records in a branch make
			// totalSourceLen indistinguishable from "source never attempted".
			raw = []byte{}
		} else {
			return err
		}
	}
	// Always fetch the per-line highlighted HTML from api/sources/lines. Its
	// "code" field carries the syntax highlighting that api/sources/raw lacks
	// (issue #420), and it doubles as a source-text fallback when raw has been
	// purged. Highlighting failures are non-fatal — source still migrates.
	highlightedLines, hErr := fetchHighlightedLines(ctx, e, fileKey, branch)
	if hErr != nil {
		e.Logger.Warn("getProjectSourceCode: syntax highlighting unavailable",
			"file", fileKey, "branch", branch, "err", hErr)
	}

	if len(raw) == 0 && len(highlightedLines) > 0 {
		// api/sources/raw returned empty — SonarQube housekeeping may have purged the
		// raw_source_data column while the data column (used by the Code view UI) remains.
		// Reconstruct plain source text from the highlighted lines we just fetched.
		if fallback := plainTextFromHighlighted(highlightedLines); fallback != "" {
			e.Logger.Info("getProjectSourceCode: recovered source via sources/lines fallback",
				"file", fileKey, "branch", branch, "bytes", len(fallback))
			raw = []byte(fallback)
		}
	}
	record := map[string]any{
		"key":              fileKey,
		"branch":           branch,
		"projectKey":       extractField(item, "projectKey"),
		"source":           string(raw),
		"highlightedLines": highlightedLines,
		"serverUrl":        e.ServerURL,
	}
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return w.WriteOne(b)
}

// fetchHighlightedLines retrieves the per-line highlighted HTML ("code" field)
// from api/sources/lines. The returned slice is indexed by line-1; any line the
// API omits is left as an empty string so indices keep matching line numbers.
// The lines endpoint reads the 'data' column (behind the SonarQube Code view),
// which housekeeping leaves intact even after purging raw_source_data. This HTML
// is the only place the source server still exposes syntax highlighting.
func fetchHighlightedLines(ctx context.Context, e *Executor, fileKey, branch string) ([]string, error) {
	resp, err := e.Raw.Get(ctx, "api/sources/lines", fileParams(fileKey, branch))
	if err != nil {
		return nil, err
	}
	var result struct {
		Sources []struct {
			Line int    `json:"line"`
			Code string `json:"code"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("parsing sources/lines response: %w", err)
	}
	maxLine := 0
	for _, s := range result.Sources {
		if s.Line > maxLine {
			maxLine = s.Line
		}
	}
	if maxLine == 0 {
		return nil, nil
	}
	lines := make([]string, maxLine)
	for _, s := range result.Sources {
		if s.Line >= 1 && s.Line <= maxLine {
			lines[s.Line-1] = s.Code
		}
	}
	return lines, nil
}

// plainTextFromHighlighted reconstructs plain source text from highlighted HTML
// lines by stripping tags and unescaping entities. Used as a fallback when
// api/sources/raw returns empty.
func plainTextFromHighlighted(lines []string) string {
	plain := make([]string, len(lines))
	for i, code := range lines {
		plain[i] = html.UnescapeString(htmlTagRe.ReplaceAllString(code, ""))
	}
	return strings.Join(plain, "\n")
}

// projectSCMDataTask extracts SCM blame data for each file component.
func projectSCMDataTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachDep(ctx, e, "getProjectSCMData", "getProjectComponentTree",
			func(ctx context.Context, item json.RawMessage, w *ChunkWriter) error {
				return fetchSCMData(ctx, e, item, w)
			})
	}
}

func fetchSCMData(ctx context.Context, e *Executor, item json.RawMessage, w *ChunkWriter) error {
	fileKey := extractField(item, "key")
	branch := extractField(item, "branch")
	if fileKey == "" {
		return nil
	}
	if branch == "" {
		e.Logger.Warn("getProjectSCMData skipped: component has no branch field", "file", fileKey)
		return nil
	}
	raw, err := e.Raw.Get(ctx, "api/sources/scm", fileParams(fileKey, branch))
	if err != nil {
		return handleNonFatal(e, "getProjectSCMData", fileKey, err)
	}
	return w.WriteOne(EnrichRaw(raw, map[string]any{
		"key":        fileKey,
		"branch":     branch,
		"projectKey": extractField(item, "projectKey"),
		"serverUrl":  e.ServerURL,
	}))
}

// projectVersionsTask extracts the current project version per branch via
// /api/navigation/component. This matches how CloudVoyager resolves the
// source project version for each branch during transfer.
func projectVersionsTask() func(ctx context.Context, e *Executor) error {
	return func(ctx context.Context, e *Executor) error {
		return forEachProjectBranch(ctx, e, "getProjectVersions",
			func(ctx context.Context, projectKey, branch string, w *ChunkWriter) error {
				params := url.Values{
					"component": {projectKey},
				}
				if branch != "" {
					params.Set("branch", branch)
				}
				raw, err := e.Raw.Get(ctx, "api/navigation/component", params)
				if err != nil {
					if isNonFatalHTTPErr(err) {
						e.Logger.Debug("getProjectVersions skipped", "project", projectKey, "branch", branch, "err", err)
						return nil
					}
					return err
				}
				version := extractField(raw, "version")
				record := map[string]any{
					"projectKey": projectKey,
					"branch":     branch,
					"version":    version,
					"serverUrl":  e.ServerURL,
				}
				b, err := json.Marshal(record)
				if err != nil {
					return err
				}
				return w.WriteOne(b)
			})
	}
}

// forEachProjectBranch iterates over all projects and their branches,
// calling fn for each project+branch combination.
func forEachProjectBranch(ctx context.Context, e *Executor, taskName string,
	fn func(ctx context.Context, projectKey, branch string, w *ChunkWriter) error) error {

	projects, err := e.Store.ReadAll("getProjects")
	if err != nil {
		return fmt.Errorf("%s: reading projects: %w", taskName, err)
	}
	branches, err := e.Store.ReadAll("getBranches")
	if err != nil {
		return fmt.Errorf("%s: reading branches: %w", taskName, err)
	}

	branchMap := buildBranchMap(branches)

	w, err := e.Store.Writer(taskName)
	if err != nil {
		return err
	}

	// Filter once to know how many projects we'll actually process —
	// projects with no key or marked skipped (e.g. permissions denied
	// earlier in the extract) don't count toward the progress total.
	var keys []string
	for _, proj := range projects {
		projectKey := extractField(proj, "key")
		if projectKey == "" || e.IsSkipped(projectKey) {
			continue
		}
		keys = append(keys, projectKey)
	}

	// Progress is counted in projects, not project×branch pairs —
	// branches per project are sequential (see iterateBranches) and
	// typically singular, so projects is the right operator-visible
	// denominator (#340).
	e.Logger.Info("starting task", "task", taskName, "items", len(keys))
	prog := common.NewProgressLogger(e.Logger, taskName, len(keys))

	for _, projectKey := range keys {
		if err := iterateBranches(ctx, e, w, taskName, projectKey, branchMap[projectKey], fn); err != nil {
			return err
		}
		prog.Increment()
	}
	return nil
}

func iterateBranches(ctx context.Context, e *Executor, w *ChunkWriter,
	taskName, projectKey string, branches []string,
	fn func(ctx context.Context, projectKey, branch string, w *ChunkWriter) error) error {

	if len(branches) == 0 {
		branches = []string{"main"}
	}
	for _, branch := range branches {
		if err := acquireSem(ctx, e.Sem); err != nil {
			return err
		}
		err := fn(ctx, projectKey, branch, w)
		<-e.Sem
		if err != nil {
			return fmt.Errorf("%s [%s/%s]: %w", taskName, projectKey, branch, err)
		}
	}
	return nil
}

// buildBranchMap builds a map of projectKey -> []branchName from extracted branch data.
func buildBranchMap(branches []json.RawMessage) map[string][]string {
	result := make(map[string][]string)
	for _, item := range branches {
		projectKey := extractField(item, "projectKey")
		name := extractField(item, "name")
		if projectKey == "" || name == "" {
			continue
		}
		// Only include long-lived branches (not short-lived/PR branches).
		branchType := strings.ToUpper(extractField(item, "type"))
		if branchType == "SHORT" {
			continue
		}
		result[projectKey] = append(result[projectKey], name)
	}
	return result
}

// fileParams builds url.Values for a file key and its branch.
// Both fetchSourceCode and fetchSCMData guard against empty branch before
// calling this, so branch is always non-empty here.
func fileParams(fileKey, branch string) url.Values {
	params := url.Values{"key": {fileKey}}
	if branch != "" {
		params.Set("branch", branch)
	}
	return params
}

// handleNonFatal returns nil for 403/404 errors (logging them), or the original error.
func handleNonFatal(e *Executor, task, key string, err error) error {
	if isNonFatalHTTPErr(err) {
		e.Logger.Debug(task+" skipped", "file", key, "err", err)
		return nil
	}
	return err
}
