// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// Project-level outcome routing (#228): some post-create operations on a
// project (set tags, set settings, grant group permissions, ...) can fail
// without preventing the project itself from being migrated. The report
// surfaces those projects in NearPerfect (yellow) or Partial (orange)
// depending on how the operation impacts the migrated project on the
// SonarQube Cloud side.

// projectOutcomeBucket is the bucket a per-project failure routes to.
type projectOutcomeBucket int

const (
	projectBucketNearPerfect projectOutcomeBucket = iota
	projectBucketPartial
)

// projectFailureMatcher classifies a failed HTTP request against a SQC
// project-scoped endpoint. URLSuffix is matched against the request URL
// path; ProjectParam names the request-body / query field that carries
// the cloud project key.
type projectFailureMatcher struct {
	URLSuffix    string
	Bucket       projectOutcomeBucket
	Operation    string
	ProjectParam string
}

// projectFailureMatchers enumerates the per-project endpoints whose
// failures should affect the project's outcome row in the report.
// Failures matching no entry here fall through to the existing
// generic Failed / Partial paths and don't affect this routing.
var projectFailureMatchers = []projectFailureMatcher{
	{URLSuffix: "/api/project_tags/set", Bucket: projectBucketNearPerfect,
		Operation: "Project tags not migrated", ProjectParam: "project"},
	{URLSuffix: "/api/project_links/create", Bucket: projectBucketNearPerfect,
		Operation: "Project link not migrated", ProjectParam: "projectKey"},
	{URLSuffix: "/api/settings/set", Bucket: projectBucketNearPerfect,
		Operation: "Project setting not migrated", ProjectParam: "component"},
	{URLSuffix: "/api/permissions/add_group", Bucket: projectBucketPartial,
		Operation: "Group permission not migrated", ProjectParam: "projectKey"},
	{URLSuffix: "/api/webhooks/create", Bucket: projectBucketPartial,
		Operation: "Webhook not migrated", ProjectParam: "project"},
}

// projectFailure is one matched failure attached to a project.
type projectFailure struct {
	CloudProjectKey string
	Bucket          projectOutcomeBucket
	Operation       string
	// Detail is the per-failure context line (e.g. the tag value, the
	// setting key, the group + permission). Empty when the matcher
	// could not extract anything meaningful.
	Detail string
	Error  string
}

// collectProjectFailures re-parses requests.log and returns one
// projectFailure per failed call to a project-scoped endpoint listed in
// projectFailureMatchers.
func collectProjectFailures(runDir string) []projectFailure {
	f, err := os.Open(filepath.Join(runDir, "requests.log"))
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []projectFailure
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		entry, ok := parseConfigLogLine(scanner.Text())
		if !ok {
			continue
		}
		pf, ok := classifyProjectFailure(entry)
		if !ok {
			continue
		}
		out = append(out, pf)
	}
	return out
}

func classifyProjectFailure(entry map[string]any) (projectFailure, bool) {
	if asString(entry["process_type"]) != "request_completed" {
		return projectFailure{}, false
	}
	payload, ok := entry["payload"].(map[string]any)
	if !ok {
		return projectFailure{}, false
	}
	if asString(payload["method"]) != "POST" {
		return projectFailure{}, false
	}
	if !isFailure(payload["status"], asString(entry["status"])) {
		return projectFailure{}, false
	}
	url := asString(payload["url"])
	var matcher projectFailureMatcher
	matched := false
	for _, m := range projectFailureMatchers {
		if strings.HasSuffix(url, m.URLSuffix) {
			matcher = m
			matched = true
			break
		}
	}
	if !matched {
		return projectFailure{}, false
	}
	body := configRequestBody(payload)
	projectKey := asString(body[matcher.ProjectParam])
	if projectKey == "" {
		return projectFailure{}, false
	}
	return projectFailure{
		CloudProjectKey: projectKey,
		Bucket:          matcher.Bucket,
		Operation:       matcher.Operation,
		Detail:          projectFailureDetail(matcher, body),
		Error:           extractFailureError(payload),
	}, true
}

