---
spec_id: SPEC-002
title: Issue Migration Pipeline
status: draft
priority: P0
epic: "Core Data Migration"
depends_on: [SPEC-001]
estimated_effort: XL
cloudvoyager_ref: "src/pipelines/issues/..."
---

# SPEC-002: Issue Migration Pipeline
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

The Issue Migration Pipeline extracts all code issues from SonarQube Server and encodes them into the protobuf scanner report format (SPEC-001) for injection into SonarQube Cloud. Issues are the core unit of value in SonarQube — they represent bugs, vulnerabilities, and code smells identified by static analysis. Losing issue history during migration means losing years of triage decisions, false-positive annotations, and trend data that engineering teams rely on for compliance and quality tracking.

This pipeline handles the complete lifecycle: extraction from the SonarQube Server API with full pagination, transformation into protobuf-compatible structures, grouping by component, and encoding into `issues-{ref}.pb` files within the scanner report archive. It must handle the significant API differences between SonarQube Server versions (9.9 LTA, 10.x, and 2025.x) and correctly map issue metadata including severity, type, status, text ranges, flows, and Clean Code attributes.

Post-upload status synchronization (transitioning issues to their correct status in SonarQube Cloud after CE processing) is covered in a separate spec (SPEC-008). This spec focuses on the extraction and protobuf encoding phases.

## Problem Statement

The sonar-migration-tool currently migrates configurations but no historical data. When organizations move from SonarQube Server to SonarQube Cloud, they lose all accumulated issues — often tens or hundreds of thousands of issues per project representing years of analysis history. This loss is unacceptable for organizations that depend on issue trend data for compliance (SOC2, ISO 27001), engineering metrics (defect density trends), and operational workflows (triage queues, assignment tracking). The Issue Migration Pipeline fills this critical gap by preserving the complete issue corpus.

## User Stories

- **As a** migration operator, **I want to** extract all issues from SonarQube Server projects, **so that** no issue data is lost during migration.
- **As a** migration operator, **I want to** preserve issue metadata (severity, type, status, tags, text ranges), **so that** issues appear correctly in SonarQube Cloud.
- **As a** migration operator, **I want to** handle different SonarQube Server versions transparently, **so that** migration works regardless of the source server version.
- **As a** migration operator, **I want to** see progress during issue extraction, **so that** I know how long the migration will take for large projects.
- **As a** migration operator, **I want to** resume interrupted extractions, **so that** network failures do not require starting over.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Extract all issues from SonarQube Server via `/api/issues/search` with full pagination | Must |
| FR-2 | Support all issue types: BUG, VULNERABILITY, CODE_SMELL | Must |
| FR-3 | Extract issue text ranges (startLine, endLine, startOffset, endOffset) | Must |
| FR-4 | Extract issue flows and secondary locations | Must |
| FR-5 | Extract issue tags, author, creation date, and update date | Must |
| FR-6 | Handle SQ 9.9 status model: OPEN, CONFIRMED, REOPENED, RESOLVED, CLOSED | Must |
| FR-7 | Handle SQ 10.0-10.3 status model: uses `statuses` param with OPEN, CONFIRMED, REOPENED, RESOLVED, CLOSED (same as 9.9) | Must |
| FR-8 | Handle SQ 10.4+ API change: `issueStatuses` parameter replaces `statuses`, with values OPEN, CONFIRMED, FALSE_POSITIVE, ACCEPTED, FIXED | Must |
| FR-9 | Encode issues into protobuf `issues-{ref}.pb` files (length-delimited, one per component) | Must |
| FR-10 | Map issue rule keys to ruleRepository + ruleKey protobuf fields | Must |
| FR-11 | Map issue severity to protobuf Severity enum (BLOCKER, CRITICAL, MAJOR, MINOR, INFO) | Must |
| FR-12 | Issue type is determined by the rule definition, not set on the Issue proto (type field only exists on ExternalIssue) | Must |
| FR-13 | Extract issue changelog via `/api/issues/changelog` for metadata pre-filtering | Should |
| FR-14 | Detect manual changes (human-authored status transitions) to avoid re-syncing | Should |
| FR-15 | Include Clean Code attributes in protobuf encoding (see SPEC-012) | Should |
| FR-16 | Separate external issues into `externalissues-{ref}.pb` files (see SPEC-013) | Should |
| FR-17 | Write extracted issues to JSONL intermediate files for resume capability | Must |
| FR-18 | Support filtering by issue creation date range for incremental migration | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Extraction throughput | >= 1,000 issues/second with pagination |
| NFR-2 | Maximum issues per project | Handle 500K+ issues per project |
| NFR-3 | Memory usage | Stream processing, not loading all issues into memory |
| NFR-4 | API rate limiting | Respect SonarQube Server rate limits with backoff |
| NFR-5 | Progress reporting | Log progress every 1,000 issues extracted |
| NFR-6 | Resume capability | Track last extracted page for crash recovery |

