---
spec_id: SPEC-003
title: Security Hotspot Migration
status: draft
priority: P0
epic: "Core Data Migration"
depends_on: [SPEC-001]
estimated_effort: L
cloudvoyager_ref: "src/pipelines/hotspots/..."
---

# SPEC-003: Security Hotspot Migration
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

Security Hotspots are a distinct category of findings in SonarQube that represent security-sensitive code that requires human review. Unlike issues (which are deterministic findings), hotspots flag code patterns that may or may not be vulnerable depending on context — such as hardcoded credentials, cryptographic usage, or input validation patterns. Each hotspot has a review status (TO_REVIEW, REVIEWED) and, if reviewed, a resolution (SAFE, FIXED, ACKNOWLEDGED) that captures the human triage decision.

This pipeline extracts all security hotspots from SonarQube Server, converts them into the protobuf issue format (hotspots are encoded as issues distinguished by their rule repository/key in the scanner report), and prepares them for injection into SonarQube Cloud via the Scanner Protocol Engine (SPEC-001). Because hotspot review statuses are critical security audit artifacts, post-upload metadata synchronization (SPEC-009) handles transitioning hotspots to their correct review state after CE processing.

Hotspot migration is essential for organizations subject to security compliance frameworks (SOC2, PCI-DSS, HIPAA) where historical security review decisions must be preserved as audit evidence.

## Problem Statement

Security hotspots represent human security review decisions that are often required for compliance. When migrating from SonarQube Server to SonarQube Cloud without hotspot migration, all review statuses are lost — previously reviewed and marked-safe hotspots reappear as TO_REVIEW, creating a massive re-review burden. For large codebases, this can mean thousands of hotspots that security teams have already triaged must be re-triaged from scratch. The Security Hotspot Migration Pipeline preserves these decisions.

## User Stories

- **As a** migration operator, **I want to** extract all security hotspots from SonarQube Server, **so that** security review history is preserved during migration.
- **As a** security engineer, **I want to** see previously reviewed hotspots retain their SAFE/FIXED/ACKNOWLEDGED status in SonarQube Cloud, **so that** I do not need to re-review thousands of already-triaged findings.
- **As a** compliance officer, **I want to** maintain an unbroken audit trail of security review decisions, **so that** migration does not create compliance gaps.
- **As a** migration operator, **I want to** see hotspot extraction progress with estimated completion times, **so that** I can plan the migration window.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Extract all hotspots from SonarQube Server via `/api/hotspots/search` with full pagination | Must |
| FR-2 | Fetch individual hotspot details via `/api/hotspots/show` for complete metadata | Must |
| FR-3 | Support concurrent hotspot detail fetching (configurable, default 10 workers) | Must |
| FR-4 | Handle hotspot statuses: TO_REVIEW, REVIEWED | Must |
| FR-5 | Handle hotspot resolutions: SAFE, FIXED, ACKNOWLEDGED (when status=REVIEWED) | Must |
| FR-6 | Preserve vulnerability probability: HIGH, MEDIUM, LOW | Must |
| FR-7 | Convert hotspots to protobuf Issue messages (hotspots are distinguished by their rule repository/key, not a type field on pb.Issue) | Must |
| FR-8 | Encode converted hotspots into `issues-{ref}.pb` files alongside regular issues | Must |
| FR-9 | Preserve hotspot rule key, component, line, message, and security category | Must |
| FR-10 | Write extracted hotspots to JSONL intermediate files for resume capability | Must |
| FR-11 | Record hotspot review metadata for post-upload status sync (SPEC-009) | Must |
| FR-12 | Handle pagination limit (max 500 results per page for hotspots API) | Must |
| FR-13 | Support SonarQube Server 9.9+ hotspot API | Must |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Extraction throughput | >= 100 hotspots/second (limited by detail fetching) |
| NFR-2 | Concurrent detail fetches | Configurable 1-20, default 10 |
| NFR-3 | Memory usage | Stream processing per project |
| NFR-4 | Progress reporting | Log progress every 100 hotspots |
| NFR-5 | Error resilience | Continue on individual hotspot fetch failure, log and skip |

## Technical Design

### Architecture

The `go/internal/scanreport/` package already contains a working skeleton (builder.go, packager.go, submit.go, backdate.go) that should be extended, not replaced.