// projectFailureDetail extracts the operation-specific subject from the
// request body (the tag list, the setting key, the group + permission,
// etc.) so the report shows operators what actually didn't migrate.
func projectFailureDetail(matcher projectFailureMatcher, body map[string]any) string {
	switch matcher.URLSuffix {
	case "/api/project_tags/set":
		if tags := asString(body["tags"]); tags != "" {
			return "tags: " + tags
		}
	case "/api/project_links/create":
		name := asString(body["name"])
		urlStr := asString(body["url"])
		switch {
		case name != "" && urlStr != "":
			return name + " (" + urlStr + ")"
		case name != "":
			return name
		case urlStr != "":
			return urlStr
		}
	case "/api/settings/set":
		key := asString(body["key"])
		val := asString(body["value"])
		switch {
		case key != "" && val != "":
			return key + " = " + val
		case key != "":
			return key
		}
	case "/api/permissions/add_group":
		group := asString(body["groupName"])
		perm := asString(body["permission"])
		switch {
		case group != "" && perm != "":
			return group + " → " + perm
		case group != "":
			return group
		case perm != "":
			return perm
		}
	case "/api/webhooks/create":
		name := asString(body["name"])
		urlStr := asString(body["url"])
		switch {
		case name != "" && urlStr != "":
			return name + " (" + urlStr + ")"
		case name != "":
			return name
		}
	}
	return ""
}

// applyProjectFailures routes projects in Succeeded with matching
// per-project failures into NearPerfect (yellow) or Partial (orange).
// When both yellow and orange failures apply to the same project, the
// project lands in Partial (orange dominates per the #224 taxonomy).
//
// Detail in Project EntityItems is the cloud_project_key (sometimes
// suffixed with "|scan:..."); we strip the suffix for matching.
func applyProjectFailures(succeeded, nearPerfect, partial []EntityItem,
	failures []projectFailure) ([]EntityItem, []EntityItem, []EntityItem) {

	if len(failures) == 0 || len(succeeded) == 0 {
		return succeeded, nearPerfect, partial
	}
	// Group failures by project key, accumulating the worst bucket
	// (orange wins) and one Issues line per Operation+detail combo.
	type perProject struct {
		worst projectOutcomeBucket
		// issues by operation → ordered list of details so the same
		// operation can carry multiple distinct subjects (multiple
		// failing settings, several groups, etc.).
		byOp     map[string][]string
		opErrors map[string]string
		opsOrder []string
	}
	byKey := make(map[string]*perProject)
	for _, f := range failures {
		pp, ok := byKey[f.CloudProjectKey]
		if !ok {
			pp = &perProject{worst: f.Bucket, byOp: map[string][]string{}, opErrors: map[string]string{}}
			byKey[f.CloudProjectKey] = pp
		}
		if f.Bucket > pp.worst {
			pp.worst = f.Bucket
		}
		// Route-only failures (empty Operation) bubble the bucket but
		// add nothing to Issues — used by #359 to push project-data-
		// skipped projects into Partial without re-stating the reason
		// (the |scan: marker already carries it as a head line).
		if f.Operation == "" {
			continue
		}
		if _, seen := pp.byOp[f.Operation]; !seen {
			pp.opsOrder = append(pp.opsOrder, f.Operation)
		}
		if f.Detail != "" {
			pp.byOp[f.Operation] = append(pp.byOp[f.Operation], f.Detail)
		}
		if f.Error != "" && pp.opErrors[f.Operation] == "" {
			pp.opErrors[f.Operation] = f.Error
		}
	}

	render := func(pp *perProject) []string {
		lines := make([]string, 0, len(pp.opsOrder))
		for _, op := range pp.opsOrder {
			details := pp.byOp[op]
			// Dedup while preserving first-seen order.
			seen := map[string]bool{}
			var unique []string
			for _, d := range details {
				if d == "" || seen[d] {
					continue
				}
				seen[d] = true
				unique = append(unique, d)
			}
			sort.Strings(unique) // stable rendering for testability
			line := op
			if len(unique) > 0 {
				line += ": " + strings.Join(unique, ", ")
			}
			if msg := pp.opErrors[op]; msg != "" {
				line = fmt.Sprintf("%s — %s", line, msg)
			}
			lines = append(lines, line)
		}
		return lines
	}

	keep := succeeded[:0:0]
	for _, item := range succeeded {
		key := projectCloudKey(item.Detail)
		pp, ok := byKey[key]
		if !ok {
			keep = append(keep, item)
			continue
		}
		moved := item
		moved.Issues = append(append([]string(nil), item.Issues...), render(pp)...)
		switch pp.worst {
		case projectBucketPartial:
			partial = append(partial, moved)
		default:
			nearPerfect = append(nearPerfect, moved)
		}
	}
	return keep, nearPerfect, partial
}

