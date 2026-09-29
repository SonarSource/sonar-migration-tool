---
spec_id: SPEC-005
title: Measures & Metrics Migration
status: draft
priority: P0
epic: "Core Data Migration"
depends_on: [SPEC-001]
estimated_effort: L
cloudvoyager_ref: "src/pipelines/measures/..."
---

# SPEC-005: Measures & Metrics Migration
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

The Measures & Metrics Migration pipeline extracts component-level metrics from SonarQube Server and encodes them into the protobuf scanner report format for injection into SonarQube Cloud. Measures include quantitative data about code quality — lines of code, cyclomatic complexity, test coverage, duplication density, technical debt, violation counts, and more. These metrics drive SonarQube's dashboards, quality gates, and trend analysis.

A critical technical challenge in this pipeline is measure type intelligence: the protobuf `Measure` message has separate fields for integer, double, and string values, and the correct field must be used based on the metric's data type. Using the wrong field causes the Compute Engine to silently discard the measure or interpret it incorrectly. CloudVoyager's implementation includes a comprehensive metric type mapping table that this spec captures in full.

This pipeline also handles code duplication data, which is encoded separately as `duplications-{ref}.pb` files. Duplication blocks describe ranges of identical or near-identical code across files, enabling SonarQube Cloud's duplication analysis features.

## Problem Statement

SonarQube's dashboards, quality gates, and trend analysis depend entirely on measures data. Without migrating measures, SonarQube Cloud projects start with zero historical metrics — no coverage trends, no complexity baselines, no violation counts, no duplication tracking. This makes quality gates non-functional (no data to evaluate against), trend charts empty, and management reports useless until enough new analyses accumulate. For organizations that track metrics for compliance or engineering KPIs, this gap is unacceptable.

Additionally, code duplication data is critical for understanding code health. Duplication metrics are among the most commonly used quality gate conditions, and losing this data means quality gates that reference duplication metrics will have no baseline to compare against.

## User Stories

- **As a** migration operator, **I want to** migrate all code metrics from SonarQube Server, **so that** SonarQube Cloud dashboards show historical data from day one.
- **As a** quality engineer, **I want to** preserve coverage and complexity trends, **so that** engineering KPIs are not disrupted by the migration.
- **As a** migration operator, **I want to** correctly encode each metric type (integer, float, string), **so that** measures are processed accurately by the Compute Engine.
- **As a** quality engineer, **I want to** preserve duplication data, **so that** quality gates referencing duplication metrics function correctly.
- **As a** engineering manager, **I want to** see accurate trend data in SonarQube Cloud immediately after migration, **so that** there is no "dark period" in our metrics history.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Extract component-level measures via `/api/measures/component_tree` | Must |
| FR-2 | Extract metric definitions via `/api/metrics/search` for type mapping | Must |
| FR-3 | Encode measures as `measures-{ref}.pb` files (length-delimited, one per component) | Must |
| FR-4 | Use correct protobuf value field based on metric type: `intValue`, `doubleValue`, or `stringValue` | Must |
| FR-5 | Support all 18+ key metrics: ncloc, complexity, violations, coverage, duplicated_lines_density, etc. | Must |
| FR-6 | Batch metricKeys parameter: 15 per request for SQ 9.9/10.x, no batching for SQ 2025+ | Must |
| FR-7 | Extract duplication data and encode as `duplications-{ref}.pb` (length-delimited) | Must |
| FR-8 | Preserve duplication block structure: origin position, duplicate references (otherFileRef, range) | Must |
| FR-9 | Write extracted measures to JSONL intermediate files for resume capability | Should |
| FR-10 | Handle missing/null measure values gracefully (skip rather than encode zero) | Must |
| FR-11 | Support component-level and project-level measures | Must |
| FR-12 | Handle metric key changes between SonarQube versions | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Extraction throughput | >= 500 components/second with batched metric requests |
| NFR-2 | Memory usage | Stream processing per component batch |
| NFR-3 | Metric batching efficiency | Minimize API calls via 15-key batching |
| NFR-4 | Progress reporting | Log progress every 500 components |
| NFR-5 | Error resilience | Continue on individual component fetch failure, log and skip |

