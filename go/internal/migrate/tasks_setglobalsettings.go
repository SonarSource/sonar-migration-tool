package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/types"
	"golang.org/x/sync/errgroup"
)

// exclusionPair captures a "global vs local" SQS pair that has no
// direct SQC equivalent for the global half — the migrate task folds the
// global patterns into the local key before posting.
//
// Today this covers:
//
//   - sonar.global.exclusions      → sonar.exclusions
//   - sonar.global.test.exclusions → sonar.test.exclusions
//
// More pairs can be added without touching the merge or detail-render
// logic; just append to globalExclusionPairs.
type exclusionPair struct {
	// SQSGlobalKey is the SQS-side key with no SQC counterpart. Its
	// patterns are moved onto SQSLocalKey before migration.
	SQSGlobalKey string
	// SQSLocalKey is the SQS key that DOES have a SQC counterpart
	// (same name on both sides). After the merge it carries the
	// union of patterns from both sources.
	SQSLocalKey string
}

var globalExclusionPairs = []exclusionPair{
	{SQSGlobalKey: "sonar.global.exclusions", SQSLocalKey: "sonar.exclusions"},
	{SQSGlobalKey: "sonar.global.test.exclusions", SQSLocalKey: "sonar.test.exclusions"},
}

// runSetGlobalSettings migrates customized SQS-side global settings to
// every SonarQube Cloud organization in scope (issue #186).
//
// Pipeline:
//
//  1. Read getServerSettingsDefinitions to learn each SQS setting's
//     defaultValue (and shape) — that's how we detect "customized".
//  2. Read getServerSettings (raw values) and filter out any setting
//     whose value equals the SQS default — uncustomized settings are
//     skipped entirely.
//  3. Read generateOrganizationMappings and collect every target
//     sonarcloud_org_key that isn't empty / SKIPPED.
//  4. For each org, fetch SQC's list_definitions once (cached) so we know
//     which keys actually exist on the target and what shape (single /
//     multi / property-set) they expect.
//  5. For each (customized SQS setting × target SQC org):
//     – not in SQC's defs → log Warn, record skipped(reason=not-on-sqc).
//     – in SQC's defs → dispatch via applySettingByDef (the same helper
//     that drives setProjectSettings, but with empty projectKey so the
//     SDK scopes the request to the organization).
//  6. Emit one JSONL record per setting key, with applied / failed /
//     skipped org lists plus a pre-built "detail" string that the
//     summary report renders verbatim.
func runSetGlobalSettings(ctx context.Context, e *Executor) error {
	// getServerSettings and getServerSettingsDefinitions are EXTRACT
	// tasks — their output lives in the per-server extract directories
	// (one level above the migrate run dir) and is reached via
	// readExtractItems / e.Mapping. Using e.Store.ReadAll here would
	// silently return zero records and the task would no-op.
	sqsDefItems, err := readExtractItems(e, "getServerSettingsDefinitions")
	if err != nil {
		return fmt.Errorf("setGlobalSettings: reading getServerSettingsDefinitions: %w", err)
	}
	sqsDefaultByKey := make(map[string]string, len(sqsDefItems))
	for _, d := range sqsDefItems {
		k := extractField(d.Data, "key")
		if k == "" {
			continue
		}
		sqsDefaultByKey[k] = extractField(d.Data, "defaultValue")
	}

	// Raw SQS global settings — kept only when customized.
	sqsItems, err := readExtractItems(e, "getServerSettings")
	if err != nil {
		return fmt.Errorf("setGlobalSettings: reading getServerSettings: %w", err)
	}
	sqsValues := make([]json.RawMessage, 0, len(sqsItems))
	for _, it := range sqsItems {
		sqsValues = append(sqsValues, it.Data)
	}
	customized := make([]json.RawMessage, 0, len(sqsValues))
	for _, raw := range sqsValues {
		key := extractField(raw, "key")
		if key == "" {
			continue
		}
		if !isSettingCustomized(raw, sqsDefaultByKey[key]) {
			continue
		}
		customized = append(customized, raw)
	}

	// SQS exposes platform-enforced exclusion patterns via the
	// sonar.global.* keys (today: sonar.global.exclusions and
	// sonar.global.test.exclusions). SQC has no global counterparts,
	// so the migrate task folds each global key's patterns into its
	// non-global sibling (sonar.exclusions / sonar.test.exclusions)
	// and drops the global side from the migration list — otherwise
	// a "not-on-sqc" Warn would fire and the platform-enforced
	// patterns would be silently lost.
	for _, pair := range globalExclusionPairs {
		customized = mergeGlobalIntoLocal(pair, sqsValues, customized, e.Logger)
	}

	// Target SQC orgs.
	orgItems, _ := e.Store.ReadAll("generateOrganizationMappings")
	orgs := make(map[string]struct{})
	orgList := make([]string, 0, len(orgItems))
	for _, o := range orgItems {
		orgKey := extractField(o, "sonarcloud_org_key")
		if shouldSkipOrg(orgKey) {
			continue
		}
		if _, dup := orgs[orgKey]; dup {
			continue
		}
		orgs[orgKey] = struct{}{}
		orgList = append(orgList, orgKey)
	}
	sort.Strings(orgList)

	e.Logger.Info("starting task", "task", "setGlobalSettings",
		"customized_settings", len(customized), "target_orgs", len(orgList))

	// One list_definitions fetch per target org (org scope).
	defsByOrg := loadSettingDefinitionsForOrgs(ctx, e, orgs, "setGlobalSettings")

	// Project-scope defs cover the superset of keys visible to a
	// project (language settings, external-analyzer settings, etc.).
	// Used below to distinguish "truly not on SQC" (warn) from "exists
	// at project scope only — handled by setProjectSettings" (info).
	// Issues #189 / #191.
	projects, _ := e.Store.ReadAll("createProjects")
	projectKeyMap := make(map[string]projectMapping, len(projects))
	for _, p := range projects {
		serverURL := extractField(p, "server_url")
		key := extractField(p, "key")
		projectKeyMap[serverURL+key] = projectMapping{
			CloudKey: extractField(p, "cloud_project_key"),
			OrgKey:   extractField(p, "sonarcloud_org_key"),
		}
	}
	projectDefsByOrg := loadProjectScopedSettingDefinitionsForOrgs(ctx, e, projectKeyMap, "setGlobalSettings")

	// Read getProjectSettings extract so the fan-out (below) knows
	// which (project, key) pairs already have a per-project SQS
	// override. setProjectSettings's per-record loop applies those in
	// parallel with this task; without the coverage map the fan-out
	// would race against — and potentially overwrite — those values.
	overrideCovered := buildPerProjectOverrideCoverage(e)

	counter := NewTaskCounter("setGlobalSettings")
	w, err := e.Store.Writer("setGlobalSettings")
	if err != nil {
		return err
	}

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))
	for _, raw := range customized {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			rec := applyOneGlobalSetting(gctx, e, raw, orgList, defsByOrg, projectDefsByOrg, projectKeyMap, overrideCovered, counter)
			b, _ := json.Marshal(rec)
			mu.Lock()
			defer mu.Unlock()
			return w.WriteOne(b)
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	counter.LogSummary(e.Logger)
	return nil
}