## Technical Design

### Architecture

The `go/internal/scanreport/` package already contains a working skeleton (builder.go, packager.go, submit.go, backdate.go) that should be extended, not replaced.

The Issue Migration Pipeline spans two phases of the existing task engine:

**Extract phase** (`go/internal/extract/tasks_issues.go`):
- New extract tasks that fetch issues from SonarQube Server
- Write issues to JSONL files in `files/<extract_id>/issues/`

**Scanner phase** (`go/internal/scanreport/issues.go`):
- Transform extracted issues into protobuf message structures
- Group by component reference
- Encode into `issues-{ref}.pb` files for the report archive

The existing `types.Issue` struct in `lib/sq-api-go/types/issues.go` needs to be extended with additional fields (textRange, flows, comments, Clean Code attributes) to capture the full issue data needed for protobuf encoding.

### Key Algorithms

#### Version-Aware Issue Extraction

```
func extractIssues(ctx context.Context, client *server.Client, projectKey string, serverVersion semver.Version) ([]Issue, error):
    params = baseParams(projectKey)

    // Version-specific status parameter
    // IMPORTANT: FALSE_POSITIVE and ACCEPTED are NOT valid `statuses` param values in SQ 9.9-10.3.
    // The `issueStatuses` param (with new status values) was introduced in SQ 10.4.
    if serverVersion >= 10.4:
        params.Set("issueStatuses", "OPEN,CONFIRMED,FALSE_POSITIVE,ACCEPTED,FIXED")
    else:  // 9.9 through 10.3
        params.Set("statuses", "OPEN,CONFIRMED,REOPENED,RESOLVED,CLOSED")

    // SonarQube API limits search to 10,000 results
    // For projects with >10K issues, partition by creation date
    issues = []Issue{}
    paginator = client.Issues().Search(ctx, projectKey, params)

    for paginator.HasNext():
        batch, err = paginator.Next(ctx)
        if err != nil:
            return nil, err
        issues = append(issues, batch...)

    // If we hit the 10K limit, re-fetch with date partitioning
    if len(issues) >= 10000:
        issues = extractWithDatePartitioning(ctx, client, projectKey, params)

    return issues, nil
```

#### 10K Issue Limit Workaround

SonarQube's `/api/issues/search` returns a maximum of 10,000 results. For projects exceeding this limit, partition by creation date:

```
func extractWithDatePartitioning(ctx, client, projectKey, baseParams) []Issue:
    allIssues = []Issue{}

    // Binary search for date ranges that fit within 10K limit
    startDate = "2000-01-01"
    endDate = today()

    queue = [(startDate, endDate)]

    while len(queue) > 0:
        start, end = queue.pop()
        params = clone(baseParams)
        params.Set("createdAfter", start)
        params.Set("createdBefore", end)

        count = fetchIssueCount(ctx, client, projectKey, params)

        if count <= 10000:
            // Safe to paginate this range
            issues = paginateAll(ctx, client, projectKey, params)
            allIssues = append(allIssues, issues...)
        else:
            // Split the date range in half
            mid = midpoint(start, end)
            queue.push((start, mid))
            queue.push((mid, end))

    return deduplicate(allIssues)
```

#### Rule Key Decomposition

SonarQube Server returns rule keys in the format `repository:key` (e.g., `java:S1234`). The protobuf format requires these as separate fields:

```
func decomposeRuleKey(ruleKey string) (repository, key string):
    parts = strings.SplitN(ruleKey, ":", 2)
    if len(parts) == 2:
        return parts[0], parts[1]
    return "unknown", ruleKey
```

#### Issue-to-Protobuf Transformation

```
func transformIssue(issue types.Issue, componentRef int32) *pb.Issue:
    repo, key = decomposeRuleKey(issue.Rule)

    pbIssue = &pb.Issue{
        RuleRepository: repo,
        RuleKey:        key,
        Msg:            issue.Message,
        Severity:       mapSeverity(issue.Severity),
        Gap:            parseFloat(issue.Gap),
        // NOTE: pb.Issue has NO `type` field — issue type is determined by the rule definition.
        // NOTE: pb.Issue has NO `effort` field.
        // NOTE: pb.Issue has NO `line` field — line number is part of TextRange (start_line).
    }

    // Text range
    if issue.TextRange != nil:
        pbIssue.TextRange = &pb.TextRange{
            StartLine:   int32(issue.TextRange.StartLine),
            EndLine:     int32(issue.TextRange.EndLine),
            StartOffset: int32(issue.TextRange.StartOffset),
            EndOffset:   int32(issue.TextRange.EndOffset),
        }

    // Flows (secondary locations)
    for _, flow in issue.Flows:
        pbFlow = &pb.Flow{}
        for _, location in flow.Locations:
            pbFlow.Location = append(pbFlow.Location, &pb.IssueLocation{
                ComponentRef: resolveComponentRef(location.Component),
                TextRange:    mapTextRange(location.TextRange),
                Msg:          location.Msg,
            })
        pbIssue.Flow = append(pbIssue.Flow, pbFlow)

    return pbIssue
```