// collectUnsupportedLanguageExclusions returns one Partial failure per project
// whose scanner report had file components dropped because their language has
// no quality profile on the target organization (#474,
// --unsupported_languages=exclude).
//
// Those branches import successfully, so without this the project would be
// reported as a full migration even though some of its files — and every issue
// on them — never left the source. The project is routed to Partial with an
// Issues line naming the languages and the file count.
func collectUnsupportedLanguageExclusions(store *common.DataStore) []projectFailure {
	items, err := store.ReadAll("importProjectData")
	if err != nil || len(items) == 0 {
		return nil
	}
	by, order := aggregateLanguageExclusions(items)

	out := make([]projectFailure, 0, len(order))
	for _, key := range order {
		bucket := by[key]
		out = append(out, projectFailure{
			CloudProjectKey: key,
			Bucket:          projectBucketPartial,
			Operation:       "Files in unsupported languages not migrated",
			Detail:          languageExclusionDetail(bucket),
			Error: "the target organization has no quality profile for these languages — typically " +
				"languages provided by 3rd-party SonarQube Server plugins. The files were excluded from the " +
				"analysis report so the rest of the project could migrate; issues on them were not migrated",
		})
	}
	return out
}

// languageExclusionAcc accumulates one project's excluded-file data across all
// of its branch records.
type languageExclusionAcc struct {
	langs map[string]bool
	files int
}

// aggregateLanguageExclusions folds the per-branch importProjectData records
// into one accumulator per project, preserving first-seen project order.
func aggregateLanguageExclusions(items []json.RawMessage) (map[string]*languageExclusionAcc, []string) {
	by := make(map[string]*languageExclusionAcc)
	var order []string
	for _, raw := range items {
		key := jsonStr(raw, "cloud_project_key")
		excluded := jsonInt(raw, "excluded_files")
		if key == "" || excluded <= 0 {
			continue
		}
		bucket := by[key]
		if bucket == nil {
			bucket = &languageExclusionAcc{langs: map[string]bool{}}
			by[key] = bucket
			order = append(order, key)
		}
		// Every branch of a project drops the same files, so the per-project
		// count is the worst branch's count — not the sum across branches.
		if excluded > bucket.files {
			bucket.files = excluded
		}
		for _, lang := range jsonStrSlice(raw, "unsupported_languages") {
			bucket.langs[lang] = true
		}
	}
	return by, order
}

// languageExclusionDetail renders "lua, delphi (13 files)".
func languageExclusionDetail(bucket *languageExclusionAcc) string {
	langs := make([]string, 0, len(bucket.langs))
	for l := range bucket.langs {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	unit := "files"
	if bucket.files == 1 {
		unit = "file"
	}
	return fmt.Sprintf("%s (%d %s)", strings.Join(langs, ", "), bucket.files, unit)
}

// jsonStrSlice reads a JSON array of strings from raw, returning nil when the
// key is absent or is not an array of strings.
func jsonStrSlice(raw json.RawMessage, key string) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	nested, ok := obj[key]
	if !ok {
		return nil
	}
	var out []string
	if err := json.Unmarshal(nested, &out); err != nil {
		return nil
	}
	return out
}