// applyOneGlobalSetting applies a single customized SQS global setting to
// every target SQC org and returns a result record describing the per-org
// outcomes plus a pre-built detail string for the report.
func applyOneGlobalSetting(ctx context.Context, e *Executor, raw json.RawMessage, orgs []string,
	defsByOrg, projectDefsByOrg map[string]map[string]types.SettingDefinition,
	projectKeyMap map[string]projectMapping,
	overrideCovered map[string]map[string]bool,
	counter *TaskCounter) globalSettingResult {

	key := extractField(raw, "key")
	rec := globalSettingResult{Key: key}
	rec.Value, rec.Values, rec.FieldValues = readSettingPayload(raw)
	// Carry the merge provenance through to the output record so the
	// report can call out which SQS-global key contributed patterns to
	// this SQC-side key (e.g. sonar.global.test.exclusions for the
	// sonar.test.exclusions record).
	rec.MergedFromGlobal = extractField(raw, "_merged_from")

	// Memoize the "SQC rejects this key at org level" verdict across
	// the orgs we iterate below. Without this, every org would try
	// the same failing org-scope POST before falling back; with it,
	// we try at most ONCE and reuse the result for the remaining
	// orgs. The verdict is per-key but cached across orgs because
	// SQC's list_definitions inconsistency is a property of the
	// setting key, not of the org.
	orgRejected := false

	valueSummary := renderValueSummary(rec)
	mergeSuffix := ""
	if rec.MergedFromGlobal != "" {
		mergeSuffix = fmt.Sprintf(" (merged from %s + %s)", rec.MergedFromGlobal, key)
	}

	for _, org := range orgs {
		def, hasDef := defsByOrg[org][key]
		if !hasDef {
			// Distinguish two cases here (issues #189 / #191):
			//   - key NOT in org-scope and NOT in project-scope → truly
			//     unknown to SQC; keep the existing Warn.
			//   - key NOT in org-scope BUT IS in project-scope →
			//     handled by setProjectSettings's propagation pass;
			//     downgrade to Info and use a non-alarming reason so
			//     the report doesn't flag it red.
			if _, atProject := projectDefsByOrg[org][key]; atProject {
				e.Logger.Info("setGlobalSettings: key exists only at SQC project scope, will be propagated by setProjectSettings",
					"key", key, "org", org)
				rec.Outcomes = append(rec.Outcomes, orgOutcome{
					Org:    org,
					Status: outcomeSkipped,
					Reason: "project-scope-only",
					Detail: "Skipped (handled by setProjectSettings)" + mergeSuffix,
				})
				continue
			}
			e.Logger.Warn("setGlobalSettings: setting key not available on SQC, skipping",
				"key", key, "org", org)
			rec.Outcomes = append(rec.Outcomes, orgOutcome{
				Org:    org,
				Status: outcomeSkipped,
				Reason: "not-on-sqc",
				Detail: "Skipped (not on SQC)" + mergeSuffix,
			})
			continue
		}
		// If a previous org for THIS key already rejected the
		// org-scope POST, skip the org attempt entirely and go
		// straight to the project fan-out. Saves one wasted 400 per
		// extra org.
		if orgRejected {
			rec.Outcomes = append(rec.Outcomes,
				fanOutOutcome(ctx, e, raw, key, org, valueSummary, mergeSuffix, projectDefsByOrg, projectKeyMap, overrideCovered, counter, /*alreadyKnown=*/ true))
			continue
		}

		err := applySettingByDef(ctx, e, "", org, raw, key, def, true)
		switch {
		case errors.Is(err, errSettingEmpty):
			rec.Outcomes = append(rec.Outcomes, orgOutcome{
				Org: org, Status: outcomeSkipped, Reason: "empty",
				Detail: "Skipped (empty payload)" + mergeSuffix,
			})
		case sqapi.IsOrgLevelRejection(err):
			// SQC's list_definitions falsely reported this key as
			// settable at org scope (e.g. sonar.coverage.jacoco.xmlReportPaths,
			// sonar.androidLint.reportPaths). Fall back to setting the
			// value on each project in that org. Mark orgRejected so
			// the remaining orgs in the loop skip the failing org-
			// scope POST altogether.
			orgRejected = true
			rec.Outcomes = append(rec.Outcomes,
				fanOutOutcome(ctx, e, raw, key, org, valueSummary, mergeSuffix, projectDefsByOrg, projectKeyMap, overrideCovered, counter, /*alreadyKnown=*/ false))
		case err != nil:
			counter.Fail()
			logAPIWarn(e.Logger, "setGlobalSettings failed", err, "key", key, "org", org)
			rec.Outcomes = append(rec.Outcomes, orgOutcome{
				Org: org, Status: outcomeFailed, Reason: err.Error(),
				Detail: "Failed: " + apiErrMessage(err) + mergeSuffix,
			})
		default:
			counter.Success()
			rec.Outcomes = append(rec.Outcomes, orgOutcome{
				Org: org, Status: outcomeApplied,
				Detail: "Applied (" + valueSummary + ")" + mergeSuffix,
			})
		}
	}
	return rec
}

