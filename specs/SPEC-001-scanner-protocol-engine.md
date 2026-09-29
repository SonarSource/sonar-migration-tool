---
spec_id: SPEC-001
title: Scanner Protocol Engine (Protobuf Report Generation)
status: draft
priority: P0
epic: "Core Data Migration"
depends_on: []
estimated_effort: XL
cloudvoyager_ref: "src/pipelines/scanner-report/..."
---

# SPEC-001: Scanner Protocol Engine (Protobuf Report Generation)
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

The Scanner Protocol Engine is the foundational subsystem that enables historical data injection into SonarQube Cloud without requiring a real SonarScanner analysis. SonarQube Cloud accepts analysis results exclusively through a proprietary binary protobuf ZIP archive format — the same format that SonarScanner produces after scanning source code. By reconstructing this format programmatically, the migration tool can inject issues, measures, source code, SCM blame data, and other artifacts directly into SonarQube Cloud's Compute Engine (CE) processing pipeline.

CloudVoyager reverse-engineered the SonarScanner report format by inspecting the binary payloads submitted during real scanner analyses. The format consists of a ZIP archive containing multiple `.pb` (protobuf) and `.txt` files, each representing a different aspect of the analysis. This spec captures the complete knowledge needed to reconstruct these archives in Go, using compiled protobuf definitions and the `google.golang.org/protobuf` library that is already a dependency of the project.

This is the crown jewel of the migration tool's data migration capabilities. Every other data migration spec (issues, hotspots, source code, measures) depends on this engine to serialize and upload their data. Without it, no historical data can be transferred to SonarQube Cloud.

## Problem Statement

SonarQube Cloud has no public API for importing historical issues, measures, or source code. The only ingestion path is through the Compute Engine, which processes analysis reports submitted by SonarScanner. Currently, the sonar-migration-tool can migrate configurations (projects, quality gates, profiles, groups, permissions, portfolios) but cannot migrate any historical data. This means organizations migrating from SonarQube Server to SonarQube Cloud lose all their historical issue tracking, trend data, and code quality metrics — often years of accumulated data that is critical for compliance, auditing, and engineering decision-making.

## User Stories

- **As a** migration operator, **I want to** generate synthetic SonarScanner report archives from extracted SonarQube Server data, **so that** historical issues and metrics appear in SonarQube Cloud as if they were produced by real scans.
- **As a** migration operator, **I want to** upload generated report archives to SonarQube Cloud's CE endpoint, **so that** the data is processed and indexed by the platform.
- **As a** migration operator, **I want to** track upload status and retry failed submissions, **so that** large migrations complete reliably even under transient network failures.
- **As a** migration operator, **I want to** prevent duplicate report submissions, **so that** re-running the migration does not create duplicate data in SonarQube Cloud.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Generate valid ZIP archives matching SonarScanner's binary report format | Must |
| FR-2 | Support all required file types within the ZIP: `metadata.pb`, `activerules.pb`, `adhocrules.pb`, `context-props.pb`, `component-{ref}.pb`, `issues-{ref}.pb`, `external-issues-{ref}.pb`, `measures-{ref}.pb`, `duplications-{ref}.pb`, `changesets-{ref}.pb`, `source-{ref}.txt` | Must |
| FR-3 | Implement single-message protobuf encoding for metadata, components, and changesets | Must |
| FR-4 | Implement length-delimited protobuf encoding for issues, measures, rules, and duplications | Must |
| FR-5 | Upload generated archives via `POST /api/ce/submit` with multipart form data | Must |
| FR-6 | Poll CE task status via `GET /api/ce/task?id={taskId}` after upload to confirm processing | Must |
| FR-7 | Implement retry mechanism: 5 retries at 3-second intervals, then re-submit | Must |
| FR-8 | Deduplicate submissions using `scm_revision_id` to prevent duplicate analyses | Must |
| FR-9 | Filter active rules to only languages in use to reduce report size (~84% reduction) | Should |
| FR-10 | Support both SonarQube Server 9.9 LTA and 10.x+ protobuf schema differences | Must |
| FR-11 | Generate random `scm_revision_id` values via `randomHex(20)` for each synthetic analysis | Must |
| FR-12 | Include empty sentinel files (e.g., `context-props.pb`) where required by the format | Must |
| FR-13 | Use flat component structure where all files are direct children of the project root | Must |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Report generation throughput | >= 100 components/second |
| NFR-2 | Maximum report archive size | Handle up to 500 MB archives |
| NFR-3 | Memory usage during generation | Stream to disk, not buffer entirely in memory |
| NFR-4 | Upload timeout | Configurable, default 5 minutes per archive |
| NFR-5 | Concurrent uploads | Configurable, default 1 (sequential to avoid CE queue saturation) |
| NFR-6 | Proto compilation | Build-time via `protoc` + `protoc-gen-go`, not runtime |

