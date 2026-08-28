// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/cloud"
)

// configureTasks returns tasks that configure profiles, gates, and defaults.
func configureTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "setProfileParent",
			Dependencies: []string{"createProfiles"},
			Run:          runSetProfileParent,
		},
		{
			Name:         "restoreProfiles",
			Dependencies: []string{"createProfiles", "setProfileParent", "getProfileBackups"},
			Run:          runRestoreProfiles,
		},
		{
			Name:         "addGateConditions",
			Dependencies: []string{"createGates", "getGateConditions"},
			Run:          runAddGateConditions,
		},
		{
			// Analyse each migrated quality profile against the six
			// #226 yellow criteria and write per-finding rows. The
			// summary report consumes this output to move QPs from
			// Succeeded into NearPerfect with rule-key listings.
			Name:         "analyzeProfileRules",
			Dependencies: []string{"createProfiles"},
			Run:          runAnalyzeProfileRules,
		},
		{
			Name:         "setDefaultProfiles",
			Dependencies: []string{"createProfiles", "restoreProfiles"},
			Run:          runSetDefaultProfiles,
		},
		{
			Name:         "setDefaultGates",
			Dependencies: []string{"createGates", "addGateConditions"},
			Run:          runSetDefaultGates,
		},
		{
			Name:         "setDefaultTemplates",
			Dependencies: []string{"createPermissionTemplates"},
			Run:          runSetDefaultTemplates,
		},
	}
}

func runSetProfileParent(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItemFiltered(ctx, e, "setProfileParent", "createProfiles",
		func(item json.RawMessage) bool {
			return extractField(item, "parent_name") != ""
		},
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			name := extractField(item, "name")
			lang := extractField(item, "language")
			parent := extractField(item, "parent_name")

			err := e.Cloud.QualityProfiles.ChangeParent(ctx, lang, name, parent, orgKey)
			if err != nil {
				failAPI(counter, e.Logger, "setProfileParent failed", err, "name", name)
			} else {
				counter.Success()
			}
			return nil
		})
	return err
}

// loadProfileBackups indexes every quality-profile XML backup by profile
// key. A profile's backup is the complete ruleset, so this is multi-MB for
// something like Sonar way for Java.
//
// Built ONCE up front and shared across all profiles: the index is
// read-only after construction, so it is safe to fan out concurrently.
// Reading it inside the per-profile callback instead meant every one of
// the 25 workers held its own copy of every backup on the instance, and
// turned profile restore into an O(profiles^2) scan.
func loadProfileBackups(e *Executor) map[string]string {
	backups := make(map[string]string)
	for item := range serverAgnosticExtractItems(e, "getProfileBackups") {
		key := extractField(item.Data, "profileKey")
		if key == "" {
			continue
		}
		// The backup data is stored as XML in the "backup" field.
		if backup := extractField(item.Data, "backup"); backup != "" {
			backups[key] = backup
		}
	}
	return backups
}

func runRestoreProfiles(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	backups := loadProfileBackups(e)
	err := forEachMigrateItem(ctx, e, "restoreProfiles", "getProfileBackups",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			profileKey := extractField(item, "profileKey")
			if shouldSkipOrg(orgKey) || profileKey == "" {
				return nil
			}

			backup := backups[profileKey]
			if backup == "" {
				return nil
			}

			_, err := e.Cloud.QualityProfiles.Restore(ctx, orgKey, []byte(backup))
			if err != nil {
				failAPI(counter, e.Logger, "restoreProfiles failed", err, "profile", profileKey)
			} else {
				counter.Success()
			}
			return nil
		})
	return err
}