// renderValueSummary picks the compact form of the value (value=X /
// values=[a,b] / fieldValues=[...]) for inclusion in per-row Detail
// strings. The shape mirrors what /api/settings/set would receive.
func renderValueSummary(r globalSettingResult) string {
	switch {
	case len(r.FieldValues) > 0:
		b, _ := json.Marshal(r.FieldValues)
		return "fieldValues=" + string(b)
	case len(r.Values) > 0:
		return "values=[" + strings.Join(r.Values, ",") + "]"
	default:
		return "value=" + r.Value
	}
}

// apiErrMessage extracts the API error message when err is an
// sqapi.APIError, falling back to err.Error() otherwise. Keeps the
// report's Detail text compact (no Method/URL noise).
func apiErrMessage(err error) string {
	var apiErr *sqapi.APIError
	if errors.As(err, &apiErr) {
		if msg := apiErr.Message(); msg != "" {
			return msg
		}
	}
	return err.Error()
}

// fanOutOutcome handles the per-org fan-out when SQC rejected the
// org-scope POST for a key (or when we've already seen that rejection
// on a previous org during this run). Per-project SQS overrides are
// skipped so we don't race against the parallel setProjectSettings
// per-record loop. Returns a single orgOutcome with a Detail string
// that summarises the apply ("Applied to all projects (value=…)") and
// enumerates any project-level exceptions.
func fanOutOutcome(ctx context.Context, e *Executor, raw json.RawMessage,
	key, org, valueSummary, mergeSuffix string,
	projectDefsByOrg map[string]map[string]types.SettingDefinition,
	projectKeyMap map[string]projectMapping,
	overrideCovered map[string]map[string]bool,
	counter *TaskCounter, alreadyKnown bool) orgOutcome {

	projectDef, hasProjDef := projectDefsByOrg[org][key]
	if !hasProjDef {
		// Pathological — org said yes, project says no.
		counter.Fail()
		return orgOutcome{
			Org: org, Status: outcomeFailed,
			Reason: "rejected at org scope, key absent from project scope",
			Detail: "Failed: rejected at org scope, key absent from project scope" + mergeSuffix,
		}
	}
	if alreadyKnown {
		e.Logger.Info("setGlobalSettings: org-level write already rejected for this key, propagating to projects without retrying",
			"key", key, "org", org)
	} else {
		e.Logger.Info("setGlobalSettings: key not settable at org level despite list_definitions claim, propagating to projects",
			"key", key, "org", org)
	}
	applied, failed, skipped := fanOutGlobalToProjects(ctx, e, raw, key, org, projectDef, projectKeyMap, overrideCovered)
	for range applied {
		counter.Success()
	}
	for range failed {
		counter.Fail()
	}

	// Branch the Detail wording on actual per-project counts so the
	// row doesn't say "Applied to all projects" while also listing
	// failures — that's the contradictory text the report showed
	// before. Three cases:
	//
	//   - applied=N, failed=0 → "Applied to all projects (value=…)"
	//   - applied=N, failed=M → "Applied to N of (N+M) projects
	//                            (value=…) (failed: …; skipped: …)"
	//   - applied=0, failed=M → "Failed: N projects (e.g. <error>)"
	//
	// Override-skipped projects (per-project SQS override wins) are
	// listed alongside failures but are not failures themselves.
	a, f, s := len(applied), len(failed), len(skipped)
	total := a + f
	var detail string
	var status string
	switch {
	case a > 0 && f == 0:
		detail = "Applied to all projects (" + valueSummary + ")"
		status = outcomeAppliedToProjects
	case a > 0 && f > 0:
		detail = fmt.Sprintf("Applied to %d of %d projects (%s)", a, total, valueSummary)
		status = outcomePartial
	default: // a == 0 — every fan-out project failed (or no targets).
		if f > 0 {
			detail = fmt.Sprintf("Failed: %d project(s), e.g. %s", f, failed[0].reason)
		} else {
			detail = "Failed: no eligible projects in org"
		}
		status = outcomeFailed
	}
	var notes []string
	if f > 0 {
		names := make([]string, 0, f)
		for _, p := range failed {
			names = append(names, p.project)
		}
		notes = append(notes, "failed: "+strings.Join(names, ", "))
	}
	if s > 0 {
		names := make([]string, 0, s)
		for _, p := range skipped {
			names = append(names, p+" (override)")
		}
		notes = append(notes, "skipped: "+strings.Join(names, ", "))
	}
	if len(notes) > 0 {
		detail += " (" + strings.Join(notes, "; ") + ")"
	}
	detail += mergeSuffix
	out := orgOutcome{Org: org, Status: status, Detail: detail}
	if status == outcomeFailed && f > 0 {
		// Surface a concrete API error message in the Reason so the
		// report's Failed bucket can populate EntityItem.ErrorMessage.
		out.Reason = failed[0].reason
	}
	return out
}