## Technical Design

> **Implementation Note:** The protobuf definitions below are copied from the ACTUAL `scanner-report.proto` and `constants.proto` already in the codebase at `go/internal/scanreport/proto/`. Always use the proto files as the source of truth — they are compiled at build time via `protoc`.

### Architecture

The Scanner Protocol Engine introduces a new package at `go/internal/scanreport/` with the following structure:

```
go/internal/scanreport/
├── proto/                    # Compiled protobuf definitions
│   ├── scanner_report.proto  # Main proto schema (from SonarScanner source)
│   └── *.pb.go              # Generated Go code
├── report.go                # ReportBuilder — orchestrates archive construction
├── encoder.go               # Protobuf encoding (single-message + length-delimited)
├── archive.go               # ZIP archive assembly and streaming
├── uploader.go              # HTTP upload to CE endpoint + status polling
├── dedup.go                 # Duplicate detection via scm_revision_id
└── scanner_test.go          # Unit tests with golden file comparisons
```

The engine integrates into the existing task system. A new set of migrate tasks in `go/internal/migrate/tasks_scanner.go` will orchestrate the pipeline: build report -> write archive -> upload -> poll status.

### Key Algorithms

#### Length-Delimited Encoding

For files containing multiple protobuf messages (issues, rules, measures, duplications), each message is prefixed with its byte length as a varint:

```
[varint: message_1_size][message_1_bytes][varint: message_2_size][message_2_bytes]...
```

Pseudocode:
```
func encodeLengthDelimited(messages []proto.Message) []byte:
    buf = new bytes.Buffer
    for each msg in messages:
        data = proto.Marshal(msg)
        writeVarint(buf, len(data))
        buf.Write(data)
    return buf.Bytes()
```

#### Single-Message Encoding

For files containing exactly one protobuf message (metadata, components, changesets), the message is encoded directly without a length prefix:

```
func encodeSingleMessage(msg proto.Message) []byte:
    return proto.Marshal(msg)
```

#### Report Archive Assembly

```
func buildReport(project ProjectData) *ZipArchive:
    archive = new ZipArchive

    // 1. Metadata (single-message)
    metadata = buildMetadata(project)
    archive.add("metadata.pb", encodeSingle(metadata))

    // 2. Active rules (length-delimited, filtered by language)
    languages = collectLanguages(project.components)
    rules = filterRulesByLanguage(project.activeRules, languages)
    archive.add("activerules.pb", encodeLengthDelimited(rules))

    // 3. Ad-hoc rules for external issues (length-delimited)
    archive.add("adhocrules.pb", encodeLengthDelimited(project.adHocRules))

    // 4. Context properties (empty sentinel)
    archive.add("context-props.pb", []byte{})

    // 5. Per-component files
    for ref, component in project.components:
        archive.add(fmt.Sprintf("component-%d.pb", ref), encodeSingle(component))

        if issues = project.issues[ref]; len(issues) > 0:
            archive.add(fmt.Sprintf("issues-%d.pb", ref), encodeLengthDelimited(issues))

        if extIssues = project.externalIssues[ref]; len(extIssues) > 0:
            archive.add(fmt.Sprintf("external-issues-%d.pb", ref), encodeLengthDelimited(extIssues))

        if measures = project.measures[ref]; len(measures) > 0:
            archive.add(fmt.Sprintf("measures-%d.pb", ref), encodeLengthDelimited(measures))

        if duplications = project.duplications[ref]; len(duplications) > 0:
            archive.add(fmt.Sprintf("duplications-%d.pb", ref), encodeLengthDelimited(duplications))

        if changeset = project.changesets[ref]; changeset != nil:
            archive.add(fmt.Sprintf("changesets-%d.pb", ref), encodeSingle(changeset))

        if source = project.sources[ref]; source != "":
            archive.add(fmt.Sprintf("source-%d.txt", ref), []byte(source))

    return archive
```

