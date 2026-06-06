# PLAN-FIX-106: Migrate Project Measures

> **Issue:** [#106](https://github.com/sonar-solutions/sonar-migration-tool/issues/106) — `sonar-migration-tool` must migrate project measures, for all measures that exist on the target platform

---

## Problem

Scanner reports submitted to SonarQube Cloud contain **no measures data**. In `importBranch()` ([tasks_projectdata.go:224](go/internal/migrate/tasks_projectdata.go#L224)), the `Measures` field is always set to `make(map[int32][]*pb.Measure)` — an empty map. As a result, SonarQube Cloud has no file-level metric data from which to compute project-level aggregates (violations, complexity, ratings, etc.).

The full protobuf infrastructure is already built and tested — it just isn't wired up.

---

## Current State

### What Already Works

| Layer | File | Status |
|-------|------|--------|
| Protobuf schema | [scanner-report.proto:135-168](go/internal/scanreport/proto/scanner-report.proto#L135-L168) | `Measure` message with typed `oneof` value (bool/int/long/double/string) |
| Builder | [builder.go:274-295](go/internal/scanreport/builder.go#L274-L295) | `MeasureInput` struct, `BuildMeasures()` groups by component ref |
| Type coercion | [builder.go:376-403](go/internal/scanreport/builder.go#L376-L403) | `buildMeasureValue()` — bool/int/float/string detection |
| ZIP packager | [packager.go:115-128](go/internal/scanreport/packager.go#L115-L128) | `addMeasures()` writes `measures-{ref}.pb` files, length-delimited |
| ReportData struct | [packager.go:14-25](go/internal/scanreport/packager.go#L14-L25) | `Measures map[int32][]*pb.Measure` field exists |
| Extract (project-level) | [tasks_projects.go:158-171](go/internal/extract/tasks_projects.go#L158-L171) | `projectMeasuresTask()` — fetches project-level measures (used for reporting only) |
| Extract (component tree) | [extract/tasks_projectdata.go:192-225](go/internal/extract/tasks_projectdata.go#L192-L225) | `projectComponentTreeTask()` — fetches per-file components, but only requests `ncloc` |
| Builder tests | [builder_test.go:135-172](go/internal/scanreport/builder_test.go#L135-L172) | Tests for `BuildMeasures` covering int/double/string/bool |

### The Gap

```
Extract Phase                          Migrate Phase
─────────────                          ─────────────
projectComponentTreeTask()             importBranch()
  └─ metricKeys: {"ncloc"}  ←─ ONLY     ├─ loadExtractedIssues()        ✓
                                         ├─ loadExtractedComponents()    ✓  (uses ncloc for line count)
                                         ├─ loadExtractedSources()      ✓
                                         ├─ loadExtractedActiveRules()   ✓
                                         ├─ loadExtractedMeasures()      ✗  MISSING
                                         ├─ BuildMeasures()              ✗  NEVER CALLED
                                         └─ ReportData.Measures          ✗  ALWAYS EMPTY
```

### CloudVoyager Reference

CloudVoyager implements the complete pipeline in JavaScript:
- Extracts 23+ metric keys per file component via `/api/measures/component_tree`
- Filters to `FIL` qualifier components only
- Encodes with a `STRING_METRICS` allowlist (always StringValue for `alert_status`, `ncloc_data`, etc.)
- Adds int32/int64 range splitting (IntValue vs LongValue)
- Verifies source vs target measures with 1% tolerance for `ncloc`/`lines`

Key files: `src/pipelines/sq-2025/protobuf/build-measures/`, `src/shared/verification/checkers/measures/`

---

## Metric Key Selection

Per issue #106: skip coverage metrics, skip new-code metrics, skip metrics absent on target.

### Include (file-level metrics that exist on SonarQube Cloud)

```
ncloc, lines, statements, functions, classes,
complexity, cognitive_complexity,
comment_lines, comment_lines_density, ncloc_language_distribution,
violations, blocker_violations, critical_violations, major_violations, minor_violations, info_violations,
code_smells, bugs, vulnerabilities, security_hotspots,
accepted_issues (or wont_fix_issues for SQ < 10.2), false_positive_issues,
reliability_rating, security_rating, sqale_rating, security_review_rating,
sqale_index, sqale_debt_ratio,
reliability_remediation_effort, security_remediation_effort,
alert_status, quality_gate_details,
duplicated_lines, duplicated_lines_density, duplicated_blocks, duplicated_files,
executable_lines_data, ncloc_data
```

### Exclude — Coverage (recreated on first analysis)

```
coverage, new_coverage, line_coverage, branch_coverage,
lines_to_cover, new_lines_to_cover, uncovered_lines, new_uncovered_lines,
conditions_to_cover, uncovered_conditions, covered_conditions_by_line, conditions_by_line
```

### Exclude — New-code metrics

```
new_violations, new_blocker_violations, new_critical_violations,
new_major_violations, new_minor_violations, new_info_violations,
new_code_smells, new_bugs, new_vulnerabilities, new_security_hotspots, new_lines, ...
```

### String Metrics (always encode as StringValue, even if value looks numeric)

Matches CloudVoyager's `STRING_METRICS` constant:

```
alert_status, quality_gate_details, executable_lines_data, ncloc_data, ncloc_language_distribution
```

---

## Implementation Steps

### Step 1: Expand extract metric keys

**File:** [go/internal/extract/tasks_projectdata.go](go/internal/extract/tasks_projectdata.go)

Add a `componentMeasureMetricKeysFor(version common.Version) string` function (similar to the existing `measureMetricKeysFor()` in [tasks_projects.go:16-29](go/internal/extract/tasks_projects.go#L16-L29)) that returns the full metric key list above. Uses the same `acceptedIssuesRename` version boundary for `accepted_issues` vs `wont_fix_issues`.

Change `projectComponentTreeTask()` line 201 from:
```go
"metricKeys": {"ncloc"},
```
to:
```go
"metricKeys": {componentMeasureMetricKeysFor(e.Version)},
```

The SonarQube API silently ignores unknown metric keys, so older versions just return fewer metrics.

---

### Step 2: Fix `buildMeasureValue()` for LongValue and string metrics

**File:** [go/internal/scanreport/builder.go](go/internal/scanreport/builder.go)

**2a.** Add `isStringMetric()` — a lookup map for metrics that must always be encoded as `StringValue`:

```go
var stringMetrics = map[string]bool{
    "alert_status": true, "quality_gate_details": true,
    "executable_lines_data": true, "ncloc_data": true,
    "ncloc_language_distribution": true,
}

func isStringMetric(key string) bool { return stringMetrics[key] }
```

**2b.** Fix the int32 overflow bug in `buildMeasureValue()` (line 384: `ParseInt(value, 10, 32)` silently truncates values that pass `isInt()` which uses bitSize 64). Add string-metric check and int32/int64 range splitting:

```go
func buildMeasureValue(metricKey, value string) *pb.Measure {
    m := &pb.Measure{MetricKey: metricKey}
    if isStringMetric(metricKey) {
        m.Value = &pb.Measure_StringValue_{...}
        return m
    }
    switch {
    case value == "true" || value == "false":
        // BoolValue (unchanged)
    case isInt(value):
        v, _ := strconv.ParseInt(value, 10, 64)
        if v >= math.MinInt32 && v <= math.MaxInt32 {
            // IntValue (int32)
        } else {
            // LongValue (int64) — NEW
        }
    case isFloat(value):
        // DoubleValue (unchanged)
    default:
        // StringValue (unchanged)
    }
    return m
}
```

---

### Step 3: Add measures loader in migrate phase

**File:** [go/internal/migrate/tasks_projectdata.go](go/internal/migrate/tasks_projectdata.go)

**3a.** Add `measureKeyValue` struct and `extractAllMeasures()` helper (mirrors the existing `extractMeasureInt32()` at [line 874](go/internal/migrate/tasks_projectdata.go#L874) but returns ALL key-value pairs):

```go
type measureKeyValue struct {
    Metric string `json:"metric"`
    Value  string `json:"value"`
}

func extractAllMeasures(data json.RawMessage) []measureKeyValue { ... }
```

**3b.** Add `loadExtractedComponentMeasures()` — follows the exact pattern of `loadExtractedComponents()` at [line 542](go/internal/migrate/tasks_projectdata.go#L542):

```go
func loadExtractedComponentMeasures(e *Executor, serverURL, serverKey, branch string) []scanreport.MeasureInput {
    items, err := readExtractItems(e, "getProjectComponentTree")  // same source as components
    // filter by serverURL, projectKey, branch
    // for each component, call extractAllMeasures() and build MeasureInput structs
}
```

This reads from the **same** extract store as `loadExtractedComponents()` — no new extract task needed. The component tree data already contains the measures array (once Step 1 expands the requested metric keys).

---

### Step 4: Wire measures into `importBranch()`

**File:** [go/internal/migrate/tasks_projectdata.go](go/internal/migrate/tasks_projectdata.go)

Four surgical changes in `importBranch()`:

**4a.** After line 142 (data loading block), add:
```go
componentMeasures := loadExtractedComponentMeasures(e, input.ServerURL, input.ServerKey, input.Branch)
```

**4b.** After line 181 (protobuf build block), add:
```go
pbMeasures := scanreport.BuildMeasures(componentMeasures, cr)
```

**4c.** Replace line 224:
```go
// BEFORE:
Measures: make(map[int32][]*pb.Measure),
// AFTER:
Measures: pbMeasures,
```

**4d.** Add to log line at ~237-246:
```go
"measures", len(componentMeasures),
```

---

### Step 5: Tests

**5a. `builder_test.go`** — Add test cases for:
- `LongValue` (integer outside int32 range, e.g. `"3000000000"`)
- `isStringMetric()` (e.g. `ncloc_data` with value `"123"` → still StringValue)

**5b. `tasks_projectdata_test.go`** (migrate) — Add:
- `TestExtractAllMeasures()` — JSON parsing of measures array
- `TestLoadExtractedComponentMeasures()` — filtering by server/project/branch, conversion to `MeasureInput`
- Update `setupProjectDataExtract()` fixture to include measures in component tree records

**5c. `tasks_projectdata_test.go`** (extract) — Update:
- `TestProjectComponentTreeTask()` — verify expanded metric keys in mock HTTP request

**5d. `tasks_projects_test.go`** or new test file — Add:
- `TestComponentMeasureMetricKeysFor()` — version boundary for `accepted_issues` vs `wont_fix_issues`, absence of coverage/new-code metrics

---

## File Change Summary

| File | Change |
|------|--------|
| `go/internal/extract/tasks_projectdata.go` | Add `componentMeasureMetricKeysFor()`, expand `projectComponentTreeTask()` metric keys |
| `go/internal/scanreport/builder.go` | Add `isStringMetric()`, fix `buildMeasureValue()` for LongValue + string metrics |
| `go/internal/migrate/tasks_projectdata.go` | Add `measureKeyValue`, `extractAllMeasures()`, `loadExtractedComponentMeasures()`, wire into `importBranch()` |
| `go/internal/scanreport/builder_test.go` | Add LongValue and string metric test cases |
| `go/internal/migrate/tasks_projectdata_test.go` | Add measure loader tests, update fixtures |
| `go/internal/extract/tasks_projectdata_test.go` | Update component tree task test for expanded metric keys |

---

## Implementation Order

Steps 1, 2, and 3 are independent — can be implemented in parallel.
Step 4 depends on all three.
Step 5 (tests) can be written alongside each step.

```
Step 1 (extract metrics) ──┐
Step 2 (builder fixes)  ───┼──► Step 4 (wire into importBranch)
Step 3 (migrate loader) ───┘
```

---

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| SQ API rejects too many `metricKeys` in one request (15-key limit on some versions) | Extract fails for older SQ | SQ API silently ignores unknown keys in practice. If failures occur, add metric key batching (split into chunks of 15, merge per-component). Not needed initially. |
| `ncloc_data` and `executable_lines_data` can be very large (per-line data) | Scanner report ZIP size increases | Already Deflate-compressed. SonarCloud handles these in normal analysis. Monitor ZIP sizes in testing. |
| Older extracts (before this change) lack expanded measures | `loadExtractedComponentMeasures()` returns empty | Graceful degradation — scanner report has empty measures (same as current behavior). Users re-extract to get measures. |
| int32 overflow in current `buildMeasureValue()` | Silent data corruption for large integer metrics | Step 2 fixes this by adding int32 range check + LongValue path |
| Some metrics don't exist on all SQ versions | Missing metrics in extracted data | SQ API silently omits unknown metric keys from response. No error handling needed. |

---

## Backward Compatibility

- **No config changes** — measures are automatically included as part of the project-data extract (default; opt-out via `--skip_project_data_migration`)
- **No new CLI flags** — the metric key set is determined by the source SQ version
- **Graceful with old extracts** — if component tree data lacks measures (extracted before this change), the measures map stays empty (identical to current behavior)
- **No new extract tasks** — reuses existing `getProjectComponentTree` task, just requests more data

---

## Verification

1. Run `go test ./internal/scanreport/...` — builder tests pass including new LongValue/string metric cases
2. Run `go test ./internal/migrate/...` — project data tests pass including new measure loader tests
3. Run `go test ./internal/extract/...` — extract tests pass with expanded metric keys
4. End-to-end: Run a full extract + migrate against a test SQ instance, verify the scanner report ZIP contains `measures-*.pb` files
5. Verify on SonarQube Cloud: after migration, project dashboard shows correct metric values matching the source SQ instance