The Hotspot Migration Pipeline adds new components:

**Extract phase** (`go/internal/extract/tasks_hotspots.go`):
- `ExtractHotspots` task: paginate `/api/hotspots/search` per project
- `ExtractHotspotDetails` task: concurrent `/api/hotspots/show` calls for full metadata
- Write to JSONL in `files/<extract_id>/hotspots/`

**Scanner phase** (`go/internal/scanreport/hotspots.go`):
- Transform hotspots into `pb.Issue` messages (distinguished by rule repository/key)
- Merge into component-level issue files alongside regular issues from SPEC-002

The existing `types.Hotspot` struct in `lib/sq-api-go/types/hotspots.go` needs extension for the detail fields returned by `/api/hotspots/show`.

### Key Algorithms

#### Two-Phase Hotspot Extraction

The hotspot search endpoint returns summary data. Full details (including rule info, text range, and review comments) require per-hotspot detail calls:

```
func extractHotspots(ctx context.Context, client *server.Client, projectKey string) ([]HotspotDetail, error):
    // Phase 1: Paginate search to get all hotspot keys
    hotspotSummaries = []Hotspot{}
    paginator = client.Hotspots().Search(ctx, projectKey)

    for paginator.HasNext():
        batch, err = paginator.Next(ctx)
        if err != nil:
            return nil, err
        hotspotSummaries = append(hotspotSummaries, batch...)

    // Phase 2: Concurrent detail fetching
    details = make([]HotspotDetail, len(hotspotSummaries))
    sem = semaphore.NewWeighted(int64(concurrency))  // default 10

    var g errgroup.Group
    for i, hs in enumerate(hotspotSummaries):
        i, hs := i, hs
        g.Go(func() error:
            sem.Acquire(ctx, 1)
            defer sem.Release(1)

            detail, err = client.Hotspots().Show(ctx, hs.Key)
            if err != nil:
                log.Warn("failed to fetch hotspot detail", "key", hs.Key, "err", err)
                details[i] = fallbackFromSummary(hs)  // degrade gracefully
                return nil  // continue, don't fail the whole batch
            details[i] = detail
            return nil
        )

    if err = g.Wait(); err != nil:
        return nil, err

    return details, nil
```

#### Hotspot-to-Issue Protobuf Conversion

Hotspots are represented as issues in the protobuf format, distinguished by their security hotspot rule repository/key:

```
func convertHotspotToProtoIssue(hotspot HotspotDetail, componentRef int32) *pb.Issue:
    repo, key = decomposeRuleKey(hotspot.Rule.Key)

    pbIssue = &pb.Issue{
        RuleRepository: repo,
        RuleKey:        key,
        Msg:            hotspot.Message,
        Severity:       mapVulnerabilityProbabilityToSeverity(hotspot.VulnerabilityProbability),
        // NOTE: pb.Issue has NO `type` field. Hotspot issues are distinguished by their
        // rule repository/key (e.g., security hotspot rules), not by a type field on the
        // Issue proto. The IssueType field only exists on ExternalIssue messages.
        // NOTE: pb.Issue has NO `line` field. Line number is part of TextRange (start_line).
    }

    // Text range if available
    if hotspot.TextRange != nil:
        pbIssue.TextRange = &pb.TextRange{
            StartLine:   int32(hotspot.TextRange.StartLine),
            EndLine:     int32(hotspot.TextRange.EndLine),
            StartOffset: int32(hotspot.TextRange.StartOffset),
            EndOffset:   int32(hotspot.TextRange.EndOffset),
        }

    return pbIssue
```

#### Vulnerability Probability to Severity Mapping

SonarQube Server hotspots use `vulnerabilityProbability` (HIGH, MEDIUM, LOW) instead of the standard severity scale. For protobuf encoding, map to:

```
func mapVulnerabilityProbabilityToSeverity(prob string) pb.Severity:
    switch strings.ToUpper(prob):
        case "HIGH":   return pb.Severity_CRITICAL
        case "MEDIUM": return pb.Severity_MAJOR
        case "LOW":    return pb.Severity_MINOR
        default:       return pb.Severity_MAJOR
```

#### Merging Hotspots with Issues per Component

