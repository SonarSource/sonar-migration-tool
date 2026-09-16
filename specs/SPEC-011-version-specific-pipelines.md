---
spec_id: SPEC-011
title: Version-Specific Pipeline Architecture
status: draft
priority: P1
epic: "Version Compatibility"
depends_on: [SPEC-002, SPEC-012]
estimated_effort: XL
cloudvoyager_ref: "src/version-router.js, src/pipelines/sq-9.9/, src/pipelines/sq-10.0/, src/pipelines/sq-10.4/, src/pipelines/sq-2025/"
---

# SPEC-011: Version-Specific Pipeline Architecture
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

SonarQube Server has undergone significant API evolution across major versions. The issue search API, status enumerations, metric batching limits, Clean Code attribute availability, and user group APIs all differ depending on the server version. Rather than scattering version checks throughout the codebase, CloudVoyager adopted a clean architectural pattern: four fully independent pipelines, one per version range, each hardcoding the exact API behaviors for its target version. A version router inspects the server at startup and selects the correct pipeline.

This spec defines the Go equivalent of that architecture. The sonar-migration-tool must ship a `Pipeline` interface with four concrete implementations (SQ 9.9 LTS, SQ 10.0-10.3, SQ 10.4-10.8, SQ 2025.1+). Each implementation encapsulates all version-specific extraction, transformation, and encoding logic. The version router calls `/api/system/status` once, parses the server version string, and returns the appropriate pipeline. No runtime version branching occurs inside a pipeline — this eliminates an entire class of bugs where the wrong API parameter or status value is used for a given server.

The Go implementation should leverage interface-based polymorphism and the existing `sqapi.Client` (which already carries the server version). The pipeline selection happens once at startup, and the selected pipeline is threaded through the entire extraction and build phase. This approach also makes testing straightforward: each pipeline can be tested independently against recorded API responses for its version range.

## Problem Statement

SonarQube's API surface has changed meaningfully across versions 9.9, 10.0, 10.4, and 2025.1. Key differences include the issue search parameter name (`statuses` vs `issueStatuses`), the set of valid status values, metric batching requirements, Clean Code attribute availability, and the groups API endpoint. Without version-specific handling, the migration tool would either fail on older servers or produce incorrect data extractions. A monolithic approach with scattered `if version >= X` checks would be fragile, hard to test, and prone to subtle bugs where a new version's behavior accidentally leaks into an older pipeline.

The current sonar-migration-tool codebase has edition detection (`go/internal/common/edition.go`) but no version-specific extraction pipelines. All issue extraction, hotspot extraction, and report building must be version-aware to produce correct scanner reports for SonarQube Cloud ingestion.

## User Stories