func runAddGateConditions(ctx context.Context, e *Executor) error {
	// Sidecar JSONL recording per-condition mapping notes — used by the
	// summary report to mark a quality gate as Partial when some of its
	// conditions were either remapped to a close SQC equivalent (#143) or
	// dropped because no SQC equivalent exists.
	notesW, _ := e.Store.Writer("addGateConditions.notes")
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItemTransformed(ctx, e, "addGateConditions", "getGateConditions",
		mergeGateRecordsByCloudGate,
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			gateIDStr := extractField(item, "cloud_gate_id")
			gateName := extractField(item, "gate_name")
			wasPreexisting := extractBool(item, "was_preexisting")
			if shouldSkipOrg(orgKey) || gateIDStr == "" {
				return nil
			}
			gateID, _ := strconv.Atoi(gateIDStr)

			// Override semantics: if the target gate already existed on SQC,
			// wipe its current conditions first so the migrated set is the
			// authoritative one — never a union of source + whatever was
			// already on the target.
			if wasPreexisting {
				clearTargetGateConditions(ctx, e, counter, gateName, orgKey)
			}

			// Extract conditions from the gate data.
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(item, &obj); err != nil {
				return nil
			}
			conditionsRaw, ok := obj["conditions"]
			if !ok {
				return nil
			}
			var conditions []map[string]any
			json.Unmarshal(conditionsRaw, &conditions)

			// First pass: expand every source condition into zero or more
			// target conditions, recording notes for drops / remaps. The
			// actual POSTs are deferred so the resolver (#234) can collapse
			// collisions across multiple source conditions before any HTTP
			// traffic.
			var pending []targetCondition
			for _, cond := range conditions {
				metric, _ := cond["metric"].(string)
				op, _ := cond["op"].(string)
				errorVal, _ := cond["error"].(string)
				if metric == "" || op == "" {
					continue
				}

				targets, mapped := LookupMetricReplacement(metric)
				if mapped && len(targets) == 0 {
					e.Logger.Warn("addGateConditions: source metric has no SonarQube Cloud equivalent — condition skipped (#143)",
						"gate", gateName, "metric", metric, "op", op, "error", errorVal)
					recordGateConditionNote(notesW, gateIDStr, gateName, gateConditionNoteInput{
						Action:       "dropped",
						SourceMetric: metric,
						SourceOp:     op,
						SourceError:  errorVal,
					})
					// A source metric with no Cloud counterpart is an
					// expected mapping limitation, not a fault.
					counter.FailWith(FailureByDesign)
					continue
				}
				if !mapped {
					targets = []ReplacementCondition{{Metric: metric}}
				}

				// Compute effective target conditions (each target inherits
				// the source's op/threshold unless the mapping table
				// overrides them, e.g. composite expansions).
				targetConds := make([]targetCondition, 0, len(targets))
				for _, repl := range targets {
					effOp := repl.Op
					if effOp == "" {
						effOp = op
					}
					effErr := repl.Error
					if effErr == "" {
						effErr = errorVal
					}
					targetConds = append(targetConds, targetCondition{
						Metric:       repl.Metric,
						Op:           effOp,
						Error:        effErr,
						SourceMetric: metric,
					})
				}

				if mapped {
					e.Logger.Info("addGateConditions: source metric remapped to SonarQube Cloud equivalent(s) (#143)",
						"gate", gateName, "source_metric", metric, "target_metrics", targets)
					targetMetrics := make([]string, 0, len(targetConds))
					for _, tc := range targetConds {
						targetMetrics = append(targetMetrics, tc.Metric)
					}
					// Suppress the sidecar note (and therefore the
					// report's "Near Perfect" Issues line + yellow
					// classification) for remaps that are obvious from
					// the metric names alone — e.g. software_quality_*
					// _rating → its same-axis SQC equivalent. Operators
					// don't need a callout for those.
					if !IsObviousMetricRemap(metric, targetMetrics) {
						recordGateConditionNote(notesW, gateIDStr, gateName, gateConditionNoteInput{
							Action:       "remapped",
							SourceMetric: metric,
							SourceOp:     op,
							SourceError:  errorVal,
							Targets:      targetConds,
						})
					}
				}

				pending = append(pending, targetConds...)
			}

			// Second pass: collapse collisions per #234, then POST.
			for _, tc := range resolveTargetConditions(pending) {
				e.Logger.Debug("gate api call: POST /api/qualitygates/create_condition",
					"gate_id", gateID, "metric", tc.Metric, "op", tc.Op, "error", tc.Error, "org", orgKey,
					"source_metric", tc.SourceMetric)
				_, err := e.Cloud.QualityGates.CreateCondition(ctx, cloud.CreateConditionParams{
					GateID: gateID, Organization: orgKey,
					Metric: tc.Metric, Op: tc.Op, Error: tc.Error,
				})
				switch {
				case err == nil:
					counter.Success()
				case sqapi.IsAlreadyExists(err), sqapi.IsMangledAlreadyExists(err):
					// The condition is already on the target gate, so the
					// desired end state holds — treat as success like every
					// other create-style task does. IsMangledAlreadyExists
					// covers the percent-named metrics whose message the
					// server destroys (see the SDK doc comment).
					e.Logger.Debug("addGateConditions: condition already present on target gate",
						"gate", gateName, "metric", tc.Metric, "org", orgKey)
					counter.Success()
				default:
					counter.FailAPI(err)
					// gate/op/error are on the line because the source
					// threshold is otherwise unrecoverable from the log.
					logAPIWarn(e.Logger, "addGateConditions failed", err,
						"gate", gateName, "metric", tc.Metric, "source_metric", tc.SourceMetric,
						"op", tc.Op, "threshold", tc.Error)
				}
			}
			return nil
		})
	return err
}