#### Flat Component Structure

All file components are assigned as direct children of the project root component (ref=1). Component references are assigned sequentially starting from ref=2. This flat structure avoids directory hierarchy complexities:

```
ref=1: PROJECT  (root, type=PROJECT)
ref=2: FILE     (path="src/main/java/Foo.java", parent=1)
ref=3: FILE     (path="src/main/java/Bar.java", parent=1)
...
```

#### Duplicate Detection

Before uploading, generate a random `scm_revision_id` for the synthetic analysis. The actual implementation in `builder.go` uses `randomHex(20)` to produce a 40-character hex string (mimicking a git SHA), NOT a deterministic SHA-256 hash:

```
func generateRevisionID() string:
    return randomHex(20)  // 40-char random hex string (20 random bytes, hex-encoded)
```

Query `/api/ce/activity?component={projectKey}` to check if this revision has already been processed. Skip upload if found.

#### Upload and Retry

```
func upload(archive *ZipArchive, projectKey string, cloudClient *cloud.Client) error:
    for attempt = 1 to MAX_RETRIES (5):
        taskID, err = submitReport(archive, projectKey)
        if err != nil:
            sleep(3 * time.Second)
            continue

        // Poll for completion via GET /api/ce/task?id={taskID}
        for pollAttempt = 1 to 5:
            status = pollTaskStatus(taskID)  // uses GET /api/ce/task?id={taskID}
            if status == "SUCCESS":
                return nil
            if status == "FAILED":
                return error("CE task failed")
            sleep(3 * time.Second)

    return error("upload failed after retries")
```

### Data Flow

1. **Collect** — Downstream specs (SPEC-002 through SPEC-005) extract data from SonarQube Server and produce typed Go structs.
2. **Transform** — Each data type is converted into the corresponding protobuf message structure using builder functions.
3. **Encode** — Protobuf messages are serialized using the appropriate encoding style (single-message or length-delimited).
4. **Archive** — Encoded files are assembled into a ZIP archive matching SonarScanner's expected layout.
5. **Upload** — The ZIP archive is uploaded to SonarQube Cloud's `/api/ce/submit` endpoint via multipart POST.
6. **Poll** — The Compute Engine task status is polled until completion, failure, or timeout.
7. **Verify** — The deduplication system records the `scm_revision_id` to prevent re-uploads on subsequent runs.

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/ce/submit` | POST | Upload scanner report ZIP archive (multipart form: `report` file + `projectKey` param) |
| `/api/ce/task` | GET | Poll CE task status by task ID (`?id={taskId}`) — primary polling endpoint used by `submit.go` |
| `/api/ce/activity` | GET | Query CE task history by component key (used for deduplication checks) |
| `/api/qualityprofiles/search` | GET | Fetch active quality profiles to build `activerules.pb` |
| `/api/rules/search` | GET | Fetch rule details for active rules encoding |

### Protobuf Schema

The scanner report uses protobuf messages defined in SonarScanner's internal schema. Key message types:

#### ScannerReport.Metadata
```protobuf
message Metadata {
  int64 analysis_date = 1;
  string organization_key = 2;
  string project_key = 3;
  // reserved 4
  int32 root_component_ref = 5;
  bool cross_project_duplication_activated = 6;
  map<string, QProfile> qprofiles_per_language = 7;
  map<string, Plugin> plugins_by_key = 8;
  string branch_name = 9;
  BranchType branch_type = 10;
  string reference_branch_name = 11;
  string relative_path_from_scm_root = 12;
  string scm_revision_id = 13;
  string pull_request_key = 14;
  // reserved 15
  string projectVersion = 16;
  string buildString = 17;
  string target_branch_name = 18;
  // reserved 19, 20
  string new_code_reference_branch = 21;
}