- **As a** migration operator with a SQ 9.9 LTS server, **I want to** have the tool automatically detect my server version and use the correct API parameters, **so that** extraction completes without errors and no issues are missed due to wrong status values.
- **As a** migration operator upgrading from SQ 10.3 to 10.4, **I want to** re-run the migration tool against the new server without changing any configuration, **so that** the tool automatically adapts to the new `issueStatuses` parameter.
- **As a** migration operator with a SQ 2025.1 instance, **I want to** have the tool use the Web API V2 groups endpoint with graceful fallback, **so that** group data is extracted even if the V2 API is partially available.
- **As a** developer extending the tool, **I want to** add support for a new SQ version by implementing a single interface, **so that** I do not need to modify existing pipeline code.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Detect SQ Server version by calling `GET /api/system/status` and parsing the `version` field | Must |
| FR-2 | Parse version string into major.minor components, handling formats like `9.9.0.65466`, `10.4.1`, `2025.1.0` | Must |
| FR-3 | Map parsed version to one of four pipeline ranges: 9.9 LTS, 10.0-10.3, 10.4-10.8, 2025.1+ | Must |
| FR-4 | Return a clear error if the server version is below 9.9 (unsupported) | Must |
| FR-5 | SQ 9.9 pipeline uses `statuses` parameter with values OPEN, CONFIRMED, REOPENED, RESOLVED, CLOSED | Must |
| FR-6 | SQ 10.0-10.3 pipeline uses `statuses` parameter with values OPEN, CONFIRMED, REOPENED, RESOLVED, CLOSED (same as SQ 9.9; FALSE_POSITIVE and ACCEPTED are resolutions, not statuses, in this version range) | Must |
| FR-7 | SQ 10.4-10.8 pipeline uses `issueStatuses` parameter with values OPEN, CONFIRMED, FALSE_POSITIVE, ACCEPTED, FIXED. Note: The `issueStatuses` parameter was introduced in SQ 10.2 but became the recommended parameter starting in 10.4. For simplicity, the 10.0-10.3 pipeline uses the legacy `statuses` parameter. | Must |
| FR-8 | SQ 2025.1+ pipeline uses `issueStatuses` parameter with values OPEN, CONFIRMED, FALSE_POSITIVE, ACCEPTED, FIXED | Must |
| FR-9 | SQ 9.9 pipeline batches metricKeys at 15 per request | Must |
| FR-10 | SQ 2025.1+ pipeline sends all metricKeys in a single request (no batching) | Must |
| FR-11 | SQ 9.9 pipeline enriches Clean Code attributes from SonarQube Cloud (see SPEC-012) | Must |
| FR-12 | SQ 10.0+ pipelines extract native Clean Code attributes from server responses | Must |
| FR-13 | SQ 2025.1+ pipeline uses `/api/v2/authorizations/groups` with fallback to `/api/user_groups/search`. Note: The `/api/v2/authorizations/groups` endpoint is available starting from SQ 10.5 Enterprise edition, not just 2025.1+. However, for simplicity, only the 2025.1+ pipeline uses V2 with fallback. | Must |
| FR-14 | SQ 2025.1+ pipeline handles IN_SANDBOX status (skip or map, since SC has no equivalent). IN_SANDBOX is a SQ 2025.1+ status and should NOT be added to the `issueStatuses` query parameter (it may not be a valid search value). Instead, issues with IN_SANDBOX status should be detected in the results and logged as warnings. | Must |
| FR-15 | Pipeline selection is logged at INFO level with detected version and selected pipeline name | Should |
| FR-16 | Each pipeline exposes identical interface methods regardless of internal implementation differences | Must |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Version detection latency | < 2 seconds (single API call) |
| NFR-2 | Pipeline initialization | No additional API calls beyond version detection |
| NFR-3 | Test coverage per pipeline | >= 90% line coverage via recorded HTTP fixtures |
| NFR-4 | Interface stability | Adding a new version range must not modify existing pipeline implementations |
| NFR-5 | Memory overhead | Pipeline structs should be lightweight (< 1KB each) — heavy data stays in extraction results |

## Technical Design

### Architecture

The pipeline architecture introduces a new package at `go/internal/pipeline/` with the following structure:

```
go/internal/pipeline/
├── pipeline.go          # Pipeline interface definition
├── router.go            # Version detection and pipeline selection
├── router_test.go       # Router tests with version string fixtures
├── sq99/                # SQ 9.9 LTS pipeline
│   ├── pipeline.go      # Implements Pipeline interface
│   ├── issues.go        # Issue extraction with legacy statuses
│   ├── hotspots.go      # Hotspot extraction
│   ├── metrics.go       # Metric extraction with batching
│   ├── groups.go        # Standard groups API
│   └── cleancode.go     # Clean Code enrichment from SC
├── sq100/               # SQ 10.0-10.3 pipeline
│   ├── pipeline.go
│   ├── issues.go
│   ├── hotspots.go
│   ├── metrics.go
│   ├── groups.go
│   └── cleancode.go     # Native Clean Code extraction
├── sq104/               # SQ 10.4-10.8 pipeline
│   ├── pipeline.go
│   ├── issues.go        # Uses issueStatuses (modern)
│   ├── hotspots.go
│   ├── metrics.go
│   ├── groups.go
│   └── cleancode.go
└── sq2025/              # SQ 2025.1+ pipeline
    ├── pipeline.go
    ├── issues.go
    ├── hotspots.go
    ├── metrics.go        # No batching needed
    ├── groups.go          # V2 API with fallback
    └── cleancode.go
```

The `Pipeline` interface:

```go
package pipeline

import "context"

// Pipeline defines the version-specific extraction and transformation
// operations. Each SQ version range has a concrete implementation.
type Pipeline interface {
    // Version returns the human-readable pipeline identifier (e.g., "sq-9.9").
    Version() string

    // ExtractIssues extracts all issues for a project using version-appropriate
    // API parameters and status values.
    ExtractIssues(ctx context.Context, projectKey string) ([]Issue, error)

    // ExtractHotspots extracts all security hotspots for a project.
    ExtractHotspots(ctx context.Context, projectKey string) ([]Hotspot, error)

    // ExtractMetrics extracts all component metrics, respecting version-specific
    // batching requirements for metricKeys.
    ExtractMetrics(ctx context.Context, projectKey string, metricKeys []string) ([]ComponentMetrics, error)

    // ExtractGroups extracts user groups, using the appropriate API endpoint
    // for the server version.
    ExtractGroups(ctx context.Context) ([]Group, error)

    // EnrichCleanCode applies Clean Code attributes to issues. For SQ 9.9
    // this enriches from SonarQube Cloud; for 10.0+ this is a no-op (already native).
    EnrichCleanCode(ctx context.Context, issues []Issue, cloudClient *sqapi.Client) ([]Issue, error)

    // IssueSearchParam returns the query parameter name for issue status
    // filtering ("statuses" or "issueStatuses").
    IssueSearchParam() string

    // IssueStatusValues returns the valid status values for issue search.
    IssueStatusValues() []string

    // SupportsMetricBatching returns true if metricKeys must be batched
    // (SQ 9.9 through 10.8) and the batch size.
    SupportsMetricBatching() (bool, int)
}
```

