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

	// One list_definitions fetch per target org.
	defsByOrg := loadSettingDefinitionsForOrgs(ctx, e, orgs, "setGlobalSettings")

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
			rec := applyOneGlobalSetting(gctx, e, raw, orgList, defsByOrg, counter)
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
	defsByOrg map[string]map[string]types.SettingDefinition, counter *TaskCounter) globalSettingResult {

	key := extractField(raw, "key")
	rec := globalSettingResult{Key: key}
	rec.Value, rec.Values, rec.FieldValues = readSettingPayload(raw)
	// Carry the merge provenance through to the output record so the
	// report can call out which SQS-global key contributed patterns to
	// this SQC-side key (e.g. sonar.global.test.exclusions for the
	// sonar.test.exclusions record).
	rec.MergedFromGlobal = extractField(raw, "_merged_from")

	for _, org := range orgs {
		def, hasDef := defsByOrg[org][key]
		if !hasDef {
			e.Logger.Warn("setGlobalSettings: setting key not available on SQC, skipping",
				"key", key, "org", org)
			rec.SkippedOrgs = append(rec.SkippedOrgs, skippedOrg{Org: org, Reason: "not-on-sqc"})
			continue
		}
		err := applySettingByDef(ctx, e, "", org, raw, key, def, true)
		switch {
		case errors.Is(err, errSettingEmpty):
			rec.SkippedOrgs = append(rec.SkippedOrgs, skippedOrg{Org: org, Reason: "empty"})
		case err != nil:
			counter.Fail()
			logAPIWarn(e.Logger, "setGlobalSettings failed", err, "key", key, "org", org)
			rec.FailedOrgs = append(rec.FailedOrgs, failedOrg{Org: org, Reason: err.Error()})
		default:
			counter.Success()
			rec.AppliedOrgs = append(rec.AppliedOrgs, org)
		}
	}
	rec.Detail = renderGlobalSettingDetail(rec)
	return rec
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

// renderGlobalSettingDetail produces the string the summary report shows
// in the Detail column for one global-setting row. Matches the format
// requested in issue #186: "value=… — applied to: org1, org2".
func renderGlobalSettingDetail(r globalSettingResult) string {
	var parts []string
	switch {
	case len(r.FieldValues) > 0:
		b, _ := json.Marshal(r.FieldValues)
		parts = append(parts, fmt.Sprintf("fieldValues=%s", string(b)))
	case len(r.Values) > 0:
		parts = append(parts, fmt.Sprintf("values=[%s]", strings.Join(r.Values, ",")))
	default:
		parts = append(parts, fmt.Sprintf("value=%s", r.Value))
	}
	if r.MergedFromGlobal != "" {
		// Surface the cross-key merge so an operator inspecting the
		// report can see where the patterns actually came from. This
		// satisfies the issue requirement that the report "detail
		// that SQC org <key> is set from the combination of the SQS
		// global <global-key> and <key> setting".
		parts = append(parts, fmt.Sprintf("merged from %s + %s", r.MergedFromGlobal, r.Key))
	}
	if len(r.AppliedOrgs) > 0 {
		parts = append(parts, "applied to: "+strings.Join(r.AppliedOrgs, ", "))
	}
	if len(r.SkippedOrgs) > 0 {
		skipped := make([]string, 0, len(r.SkippedOrgs))
		for _, s := range r.SkippedOrgs {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", s.Org, s.Reason))
		}
		parts = append(parts, "skipped: "+strings.Join(skipped, ", "))
	}
	if len(r.FailedOrgs) > 0 {
		failed := make([]string, 0, len(r.FailedOrgs))
		for _, f := range r.FailedOrgs {
			failed = append(failed, f.Org)
		}
		parts = append(parts, "failed: "+strings.Join(failed, ", "))
	}
	return strings.Join(parts, " — ")
}

// globalSettingResult is the per-setting record written to the
// setGlobalSettings task output (one JSONL line per setting key) and
// read back by the summary report to populate the Global Settings
// section.
type globalSettingResult struct {
	Key              string           `json:"key"`
	Value            string           `json:"value,omitempty"`
	Values           []string         `json:"values,omitempty"`
	FieldValues      []map[string]any `json:"fieldValues,omitempty"`
	AppliedOrgs      []string         `json:"applied_orgs,omitempty"`
	SkippedOrgs      []skippedOrg     `json:"skipped_orgs,omitempty"`
	FailedOrgs       []failedOrg      `json:"failed_orgs,omitempty"`
	Detail           string           `json:"detail"`
	MergedFromGlobal string           `json:"merged_from_global,omitempty"`
}

type skippedOrg struct {
	Org    string `json:"org"`
	Reason string `json:"reason"`
}

type failedOrg struct {
	Org    string `json:"org"`
	Reason string `json:"reason"`
}