enum BranchType {
  UNSET = 0;
  BRANCH = 1;
  PULL_REQUEST = 2;
}
```

#### ScannerReport.Component
```protobuf
message Component {
  int32 ref = 1;
  // field 2 does not exist
  string name = 3;
  ComponentType type = 4;
  bool is_test = 5;
  string language = 6;
  repeated int32 child_ref = 7 [packed = true];
  repeated ComponentLink link = 8;
  // field 9 does not exist
  string key = 10;
  int32 lines = 11;
  string description = 12;
  FileStatus status = 13;
  string project_relative_path = 14;
  bool markedAsUnchanged = 15;
  string old_relative_file_path = 16;
}

enum ComponentType {
  UNSET = 0;
  PROJECT = 1;
  MODULE = 2 [deprecated];
  DIRECTORY = 3 [deprecated];
  FILE = 4;
}
```

#### ScannerReport.Issue

> **Note:** The Issue message has NO `line`, NO `effort`, and NO `type` fields. Those fields exist on `ExternalIssue` instead.

```protobuf
message Issue {
  string rule_repository = 1;
  string rule_key = 2;
  string msg = 3;
  optional Severity overriddenSeverity = 4;
  double gap = 5;
  TextRange text_range = 6;
  repeated Flow flow = 7;
  bool quickFixAvailable = 8;
  optional string ruleDescriptionContextKey = 9;
  repeated MessageFormatting msgFormatting = 10;
  repeated string codeVariants = 11;
  repeated Impact overriddenImpacts = 12;
  repeated string internal_tags = 13;
}

message TextRange {
  int32 start_line = 1;
  int32 end_line = 2;
  int32 start_offset = 3;
  int32 end_offset = 4;
}

message Flow {
  repeated IssueLocation location = 1;
}

message IssueLocation {
  int32 component_ref = 1;
  TextRange text_range = 2;
  string msg = 3;
}
```

#### ScannerReport.ExternalIssue

```protobuf
message ExternalIssue {
  string engine_id = 1;
  string rule_id = 2;
  string msg = 3;
  optional Severity severity = 4;
  int64 effort = 5;
  TextRange text_range = 6;
  repeated Flow flow = 7;
  optional IssueType type = 8;
  repeated MessageFormatting msgFormatting = 9;
  repeated Impact impacts = 10;
  optional CleanCodeAttribute cleanCodeAttribute = 11;
}
```

#### ScannerReport.AdHocRule

```protobuf
message AdHocRule {
  string engine_id = 1;
  string rule_id = 2;
  string name = 3;
  string description = 4;
  optional Severity severity = 5;
  optional IssueType type = 6;
  optional CleanCodeAttribute cleanCodeAttribute = 7;
  repeated Impact defaultImpacts = 8;
}
```

#### ScannerReport.ActiveRule
```protobuf
message ActiveRule {
  string rule_repository = 1;
  string rule_key = 2;
  Severity severity = 3;
  map<string, string> params_by_key = 4;
  int64 created_at = 5;
  int64 updated_at = 6;
  string q_profile_key = 7;
  repeated Impact impacts = 8;
}
```

#### ScannerReport.Measure
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
// Each nested type has: int32/double/string value = 1; string data = 2;
```

#### ScannerReport.Changesets
```protobuf
message Changesets {
  int32 component_ref = 1;
  bool copy_from_previous = 2;
  repeated Changeset changeset = 3;
  repeated int32 changesetIndexByLine = 4 [packed = true];
}

message Changeset {
  string revision = 1;
  string author = 2;
  int64 date = 3;
}
```

#### ScannerReport.Duplication
```protobuf
message Duplication {
  TextRange origin_position = 1;
  repeated Duplicate duplicate = 2;

  message Duplicate {
    int32 other_file_ref = 1;
    TextRange range = 2;
  }
}
```

#### Enums (from constants.proto)