Both regular issues (SPEC-002) and hotspots contribute to the same `issues-{ref}.pb` files:

```
func buildComponentIssueFile(componentRef int32, issues []pb.Issue, hotspots []pb.Issue) []byte:
    allIssues = append(issues, hotspots...)

    // Sort by start line (from TextRange) for deterministic output
    sort.Slice(allIssues, func(i, j int) bool:
        return allIssues[i].TextRange.GetStartLine() < allIssues[j].TextRange.GetStartLine()
    )

    return encodeLengthDelimited(allIssues)
```

#### Review Metadata Recording

For post-upload status synchronization (SPEC-009), record hotspot review decisions as a sidecar file:

```
type HotspotReviewRecord struct {
    HotspotKey        string `json:"hotspotKey"`
    RuleKey           string `json:"ruleKey"`
    Component         string `json:"component"`
    Line              int    `json:"line"`
    Status            string `json:"status"`            // TO_REVIEW, REVIEWED
    Resolution        string `json:"resolution"`        // SAFE, FIXED, ACKNOWLEDGED
    ReviewDate        string `json:"reviewDate"`
    Reviewer          string `json:"reviewer"`
    ReviewComment     string `json:"reviewComment"`
}
```

Write these records to `files/<extract_id>/hotspot_reviews.jsonl` for consumption by SPEC-009.

### Data Flow

1. **Search** — Paginate `/api/hotspots/search` per project to get all hotspot summary records.
2. **Detail Fetch** — Concurrently call `/api/hotspots/show` for each hotspot key to get full metadata including rule info, text ranges, and review data.
3. **Write JSONL** — Write full hotspot details to JSONL intermediate files.
4. **Record Reviews** — Write hotspot review metadata to a separate JSONL file for post-upload sync.
5. **Convert to Protobuf** — Transform each hotspot into a `pb.Issue` (distinguished by rule repository/key).
6. **Merge with Issues** — Combine hotspot-as-issue messages with regular issues per component.
7. **Encode** — Write merged issues to length-delimited `issues-{ref}.pb` files via SPEC-001.

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/hotspots/search` | GET | Paginated search for hotspots. Params: `projectKey`, `status`, `resolution`, `p`, `ps` (max 500) |
| `/api/hotspots/show` | GET | Full hotspot detail. Params: `hotspot` (key). Returns rule, textRange, flows, changelog, comments |
| `/api/system/info` | GET | Server version detection for API compatibility |

### Protobuf Schema

Hotspots use the same `ScannerReport.Issue` message as regular issues (see SPEC-001), with the following specific field values:

```
Issue {
    rule_repository: <extracted from hotspot rule key>
    rule_key: <extracted from hotspot rule key>
    msg: <hotspot message>
    severity: <mapped from vulnerabilityProbability>
    text_range: <hotspot text range, start_line contains the line number>
    flow: []  // hotspots typically have no flows
    gap: 0
    // NOTE: pb.Issue has NO `type` field — hotspots are distinguished by their rule
    // repository/key, not by a type field. IssueType only exists on ExternalIssue.
    // NOTE: pb.Issue has NO `line` field — line number is part of text_range (start_line).
    // NOTE: pb.Issue has NO `effort` field.
}
```

For reference, the IssueType enum values are: ISSUE_TYPE_UNSET=0, CODE_SMELL=1, BUG=2, VULNERABILITY=3, SECURITY_HOTSPOT=4.

### Extended Hotspot Type

The existing `types.Hotspot` struct needs extension for detail data:

```go
type HotspotDetail struct {
    Key                      string      `json:"key"`
    Component                string      `json:"component"`
    Project                  string      `json:"project"`
    SecurityCategory         string      `json:"securityCategory"`
    VulnerabilityProbability string      `json:"vulnerabilityProbability"`
    Status                   string      `json:"status"`
    Resolution               string      `json:"resolution"`
    Line                     int         `json:"line"`
    Message                  string      `json:"message"`
    Author                   string      `json:"author"`
    CreationDate             string      `json:"creationDate"`
    UpdateDate               string      `json:"updateDate"`
    TextRange                *TextRange  `json:"textRange"`
    Rule                     HotspotRule `json:"rule"`
    Changelog                []ChangelogEntry `json:"changelog"`
    Comment                  []HotspotComment `json:"comment"`
    Assignee                 string      `json:"assignee"`
}