## Technical Design

### Architecture

The `go/internal/scanreport/` package already contains a working skeleton (builder.go, packager.go, submit.go, backdate.go) that should be extended, not replaced.

**Extract phase** (`go/internal/extract/tasks_measures.go`):
- `ExtractMetricDefinitions` task: fetch all metric types via `/api/metrics/search`
- `ExtractMeasures` task: paginate `/api/measures/component_tree` per project, batching metric keys
- `ExtractDuplications` task: extract duplication blocks per component
- Write to JSONL in `files/<extract_id>/measures/`

**Scanner phase** (`go/internal/scanreport/measures.go`):
- Transform measures into `pb.Measure` messages with correct value field selection
- Encode as length-delimited `measures-{ref}.pb` per component

**Scanner phase** (`go/internal/scanreport/duplications.go`):
- Transform duplication blocks into `pb.Duplication` messages
- Encode as length-delimited `duplications-{ref}.pb` per component

### Key Algorithms

#### Metric Type Classification

The protobuf `Measure` message uses a `oneof value` with nested types (IntValue, LongValue, DoubleValue, StringValue, BoolValue). The correct oneof field must be selected based on the metric's type:

```
// Comprehensive metric type mapping table
// NOTE: There is NO MeasureValueType enum — these string constants map to oneof fields.
var metricTypeMap = map[string]string{
    // Integer metrics (use IntValue oneof field)
    "ncloc":                    "INT",
    "lines":                    "INT",
    "complexity":               "INT",
    "cognitive_complexity":     "INT",
    "violations":               "INT",
    "bugs":                     "INT",
    "vulnerabilities":          "INT",
    "code_smells":              "INT",
    "security_hotspots":        "INT",
    "blocker_violations":       "INT",
    "critical_violations":      "INT",
    "major_violations":         "INT",
    "minor_violations":         "INT",
    "info_violations":          "INT",
    "open_issues":              "INT",
    "reopened_issues":          "INT",
    "confirmed_issues":        "INT",
    "false_positive_issues":   "INT",
    "wont_fix_issues":         "INT",
    "classes":                  "INT",
    "functions":                "INT",
    "statements":               "INT",
    "files":                    "INT",
    "directories":              "INT",
    "comment_lines":            "INT",
    "duplicated_lines":         "INT",
    "duplicated_blocks":        "INT",
    "duplicated_files":         "INT",
    "tests":                    "INT",
    "test_failures":            "INT",
    "test_errors":              "INT",
    "skipped_tests":            "INT",
    "new_violations":           "INT",
    "new_bugs":                 "INT",
    "new_vulnerabilities":      "INT",
    "new_code_smells":          "INT",
    "new_security_hotspots":    "INT",
    "new_lines":                "INT",
    "new_technical_debt":       "INT",
    "security_hotspots_reviewed": "INT",

    // Float/percentage metrics (use DoubleValue oneof field)
    "coverage":                        "DOUBLE",
    "line_coverage":                   "DOUBLE",
    "branch_coverage":                 "DOUBLE",
    "duplicated_lines_density":        "DOUBLE",
    "comment_lines_density":           "DOUBLE",
    "sqale_debt_ratio":                "DOUBLE",
    "new_sqale_debt_ratio":            "DOUBLE",
    "sqale_rating":                    "DOUBLE",
    "reliability_rating":              "DOUBLE",
    "security_rating":                 "DOUBLE",
    "security_review_rating":          "DOUBLE",
    "new_coverage":                    "DOUBLE",
    "new_line_coverage":               "DOUBLE",
    "new_branch_coverage":             "DOUBLE",
    "new_duplicated_lines_density":    "DOUBLE",
    "effort_to_reach_maintainability_rating_a": "DOUBLE",
    "test_success_density":            "DOUBLE",
    "test_execution_time":             "DOUBLE",
    "new_reliability_rating":          "DOUBLE",
    "new_security_rating":             "DOUBLE",
    "new_maintainability_rating":      "DOUBLE",
    "new_security_review_rating":      "DOUBLE",
    "security_hotspots_reviewed_status": "DOUBLE",
    "new_security_hotspots_reviewed_status": "DOUBLE",

    // Long metrics (use LongValue oneof field — stored as int64 in protobuf)
    "sqale_index":              "LONG",
    "new_sqale_index":          "LONG",
    "reliability_remediation_effort": "LONG",
    "new_reliability_remediation_effort": "LONG",
    "security_remediation_effort": "LONG",
    "new_security_remediation_effort": "LONG",

    // String metrics (use StringValue oneof field)
    "ncloc_data":               "STRING",
    "executable_lines_data":    "STRING",
    "ncloc_language_distribution": "STRING",
    "alert_status":             "STRING",
    "quality_gate_details":     "STRING",
    "quality_profiles":         "STRING",
}
```