// collectProjectSyncSkips reads the per-project status JSONL produced
// by the data-migration tasks and returns a synthetic []projectFailure
// covering #228's orange criteria plus #356's yellow criteria:
//
//   - importProjectData rows with status != "success" → Partial,
//     ROUTE-ONLY (empty Operation): the new |scan: marker attached by
//     attachProjectData renders the operator-facing skip reason in the
//     head, so this entry is solely for bucket routing (#359).
//   - syncHotspotMetadata / syncIssueMetadata rows whose source-issue
//     could not be resolved to a single cloud counterpart on the same
//     line (line_mismatch > 0 || not_found > 0) → NearPerfect,
//     ROUTE-ONLY. The sync stats line on the Detail (#356) already
//     conveys "X% synced (N/M)" — duplicating it as an Issues line
//     was operator noise, dropped per #359 follow-up.
//   - syncHotspotMetadata / syncIssueMetadata rows with error != ""
//     → Partial, "<task> errored" — gated on the project's data
//     import being successful (otherwise the sync error is moot).
//
// scanMap (the per-project data outcomes from collectProjectData) is
// used to gate the sync-side failures.
func collectProjectSyncSkips(store *common.DataStore, scanMap map[string]projectDataOutcome) []projectFailure {
	dataSkipped := func(key string) bool {
		o, ok := scanMap[key]
		if !ok {
			return false
		}
		return o.State == "skipped" || o.State == "failed"
	}

	var out []projectFailure

	// importProjectData — one row per branch per project. Emit a
	// route-only Partial failure (empty Operation/Detail) so the
	// project lands in the Partial bucket without duplicating the
	// |scan: marker's "Project data migration skipped: <reason>"
	// head line into Issues.
	seenSkipped := make(map[string]bool)
	historyItems, _ := store.ReadAll("importProjectData")
	for _, raw := range historyItems {
		key := jsonStr(raw, "cloud_project_key")
		status := jsonStr(raw, "status")
		if key == "" || status == "success" || seenSkipped[key] {
			continue
		}
		// #432 — a project that was provisioned but never analyzed on the
		// source must keep whatever outcome the other migration steps gave
		// it (its settings still migrated); do not route it to Partial.
		if o, ok := scanMap[key]; ok && o.NeverAnalyzed {
			continue
		}
		seenSkipped[key] = true
		out = append(out, projectFailure{
			CloudProjectKey: key,
			Bucket:          projectBucketPartial,
			// Operation/Detail intentionally empty — see applyProjectFailures.
		})
	}

	// Per-project issue / hotspot sync rows — Near perfect when b+c > 0
	// (route-only; the sync stats line carries the synced fraction),
	// Partial when a fatal error was captured. Skip emission entirely
	// for projects whose data import was skipped or failed — there's
	// nothing meaningful to say about sync fidelity in that case.
	for _, f := range collectSyncOutcome(store, "syncIssueMetadata", "Issue sync errored") {
		if dataSkipped(f.CloudProjectKey) {
			continue
		}
		out = append(out, f)
	}
	for _, f := range collectSyncOutcome(store, "syncHotspotMetadata", "Hotspot sync errored") {
		if dataSkipped(f.CloudProjectKey) {
			continue
		}
		out = append(out, f)
	}

	return out
}

// collectSyncOutcome reads per-project sync records for a given task
// (syncIssueMetadata or syncHotspotMetadata) and converts them to
// projectFailure entries:
//
//   - line_mismatch + not_found > 0 → route-only NearPerfect failure
//     (empty Operation/Detail). The sync stats line attached via
//     attachSyncStats already communicates "X% of items synced (N/M)";
//     the unresolved-counterparts detail was redundant and was
//     dropped per #359 follow-up.
//   - error != "" → Partial failure with the captured error as the
//     visible Issues line, labelled with errorOp.
func collectSyncOutcome(store *common.DataStore, taskName, errorOp string) []projectFailure {
	items, err := store.ReadAll(taskName)
	if err != nil || len(items) == 0 {
		return nil
	}
	var out []projectFailure
	for _, raw := range items {
		key := jsonStr(raw, "cloud_project_key")
		if key == "" {
			continue
		}
		lineMismatch := jsonInt(raw, "line_mismatch")
		notFound := jsonInt(raw, "not_found")
		// #323: hotspot-only field; absent on issue sync records.
		ackDemoted := jsonInt(raw, "acknowledged_demoted")
		errMsg := jsonStr(raw, "error")

		if lineMismatch+notFound+ackDemoted > 0 {
			out = append(out, projectFailure{
				CloudProjectKey: key,
				Bucket:          projectBucketNearPerfect,
				// Operation/Detail intentionally empty — route-only.
				// The sync stats line on the Detail already conveys
				// the "X% synced (N/M)" head and the dedicated
				// "N ACKNOWLEDGED hotspot(s)…" line (#323) when any
				// were demoted.
			})
		}
		if errMsg != "" {
			out = append(out, projectFailure{
				CloudProjectKey: key,
				Bucket:          projectBucketPartial,
				Operation:       errorOp,
				Error:           errMsg,
			})
		}
	}
	return out
}