type HotspotRule struct {
    Key                    string `json:"key"`
    Name                   string `json:"name"`
    SecurityCategory       string `json:"securityCategory"`
    VulnerabilityProbability string `json:"vulnerabilityProbability"`
}

type HotspotComment struct {
    Key       string `json:"key"`
    Login     string `json:"login"`
    HtmlText  string `json:"htmlText"`
    Markdown  string `json:"markdown"`
    CreatedAt string `json:"createdAt"`
}

type ChangelogEntry struct {
    User         string              `json:"user"`
    UserName     string              `json:"userName"`
    CreationDate string              `json:"creationDate"`
    Diffs        []ChangelogDiff     `json:"diffs"`
}

type ChangelogDiff struct {
    Key      string `json:"key"`
    NewValue string `json:"newValue"`
    OldValue string `json:"oldValue"`
}
```

## Acceptance Criteria

- [ ] AC-1: All hotspots are extracted from SonarQube Server, including TO_REVIEW and REVIEWED statuses.
- [ ] AC-2: Hotspot details are fetched concurrently with configurable worker count.
- [ ] AC-3: Individual hotspot detail fetch failures are logged and skipped without failing the entire extraction.
- [ ] AC-4: Hotspots are correctly converted to protobuf Issue messages (distinguished by rule repository/key, not a type field on pb.Issue).
- [ ] AC-5: Vulnerability probability is correctly mapped to protobuf severity.
- [ ] AC-6: Hotspot-as-issue messages are merged with regular issues in the same `issues-{ref}.pb` files.
- [ ] AC-7: Hotspot review metadata (status, resolution, reviewer, comment) is recorded to JSONL for post-upload sync.
- [ ] AC-8: Hotspot text ranges are preserved when available from the detail API.
- [ ] AC-9: Extraction handles the 500-per-page pagination limit of the hotspots API.
- [ ] AC-10: Unit tests cover hotspot-to-issue conversion with all vulnerability probability levels.
- [ ] AC-11: Integration test verifies round-trip: extract from mock server, encode to protobuf, decode and verify hotspot rule key is preserved.
- [ ] AC-12: Hotspot extraction for a project with 5,000+ hotspots completes within 5 minutes with 10 concurrent workers.

## CloudVoyager Reference

| Area | Path |
|------|------|
| Hotspot extraction | `src/pipelines/hotspots/extractor.js` |
| Hotspot detail fetching | `src/pipelines/hotspots/detail-fetcher.js` |
| Hotspot-to-issue conversion | `src/pipelines/hotspots/converter.js` |
| Review metadata recording | `src/pipelines/hotspots/review-recorder.js` |
| Concurrent worker pool | `src/pipelines/hotspots/pool.js` |

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt

## Known Limitations

- The `/api/hotspots/show` endpoint must be called individually per hotspot, creating an N+1 query pattern. For projects with thousands of hotspots, this is the primary bottleneck. Concurrency helps but cannot eliminate the inherent latency.
- Hotspot review comments are extracted but cannot be encoded into the protobuf format. Comment migration requires post-upload API calls.
- The `ACKNOWLEDGED` resolution was added in SonarQube 9.4. Servers running older versions will not have this resolution type.
- Hotspot assignments (assignee field) are not part of the protobuf format and require post-upload sync.
- Hotspot security categories may differ between SonarQube Server and SonarQube Cloud if rules have been updated. The category is determined by the rule, not the hotspot record.
- The vulnerability probability-to-severity mapping is an approximation. The protobuf format does not have a native vulnerability probability field, so this information is encoded indirectly via severity.

## Open Questions

- Should we store hotspot review metadata in a separate JSONL file or extend the existing hotspot JSONL with review fields?
- How do we handle hotspots whose rules do not exist in SonarQube Cloud (custom rules, deprecated rules)?
- Should hotspot detail fetching concurrency be auto-tuned based on server response times?
- How do we match hotspots after upload for status sync? The combination of ruleKey + component + line may not be unique if the same rule fires multiple times on the same line.
- Should we support extracting only REVIEWED hotspots (for faster migration of just the review decisions)?