#### Measure-to-Protobuf Transformation

```
// Uses the oneof pattern matching buildMeasureValue() in builder.go.
// There is NO MeasureValueType enum — the correct type is set via the oneof field.
func transformMeasure(measure types.Measure, metricType string) *pb.Measure:
    if measure.Value == "":
        return nil  // Skip null/empty measures

    pbMeasure = &pb.Measure{
        MetricKey: measure.Metric,
    }

    switch metricType:
        case "INT":
            val, err = strconv.Atoi(measure.Value)
            if err != nil:
                log.Warn("invalid int measure value", "metric", measure.Metric, "value", measure.Value)
                return nil
            pbMeasure.Value = &pb.Measure_IntValue{IntValue: &pb.IntValue{Value: int32(val)}}

        case "LONG":
            val, err = strconv.ParseInt(measure.Value, 10, 64)
            if err != nil:
                log.Warn("invalid long measure value", "metric", measure.Metric, "value", measure.Value)
                return nil
            pbMeasure.Value = &pb.Measure_LongValue{LongValue: &pb.LongValue{Value: val}}

        case "DOUBLE":
            val, err = strconv.ParseFloat(measure.Value, 64)
            if err != nil:
                log.Warn("invalid float measure value", "metric", measure.Metric, "value", measure.Value)
                return nil
            pbMeasure.Value = &pb.Measure_DoubleValue{DoubleValue: &pb.DoubleValue{Value: val}}

        case "STRING":
            pbMeasure.Value = &pb.Measure_StringValue{StringValue: &pb.StringValue{Value: measure.Value}}

        case "BOOLEAN":
            boolVal = measure.Value == "true"
            pbMeasure.Value = &pb.Measure_BooleanValue{BooleanValue: &pb.BoolValue{Value: boolVal}}

        default:
            // Unknown metric type: try to auto-detect
            pbMeasure = autoDetectMeasureType(measure)

    return pbMeasure
```

#### Auto-Detection for Unknown Metrics

For metrics not in the hardcoded type map (custom metrics, new metrics added in future SQ versions):

```
// Uses the oneof pattern — there is NO MeasureValueType enum.
func autoDetectMeasureType(measure types.Measure) *pb.Measure:
    value = measure.Value
    pbMeasure = &pb.Measure{MetricKey: measure.Metric}

    // Try integer first
    if intVal, err = strconv.Atoi(value); err == nil:
        pbMeasure.Value = &pb.Measure_IntValue{IntValue: &pb.IntValue{Value: int32(intVal)}}
        return pbMeasure

    // Try float
    if floatVal, err = strconv.ParseFloat(value, 64); err == nil:
        pbMeasure.Value = &pb.Measure_DoubleValue{DoubleValue: &pb.DoubleValue{Value: floatVal}}
        return pbMeasure

    // Fall back to string
    pbMeasure.Value = &pb.Measure_StringValue{StringValue: &pb.StringValue{Value: value}}
    return pbMeasure
```