// projectFanOutFailure is returned from fanOutGlobalToProjects for each
// project where the per-project apply failed, so the caller can roll
// the error into its result record.
type projectFanOutFailure struct {
	project string
	reason  string
}

// fanOutGlobalToProjects applies a global setting record to every
// project in the given org using the same definition-aware dispatcher
// that setProjectSettings uses. This is the runtime fallback when
// SQC's list_definitions claims a key is settable at org-scope but
// /api/settings/set?organization=... returns 400 — the key is in
// reality project-scope-only.
//
// Projects whose source SQS has a per-project override for this key
// (recorded in overrideCovered, keyed by serverURL+sourceKey) are
// skipped here — setProjectSettings applies their specific value via
// its per-record loop; the fan-out would race against it and could
// clobber the override. Such projects are returned as `skipped` so
// the caller can include them in the result record.
func fanOutGlobalToProjects(ctx context.Context, e *Executor, raw json.RawMessage,
	key, org string, def types.SettingDefinition,
	projectKeyMap map[string]projectMapping,
	overrideCovered map[string]map[string]bool,
) (applied []string, failed []projectFanOutFailure, skipped []string) {

	for projLookupKey, pm := range projectKeyMap {
		if pm.OrgKey != org {
			continue
		}
		if overrideCovered[projLookupKey][key] {
			e.Logger.Debug("setGlobalSettings: per-project override wins, skipping fan-out",
				"project", pm.CloudKey, "key", key, "org", org)
			skipped = append(skipped, pm.CloudKey)
			continue
		}
		err := applySettingByDef(ctx, e, pm.CloudKey, pm.OrgKey, raw, key, def, true)
		switch {
		case errors.Is(err, errSettingEmpty):
			// nothing to do
		case err != nil:
			logAPIWarn(e.Logger, "setGlobalSettings: project fan-out failed", err,
				"key", key, "project", pm.CloudKey, "org", org)
			failed = append(failed, projectFanOutFailure{project: pm.CloudKey, reason: err.Error()})
		default:
			applied = append(applied, pm.CloudKey)
		}
	}
	return applied, failed, skipped
}