// gateConditionNoteInput is the per-condition decision payload written to
// the addGateConditions.notes sidecar JSONL. Carrying source op/threshold
// and per-target op/threshold lets the report render the full #143-style
// mapping (e.g. "software_quality_blocker_issues > 0 --> security_rating
// <= D") rather than just metric names.
type gateConditionNoteInput struct {
	Action       string            // "remapped" | "dropped"
	SourceMetric string            // source SQS metric
	SourceOp     string            // source condition op (GT, LT, ...)
	SourceError  string            // source threshold
	Targets      []targetCondition // target conditions; empty for "dropped"
}

// recordGateConditionNote appends a sidecar JSONL entry describing one
// per-condition mapping decision. The summary report reads this file to
// classify the parent gate (NearPerfect / Partial) and render Issues.
func recordGateConditionNote(w *common.ChunkWriter, cloudGateID, gateName string, n gateConditionNoteInput) {
	if w == nil || cloudGateID == "" {
		return
	}
	rec := map[string]any{
		"cloud_gate_id": cloudGateID,
		"gate_name":     gateName,
		"action":        n.Action,
		"source": map[string]string{
			"metric": n.SourceMetric,
			"op":     n.SourceOp,
			"error":  n.SourceError,
		},
	}
	if len(n.Targets) > 0 {
		out := make([]map[string]string, 0, len(n.Targets))
		for _, t := range n.Targets {
			out = append(out, map[string]string{
				"metric": t.Metric,
				"op":     t.Op,
				"error":  t.Error,
			})
		}
		rec["targets"] = out
	}
	b, _ := json.Marshal(rec)
	_ = w.WriteOne(b)
}

// clearTargetGateConditions removes every condition from a pre-existing target
// quality gate before the migrated source conditions are added. Failures here
// are logged at Warn level but do not abort the migration — the subsequent
// CreateCondition calls will surface a conflict if the cleanup did not take
// effect.
func clearTargetGateConditions(ctx context.Context, e *Executor, counter *TaskCounter, gateName, orgKey string) {
	if gateName == "" {
		return
	}
	e.Logger.Debug("gate api call: GET /api/qualitygates/show",
		"name", gateName, "org", orgKey)
	gate, err := e.Cloud.QualityGates.Show(ctx, gateName, orgKey)
	if err != nil {
		logAPIWarn(e.Logger, "addGateConditions: show gate failed during override cleanup", err,
			"gate", gateName)
		return
	}
	for _, cond := range gate.Conditions {
		if cond.ID == 0 {
			continue
		}
		e.Logger.Debug("gate api call: POST /api/qualitygates/delete_condition",
			"gate", gateName, "condition_id", cond.ID, "metric", cond.Metric, "org", orgKey)
		if err := e.Cloud.QualityGates.DeleteCondition(ctx, cond.ID, orgKey); err != nil {
			if sqapi.IsNotFound(err) {
				// Already gone — the goal of the clear is met. Happens when
				// the target gate was mutated between the getGateConditions
				// read and this delete.
				e.Logger.Debug("addGateConditions: condition already removed from target gate",
					"gate", gateName, "condition_id", cond.ID, "metric", cond.Metric)
				continue
			}
			counter.FailAPI(err)
			logAPIWarn(e.Logger, "addGateConditions: delete existing condition failed", err,
				"gate", gateName, "condition_id", cond.ID, "metric", cond.Metric)
		}
	}
	e.Logger.Info("addGateConditions: cleared pre-existing conditions on overridden gate",
		"gate", gateName, "count", len(gate.Conditions))
}

func runSetDefaultProfiles(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItemFiltered(ctx, e, "setDefaultProfiles", "createProfiles",
		func(item json.RawMessage) bool {
			return extractBool(item, "is_default")
		},
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			name := extractField(item, "name")
			lang := extractField(item, "language")

			err := e.Cloud.QualityProfiles.SetDefault(ctx, lang, name, orgKey)
			if err != nil {
				failAPI(counter, e.Logger, "setDefaultProfiles failed", err, "name", name)
			} else {
				counter.Success()
			}
			return nil
		})
	return err
}