#### MetricKey Batching

SonarQube 9.9 and 10.x have a URL length limit that restricts the number of metric keys per request:

```
func extractMeasuresWithBatching(
    ctx context.Context,
    client *server.Client,
    projectKey string,
    metricKeys []string,
    serverVersion semver.Version,
) ([]types.Measure, error):

    batchSize = 15  // SQ 9.9/10.x safe limit
    if serverVersion >= semver.MustParse("2025.1.0"):
        batchSize = len(metricKeys)  // No batching needed

    allMeasures = []types.Measure{}
    for i = 0; i < len(metricKeys); i += batchSize:
        end = min(i+batchSize, len(metricKeys))
        batch = metricKeys[i:end]

        measures, err = client.Measures().ComponentTree(ctx, projectKey, batch)
        if err != nil:
            return nil, fmt.Errorf("measure batch %d: %w", i/batchSize, err)

        allMeasures = append(allMeasures, measures...)

    return allMeasures, nil
```

#### Duplication Block Extraction and Encoding

```
func extractDuplications(
    ctx context.Context,
    client *server.Client,
    componentKey string,
) ([]DuplicationBlock, error):
    // Extract via /api/duplications/show
    resp, err = client.Duplications().Show(ctx, componentKey)
    if err != nil:
        return nil, err
    return resp.Duplications, nil

func transformDuplication(
    block DuplicationBlock,
    componentRefMap map[string]int32,
) *pb.Duplication:
    pbDup = &pb.Duplication{
        OriginPosition: &pb.TextRange{
            StartLine: int32(block.Origin.StartLine),
            EndLine:   int32(block.Origin.EndLine),
        },
    }

    for _, dup in block.Duplicates:
        otherRef = componentRefMap[dup.Component]
        pbDup.Duplicate = append(pbDup.Duplicate, &pb.Duplicate{
            OtherFileRef: otherRef,
            Range: &pb.TextRange{
                StartLine: int32(dup.StartLine),
                EndLine:   int32(dup.EndLine),
            },
        })

    return pbDup
```

### Data Flow

1. **Fetch Metric Definitions** — Call `/api/metrics/search` to get all available metrics and their types. Build the metric type mapping table.
2. **Extract Measures per Project** — Paginate `/api/measures/component_tree` with batched metric keys (15 per request for SQ 9.9/10.x).
3. **Write Intermediates** — Write extracted measures to JSONL files per project.
4. **Group by Component** — Partition measures by component key. Resolve each component to its protobuf reference number.
5. **Transform to Protobuf** — Convert each measure to a `pb.Measure` message, selecting the correct value field based on metric type.
6. **Extract Duplications** — For each component, fetch duplication blocks and transform to `pb.Duplication` messages.
7. **Encode Measures** — For each component, collect all measures and encode as length-delimited `measures-{ref}.pb`.
8. **Encode Duplications** — For each component, collect all duplication blocks and encode as length-delimited `duplications-{ref}.pb`.
9. **Add to Report** — Pass encoded files to the Report Builder (SPEC-001).

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/measures/component_tree` | GET | Fetch measures for all components in a project. Params: `component` (project key), `metricKeys` (comma-separated, max 15), `qualifiers=FIL`, `p`, `ps` |
| `/api/measures/component` | GET | Fetch measures for a single component. Params: `component`, `metricKeys` |
| `/api/metrics/search` | GET | Fetch all metric definitions including type. Params: `p`, `ps` |
| `/api/duplications/show` | GET | Fetch duplication blocks for a component. Params: `key` (component key) |

### Protobuf Schema

#### ScannerReport.Measure (length-delimited, per component)
```protobuf
message Measure {
    string metric_key = 1;
    oneof value {
        BoolValue boolean_value = 2;
        IntValue int_value = 3;
        LongValue long_value = 4;
        DoubleValue double_value = 5;
        StringValue string_value = 6;
    }
}