// buildPerProjectOverrideCoverage scans the getProjectSettings extract
// and returns a {serverURL+projectKey -> {settingKey -> true}} set of
// per-project SQS overrides. setProjectSettings's per-record loop
// applies these in parallel; the fan-out in fanOutGlobalToProjects
// uses this map to avoid clobbering them. Errors are swallowed: an
// empty map degrades gracefully to the previous behaviour (fan-out
// over-writes overrides) rather than failing the migration.
func buildPerProjectOverrideCoverage(e *Executor) map[string]map[string]bool {
	items, err := readExtractItems(e, "getProjectSettings")
	if err != nil {
		e.Logger.Debug("setGlobalSettings: could not read getProjectSettings extract for override coverage",
			"err", err)
		return map[string]map[string]bool{}
	}
	out := make(map[string]map[string]bool)
	for _, it := range items {
		projectKey := extractField(it.Data, "project")
		if projectKey == "" {
			projectKey = extractField(it.Data, "projectKey")
		}
		settingKey := extractField(it.Data, "key")
		if projectKey == "" || settingKey == "" {
			continue
		}
		lookup := it.ServerURL + projectKey
		if out[lookup] == nil {
			out[lookup] = make(map[string]bool)
		}
		out[lookup][settingKey] = true
	}
	return out
}