### Key Algorithms

#### Version Detection and Pipeline Selection

```
FUNCTION DetectPipeline(serverClient):
    response = GET /api/system/status
    versionStr = response.version       // e.g., "10.4.1.87632"
    
    parts = split(versionStr, ".")
    major = parseInt(parts[0])
    minor = parseInt(parts[1])
    
    // Handle new versioning scheme (2025.x)
    IF major >= 2025:
        RETURN new SQ2025Pipeline(serverClient)
    // Forward compatibility: 10.9+ (if it ever exists) maps to the 10.4 pipeline
    // 11.x would also map to 2025 pipeline (handled by >= 2025 above, but 11.x
    // is not expected; if it occurs, treat as 10.4 pipeline)
    ELSE IF major >= 11:
        log.Warn("Unexpected SQ version %s (major=%d), using SQ 10.4 pipeline", versionStr, major)
        RETURN new SQ104Pipeline(serverClient)
    ELSE IF major == 10 AND minor >= 4:
        RETURN new SQ104Pipeline(serverClient)
    ELSE IF major == 10 AND minor >= 0:
        RETURN new SQ100Pipeline(serverClient)
    ELSE IF major == 9 AND minor == 9:
        RETURN new SQ99Pipeline(serverClient)
    ELSE:
        RETURN error("Unsupported SQ version: {versionStr}. Minimum supported: 9.9")
```

> **Note on metric batching:** The 15-key batching limit for metricKeys is a CloudVoyager implementation convention (likely a conservative URL-length workaround), not a documented SonarQube API constraint. Implementations may use higher batch sizes or send all keys at once if the URL length permits.

#### Metric Batching (SQ 9.9 through 10.8)

```
FUNCTION ExtractMetricsBatched(projectKey, allMetricKeys, batchSize):
    results = []
    FOR i = 0; i < len(allMetricKeys); i += batchSize:
        batch = allMetricKeys[i : min(i+batchSize, len(allMetricKeys))]
        batchResult = GET /api/measures/component?component={projectKey}&metricKeys={join(batch, ",")}
        results = append(results, batchResult.measures...)
    RETURN results
```

#### V2 Groups API with Fallback (SQ 2025.1+)

```
FUNCTION ExtractGroupsV2WithFallback():
    response, err = GET /api/v2/authorizations/groups?pageSize=500
    IF err != nil OR response.status == 404:
        log.Warn("V2 groups API unavailable, falling back to standard API")
        RETURN ExtractGroupsStandard()   // /api/user_groups/search
    RETURN parseV2GroupsResponse(response)
```

### Data Flow

1. **Startup**: CLI parses flags and loads config. Server URL and token are available.
2. **Version Detection**: `router.DetectPipeline()` calls `/api/system/status`, parses version, instantiates the correct pipeline.
3. **Pipeline Injection**: The selected `Pipeline` is passed to all extraction and build functions via dependency injection (struct field or function parameter).
4. **Extraction Phase**: Each extraction task (issues, hotspots, metrics, groups) calls the pipeline's methods. The pipeline internally uses the correct API parameters.
5. **Build Phase**: The build phase receives extracted data (already version-normalized) and encodes into protobuf scanner reports.
6. **Upload Phase**: Scanner reports are submitted to SonarQube Cloud. This phase is version-independent.

### API Dependencies

| Endpoint | Method | Purpose | Pipeline(s) |
|----------|--------|---------|-------------|
| `/api/system/status` | GET | Detect server version | Router (all) |
| `/api/issues/search` | GET | Extract issues with version-specific params | All |
| `/api/hotspots/search` | GET | Extract security hotspots | All |
| `/api/measures/component` | GET | Extract component metrics (batched or unbatched) | All |
| `/api/user_groups/search` | GET | Extract user groups (standard API) | SQ 9.9, 10.0, 10.4 |
| `/api/v2/authorizations/groups` | GET | Extract user groups (V2 API) | SQ 2025.1+ |
| `/api/rules/search` | GET | Fetch rules for Clean Code enrichment | SQ 9.9 (via SC client) |