#### Severity Mapping

Severity enum values: UNSET_SEVERITY=0, INFO=1, MINOR=2, MAJOR=3, CRITICAL=4, BLOCKER=5.

```
func mapSeverity(s string) pb.Severity:
    switch strings.ToUpper(s):
        case "BLOCKER": return pb.Severity_BLOCKER    // 5
        case "CRITICAL": return pb.Severity_CRITICAL  // 4
        case "MAJOR": return pb.Severity_MAJOR        // 3
        case "MINOR": return pb.Severity_MINOR        // 2
        case "INFO": return pb.Severity_INFO          // 1
        default: return pb.Severity_MAJOR             // safe default (3)
```

#### Issue Type — N/A for pb.Issue

The `pb.Issue` message does NOT have a `type` field. Issue type (BUG, VULNERABILITY, CODE_SMELL, SECURITY_HOTSPOT) is determined by the rule definition, not set on the Issue proto. The `IssueType` field only exists on `ExternalIssue` messages.

For reference, the IssueType enum values are: ISSUE_TYPE_UNSET=0, CODE_SMELL=1, BUG=2, VULNERABILITY=3, SECURITY_HOTSPOT=4.

#### Effort Duration — N/A for pb.Issue

The `pb.Issue` message does NOT have an `effort` field. The `parseDuration()` function referenced in CloudVoyager is not applicable to the internal Issue proto. Effort/remediation data is not encoded on the Issue message.

### Data Flow

1. **Extract Issues** — Paginate through `/api/issues/search` for each project, handling the 10K result limit with date partitioning. Write raw issue JSON to JSONL files.
2. **Extend Issue Data** — For issues with flows, fetch additional location details. For issues needing changelog, fetch via `/api/issues/changelog`.
3. **Group by Component** — Partition issues by their component key. Resolve each component to its protobuf reference number (assigned in SPEC-001).
4. **Transform to Protobuf** — Convert each issue to a `pb.Issue` message, decomposing rule keys and mapping severity enums. Note: issue type is not set on pb.Issue (determined by rule), and effort is not a field on pb.Issue.
5. **Encode per Component** — For each component reference, collect all its issues and encode as a length-delimited `issues-{ref}.pb` file.
6. **Add to Report** — Pass encoded files to the Report Builder (SPEC-001) for inclusion in the ZIP archive.

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/issues/search` | GET | Extract issues with pagination. Key params: `components`, `statuses`/`issueStatuses`, `types`, `createdAfter`, `createdBefore`, `p`, `ps` |
| `/api/issues/changelog` | GET | Fetch issue changelog for manual change detection. Params: `issue` (issue key) |
| `/api/rules/show` | GET | Fetch full rule details including Clean Code attributes. Params: `key` |
| `/api/system/info` | GET | Detect SonarQube Server version for API compatibility |

### Protobuf Schema

Issues are encoded using the `ScannerReport.Issue` message (see SPEC-001 for full schema). Each `issues-{ref}.pb` file contains zero or more Issue messages in length-delimited format. Key points:

- `rule_repository` and `rule_key` are split from the Server's combined `rule` field (e.g., `"java:S1234"` becomes `repository="java"`, `key="S1234"`)
- `severity` uses the Severity enum: UNSET_SEVERITY=0, INFO=1, MINOR=2, MAJOR=3, CRITICAL=4, BLOCKER=5
- `text_range` captures the exact code location
- `flow` contains secondary locations for data-flow issues
- **Note:** The `pb.Issue` message does NOT have `type`, `effort`, or `line` fields. Issue type is determined by the rule definition, not set on the Issue proto. Line number is part of `text_range` (start_line). Effort does not exist on the Issue proto.

### Extended Issue Type

The existing `types.Issue` struct must be extended to capture all fields needed for protobuf encoding:

```go
type Issue struct {
    Key              string      `json:"key"`
    Rule             string      `json:"rule"`
    Severity         string      `json:"severity"`
    Component        string      `json:"component"`
    Project          string      `json:"project"`
    Status           string      `json:"status"`
    Resolution       string      `json:"resolution"`
    Type             string      `json:"type"`
    Effort           string      `json:"effort"`
    Debt             string      `json:"debt"`
    Tags             []string    `json:"tags"`
    Author           string      `json:"author"`
    CreationDate     string      `json:"creationDate"`
    UpdateDate       string      `json:"updateDate"`
    CloseDate        string      `json:"closeDate"`
    // New fields for protobuf encoding
    Line             int         `json:"line"`
    Message          string      `json:"message"`
    Gap              string      `json:"gap"`
    TextRange        *TextRange  `json:"textRange"`
    Flows            []Flow      `json:"flows"`
    Comments         []Comment   `json:"comments"`
    Transitions      []string    `json:"transitions"`
    CleanCodeAttribute          string `json:"cleanCodeAttribute"`
    CleanCodeAttributeCategory  string `json:"cleanCodeAttributeCategory"`
    Impacts          []Impact    `json:"impacts"`
}