// mergeGlobalIntoLocal folds the SQS-side patterns from a global-only
// key (pair.SQSGlobalKey) into the related key that exists on SQC
// (pair.SQSLocalKey), so platform-enforced patterns aren't lost during
// migration. The synthesized record carries a _merged_from marker
// holding the global key name; renderGlobalSettingDetail uses it to
// produce a "merged from <global> + <local>" note in the report.
//
// Behaviour rules (same shape for every pair):
//   - If pair.SQSGlobalKey has no values (or value=) on SQS, this is a
//     no-op — the original customized list passes through.
//   - If pair.SQSGlobalKey IS set, the synthesized pair.SQSLocalKey
//     record carries the union (order-preserving, deduped) of the
//     global patterns and the local patterns.
//   - pair.SQSGlobalKey is removed from the customized list — its
//     patterns have moved to pair.SQSLocalKey, so there's nothing to
//     migrate under the original key.
//   - If pair.SQSLocalKey wasn't customized on its own (only the
//     global side was) we still emit the synthesized record so the
//     global patterns make it to SQC.
func mergeGlobalIntoLocal(pair exclusionPair, sqsValues []json.RawMessage,
	customized []json.RawMessage, logger *slog.Logger) []json.RawMessage {

	// Look up both sides in the full extract — we need the global patterns
	// even if the local key wasn't filtered into `customized`.
	var globalRec, localRec json.RawMessage
	for _, raw := range sqsValues {
		switch extractField(raw, "key") {
		case pair.SQSGlobalKey:
			globalRec = raw
		case pair.SQSLocalKey:
			localRec = raw
		}
	}
	globalVals := readPatterns(globalRec)
	if len(globalVals) == 0 {
		return customized
	}
	localVals := readPatterns(localRec)
	merged := unionPreservingOrder(globalVals, localVals)

	synth := map[string]any{
		"key":          pair.SQSLocalKey,
		"values":       merged,
		"_merged_from": pair.SQSGlobalKey,
	}
	synthRaw, _ := json.Marshal(synth)

	out := make([]json.RawMessage, 0, len(customized)+1)
	replaced := false
	for _, raw := range customized {
		switch extractField(raw, "key") {
		case pair.SQSGlobalKey:
			// Drop — its patterns have moved into the local key.
			continue
		case pair.SQSLocalKey:
			out = append(out, synthRaw)
			replaced = true
		default:
			out = append(out, raw)
		}
	}
	if !replaced {
		// Local key wasn't in the customized list (it was at SQS
		// default), but the global side was set — synthesize a
		// record so the global patterns make it across.
		out = append(out, synthRaw)
	}
	logger.Info("setGlobalSettings: merged global key into local key",
		"global_key", pair.SQSGlobalKey,
		"local_key", pair.SQSLocalKey,
		"global_patterns", len(globalVals),
		"local_patterns", len(localVals),
		"merged_patterns", len(merged))
	return out
}