## Acceptance Criteria

- [ ] AC-1: Version router correctly identifies and selects the SQ 9.9 pipeline for server versions 9.9.x
- [ ] AC-2: Version router correctly identifies and selects the SQ 10.0 pipeline for server versions 10.0.x through 10.3.x
- [ ] AC-3: Version router correctly identifies and selects the SQ 10.4 pipeline for server versions 10.4.x through 10.8.x
- [ ] AC-4: Version router correctly identifies and selects the SQ 2025 pipeline for server versions 2025.1.x and later
- [ ] AC-5: Version router returns a descriptive error for server versions below 9.9
- [ ] AC-6: SQ 9.9 pipeline issue extraction uses `statuses=OPEN,CONFIRMED,REOPENED,RESOLVED,CLOSED`
- [ ] AC-7: SQ 10.4+ pipeline issue extraction uses `issueStatuses=OPEN,CONFIRMED,FALSE_POSITIVE,ACCEPTED,FIXED`
- [ ] AC-8: SQ 9.9 pipeline metric extraction batches at 15 keys per request (verified via HTTP recording)
- [ ] AC-9: SQ 2025.1+ pipeline metric extraction sends all keys in one request (verified via HTTP recording)
- [ ] AC-10: SQ 2025.1+ pipeline attempts V2 groups API first, falls back to standard on 404
- [ ] AC-11: SQ 2025.1+ pipeline gracefully handles IN_SANDBOX status (logged and skipped)
- [ ] AC-12: All four pipelines implement the full `Pipeline` interface (compile-time check via `var _ Pipeline = (*SQ99Pipeline)(nil)`)
- [ ] AC-13: Adding a hypothetical SQ 11.0 pipeline requires zero changes to existing pipeline implementations
- [ ] AC-14: Pipeline selection is logged at INFO level: "Detected SonarQube Server {version}, using pipeline {name}"

## CloudVoyager Reference

| Area | Path |
|------|------|
| Version router | `src/version-router.js` |
| SQ 9.9 pipeline | `src/pipelines/sq-9.9/` |
| SQ 10.0 pipeline | `src/pipelines/sq-10.0/` |
| SQ 10.4 pipeline | `src/pipelines/sq-10.4/` |
| SQ 2025 pipeline | `src/pipelines/sq-2025/` |
| Pipeline shared utilities | `src/pipelines/shared/` |
| Issue status constants | `src/shared/constants/issue-statuses.js` |

## Known Limitations

- The SQ 2025.1+ pipeline assumes the V2 groups API follows the documented pagination model. If SonarSource changes the V2 response format in a point release, the fallback to the standard API will activate, but the V2 parser may need updating.
- IN_SANDBOX status (SQ 2025.1+) has no SonarQube Cloud equivalent. Issues with this status are logged as skipped. If SonarQube Cloud later adds sandbox support, a mapping can be added without changing other pipelines.
- Version string parsing assumes `major.minor.patch.build` format. Non-standard version strings from development builds (e.g., `10.5-SNAPSHOT`) need explicit handling in the parser.
- The four-pipeline model means some code is duplicated across pipelines (e.g., hotspot extraction is identical in 10.0, 10.4, and 2025). This is intentional: duplication is preferred over shared mutable state that creates coupling between pipelines. Common helpers can be extracted to a `pipeline/shared/` package for pure utility functions.

## Dependency Notes

> **Bidirectional dependency with SPEC-012:** SPEC-011 and SPEC-012 have a bidirectional dependency: pipelines need Clean Code enrichment (SPEC-012), and enrichment is version-specific (SPEC-011). Implementations should be aware that changes to either spec may impact the other.

## Sonar Documentation Reference

For full Sonar product documentation, see: https://docs.sonarsource.com/llms.txt

## Open Questions

- Q1: Should the pipeline interface include `ExtractRules()` as a method, or should rule extraction remain a standalone operation outside the pipeline? CloudVoyager handles rules outside pipelines in the shared utils.
- Q2: For SQ 2025.1+ with the new versioning scheme (year.release), should the tool support pre-release versions like `2025.1-RC1`? CloudVoyager does not handle these.
- Q3: Should the pipeline carry a reference to the `sqapi.Client`, or should the client be passed as a parameter to each method? Carrying it as a struct field is simpler; passing it as a parameter is more testable.
- Q4: Should there be a `NullPipeline` or `DryRunPipeline` implementation for testing the migration orchestrator without real API calls?