type TextRange struct {
    StartLine   int `json:"startLine"`
    EndLine     int `json:"endLine"`
    StartOffset int `json:"startOffset"`
    EndOffset   int `json:"endOffset"`
}

type Flow struct {
    Locations []IssueLocation `json:"locations"`
}

type IssueLocation struct {
    Component string     `json:"component"`
    TextRange *TextRange `json:"textRange"`
    Msg       string     `json:"msg"`
}

type Comment struct {
    Key       string `json:"key"`
    Login     string `json:"login"`
    HtmlText  string `json:"htmlText"`
    Markdown  string `json:"markdown"`
    CreatedAt string `json:"createdAt"`
}

type Impact struct {
    SoftwareQuality string `json:"softwareQuality"`
    Severity        string `json:"severity"`
}
```

## Acceptance Criteria

- [ ] AC-1: All issues are extracted from SonarQube Server for each project, including projects with >10K issues (date partitioning).
- [ ] AC-2: Issues are correctly encoded into length-delimited `issues-{ref}.pb` files grouped by component.
- [ ] AC-3: Rule keys are correctly decomposed into repository and key fields.
- [ ] AC-4: Severity and type enums are correctly mapped to protobuf values.
- [ ] AC-5: Text ranges and flows are preserved with correct line/offset values.
- [ ] AC-6: Gap values are correctly parsed from SQ format (effort is not encoded on pb.Issue).
- [ ] AC-7: Extraction works with SonarQube Server 9.9, 10.x, and 2025.x API differences.
- [ ] AC-8: Issue extraction progress is logged at regular intervals.
- [ ] AC-9: Extracted issues are written to JSONL for resume capability.
- [ ] AC-10: The 10K API limit workaround correctly partitions and deduplicates issues.
- [ ] AC-11: Unit tests cover all severity, type, and status mapping paths.
- [ ] AC-12: Integration test verifies round-trip: extract from mock server, encode to protobuf, decode and verify.

## CloudVoyager Reference

| Area | Path |
|------|------|
| Issue extraction | `src/pipelines/issues/extractor.js` |
| Issue transformer | `src/pipelines/issues/transformer.js` |
| Protobuf encoding | `src/pipelines/scanner-report/encoders/issues.js` |
| Date partitioning | `src/pipelines/issues/partitioner.js` |
| Changelog fetching | `src/pipelines/issues/changelog.js` |
| Status mapping | `src/pipelines/issues/status-mapper.js` |

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt

## Known Limitations

- The SonarQube Server API `/api/issues/search` has a hard limit of 10,000 results per query. The date partitioning workaround adds complexity and may be slow for projects with very dense issue creation patterns.
- Issue comments are extracted but cannot be directly encoded into the protobuf report. Comment migration requires a separate post-upload sync mechanism (future spec).
- Issue assignments are not part of the protobuf format. Assigned-to metadata must be synced post-upload via SonarQube Cloud's issue API.
- The protobuf encoding does not preserve issue keys — SonarQube Cloud generates new keys during CE processing. Matching for post-upload metadata sync relies on rule + component + line.
- Closed issues in SonarQube Server may not have complete text range data. These issues can still be encoded but with degraded location precision.

## Open Questions

- Should we extract CLOSED issues? They do not appear in the default SonarQube UI but may be needed for historical accuracy and metrics.
- How should we handle issues on files that no longer exist in the source code? Should we skip them or create placeholder source files?
- What is the performance impact of fetching changelogs for every issue? Should this be an opt-in feature?
- Should we support incremental issue migration (only new issues since last migration run)?
- How do we handle rule key changes between SonarQube versions (e.g., rule renames in SQ 10.x)?