// Each nested type has value (field 1) and data (field 2) fields.
message BoolValue { bool value = 1; string data = 2; }
message IntValue { int32 value = 1; string data = 2; }
message LongValue { int64 value = 1; string data = 2; }
message DoubleValue { double value = 1; string data = 2; }
message StringValue { string value = 1; }
```

**IMPORTANT:** There is NO `MeasureValueType` enum. The actual proto uses `oneof` with nested value types. The correct value type is selected by setting the appropriate oneof field. See `buildMeasureValue()` in `builder.go` for the actual implementation.

#### ScannerReport.Duplication (length-delimited, per component)

**IMPORTANT:** In the actual proto, `Duplicate` is a top-level message, not nested inside `Duplication`.

```protobuf
message Duplication {
    TextRange origin_position = 1;
    repeated Duplicate duplicate = 2;
}

// Duplicate is a standalone top-level message, NOT nested inside Duplication.
message Duplicate {
    int32 other_file_ref = 1;  // component ref of the duplicate file
    TextRange range = 2;       // line range in the duplicate file
}
```

### Extended Measure Types

The existing `types.Measure` struct in `lib/sq-api-go/types/measures.go` needs extension for the component tree response:

```go
// MeasureComponent represents a component with its measures from
// /api/measures/component_tree.
type MeasureComponent struct {
    Key       string    `json:"key"`
    Name      string    `json:"name"`
    Qualifier string    `json:"qualifier"`
    Path      string    `json:"path"`
    Language  string    `json:"language"`
    Measures  []Measure `json:"measures"`
}

// MeasuresComponentTreeResponse is the paged response for
// /api/measures/component_tree.
type MeasuresComponentTreeResponse struct {
    PagedResponse
    BaseComponent MeasureComponent   `json:"baseComponent"`
    Components    []MeasureComponent `json:"components"`
}

// MetricDefinition represents a metric type definition from
// /api/metrics/search.
type MetricDefinition struct {
    Key         string `json:"key"`
    Name        string `json:"name"`
    Description string `json:"description"`
    Domain      string `json:"domain"`
    Type        string `json:"type"`  // INT, FLOAT, PERCENT, MILLISEC, DATA, STRING, RATING, WORK_DUR, BOOL, DISTRIB, LEVEL
    Direction   int    `json:"direction"`
    Qualitative bool   `json:"qualitative"`
    Hidden      bool   `json:"hidden"`
}

// MetricsSearchResponse is the paged response for /api/metrics/search.
type MetricsSearchResponse struct {
    PagedResponse
    Metrics []MetricDefinition `json:"metrics"`
}

// DuplicationBlock represents a group of duplicated code blocks.
type DuplicationBlock struct {
    Origin     DuplicationRange    `json:"origin"`
    Duplicates []DuplicationTarget `json:"duplicates"`
}

type DuplicationRange struct {
    StartLine int `json:"startLine"`
    EndLine   int `json:"endLine"`
}