func runSetDefaultGates(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItemFiltered(ctx, e, "setDefaultGates", "createGates",
		func(item json.RawMessage) bool {
			return extractBool(item, "is_default")
		},
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			gateIDStr := extractField(item, "cloud_gate_id")
			gateName := extractField(item, "name")
			gateID, _ := strconv.Atoi(gateIDStr)

			e.Logger.Debug("gate api call: POST /api/qualitygates/set_as_default",
				"name", gateName, "gate_id", gateIDStr, "org", orgKey)
			err := e.Cloud.QualityGates.SetDefault(ctx, gateID, orgKey)
			if err != nil {
				failAPI(counter, e.Logger, "setDefaultGates failed", err, "gate", gateIDStr)
			} else {
				counter.Success()
			}
			return nil
		})
	return err
}

func runSetDefaultTemplates(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItemFiltered(ctx, e, "setDefaultTemplates", "createPermissionTemplates",
		func(item json.RawMessage) bool {
			return extractBool(item, "is_default")
		},
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			templateID := extractField(item, "cloud_template_id")

			orgKey := extractField(item, "sonarcloud_org_key")
			err := e.Cloud.Permissions.SetDefaultTemplate(ctx, templateID, "TRK", orgKey)
			if err != nil {
				failAPI(counter, e.Logger, "setDefaultTemplates failed", err, "template", templateID)
			} else {
				counter.Success()
			}
			return nil
		})
	return err
}

// mergeGateRecordsByCloudGate folds every getGateConditions record that
// targets the same SonarQube Cloud gate into a single record whose
// conditions are the union of theirs.
//
// gates.csv is deduplicated on the SOURCE organization key
// (internal/structure/gates.go), but the CSV-to-JSONL load then joins
// source org to CLOUD org, and that join is many-to-one. N source orgs
// sharing a gate name therefore yield N createGates rows — and N
// getGateConditions rows — all carrying the same cloud_gate_id. Fanned
// out 25-wide, each ran "show -> delete every condition -> create every
// condition" against the same gate concurrently: one worker won every
// race and the others logged 404s on delete and "already exists" on
// create, with the surviving condition set decided by interleaving.
//
// Merging first makes the outcome deterministic and lets the existing
// per-metric resolver (#234) collapse collisions across source gates
// exactly as it already does within one, keeping the most stringent
// threshold per metric. was_preexisting is OR-ed so the clear still
// happens if any contributing record saw an existing target gate.
func mergeGateRecordsByCloudGate(items []json.RawMessage) []json.RawMessage {
	type merged struct {
		base        map[string]json.RawMessage
		conditions  []map[string]any
		preexisting bool
	}
	order := make([]string, 0, len(items))
	byGate := make(map[string]*merged, len(items))
	passthrough := make([]json.RawMessage, 0)

	for _, item := range items {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(item, &obj); err != nil {
			passthrough = append(passthrough, item)
			continue
		}
		gateID := extractField(item, "cloud_gate_id")
		org := extractField(item, "sonarcloud_org_key")
		if gateID == "" {
			// Nothing to key on; leave it for the task body to skip.
			passthrough = append(passthrough, item)
			continue
		}
		var conds []map[string]any
		if raw, ok := obj["conditions"]; ok {
			_ = json.Unmarshal(raw, &conds)
		}
		key := org + "\x00" + gateID
		m, seen := byGate[key]
		if !seen {
			order = append(order, key)
			byGate[key] = &merged{base: obj, conditions: conds, preexisting: extractBool(item, "was_preexisting")}
			continue
		}
		m.conditions = append(m.conditions, conds...)
		m.preexisting = m.preexisting || extractBool(item, "was_preexisting")
	}

	out := make([]json.RawMessage, 0, len(order)+len(passthrough))
	for _, key := range order {
		m := byGate[key]
		condRaw, err := json.Marshal(m.conditions)
		if err != nil {
			continue
		}
		m.base["conditions"] = condRaw
		preRaw, _ := json.Marshal(m.preexisting)
		m.base["was_preexisting"] = preRaw
		rec, err := json.Marshal(m.base)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return append(out, passthrough...)
}