// readPatterns reads exclusion-style patterns from a setting record,
// handling both shapes that /api/settings/values may return (values=[...]
// for a multi-value field, value="csv,joined" for a single field).
func readPatterns(raw json.RawMessage) []string {
	if raw == nil {
		return nil
	}
	if vals := extractStringArray(raw, "values"); len(vals) > 0 {
		return vals
	}
	if v := extractField(raw, "value"); v != "" {
		return strings.Split(v, ",")
	}
	return nil
}

// unionPreservingOrder returns the deduplicated concatenation a ++ b,
// preserving first-seen order. Used to merge two exclusion lists into a
// stable, predictable single list.
func unionPreservingOrder(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range b {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// isSettingCustomized reports whether the SQS-side value for a setting
// differs from its declared defaultValue. SQS exposes values in three
// shapes (value / values / fieldValues); for the comparison we collapse
// each into a comparable scalar string — fieldValues collapses to its
// JSON encoding, values to a sorted CSV.
func isSettingCustomized(raw json.RawMessage, defaultValue string) bool {
	if fvs := extractObjectArray(raw, "fieldValues"); len(fvs) > 0 {
		// PROPERTY_SET — defaultValue is unlikely to match a complex
		// JSON payload, so treat any populated fieldValues as
		// customized.
		return true
	}
	if vals := extractStringArray(raw, "values"); len(vals) > 0 {
		sorted := append([]string(nil), vals...)
		sort.Strings(sorted)
		joined := strings.Join(sorted, ",")
		defSorted := strings.Split(defaultValue, ",")
		sort.Strings(defSorted)
		return joined != strings.Join(defSorted, ",")
	}
	return extractField(raw, "value") != defaultValue
}

// readSettingPayload extracts the three possible value shapes from a
// settings record so the result can be serialized back into the output
// JSONL record exactly as it came from SQS.
func readSettingPayload(raw json.RawMessage) (value string, values []string, fieldValues []map[string]any) {
	return extractField(raw, "value"),
		extractStringArray(raw, "values"),
		extractObjectArray(raw, "fieldValues")
}

// globalSettingResult is the per-setting record written to the
// setGlobalSettings task output (one JSONL line per setting key) and
// read back by the summary report to populate the Global Settings
// section. Outcomes carry the per-org result, so the report can
// render a row per (setting × org) with a Detail string specific to
// what happened on THAT org.
type globalSettingResult struct {
	Key              string           `json:"key"`
	Value            string           `json:"value,omitempty"`
	Values           []string         `json:"values,omitempty"`
	FieldValues      []map[string]any `json:"fieldValues,omitempty"`
	Outcomes         []orgOutcome     `json:"outcomes"`
	MergedFromGlobal string           `json:"merged_from_global,omitempty"`
}

// orgOutcome is the per-(setting, org) result. Status drives the
// report bucket (applied / applied-to-projects → Succeeded, failed →
// Failed, skipped → Skipped); Detail is the pre-rendered row text the
// report displays verbatim; Reason is forwarded to EntityItem's
// ErrorMessage (failed) or SkipReason (skipped) fields.
type orgOutcome struct {
	Org    string `json:"org"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Reason string `json:"reason,omitempty"`
}

// outcomeStatus* constants name the values orgOutcome.Status can take.
// Kept here (rather than as an exported enum on the report side) so
// migrate and report agree on the contract.
const (
	outcomeApplied           = "applied"
	outcomeAppliedToProjects = "applied-to-projects"
	outcomePartial           = "partial"
	outcomeFailed            = "failed"
	outcomeSkipped           = "skipped"
)