type DuplicationTarget struct {
    Component string           `json:"component"`
    StartLine int              `json:"startLine"`
    EndLine   int              `json:"endLine"`
}
```

### Metric Type to Protobuf Value Field Mapping

This table maps SonarQube's metric `type` field (from `/api/metrics/search`) to the protobuf value field:

| SonarQube Metric Type | Protobuf ValueType | Protobuf Field |
|----------------------|-------------------|----------------|
| INT | INT | `int_value` (int32) |
| FLOAT | DOUBLE | `double_value` (double) |
| PERCENT | DOUBLE | `double_value` (double) |
| MILLISEC | LONG | `long_value` (int64) |
| RATING | DOUBLE | `double_value` (double, 1.0-5.0 scale) |
| WORK_DUR | LONG | `long_value` (int64, minutes) |
| BOOL | BOOLEAN | `boolean_value` (bool) |
| STRING | STRING | `string_value` (string) |
| DATA | STRING | `string_value` (string, serialized data) |
| DISTRIB | STRING | `string_value` (string, distribution format) |
| LEVEL | STRING | `string_value` (string, e.g., "OK", "ERROR") |

## Acceptance Criteria

- [ ] AC-1: All component-level measures are extracted from SonarQube Server for each project.
- [ ] AC-2: Measures are encoded into length-delimited `measures-{ref}.pb` files with the correct protobuf value field for each metric type.
- [ ] AC-3: Integer metrics use `int_value`, float/percentage metrics use `double_value`, string metrics use `string_value`, long metrics use `long_value`.
- [ ] AC-4: Unknown metrics are auto-detected and encoded with a best-effort type selection.
- [ ] AC-5: Metric key batching respects the 15-key limit for SQ 9.9/10.x and removes batching for SQ 2025+.
- [ ] AC-6: Duplication blocks are extracted and encoded as `duplications-{ref}.pb` with correct origin and duplicate references.
- [ ] AC-7: Duplication cross-file references correctly use component reference numbers from the flat component structure.
- [ ] AC-8: Null/empty measure values are skipped rather than encoded as zero.
- [ ] AC-9: Progress is logged at regular intervals during extraction.
- [ ] AC-10: Extracted measures are written to JSONL for resume capability.
- [ ] AC-11: Unit tests cover all metric type mappings including edge cases (missing metrics, invalid values).
- [ ] AC-12: Integration test verifies: extract measures from mock server, encode to protobuf, decode and verify value types match.
- [ ] AC-13: Metric definitions from `/api/metrics/search` are used to dynamically supplement the hardcoded type map.

## CloudVoyager Reference

| Area | Path |
|------|------|
| Measure extraction | `src/pipelines/measures/extractor.js` |
| Metric type mapping | `src/pipelines/measures/type-mapper.js` |
| Measure protobuf encoding | `src/pipelines/scanner-report/encoders/measures.js` |
| Duplication extraction | `src/pipelines/measures/duplications-extractor.js` |
| Duplication encoding | `src/pipelines/scanner-report/encoders/duplications.js` |
| MetricKey batching | `src/pipelines/measures/batcher.js` |

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt

## Known Limitations

- The `/api/measures/component_tree` endpoint returns current values only, not historical time series. Trend data in SonarQube Cloud will show a single data point at the migration date, not a reconstructed historical trend line.
- Some metrics are computed by SonarQube Cloud's CE during processing (e.g., `sqale_rating` derived from `sqale_index`). Encoding these computed metrics may cause double-counting or conflicts. The safe approach is to encode only "raw" metrics and let CE compute derived ones.
- The duplication detection in SonarQube Cloud may differ slightly from SonarQube Server, especially for near-duplicates. Injected duplication data represents the server's view, which may not match Cloud's re-analysis.
- The `ncloc_data` and `executable_lines_data` string metrics contain large serialized data structures (per-line code/executable status). These are needed for accurate coverage computation but significantly increase report size.
- Custom metrics defined via SonarQube Server plugins may not exist in SonarQube Cloud. These metrics will be extracted and encoded, but CE may silently discard unknown metric keys.
- The 15-key batch limit for SQ 9.9/10.x is based on URL length constraints. Metric keys with very long names may require smaller batches.

## Open Questions

- Should we encode derived/computed metrics (ratings, ratios) or only raw metrics and let CE recompute them? Encoding derived metrics may cause inconsistencies if CE uses different calculation logic.
- What is the complete list of metrics that CE recomputes? We should exclude those from encoding.
- How should we handle custom plugin metrics that exist on Server but not on Cloud? Log a warning and skip, or encode and hope CE accepts them?
- Should duplication extraction be concurrent (per-component) or sequential to avoid overloading the server?
- Is the `/api/duplications/show` endpoint the right source, or should we use `/api/measures/component` with `duplicated_lines_density` and reconstruct blocks differently?
- How do we handle the transition from SQ 9.9's metric key names to the renamed keys in SQ 10.x (if any)?