// bindingSkipOperations maps the skip reasons recorded by the migrate
// package's matchProjectRepos task to the operator-facing sentence shown
// in the report's Details column (issue #122).
//
// The map is keyed by the same string constants the migrate package
// writes (migrate.BindingSkip*); they are duplicated here rather than
// imported to keep internal/report free of a dependency on
// internal/migrate, matching how the global-settings outcome contract is
// already handled.
//
// org_binding_unknown / repos_unknown (issue #505) exist because the
// lookups feeding a project binding are best-effort: when one of them
// fails the tool never learns whether the org is bound or which
// repositories it has, and saying "the org is not bound" would state
// something it never observed. Those two records carry the API error in
// skip_error, which is rendered after the sentence (issue #122 asked for
// the API error to be surfaced in the report).
var bindingSkipOperations = map[string]string{
	"org_not_bound":       "project binding was not possible because the org itself is not bound",
	"repo_not_found":      "project binding was not possible because the repository was not found in the bound DevOps organization",
	"no_project_id":       "project binding was not possible because the target project id could not be resolved",
	"org_binding_unknown": "project binding was not possible because the target organization's DevOps platform binding could not be read",
	"repos_unknown":       "project binding was not possible because the repositories of the bound DevOps organization could not be listed",
}

// collectProjectBindingOutcomes reads the per-project DevOps platform
// binding records written by matchProjectRepos / setProjectBinding and
// converts the non-successful ones into projectFailure entries so the
// affected projects are reported as Partial Migration (issue #122).
//
// Two shapes are handled:
//
//   - skip records ({"binding_skipped":true,"skip_reason":...}) — the
//     binding was never attempted. The dominant case is a source project
//     that WAS bound while the target organization is not bound to the
//     same DevOps platform, which issue #122 requires to be reported as
//     partially migrated with an explicit explanation.
//   - failed writes ({"status":"failed","error":...}) — the binding call
//     itself was rejected by SonarQube Cloud.
//
// Records are read from setProjectBinding when present (it forwards the
// skip records verbatim and adds the write outcomes) and fall back to
// matchProjectRepos so the skips are still reported when the binding
// write task did not run.
func collectProjectBindingOutcomes(store *common.DataStore) []projectFailure {
	items, err := store.ReadAll("setProjectBinding")
	if err != nil || len(items) == 0 {
		items, err = store.ReadAll("matchProjectRepos")
		if err != nil {
			return nil
		}
	}

	var out []projectFailure
	seen := make(map[string]bool)
	for _, raw := range items {
		key := jsonStr(raw, "cloud_project_key")
		op, errMsg, ok := classifyBindingRecord(raw)
		if key == "" || !ok {
			continue
		}
		// One line per project per distinct message.
		dedup := key + "\x00" + op
		if seen[dedup] {
			continue
		}
		seen[dedup] = true
		out = append(out, projectFailure{
			CloudProjectKey: key,
			Bucket:          projectBucketPartial,
			Operation:       op,
			Error:           errMsg,
		})
	}
	return out
}

// classifyBindingRecord turns one DevOps-binding record into the operator
// sentence and error to report. ok is false for records that need no
// report entry (a successful binding, or an unrecognised shape).
func classifyBindingRecord(raw json.RawMessage) (op, errMsg string, ok bool) {
	if jsonBool(raw, "binding_skipped") {
		reason := jsonStr(raw, "skip_reason")
		op = bindingSkipOperations[reason]
		if op == "" {
			// Prefer the sentence the migrate side recorded so a newly
			// added reason still renders something useful.
			op = jsonStr(raw, "skip_detail")
		}
		if op == "" {
			op = "DevOps platform binding was not migrated"
		}
		// skip_error is set only when a failed lookup — not an observed
		// fact — caused the skip (#505); it is the API error to quote.
		return op, jsonStr(raw, "skip_error"), true
	}
	if jsonStr(raw, "status") == "failed" {
		return "DevOps platform binding not migrated", jsonStr(raw, "error"), true
	}
	return "", "", false
}