```protobuf
enum Severity {
  UNSET_SEVERITY = 0;
  INFO = 1;
  MINOR = 2;
  MAJOR = 3;
  CRITICAL = 4;
  BLOCKER = 5;
}

enum IssueType {
  ISSUE_TYPE_UNSET = 0;
  CODE_SMELL = 1;
  BUG = 2;
  VULNERABILITY = 3;
  SECURITY_HOTSPOT = 4;
}

enum CleanCodeAttribute {
  CLEAN_CODE_ATTRIBUTE_UNSPECIFIED = 0;
  CONVENTIONAL = 1; FORMATTED = 2; IDENTIFIABLE = 3;
  CLEAR = 4; COMPLETE = 5; EFFICIENT = 6; LOGICAL = 7;
  DISTINCT = 8; FOCUSED = 9; MODULAR = 10; TESTED = 11;
  LAWFUL = 12; RESPECTFUL = 13; TRUSTWORTHY = 14;
}

enum SoftwareQuality {
  UNKNOWN_IMPACT_QUALITY = 0;
  MAINTAINABILITY = 1; RELIABILITY = 2; SECURITY = 3;
}

enum ImpactSeverity {
  UNKNOWN_IMPACT_SEVERITY = 0;
  LOW = 1; MEDIUM = 2; HIGH = 3; INFO = 4; BLOCKER = 5;
}

message Impact {
  SoftwareQuality software_quality = 1;
  ImpactSeverity severity = 2;
}
```

## Acceptance Criteria

- [ ] AC-1: Proto definitions compile at build time via `protoc-gen-go` with no runtime proto compilation.
- [ ] AC-2: Generated ZIP archive is byte-compatible with SonarScanner output (validated against a reference archive from a real scan).
- [ ] AC-3: Single-message encoding produces correct output for metadata, component, and changeset files.
- [ ] AC-4: Length-delimited encoding produces correct output for issues, rules, measures, and duplication files.
- [ ] AC-5: Upload via `/api/ce/submit` succeeds and CE processes the report without errors.
- [ ] AC-6: Retry mechanism handles transient HTTP failures (429, 502, 503, 504) and CE timeouts.
- [ ] AC-7: Duplicate detection prevents re-upload of the same migration revision.
- [ ] AC-8: Active rule filtering reduces report size by at least 50% for multi-language projects.
- [ ] AC-9: Empty sentinel files (`context-props.pb`) are included in the archive.
- [ ] AC-10: Reports with 10,000+ components generate within 60 seconds.
- [ ] AC-11: Archive generation streams to disk and does not buffer entire archive in memory.
- [ ] AC-12: Unit tests with golden file comparisons cover all encoding paths.

## CloudVoyager Reference

| Area | Path |
|------|------|
| Proto schema definitions | `src/pipelines/scanner-report/proto/` |
| Report builder | `src/pipelines/scanner-report/builders/` |
| Protobuf encoder | `src/pipelines/scanner-report/encoders/` |
| ZIP archive assembly | `src/pipelines/scanner-report/archive.js` |
| CE upload + retry | `src/pipelines/scanner-report/uploader.js` |
| Duplicate detection | `src/pipelines/scanner-report/dedup.js` |
| Active rule filtering | `src/pipelines/scanner-report/rules.js` |

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt

## Known Limitations

- The protobuf schema is reverse-engineered from SonarScanner internals and is not a public API. Schema changes in future SonarScanner versions may break compatibility.
- The flat component structure (all files as direct children of project root) may not preserve directory-level aggregation metrics accurately. SonarQube Cloud re-computes these during CE processing.
- CE task processing is asynchronous and can take significant time for large projects. The polling mechanism must handle long-running tasks gracefully.
- Maximum ZIP archive size accepted by SonarQube Cloud CE is undocumented. Very large projects (>100K files) may need to be split across multiple synthetic analyses.
- The `context-props.pb` file is always empty. Real SonarScanner populates this with CI environment detection, but this is unnecessary for migration purposes.

## Open Questions

- What is the maximum report size accepted by SonarQube Cloud's CE endpoint? Should we implement archive splitting for very large projects?
- Should we use SonarScanner's public proto definitions (from `sonar-scanner-engine`) or maintain our own copy? The public definitions may drift from what CE actually accepts.
- How should we handle SonarQube Server versions that use different protobuf field numbering (9.9 vs 10.x vs 2025.x)?
- Should report generation be parallelized per-project or per-component within a project?
- What is the optimal CE queue depth — can we upload multiple reports concurrently without causing CE backpressure?
